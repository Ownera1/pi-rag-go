package rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/catalog"
	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/internal/zotero"
)

func writePackage(t *testing.T, c *Core, name, hash, path, doi string) string {
	t.Helper()
	dir := docPath(c, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, filepath.Join(dir, "body.txt"), []byte("signal 信道 estimation evidence "+name))
	manifest := document.Manifest{Version: 1, Format: "text", ContentPath: "body.txt", SourceHash: strings.Repeat(hash, 64), SourcePath: path, Title: "Wireless channel estimation", DOI: doi}
	b, _ := json.Marshal(manifest)
	p := filepath.Join(dir, "rag-source.json")
	sourceFile(t, p, b)
	return p
}

func applyFixtureCatalog(t *testing.T, c *Core, changed bool) {
	t.Helper()
	db, err := catalog.Open(filepath.Join(workspace.Store(c.WorkspaceDir()), "catalog.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	y1, y2 := 2024, 2025
	title := "Selected paper"
	hash := "first"
	version := int64(0)
	if changed {
		title = "Updated local title"
		hash = "edited"
		version = 0
	}
	s := zotero.Snapshot{LibraryType: "user", LibraryID: "42", RequestedID: "0", Items: []zotero.Item{
		{Key: "PAPER001", Hash: "first", Metadata: model.ZoteroMetadata{ZoteroReference: model.ZoteroReference{LibraryType: "user", LibraryID: "42", ItemKey: "PAPER001"}, ItemType: "journalArticle", Title: "Original paper", Year: &y1, DOI: "10.1234/first"}},
		{Key: "PAPER002", Version: version, Hash: hash, Metadata: model.ZoteroMetadata{ZoteroReference: model.ZoteroReference{LibraryType: "user", LibraryID: "42", ItemKey: "PAPER002"}, ItemType: "conferencePaper", Title: title, Year: &y2, DOI: "10.1234/second", Tags: []model.ZoteroTag{{Tag: "ISAC", Type: 1}}, Collections: []string{"COLLECT1"}}},
		{Key: "ATTACH01", Hash: "file", ParentKey: "PAPER001", Path: "/Zotero/storage/ATTACH01/file.pdf", Metadata: model.ZoteroMetadata{ItemType: "attachment"}},
	}, Collections: []zotero.Collection{{Key: "COLLECT1", Name: "Research"}}}
	if _, err = db.Apply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataFiltersLocksMoveRebuildAndReadOnly(t *testing.T) {
	ctx := context.Background()
	p := &countedEmbedding{}
	c := openTest(t, t.TempDir(), p)
	defer c.Close()
	first := writePackage(t, c, "first", "a", "/Zotero/storage/ATTACH01/file.pdf", "")
	second := writePackage(t, c, "second", "b", "", "https://doi.org/10.1234/SECOND")
	applyFixtureCatalog(t, c, false)
	if _, err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"bm25", "vector", "hybrid"} {
		q, err := c.Query(ctx, "signal", QueryOptions{Mode: mode, DisableSync: true, Filter: &MetadataFilter{Tags: []string{"ISAC"}, Collections: []string{"COLLECT1"}}})
		if err != nil || len(q.Hits) != 1 || q.Hits[0].Chunk.Path != second || q.Hits[0].Metadata.ItemKey != "PAPER002" || q.MetadataSyncedAt == "" {
			t.Fatalf("%s filter: %+v %v", mode, q, err)
		}
		q, err = c.Query(ctx, "信道", QueryOptions{Mode: "bm25", DisableSync: true, Filter: &MetadataFilter{Tags: []string{"ISAC"}}})
		if err != nil || len(q.Hits) != 1 || q.Hits[0].Chunk.Path != second {
			t.Fatalf("Han filter: %+v %v", q, err)
		}
		q, err = c.Query(ctx, "signal", QueryOptions{Mode: mode, DisableSync: true, Filter: &MetadataFilter{Tags: []string{"missing"}}})
		if err != nil || len(q.Hits) != 0 {
			t.Fatalf("empty filter: %+v %v", q, err)
		}
	}
	if _, err := c.LinkZotero(ctx, first, ZoteroReference{LibraryType: "user", LibraryID: "0", ItemKey: "PAPER002"}, false); err != nil {
		t.Fatal(err)
	}
	calls := p.calls.Load()
	applyFixtureCatalog(t, c, true)
	if _, err := c.MatchZotero(ctx); err != nil {
		t.Fatal(err)
	}
	q, err := c.Query(ctx, "signal", QueryOptions{Mode: "bm25", DisableSync: true})
	if err != nil || len(q.Hits) != 2 || p.calls.Load() != calls {
		t.Fatalf("metadata refresh re-embedded: %+v %v", q, err)
	}
	for _, h := range q.Hits {
		if h.Metadata == nil || h.Metadata.Title != "Updated local title" {
			t.Fatal("metadata not enriched")
		}
	}
	moved := docPath(c, "moved")
	if err = os.Rename(filepath.Dir(first), moved); err != nil {
		t.Fatal(err)
	}
	first = filepath.Join(moved, "rag-source.json")
	if _, err = c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	q, err = c.Query(ctx, "signal", QueryOptions{Mode: "bm25", DisableSync: true, Filter: &MetadataFilter{Tags: []string{"ISAC"}}})
	if err != nil || len(q.Hits) != 2 {
		t.Fatalf("move/rebuild lost links %+v %v", q, err)
	}
	found := false
	for _, h := range q.Hits {
		if h.Chunk.Path == first {
			found = h.Metadata.Locked && h.Metadata.MatchMethod == "manual"
		}
	}
	if !found {
		t.Fatal("manual lock did not follow SourceHash")
	}
	ro, err := Open(Options{WorkspaceDir: c.WorkspaceDir(), ReadOnly: true, Embedder: p})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	q, err = ro.Query(ctx, "signal", QueryOptions{Mode: "vector", Filter: &MetadataFilter{Tags: []string{"ISAC"}}})
	if err != nil || len(q.Hits) != 2 {
		t.Fatalf("read-only prefilter %+v %v", q, err)
	}
	if _, err = ro.SyncZotero(ctx, nil); err == nil {
		t.Fatal("read-only metadata sync allowed")
	}
	status, err := c.Status(ctx)
	if err != nil || status.Zotero == nil || status.Zotero.Locked != 1 {
		t.Fatalf("status %+v %v", status, err)
	}
	db, err := store.Open(status.ActiveDB, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL.Exec("UPDATE files SET document_key='',source_path=''")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	calls = p.calls.Load()
	if _, err = c.MatchZotero(ctx); err != nil {
		t.Fatal(err)
	}
	q, err = c.Query(ctx, "signal", QueryOptions{Mode: "bm25", DisableSync: true})
	if err != nil || len(q.Hits) != 2 || p.calls.Load() != calls {
		t.Fatal("old-index hydration failed")
	}
	for _, h := range q.Hits {
		if h.Metadata == nil {
			t.Fatal("hydration lost metadata")
		}
	}
}

func TestMetadataFilterValidationPrecedesAutomaticIndexing(t *testing.T) {
	p := &countedEmbedding{}
	c := openTest(t, t.TempDir(), p)
	defer c.Close()
	sourceFile(t, docPath(c, "note.txt"), []byte("signal evidence"))
	year := 0
	if _, err := c.Query(context.Background(), "signal", QueryOptions{Filter: &MetadataFilter{YearFrom: &year}}); err == nil {
		t.Fatal("invalid year accepted")
	}
	if p.calls.Load() != 0 {
		t.Fatal("invalid filter triggered embedding")
	}
}
