package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gitlab.com/brenden.vaughan/claude-context-go/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertError is a sentinel embedding error used by failing fakes.
var assertError = errors.New("simulated embed failure")

// memStore is an in-memory Store recording file state and chunk counts.
type memStore struct {
	mu       sync.Mutex
	nextID   int64
	byPath   map[string]*fileRec
	upserts  int
	replaces int
}

type fileRec struct {
	id     int64
	hash   string
	chunks int
}

func newMemStore() *memStore {
	return &memStore{byPath: map[string]*fileRec{}}
}

func (m *memStore) UpsertFile(_ context.Context, f core.File) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts++
	if rec, ok := m.byPath[f.Path]; ok {
		// Change is relative to the last committed hash; UpsertFile never
		// advances it (that is ReplaceChunks' job).
		return rec.id, rec.hash != f.ContentHash, nil
	}
	m.nextID++
	m.byPath[f.Path] = &fileRec{id: m.nextID, hash: ""}
	return m.nextID, true, nil
}

func (m *memStore) ReplaceChunks(_ context.Context, fileID int64, meta core.File, chunks []core.Chunk, _ [][]float32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replaces++
	for _, rec := range m.byPath {
		if rec.id == fileID {
			rec.hash = meta.ContentHash // commit the hash
			rec.chunks = len(chunks)
		}
	}
	return nil
}

func (m *memStore) DeleteFile(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byPath, path)
	return nil
}

func (m *memStore) ListPaths(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.byPath))
	for p := range m.byPath {
		out = append(out, p)
	}
	return out, nil
}

// oneChunkChunker emits exactly one chunk per non-empty file.
type oneChunkChunker struct{}

func (oneChunkChunker) Chunk(_ context.Context, _ string, src []byte) ([]core.Chunk, error) {
	if len(src) == 0 {
		return nil, nil
	}
	return []core.Chunk{{StartLine: 1, EndLine: 1, Content: string(src)}}, nil
}

// countingEmbedder returns a fixed-dim vector per input and counts inputs.
type countingEmbedder struct {
	mu    sync.Mutex
	count int
}

func (e *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.count += len(texts)
	e.mu.Unlock()
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func newIndexer(t *testing.T) (*Indexer, *memStore, *countingEmbedder) {
	t.Helper()
	store := newMemStore()
	emb := &countingEmbedder{}
	ix, err := New(store, oneChunkChunker{}, emb)
	require.NoError(t, err)
	return ix, store, emb
}

func TestNewValidation(t *testing.T) {
	store := newMemStore()
	_, err := New(nil, oneChunkChunker{}, &countingEmbedder{})
	require.Error(t, err)
	_, err = New(store, nil, &countingEmbedder{})
	require.Error(t, err)
	_, err = New(store, oneChunkChunker{}, nil)
	require.Error(t, err)
}

func TestIndexSelectsAndIgnores(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "app.py", "print(1)")
	writeFile(t, root, "notes.txt", "hello")
	writeFile(t, root, "image.png", "binarydata")                  // extension not allowed
	writeFile(t, root, "node_modules/dep.js", "module.exports={}") // ignored dir
	writeFile(t, root, ".git/config", "gitstuff")                  // dot dir

	ix, store, _ := newIndexer(t)
	require.NoError(t, ix.Index(context.Background(), root))

	paths, err := store.ListPaths(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"main.go", "app.py", "notes.txt"}, paths)

	status := ix.Status()
	assert.Equal(t, PhaseDone, status.Phase)
	assert.Equal(t, 3, status.Total)
	assert.Equal(t, 3, status.Done)
	assert.Empty(t, status.Errors)
}

func TestIndexIncrementalReembedsOnlyChanged(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a")
	writeFile(t, root, "b.go", "package b")

	ix, store, emb := newIndexer(t)
	ctx := context.Background()
	require.NoError(t, ix.Index(ctx, root))
	assert.Equal(t, 2, emb.count, "first run embeds both files")
	assert.Equal(t, 2, store.replaces)

	// Second run with no changes: nothing re-embedded.
	before := emb.count
	require.NoError(t, ix.Index(ctx, root))
	assert.Equal(t, before, emb.count, "unchanged files are not re-embedded")

	// Change one file: only it re-embeds.
	writeFile(t, root, "a.go", "package a // edited")
	require.NoError(t, ix.Index(ctx, root))
	assert.Equal(t, before+1, emb.count, "only the changed file re-embeds")
}

func TestIndexPrunesDeletedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "keep.go", "package keep")
	writeFile(t, root, "gone.go", "package gone")

	ix, store, _ := newIndexer(t)
	ctx := context.Background()
	require.NoError(t, ix.Index(ctx, root))

	require.NoError(t, os.Remove(filepath.Join(root, "gone.go")))
	require.NoError(t, ix.Index(ctx, root))

	paths, err := store.ListPaths(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"keep.go"}, paths)
}

func TestIndexEmptyFileSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "empty.go", "")
	writeFile(t, root, "real.go", "package real")

	ix, store, _ := newIndexer(t)
	require.NoError(t, ix.Index(context.Background(), root))

	paths, err := store.ListPaths(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"real.go"}, paths, "zero-byte files are skipped by the walker")
}

func TestIndexBatchesEmbeddings(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "big.go", "package big")

	store := newMemStore()
	emb := &countingEmbedder{}
	// A chunker emitting many chunks exercises batch boundaries.
	ix, err := New(store, manyChunkChunker{n: 100}, emb, WithBatchSize(32))
	require.NoError(t, err)

	require.NoError(t, ix.Index(context.Background(), root))
	assert.Equal(t, 100, emb.count, "every chunk is embedded exactly once across batches")
}

func TestIndexRootErrors(t *testing.T) {
	ix, _, _ := newIndexer(t)
	err := ix.Index(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	require.Error(t, err)
}

func TestIndexRejectsNonDirRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f.go")
	require.NoError(t, os.WriteFile(file, []byte("package f"), 0o644))

	ix, _, _ := newIndexer(t)
	err := ix.Index(context.Background(), file)
	require.Error(t, err)
}

// failOnceEmbedder returns an error on its first call, then succeeds. It models
// a transient embedding failure (e.g. an oversized chunk) to prove the file is
// retried rather than silently skipped.
type failOnceEmbedder struct {
	mu     sync.Mutex
	failed bool
}

func (e *failOnceEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.failed {
		e.failed = true
		return nil, assertError
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func TestIndexRetriesAfterEmbedFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a")

	store := newMemStore()
	ix, err := New(store, oneChunkChunker{}, &failOnceEmbedder{})
	require.NoError(t, err)
	ctx := context.Background()

	// First run: embedding fails, so the file is recorded but not committed.
	require.NoError(t, ix.Index(ctx, root))
	require.NotEmpty(t, ix.Status().Errors, "the embed failure is recorded")
	store.mu.Lock()
	rec := store.byPath["a.go"]
	committed := rec != nil && rec.hash != ""
	store.mu.Unlock()
	assert.False(t, committed, "a failed file must not commit its hash")

	// Second run: nothing changed on disk, but the file is still 'changed'
	// because its hash was never committed, so it retries and succeeds.
	require.NoError(t, ix.Index(ctx, root))
	assert.Empty(t, ix.Status().Errors, "retry succeeds with no errors")
	store.mu.Lock()
	rec = store.byPath["a.go"]
	store.mu.Unlock()
	require.NotNil(t, rec)
	assert.NotEmpty(t, rec.hash, "hash is committed after a successful retry")
	assert.Equal(t, 1, rec.chunks)
}

// manyChunkChunker emits n chunks per file to test embedding batching.
type manyChunkChunker struct{ n int }

func (c manyChunkChunker) Chunk(_ context.Context, _ string, _ []byte) ([]core.Chunk, error) {
	out := make([]core.Chunk, c.n)
	for i := range out {
		out[i] = core.Chunk{StartLine: i + 1, EndLine: i + 1, Content: "line"}
	}
	return out, nil
}
