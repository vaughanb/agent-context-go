# agent-context-go

A **local-only** MCP server that gives coding agents (Claude Code and others)
semantic + lexical code search over a codebase — with zero cloud dependencies.

It's modeled on [`zilliztech/claude-context`](https://github.com/zilliztech/claude-context),
but every remote piece is replaced with a local equivalent:

| claude-context (reference) | agent-context-go (local)                          |
| -------------------------- | ------------------------------------------------- |
| Cloud embeddings (OpenAI…) | In-process ONNX via hugot, or a local Ollama daemon |
| Milvus / Zilliz Cloud      | SQLite (embeddings as BLOBs + brute-force cosine) |
| BM25 lexical index         | SQLite FTS5 (`bm25()` ranking)                    |
| AST chunking               | tree-sitter (Go, Python, JS/TS, C#)               |
| Incremental re-index       | Per-file content-hash diffing in SQLite           |

Everything runs on your machine. Indexing and search make **no network calls**
to any third-party service (the embedding model is downloaded once from
HuggingFace on first run, after which it is fully offline).

## MCP tools

| Tool                  | Purpose                                                        |
| --------------------- | ------------------------------------------------------------- |
| `index_codebase`      | Index (or incrementally re-index) a codebase in the background |
| `search_code`         | Hybrid semantic + lexical search over an indexed codebase     |
| `get_indexing_status` | Poll indexing progress (phase, files done/total, errors)      |
| `clear_index`         | Empty a codebase's index                                      |

## Install

### Quick install (recommended)

One command downloads a prebuilt binary and registers it into every supported
AI coding agent it detects — no Go toolchain or C compiler required.

**macOS / Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/vaughanb/agent-context-go/main/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/vaughanb/agent-context-go/main/install.ps1 | iex
```

Then restart your agent(s) (see [Configure your agents](#configure-your-agents))
and start searching. On first use the server downloads the ~90 MB embedding
model into `CCG_MODEL_DIR`, after which it runs fully offline.

### With Go

With Go 1.26+ and a C compiler on `PATH` (see below):

```bash
go install github.com/vaughanb/agent-context-go/cmd/agent-context-go@latest
agent-context-go install
```

### Build from source

Building yourself needs **Go 1.26+** and a **C compiler** (CGO is used by the
tree-sitter chunker). Embeddings do *not* need a native ONNX Runtime — hugot's
pure-Go backend runs the model in-process; CGO is required only for tree-sitter.
Ollama is optional, only for the Ollama embedding backend.

Install a C compiler:

- **Windows** — `winget install --id BrechtSanders.WinLibs.POSIX.UCRT` (WinLibs /
  MinGW-w64), then add its `mingw64\bin` to `PATH`; or use MSYS2 / TDM-GCC.
- **macOS** — `xcode-select --install`.
- **Linux (Debian/Ubuntu)** — `sudo apt-get install build-essential`.

Then build and self-register:

```bash
git clone https://github.com/vaughanb/agent-context-go.git
cd agent-context-go
CGO_ENABLED=1 go build -o agent-context-go ./cmd/agent-context-go
./agent-context-go install
```

On Windows (PowerShell): `$env:CGO_ENABLED=1; go build -o agent-context-go.exe ./cmd/agent-context-go`.

## Configuration

All configuration is via environment variables with local defaults:

| Variable             | Default                                       | Description                                   |
| -------------------- | --------------------------------------------- | --------------------------------------------- |
| `CCG_EMBED_PROVIDER` | `onnx`                                         | `onnx` (in-process) or `ollama`               |
| `CCG_EMBED_MODEL`    | `sentence-transformers/all-MiniLM-L6-v2`       | HuggingFace repo id (onnx) or Ollama model    |
| `CCG_EMBED_DIM`      | `384`                                          | Expected embedding dimensionality             |
| `CCG_INDEX_DIR`      | `~/.claude-context-go/index`                   | Where per-codebase SQLite indexes live        |
| `CCG_MODEL_DIR`      | `~/.claude-context-go/models`                  | ONNX model cache                              |
| `CCG_OLLAMA_HOST`    | `http://localhost:11434`                       | Ollama daemon URL (ollama provider only)      |
| `CCG_INDEX_CONCURRENCY` | CPU count (capped at 4)                     | Files chunked/embedded in parallel while indexing |
| `CCG_LOW_PRIORITY`   | `1`                                            | Run at below-normal OS priority (`0` disables)  |

Indexing embeds files in parallel: `CCG_INDEX_CONCURRENCY` workers each hold
their own embedder. With the in-process `onnx` provider each worker loads its own
model session, so higher values trade memory for throughput; raise it on a large
codebase, or lower it to `1` to minimize memory. Embedding is the indexing
bottleneck, so this is the main speed knob. For a large first index you can also
offload embedding to a GPU by pointing `CCG_EMBED_PROVIDER=ollama` at a local
Ollama daemon.

The server also drops itself to below-normal scheduling priority by default
(`CCG_LOW_PRIORITY=0` restores normal priority), so a long first index yields
CPU to whatever you're working in — an IDE, a game editor — instead of
crawling the machine.

Each codebase gets its own index database, named by a hash of its absolute
path, under `CCG_INDEX_DIR`. The embedding model and dimension are recorded in
the index; switching models requires `clear_index` (embeddings from different
models are not comparable). The chunker's algorithm version is recorded too:
after upgrading to a build with a newer chunker, every file is automatically
marked stale and re-chunked on the next `index_codebase` run — no manual
`clear_index` needed, and the old chunks stay searchable until then.

## Configure your agents

The `install` subcommand detects installed agents and writes each one's MCP
config in its own format (idempotently, backing up any existing file). The quick
installers run this for you; you can also run it directly:

```bash
agent-context-go install            # configure every detected agent
agent-context-go list               # show agents and which are detected
agent-context-go install --dry-run  # preview changes without writing
agent-context-go uninstall          # remove this server from agents' configs
```

Useful flags: `--agents <id,...>` targets specific agents, `--all` targets every
supported agent, `--env KEY=VALUE` adds an env var to the registered server
(e.g. for the Ollama backend), `--name` overrides the server name.

Supported agents:

| Agent | Config written |
| ----- | -------------- |
| Claude Desktop | `claude_desktop_config.json` (`mcpServers`) |
| Claude Code | via `claude mcp add -s user` (when the CLI is on `PATH`) |
| Cursor | `~/.cursor/mcp.json` |
| Windsurf | `~/.codeium/windsurf/mcp_config.json` |
| VS Code (Copilot) | user `mcp.json` (`servers`) |
| Codex CLI | via `codex mcp add` (when the CLI is on `PATH`) |
| OpenCode | `~/.config/opencode/opencode.json` (`mcp`) |
| Cline | `cline_mcp_settings.json` |

For CLI-based agents that aren't on `PATH`, `install` prints the exact command
or config snippet to add manually. After configuring, **restart the agent**
(fully quit and reopen desktop apps) so it picks up the new server, then ask it
to index a project and search it.

To register with a tool the installer doesn't cover, point it at the binary as a
stdio MCP server — e.g. `claude mcp add agent-context-go -- /path/to/agent-context-go`.
All diagnostic output goes to stderr; stdout carries only the MCP protocol.

## How it works

1. **Index** — walk the codebase (respecting an ignore-directory set and an
   extension allowlist), hash each file, and re-chunk + re-embed only files that
   are new or changed, with up to `CCG_INDEX_CONCURRENCY` files processed in
   parallel. tree-sitter cuts chunks on declaration boundaries (functions, types,
   classes) with a line-splitter fallback. Container declarations — C#
   namespaces and classes, Python/JS/TS classes — are descended into rather
   than split blindly: each member becomes its own chunk with a qualified
   symbol (`PlayerController.UpdateLookRotation`), and the container's header
   (attributes, declaration, base list) is a chunk of its own. Files deleted
   from disk are pruned from the index. The ignore set covers common
   build/cache dirs plus generated Unity directories (`Library`, `Temp`,
   `Logs`, `Builds`), so pointing it at a Unity project root indexes your
   `Assets` code (including `.shader`/`.hlsl`/`.cginc`/`.asmdef` sources)
   rather than the engine's package cache; Unity serialized YAML (`.asset`,
   `.unity`, `.prefab`) is deliberately excluded.
2. **Search** — embed the query, score it against every stored chunk vector by
   cosine similarity (brute force over an in-memory embedding cache that
   writes invalidate), hydrate full rows for only the top dense candidates,
   run an FTS5 BM25 lexical query (tokens match as prefixes, so `damage`
   finds `damageAmount`), and fuse the two rankings with Reciprocal Rank
   Fusion.

## Architecture

```
cmd/agent-context-go/   CLI entry: serve (stdio) + install/uninstall/list
internal/config/        Config from env with validated defaults
internal/store/         SQLite: schema, files/chunks, FTS5, embedding BLOBs
internal/chunker/       Chunker interface + tree-sitter impl + line fallback
internal/embed/         Embedder interface + in-process ONNX + Ollama
internal/indexer/       Walk → chunk → embed → store; incremental sync; progress
internal/search/        Hybrid: dense cosine + FTS5 BM25 + RRF fusion
internal/mcpserver/     The four MCP tools wired to indexer/search
internal/installer/     Detect agents + write each one's MCP config format
```

## Limitations / roadmap

- Vector search is brute-force cosine over stored `float32` vectors — fine for
  personal/team codebases (hundreds of thousands of chunks); an ANN index
  (HNSW / sqlite-vec) is a later optimization behind the same store interface.
- File selection uses a default ignore set + extension allowlist; full
  `.gitignore` parsing is not yet implemented.
- The default embedding model (all-MiniLM-L6-v2) has a 512-token limit. Chunks
  longer than that are truncated for embedding only (their full text is still
  stored and searchable lexically). For large chunks, the code-tuned
  `jinaai/jina-embeddings-v2-base-code` (8192 tokens) is a drop-in upgrade via
  `CCG_EMBED_MODEL` (also set `CCG_EMBED_DIM=768` and `CCG_ONNX_FILE`).
- Languages with AST chunking are Go, Python, JavaScript/TypeScript, and C#;
  others fall back to line-based chunking. Adding a language is one grammar
  dependency plus registration (plus container kinds if its declarations nest).

## Development

```bash
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=1 go build ./...
gofmt -l .
```
