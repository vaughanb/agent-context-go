package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDefaults(t *testing.T) {
	// Ensure a clean environment so defaults are exercised.
	for _, k := range []string{
		"CCG_INDEX_DIR", "CCG_EMBED_PROVIDER", "CCG_EMBED_MODEL", "CCG_EMBED_DIM", "CCG_OLLAMA_HOST",
	} {
		t.Setenv(k, "")
	}

	c, err := New()
	require.NoError(t, err)
	assert.Equal(t, ProviderONNX, c.EmbedProvider)
	assert.Equal(t, defaultONNXModel, c.EmbedModel)
	assert.Equal(t, defaultEmbedDim, c.EmbedDim)
	assert.NotEmpty(t, c.IndexDir)
	assert.NotEmpty(t, c.ModelDir)
	assert.GreaterOrEqual(t, c.Concurrency, 1, "concurrency defaults to at least 1")
}

func TestNewConcurrencyOverride(t *testing.T) {
	t.Setenv("CCG_INDEX_CONCURRENCY", "8")
	c, err := New()
	require.NoError(t, err)
	assert.Equal(t, 8, c.Concurrency)
}

func TestNewOllamaDefaultModel(t *testing.T) {
	for _, k := range []string{"CCG_EMBED_MODEL"} {
		t.Setenv(k, "")
	}
	t.Setenv("CCG_EMBED_PROVIDER", ProviderOllama)

	c, err := New()
	require.NoError(t, err)
	assert.Equal(t, defaultOllamaModel, c.EmbedModel)
}

func TestNewEnvOverrides(t *testing.T) {
	t.Setenv("CCG_INDEX_DIR", "/tmp/ccg")
	t.Setenv("CCG_EMBED_PROVIDER", ProviderOllama)
	t.Setenv("CCG_EMBED_MODEL", "nomic-embed-text")
	t.Setenv("CCG_EMBED_DIM", "768")
	t.Setenv("CCG_OLLAMA_HOST", "http://localhost:1234")

	c, err := New()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/ccg", c.IndexDir)
	assert.Equal(t, ProviderOllama, c.EmbedProvider)
	assert.Equal(t, "nomic-embed-text", c.EmbedModel)
	assert.Equal(t, 768, c.EmbedDim)
	assert.Equal(t, "http://localhost:1234", c.OllamaHost)
}

func TestNewInvalid(t *testing.T) {
	testCases := map[string]map[string]string{
		"bad provider":     {"CCG_EMBED_PROVIDER": "cloud"},
		"bad dim":          {"CCG_EMBED_DIM": "not-a-number"},
		"bad concurrency":  {"CCG_INDEX_CONCURRENCY": "not-a-number"},
		"zero concurrency": {"CCG_INDEX_CONCURRENCY": "0"},
	}
	for name, env := range testCases {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			_, err := New()
			require.Error(t, err)
		})
	}
}

func TestDBPathStableAndDistinct(t *testing.T) {
	c := &Config{IndexDir: "/idx"}

	a1 := c.DBPath("/home/user/proj-a")
	a2 := c.DBPath("/home/user/proj-a")
	b := c.DBPath("/home/user/proj-b")

	assert.Equal(t, a1, a2, "same root maps to same db file")
	assert.NotEqual(t, a1, b, "distinct roots map to distinct db files")
	assert.Equal(t, filepath.Clean("/idx"), filepath.Dir(a1))
	assert.Equal(t, ".db", filepath.Ext(a1))
}
