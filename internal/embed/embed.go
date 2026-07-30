// Package embed turns text into dense vectors for semantic search. It offers
// two fully local backends behind one interface: in-process ONNX (default, no
// external process) and a local Ollama daemon. Neither backend calls a
// third-party cloud service.
package embed

import (
	"context"
	"fmt"

	"gitlab.com/brenden.vaughan/claude-context-go/internal/config"
)

// Embedder produces embeddings for text. Implementations discover their own
// dimensionality at construction, so Dim is authoritative for sizing storage.
type Embedder interface {
	// Embed returns one vector per input string, in order. All vectors have
	// length Dim.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dim reports the embedding dimensionality.
	Dim() int
	// Model reports the model identifier, persisted with the index so a later
	// run can detect an incompatible model.
	Model() string
	// Close releases any resources (model session, connections).
	Close() error
}

// New constructs the Embedder selected by cfg.EmbedProvider.
func New(ctx context.Context, cfg *config.Config) (Embedder, error) {
	switch cfg.EmbedProvider {
	case config.ProviderONNX:
		return NewONNX(ctx, cfg.EmbedModel, cfg.ModelDir, cfg.ONNXFile)
	case config.ProviderOllama:
		return NewOllama(ctx, cfg.OllamaHost, cfg.EmbedModel)
	default:
		return nil, fmt.Errorf("embed: unknown provider %q", cfg.EmbedProvider)
	}
}
