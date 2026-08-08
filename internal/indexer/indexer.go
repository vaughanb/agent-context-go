// Package indexer builds and incrementally maintains a codebase's search
// index. It walks a root directory, filters to indexable files, and for each
// changed file re-chunks, re-embeds, and persists the result. Unchanged files
// are skipped by content hash; files deleted from disk are pruned from the
// index. Progress is tracked in-memory so a long index can be polled.
//
// The indexer depends only on small interfaces (Chunker, Embedder, Store) so
// the concrete tree-sitter chunker (which needs CGO) is wired in by the caller
// and never imported here.
package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"

	"github.com/vaughanb/agent-context-go/internal/core"
)

// Chunker splits a file's bytes into searchable chunks. It mirrors
// chunker.Chunker; declaring it here keeps this package free of the CGO
// tree-sitter dependency.
type Chunker interface {
	Chunk(ctx context.Context, path string, src []byte) ([]core.Chunk, error)
}

// Embedder turns chunk text into vectors. It is the write-side subset of
// embed.Embedder.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Store persists file metadata and chunk embeddings. It is the write-side
// subset of the index store.
type Store interface {
	UpsertFile(ctx context.Context, f core.File) (id int64, changed bool, err error)
	ReplaceChunks(ctx context.Context, fileID int64, meta core.File, chunks []core.Chunk, vecs [][]float32) error
	DeleteFile(ctx context.Context, path string) error
	ListPaths(ctx context.Context) ([]string, error)
}

// Default tuning.
const (
	defaultMaxFileSize = 1 << 20 // 1 MiB: skip larger files (minified bundles, data)
	defaultBatchSize   = 32      // chunks embedded per Embed call
)

// Indexer maintains one codebase's index. It is safe for concurrent Status
// calls during an Index run, but a single Indexer runs at most one Index at a
// time (a second concurrent call returns ErrAlreadyRunning). Construct it with
// New.
type Indexer struct {
	store    Store
	chunker  Chunker
	embedder Embedder

	allowExts   map[string]bool
	ignoreDirs  map[string]bool
	maxFileSize int64
	batchSize   int

	mu       sync.Mutex
	running  bool
	progress Progress
}

// New constructs an Indexer. store, chunker, and embedder must be non-nil.
func New(store Store, chunker Chunker, embedder Embedder, opts ...Option) (*Indexer, error) {
	if store == nil {
		return nil, fmt.Errorf("indexer: store must not be nil")
	}
	if chunker == nil {
		return nil, fmt.Errorf("indexer: chunker must not be nil")
	}
	if embedder == nil {
		return nil, fmt.Errorf("indexer: embedder must not be nil")
	}
	ix := &Indexer{
		store:       store,
		chunker:     chunker,
		embedder:    embedder,
		allowExts:   defaultAllowExts(),
		ignoreDirs:  defaultIgnoreDirs(),
		maxFileSize: defaultMaxFileSize,
		batchSize:   defaultBatchSize,
	}
	for _, opt := range opts {
		opt(ix)
	}
	return ix, nil
}

// Index synchronously (re)indexes the codebase rooted at absRoot. It is
// incremental: only new or content-changed files are re-embedded, and files
// removed from disk are deleted from the index. Per-file failures are recorded
// in the progress and do not abort the run; the returned error is non-nil only
// for a failure that prevents indexing entirely (e.g. the root is unreadable).
func (ix *Indexer) Index(ctx context.Context, absRoot string) error {
	if err := ix.begin(); err != nil {
		return err
	}
	defer ix.finish()
	return ix.run(ctx, absRoot)
}

// Start launches an index run for absRoot in a background goroutine and returns
// immediately. It claims the single-run lock synchronously, so Running reports
// true (and Status reflects the new run) before Start returns — there is no
// window in which a poller sees the previous run's state. It returns
// ErrAlreadyRunning if a run is already in progress. Any run error is recorded
// in the progress (surfaced by Status), not returned.
func (ix *Indexer) Start(ctx context.Context, absRoot string) error {
	if err := ix.begin(); err != nil {
		return err
	}
	go func() {
		defer ix.finish()
		_ = ix.run(ctx, absRoot)
	}()
	return nil
}

// run performs the index. Callers must hold the run lock (via begin) and
// release it (via finish) around this call.
func (ix *Indexer) run(ctx context.Context, absRoot string) error {
	info, err := os.Stat(absRoot)
	if err != nil {
		ix.fail(fmt.Sprintf("stat root %q: %v", absRoot, err))
		return fmt.Errorf("indexer: stat root %q: %w", absRoot, err)
	}
	if !info.IsDir() {
		ix.fail(fmt.Sprintf("root %q is not a directory", absRoot))
		return fmt.Errorf("indexer: root %q is not a directory", absRoot)
	}

	ix.setPhase(PhaseWalking)
	files, err := ix.walk(absRoot)
	if err != nil {
		ix.fail(fmt.Sprintf("walk %q: %v", absRoot, err))
		return fmt.Errorf("indexer: walk %q: %w", absRoot, err)
	}

	ix.setPhase(PhaseIndexing)
	ix.setTotal(len(files))

	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			ix.fail(fmt.Sprintf("cancelled: %v", err))
			return fmt.Errorf("indexer: %w", err)
		}
		seen[f.rel] = true
		if err := ix.indexFile(ctx, f); err != nil {
			ix.addError(fmt.Sprintf("%s: %v", f.rel, err))
		}
		ix.incDone()
	}

	ix.setPhase(PhasePruning)
	if err := ix.prune(ctx, seen); err != nil {
		ix.addError(fmt.Sprintf("prune: %v", err))
	}

	ix.setPhase(PhaseDone)
	return nil
}

// indexFile reads, hashes, and — only if changed — re-chunks and re-embeds a
// single file, replacing its stored chunks.
func (ix *Indexer) indexFile(ctx context.Context, f candidate) error {
	src, err := os.ReadFile(f.abs)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	meta := core.File{
		Path:        f.rel,
		ModTimeUnix: f.modTimeUnix,
		Size:        f.size,
		ContentHash: hashBytes(src),
	}

	id, changed, err := ix.store.UpsertFile(ctx, meta)
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	if !changed {
		return nil
	}

	chunks, err := ix.chunker.Chunk(ctx, f.rel, src)
	if err != nil {
		return fmt.Errorf("chunk: %w", err)
	}

	var vecs [][]float32
	if len(chunks) > 0 {
		vecs, err = ix.embedChunks(ctx, chunks)
		if err != nil {
			return fmt.Errorf("embed: %w", err)
		}
	}

	// ReplaceChunks commits the content hash only now that chunks are ready,
	// so a failure above leaves the file marked changed for the next run. A
	// file yielding no chunks still commits (empty), so it is not reprocessed.
	if err := ix.store.ReplaceChunks(ctx, id, meta, chunks, vecs); err != nil {
		return fmt.Errorf("store chunks: %w", err)
	}
	return nil
}

// embedChunks embeds every chunk's content in batches, preserving order.
func (ix *Indexer) embedChunks(ctx context.Context, chunks []core.Chunk) ([][]float32, error) {
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}

	vecs := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += ix.batchSize {
		end := min(start+ix.batchSize, len(texts))
		batch, err := ix.embedder.Embed(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf("embedder returned %d vectors for %d inputs", len(batch), end-start)
		}
		vecs = append(vecs, batch...)
	}
	return vecs, nil
}

// prune deletes index entries for files that are no longer present on disk.
func (ix *Indexer) prune(ctx context.Context, seen map[string]bool) error {
	paths, err := ix.store.ListPaths(ctx)
	if err != nil {
		return fmt.Errorf("list indexed paths: %w", err)
	}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		if err := ix.store.DeleteFile(ctx, p); err != nil {
			return fmt.Errorf("delete %q: %w", p, err)
		}
	}
	return nil
}

// begin claims the single-run lock, returning ErrAlreadyRunning if an index is
// already in progress. It resets progress for the new run.
func (ix *Indexer) begin() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.running {
		return ErrAlreadyRunning
	}
	ix.running = true
	ix.progress = Progress{Phase: PhaseWalking}
	return nil
}

func (ix *Indexer) finish() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.running = false
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
