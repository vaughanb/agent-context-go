// Package config resolves runtime configuration for the code-search server
// from sane defaults and environment overrides. Everything is local: there
// are no cloud endpoints or API keys.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Embedding providers. onnx runs a model in-process; ollama talks to a local
// Ollama daemon. Both are local-only.
const (
	ProviderONNX   = "onnx"
	ProviderOllama = "ollama"
)

// Config is the fully resolved configuration for a run. Construct it with New.
type Config struct {
	// IndexDir is the directory holding one SQLite database per indexed
	// codebase.
	IndexDir string

	// EmbedProvider selects the embedding backend (ProviderONNX or ProviderOllama).
	EmbedProvider string

	// EmbedModel is the model identifier for the selected provider: a
	// HuggingFace repo id for ONNX, or an Ollama model name for Ollama.
	EmbedModel string

	// EmbedDim is the embedding vector dimensionality. In practice the
	// embedder is the source of truth (it reports its own Dim); this default
	// documents the expected size for the default model.
	EmbedDim int

	// ModelDir is where downloaded ONNX models are cached, used only when
	// EmbedProvider is ProviderONNX.
	ModelDir string

	// ONNXFile is the repo-relative path of the .onnx file to use, needed for
	// models that ship several variants (e.g. quantized). Used only when
	// EmbedProvider is ProviderONNX.
	ONNXFile string

	// OllamaHost is the base URL of the local Ollama daemon, used only when
	// EmbedProvider is ProviderOllama.
	OllamaHost string
}

const (
	defaultONNXModel   = "sentence-transformers/all-MiniLM-L6-v2"
	defaultOllamaModel = "nomic-embed-text"
	defaultEmbedDim    = 384
	defaultOllamaHost  = "http://localhost:11434"
	// defaultONNXFile selects the .onnx variant of the default model, which
	// ships several. Override with CCG_ONNX_FILE when using a different model
	// whose file lives elsewhere (or set it empty for single-file models).
	defaultONNXFile = "onnx/model.onnx"
)

// New builds a Config from defaults, applying environment overrides
// (CCG_INDEX_DIR, CCG_EMBED_PROVIDER, CCG_EMBED_MODEL, CCG_EMBED_DIM,
// CCG_OLLAMA_HOST), and validates the result.
func New() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	base := filepath.Join(home, ".claude-context-go")
	provider := envOr("CCG_EMBED_PROVIDER", ProviderONNX)
	c := &Config{
		IndexDir:      envOr("CCG_INDEX_DIR", filepath.Join(base, "index")),
		EmbedProvider: provider,
		EmbedModel:    envOr("CCG_EMBED_MODEL", defaultModelFor(provider)),
		EmbedDim:      defaultEmbedDim,
		ModelDir:      envOr("CCG_MODEL_DIR", filepath.Join(base, "models")),
		ONNXFile:      envOr("CCG_ONNX_FILE", defaultONNXFile),
		OllamaHost:    envOr("CCG_OLLAMA_HOST", defaultOllamaHost),
	}

	if raw := os.Getenv("CCG_EMBED_DIM"); raw != "" {
		dim, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("parse CCG_EMBED_DIM %q: %w", raw, err)
		}
		c.EmbedDim = dim
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	switch c.EmbedProvider {
	case ProviderONNX, ProviderOllama:
	default:
		return fmt.Errorf("invalid embed provider %q: must be %q or %q", c.EmbedProvider, ProviderONNX, ProviderOllama)
	}
	if c.IndexDir == "" {
		return fmt.Errorf("index dir must not be empty")
	}
	if c.EmbedModel == "" {
		return fmt.Errorf("embed model must not be empty")
	}
	if c.EmbedDim <= 0 {
		return fmt.Errorf("embed dim must be positive, got %d", c.EmbedDim)
	}
	return nil
}

// DBPath returns the SQLite database path for the codebase rooted at absRoot.
// The name is derived from a hash of the absolute path so distinct codebases
// never collide while the same codebase always maps to the same file.
func (c *Config) DBPath(absRoot string) string {
	sum := sha256.Sum256([]byte(absRoot))
	name := hex.EncodeToString(sum[:]) + ".db"
	return filepath.Join(c.IndexDir, name)
}

// defaultModelFor returns the default embedding model for a provider.
func defaultModelFor(provider string) string {
	if provider == ProviderOllama {
		return defaultOllamaModel
	}
	return defaultONNXModel
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
