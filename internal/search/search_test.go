package search

import (
	"context"
	"testing"

	"github.com/vaughanb/agent-context-go/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore serves canned embeddings, hydration rows, and lexical hits. It
// records the ids passed to ChunksByIDs so tests can assert that only the top
// candidates are hydrated.
type fakeStore struct {
	embeddings  []core.EmbRow
	rows        map[int64]core.Hit
	lexical     []core.Hit
	hydratedIDs []int64
}

// addChunk registers a chunk with both an embedding and a hydratable row.
func (f *fakeStore) addChunk(id int64, path string, vector []float32) {
	f.embeddings = append(f.embeddings, core.EmbRow{ChunkID: id, Vector: vector})
	if f.rows == nil {
		f.rows = map[int64]core.Hit{}
	}
	f.rows[id] = core.Hit{ChunkID: id, Path: path}
}

func (f *fakeStore) AllEmbeddings(context.Context) ([]core.EmbRow, error) {
	return f.embeddings, nil
}

func (f *fakeStore) ChunksByIDs(_ context.Context, ids []int64) (map[int64]core.Hit, error) {
	f.hydratedIDs = ids
	out := make(map[int64]core.Hit, len(ids))
	for _, id := range ids {
		if h, ok := f.rows[id]; ok {
			out[id] = h
		}
	}
	return out, nil
}

func (f *fakeStore) LexicalSearch(_ context.Context, _ string, k int) ([]core.Hit, error) {
	if k < len(f.lexical) {
		return f.lexical[:k], nil
	}
	return f.lexical, nil
}

// fakeEmbedder returns a fixed query vector regardless of input text.
type fakeEmbedder struct {
	vec []float32
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

func (f *fakeEmbedder) Dim() int { return len(f.vec) }

func hitIDs(hits []core.Hit) []int64 {
	out := make([]int64, len(hits))
	for i, h := range hits {
		out[i] = h.ChunkID
	}
	return out
}

func TestNewValidation(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{1, 0}}
	store := &fakeStore{}

	_, err := New(nil, emb)
	require.Error(t, err)

	_, err = New(store, nil)
	require.Error(t, err)

	s, err := New(store, emb)
	require.NoError(t, err)
	require.NotNil(t, s)
}

func TestSearchEmptyInputs(t *testing.T) {
	s, err := New(&fakeStore{}, &fakeEmbedder{vec: []float32{1, 0}})
	require.NoError(t, err)

	testCases := map[string]struct {
		query string
		topN  int
	}{
		"empty query":   {query: "", topN: 5},
		"zero topN":     {query: "foo", topN: 0},
		"negative topN": {query: "foo", topN: -1},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			hits, err := s.Search(context.Background(), tc.query, tc.topN)
			require.NoError(t, err)
			assert.Empty(t, hits)
		})
	}
}

func TestSearchDenseRanksByCosine(t *testing.T) {
	// Query points along +x. Chunk 1 aligns with it, chunk 2 is orthogonal,
	// chunk 3 points away. Dense ranking must be 1, 2, 3.
	store := &fakeStore{}
	store.addChunk(1, "a.go", []float32{1, 0})
	store.addChunk(2, "b.go", []float32{0, 1})
	store.addChunk(3, "c.go", []float32{-1, 0})
	s, err := New(store, &fakeEmbedder{vec: []float32{1, 0}})
	require.NoError(t, err)

	hits, err := s.Search(context.Background(), "query", 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, hitIDs(hits))
	assert.Equal(t, "a.go", hits[0].Path, "hits must be hydrated with stored rows")
}

func TestSearchSkipsDimensionMismatch(t *testing.T) {
	store := &fakeStore{}
	store.addChunk(1, "a.go", []float32{1, 0})
	store.addChunk(2, "b.go", []float32{1, 0, 0}) // wrong dim
	s, err := New(store, &fakeEmbedder{vec: []float32{1, 0}})
	require.NoError(t, err)

	hits, err := s.Search(context.Background(), "query", 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, hitIDs(hits))
}

func TestSearchHydratesOnlyTopDenseK(t *testing.T) {
	store := &fakeStore{}
	store.addChunk(1, "a.go", []float32{1, 0})
	store.addChunk(2, "b.go", []float32{0.9, 0.1})
	store.addChunk(3, "c.go", []float32{0.8, 0.2})
	s, err := New(store, &fakeEmbedder{vec: []float32{1, 0}}, WithDenseK(2))
	require.NoError(t, err)

	hits, err := s.Search(context.Background(), "query", 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, hitIDs(hits))
	assert.Len(t, store.hydratedIDs, 2, "only the top denseK candidates are hydrated")
}

func TestSearchDropsChunksDeletedDuringHydration(t *testing.T) {
	store := &fakeStore{}
	store.addChunk(1, "a.go", []float32{1, 0})
	store.addChunk(2, "b.go", []float32{0.9, 0.1})
	delete(store.rows, 2) // deleted between scoring and hydration
	s, err := New(store, &fakeEmbedder{vec: []float32{1, 0}})
	require.NoError(t, err)

	hits, err := s.Search(context.Background(), "query", 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, hitIDs(hits))
}

func TestSearchFusesDenseAndLexical(t *testing.T) {
	// Chunk 2 is mediocre in dense (rank 2) but top in lexical (rank 1); the
	// fusion should surface it. Chunk 99 appears only in lexical and must be
	// included even with no dense vector.
	store := &fakeStore{
		lexical: []core.Hit{
			{ChunkID: 2, Path: "b.go", Score: 5},
			{ChunkID: 99, Path: "z.go", Score: 1},
		},
	}
	store.addChunk(1, "a.go", []float32{1, 0})
	store.addChunk(2, "b.go", []float32{0.2, 1})
	s, err := New(store, &fakeEmbedder{vec: []float32{1, 0}})
	require.NoError(t, err)

	hits, err := s.Search(context.Background(), "query", 10)
	require.NoError(t, err)

	ids := hitIDs(hits)
	assert.Contains(t, ids, int64(99), "lexical-only hit must be fused in")
	// Chunk 2 (dense rank 2 + lexical rank 1) should outrank chunk 1
	// (dense rank 1 only).
	assert.Equal(t, int64(2), ids[0])
	// Fused scores are strictly descending.
	for i := 1; i < len(hits); i++ {
		assert.LessOrEqual(t, hits[i].Score, hits[i-1].Score)
	}
}

func TestSearchRespectsTopN(t *testing.T) {
	store := &fakeStore{}
	store.addChunk(1, "a.go", []float32{1, 0})
	store.addChunk(2, "b.go", []float32{0.9, 0.1})
	store.addChunk(3, "c.go", []float32{0.8, 0.2})
	s, err := New(store, &fakeEmbedder{vec: []float32{1, 0}})
	require.NoError(t, err)

	hits, err := s.Search(context.Background(), "query", 2)
	require.NoError(t, err)
	assert.Len(t, hits, 2)
}

func TestFuseRRFScore(t *testing.T) {
	s := &Service{rrfK: 60}
	dense := []core.Hit{{ChunkID: 1}, {ChunkID: 2}}
	lexical := []core.Hit{{ChunkID: 2}, {ChunkID: 3}}

	out := s.fuse(dense, lexical)

	scores := map[int64]float64{}
	for _, h := range out {
		scores[h.ChunkID] = h.Score
	}
	// Chunk 1: dense rank 1 only. Chunk 2: dense rank 2 + lexical rank 1.
	// Chunk 3: lexical rank 2 only.
	assert.InDelta(t, 1.0/61, scores[1], 1e-9)
	assert.InDelta(t, 1.0/62+1.0/61, scores[2], 1e-9)
	assert.InDelta(t, 1.0/62, scores[3], 1e-9)
	assert.Equal(t, int64(2), out[0].ChunkID, "chunk in both lists ranks first")
}

func TestCosine(t *testing.T) {
	assert.InDelta(t, 1.0, cosine([]float32{1, 0}, []float32{2, 0}), 1e-9)
	assert.InDelta(t, 0.0, cosine([]float32{1, 0}, []float32{0, 3}), 1e-9)
	assert.InDelta(t, -1.0, cosine([]float32{1, 0}, []float32{-1, 0}), 1e-9)
	assert.Equal(t, 0.0, cosine([]float32{0, 0}, []float32{1, 1}), "zero norm yields 0")
}
