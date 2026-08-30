// Package chunker splits source files into searchable chunks. It uses
// tree-sitter to cut on declaration boundaries (functions, types, classes)
// for supported languages, and falls back to a fixed-size line splitter for
// everything else.
package chunker

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/vaughanb/agent-context-go/internal/core"
)

// Chunker splits a source file's bytes into chunks. path informs language
// detection; it does not need to exist on disk.
type Chunker interface {
	Chunk(ctx context.Context, path string, src []byte) ([]core.Chunk, error)
}

// Version identifies the chunking algorithm. The store records it per index
// and marks every file changed when it differs, so a chunker upgrade
// re-chunks existing indexes instead of leaving stale chunk boundaries
// behind (content-hash change detection alone would never notice). Bump it
// whenever a change alters the chunks produced for identical input: new
// grammars, tuning changes, splitting or merging logic.
//
// Version 2: C# grammar, container descent with qualified symbols, and the
// merge size cap.
const Version = "2"

// Default tuning. maxLines bounds a chunk so no single embedding covers too
// much; overlap keeps context across split boundaries; mergeMaxLines is the
// size under which trivial symbol-less chunks are folded into a neighbor.
const (
	defaultMaxLines     = 60
	defaultOverlap      = 10
	defaultMergeMaxLine = 4
)

// Service is the default Chunker. It dispatches by file extension to a
// tree-sitter language when one is registered, otherwise to the line splitter.
type Service struct {
	maxLines     int
	overlap      int
	mergeMaxLine int
	langs        map[string]*language // file extension (with dot) -> language
}

// Option configures a Service.
type Option func(*Service)

// WithMaxLines sets the maximum number of lines per chunk.
func WithMaxLines(n int) Option { return func(s *Service) { s.maxLines = n } }

// WithOverlap sets how many lines overlap between adjacent splits of an
// oversized unit.
func WithOverlap(n int) Option { return func(s *Service) { s.overlap = n } }

// New returns a Service with all built-in languages registered.
func New(opts ...Option) *Service {
	s := &Service{
		maxLines:     defaultMaxLines,
		overlap:      defaultOverlap,
		mergeMaxLine: defaultMergeMaxLine,
		langs:        builtinLanguages(),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.overlap >= s.maxLines {
		s.overlap = s.maxLines / 2
	}
	return s
}

// Version reports the chunking algorithm version for index bookkeeping (see
// the package-level Version constant).
func (s *Service) Version() string { return Version }

// Chunk implements Chunker. It never returns an error for an unsupported
// language or a parse failure; it falls back to line-based chunking so every
// file is always indexable.
func (s *Service) Chunk(ctx context.Context, path string, src []byte) ([]core.Chunk, error) {
	if len(src) == 0 {
		return nil, nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := s.langs[ext]; ok {
		if chunks := s.astChunks(src, lang); len(chunks) > 0 {
			return chunks, nil
		}
	}
	return s.lineChunks(src, 1, ""), nil
}

// lineChunks splits content into fixed-size, overlapping windows. startLine is
// the 1-based line number of content's first line within the original file;
// symbol is attached to every produced chunk (empty for whole-file fallback).
func (s *Service) lineChunks(content []byte, startLine int, symbol string) []core.Chunk {
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) == 0 {
		return nil
	}

	var chunks []core.Chunk
	step := s.maxLines - s.overlap
	if step < 1 {
		step = s.maxLines
	}
	for start := 0; start < len(lines); start += step {
		end := min(start+s.maxLines, len(lines))
		text := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		chunks = append(chunks, core.Chunk{
			StartLine: startLine + start,
			EndLine:   startLine + end - 1,
			Symbol:    symbol,
			Content:   text,
		})
		if end == len(lines) {
			break
		}
	}
	return chunks
}

// mergeTrivial folds consecutive tiny, symbol-less chunks into the previous
// chunk to reduce noise (e.g. a package clause followed by a lone import, or
// a run of C# field declarations). Chunks carrying a symbol are always
// preserved as distinct units, and a merged chunk stops absorbing once it
// reaches maxLines so a long run cannot snowball into one oversized chunk.
func (s *Service) mergeTrivial(chunks []core.Chunk) []core.Chunk {
	if len(chunks) < 2 {
		return chunks
	}
	out := make([]core.Chunk, 0, len(chunks))
	out = append(out, chunks[0])
	for _, c := range chunks[1:] {
		prev := &out[len(out)-1]
		trivial := c.Symbol == "" && (c.EndLine-c.StartLine+1) <= s.mergeMaxLine
		if trivial && prev.Symbol == "" && (prev.EndLine-prev.StartLine+1) < s.maxLines {
			prev.Content += "\n" + c.Content
			prev.EndLine = c.EndLine
			continue
		}
		out = append(out, c)
	}
	return out
}
