# Document ingestion

Conversion and indexing are separate operations. `ragprep` produces validated source artifacts; `ragctl index` and the MCP `rag_index` tool consume them. This keeps extraction retries and model/runtime setup independent of the database writer and embedding provider.

## PDF conversion

Build `go build -o bin/ragprep ./cmd/ragprep`. Choose an explicit backend:

```sh
bin/ragprep convert --backend grobid --url http://localhost:8070 \
  --input /absolute/pdfs --output /absolute/converted --timeout 10m
bin/ragprep convert --backend pdftotext \
  --input /absolute/paper.pdf --output /absolute/converted
bin/ragprep convert --backend mineru \
  --input /absolute/scanned-paper.pdf --output /absolute/converted \
  --mineru-command /absolute/bin/mineru-kit --timeout 30m
bin/ragctl index /absolute/converted
```

`--input` accepts a file or recursively scanned PDF directory. Hidden directories and directory symlinks are skipped. The converter prints a JSON report of successful packages and failures, continues independent files after a failure, and exits nonzero if any conversion failed. SIGINT/SIGTERM cancels HTTP requests and converter processes. Each PDF has a per-file timeout and a 100 MiB input limit. Parsed artifacts and converter stdout are bounded to 64 MiB; child XML parts inside DOCX have the same limit. XML nesting and element counts are bounded.

GROBID sends multipart PDF input to `POST /api/processFulltextDocument`, requests paragraph/sentence/heading coordinates, and disables external consolidation. It requires a service reachable at `--url`. Its response must contain valid TEI body paragraphs before publication. Physical pages come only from paragraph coordinates; missing coordinates remain unknown. GROBID is suited to scholarly PDFs with a text layer; scanned pages require OCR. Consult the [GROBID service](https://grobid.readthedocs.io/en/latest/Grobid-service/) and [coordinate contract](https://grobid.readthedocs.io/en/latest/Coordinates-in-PDF/).

`pdftotext` runs Poppler with UTF-8 output. Form-feed page boundaries are retained, including intervening blank pages. It extracts the PDF text layer and supplies page bounds, but does not perform OCR, identify scholarly sections, or recover complex column reading order. No automatic fallback between engines occurs; failures remain visible.

The MinerU adapter targets the current `mineru-kit parse INPUT --format middle_json --pages all -o OUTPUT` interface. Install/configure MinerU and its models separately. It consumes the semantic MiddleJson protocol rather than a bounded `mineru parse --json` command response. Earlier MinerU exports can be imported using the command below; invoking earlier CLI versions is not supported by this adapter. Consult the [official CLI](https://opendatalab.github.io/MinerU/usage/cli_tools/) and [output contract](https://opendatalab.github.io/MinerU/reference/output_files/).

## Import existing OCR output

```sh
bin/ragprep import-mineru --input /absolute/results/paper_content_list.json \
  --source /absolute/original/paper.pdf --output /absolute/converted
bin/ragprep inspect /absolute/converted/PACKAGE/rag-source.json
bin/ragctl index /absolute/converted
```

Supported MinerU contracts:

- Legacy Content List V1: flat text/title/list objects, explicit `page_idx`.
- Content List V2: page-grouped arrays, typed paragraph/title/list content and text spans. If an export omits `page_idx`, page bounds remain null: array position cannot establish the source page of a selected-page export.
- Legacy MiddleJson: `pdf_info[].page_idx` and `para_blocks[].lines[].spans`.
- Current MiddleJson: `schema="docvortex.middle"`, `schema_version="2.0"`, `pages[].page_idx` and semantic `blocks`.
- Current structured content: schema-free `pages[].page_idx` and text content.

ModelJson, unknown schema versions and negative/fractional/duplicate page indices are rejected. Image, table, equation and page decoration block types are skipped. Recognized reference sections and V2 reference lists are excluded. Heading levels are retained when supplied; inferred generic title types default to level 1. OCR accuracy depends on the producing engine and document. Importing a JSON artifact does not run OCR.

## One canonical body per document

Each output package contains a `rag-source.json` manifest and immutable content whose filename includes its SHA-256 hash. The manifest is replaced atomically only after content validation. A failed or cancelled conversion preserves the previous manifest; unused immutable content may remain for inspection and is ignored by indexing when a manifest is present. A later successful conversion can be picked up by `ragctl refresh`.

```json
{
  "version": 1,
  "format": "grobid-tei",
  "contentPath": "content-HASH.tei.xml",
  "sourcePath": "/absolute/original/paper.pdf",
  "sourceHash": "SHA256_OF_ORIGINAL_PDF",
  "title": "paper.pdf"
}
```

Manifest formats are `grobid-tei`, `jats`, `markdown`, `html`, `docx`, `mineru`, `paged-text`, and `text`. `contentPath` must resolve inside the package directory, including after symlink resolution. `sourcePath` is attribution metadata, may refer to a file on another machine, and is never opened by the parser. Imported artifacts leave the original source hash unknown. A `markdown` manifest may add `pagesFrom`, a MinerU JSON export of the same document inside the package; Markdown paragraphs are aligned to it by text to take its physical pages. A legacy MiddleJson (`layout.json`, `*_middle.json`) is preferred: its `cross_page` spans give a paragraph continuing onto the next page both pages, where content lists keep only the first. A raw MinerU folder indexed from its content list is repaged from such a sibling file the same way; blocks it cannot locate, or a sibling from another run, leave the content list's pages unchanged. Manifest bytes and all referenced files participate in change detection. Document ID is based on the canonical absolute path and remains stable while content changes.

A manifest makes its directory one document: companion Markdown, JSON, images and PDF files are suppressed. A raw MinerU output directory is also treated as one document. Put each paper in its own directory. Without a manifest, selection prefers explicit `.mineru.json` or `structured_content.json`, then Content List V1, MiddleJson, and V2. Exports with explicit source page indices take precedence over V2's page-grouped arrays. Different paper stems in one artifact directory cause a scan failure; use separate directories or a manifest. Current saved packages use `middle_json.json`/`structured_content.json`, while historical files often use `paper_middle.json`/`paper_content_list.json`. Source attribution for raw exports requires an import or a manifest; the parser does not guess the original PDF.

Query chunks return `sourcePath`, `title`, `format`, `parserVersion`, section, and available page/line bounds. `path` remains the canonical indexed artifact so document listing, refresh and deletion use the same identity. Unknown page bounds are null; unknown chunk line bounds retain the existing API sentinel `0`.

## Upgrade and verification

This release changes the processing fingerprint to `document-blocks-v3`. Existing Go generations must be rebuilt with `ragctl rebuild`. The old active generation remains available until a complete rebuild succeeds; incompatible queries and incremental writes are refused. Legacy TypeScript databases remain read-only and retain the previous query schema.

```sh
go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 ./cmd/...
```

Tests cover parser boundaries and excluded content, DOCX ZIP/XML extraction, source page provenance, directory deduplication, manifest traversal, cancellation, publication failures, index/query/refresh for both chunking modes, a real three-page PDF through Poppler, and deterministic GROBID HTTP/MinerU subprocess protocols. The real PDF test skips only when Poppler is absent locally; CI installs it on both operating systems. A live GROBID or MinerU extraction-quality benchmark requires those engines and representative original documents.
