# Changelog

## Unreleased

Existing v0.4 indexes report `rebuild required`; run `rag rebuild` once after upgrading.

- Security: `rag init` on an existing workspace config no longer reads an environment variable for an untrusted endpoint, so a cloned `.rag-go/config.json` naming another `baseUrl` and, say, `GITHUB_TOKEN` cannot have that token saved or sent by the probe. A new workspace or an explicit `--base-url` still reads it.
- MinerU text is kept more completely: inline equations (content list v2, middle JSON) as `$…$`, display equations in legacy middle JSON as `$$…$$`, algorithm and code blocks with their captions in their own chunks, and figure and table captions in middle JSON.
- Chunk splits never cut a `$…$` or `$$…$$` formula: length cuts, sentence breaks and legacy overlap move to a formula's edge.
- Chinese sentences split at `。！？` without a following space, so semantic chunking gets sentence boundaries in Chinese text.
- Keyword search indexes each document's path below the documents root instead of its absolute path, so a directory name shared by every document, such as `rag` or `documents`, no longer matches every chunk.
- `require_rerank` fails when the reranker errors instead of silently returning unreranked results; `rag eval --modes rerank` counts those as failures.
- A Zotero reference in a `rag-source.json` manifest follows edits to the manifest; previously the first linked item stuck. Manual links from `rag zotero link` still stay.
- Hard splits of very long unbroken lines, such as minified code, are bounded and no longer take minutes.
- `rag install` no longer panics on an agent config that is `null` or has `"mcpServers": null`.
- `rag clean` refuses a symlinked `.rag-go/staging` instead of following it.

## v0.4.0 (2026-10-07)

Existing v0.3 indexes report `rebuild required`; run `rag rebuild` once after upgrading.

- MinerU page numbers: a paragraph that continues onto the next page now spans both pages. MinerU content lists file it under its first page; a `layout.json` (MinerU Desktop) or `*_middle.json` beside the export marks the moved lines and is now read automatically. Checked against the PDF text layer on six papers, correct chunk page ranges rose from 87.7% to 98.7%.
- Hand-editable Markdown that keeps page numbers: a `"format": "markdown"` manifest may add `"pagesFrom": "layout.json"` (or a content list). Each paragraph of MinerU's `full.md` is located in that export by its text and takes its pages, so hand corrections keep them. A paragraph it cannot find spans its neighbours' pages; an export matching under 70% of paragraphs fails the document.
- Markdown chunking for papers: `$$` display equations get their own chunks, image links are reduced to their alt text and one-line HTML tables to their cell text, and References/Bibliography/参考文献 sections are skipped.
- A stray NUL byte in Markdown, an OCR artefact, no longer rejects the whole file.

## v0.3.4 (2026-10-07)

- Security: environment variables and user-wide credentials are sent only to Voyage's default endpoint or the one `rag install` recorded. A workspace config can no longer redirect them, or any other environment variable, to its own `baseUrl`; other endpoints use the workspace's own `credentials.json`, which `rag init` fills. A workspace on another endpoint that relied on an exported key needs `rag init` once in that workspace.
- A symlinked documents directory is followed instead of scanning as empty, which removed every indexed document.
- A MinerU or `rag-source.json` package file directly in the documents root fails the scan instead of silently reducing the whole tree to one document.
- `.env` files are no longer indexed or sent to the embedding provider.
- Semantic chunking is linear in block length: an 800k-character block chunks in tens of milliseconds instead of seconds, with identical chunks.
- `rag zotero link --write-manifest` works under symlinked ancestors such as macOS `/var` and `/tmp`; only a symlinked manifest itself is refused.
- `rag connect codex` edits only the `[mcp_servers.rag-go]` table, keeping comments and formatting, and writes through a symlinked config. `rag install` also writes through a symlinked `~/.codex/config.toml`.
- The filename boost matches words of the path below the documents root that start with the first query term, so `to` no longer boosts `history` and the absolute path no longer boosts every hit.
- `rag clean` removes staging databases left by an interrupted rebuild. Atomic writes also sync the parent directory.
- Database read errors are reported as such instead of as "not a recognized Go store" or "rebuild required".
- A Zotero attachment whose file URL is not a local `file:` URL is skipped like a missing file instead of failing the whole sync.

## v0.3.3 (2026-10-07)

- Intel Macs are no longer supported: releases, the Homebrew Formula and `install.sh` cover macOS arm64 and Linux arm64/amd64.

## v0.3.2 (2026-10-07)

- In a terminal, `rag install` and `rag init` ask with arrow-key menus, a masked key field and spinners for the embedding probe and each agent registration. `rag install` without `--agents` shows a checklist of agents with the detected ones ticked. Non-interactive runs and the JSON report are unchanged.

## v0.3.1 (2026-10-07)

- `rag install`/`rag uninstall` also register `rag mcp` with Claude Desktop, Antigravity (IDE and `agy` CLI, `~/.gemini/config/mcp_config.json`) and pi (`~/.pi/agent/mcp.json`), editing only `mcpServers.rag-go`. `--agents` accepts `claude,codex,claude-desktop,antigravity,pi`.
- The MCP server reports the build version in `serverInfo` instead of a hard-coded `0.2.0`.

## v0.3.0 (2026-10-06)

Existing v0.2 indexes report `rebuild required`; run `rag rebuild` once after upgrading.

- Chunks are embedded as `title > section` followed by the body, preferring the linked Zotero title, so vectors carry document and section context. Stored content and display are unchanged.
- The keyword index gains a heading column, so section names and Zotero titles are BM25/Chinese-searchable.
- MinerU figure/chart/table captions and footnotes, table cell text and display equations (LaTeX) are indexed in their section. Equations stay in their own chunks instead of merging into prose.
- A per-package `rag-fixes.tsv` of `wrong<TAB>right` pairs corrects OCR errors in parsed MinerU or manifest text without editing MinerU output. Editing it triggers reindexing; a pair that matches nothing fails the document with its line number.
- MinerU Desktop folders (`<source file>-<uuid>`) without a manifest link automatically to the Zotero attachment with exactly that filename.
- `scripts/ab-eval.sh` compares retrieval metrics between a git ref and the working tree.

## v0.2.0 (2026-10-06)

- `rag install` saves user-wide embedding defaults and the API key under `~/.config/rag-go/` after verifying the endpoint, and registers one workspace-agnostic `rag mcp` with Claude Code (user scope) and Codex (`~/.codex/config.toml`, editing only the rag-go table). `rag uninstall [--purge]` reverses it.
- `rag init` copies the installed defaults without prompts and indexes existing documents; `--no-sync` skips indexing. Credentials resolve from the environment, then the workspace, then the user-wide file.
- Stdio `rag mcp` without `--workspace` starts in any directory and resolves the workspace per call from a new optional `workspace` tool argument or the working directory. Pinned and HTTP servers reject other workspaces.
- Optional Zotero Local API metadata catalog with full snapshots, content hashes, normalized creators/tags/collections, soft deletion, and preserved manual orphan links.
- Stable bibliographic document keys, exact attachment/unique DOI matching, portable Manifest references, query metadata, and prefilters for BM25/Chinese/vector recall.
- `rag zotero sync/status/match/link/links` and local writable MCP metadata tools; read-only HTTP/stdio retain query/status/list only.
- BM25 and Chinese BM25 match any query term and rank by shared terms, so natural-language questions recall instead of requiring every word or bigram.
- Hybrid retrieval fuses BM25 and vector rankings with weighted reciprocal rank fusion (k = 60) instead of min-max score normalization, which always discarded each list's weakest candidate. `alpha` keeps its default and now weights rankings. Hit `bm25`/`vector` fields report raw BM25 relevance and cosine similarity rather than min-max values.
- Freshness checks reuse persisted input fingerprints while file size, modification time and inode are unchanged; recently modified files are always rehashed.
- Adjacent body blocks in the same section and page merge before chunking, so paragraph-level exports (MinerU, DOCX, HTML, JATS) no longer produce one chunk per paragraph. This changes the processing fingerprint: existing v0.2 indexes report `rebuild required`.
- Semantic chunking batches sentence-unit embeddings across blocks instead of issuing one request per block.
- Per-document source attribution moves from the per-chunk `chunk_sources` table to the `files` row; readers of older stores still work.
- Replacement lookup for canonical packages uses a sorted prefix range and an ancestor walk instead of comparing every indexed path; chunk inserts use prepared statements.
- Indexing embeds documents concurrently, bounded by the new `indexing.embeddingWorkers` (default 4); SQLite writes remain serialized.

- Workspace-scoped `.rag-go` configuration and indexes, one documents root, direct Core CLI and stdio MCP.
- Query-triggered incremental synchronization with explicit freshness, persisted failures and a 60-second retry cooldown.
- Operation-scoped cross-process reader/writer locks, transactional canonical replacement and atomic rebuild publication.
- Three-tool read-only stdio/HTTP boundary; local MCP also exposes sync and rebuild.
- Project-scoped Claude/Codex registrations and direct workspace credential injection.
- Preserved structured parsers, source pages, Chinese BM25, vector/hybrid/rerank and evaluation.
- Removed PDF conversion, background service/watchers, global store/path registration, TypeScript legacy mode and compatibility binaries.
- Native archives, installer and Homebrew Formula now install only `rag`.

## v0.1.0

The first release provides a local knowledge service with one command, automatic source updates and precompiled installation.

- `rag` unifies initialization, indexing, querying, conversion, evaluation, Agent registration and service management. `ragd`, `ragctl`, `ragprep` and `rageval` remain available.
- `rag init` checks embedding dimensions and the selected PDF backend, stores credentials separately with mode `0600`, and enables automatic refresh for new stores.
- `rag connect claude/codex` registers HTTP MCP through the official clients, with idempotency and explicit replacement of conflicting entries.
- macOS LaunchAgent and Linux user systemd services use absolute paths and load credentials in the background.
- Tracked directories refresh automatically with recursive watching, debounce and retry. PDFs use the configured Poppler, GROBID or MinerU backend and retain source/page provenance.
- Original-to-canonical mappings prevent duplicate PDF ingestion and cached conversions reappearing after source deletion. Failed conversions retain the existing index; removing a tracked root preserves documents covered by other roots.
- macOS/Linux arm64/amd64 archives include all five binaries, built with CGO, SQLite FTS5 and sqlite-vec. The installer verifies SHA-256 checksums; the Homebrew Tap provides a precompiled CLI Formula. The compatibility Cask may be blocked by macOS quarantine.

Existing stores keep manual refresh until explicitly enabled. Legacy TypeScript stores require read-only mode. External embedding services and PDF engines are configured separately; Linux archives require glibc.

Validation includes race tests, vet, native archives on all four platforms, real Poppler PDF ingestion/deletion, macOS/Linux service lifecycle and actual Codex MCP retrieval. GROBID/MinerU tests validate their protocols; real extraction quality, Claude retrieval and login/reboot behavior require separate acceptance.
