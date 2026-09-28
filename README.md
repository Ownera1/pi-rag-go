# pi-rag-go

Go RAG Core for a shared local knowledge store. It parses GROBID TEI, Markdown, plain text, and source/configuration files; indexes with SQLite FTS5 and sqlite-vec; and serves structured results over MCP. The existing TypeScript `XML_parse` implementation is the migration reference.

## Build

Requires Go 1.25+, CGO and a C compiler. On macOS, Xcode command line tools satisfy the compiler requirement.

```sh
go test -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 -o bin/ragd ./cmd/ragd
go build -tags sqlite_fts5 -o bin/ragctl ./cmd/ragctl
```

## Configure a new store

Copy `config.example.json` to a separate store directory as `config.json`, then set `VOYAGE_API_KEY` for the example Voyage provider. `ragd --config /absolute/config.json` can load a different config path. Credentials are read from the environment and are never stored in the index or returned by `rag_status`.

For an OpenAI-compatible embedding service, set `embedding.type` to `openai`, `embedding.baseUrl` to the service's API prefix, and provide its exact model and dimensions. Set `apiKeyEnv` only when authentication is required. The server must accept `POST {baseUrl}/embeddings` with `model` and `input`, and return `data[{index,embedding}]`.

For reranking, set `reranker.type` to `voyage` or `http`. The generic HTTP protocol sends `{model,query,documents,top_n}` to `POST {baseUrl}/rerank` and expects `results[{index,relevance_score}]`. `none` disables reranking.

## Run

```sh
bin/ragd serve --store /absolute/path/to/new-store
bin/ragctl status
bin/ragctl index /absolute/path/to/tei-or-text-directory
bin/ragctl query 'channel estimation'
bin/ragctl refresh
bin/ragctl rebuild
bin/ragctl list
```

`ragd` binds `127.0.0.1:7331` and serves Streamable HTTP MCP at `http://127.0.0.1:7331/mcp`. Clients that only support stdio can launch `bin/ragd stdio --endpoint http://127.0.0.1:7331/mcp`; this forwards calls to the same service and does not open a second SQLite writer. Pass CLI flags before the subcommand, for example `bin/ragctl --mode bm25 query text`. Logs go to stderr; CLI results are JSON on stdout.

The MCP tools are `rag_query`, `rag_index`, `rag_status`, `rag_refresh`, `rag_list_documents`, `rag_rebuild`, and `rag_clear`. `rag_clear` requires `confirm=true`; the CLI equivalent is `bin/ragctl --confirm clear`. The clear operation publishes an empty generation and retains older generations and tracked paths.

Indexing a directory adds it to the tracked paths. `refresh` rescans those paths and removes deleted files only after a complete, successful scan. `rebuild` creates a staging generation and publishes it only when all files succeed and every chunk has a vector. Store configuration and tracked paths are separate: `state.json` holds tracked paths. A failed rebuild leaves the active index in place.

The Go version indexes `.tei.xml` files directly. To convert scholarly PDFs, use the TypeScript repository's existing GROBID `prepare:tei` command, then index only the TEI output directory. Direct PDF/OCR, DOCX, HTML conversion and Pi automatic context injection are not yet implemented in Go. Ordinary `.xml` files follow the text path.

## Read an existing TypeScript index

```sh
bin/ragd serve --store /absolute/path/to/old-pi-rag-store --legacy-readonly
bin/ragctl --mode bm25 query 'channel estimation'
```

Normal Go mode refuses an existing database without the Go storage marker, so pass `--legacy-readonly` when pointing at a TypeScript store. The legacy reader resolves the old `active.json`, opens its SQLite database with `mode=ro` and `query_only`, and performs no schema migrations or index writes. Old MiniLM vectors cannot be queried with a different embedding model; use explicit BM25 or build a separate Go store. For a Voyage legacy index, the saved `config.json` and optional `provider.json` must resolve to the exact indexed provider ID, model, dimensions, adapter, and endpoint before hybrid search is allowed. SQLite WAL read connections can create `-wal` and `-shm` sidecar files in the old directory; the main database is not written. Use a copied, quiescent store if the old directory itself must remain byte-for-byte untouched.

## Go API

`pkg/rag` exposes `Open(Options)`, `Core.Index`, `Query`, `Refresh`, `Rebuild`, `Status`, `ListDocuments`, `Clear`, and `Close`. Options supply the store directory and optional model providers. Core methods accept `context.Context`; results contain chunk text, scores and source positions, without Pi-specific prompt formatting.
