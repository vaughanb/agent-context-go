package embed

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestONNXEndToEnd exercises the real in-process ONNX backend, including a
// one-time model download. It is gated behind CCG_TEST_ONNX so normal test
// runs stay fast and offline. Run it with:
//
//	CCG_TEST_ONNX=1 go test ./internal/embed/ -run ONNX
func TestONNXEndToEnd(t *testing.T) {
	if os.Getenv("CCG_TEST_ONNX") == "" {
		t.Skip("set CCG_TEST_ONNX=1 to run the ONNX download+embed integration test")
	}
	ctx := context.Background()

	e, err := NewONNX(ctx, "sentence-transformers/all-MiniLM-L6-v2", t.TempDir(), "onnx/model.onnx")
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	assert.Equal(t, 384, e.Dim())

	vecs, err := e.Embed(ctx, []string{
		"function that reads a configuration file",
		"parse settings from disk",
		"bright orange sunset over the ocean",
	})
	require.NoError(t, err)
	require.Len(t, vecs, 3)
	assert.Len(t, vecs[0], e.Dim())

	// The two config-related sentences should be more similar to each other
	// than either is to the unrelated sentence about a sunset.
	related := cosine(vecs[0], vecs[1])
	unrelated := cosine(vecs[0], vecs[2])
	assert.Greater(t, related, unrelated, "semantically similar text should score higher")

	// A chunk far longer than the model's token limit must still embed (via
	// truncation) rather than crashing the batch.
	long := strings.Repeat("func Handler(ctx context.Context, id int64) error { return process(ctx, id) }\n", 200)
	longVecs, err := e.Embed(ctx, []string{"short", long})
	require.NoError(t, err)
	require.Len(t, longVecs, 2)
	assert.Len(t, longVecs[1], e.Dim())
}

func TestTruncateRunes(t *testing.T) {
	testCases := map[string]struct {
		in   string
		n    int
		want string
	}{
		"under limit":      {in: "hello", n: 10, want: "hello"},
		"at limit":         {in: "hello", n: 5, want: "hello"},
		"over limit":       {in: "hello world", n: 5, want: "hello"},
		"multibyte safe":   {in: "héllo", n: 3, want: "hél"},
		"empty":            {in: "", n: 5, want: ""},
		"multibyte cutoff": {in: "日本語テスト", n: 2, want: "日本"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := truncateRunes(tc.in, tc.n)
			assert.Equal(t, tc.want, got)
			assert.True(t, len([]rune(got)) <= tc.n)
		})
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
