# pi-rag-go

Go RAG Core for a shared local knowledge store. It parses GROBID TEI, Markdown, plain text, and source/configuration files; indexes with SQLite FTS5 and sqlite-vec; and serves structured results over MCP. The existing TypeScript `XML_parse` implementation is the migration reference.

## Build

Requires Go 1.25+, CGO and a C compiler. On macOS, Xcode command line tools satisfy the compiler requirement.

```sh
go test -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 -o bin/ragd ./cmd/ragd
go build -tags sqlite_fts5 -o bin/ragctl ./cmd/ragctl
go build -tags sqlite_fts5 -o bin/rageval ./cmd/rageval
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

The MCP tools are `rag_query`, `rag_index`, `rag_status`, `rag_refresh`, `rag_list_documents`, `rag_rebuild`, `rag_clear`, and `rag_cleanup`. `rag_clear` requires `confirm=true`; the CLI equivalent is `bin/ragctl --confirm clear`. The clear operation publishes an empty generation and retains older generations and tracked paths.

Indexing a directory adds it to the tracked paths. `refresh` rescans those paths and removes deleted files only after a complete, successful scan. `rebuild` creates a staging generation and publishes it only when all files succeed and every chunk has a vector. Store configuration and mutable indexing state are separate: `state.json` holds tracked paths and structured failed-file records. A successfully scanned root remains tracked even when some of its files fail; fixing those files and running `refresh` retries them, including after a server restart. Scan failures preserve previous failure records and prevent pruning. Partial `index`/`refresh` results retain successful writes, include `failures[{path,stage,error}]`, and cause `ragctl` to exit nonzero after printing the result. A failed rebuild leaves the active index in place.

The Go version indexes `.tei.xml` files directly. To convert scholarly PDFs, use the TypeScript repository's existing GROBID `prepare:tei` command, then index only the TEI output directory. Direct PDF/OCR, DOCX, HTML conversion and Pi automatic context injection are not yet implemented in Go. Ordinary `.xml` files follow the text path.

## Retrieval and processing settings

Queries accept `hybrid` (default), `bm25`, or `vector`. `--no-rerank` disables the configured reranker for a baseline comparison. Hybrid search falls back to BM25 for transient query-embedding failures, including an HTTP client timeout, and reports `method` and `degraded`. Caller cancellation/deadline stops the operation; vector-only queries return embedding failures instead of falling back. `elapsedMs` and `usage` expose query latency, logical provider call counts, and heuristic token estimates; HTTP retries are excluded from the counts.

Chinese BM25 queries use an additional FTS5 index containing Han unigrams and bigrams. For example, `bin/ragctl --mode bm25 query 信道估计` can match that phrase within continuous Chinese prose. Mixed Chinese/English queries require all generated terms. Ngrams improve keyword recall without linguistic segmentation; they do not guarantee that the entire phrase occurs contiguously. Stored chunk text and source positions stay unchanged. The additional index uses more disk space.

`config.example.json` shows chunking thresholds, parse workers, semantic workers, and embedding batch size. Counts use the existing heuristic token estimate, not the provider's exact tokenizer. Defaults are retained for omitted JSON fields; validation rejects invalid ordering, overlap, and concurrency. Semantic chunks preserve document, page, and heading boundaries. Concurrent indexing and status calls use separate snapshots for mutable state; changes to a returned `Config()` value do not change the Core's configuration.

**Existing Go indexes require `ragctl rebuild` after this upgrade.** The processing fingerprint now includes the corrected TEI parser, Chinese search index, and chunking parameters. Status reports `needsRebuild`; querying or incrementally mixing incompatible generations is rejected. Changing processing or embedding settings also requires a rebuild. Rebuild must succeed before the active generation changes. Legacy TypeScript stores remain read-only and do not acquire the new Chinese index.

## Progress and generation cleanup

Run `bin/ragctl status` from a second terminal during indexing. `progress` reports the operation, phase, processed/indexed/skipped/failed counts and current file; it resets on server restart. `failedFiles` is persisted separately and records the latest failures for paths covered by each scan. A successful refresh clears repaired or removed failures. Server shutdown cancels active request contexts.

```sh
bin/ragctl --keep 3 cleanup
bin/ragctl --keep 3 --confirm cleanup
```

Cleanup previews by default; `--confirm` permits deletion and `--dry-run` forces a preview. It always retains the active database and the newest inactive generations up to the requested total. It only removes recognized Go generation directories under `indexes/`, verifies the Go storage marker, and skips symlinks, unrecognized names, and directories containing extra files. The initial root `rag.db`, staging directories, and unknown files are not cleanup targets. `removed` lists candidates in a preview. Reading old WAL databases can create SQLite sidecars even during a preview.

## Retrieval evaluation

`rageval` reads annotated JSONL questions and calls the running service over MCP. It reports Recall@K, MRR, p50/p95 latency, failures, degradation, usage, and optionally estimated query cost for each mode. Failed queries count as zero in aggregate retrieval metrics; the command still writes its report and exits nonzero when queries fail. Reports include per-question hits for inspection.

```sh
bin/ragctl index evaluation/sample/corpus
bin/rageval --dataset evaluation/sample/questions.jsonl --modes bm25,vector,hybrid --output evaluation/runs/sample.json
# Add rerank when a reranker is configured:
bin/rageval --dataset /absolute/path/to/annotated-paper-questions.jsonl --modes bm25,vector,hybrid,rerank --output evaluation/runs/papers.json
```

The included 20 questions and short TEI passages are a **synthetic smoke dataset**, not a real-paper benchmark. The automated end-to-end test uses deterministic HTTP model stubs and proves wiring and scoring only. See [evaluation/README.md](evaluation/README.md) for passage labels, real-paper evaluation steps, and cost assumptions. Cloud-provider quality, billing, and latency must be measured with your actual provider and corpus.

GitHub Actions runs race tests, vet, formatting, and command builds on Ubuntu/macOS with the minimum Go version and stable Go. The smoke evaluation is part of these tests and needs no provider credentials. Workflow action versions follow the official [checkout](https://github.com/actions/checkout) and [setup-go](https://github.com/actions/setup-go) documentation.

## Read an existing TypeScript index

```sh
bin/ragd serve --store /absolute/path/to/old-pi-rag-store --legacy-readonly
bin/ragctl --mode bm25 query 'channel estimation'
```

Normal Go mode refuses an existing database without the Go storage marker, so pass `--legacy-readonly` when pointing at a TypeScript store. The legacy reader resolves the old `active.json`, opens its SQLite database with `mode=ro` and `query_only`, and performs no schema migrations or index writes. Old MiniLM vectors cannot be queried with a different embedding model; use explicit BM25 or build a separate Go store. For a Voyage legacy index, the saved `config.json` and optional `provider.json` must resolve to the exact indexed provider ID, model, dimensions, adapter, and endpoint before hybrid search is allowed. SQLite WAL read connections can create `-wal` and `-shm` sidecar files in the old directory; the main database is not written. Use a copied, quiescent store if the old directory itself must remain byte-for-byte untouched.

## Go API

`pkg/rag` exposes `Open(Options)`, `Core.Index`, `Query`, `Refresh`, `Rebuild`, `Status`, `ListDocuments`, `Clear`, `Cleanup`, and `Close`. Options supply the store directory and optional model providers. Core methods accept `context.Context`; results contain chunk text, scores and source positions, without Pi-specific prompt formatting.
