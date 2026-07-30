package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOllama returns a test server that echoes one fixed-dimension vector per
// input string, unless overridden by the handler options.
func fakeOllama(t *testing.T, dim int, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		var req ollamaEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			v := make([]float32, dim)
			for j := range v {
				v[j] = float32(i + 1)
			}
			vecs[i] = v
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{Embeddings: vecs})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewOllamaValidation(t *testing.T) {
	ctx := context.Background()
	_, err := NewOllama(ctx, "", "m")
	require.Error(t, err)
	_, err = NewOllama(ctx, "http://x", "")
	require.Error(t, err)
}

func TestOllamaDiscoversDimAndEmbeds(t *testing.T) {
	ctx := context.Background()
	srv := fakeOllama(t, 4, http.StatusOK)

	o, err := NewOllama(ctx, srv.URL, "test-model")
	require.NoError(t, err)
	t.Cleanup(func() { _ = o.Close() })

	assert.Equal(t, 4, o.Dim())
	assert.Equal(t, "test-model", o.Model())

	vecs, err := o.Embed(ctx, []string{"alpha", "beta"})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
	assert.Len(t, vecs[0], 4)

	empty, err := o.Embed(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, empty)
}

func TestOllamaServerError(t *testing.T) {
	ctx := context.Background()
	srv := fakeOllama(t, 4, http.StatusInternalServerError)

	_, err := NewOllama(ctx, srv.URL, "test-model")
	require.Error(t, err, "warmup against a failing daemon should error")
}

func TestOllamaUnreachable(t *testing.T) {
	ctx := context.Background()
	// Port 1 is not listening; the connection should fail fast.
	_, err := NewOllama(ctx, "http://127.0.0.1:1", "test-model")
	require.Error(t, err)
}
