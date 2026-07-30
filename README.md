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
| AST chunking               | tree-sitter (Go, Python, JS/TS)                   |
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

## Requirements

- **Go 1.26+**
- **A C compiler + CGO** — the tree-sitter chunker uses CGO. On Windows install
  [mingw-w64](https://www.mingw-w64.org/) (e.g. via `winget install --id BrechtSanders.WinLibs.POSIX.UCRT`),
  MSYS2, or TDM-GCC, and ensure `gcc` is on `PATH`. On macOS install the Xcode
  command-line tools; on Linux install `gcc`/`build-essential`.

  > Embeddings do **not** need a native ONNX Runtime: hugot's pure-Go backend
  > runs the model in-process. CGO is required only for tree-sitter.

- **(Optional) Ollama** — only if you select the Ollama embedding backend.

## Build

```bash
CGO_ENABLED=1 go build -o agent-context-go ./cmd/agent-context-go
```

On Windows (PowerShell):

```powershell
$env:CGO_ENABLED=1; go build -o agent-context-go.exe ./cmd/agent-context-go
```

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

Each codebase gets its own index database, named by a hash of its absolute
path, under `CCG_INDEX_DIR`. The embedding model and dimension are recorded in
the index; switching models requires `clear_index` (embeddings from different
models are not comparable).

## Register with Claude Code

```bash
claude mcp add agent-context-go -- /absolute/path/to/agent-context-go
```

On Windows, point at the built `.exe`:

```bash
claude mcp add agent-context-go -- C:\path\to\agent-context-go.exe
```

To use the Ollama backend instead of in-process ONNX:

```bash
claude mcp add agent-context-go -e CCG_EMBED_PROVIDER=ollama -e CCG_EMBED_MODEL=nomic-embed-text -- /path/to/agent-context-go
```

Then, from Claude Code, ask it to index a project and search it. All diagnostic
output goes to stderr; stdout carries only the MCP protocol.

## How it works

1. **Index** — walk the codebase (respecting an ignore-directory set and an
   extension allowlist), hash each file, and re-chunk + re-embed only files that
   are new or changed. tree-sitter cuts chunks on declaration boundaries
   (functions, types, classes) with a line-splitter fallback. Files deleted from
   disk are pruned from the index.
2. **Search** — embed the query, score it against every stored chunk vector by
   cosine similarity (brute force), run an FTS5 BM25 lexical query, and fuse the
   two rankings with Reciprocal Rank Fusion.

## Architecture

```
cmd/agent-context-go/   CLI entry: config, deps, run MCP stdio server
internal/config/        Config from env with validated defaults
internal/store/         SQLite: schema, files/chunks, FTS5, embedding BLOBs
internal/chunker/       Chunker interface + tree-sitter impl + line fallback
internal/embed/         Embedder interface + in-process ONNX + Ollama
internal/indexer/       Walk → chunk → embed → store; incremental sync; progress
internal/search/        Hybrid: dense cosine + FTS5 BM25 + RRF fusion
internal/mcpserver/     The four MCP tools wired to indexer/search
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
- Starter languages for AST chunking are Go, Python, and JavaScript/TypeScript;
  others fall back to line-based chunking. Adding a language is one grammar
  dependency plus registration.

## Development

```bash
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=1 go build ./...
gofmt -l .
```
