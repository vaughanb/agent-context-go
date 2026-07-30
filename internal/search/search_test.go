package search

import (
	"context"
	"testing"

	"gitlab.com/brenden.vaughan/claude-context-go/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore serves canned vectors and lexical hits.
type fakeStore struct {
	vectors []core.VecRow
	lexical []core.Hit
}

func (f *fakeStore) AllVectors(context.Context) ([]core.VecRow, error) {
	return f.vectors, nil
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
	store := &fakeStore{
		vectors: []core.VecRow{
			{ChunkID: 1, Path: "a.go", Vector: []float32{1, 0}},
			{ChunkID: 2, Path: "b.go", Vector: []float32{0, 1}},
			{ChunkID: 3, Path: "c.go", Vector: []float32{-1, 0}},
		},
	}
	s, err := New(store, &fakeEmbedder{vec: []float32{1, 0}})
	require.NoError(t, err)

	hits, err := s.Search(context.Background(), "query", 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, hitIDs(hits))
}

func TestSearchSkipsDimensionMismatch(t *testing.T) {
	store := &fakeStore{
		vectors: []core.VecRow{
			{ChunkID: 1, Path: "a.go", Vector: []float32{1, 0}},
			{ChunkID: 2, Path: "b.go", Vector: []float32{1, 0, 0}}, // wrong dim
		},
	}
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
		vectors: []core.VecRow{
			{ChunkID: 1, Path: "a.go", Vector: []float32{1, 0}},
			{ChunkID: 2, Path: "b.go", Vector: []float32{0.2, 1}},
		},
		lexical: []core.Hit{
			{ChunkID: 2, Path: "b.go", Score: 5},
			{ChunkID: 99, Path: "z.go", Score: 1},
		},
	}
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
	store := &fakeStore{
		vectors: []core.VecRow{
			{ChunkID: 1, Vector: []float32{1, 0}},
			{ChunkID: 2, Vector: []float32{0.9, 0.1}},
			{ChunkID: 3, Vector: []float32{0.8, 0.2}},
		},
	}
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
