// Package search implements hybrid code search over a single index: dense
// (semantic) retrieval via brute-force cosine similarity over stored
// embeddings, lexical retrieval via SQLite FTS5 BM25, and Reciprocal Rank
// Fusion (RRF) to combine the two rankings into a single result list.
package search

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/vaughanb/agent-context-go/internal/core"
)

// Store supplies the two retrieval channels search fuses. It is a narrow view
// of the full index store (interface segregation): only what search reads.
type Store interface {
	// AllEmbeddings returns every chunk id with its embedding, for brute-force
	// cosine scoring. Implementations may cache and share the result across
	// calls; callers must treat it as read-only.
	AllEmbeddings(ctx context.Context) ([]core.EmbRow, error)
	// ChunksByIDs hydrates the full stored rows for the given chunk ids, keyed
	// by id; unknown ids are absent from the result.
	ChunksByIDs(ctx context.Context, ids []int64) (map[int64]core.Hit, error)
	// LexicalSearch returns up to k BM25-ranked hits for query, best-first.
	LexicalSearch(ctx context.Context, query string, k int) ([]core.Hit, error)
}

// Embedder embeds the query text so it can be compared against stored chunk
// vectors. It is the query-side subset of embed.Embedder.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
}

// Default tuning. denseK/lexicalK bound how many candidates each channel
// contributes before fusion; rrfK is the RRF damping constant (larger flattens
// the contribution of top ranks), 60 per the original RRF paper.
const (
	defaultDenseK   = 50
	defaultLexicalK = 50
	defaultRRFK     = 60.0
)

// Service runs hybrid search against one index. Construct it with New.
type Service struct {
	store    Store
	embedder Embedder
	denseK   int
	lexicalK int
	rrfK     float64
}

// Option configures a Service.
type Option func(*Service)

// WithDenseK sets how many dense (cosine) candidates feed into fusion.
func WithDenseK(k int) Option { return func(s *Service) { s.denseK = k } }

// WithLexicalK sets how many lexical (BM25) candidates feed into fusion.
func WithLexicalK(k int) Option { return func(s *Service) { s.lexicalK = k } }

// WithRRFK sets the RRF damping constant.
func WithRRFK(k float64) Option { return func(s *Service) { s.rrfK = k } }

// New returns a Service that searches store, embedding queries with embedder.
// It returns an error if either dependency is nil.
func New(store Store, embedder Embedder, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("search: store must not be nil")
	}
	if embedder == nil {
		return nil, fmt.Errorf("search: embedder must not be nil")
	}
	s := &Service{
		store:    store,
		embedder: embedder,
		denseK:   defaultDenseK,
		lexicalK: defaultLexicalK,
		rrfK:     defaultRRFK,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.rrfK <= 0 {
		s.rrfK = defaultRRFK
	}
	return s, nil
}

// Search returns up to topN results for query, fusing dense and lexical
// rankings with RRF. An empty query or topN <= 0 yields no results. The
// returned hits carry the fused RRF score in Score, ordered best-first.
func (s *Service) Search(ctx context.Context, query string, topN int) ([]core.Hit, error) {
	if query == "" || topN <= 0 {
		return nil, nil
	}

	dense, err := s.denseHits(ctx, query)
	if err != nil {
		return nil, err
	}
	lexical, err := s.store.LexicalSearch(ctx, query, s.lexicalK)
	if err != nil {
		return nil, fmt.Errorf("search: lexical retrieval: %w", err)
	}

	fused := s.fuse(dense, lexical)
	if len(fused) > topN {
		fused = fused[:topN]
	}
	return fused, nil
}

// denseHits embeds the query, scores it against every cached chunk embedding
// by cosine similarity, then hydrates full rows for only the top denseK ids —
// so the per-query cost is one embedding, one pass over the vectors, and a
// handful of row fetches, never a scan of every chunk's text.
func (s *Service) denseHits(ctx context.Context, query string) ([]core.Hit, error) {
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("search: embed query: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("search: embedder returned no query vector")
	}
	q := vecs[0]

	rows, err := s.store.AllEmbeddings(ctx)
	if err != nil {
		return nil, fmt.Errorf("search: load embeddings: %w", err)
	}

	type scored struct {
		id    int64
		score float64
	}
	best := make([]scored, 0, len(rows))
	for _, r := range rows {
		if len(r.Vector) != len(q) {
			// Dimension mismatch means the stored vector came from a different
			// model; skip rather than compare incomparable spaces.
			continue
		}
		best = append(best, scored{id: r.ChunkID, score: cosine(q, r.Vector)})
	}
	sort.SliceStable(best, func(i, j int) bool {
		if best[i].score != best[j].score {
			return best[i].score > best[j].score
		}
		return best[i].id < best[j].id
	})
	if len(best) > s.denseK {
		best = best[:s.denseK]
	}
	if len(best) == 0 {
		return nil, nil
	}

	ids := make([]int64, len(best))
	for i, b := range best {
		ids[i] = b.id
	}
	byID, err := s.store.ChunksByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("search: hydrate top chunks: %w", err)
	}

	hits := make([]core.Hit, 0, len(best))
	for _, b := range best {
		h, ok := byID[b.id]
		if !ok {
			// The chunk was deleted between scoring and hydration; skip it.
			continue
		}
		h.Score = b.score
		hits = append(hits, h)
	}
	return hits, nil
}

// fuse combines two best-first ranked lists with Reciprocal Rank Fusion. Each
// list contributes 1/(rrfK + rank) per hit (rank is 1-based); scores for the
// same chunk sum across lists. The result is sorted by fused score, best-first.
func (s *Service) fuse(dense, lexical []core.Hit) []core.Hit {
	type agg struct {
		hit   core.Hit
		score float64
	}
	byID := make(map[int64]*agg)

	accumulate := func(list []core.Hit) {
		for rank, h := range list {
			contribution := 1.0 / (s.rrfK + float64(rank+1))
			if a, ok := byID[h.ChunkID]; ok {
				a.score += contribution
			} else {
				byID[h.ChunkID] = &agg{hit: h, score: contribution}
			}
		}
	}
	accumulate(dense)
	accumulate(lexical)

	out := make([]core.Hit, 0, len(byID))
	for _, a := range byID {
		h := a.hit
		h.Score = a.score
		out = append(out, h)
	}
	sortHitsDesc(out)
	return out
}

// sortHitsDesc orders hits by descending Score, breaking ties by ChunkID so the
// ordering is deterministic regardless of input order or map iteration.
func sortHitsDesc(hits []core.Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ChunkID < hits[j].ChunkID
	})
}

// cosine returns the cosine similarity of two equal-length vectors, in [-1, 1].
// It normalizes explicitly so it is correct even when inputs are not unit
// length (e.g. some Ollama models). Returns 0 if either vector has zero norm.
func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
