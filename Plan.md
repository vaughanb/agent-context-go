# **Plan: Local-Only Code Search MCP Server (Go)**

## Context

We want an MCP server, written in Go, that gives Claude (and other coding agents) semantic \+ lexical code search over a codebase — modeled on [`zilliztech/claude-context`](https://github.com/zilliztech/claude-context) but with zero third-party/cloud dependencies. The reference project depends on Zilliz Cloud / Milvus for vector storage and cloud embedding APIs (OpenAI, VoyageAI, Gemini). Our version must run entirely on the local machine.  
Everything the reference does has a clean local equivalent:

| Reference component | Local replacement |
| :---- | :---- |
| Cloud embeddings (OpenAI/Voyage/Gemini) | In-process ONNX via hugot (default) \+ optional local Ollama |
| Milvus / Zilliz Cloud vector DB | SQLite (embeddings as BLOBs \+ brute-force cosine) |
| BM25 lexical index | SQLite FTS5 (bm25() ranking, built in) |
| AST chunking (tree-sitter) | tree-sitter official Go CGO bindings |
| Merkle-tree incremental re-index | Per-file content-hash diffing in SQLite |
| MCP tools | Official modelcontextprotocol/go-sdk |

Intended outcome: a single Go binary (agent-context-go) that Claude Code registers as an stdio MCP server, exposing index\_codebase, search\_code, clear\_index, and get\_indexing\_status. Indexes and searches with no network calls to any external service.

### Confirmed decisions

* Embeddings: in-process ONNX as default, behind an Embedder interface, with a local Ollama implementation available as a config-selectable alternative.  
* Chunking: tree-sitter AST from the start.

### Key design choices (my recommendations, applied unless you say otherwise)

* CGO is required (tree-sitter \+ ONNX Runtime). Acceptable given the tree-sitter decision.  
* Vector search \= brute-force cosine over stored float32 vectors for v1. Fine for personal/team codebases (\~hundreds of k chunks, \<100ms). HNSW / sqlite-vec is a later optimization behind the same Store interface.  
* Index location: central cache, \~/.agent-context-go/index/\<sha256(abs-path)\>.db, one SQLite file per indexed codebase (mirrors claude-context's per-codebase collections).  
* Default embedding model: all-MiniLM-L6-v2 (384-dim, well-supported by hugot) for the first working slice; jina-embeddings-v2-base-code (code-tuned) as a documented upgrade. Embedding dim is stored in the DB so models can differ per index.  
* Starter languages: Go, Python, JavaScript/TypeScript. Others added incrementally (each is one grammar dependency \+ a query file behind the Chunker interface).

## Architecture / Layout

cmd/agent-context-go/main.go   \# CLI entry: parse config, build deps, run MCP stdio server  
internal/config/                \# Config struct \+ New() from env/flags with validated defaults  
internal/store/                 \# SQLite: schema, files/chunks upsert, FTS5, embedding BLOBs  
internal/chunker/               \# Chunker interface \+ tree-sitter impl \+ line-splitter fallback  
internal/embed/                 \# Embedder interface \+ onnx (hugot) impl \+ ollama impl  
internal/indexer/               \# Walk \-\> ignore \-\> chunk \-\> embed \-\> store; hash-based incremental sync; progress  
internal/search/                \# Hybrid: dense cosine \+ FTS5 BM25 \+ RRF fusion  
internal/mcpserver/             \# 4 MCP tool handlers wired to indexer/search  
Follows the standard Go layout and your conventions: New... constructors with  
validation, ctx first arg on all I/O, small interfaces (Chunker, Embedder,  
Store), errors wrapped with fmt.Errorf(... %w ...), table-driven tests, and  
go-util reused where it fits (e.g. log, errors, pointer, set, transform).

### Core interfaces (sketch)

type Chunker interface {  
   Chunk(ctx context.Context, path string, src \[\]byte) (\[\]Chunk, error)  
}  
type Embedder interface {  
   Embed(ctx context.Context, texts \[\]string) (\[\]\[\]float32, error)  
   Dim() int  
}  
type Store interface {  
   UpsertFile(ctx context.Context, f File) (changed bool, err error)  
   ReplaceChunks(ctx context.Context, fileID int64, chunks \[\]Chunk, vecs \[\]\[\]float32) error  
   DeleteFile(ctx context.Context, path string) error  
   AllVectors(ctx context.Context) (\[\]VecRow, error)   // for brute-force cosine  
   LexicalSearch(ctx context.Context, query string, k int) (\[\]Hit, error) // FTS5 bm25()  
   Clear(ctx context.Context) error  
}

### SQLite schema (one DB per codebase)

* meta(key, value) — embedding model name, dim, schema version.  
* files(id, path UNIQUE, mtime, size, content\_hash, chunk\_count).  
* chunks(id, file\_id, start\_line, end\_line, symbol, content, embedding BLOB).  
* chunks\_fts — FTS5 virtual table over content (contentless, external-content linked to chunks) for BM25 lexical search.

### Hybrid search flow (search\_code)

1. Embed the query → cosine vs all chunk vectors → dense top-K.  
2. FTS5 MATCH with bm25() → lexical top-K.  
3. Fuse with Reciprocal Rank Fusion (score \= Σ 1/(k+rank), k≈60).  
4. Return top-N: path:startLine-endLine, symbol, snippet, fused score.

### Incremental indexing

* Walk respecting .gitignore \+ configurable ignore globs and extension allowlist.  
* For each file: compute content hash; UpsertFile reports changed/new.  
* Only changed/new files are re-chunked \+ re-embedded (ReplaceChunks).  
* Files present in DB but gone from disk are deleted.  
* index\_codebase runs sync in a goroutine; progress (files done / total, phase, errors) tracked in-memory and reported by get\_indexing\_status.

## Incremental delivery (one MR each)

1. Skeleton \+ SQLite store. go.mod (go 1.26), layout, config, store with schema \+ FTS5 \+ upsert/replace/delete/lexical-search, unit tests. No embeddings/chunking yet.  
2. Chunker (tree-sitter). Chunker interface, tree-sitter impl for Go/Python/JS/TS with size-bounded chunks \+ symbol names, line-splitter fallback, tests.  
3. Embedder. Embedder interface, in-process ONNX (hugot) impl \+ Ollama impl, config selection, model/dim persisted to meta. Tests skip gracefully if model/runtime absent.  
4. Indexer \+ incremental sync. Walk/ignore/allowlist, hash diffing, progress tracking, wire chunker+embedder+store, tests.  
5. Hybrid search. Dense cosine \+ BM25 \+ RRF fusion, tests with a small fixture index.  
6. MCP server. Wire the 4 tools over stdio via the official SDK; main.go; README with claude mcp add registration \+ ONNX Runtime setup notes.

## Risks / notes

* ONNX Runtime shared lib: hugot/onnxruntime\_go need libonnxruntime. MR 3 documents install/bundling; the Embedder interface means Ollama is the fallback if this is painful in your environment.  
* CGO cross-compilation is harder; acceptable for a local dev tool. Documented in README.  
* Brute-force cosine is a deliberate v1 simplification; swap-in point is isolated to Store.

## Verification

* go build ./..., go vet ./..., gofmt/goimports, go test ./... \-race.  
* Per-package table-driven unit tests (store round-trips, chunk boundaries, RRF fusion, hash diffing).  
* End-to-end: build binary, index\_codebase this repo, search\_code a natural-language query, confirm relevant path:line hits; edit a file, re-index, confirm only it re-embeds; clear\_index empties the DB.  
* Register with Claude Code via claude mcp add and exercise the tools live.

