package embed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/backends"
	"github.com/knights-analytics/hugot/pipelines"
)

// ONNX embeds text with an ONNX transformer model running in-process via
// hugot's pure-Go backend. No CGO and no external ONNX Runtime shared library
// are required; the model is downloaded once from HuggingFace and cached
// locally, after which embedding is fully offline.
type ONNX struct {
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	model    string
	dim      int
}

// NewONNX loads modelID (a HuggingFace repo id, e.g.
// "sentence-transformers/all-MiniLM-L6-v2"), caching it under modelDir. onnxFile
// is the repo-relative path of the .onnx variant to use (e.g. "onnx/model.onnx"),
// required for models that ship several; pass "" to let hugot pick when a model
// has exactly one. It downloads the model only when it is not already cached,
// then warms up the pipeline to discover the embedding dimensionality.
func NewONNX(ctx context.Context, modelID, modelDir, onnxFile string) (_ *ONNX, err error) {
	if modelID == "" {
		return nil, fmt.Errorf("onnx: model id must not be empty")
	}

	session, err := hugot.NewGoSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("create onnx session: %w", err)
	}
	// Destroy the session if we fail before handing ownership to the caller.
	defer func() {
		if err != nil {
			_ = session.Destroy()
		}
	}()

	modelPath, err := ensureModel(ctx, modelID, modelDir, onnxFile)
	if err != nil {
		return nil, err
	}

	cfg := hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "ccg-embeddings",
		OnnxFilename: onnxFile,
		Options: []backends.PipelineOption[*pipelines.FeatureExtractionPipeline]{
			// Normalize so vectors are unit length and cosine similarity
			// reduces to a dot product.
			pipelines.WithNormalization(),
		},
	}
	pipeline, err := hugot.NewPipeline(session, cfg)
	if err != nil {
		return nil, fmt.Errorf("create feature extraction pipeline for %q: %w", modelID, err)
	}

	out, err := pipeline.RunPipeline(ctx, []string{"warmup"})
	if err != nil {
		return nil, fmt.Errorf("warm up embedding pipeline for %q: %w", modelID, err)
	}
	if len(out.Embeddings) == 0 || len(out.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("onnx: model %q produced an empty embedding", modelID)
	}

	return &ONNX{
		session:  session,
		pipeline: pipeline,
		model:    modelID,
		dim:      len(out.Embeddings[0]),
	}, nil
}

// Rune caps for embedding. The default model has a fixed 512-token position
// embedding and hugot does not truncate, so an over-long chunk crashes the
// whole batch during graph compilation.
//
//   - maxEmbedRunes is a proactive cap that keeps typical source text under the
//     token limit while preserving embedding quality. Dense code can still
//     exceed 512 tokens at this length, which is why the fallback exists.
//   - safeEmbedRunes is a guaranteed-safe cap: every WordPiece token spans at
//     least one rune, so a text of n runes yields at most n+2 tokens (including
//     the [CLS]/[SEP] specials). 508 runes therefore never exceeds 512 tokens.
//
// Longer chunks are truncated for embedding only; their full text is still
// stored and remains searchable lexically.
const (
	maxEmbedRunes  = 1024
	safeEmbedRunes = 508
)

// Embed implements Embedder. It first tries the whole batch at the proactive
// cap; if that fails (a chunk still tokenized past the model limit), it falls
// back to embedding each input individually, hard-truncating only the ones that
// fail. This keeps full quality for normal chunks while guaranteeing every
// input yields a vector.
func (o *ONNX) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	bounded := make([]string, len(texts))
	for i, t := range texts {
		bounded[i] = truncateRunes(t, maxEmbedRunes)
	}

	if vecs, err := o.runPipeline(ctx, bounded); err == nil {
		return vecs, nil
	}

	// A chunk in the batch exceeded the model's token limit. Recover per item.
	out := make([][]float32, len(bounded))
	for i, t := range bounded {
		vecs, err := o.runPipeline(ctx, []string{t})
		if err != nil {
			vecs, err = o.runPipeline(ctx, []string{truncateRunes(t, safeEmbedRunes)})
			if err != nil {
				return nil, fmt.Errorf("embed input %d even after safe truncation: %w", i, err)
			}
		}
		out[i] = vecs[0]
	}
	return out, nil
}

// runPipeline embeds inputs as a single batch, validating the returned count.
func (o *ONNX) runPipeline(ctx context.Context, inputs []string) ([][]float32, error) {
	out, err := o.pipeline.RunPipeline(ctx, inputs)
	if err != nil {
		return nil, fmt.Errorf("run embedding pipeline: %w", err)
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("onnx: expected %d embeddings, got %d", len(inputs), len(out.Embeddings))
	}
	return out.Embeddings, nil
}

// truncateRunes returns s limited to at most n runes, cutting on a rune
// boundary so the result is always valid UTF-8.
func truncateRunes(s string, n int) string {
	if len(s) <= n { // fast path: byte length is an upper bound on rune count
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// Dim implements Embedder.
func (o *ONNX) Dim() int { return o.dim }

// Model implements Embedder.
func (o *ONNX) Model() string { return o.model }

// Close implements Embedder.
func (o *ONNX) Close() error { return o.session.Destroy() }

// ensureModel returns a local directory holding modelID's files, downloading
// the ones needed (the onnxFile variant plus config and tokenizer files) from
// HuggingFace on first use. onnxFile selects the .onnx variant for models that
// ship several. Downloads use a plain HTTP client rather than hugot's hub
// downloader, which mishandles lock files on Windows.
func ensureModel(ctx context.Context, modelID, modelDir, onnxFile string) (string, error) {
	name := modelID
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	dest := filepath.Join(modelDir, strings.ReplaceAll(name, "/", "_"))

	// If the target .onnx file is already present, the model is cached.
	if onnxFile != "" {
		if fi, err := os.Stat(filepath.Join(dest, filepath.FromSlash(onnxFile))); err == nil && fi.Size() > 0 {
			return dest, nil
		}
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create model dir %q: %w", dest, err)
	}
	if err := downloadModel(ctx, downloadClient, modelID, dest, onnxFile); err != nil {
		return "", fmt.Errorf("download model %q (first run needs network access; "+
			"or set CCG_EMBED_PROVIDER=ollama): %w", modelID, err)
	}
	return dest, nil
}
