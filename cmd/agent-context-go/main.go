// Command agent-context-go is a local-only MCP server providing semantic and
// lexical code search over a codebase. It runs entirely on the local machine:
// embeddings are computed in-process (ONNX) or via a local Ollama daemon, and
// the index is stored in per-codebase SQLite databases. Register it with an MCP
// client (e.g. Claude Code) as a stdio server.
//
// All diagnostic output goes to stderr; stdout is reserved for the MCP
// JSON-RPC transport and must never be written to.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gitlab.com/brenden.vaughan/claude-context-go/internal/chunker"
	"gitlab.com/brenden.vaughan/claude-context-go/internal/config"
	"gitlab.com/brenden.vaughan/claude-context-go/internal/embed"
	"gitlab.com/brenden.vaughan/claude-context-go/internal/mcpserver"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("agent-context-go: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.New()
	if err != nil {
		return err
	}

	log.Printf("loading embedding model %q (provider %s); first run may download it…",
		cfg.EmbedModel, cfg.EmbedProvider)
	embedder, err := embed.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = embedder.Close() }()
	log.Printf("embedder ready: model=%q dim=%d", embedder.Model(), embedder.Dim())

	srv, err := mcpserver.New(cfg, embedder, chunker.New())
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	log.Printf("serving MCP over stdio; index dir=%s", cfg.IndexDir)
	if err := srv.Serve(ctx); err != nil {
		return err
	}
	log.Print("shutdown complete")
	return nil
}
