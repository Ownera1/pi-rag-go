# rag-go

[English](README.md) | [简体中文](README.zh-CN.md)

A workspace-scoped local retrieval engine for Agents. Documents are loaded into canonical body blocks, chunked, embedded, and searched with SQLite FTS5 + sqlite-vec. Each workspace owns its configuration and index. Multiple Agent processes share it through operation-scoped locks.

PDF extraction, OCR and layout recovery happen upstream, using tools such as MinerU Desktop. rag-go reads their Markdown or structured JSON output and keeps source provenance when it is available.

## Quick start

See [migration](MIGRATION.md) before upgrading an existing v0.1 installation.

Install a published native macOS/Linux binary with Homebrew. Install once, then initialize each project:

```sh
brew install --formula ownera1/tap/rag-go
rag install    # once: embedding provider, hidden API key, installed agents
cd my-project
rag init       # no prompts; indexes ./documents when it has files
```

Restart the agents after `rag install`. The agent then searches whichever project it runs in, and `rag query 'channel estimation'` works the same way from the shell. Queries synchronize changed documents automatically, so there is no separate indexing step.

`rag install` saves user-wide defaults to `~/.config/rag-go/config.json` and the API key to `credentials.json` beside it (mode `0600`; `XDG_CONFIG_HOME` or `RAG_GO_CONFIG_DIR` relocates both). It verifies the embedding endpoint, then registers one workspace-agnostic MCP server, `rag mcp`, with Claude Code at user scope and with Codex in `~/.codex/config.toml`, editing only the `[mcp_servers.rag-go]` table so other settings and comments survive. Claude Desktop, Antigravity (the IDE and `agy` CLI share `~/.gemini/config/mcp_config.json`) and pi (`~/.pi/agent/mcp.json`) get only `mcpServers.rag-go` edited in their JSON. Detection checks for each CLI or configuration directory, and in a terminal a checklist starts with the detected agents ticked; `--agents claude,codex,claude-desktop,antigravity,pi` or `--agents none` overrides it. Claude Desktop chats have no project directory, so name the workspace path when asking. Re-running is safe; `rag uninstall [--purge]` reverses it. Voyage defaults remain `voyage-4-lite`, 1024 dimensions. OpenAI-compatible endpoints require the correct model, URL and dimensions:

```sh
rag install --embedding-type openai --model YOUR_MODEL --dimensions YOUR_DIMENSIONS \
  --base-url http://127.0.0.1:11434/v1 --api-key-env=
```

`rag init [workspace] --docs PATH` defaults to the current directory and `./documents`. A new workspace copies the installed defaults, so later global changes never invalidate its index. An explicit external documents directory is supported, including an existing directory containing one MinerU output folder per paper:

```sh
rag init ~/Projects/my-project --docs /absolute/converted-papers
```

Without `rag install`, `rag init` asks for the documents directory, embedding provider/model and hidden API key, and stores the key in the workspace; it accepts the same provider options as `rag install`. Credentials resolve from the environment, then the workspace, then the user-wide file. The environment and the user-wide file serve only Voyage's default endpoint or the one `rag install` recorded, so a workspace config from a cloned repository cannot redirect them; any other endpoint uses the key `rag init` stored in its workspace. `--no-sync` skips initial indexing; `--offline` skips the live embedding probe and indexing. Repeating initialization preserves settings except explicitly supplied options; damaged configuration is rejected. A documents-root, embedding or chunking change requires `rag rebuild`.

## Workspace and synchronization

```text
my-project/
├── documents/
└── .rag-go/
    ├── config.json
    ├── credentials.json        # optional, private
    ├── state.json
    ├── catalog.db              # optional, durable Zotero metadata and links
    ├── .lock
    ├── rag.db                  # created on first sync
    ├── active.json             # published by rebuild
    └── indexes/                # retained generations
```

Commands discover the nearest `.rag-go/config.json` by walking up from the current directory. `--workspace PATH` selects exactly that root; it never falls back to a parent or global store. Nested workspaces remain independent. The `.rag-go` directory and credentials are excluded from Git through its own ignore file. Project documents remain source files managed by the user.

`rag sync` recursively scans the single documents root and hashes canonical content, including manifest metadata. Unchanged documents skip parsing and embedding. Successful replacements are transactional; failed documents retain their previous chunks. Deletion is applied only after a complete, successful and stable scan. A file changing during processing remains pending for another sync.

Queries automatically check for changes and perform at most one sync pass. `rag query --no-sync` searches the existing index without writing. Results include `freshness` (`fresh`, `stale`, or `unknown`), an optional sync report and `syncError`. A sync failure can return an existing compatible index with its failure stated. Failed first indexing, incompatible indexes and caller cancellation return errors. Identical failed inputs have a 60-second automatic retry cooldown, persisted across process restarts; changed inputs and manual `rag sync` retry immediately. There is no watcher or background service.

Every operation reloads configuration, state and the active database. Reads hold shared process locks; sync, rebuild, initialization and cleanup hold exclusive locks. Idle MCP processes retain no database handle or lock. Readers can run together; writes wait for active readers, with cancellable lock acquisition.

`rag rebuild` stages a new database and publishes it atomically only after a successful stable scan and complete vector coverage. Failed rebuilds keep the active index. `rag clean --keep 3` previews inactive generation cleanup; `--confirm` permits deletion and `--dry-run` forces a preview. Active generations, unknown files and symlinks are retained. SQLite read connections can create WAL sidecar files; read-only means no content/index/state mutation.

## Document formats and provenance

| Input | Canonical body content | Locations |
| --- | --- | --- |
| Markdown / MDX | CommonMark headings, paragraphs and code | Markdown lines |
| TEI XML | Abstract, body and appendix; visual/formula/reference subtrees excluded | Physical PDF pages from explicit GROBID coordinates |
| JATS XML | Abstract and nested body sections | Unknown pages |
| MinerU JSON | Supported body-text, heading and list exports | Explicit `page_idx` converted from zero-based to one-based |
| `.rag-blocks.json` | Version 1 normalized body blocks | Explicit validated page ranges |
| DOCX | Visible paragraphs, headings, lists and table-cell text | Unknown pages |
| HTML | Main/article/body text with baseline hidden/navigation filtering | Unknown pages |
| TXT, code and other supported UTF-8 text | Plain text | Text lines |

One document folder produces one canonical representation. Existing `rag-source.json` manifests take precedence; recognizable MinerU folders select one JSON representation and exclude Markdown, metadata and other companions. Ambiguous multi-paper MinerU directories fail with instructions to split them into separate folders. Ordinary files outside such packages remain independent documents. A package covers its subfolders, so a package file directly in the documents root fails the scan instead of hiding every other document. PDF and other unsupported assets are ignored during scanning.

To correct OCR errors without editing MinerU output, put a `rag-fixes.tsv` in the package folder: one `wrong<TAB>right` pair per line (`#` starts a comment), applied in order to the parsed text. Copy the wrong text from query results; LaTeX backslashes are written as-is. A pair that matches nothing fails the document with its line number, so typos and fixes made stale by a MinerU rerun surface. Editing the file triggers reindexing.

An optional source manifest uses the existing format:

```json
{
  "version": 1,
  "format": "markdown",
  "contentPath": "content.md",
  "sourcePath": "original.pdf",
  "title": "Research Paper"
}
```

`contentPath` stays within the package, including after symlink resolution. `sourcePath` is attribution only; rag-go does not parse or require the original PDF. Markdown alone does not recover PDF pages. Unknown locations stay unset. Formula, figure, table-relationship and image retrieval remain outside the scholarly body-retrieval scope. The parser input bound is 64 MiB; supported oversized inputs are reported rather than silently pruned.

## MCP and Agent connections

```sh
rag mcp                                      # local stdio, eight tools, workspace per call
rag mcp --workspace /absolute/project        # stdio pinned to one workspace
rag mcp --read-only                          # stdio, three read tools
rag mcp --transport http --listen 127.0.0.1:7331  # optional foreground, read-only
```

Local tools are `rag_query`, `rag_status`, `rag_list_documents`, `rag_sync`, `rag_rebuild`, `rag_zotero_sync`, `rag_zotero_match` and `rag_zotero_link`. Query arguments retain `query`, `mode`, `top_k`, `candidate_top_k`, `alpha`, `disable_rerank`, and `require_rerank`; `disable_sync=true` searches without synchronization. Optional `filter` applies cached Zotero metadata before recall. Local auto-sync queries are not marked read-only.

Without `--workspace`, stdio `rag mcp` starts in any directory and resolves the workspace on every call: from the optional `workspace` tool argument (a project directory or any directory inside it), otherwise from the agent's working directory. A call outside any workspace reports how to initialize one. Servers started with `--workspace`, and all HTTP servers, serve exactly one workspace and reject others.

Read-only stdio and all HTTP servers expose only query/status/list, reject write tools and disable auto-sync. Queries can still call the configured query embedding/reranker. HTTP listens only on loopback, retains Host/Origin checks and prints its URL to stderr. Its default port is ephemeral. A tunnel may launch `rag mcp --read-only --workspace /absolute/project` or connect to the optional HTTP server; tunnel installation and account configuration are external to this repository.

`rag install` is the usual way to connect agents. `rag connect claude|codex` instead registers a single project: `rag connect claude` calls the Claude CLI with local scope from the workspace directory. `rag connect codex` merges the project's `.codex/config.toml` using TOML parsing, retaining unrelated settings and other servers. TOML comments/formatting may be normalized. Both pin the absolute binary and workspace. Repeated identical registrations are retained; conflicting project entries require `--replace`. Codex loads project configuration only for trusted projects. Reload the Agent after connecting. Registration tests do not establish actual Agent tool use.

## Configuration and retrieval

Optional Zotero Local API integration uses a persistent `catalog.db`, independent of rebuildable index generations. Full metadata snapshots, exact attachment path or filename (MinerU Desktop `<file>-<uuid>` folders)/unique DOI matches, locked manual links, portable Manifest references, and year/tag/collection prefilters are available through `rag zotero` and `rag query`. Metadata updates do not re-embed body content. See [Zotero setup, commands and verification](ZOTERO.md).

See [config.example.json](config.example.json). Configuration and indexing state are separate. `documents` is relative to the workspace unless absolute. `excludePatterns` uses the existing Gitignore-style matching. Hidden directories, build/cache directories and `.rag-go` are excluded from source scanning.

For a trusted endpoint, environment credentials take precedence over `.rag-go/credentials.json`; other endpoints receive only workspace credentials. Workspace credentials are passed directly to providers without changing process environment. The store directory uses `0700`; credentials and JSON state use `0600`. Secrets stay out of configuration, indexes and status output.

Embedding supports Voyage and OpenAI-compatible `POST {baseUrl}/embeddings` returning `data[{index,embedding}]`. Optional reranking supports Voyage or generic HTTP `POST {baseUrl}/rerank` with `{model,query,documents,top_n}`, returning `results[{index,relevance_score}]`. `none` disables reranking.

Before chunking, adjacent body blocks sharing a section and page are merged, so paragraph-level exports such as MinerU produce target-sized chunks without widening page ranges. `semantic` chunking (the default) embeds each sentence-level unit to choose boundaries and then embeds the final chunks, so indexing uses roughly twice the embedding tokens of `legacy`. `indexing.embeddingWorkers` (default 4) bounds concurrent document embedding requests.

Queries support `hybrid` (default), `bm25` and `vector`, with `--no-rerank` for baselines. Hybrid falls back to BM25 on transient query embedding failures and reports `method`/`degraded`; vector mode reports the failure. Caller cancellation stops the operation. BM25 matches any query term, and Chinese BM25 retains the Han unigram/bigram index. Hybrid fuses the BM25 and vector rankings with weighted reciprocal rank fusion (k = 60); alpha weights the BM25 ranking and 1 - alpha the vector ranking. Hits report raw BM25 relevance and cosine similarity alongside the fused `hybrid` score. Defaults remain alpha `0.4`, candidate top K `30`, top K `5`, and semantic thresholds `120/280/420/140`. Usage counts are logical query calls and heuristic tokens, excluding retries and sync costs; the sync report separately identifies indexing work.

## Build and verify

Requires Go 1.25+, CGO and a C compiler. No PDF engine or Go toolchain is required by a released binary. Export the SQLite header flags first so sqlite-vec compiles against the SQLite it links, not an older system header:

```sh
export CGO_CFLAGS="$(scripts/cgo-flags.sh)"
go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 -o bin/rag ./cmd/rag
./scripts/package.sh v0.3.0-local dist
mkdir -p /tmp/rag-go-extracted
tar -xzf dist/rag-go_v0.3.0-local_$(go env GOOS)_$(go env GOARCH).tar.gz -C /tmp/rag-go-extracted
python3 scripts/smoke-release.py /tmp/rag-go-extracted
python3 scripts/test-release.py
```

The smoke suite uses an actual extracted binary and deterministic local HTTP providers. It checks standalone queries, automatic sync, FTS5/sqlite-vec, source pages, real stdio and HTTP MCP, rebuild/clean and all four evaluation modes. Multi-process race tests cover shared readers, serialized writers, lock cancellation and duplicate embedding prevention. See [local acceptance and live acceptance boundaries](VALIDATION.md). These establish local behavior, not live provider quality, billing, Claude/Codex acceptance or all-platform release success.

Releases retain native macOS arm64 and Linux arm64/amd64 archives, SHA-256 verification and a precompiled Homebrew Formula. The archive contains only `rag`, LICENSE and README. The script installer installs only `rag`, verifies checksums and preserves an existing binary on verification failure. It does not manage old system services or remove legacy executables.

## Evaluation and Go API

```sh
rag eval --dataset evaluation/sample/questions.jsonl --modes bm25,vector,hybrid \
  --output evaluation/runs/sample.json
```

Evaluation calls Core directly, requires a synchronized compatible index and never auto-syncs during the run. Reports retain Recall@K, MRR, p50/p95 latency, failures, degradation, usage and optional cost estimates. The sample is synthetic; see [evaluation guidance](evaluation/README.md).

`pkg/rag` exposes `Open(Options{WorkspaceDir, ReadOnly, Embedder, Reranker})`, `Core.Sync`, `Query`, `Status`, `ListDocuments`, `Rebuild`, `Cleanup`, `SyncZotero`, `MatchZotero`, `LinkZotero`, `ZoteroStatus`, `ZoteroLinks`, `Close`, and `DefaultConfig`. Methods accept `context.Context`; injected providers must support concurrent calls. Core contains no MCP or external document-extraction dependency.
