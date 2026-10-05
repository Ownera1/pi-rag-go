# v0.2 local acceptance

Verified on 2026-10-06, macOS arm64 with Go 1.27.1, on `codex/workspace-rag-simplification`.

| Area | Verified evidence |
| --- | --- |
| Workspace | Current-directory initialization, upward discovery, explicit roots, nested isolation, external documents, repeat initialization, damaged config and unknown database rejection |
| Incremental sync | Add/change/delete, unchanged inputs with zero embedding, content and manifest changes, canonical artifact switches in both directions, partial failures and inaccessible source retention |
| In-flight inputs | Changes during embedding and canceled synchronization retain the previous document; later manual sync repairs it |
| Credentials | Private credential permissions, concurrent workspace isolation, environment priority and no process environment mutation |
| Processes | Three simultaneous sync processes issue one embedding request; parallel readers share locks; waiting writers cancel; an already-open peer reads the newly published generation after rebuild/clean |
| MCP | Actual extracted binary over stdio and streamable HTTP; five local tools, three read-only tools, rejected writes, no automatic sync in read-only mode, clean protocol stdout and Host/Origin rejection |
| Retrieval | Chinese BM25 and original text, vector/hybrid/rerank wiring, explicit embedding degradation, cancellation, structured document titles/sections/pages and unknown-page preservation |
| Publication/cleanup | Failed rebuild preserves active data; vector coverage checked before publication; generation cleanup previews by default and protects active, unknown and symlink entries |
| Distribution | One native `rag` binary, extracted archive smoke, complete four-platform Formula checksum requirements, installer checksum rejection preserving an existing installation |

Commands passed:

```sh
go test -race -tags sqlite_fts5 -count=1 ./...
go vet -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 -o /tmp/rag-go-v02-rag ./cmd/rag
./scripts/package.sh v0.2.0-local /tmp/rag-go-v02-dist
python3 scripts/smoke-release.py /tmp/rag-go-v02-extracted
python3 scripts/test-release.py
git diff --check
```

`gofmt -l` returned no files. Shell syntax checks passed for both packaging and installation scripts. The package smoke invokes the extracted executable, SQLite FTS5/sqlite-vec and deterministic local HTTP embedding/rerank providers; all four evaluation modes pass its labeled fixture. It does not measure real-paper retrieval quality.

No original stores or source documents were migrated or removed. The sibling `rag-go-sites` workspace retains its original uncommitted files; its read-only MCP restrictions and permission checks have been incorporated here.

## Separate live acceptance

| Check | Status |
| --- | --- |
| Actual Voyage / other model provider | Not run; HTTP fixtures establish integration behavior, not external availability, model quality or billing |
| Actual Claude / Codex tool use | Not run; registration merge/scope/conflict tests establish configuration behavior only |
| Linux and macOS Intel native execution | Not run locally; release CI retains the four native runner matrix |
| Published release / installed Homebrew upgrade | Not published; generated Formula and fixture installer checks only |

For live acceptance, follow [migration](MIGRATION.md) with a chosen workspace/provider, check `rag status`, query known passages, reconnect the Agent and invoke `rag_status` and `rag_query` from that Agent. Record its model, corpus, returned passage and provider request outcome separately before claiming end-to-end acceptance.
