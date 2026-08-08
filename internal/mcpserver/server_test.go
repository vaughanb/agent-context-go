package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vaughanb/agent-context-go/internal/chunker"
	"github.com/vaughanb/agent-context-go/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEmbedder is a deterministic, offline embedder: it maps text to a fixed
// unit vector so tests never touch the network or a model. Lexical BM25 carries
// the ranking signal in these tests.
type fakeEmbedder struct{}

func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
	}
	return out, nil
}

func (fakeEmbedder) Dim() int      { return 8 }
func (fakeEmbedder) Model() string { return "fake-test-model" }
func (fakeEmbedder) Close() error  { return nil }

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		IndexDir:      t.TempDir(),
		EmbedProvider: config.ProviderONNX,
		EmbedModel:    "fake-test-model",
		EmbedDim:      8,
	}
	srv, err := New(cfg, fakeEmbedder{}, chunker.New())
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func writeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"server.go": `package svc

func StartServer(addr string) error {
	return nil
}
`,
		"parser.py": `def parse_config(path):
    return path
`,
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	return root
}

// waitForIndex polls until the codebase's index run finishes or the deadline
// passes.
func waitForIndex(t *testing.T, srv *Server, root string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, out, err := srv.indexingStatus(ctx, nil, statusInput{Path: root})
		require.NoError(t, err)
		if !out.Running && out.Phase == "done" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("index did not finish before deadline")
}

func TestNewValidation(t *testing.T) {
	cfg := &config.Config{IndexDir: t.TempDir(), EmbedProvider: config.ProviderONNX, EmbedModel: "m", EmbedDim: 8}

	_, err := New(nil, fakeEmbedder{}, chunker.New())
	require.Error(t, err)
	_, err = New(cfg, nil, chunker.New())
	require.Error(t, err)
	_, err = New(cfg, fakeEmbedder{}, nil)
	require.Error(t, err)
}

func TestIndexSearchClearRoundTrip(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)
	root := writeRepo(t)

	_, idxOut, err := srv.indexCodebase(ctx, nil, indexInput{Path: root})
	require.NoError(t, err)
	assert.True(t, idxOut.Started)
	assert.NotEmpty(t, idxOut.Root)

	waitForIndex(t, srv, root)

	status := func() statusOutput {
		_, out, err := srv.indexingStatus(ctx, nil, statusInput{Path: root})
		require.NoError(t, err)
		return out
	}()
	assert.Equal(t, 2, status.Total)
	assert.Equal(t, 2, status.Done)
	assert.Empty(t, status.Errors)

	// Lexical signal: querying a distinctive symbol surfaces its file.
	_, searchOut, err := srv.searchCode(ctx, nil, searchInput{Path: root, Query: "StartServer", TopN: 5})
	require.NoError(t, err)
	require.NotEmpty(t, searchOut.Results)
	paths := make([]string, len(searchOut.Results))
	for i, r := range searchOut.Results {
		paths[i] = r.Path
	}
	assert.Contains(t, paths, "server.go")

	// Clear empties the index; a subsequent search returns nothing.
	_, clr, err := srv.clearIndex(ctx, nil, clearInput{Path: root})
	require.NoError(t, err)
	assert.True(t, clr.Cleared)

	_, afterClear, err := srv.searchCode(ctx, nil, searchInput{Path: root, Query: "StartServer", TopN: 5})
	require.NoError(t, err)
	assert.Empty(t, afterClear.Results)
}

func TestIncrementalReindex(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)
	root := writeRepo(t)

	_, _, err := srv.indexCodebase(ctx, nil, indexInput{Path: root})
	require.NoError(t, err)
	waitForIndex(t, srv, root)

	// Delete a file, re-index: it should be pruned from search results.
	require.NoError(t, os.Remove(filepath.Join(root, "parser.py")))
	_, _, err = srv.indexCodebase(ctx, nil, indexInput{Path: root})
	require.NoError(t, err)
	waitForIndex(t, srv, root)

	_, out, err := srv.searchCode(ctx, nil, searchInput{Path: root, Query: "parse_config", TopN: 5})
	require.NoError(t, err)
	for _, r := range out.Results {
		assert.NotEqual(t, "parser.py", r.Path, "deleted file must not appear in results")
	}
}

func TestToolsRequireArguments(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)

	_, _, err := srv.indexCodebase(ctx, nil, indexInput{Path: ""})
	require.Error(t, err)

	_, _, err = srv.searchCode(ctx, nil, searchInput{Path: "", Query: "x"})
	require.Error(t, err)

	root := writeRepo(t)
	_, _, err = srv.searchCode(ctx, nil, searchInput{Path: root, Query: ""})
	require.Error(t, err)
}
