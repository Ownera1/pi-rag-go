# Retrieval evaluation

The supplied sample contains 20 synthetic TEI passages and 20 explicit relevance labels: 10 English and 10 Chinese keyword queries. These exercise source parsing, Chinese FTS, mode selection, Core query results, and metric reporting. They are deliberately simple and cannot establish quality on research papers.

## Run a real-paper comparison

1. Select a fixed corpus of actual TEI papers and sync it in a separate workspace. Use the same active generation for all modes; avoid index changes during a run.
2. Write 20–50 realistic questions before inspecting retrieved results. Include paraphrases, mixed languages, and difficult confusable passages. Mark the relevant passages by path plus a unique text excerpt or section; verify every label against parsed chunk output. Keep unsupported questions in a separate answerability evaluation because passage Recall requires at least one label.
3. Run `rag eval --dataset questions.jsonl --modes bm25,vector,hybrid,rerank --top-k 5 --output evaluation/runs/papers.json` against that workspace. Rerank requires a configured reranker. The first three baselines disable reranking so comparisons remain distinct.
4. Inspect per-question hits, failures and degradation before comparing aggregate scores. Repeat on a held-out set when tuning chunking, alpha, and candidate limits. Rebuild after changing chunking. To compare configs, use separate workspaces. Each report records its `environment`: build, workspace configuration, active index, every document's version and the dataset's SHA-256; compare these first when two reports disagree.

Each JSONL line is an object:

```json
{"id":"paper-01","query":"How is the channel prior updated?","provenance":"human-paper-labels-v1","relevant":[{"pathSuffix":"paper.tei.xml","contains":"a unique excerpt copied from the parsed passage","section":"Method"}]}
```

A relevance label matches when every provided selector matches. Path suffixes respect filename boundaries; `contains` is case sensitive; `section` is exact. Each label is counted once even if several returned chunks match it. Recall@K is matched labels divided by all labels, and MRR uses the rank of the first matching hit. Keep labels distinct; overly broad excerpts or sections can overstate recall. Overlapping chunks may satisfy a label more than once but do not increase that label's Recall. Failed calls count as zero, and degradation is reported explicitly instead of being attributed to a successful full-model mode.

Latency is measured around each sequential Core query, including model-provider time. p50/p95 use nearest-rank percentiles. They exclude indexing and are not load/concurrency benchmarks. Warm caches, provider retries and rate limits affect these values.

Cost is optional. Supply `--embedding-usd-per-million-tokens` and `--rerank-usd-per-million-tokens` using rates verified for your model. Missing rates for used components yield `estimatedCostUsd: null`; BM25 without model calls has zero query cost. Counts are heuristic input-token estimates and logical calls, excluding HTTP retries, actual billed tokens, indexing costs and provider-specific minimum charges. Use provider usage records for billing.

The release smoke suite runs all four modes with the actual standalone `rag` binary and deterministic embedding/reranker HTTP stubs. Perfect fixture scores prove wiring, not model quality. Evaluation requires a synchronized compatible index and disables automatic sync; freeze the corpus and avoid concurrent writers while comparing modes. Run your real corpus with the actual provider before claiming retrieval improvements.
