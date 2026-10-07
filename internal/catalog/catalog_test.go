package catalog

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/zotero"
)

func snapshot() zotero.Snapshot {
	year := 2025
	m := model.ZoteroMetadata{ZoteroReference: model.ZoteroReference{LibraryType: "user", LibraryID: "42", ItemKey: "PAPER001"}, ItemType: "journalArticle", Title: "Wireless channel estimation", DOI: "10.1234/a", Year: &year, Date: "2025-06", Creators: []model.ZoteroCreator{{Type: "author", Name: "3GPP"}}, Tags: []model.ZoteroTag{{Tag: "ISAC", Type: 1}}, Collections: []string{"COLLECT1"}}
	return zotero.Snapshot{LibraryType: "user", LibraryID: "42", RequestedID: "0", Items: []zotero.Item{{Key: "PAPER001", Hash: "one", Metadata: m}, {Key: "ATTACH01", Hash: "attachment", ParentKey: "PAPER001", Path: "/Zotero/storage/ATTACH01/paper.pdf", Metadata: model.ZoteroMetadata{ItemType: "attachment"}}}, Collections: []zotero.Collection{{Key: "COLLECT1", Name: "Research"}}}
}

func TestSnapshotRollbackLocksOrphansAndRestore(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "catalog.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := snapshot()
	if _, err = db.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	doc := model.CatalogDocument{Key: "stable-source", Path: "/prepared/rag-source.json", SourcePath: s.Items[1].Path}
	match, err := db.Match(ctx, []model.CatalogDocument{doc}, "user", "0")
	if err != nil || match.Linked != 1 {
		t.Fatalf("match %+v %v", match, err)
	}
	m, err := db.Metadata(ctx, doc.Key)
	if err != nil || m.AttachmentKey != "ATTACH01" || m.Locked {
		t.Fatalf("attachment match %+v %v", m, err)
	}
	if err = db.Link(ctx, doc, m.ZoteroReference, "manual", true); err != nil {
		t.Fatal(err)
	}
	before, _ := db.Status(ctx)
	if _, err = db.Apply(ctx, zotero.Snapshot{LibraryType: "user", LibraryID: "0", RequestedID: "0"}); err == nil {
		t.Fatal("unidentified empty alias accepted")
	}
	if retained, _ := db.Status(ctx); retained.Items != before.Items {
		t.Fatal("empty alias erased another library")
	}
	if _, err = db.SQL.Exec(`CREATE TRIGGER fail_item BEFORE UPDATE ON zotero_items WHEN NEW.title='fail' BEGIN SELECT RAISE(ABORT,'fixture failure'); END;`); err != nil {
		t.Fatal(err)
	}
	s.Items[0].Metadata.Title = "fail"
	s.Items[0].Hash = "fail"
	failed, err := db.Apply(ctx, s)
	if err == nil {
		t.Fatal("expected transaction failure")
	}
	if failed.Updated != 0 || failed.SyncedAt != "" {
		t.Fatal("failed transaction reported committed changes")
	}
	after, _ := db.Status(ctx)
	m, _ = db.Metadata(ctx, doc.Key)
	if before.SyncedAt != after.SyncedAt || m.Title != "Wireless channel estimation" {
		t.Fatal("failed sync changed catalog/state")
	}
	s.Items = nil
	if _, err = db.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Match(ctx, []model.CatalogDocument{doc}, "user", "0"); err != nil {
		t.Fatal(err)
	}
	m, _ = db.Metadata(ctx, doc.Key)
	after, _ = db.Status(ctx)
	if !m.Orphan || !m.Locked || len(after.Orphans) != 1 {
		t.Fatal("manual orphan was lost")
	}
	s = snapshot()
	if _, err = db.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	m, _ = db.Metadata(ctx, doc.Key)
	if m.Orphan || !m.Locked {
		t.Fatal("restore did not recover locked association")
	}
	year := 2025
	keys, err := db.AllowedDocuments(ctx, &model.MetadataFilter{YearFrom: &year, Tags: []string{"ISAC"}, Collections: []string{"COLLECT1"}})
	if err != nil || len(keys) != 1 || keys[0] != doc.Key {
		t.Fatalf("filters %v %v", keys, err)
	}
	keys, err = db.AllowedDocuments(ctx, &model.MetadataFilter{Tags: []string{"missing"}})
	if err != nil || len(keys) != 0 {
		t.Fatal("empty filter did not exclude all")
	}
}

func TestDOIAmbiguityAndTitleCandidatesNeverAutoLink(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "catalog.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := snapshot()
	if _, err = db.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	doc := model.CatalogDocument{Key: "doi-doc", Path: "/doi.md", DOI: "https://doi.org/10.1234/A", Title: "Wireless channel estimation"}
	r, err := db.Match(ctx, []model.CatalogDocument{doc}, "user", "0")
	if err != nil || r.Linked != 1 {
		t.Fatalf("unique DOI %+v %v", r, err)
	}
	copy := s.Items[0]
	copy.Key = "PAPER002"
	copy.Metadata.ItemKey = copy.Key
	s.Items = append(s.Items, copy)
	if _, err = db.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, err = db.Match(ctx, []model.CatalogDocument{doc}, "user", "0")
	if err != nil || r.Unmatched != 1 || len(r.Candidates) != 2 {
		t.Fatalf("ambiguity %+v %v", r, err)
	}
	if m, _ := db.Metadata(ctx, doc.Key); m != nil {
		t.Fatal("ambiguous DOI retained automatic match")
	}
}

func TestMinerUFolderLinksByAttachmentFilename(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "catalog.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := snapshot()
	s.Items[1].Filename = "Jiang 等 - 2025 - Channel E.pdf"
	if _, err = db.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	doc := model.CatalogDocument{Key: "mineru", Path: "/docs/Jiang 等 - 2025 - Channel E.pdf-4c158022-fc0e-4cd6-832d-4a3e3f423bc6/auto/x_content_list.json"}
	r, err := db.Match(ctx, []model.CatalogDocument{doc}, "user", "0")
	if err != nil || r.Linked != 1 {
		t.Fatalf("match %+v %v", r, err)
	}
	if m, _ := db.Metadata(ctx, doc.Key); m == nil || m.AttachmentKey != "ATTACH01" || m.ItemKey != "PAPER001" {
		t.Fatalf("metadata %+v", m)
	}
	doc = model.CatalogDocument{Key: "plain", Path: "/docs/Jiang 等 - 2025 - Channel E.pdf/x_content_list.json"}
	if r, _ = db.Match(ctx, []model.CatalogDocument{doc}, "user", "0"); r.Linked != 0 {
		t.Fatal("folder without MinerU suffix linked")
	}
}
