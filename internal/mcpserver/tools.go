package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vaughanb/agent-context-go/internal/indexer"
)

// defaultTopN is the number of results returned by search_code when the caller
// does not specify one. maxSnippet caps snippet length in results.
const (
	defaultTopN = 10
	maxSnippet  = 800
)

// indexInput is the argument to index_codebase.
type indexInput struct {
	Path string `json:"path" jsonschema:"absolute or relative path to the codebase root to index"`
}

// indexOutput is the result of index_codebase.
type indexOutput struct {
	Root    string `json:"root"`
	Started bool   `json:"started"`
	Message string `json:"message"`
}

// indexCodebase starts (or reports an already-running) background index for the
// codebase at the given path and returns immediately.
func (s *Server) indexCodebase(ctx context.Context, _ *mcp.CallToolRequest, in indexInput) (*mcp.CallToolResult, indexOutput, error) {
	if in.Path == "" {
		return nil, indexOutput{}, fmt.Errorf("path is required")
	}
	h, root, err := s.handleFor(ctx, in.Path)
	if err != nil {
		return nil, indexOutput{}, err
	}

	// Start claims the run lock synchronously, so a subsequent
	// get_indexing_status immediately reflects this run rather than a stale
	// previous one. The run itself proceeds on s.baseCtx (not the request ctx,
	// which ends when this call returns).
	if err := h.indexer.Start(s.baseCtx, root); err != nil {
		if errors.Is(err, indexer.ErrAlreadyRunning) {
			return nil, indexOutput{Root: root, Started: false, Message: "indexing already in progress"}, nil
		}
		return nil, indexOutput{}, err
	}

	return nil, indexOutput{Root: root, Started: true, Message: "indexing started; poll get_indexing_status for progress"}, nil
}

// searchInput is the argument to search_code.
type searchInput struct {
	Path  string `json:"path" jsonschema:"path to the indexed codebase root"`
	Query string `json:"query" jsonschema:"natural-language or code query"`
	TopN  int    `json:"top_n,omitempty" jsonschema:"maximum number of results to return (default 10)"`
}

// searchHit is one result in searchOutput.
type searchHit struct {
	Path      string  `json:"path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Symbol    string  `json:"symbol,omitempty"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
}

// searchOutput is the result of search_code.
type searchOutput struct {
	Root    string      `json:"root"`
	Results []searchHit `json:"results"`
}

// searchCode runs hybrid search over an indexed codebase.
func (s *Server) searchCode(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
	if in.Path == "" {
		return nil, searchOutput{}, fmt.Errorf("path is required")
	}
	if in.Query == "" {
		return nil, searchOutput{}, fmt.Errorf("query is required")
	}
	topN := in.TopN
	if topN <= 0 {
		topN = defaultTopN
	}

	h, root, err := s.handleFor(ctx, in.Path)
	if err != nil {
		return nil, searchOutput{}, err
	}

	hits, err := h.search.Search(ctx, in.Query, topN)
	if err != nil {
		return nil, searchOutput{}, err
	}

	results := make([]searchHit, len(hits))
	for i, hit := range hits {
		results[i] = searchHit{
			Path:      hit.Path,
			StartLine: hit.StartLine,
			EndLine:   hit.EndLine,
			Symbol:    hit.Symbol,
			Score:     hit.Score,
			Snippet:   truncate(hit.Content, maxSnippet),
		}
	}
	return nil, searchOutput{Root: root, Results: results}, nil
}

// clearInput is the argument to clear_index.
type clearInput struct {
	Path string `json:"path" jsonschema:"path to the codebase whose index should be cleared"`
}

// clearOutput is the result of clear_index.
type clearOutput struct {
	Root    string `json:"root"`
	Cleared bool   `json:"cleared"`
	Message string `json:"message"`
}

// clearIndex empties the index for a codebase while keeping its embedding-model
// metadata so it can be rebuilt with the same model.
func (s *Server) clearIndex(ctx context.Context, _ *mcp.CallToolRequest, in clearInput) (*mcp.CallToolResult, clearOutput, error) {
	if in.Path == "" {
		return nil, clearOutput{}, fmt.Errorf("path is required")
	}
	h, root, err := s.handleFor(ctx, in.Path)
	if err != nil {
		return nil, clearOutput{}, err
	}
	if err := h.store.Clear(ctx); err != nil {
		return nil, clearOutput{}, err
	}
	return nil, clearOutput{Root: root, Cleared: true, Message: "index cleared"}, nil
}

// statusInput is the argument to get_indexing_status.
type statusInput struct {
	Path string `json:"path" jsonschema:"path to the codebase to report indexing status for"`
}

// statusOutput is the result of get_indexing_status.
type statusOutput struct {
	Root    string   `json:"root"`
	Phase   string   `json:"phase"`
	Running bool     `json:"running"`
	Total   int      `json:"total"`
	Done    int      `json:"done"`
	Errors  []string `json:"errors,omitempty"`
}

// indexingStatus reports the current or most recent index progress.
func (s *Server) indexingStatus(ctx context.Context, _ *mcp.CallToolRequest, in statusInput) (*mcp.CallToolResult, statusOutput, error) {
	if in.Path == "" {
		return nil, statusOutput{}, fmt.Errorf("path is required")
	}
	h, root, err := s.handleFor(ctx, in.Path)
	if err != nil {
		return nil, statusOutput{}, err
	}
	p := h.indexer.Status()
	return nil, statusOutput{
		Root:    root,
		Phase:   string(p.Phase),
		Running: h.indexer.Running(),
		Total:   p.Total,
		Done:    p.Done,
		Errors:  p.Errors,
	}, nil
}

// truncate shortens s to at most n bytes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
