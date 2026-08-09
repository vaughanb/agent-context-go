package embed

import (
	"context"
	"fmt"

	"github.com/vaughanb/agent-context-go/internal/config"
)

// Pool is an Embedder backed by n independent embedder instances, allowing up
// to n Embed calls to run concurrently. Each underlying instance is only ever
// used by one goroutine at a time (a caller checks one out, uses it, returns
// it), so a single non-thread-safe model session stays safe while the pool as a
// whole scales embedding across workers. Even at size 1 the pool provides
// mutual exclusion, so a background index and a concurrent search never touch
// the same session simultaneously.
type Pool struct {
	free  chan Embedder
	all   []Embedder
	dim   int
	model string
}

// NewPool builds a pool of n embedders, each constructed from cfg via New. All
// instances share cfg's model and dimensionality. For the in-process ONNX
// provider each instance loads its own model session (memory scales with n); for
// Ollama each instance is a lightweight HTTP client. n is clamped to at least 1.
// On any construction failure, already-built instances are closed.
func NewPool(ctx context.Context, cfg *config.Config, n int) (*Pool, error) {
	if n < 1 {
		n = 1
	}
	all := make([]Embedder, 0, n)
	free := make(chan Embedder, n)
	for i := 0; i < n; i++ {
		e, err := New(ctx, cfg)
		if err != nil {
			for _, made := range all {
				_ = made.Close()
			}
			return nil, fmt.Errorf("embed: build pool instance %d/%d: %w", i+1, n, err)
		}
		all = append(all, e)
		free <- e
	}
	return &Pool{free: free, all: all, dim: all[0].Dim(), model: all[0].Model()}, nil
}

// Embed checks out a free instance (blocking until one is available or ctx is
// cancelled), embeds with it, and returns it to the pool.
func (p *Pool) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case e := <-p.free:
		defer func() { p.free <- e }()
		return e.Embed(ctx, texts)
	}
}

// Dim implements Embedder.
func (p *Pool) Dim() int { return p.dim }

// Model implements Embedder.
func (p *Pool) Model() string { return p.model }

// Size reports how many underlying instances back the pool.
func (p *Pool) Size() int { return len(p.all) }

// Close releases every underlying instance, returning the first error.
func (p *Pool) Close() error {
	var firstErr error
	for _, e := range p.all {
		if err := e.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
