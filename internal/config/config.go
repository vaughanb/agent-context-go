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
	"runtime"
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

	// Concurrency is how many files are chunked and embedded in parallel during
	// indexing. For the in-process ONNX provider it also sizes the embedder
	// pool (one model session per worker), so higher values trade memory for
	// throughput. Always at least 1.
	Concurrency int

	// LowPriority, when true (the default), drops the serving process to
	// below-normal OS scheduling priority so a long index run yields CPU to
	// interactive applications (e.g. a game editor on the same machine)
	// instead of crawling the system. Disable with CCG_LOW_PRIORITY=0.
	LowPriority bool
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
// CCG_OLLAMA_HOST, CCG_INDEX_CONCURRENCY, CCG_LOW_PRIORITY), and validates
// the result.
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
		Concurrency:   defaultConcurrency(),
		LowPriority:   envOr("CCG_LOW_PRIORITY", "1") != "0",
	}

	if raw := os.Getenv("CCG_INDEX_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("parse CCG_INDEX_CONCURRENCY %q: %w", raw, err)
		}
		c.Concurrency = n
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
	if c.Concurrency < 1 {
		return fmt.Errorf("concurrency must be at least 1, got %d", c.Concurrency)
	}
	return nil
}

// defaultConcurrency picks a parallelism level from the machine's CPU count,
// capped so the ONNX embedder pool (one model session per worker) does not use
// an unreasonable amount of memory. Override with CCG_INDEX_CONCURRENCY.
func defaultConcurrency() int {
	const cap = 4
	n := runtime.NumCPU()
	if n > cap {
		n = cap
	}
	if n < 1 {
		n = 1
	}
	return n
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
