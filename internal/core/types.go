// Package core holds the domain types shared across the indexing, storage,
// embedding, and search packages. Keeping them here avoids import cycles
// between the producers (chunker, embedder) and consumers (store, search).
package core

// File is a source file tracked in an index. Path is always relative to the
// indexed codebase root so that an index remains valid if the root moves.
type File struct {
	ID          int64
	Path        string
	ModTimeUnix int64
	Size        int64
	ContentHash string
	ChunkCount  int
}

// Chunk is a contiguous, searchable span of a source file produced by a
// Chunker. StartLine and EndLine are 1-based and inclusive. Symbol is the
// enclosing declaration name when the chunker can determine one (e.g. a
// function or type), otherwise empty.
type Chunk struct {
	StartLine int
	EndLine   int
	Symbol    string
	Content   string
}

// Hit is a single search result. Score is comparable only within the result
// set that produced it; different rankers use different scales.
type Hit struct {
	ChunkID   int64
	Path      string
	StartLine int
	EndLine   int
	Symbol    string
	Content   string
	Score     float64
}

// EmbRow is a chunk id paired with its embedding — the minimal row dense
// search needs to score a query against every stored chunk. Full rows for
// the top-scoring ids are hydrated separately, so brute-force scoring never
// materializes every chunk's text.
type EmbRow struct {
	ChunkID int64
	Vector  []float32
}
