// Command agent-context-go is a local-only MCP server providing semantic and
// lexical code search over a codebase. It runs entirely on the local machine:
// embeddings are computed in-process (ONNX) or via a local Ollama daemon, and
// the index is stored in per-codebase SQLite databases.
//
// With no arguments it runs the MCP stdio server (how an agent launches it).
// The install/uninstall/list subcommands register it into detected agents.
//
// When serving, all diagnostic output goes to stderr; stdout is reserved for
// the MCP JSON-RPC transport and must never be written to.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/vaughanb/agent-context-go/internal/chunker"
	"github.com/vaughanb/agent-context-go/internal/config"
	"github.com/vaughanb/agent-context-go/internal/embed"
	"github.com/vaughanb/agent-context-go/internal/mcpserver"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("agent-context-go: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	var err error
	switch cmd {
	case "", "serve":
		err = serve()
	case "install":
		err = runInstall(args[1:], false)
	case "uninstall":
		err = runInstall(args[1:], true)
	case "list":
		err = runList(args[1:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// serve runs the MCP stdio server until the client disconnects or the process
// is signalled.
func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.New()
	if err != nil {
		return err
	}

	if cfg.LowPriority {
		if err := lowerProcessPriority(); err != nil {
			log.Printf("keeping normal process priority (lowering failed: %v)", err)
		} else {
			log.Print("running at below-normal process priority so indexing stays in the background (CCG_LOW_PRIORITY=0 to disable)")
		}
	}

	log.Printf("loading embedding model %q (provider %s, %d parallel); first run may download it…",
		cfg.EmbedModel, cfg.EmbedProvider, cfg.Concurrency)
	embedder, err := embed.NewPool(ctx, cfg, cfg.Concurrency)
	if err != nil {
		return err
	}
	defer func() { _ = embedder.Close() }()
	log.Printf("embedder ready: model=%q dim=%d instances=%d", embedder.Model(), embedder.Dim(), embedder.Size())

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

func printUsage(w *os.File) {
	fmt.Fprint(w, `agent-context-go — local-only code-search MCP server

Usage:
  agent-context-go                 Run the MCP server over stdio (how agents launch it)
  agent-context-go install         Register this server into detected AI coding agents
  agent-context-go uninstall       Remove this server from agents' configs
  agent-context-go list            Show supported agents and whether each is detected
  agent-context-go help            Show this help

Install options:
  --agents <id,...>   Target specific agents (default: all detected)
  --all               Target every supported agent, even if not detected
  --name <name>       Server name to register (default: agent-context-go)
  --env KEY=VALUE     Environment variable for the server (repeatable)
  --dry-run           Show what would change without writing

Run "agent-context-go list" to see agent ids.
`)
}
