package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ollama embeds text via a local Ollama daemon's /api/embed endpoint. It is
// the config-selectable alternative to the in-process ONNX backend and is
// still fully local (Ollama runs on the same machine).
type Ollama struct {
	host   string
	model  string
	client *http.Client
	dim    int
}

// NewOllama connects to the Ollama daemon at host (e.g.
// "http://localhost:11434") for the given model, verifying reachability and
// discovering the embedding dimensionality with a warmup request.
func NewOllama(ctx context.Context, host, model string) (*Ollama, error) {
	if host == "" {
		return nil, fmt.Errorf("ollama: host must not be empty")
	}
	if model == "" {
		return nil, fmt.Errorf("ollama: model must not be empty")
	}

	o := &Ollama{
		host:   strings.TrimRight(host, "/"),
		model:  model,
		client: &http.Client{Timeout: 60 * time.Second},
	}

	vecs, err := o.embed(ctx, []string{"warmup"})
	if err != nil {
		return nil, fmt.Errorf("connect to ollama at %s (is the daemon running and "+
			"has %q been pulled with `ollama pull %s`?): %w", o.host, model, model, err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("ollama: model %q produced an empty embedding", model)
	}
	o.dim = len(vecs[0])
	return o, nil
}

// Embed implements Embedder.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	vecs, err := o.embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("ollama: expected %d embeddings, got %d", len(texts), len(vecs))
	}
	return vecs, nil
}

// Dim implements Embedder.
func (o *Ollama) Dim() int { return o.dim }

// Model implements Embedder.
func (o *Ollama) Model() string { return o.model }

// Close implements Embedder. Ollama holds no client-side resources.
func (o *Ollama) Close() error { return nil }

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

func (o *Ollama) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(ollamaEmbedRequest{Model: o.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post embed request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embed response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var parsed ollamaEmbedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("ollama embed error: %s", parsed.Error)
	}
	return parsed.Embeddings, nil
}
