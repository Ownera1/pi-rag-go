# rag-go

Go RAG Core for a shared local knowledge store. It parses GROBID TEI, JATS XML, Markdown, DOCX, HTML, MinerU results, plain text, and source/configuration files; indexes with SQLite FTS5 and sqlite-vec; and serves structured results over MCP. `ragprep` converts PDFs into canonical document packages independently of indexing. The existing TypeScript `XML_parse` implementation is the migration reference.

## Install and start

Install precompiled macOS/Linux binaries for arm64 and amd64. On macOS, Homebrew installs all five commands:

```sh
brew install --formula ownera1/tap/rag-go
rag init
rag service install
rag connect claude
rag connect codex
rag add ~/Papers
rag query 'channel estimation'
rag status
```

`rag init` prompts for the PDF backend and provider credential in a terminal. The default embedding provider is Voyage (`voyage-4-lite`, 1024 dimensions). For `pdftotext`, install Poppler first (`brew install poppler` on macOS, `sudo apt install poppler-utils` on Debian/Ubuntu). GROBID requires a running HTTP service; MinerU requires its separately installed runtime/models. The initializer checks the selected converter and probes the embedding endpoint. `--offline` skips only the embedding probe and reports that it was not checked.

The script installer needs curl, tar and SHA-256 tools, without Go or a C compiler. Download and run a released installer or the repository script:

```sh
curl -fsSL https://raw.githubusercontent.com/Ownera1/rag-go/main/scripts/install.sh -o /tmp/rag-go-install.sh
sh /tmp/rag-go-install.sh                    # latest stable release
sh /tmp/rag-go-install.sh --version vX.Y.Z  # a specific published release
```

It installs all five commands to `~/.local/bin`; add that directory to PATH. Upgrade with `brew upgrade --formula ownera1/tap/rag-go` or rerun the installer, then `rag service restart`. Uninstall the service with `rag service uninstall` before removing the binaries. The installer verifies checksums before replacing existing commands. Linux archives use glibc (built on Ubuntu 22.04); Alpine/musl and Windows packages are outside this release.

The Homebrew Formula installs the precompiled CLI archive directly. These executables are ad-hoc signed, without Apple notarization; macOS quarantine can prevent the legacy Cask from running. If you installed the Cask, uninstall it first with `brew uninstall --cask ownera1/tap/rag-go`, then install the Formula above. The script installer is also supported.

## Initialize and configure

The default store is `~/Library/Application Support/rag-go` on macOS and `${XDG_DATA_HOME:-~/.local/share}/rag-go` on Linux. `--store` overrides it before or after the subcommand. One user service owns one store; the default HTTP MCP endpoint is `http://127.0.0.1:7331/mcp`.

```sh
rag init --store /absolute/new-store --pdf-backend pdftotext
# OpenAI-compatible endpoint: provide the exact model and dimensions.
rag init --store /absolute/local-store --embedding-type openai \
  --base-url http://127.0.0.1:11434/v1 --model YOUR_MODEL --dimensions YOUR_DIMENSIONS \
  --api-key-env= --pdf-backend pdftotext
# Configure a scholarly PDF service:
rag init --store /absolute/new-store --pdf-backend grobid --pdf-url http://127.0.0.1:8070
```

An environment variable named by `apiKeyEnv` supplies the provider credential. Initialization saves available credentials, or prompts with hidden input, to `<store>/credentials.json` with permission `0600`; the directory is created with permission `0700`. The daemon reads that file and fills only unset environment variables. Secrets stay out of `config.json`, the database and status output. Set each credential environment variable before a noninteractive initialization. Repeating initialization preserves current provider settings and diagnoses them; malformed configs are rejected without overwrite. Explicit `--pdf-backend` updates the conversion configuration and `--enable-auto-refresh` enables watching for an existing store. Restart the service after configuration changes.

Provider, reranking, chunking and retrieval settings remain in `<store>/config.json`; `config.example.json` documents their defaults. `rag serve --config /absolute/config.json` can load another config. OpenAI-compatible embedding endpoints must accept `POST {baseUrl}/embeddings` with `model` and `input`, and return `data[{index,embedding}]`. For reranking, configure `voyage` or `http`; the generic protocol sends `{model,query,documents,top_n}` to `POST {baseUrl}/rerank` and expects `results[{index,relevance_score}]`. `none` disables reranking.

## Service, Agents and automatic ingestion

`rag service install` enables login-session startup, starts the service and waits for MCP readiness. macOS uses `launchctl` and a LaunchAgent; Linux uses `systemctl --user`. Linux without linger runs for the user session; `rag service status` reports that lifetime. Service commands are `install`, `start`, `stop`, `restart`, `status`, and `uninstall`. macOS logs are under `<store>/logs/`; Linux logs are available with `journalctl --user -u rag-go.service`. Uninstallation preserves the knowledge store.

Agent registration calls the official `claude mcp` or `codex mcp` CLI and does not edit their files directly. Claude registrations use user scope. An identical `rag-go` registration is retained; a conflicting entry requires `rag connect TARGET --replace`. Reload the Agent after registration. `rag connect TARGET --endpoint URL` registers an explicit loopback MCP URL. Registration and service readiness are separate checks.

```sh
rag serve                         # foreground alternative to service install
rag stdio                         # forward stdio clients to the same HTTP daemon
rag add /absolute/papers           # first index and persist tracking
rag query '信道估计' --mode bm25
rag refresh                       # immediate manual refresh
rag rebuild
rag remove /absolute/papers        # untrack and remove exclusive index entries
```

Newly initialized stores enable recursive watching with 3-second debounce, startup refresh and a 5-minute reconciliation scan. File events arriving during a refresh are merged into a subsequent pass. Transient conversion/indexing failures retry from 30 seconds up to 5 minutes; stable deterministic failures remain visible until a file changes or a manual refresh occurs. `rag status` includes `autoRefresh`, persisted `failedFiles`, and current `progress`. `pending` counts coalesced refresh passes, not individual files. Changing tracked roots updates watcher subscriptions automatically.

PDF conversion reuses the selected backend and publishes one canonical package in `<store>/prepared/`. Unchanged input and conversion settings reuse the package; unchanged document hashes skip embedding. Explicit canonical packages in the source scan take precedence over matching PDFs. Conversion failures retain the old index and never switch engines automatically. Original-to-canonical mappings are persisted in `state.json`; deleting the original removes its index entry after a complete successful refresh, even though cached artifacts remain. `remove` accepts exact registered roots and preserves entries still covered by another tracked root. Source files and cached conversions are retained. The store directory is excluded from source scanning and watching.

Existing configs without runtime settings retain manual refresh behavior. The runtime configuration is independent of processing/embedding fingerprints:

```json
"runtime": {
  "listen": "127.0.0.1:7331",
  "pdf": {"backend": "pdftotext", "command": "/absolute/bin/pdftotext", "timeoutMs": 600000},
  "autoRefresh": {"enabled": true, "debounceMs": 3000, "rescanMs": 300000}
}
```

The old `ragd`, `ragctl`, `ragprep`, and `rageval` commands remain available and share command implementations with `rag`. Legacy `ragctl` flags precede the subcommand; `rag` also accepts flags after it. Results use JSON on stdout; diagnostics and prompts use stderr. Partial indexing failures print the report and return nonzero.

## Build from source

Requires Go 1.25+, CGO and a C compiler. On macOS, Xcode command line tools supply the compiler.

```sh
go test -race -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 -o bin/rag ./cmd/rag
go build -tags sqlite_fts5 -o bin/ragd ./cmd/ragd
go build -tags sqlite_fts5 -o bin/ragctl ./cmd/ragctl
go build -tags sqlite_fts5 -o bin/ragprep ./cmd/ragprep
go build -tags sqlite_fts5 -o bin/rageval ./cmd/rageval
```

To produce a native archive and exercise extracted binaries:

```sh
./scripts/package.sh v0.0.0-local dist
mkdir -p /tmp/rag-go-extracted
tar -xzf dist/rag-go_v0.0.0-local_$(go env GOOS)_$(go env GOARCH).tar.gz -C /tmp/rag-go-extracted
python3 scripts/smoke-release.py /tmp/rag-go-extracted
python3 scripts/test-release.py
```

Native service acceptance is opt-in: set `RAG_TEST_BINARY` to an absolute built `rag` path and run `go test -tags sqlite_fts5 -count=1 -run '^TestRealUserServiceLifecycle$' -v ./internal/command`. On macOS also set `RAG_TEST_LAUNCHAGENT=1` from a GUI login session; the test uses a temporary plist and store, then unloads the job. On a disposable Linux user session set `RAG_TEST_SYSTEMD=1`; it temporarily installs a user unit, then removes it. Existing rag-go services are preserved by skipping this test. The release matrix runs the Linux test on both architectures.

Tag-triggered releases build and test all four native platforms, verify downloaded release checksums, publish the release, then update the precompiled Homebrew Formula and compatibility Cask. Publication uses the repository's automatic `GITHUB_TOKEN`. Tap updates use a write-enabled SSH deploy key on `Ownera1/homebrew-tap`; its private key is the `HOMEBREW_TAP_SSH_KEY` Actions secret in this repository. The Tap must have an initial default-branch commit. Pull requests run archive smoke tests without publishing. Live provider quality, actual GROBID/MinerU extraction, login/reboot behavior, and Agent tool use require separate acceptance evidence from unit/protocol tests.

The MCP tools are `rag_query`, `rag_index`, `rag_status`, `rag_refresh`, `rag_list_documents`, `rag_rebuild`, `rag_clear`, `rag_cleanup`, and `rag_remove`. `rag_clear` requires `confirm=true`; the CLI equivalent is `bin/ragctl --confirm clear`. The clear operation publishes an empty generation and retains older generations and tracked paths.

Indexing a directory adds it to the tracked paths. `refresh` rescans those paths and removes deleted files only after a complete, successful scan. `rebuild` creates a staging generation and publishes it only when all files succeed and every chunk has a vector. Store configuration and mutable indexing state are separate: `state.json` holds tracked paths and structured failed-file records. A successfully scanned root remains tracked even when some of its files fail; fixing those files and running `refresh` retries them, including after a server restart. Scan failures preserve previous failure records and prevent pruning. Partial `index`/`refresh` results retain successful writes, include `failures[{path,stage,error}]`, and cause `ragctl` to exit nonzero after printing the result. A failed rebuild leaves the active index in place.

The indexer accepts these formats directly and chooses their parser from the extension or XML root:

| Input | Parsed content | Source positions |
| --- | --- | --- |
| `.tei.xml`, TEI XML | Abstract, body and appendix; excludes formula, figure, table, note and references | Physical PDF pages from GROBID `coords`; no XML line numbers |
| `.xml`/`.nxml` with `article` root | JATS abstract and nested body sections; excludes references and visual/formula subtrees | Unknown pages and lines |
| `.md`/`.mdx` | CommonMark AST headings including Setext; raw text retained; fenced code headings stay in code | Original Markdown lines |
| `.docx` | WordprocessingML paragraphs, headings, list text and table cell text; hidden/deleted runs excluded | Unknown pages and lines; Word layout is not rendered |
| `.html`/`.htm` | DOM text, heading paths, main/article/body selection; navigation, scripts and hidden content excluded | Unknown pages and lines |
| MinerU JSON | Body text, heading paths and text lists from supported exports | Explicit zero-based `page_idx` converted to one-based PDF pages |
| `.rag-blocks.json` | Version 1 normalized body blocks | Validated explicit page ranges |

Other XML remains UTF-8 text. MDX is parsed as CommonMark; JSX is retained as text without execution. Generic HTML filtering is a baseline for saved pages, not a trained article extraction model. Word tables are included as paragraph text without cell relationships. Formula, table and figure retrieval are outside the scholarly body-retrieval scope. See [document ingestion](docs/document-ingestion.md) for preprocessing commands, directory rules and parser limitations.

```sh
# Ordinary text PDF: requires Poppler's pdftotext on PATH.
bin/ragprep convert --backend pdftotext --input /absolute/pdfs --output /absolute/converted
# Scholarly PDF: requires a running GROBID service.
bin/ragprep convert --backend grobid --url http://localhost:8070 --input /absolute/paper.pdf --output /absolute/converted
# OCR/layout: requires MinerU 4's mineru-kit and its models/runtime.
bin/ragprep convert --backend mineru --input /absolute/paper.pdf --output /absolute/converted
bin/ragctl index /absolute/converted
```

Without a configured daemon PDF backend, directly indexing an unconverted PDF reports a parse failure with preprocessing instructions. Conversion never opens the store or calls an embedding provider. Pi automatic context injection is not implemented in Go.

## Retrieval and processing settings

Queries accept `hybrid` (default), `bm25`, or `vector`. `--no-rerank` disables the configured reranker for a baseline comparison. Hybrid search falls back to BM25 for transient query-embedding failures, including an HTTP client timeout, and reports `method` and `degraded`. Caller cancellation/deadline stops the operation; vector-only queries return embedding failures instead of falling back. `elapsedMs` and `usage` expose query latency, logical provider call counts, and heuristic token estimates; HTTP retries are excluded from the counts.

Chinese BM25 queries use an additional FTS5 index containing Han unigrams and bigrams. For example, `bin/ragctl --mode bm25 query 信道估计` can match that phrase within continuous Chinese prose. Mixed Chinese/English queries require all generated terms. Ngrams improve keyword recall without linguistic segmentation; they do not guarantee that the entire phrase occurs contiguously. Stored chunk text and source positions stay unchanged. The additional index uses more disk space.

`config.example.json` shows chunking thresholds, parse workers, semantic workers, and embedding batch size. Counts use the existing heuristic token estimate, not the provider's exact tokenizer. Defaults are retained for omitted JSON fields; validation rejects invalid ordering, overlap, and concurrency. Semantic chunks preserve document, page, and heading boundaries. Concurrent indexing and status calls use separate snapshots for mutable state; changes to a returned `Config()` value do not change the Core's configuration.

**Existing Go indexes require `ragctl rebuild` after this upgrade.** The processing fingerprint includes parser version `document-blocks-v3`, the Chinese search index, and chunking parameters. Status reports `needsRebuild`; querying or incrementally mixing incompatible generations is rejected. Changing processing or embedding settings also requires a rebuild. Rebuild must succeed before the active generation changes. Legacy TypeScript stores remain read-only and do not acquire new schema tables.

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

GitHub Actions runs race tests, vet, formatting, and command builds on Ubuntu/macOS with the minimum Go version and stable Go. It installs Poppler to test a real PDF with a blank page between two text pages. GROBID HTTP and MinerU CLI tests use deterministic local protocol fixtures; they do not measure real engine extraction quality. The smoke evaluation needs no provider credentials. Workflow action versions follow the official [checkout](https://github.com/actions/checkout) and [setup-go](https://github.com/actions/setup-go) documentation.

## Read an existing TypeScript index

```sh
bin/ragd serve --store /absolute/path/to/old-pi-rag-store --legacy-readonly
bin/ragctl --mode bm25 query 'channel estimation'
```

Normal Go mode refuses an existing database without the Go storage marker, so pass `--legacy-readonly` when pointing at a TypeScript store. The legacy reader resolves the old `active.json`, opens its SQLite database with `mode=ro` and `query_only`, and performs no schema migrations or index writes. Old MiniLM vectors cannot be queried with a different embedding model; use explicit BM25 or build a separate Go store. For a Voyage legacy index, the saved `config.json` and optional `provider.json` must resolve to the exact indexed provider ID, model, dimensions, adapter, and endpoint before hybrid search is allowed. SQLite WAL read connections can create `-wal` and `-shm` sidecar files in the old directory; the main database is not written. Use a copied, quiescent store if the old directory itself must remain byte-for-byte untouched.

## Go API

`pkg/rag` exposes `Open(Options)`, `Core.Index`, `Query`, `Refresh`, `Rebuild`, `Status`, `ListDocuments`, `Clear`, `Cleanup`, `Remove`, and `Close`. Options supply the store directory and optional model providers. An optional `Options.SourcePreparer` resolves original paths to canonical artifacts without introducing conversion dependencies into Core. Core methods accept `context.Context`; results contain chunk text, scores and source positions, without Pi-specific prompt formatting.
