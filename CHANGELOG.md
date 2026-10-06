# Changelog

## v0.2.0 (unreleased)

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
- Native archives, installer and Homebrew Formula now install only `rag`. See [migration](MIGRATION.md).

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
