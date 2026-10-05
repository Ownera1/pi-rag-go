package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
)

func TestSourceMetadataReplacementAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")
	db, err := Open(path, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	doc := model.Document{ID: "stable", Path: "canonical", Hash: "first", Title: "Paper", SourcePath: "original.pdf", Format: "mineru", ParserVersion: "v3"}
	chunks := []model.Chunk{{ID: "one", Content: "known evidence"}}
	if err := db.Replace(ctx, doc, chunks, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	var row int64
	if err := db.SQL.QueryRow("SELECT rowid FROM chunks").Scan(&row); err != nil {
		t.Fatal(err)
	}
	got, err := db.Chunks(ctx, []int64{row})
	if err != nil || got[row].SourcePath != "original.pdf" || got[row].Title != "Paper" {
		t.Fatalf("metadata=%+v err=%v", got, err)
	}
	doc.Title = "Updated Paper"
	if err := db.Replace(ctx, doc, chunks, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.SQL.QueryRow("SELECT count(*) FROM chunk_sources").Scan(&count); err != nil || count != 1 {
		t.Fatalf("stale metadata retained: %d err=%v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(path, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reader.SQL.QueryRow("SELECT rowid FROM chunks").Scan(&row); err != nil {
		t.Fatal(err)
	}
	got, err = reader.Chunks(ctx, []int64{row})
	if err != nil || got[row].Content != "known evidence" || got[row].SourcePath != "original.pdf" {
		t.Fatalf("reader query required new schema: %+v err=%v", got, err)
	}
}

func TestReplacementRollsBackAllArtifactsOnFailure(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rag.db"), false, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	doc := model.Document{ID: "paper", Path: "old.md", Hash: "old"}
	chunks := []model.Chunk{{ID: "one", Content: "old evidence"}}
	if err = db.Replace(ctx, doc, chunks, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	doc.Path = "new.json"
	doc.Hash = "new"
	doc.Replaces = []string{"old.md"}
	bad := []model.Chunk{{ID: "duplicate", Content: "a"}, {ID: "duplicate", Content: "b"}}
	if err = db.Replace(ctx, doc, bad, [][]float32{{1, 0}, {1, 0}}); err == nil {
		t.Fatal("expected insertion failure")
	}
	paths, err := db.List(ctx)
	if err != nil || len(paths) != 1 || paths[0] != "old.md" {
		t.Fatalf("rollback: %v %v", paths, err)
	}
}
