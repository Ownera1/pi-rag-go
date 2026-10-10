# Changelog

## Unreleased

- `rag tui` also offers the models an endpoint lists at `GET {baseUrl}/models`, fetched in the background once per endpoint when the panel opens or an edit points a provider somewhere new, with the same endpoint and credential rules as queries. Names are kept by role, `embed` for embedding and `rerank` for the reranker, and joined to the preset's own models; of each family, the name before its first number, only the two newest versions stay, so `voyage-3-large` and `text-embedding-v2` are not offered while `voyage-4` and `voyage-3.5` or `text-embedding-v4` and `v3` exist. Both DashScope presets read `compatible-mode/v1/models`, which lists the rerankers too though the rerank endpoint has no list; it names only the newest models (`qwen3.7-text-embedding`, `qwen3.7-text-embedding-flash`, `qwen3.7-text-rerank`) and not `text-embedding-v4` or `qwen3-rerank`, which it still serves, so the presets keep their own lists. Voyage has no model list. A custom endpoint whose list arrives gets the same search; one without a list is typed freely as before. While searching, a list still loading or one that failed, such as for a missing key, says so under the matches. With this build, switching a scratch workspace to the DashScope embedding preset and searching `flash` offered `qwen3.7-text-embedding-flash`, which is in neither preset list.

## v0.7.2 (2026-10-10)

No rebuild needed: v0.7.x indexes stay compatible. Qwen rerankers through DashScope, and `rag tui` sets up a provider from a preset, searches its models and checks every edit as it is made.

- `rag tui` has `reranker.provider` and `embedding.provider` settings that pick a known endpoint, Voyage or DashScope (or `none` for the reranker), and set its type, `baseUrl`, `apiKeyEnv` and newest model at once; a DashScope embedding also gets `dimensions` 1024 and lowers `indexing.embeddingBatchSize` to 10, the most `text-embedding-v4` accepts. Switching DashScope's rerank endpoint by hand needed four edits, and a `baseUrl` left on the other endpoint failed only at query time. The model setting then offers the endpoint's two newest generations (`voyage-4*` and `voyage-3.5*`, `rerank-3*` and `rerank-2.5*`, `qwen3.7-text-*` and `text-embedding-v4` / `qwen3-rerank`): the arrow keys cycle them, and `enter` searches them as you type, listing up to five matches under the field, names starting with the text first, then a hyphenated word starting with it, then any containing it; `enter` takes the highlighted match and the typed name stays the last entry, so a model missing from the list can still be set. Editing `type` or `baseUrl` by hand shows the provider as `custom`, whose model is typed as before, and `u` on a provider restores the whole saved endpoint. A provider pointed at an endpoint with no stored key warns that queries will fail until `rag init` stores one. Driven in a pseudo-terminal on a scratch workspace with this build, switching the reranker to dashscope, typing `3-r` and saving wrote `qwen3-rerank` with the DashScope endpoint and key name to `config.json`.
- Reranker type `dashscope` serves Alibaba Cloud Model Studio's Qwen rerankers (`qwen3-rerank`, `qwen3.7-text-rerank`) at `POST {baseUrl}/reranks`, where Model Studio's compatible endpoint lives; it takes the generic HTTP request and returns `results[{index,relevance_score}]`, but the `http` type posts to `/rerank` and got 404. Qwen embeddings already worked through the `openai` type; the README now lists the settings for both. On copies of the real six-paper workspace (183 questions), against voyage-4 with `rerank-3` (rerank Recall@5 / MRR 0.991 / 0.905, vector 0.930 / 0.737): `qwen3-rerank` on the same candidates reached 0.925 / 0.832 and `qwen3.7-text-rerank` 0.969 / 0.869; with `rerank-3`, a `text-embedding-v4` index gave vector 0.775 / 0.576 and rerank 0.960 / 0.873, a `qwen3.7-text-embedding` index vector 0.867 / 0.710 and rerank 0.974 / 0.888. The voyage defaults stay ahead on these papers.

- `rag tui` checks every edit as it is made instead of only on `ctrl+s`. A setting with fixed choices (`reranker.type`, `embedding.type`, `chunking.mode`, `zotero.libraryType`, `zotero.startOnDemand`) cycles with `enter` as with the arrow keys and no longer takes typed text, which saving rejected only afterwards. A typed or stepped value that breaks a limit is refused and the draft keeps its last valid value, so the arrow keys can no longer take `topK` to 0. Configuration errors name the setting and the bound: `invalid chunking thresholds` is now, for example, `chunking.semanticTarget (280) must be at least chunking.semanticMin (300)`, for `rag` commands and a hand-edited `config.json` as well. `alpha` outside [0, 1] is refused rather than silently clamped.
- Configuration validation also checks that a provider `baseUrl` is an http(s) URL, that `apiKeyEnv` is an environment variable name rather than a pasted key, which would otherwise be written to `config.json`, that a reranker other than `none` names a model, and that `httpTimeoutMs` is at most 600000. The two registered real workspaces load unchanged.

## v0.7.1 (2026-10-10)

No rebuild needed: v0.7.x indexes stay compatible. `-w` for every workspace command, and hybrid search without a reranker for Chinese questions about non-Chinese documents.

- `rag zotero`, `rag eval`, `rag connect` and `rag mcp` accept `-w NAME|PATH` like the other commands. They defined their own `--workspace` flag, which took only a path and had no `-w` shorthand, so `rag zotero sync -w communications` failed with "flag provided but not defined: -w" although `rag --help` lists `-w NAME|PATH` for every command. They now resolve registered names too, and outside a workspace they name the registered workspaces. `rag init` still takes a path, since it creates the workspace. Run from a directory outside the real workspace, `rag zotero status -w communications` and `rag eval -w communications` work with this build.
- Without a reranker, a hybrid query containing Han bigrams that match no indexed chunk ranks by vector search alone. A Chinese question about English papers matched only its stray Latin terms, such as "LoS" or "RKHS", and BM25 ranked chunks by those alone with weight alpha. A query whose bigrams match anything, the `bm25` mode, and the rerank candidates are unchanged. On the real six-paper workspace (English papers), over its 60 Chinese questions plus 15 English controls in `hybrid` mode: the Chinese questions went from Recall@5 / MRR 0.950 / 0.754 to 0.950 / 0.769, now identical to `vector`, with 4 questions ranked higher and 3 lower; the controls were unchanged (0.956 / 0.847).

## v0.7.0 (2026-10-09)

Existing v0.6 indexes report `rebuild required`; run `rag rebuild` once after upgrading. MinerU figure captions get chunks of their own.

- MinerU figure captions get chunks of their own, and a figure or table that the export places inside a paragraph moves after the text that continues past it. MinerU lists a figure where it sits on the page, so its caption landed mid-sentence in whatever paragraph surrounded it, or was cut from its number by a chunk boundary ("Fig. 3." ending one chunk, the caption opening the next). In the six real papers, 40 of 97 captioned figures split a paragraph on their page, and "Fig. 2. An illustration of the CKM construction framework." sat between "neighboring" and "BSs can store" in a chunk on assumptions, where no search for the framework diagram found it. `rag read` locates figure images in the same order, so all 120 images of the six papers are still listed and 86 instead of 64 sit on a chunk that opens with their caption. On a rebuilt copy of the real workspace (804 chunks instead of 760), over the 48 questions whose labelled passage changed plus 15 unchanged controls: rerank Recall@5 0.974 → 0.989, the framework diagram now ranked first, candidates@30 0.984 → 1.000 and MRR 0.914 → 0.905; hybrid 0.870 / 0.729 → 0.870 / 0.739; vector 0.892 / 0.731 → 0.892 / 0.719; bm25 0.497 / 0.364 → 0.466 / 0.334, since a caption no longer lends its words to the paragraph around it. Requires `rag rebuild`.

## v0.6.8 (2026-10-09)

No rebuild needed: v0.6.x indexes stay compatible. Hybrid fusion, rerank input, the MCP query description and evaluation.

- `rag eval` relevance labels can list alternatives under `anyOf`: such a label is found when any of them matches, for an answer stated in more than one passage, such as a list that continues into the next chunk or a result two papers report. Separate labels must still all be found. On the real six-paper workspace, four of the six questions that rerank missed were answered by a passage next to the labelled one: the list of simulation baselines starts one chunk before the labelled half, Assumption 1(a) sits beside the labelled paragraph on fixed channel parameters, the user covariance is built in (45)–(46) rather than factorized in (53), and a noise-model question that names no paper is answered by two other papers' system models. With those passages accepted, rerank Recall@5 on the 154 questions is 0.987 instead of 0.961 and MRR 0.900 instead of 0.878, and no other question's rerank score changed. A dataset that uses `anyOf` needs this version; earlier ones reject it as an unknown field.
- The `rag_query` tool description asks agents to write queries in the documents' language, translating a question asked in another, since keyword search matches only the documents' own words. On the real six-paper workspace (English papers), the 44 Chinese questions translated into English, against the originals with the same build: hybrid, the mode of workspaces without a reranker, went from Recall@5 / MRR 0.909 / 0.712 to 0.932 / 0.766; rerank from 0.909 / 0.819 to 0.932 / 0.815, one more question answered; vector from 0.909 / 0.750 to 0.932 / 0.744. The two Chinese questions whose answer never reaches the 30 rerank candidates miss in English too.
- Hybrid search weighs the BM25 and vector scores instead of their ranks: each retriever's scores are rescaled to [0, 1] over its own results and added with weights alpha and 1 - alpha. The default alpha is now 0.3; a workspace whose `config.json` sets 0.4 keeps it, and `rag tui` changes it with immediate effect. Reciprocal rank fusion gave every list the same credit per rank, and BM25, which matches any query term, ranks noise as highly as real matches: a passage middling in both lists outranked the vector winner, and hybrid scored below vector search alone (Recall@5 0.864 against 0.916 on the real six-paper workspace, 0.818 against 0.909 on its 44 Chinese questions, whose BM25 matched only stray Latin terms such as "LoS"). Replaying the stored BM25 and vector rankings of all 154 questions offline reproduced every reciprocal-rank-fusion score, then gave hybrid Recall@5 / MRR 0.922 / 0.732 at alpha 0.3 (0.922 / 0.747 at 0.2, 0.916 / 0.723 at 0.4, 0.877 / 0.685 at 0.5), against vector alone at 0.916 / 0.726; the 30 rerank candidates kept 0.981 recall from 0.2 to 0.5. A live run with this build on the 59 questions whose score any fusion changed plus 15 controls, at the workspace's alpha 0.4, matched the replay except one near-tie, left every control unchanged, and moved hybrid from 0.811 / 0.559 to 0.919 / 0.641; rerank, which reorders the candidates, went from 0.986 / 0.879 to 0.986 / 0.881 on those questions. No rebuild needed.
- The reranker sees each candidate under its document title and section (`title > section`, then the passage), as its embedding was computed, with the title named once: MinerU sections already start with the paper title, which the embedding text repeats. The title is the Zotero title when linked. The reranker used to get the bare passage, so it could not tell which paper a section several papers share belongs to, and questions such as "What dominates the computational complexity of the message-passing map construction" ranked another paper's complexity analysis first although the right passage was among the candidates. On the real six-paper workspace (154 questions, voyage `rerank-3`), rerank Recall@5 rose from 0.942 to 0.961 and MRR from 0.849 to 0.878: the three complexity questions that missed now hit, 14 questions ranked their answer higher and 3 lower, and none lost recall. `bm25` and `hybrid` were unchanged. Estimated rerank input grew 14% (1.08 M to 1.23 M tokens for the 154 queries; 24% with the title repeated); median latency stayed at about 0.8 s. Removing the repeat from the embedding text as well was tried and dropped: on a rebuilt copy of the workspace, rerank scored the same, but vector Recall@5 fell from 0.916 to 0.896, and every index would have needed a rebuild.
- `rag eval` reports the `rerank` mode's `candidateRecall`: Recall over all 30 hybrid candidates the reranker orders (`candidateTopK` in the report), per question and on average. Recall@5 alone could not tell a label the reranker saw and ranked too low from one that never reached it, so it did not show whether to improve the candidates or the reranking. The rerank query now keeps every candidate and the evaluator scores the top 5 of them; rerank scores do not depend on how many results are kept. On the real six-paper workspace (154 questions, voyage `rerank-3`), every question's Recall@5 and reciprocal rank in all four modes matched the v0.6.7 baseline (rerank 0.942 / 0.849), and `candidateRecall` was 0.981: of the 9 rerank misses, 6 were among the candidates and 3 were not.

## v0.6.7 (2026-10-09)

No rebuild needed: v0.6.x indexes stay compatible. Evaluation and provider retries only.

- `rag eval` without `--modes` also evaluates `rerank` when the workspace configures a reranker, after the `bm25`, `vector` and `hybrid` baselines. The default used to stop at the baselines, so the mode a configured workspace actually queries with went unmeasured unless named; `scripts/ab-eval.sh` named only the baselines, and every A/B report it wrote lacked rerank. The script now follows the default. On the real six-paper workspace (voyage `rerank-3`), the 6 structure questions gave `bm25,vector,hybrid` on v0.6.6 and `bm25,vector,hybrid,rerank` on this build, with the same baseline scores; a workspace without a reranker evaluates the three baselines as before.
- `rag eval` reports record what produced them under `environment`: the build, the workspace configuration, the active index, each document's version and the dataset's SHA-256. A plain `go build`, as `scripts/ab-eval.sh` makes, now reports its Git commit (with `-dirty` for uncommitted changes) in `rag version` and the report instead of `dev (unknown)`. Before, a report kept only the dataset path, top K and scores: rerunning the real six-paper set of 39 questions gave hybrid Recall@5 0.72 against 0.90 in a report from three days earlier, every label still matched the current index, and nothing recorded which build, embedding model or index produced the older score. The same run with this build records `dev (5550a7f-dirty)`, `voyage-4` with reranker `rerank-3`, the active index generation and the six document versions.
- Embedding and rerank retries wait as long as the provider's `Retry-After` header asks (seconds or an HTTP date, at most one minute), and otherwise back off with random jitter (0.5–1 s, 1–2 s, 2–4 s, …) instead of a fixed 1, 2, 4 s that ignored the header. A rate limit longer than the retries' total, 7 s with the default 3 retries, failed every document caught in it, and parallel indexing workers retried in lockstep. Against a local stub endpoint that answered 429 with `Retry-After: 10` for 10 s from its third request, syncing the six real papers failed 2 of them after 12 rejected requests on the previous build; this build waited out the window after 2 rejections and indexed all six (759 chunks) in the same 10.3 s.

## v0.6.6 (2026-10-09)

No rebuild needed: v0.6.x indexes stay compatible. A workspace using an endpoint other than Voyage's default or the one `rag install` recorded must be registered on this machine: one created before v0.6.4 needs `rag workspace add PATH` once, and sync and queries say so.

- Release binaries compile SQLite and sqlite-vec with optimization. `scripts/cgo-flags.sh` set `CGO_CFLAGS` to header paths only, which replaced Go's default `-O2 -g`, so the bundled C code was built at `-O0` in releases, CI and the documented source build; it now keeps `-O2 -g` unless `CGO_CFLAGS` is already set. Same results, less time: on a synthetic 30,000-chunk index (500 Markdown files, 1024 dimensions, local stub embedder), one `rag query` took 24 ms instead of 42 ms in `bm25` mode, 65 ms instead of 107 ms in `hybrid` and 114 ms instead of 453 ms in `literal`, and indexing the 500 files used 8.4 s of CPU instead of 14.5 s. On the real six-paper workspace (760 chunks), a `literal` query took 18 ms instead of 38 ms.
- A workspace configuration from a cloned repository can no longer send files to an endpoint of its choosing. The existing credential check kept ambient keys away from such endpoints, but documents still went out: a committed `.rag-go/config.json` could point `documents` outside the repository and `embedding.baseUrl` at any server that needs no key, and the next query, including one an agent made through the user-wide MCP server, indexed those files and posted their text there. Text now goes only to Voyage's default endpoint, the one `rag install` recorded, or the endpoints of a workspace registered on this machine by `rag init` or `rag workspace add`. In any other workspace, sync and rebuild stop before reading a document, a query reports the same error, and nothing is sent; the message names the `rag workspace add` command, after which the next query syncs at once. Workspaces created before v0.6.4 that use another endpoint need `rag workspace add` once. Reproduced with a repository whose configuration pointed `documents` at a sibling folder holding a key file and the endpoint at a local listener: the previous build posted the file's text on the first `rag query`; this build refused, and the listener received nothing until `rag workspace add`. The real six-paper workspace, on Voyage's default endpoint, queried as before.
- Queries and document lookups do less work, with the same results. A query checks that the index is non-empty with one `EXISTS` instead of counting files, chunks and vectors (counting the vector table scans it); a query containing Han characters runs only the Han full-text search, whose result already replaced the plain one; and listing, reading, outlining and fixing documents fetch Zotero titles in one catalog query instead of up to four per document. On the synthetic 30,000-chunk index, a `bm25` query took 18.5 ms instead of 23.7 ms (median of 120 alternating runs); on the real six-paper workspace, title lookup went from 24 catalog queries to 1, and 27 queries, lists, outlines and reads in English, Chinese and mixed language, with and without `--document`, returned identical output.
- A MinerU export whose `layout.json` or `*_middle.json` exceeds 64 MiB is indexed with the content list's own page numbers instead of failing. Those files hold every span's coordinates and grow far faster than the content list, so a long book reached the limit, and the whole document failed with "document exceeds 64 MiB limit" although the page source only refines pages. An oversized page source is now skipped like a missing one. On copies of the six real papers plus one with a 64 MiB `layout.json`, the previous build failed that copy at scan; this build indexed all seven (93 chunks with pages 1–15 for the copy), and the six papers' chunks and pages were identical.

## v0.6.5 (2026-10-09)

No rebuild needed: v0.6.x indexes stay compatible. TUI fixes only.

- `rag tui` started outside a workspace can reach Settings: `tab` (or `2`) opens the workspace selected in the list instead of only printing a hint, and `1`/`2` pick a tab directly for terminals that keep Tab for themselves. The key help names the target (`tab 设置` on the list, `tab 列表` in Settings). The cursor is a plain bar of background color; terminals that raise text contrast drew the `>` it used to carry as a visible glyph. With colors off (`NO_COLOR`), the cursor is still a `>`. Setting names get their 29 columns back: v0.6.4 cut `indexing.embeddingWorkers` and `indexing.embeddingBatchSize` short.
- The TUI accent (cursor bar, active tab, keys, selected names, progress bar) is a muted blue, `#81A1C1` on dark terminals and `#5E81AC` on light ones, instead of ANSI magenta, which some themes draw as crimson; status badges keep the theme's own colors. The layout stops growing at 100 columns, so on wide terminals badges and tags stay next to what they describe.

## v0.6.4 (2026-10-09)

No rebuild needed: v0.6.x indexes stay compatible. Workspaces created before this release are not listed until `rag workspace add PATH` (or `rag init` again) registers them once.

- Commands reach any workspace from anywhere. `rag init` lists the workspace in `~/.config/rag-go/workspaces.json`, and `-w NAME` (short for `--workspace`) selects it by its directory name, or `parent/name` when two share one. `rag sync --all`, `rag status --all` and `rag clean --all` run on every listed workspace, continue past failures and exit non-zero if any failed. `rag workspace list|add|remove` shows and edits the list; workspaces created before this release need `rag workspace add PATH` (or `rag init` again) once. Outside a workspace, the error now names the listed workspaces. The list holds only paths; each workspace keeps its own data, configuration and credentials. On three scratch workspaces built from real MinerU papers, run from a directory outside all of them: `sync --all` skipped the 3 unchanged papers, then removed only the paper deleted from one workspace's documents; a workspace whose `.rag-go` was deleted showed as `missing` and made `sync --all` exit 1 after syncing the others; 12 parallel `rag init` runs registered all 12.
- `rag tui` manages every registered workspace. A Workspaces tab lists them as cards with a status badge (fresh, needs sync, needs rebuild, missing), file and chunk counts and the last sync; `s` syncs the selected workspace, `S` syncs all of them one after another, skipping missing ones and keeping each outcome on its card, and `enter` opens one in the Settings tab, the previous panel. `tab` switches tabs; outside a workspace `rag tui` now starts on Workspaces instead of failing. Opening another workspace with unsaved settings asks first. The panel has a new look: a colored cursor bar, status badges and colored key help, drawn with ASCII and background colors only so terminals that render ambiguous-width characters wide still line up; badge text turns white on light terminals. Driven in a real pseudo-terminal on scratch workspaces built from the MinerU papers, `S` synced two workspaces (one paper removed) and skipped a missing one, then `enter`, `tab` and `q` opened, switched and quit cleanly; startup is unchanged from v0.6.3 (0.13 s, or 5 s in a terminal that never answers the background-color query, as before).

## v0.6.3 (2026-10-08)

No rebuild needed: v0.6.x indexes stay compatible. The first rebuild after upgrading deletes every generation except the new and the previous one.

- `rag rebuild` (and the TUI and MCP rebuild) cleans up after publishing: it keeps the new index and the previous one for a manual rollback, and deletes older generations. `rag clean` also deletes `.rag-go/rag.db`, written by the first sync and never read again once a rebuild has published, and the fingerprint directories under `.rag-go/indexes` that deleting generations leaves empty. Finder's `.DS_Store` counts as empty: it no longer keeps a folder, or a generation opened in Finder, from being deleted, and is not reported as skipped. Unknown files and symlinks are still never removed. On a copy of a real workspace holding 18 generations in 8 fingerprint directories (126 MB), `rag clean --keep 2`, what a rebuild now does, left 2 generations in 2 directories (17 MB) with the same 6 documents and 760 chunks.

## v0.6.2 (2026-10-08)

No rebuild needed: v0.6.x indexes stay compatible.

- Agents correct extraction errors while reading. `rag_fix` (CLI `rag fix`) appends a `wrong<TAB>right` pair to the document folder's `rag-fixes.tsv` after checking that the wrong text, within one line, occurs exactly once in the document with the earlier pairs applied, so a correction can neither fail the document nor change another passage; MinerU output stays untouched. It does not sync, so chunk ids stay valid while reading, and the next query reindexes the document once. The MCP instructions tell agents to fix only misreadings confirmed on the PDF page or image and to report the authors' own mistakes instead. Write servers only. On six real papers, agents recorded 118 corrections for compound hyphens MinerU drops at line breaks (`environmentaware` for `environment-aware`); 16 repeated words were first refused as ambiguous and then fixed one occurrence at a time, and one query reindexed all six with no other text changed.

## v0.6.1 (2026-10-08)

No rebuild needed: v0.6.0 indexes stay compatible.

- `rag_read` passages of MinerU documents list the images of the figures, charts and tables they hold under `images` (absolute paths inside the paper's folder), so an agent that can open files reads the figure itself. Images are matched to chunks at read time by locating the parsed blocks in the indexed text, so no rebuild is needed: on six real papers all 120 body figures were placed, 112 on a passage of the same page and the rest, panels at the top of the next page, on the text just before them; author photos after the references are left out. A read takes about 6 ms more for the parse and alignment.
- `rag_outline` and `rag_read` drop the title heading every section shares, such as the paper title MinerU puts above all headings: sections read `I. INTRODUCTION` instead of `<full title> / I. INTRODUCTION`, and the title's own section keeps its name. On three real papers outlines shrank 37–46% with identical chunk ranges.

## v0.6.0 (2026-10-08)

Existing v0.5 indexes report `rebuild required`; run `rag rebuild` once after upgrading. MCP clients and scripts that read `hits[].metadata` or the `rag_list_documents` path list need the changes marked Breaking below.

- Agents can read a paper instead of only searching it. `rag_read` returns one document's chunks in order, around a hit's chunk id, by chunk range or by PDF page, within a token budget with a `next` cursor, and fails on a stale `version` instead of reading shifted chunks. `rag_outline` lists a document's sections as chunk ranges with pages and the PDF path. `rag_query` takes `document` to search one paper, and `mode=literal` finds exact text such as `\tag{28}`. All three work on read-only and HTTP servers, and the MCP server's instructions describe the reading workflow. CLI: `rag list`, `rag outline`, `rag read`, `rag query --document`.
- Breaking: query results describe each document once. Hits no longer carry `metadata`; they name their document in `document`, and the result's `documents` map gives each document's `version` and, when linked, its Zotero `metadata` (abstract included). On six papers this cut query output by 39–51% when hits come from one paper and 25% across three, with the same chunk content. Read `documents[hit.document].metadata` where you read `hit.metadata`.
- Breaking: `rag_list_documents` returns objects with `id`, `title`, `path` and `version` instead of bare paths.
- MinerU headings that end in a period, such as a run-in `Proof: See Appendix A.`, are read as body text. Every later block used to inherit such a line as its section until the next real heading; on six papers this mislabeled 11 chunks, and none of the 121 real headings ends in a period.
- MinerU tables keep their rows and columns: one line per row, cells separated by ` | `, in a chunk of their own with the caption, so a method stays next to its values. Cells used to be flattened into words, and a table could be split between chunks of surrounding prose.

## v0.5.2 (2026-10-08)

Existing v0.5 indexes report `rebuild required`; run `rag rebuild` once after upgrading.

- Display equations from MinerU and Markdown are no longer split into their own chunks: each stays whole in the chunk of the sentence that introduces it, so lead-ins such as "Substituting to (28), we have" no longer become fragments. On six papers, formula-only chunks fell from 273 of 1118 to 8 of 749, and rerank top-5 recall on formula questions rose from 0.33 to 1.0 while prose-question MRR stayed at 0.88.
- Display `$$…$$` formulas up to 4096 characters are kept whole; longer ones could previously be cut.

## v0.5.1 (2026-10-07)

- Breaking: stdio `rag mcp` without `--workspace` requires an absolute `workspace` argument on every tool call. It used to fall back to the server's working directory, which stays where the server was launched, so an agent that had moved to another project could silently search the previous workspace. Servers pinned with `--workspace`, including HTTP, still accept calls without it and now also reject a relative one.

## v0.5.0 (2026-10-07)

- `rag tui` opens an interactive panel for a workspace: index status (counts, whether a sync or rebuild is needed and why, the last sync, failed files), every workspace setting tagged by when a change takes effect (immediately, next sync, next Zotero sync, or after rebuild), and sync, rebuild and clean with a per-file progress bar; `esc` cancels a running task. Saving validates the configuration and refuses to overwrite a `config.json` edited elsewhere since the panel loaded it. The layout adapts to the terminal size.
- `pkg/rag`: `Options.Progress` receives an `IndexProgress` (documents settled, total, last path and running counts) as each document of a sync or rebuild settles.

## v0.4.1 (2026-10-07)

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
- Zotero: a paper imported twice links automatically when every duplicate shares the same DOI and title (the lowest item key wins); differing duplicates still never auto-link.

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
