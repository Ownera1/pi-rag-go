package rag

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/document"
)

func sourceFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func wordDocument(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>wordmarker evidence</w:t></w:r></w:p></w:body></w:document>`)); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNewFormatsIndexQueryRefreshAndProvenance(t *testing.T) {
	for _, mode := range []string{"semantic", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			files := map[string][]byte{
				"note.md":                     []byte("# Methods\nmarkdownmarker evidence\n```\n# code heading\n```"),
				"page.html":                   []byte(`<nav>NOISE_LEAK</nav><main><h1>Methods</h1><p>htmlmarker evidence</p></main>`),
				"paper.nxml":                  []byte(`<article><body><sec><title>Methods</title><p>jatsmarker evidence</p></sec></body></article>`),
				"paper.docx":                  wordDocument(t),
				"ocr/paper_content_list.json": []byte(`[{"type":"text","text":"minermarker evidence","page_idx":4}]`),
				"ocr/full.md":                 []byte("DUPLICATE_LEAK minermarker evidence"),
				"ocr/metadata.json":           []byte(`{"duplicate":"DUPLICATE_LEAK"}`),
				"converted/body.tei.xml":      []byte(`<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><p coords="3,1,2,3,4">pdfmarker evidence</p></body></text></TEI>`),
			}
			for path, data := range files {
				sourceFile(t, filepath.Join(source, path), data)
			}
			manifest := filepath.Join(source, "converted", "rag-source.json")
			metadata := document.Manifest{Version: 1, Format: "grobid-tei", ContentPath: "body.tei.xml", SourcePath: "/archive/original.pdf", Title: "Research Paper"}
			b, _ := json.Marshal(metadata)
			sourceFile(t, manifest, b)
			cfg := DefaultConfig()
			cfg.Embedding.Dimensions = 2
			cfg.Embedding.Model = "fake"
			cfg.Chunking.Mode = mode
			c := configuredCore(t, filepath.Join(dir, "store"), cfg, fakeEmbedding{})
			defer c.Close()
			// Overlapping roots and explicit companion files must still be canonical.
			r, err := c.Index(ctx, []string{source, filepath.Join(source, "ocr", "full.md")})
			if err != nil || r.Failed != 0 || r.Indexed != 6 {
				t.Fatalf("index=%+v err=%v", r, err)
			}
			for marker, format := range map[string]string{"markdownmarker": "markdown", "htmlmarker": "html", "jatsmarker": "jats", "wordmarker": "docx", "minermarker": "mineru", "pdfmarker": "grobid-tei"} {
				q, err := c.Query(ctx, marker, QueryOptions{Mode: "bm25"})
				if err != nil || len(q.Hits) == 0 || q.Hits[0].Chunk.Format != format || q.Hits[0].Chunk.ParserVersion != document.ParserVersion {
					t.Fatalf("%s query=%+v err=%v", marker, q, err)
				}
				ch := q.Hits[0].Chunk
				if marker == "pdfmarker" && (ch.SourcePath != "/archive/original.pdf" || ch.Title != "Research Paper" || ch.PageStart == nil || *ch.PageStart != 3 || ch.LineStart != 0) {
					t.Fatalf("source attribution lost: %+v", ch)
				}
				if marker == "minermarker" && (ch.PageStart == nil || *ch.PageStart != 5) {
					t.Fatalf("MinerU source page lost: %+v", ch)
				}
			}
			q, err := c.Query(ctx, "DUPLICATE_LEAK", QueryOptions{Mode: "bm25"})
			if err != nil || len(q.Hits) != 0 {
				t.Fatalf("indexed duplicate conversion artifacts: %+v err=%v", q, err)
			}
			// Artifact content participates in the manifest hash, even when the
			// manifest and original PDF haven't changed.
			body := filepath.Join(source, "converted", "body.tei.xml")
			sourceFile(t, body, bytes.ReplaceAll(files["converted/body.tei.xml"], []byte("pdfmarker"), []byte("replacementmarker")))
			r, err = c.Refresh(ctx)
			if err != nil || r.Indexed != 1 || r.Failed != 0 {
				t.Fatalf("refresh=%+v err=%v", r, err)
			}
			listed, err := c.ListDocuments(ctx)
			if err != nil || len(listed) != 6 {
				t.Fatalf("refresh changed canonical documents: %v err=%v", listed, err)
			}
			if err := os.Remove(body); err != nil {
				t.Fatal(err)
			}
			r, err = c.Refresh(ctx)
			if err != nil || r.Failed == 0 {
				t.Fatalf("missing artifact wasn't reported: %+v err=%v", r, err)
			}
			q, err = c.Query(ctx, "replacementmarker", QueryOptions{Mode: "bm25"})
			if err != nil || len(q.Hits) != 1 {
				t.Fatalf("failed refresh destroyed active evidence: %+v err=%v", q, err)
			}
		})
	}
}

func TestParserUpgradeRequiresRebuild(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "source.txt")
	sourceFile(t, file, []byte("known evidence"))
	c := openTest(t, filepath.Join(dir, "store"), fakeEmbedding{})
	defer c.Close()
	if r, err := c.Index(ctx, []string{file}); err != nil || r.Failed != 0 {
		t.Fatalf("index=%+v err=%v", r, err)
	}
	if err := c.db.SetMetadata(ctx, "processing_fingerprint", "older-parser"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Index(ctx, []string{file}); err == nil || !strings.Contains(err.Error(), "rebuild required") {
		t.Fatal("old and new parser chunks mixed", err)
	}
	if r, err := c.Rebuild(ctx); err != nil || r.Failed != 0 {
		t.Fatalf("parser upgrade rebuild=%+v err=%v", r, err)
	}
}
