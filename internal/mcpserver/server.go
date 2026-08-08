// Package mcpserver exposes local code search as an MCP stdio server. It wires
// the indexer, store, and search services behind four tools (index_codebase,
// search_code, clear_index, get_indexing_status) and manages one index handle
// per codebase root.
//
// The concrete tree-sitter chunker (which requires CGO) is injected as an
// interface, so this package builds without CGO; only the final binary links it.
package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vaughanb/agent-context-go/internal/config"
	"github.com/vaughanb/agent-context-go/internal/embed"
	"github.com/vaughanb/agent-context-go/internal/indexer"
	"github.com/vaughanb/agent-context-go/internal/search"
	"github.com/vaughanb/agent-context-go/internal/store"
)

// Chunker is the chunking dependency the indexer needs. It is injected so this
// package never imports the CGO tree-sitter implementation directly.
type Chunker = indexer.Chunker

// serverName and serverVersion identify this server to MCP clients.
const (
	serverName    = "agent-context-go"
	serverVersion = "0.1.0"
)

// Server holds the shared dependencies and per-codebase index handles. It is
// safe for concurrent tool calls. Construct it with New.
type Server struct {
	cfg      *config.Config
	embedder embed.Embedder
	chunker  Chunker

	// baseCtx scopes background index goroutines; it is set by Serve and
	// cancelled when the server shuts down.
	baseCtx context.Context

	mu      sync.Mutex
	handles map[string]*handle
}

// handle is one codebase's index: its store plus the indexer and search
// service bound to it.
type handle struct {
	store   *store.Store
	indexer *indexer.Indexer
	search  *search.Service
}

// New constructs a Server. cfg, embedder, and chunker must be non-nil. The
// embedder is wrapped so its underlying model session is never called
// concurrently by a background index and a search.
func New(cfg *config.Config, embedder embed.Embedder, chunker Chunker) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("mcpserver: config must not be nil")
	}
	if embedder == nil {
		return nil, fmt.Errorf("mcpserver: embedder must not be nil")
	}
	if chunker == nil {
		return nil, fmt.Errorf("mcpserver: chunker must not be nil")
	}
	return &Server{
		cfg:      cfg,
		embedder: newSyncEmbedder(embedder),
		chunker:  chunker,
		baseCtx:  context.Background(),
		handles:  map[string]*handle{},
	}, nil
}

// Serve registers the tools and runs the MCP server over stdio until ctx is
// cancelled or the client disconnects.
func (s *Server) Serve(ctx context.Context) error {
	s.baseCtx = ctx

	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "index_codebase",
		Description: "Index a codebase for semantic and lexical code search. Runs " +
			"incrementally in the background: only new or changed files are " +
			"re-embedded. Poll get_indexing_status for progress.",
	}, s.indexCodebase)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "search_code",
		Description: "Search an indexed codebase with a natural-language or code " +
			"query. Returns the most relevant chunks (path, line range, symbol, " +
			"snippet) ranked by hybrid dense + lexical fusion.",
	}, s.searchCode)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "clear_index",
		Description: "Delete all indexed data for a codebase, leaving an empty index.",
	}, s.clearIndex)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_indexing_status",
		Description: "Report the current or most recent indexing progress for a codebase.",
	}, s.indexingStatus)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcpserver: run: %w", err)
	}
	return nil
}

// handleFor returns the index handle for the codebase at path, creating (and
// opening its store) on first use. It resolves path to an absolute, cleaned
// root so the same codebase always maps to the same handle and database.
func (s *Server) handleFor(ctx context.Context, path string) (*handle, string, error) {
	absRoot, err := filepath.Abs(path)
	if err != nil {
		return nil, "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	absRoot = filepath.Clean(absRoot)

	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handles[absRoot]; ok {
		return h, absRoot, nil
	}

	st, err := store.New(ctx, s.cfg.DBPath(absRoot), s.embedder.Model(), s.embedder.Dim())
	if err != nil {
		return nil, absRoot, fmt.Errorf("open index for %q: %w", absRoot, err)
	}
	ix, err := indexer.New(st, s.chunker, s.embedder)
	if err != nil {
		_ = st.Close()
		return nil, absRoot, fmt.Errorf("build indexer for %q: %w", absRoot, err)
	}
	sr, err := search.New(st, s.embedder)
	if err != nil {
		_ = st.Close()
		return nil, absRoot, fmt.Errorf("build search for %q: %w", absRoot, err)
	}

	h := &handle{store: st, indexer: ix, search: sr}
	s.handles[absRoot] = h
	return h, absRoot, nil
}

// Close releases every open index handle. Call after Serve returns.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, h := range s.handles {
		if err := h.store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.handles = map[string]*handle{}
	return firstErr
}

// syncEmbedder serializes access to an embedder so a background index and a
// concurrent search never call the underlying model session at the same time.
type syncEmbedder struct {
	mu sync.Mutex
	e  embed.Embedder
}

func newSyncEmbedder(e embed.Embedder) *syncEmbedder { return &syncEmbedder{e: e} }

func (s *syncEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.e.Embed(ctx, texts)
}

func (s *syncEmbedder) Dim() int      { return s.e.Dim() }
func (s *syncEmbedder) Model() string { return s.e.Model() }
func (s *syncEmbedder) Close() error  { return s.e.Close() }
