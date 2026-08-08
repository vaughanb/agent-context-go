package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vaughanb/agent-context-go/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := New(context.Background(), path, "test-model", 3)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func vec(f ...float32) []float32 { return f }

func TestNewValidation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	testCases := map[string]struct {
		model string
		dim   int
	}{
		"empty model":  {model: "", dim: 3},
		"zero dim":     {model: "m", dim: 0},
		"negative dim": {model: "m", dim: -1},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := New(ctx, filepath.Join(dir, name+".db"), tc.model, tc.dim)
			require.Error(t, err)
		})
	}
}

func TestNewModelMismatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")

	s, err := New(ctx, path, "model-a", 3)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	testCases := map[string]struct {
		model string
		dim   int
	}{
		"different model": {model: "model-b", dim: 3},
		"different dim":   {model: "model-a", dim: 4},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := New(ctx, path, tc.model, tc.dim)
			require.ErrorIs(t, err, ErrModelMismatch)
		})
	}

	reopened, err := New(ctx, path, "model-a", 3)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func TestUpsertFileChangeDetection(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	f := core.File{Path: "a.go", ModTimeUnix: 1, Size: 10, ContentHash: "h1"}

	id1, changed, err := s.UpsertFile(ctx, f)
	require.NoError(t, err)
	assert.True(t, changed, "new file should report changed")

	// Until ReplaceChunks commits the hash, the file is still considered
	// changed — an interrupted index must retry, not be skipped.
	_, changed, err = s.UpsertFile(ctx, f)
	require.NoError(t, err)
	assert.True(t, changed, "hash is not committed until ReplaceChunks")

	require.NoError(t, s.ReplaceChunks(ctx, id1, f, nil, nil))

	_, changed, err = s.UpsertFile(ctx, f)
	require.NoError(t, err)
	assert.False(t, changed, "committed identical hash reports unchanged")

	f.ContentHash = "h2"
	id2, changed, err := s.UpsertFile(ctx, f)
	require.NoError(t, err)
	assert.True(t, changed, "new hash should report changed")
	assert.Equal(t, id1, id2, "same path should keep the same row id")
}

func TestReplaceChunksCommitsHash(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	f := core.File{Path: "a.go", ModTimeUnix: 1, Size: 10, ContentHash: "h1"}
	id, _, err := s.UpsertFile(ctx, f)
	require.NoError(t, err)

	// Simulate an indexing failure: no ReplaceChunks call. The file remains
	// changed, so it is retried.
	_, changed, err := s.UpsertFile(ctx, f)
	require.NoError(t, err)
	require.True(t, changed)

	// Now commit; the file becomes up to date.
	require.NoError(t, s.ReplaceChunks(ctx, id, f,
		[]core.Chunk{{StartLine: 1, EndLine: 1, Content: "x"}}, [][]float32{vec(1, 0, 0)}))
	_, changed, err = s.UpsertFile(ctx, f)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestReplaceChunksAndVectors(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	meta := core.File{Path: "a.go", ContentHash: "h1"}
	id, _, err := s.UpsertFile(ctx, meta)
	require.NoError(t, err)

	chunks := []core.Chunk{
		{StartLine: 1, EndLine: 5, Symbol: "Foo", Content: "func Foo() {}"},
		{StartLine: 7, EndLine: 9, Symbol: "Bar", Content: "func Bar() {}"},
	}
	vecs := [][]float32{vec(1, 0, 0), vec(0, 1, 0)}
	require.NoError(t, s.ReplaceChunks(ctx, id, meta, chunks, vecs))

	rows, err := s.AllVectors(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "a.go", rows[0].Path)
	assert.Equal(t, vec(1, 0, 0), rows[0].Vector)

	// Replacing supersedes the prior chunks rather than appending.
	require.NoError(t, s.ReplaceChunks(ctx, id, meta,
		[]core.Chunk{{StartLine: 1, EndLine: 2, Content: "new"}},
		[][]float32{vec(0, 0, 1)}))
	rows, err = s.AllVectors(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "new", rows[0].Content)
}

func TestReplaceChunksValidation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	meta := core.File{Path: "a.go", ContentHash: "h1"}
	id, _, err := s.UpsertFile(ctx, meta)
	require.NoError(t, err)

	testCases := map[string]struct {
		chunks []core.Chunk
		vecs   [][]float32
	}{
		"length mismatch": {
			chunks: []core.Chunk{{Content: "x"}},
			vecs:   nil,
		},
		"wrong vector dim": {
			chunks: []core.Chunk{{Content: "x"}},
			vecs:   [][]float32{vec(1, 2)},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.ReplaceChunks(ctx, id, meta, tc.chunks, tc.vecs))
		})
	}
}

func TestDeleteFileCascades(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	meta := core.File{Path: "a.go", ContentHash: "h1"}
	id, _, err := s.UpsertFile(ctx, meta)
	require.NoError(t, err)
	require.NoError(t, s.ReplaceChunks(ctx, id, meta,
		[]core.Chunk{{StartLine: 1, EndLine: 1, Content: "func Foo"}},
		[][]float32{vec(1, 0, 0)}))

	require.NoError(t, s.DeleteFile(ctx, "a.go"))

	paths, err := s.ListPaths(ctx)
	require.NoError(t, err)
	assert.Empty(t, paths)

	rows, err := s.AllVectors(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "chunks should be removed by cascade")

	hits, err := s.LexicalSearch(ctx, "Foo", 10)
	require.NoError(t, err)
	assert.Empty(t, hits, "FTS entries should be removed by delete trigger")
}

func TestLexicalSearch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	meta := core.File{Path: "a.go", ContentHash: "h1"}
	id, _, err := s.UpsertFile(ctx, meta)
	require.NoError(t, err)
	require.NoError(t, s.ReplaceChunks(ctx, id, meta, []core.Chunk{
		{StartLine: 1, EndLine: 3, Symbol: "ParseConfig", Content: "func ParseConfig() error { return nil }"},
		{StartLine: 5, EndLine: 7, Symbol: "Serve", Content: "func Serve() { startHTTPServer() }"},
	}, [][]float32{vec(1, 0, 0), vec(0, 1, 0)}))

	hits, err := s.LexicalSearch(ctx, "ParseConfig", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "ParseConfig", hits[0].Symbol)

	// Punctuation and FTS operators must not break the query.
	hits, err = s.LexicalSearch(ctx, "ParseConfig() error;", 10)
	require.NoError(t, err)
	assert.NotEmpty(t, hits)

	hits, err = s.LexicalSearch(ctx, "   ", 10)
	require.NoError(t, err)
	assert.Empty(t, hits, "blank query yields no hits")
}

func TestClearKeepsMeta(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := New(ctx, path, "model-a", 3)
	require.NoError(t, err)

	meta := core.File{Path: "a.go", ContentHash: "h1"}
	id, _, err := s.UpsertFile(ctx, meta)
	require.NoError(t, err)
	require.NoError(t, s.ReplaceChunks(ctx, id, meta,
		[]core.Chunk{{StartLine: 1, EndLine: 1, Content: "x"}},
		[][]float32{vec(1, 0, 0)}))

	require.NoError(t, s.Clear(ctx))
	paths, err := s.ListPaths(ctx)
	require.NoError(t, err)
	assert.Empty(t, paths)
	require.NoError(t, s.Close())

	// Meta survived, so reopening with the same model succeeds.
	reopened, err := New(ctx, path, "model-a", 3)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func TestVecRoundTrip(t *testing.T) {
	in := vec(1.5, -2.25, 0, 3.75)
	out, err := decodeVec(encodeVec(in))
	require.NoError(t, err)
	assert.Equal(t, in, out)

	_, err = decodeVec([]byte{1, 2, 3})
	require.Error(t, err, "non-multiple-of-4 blob should error")
}
