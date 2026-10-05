package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

func TestSourceMetadataReplacementAndLegacyReadOnly(t *testing.T) {
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
	if _, err := db.SQL.Exec("DROP TABLE chunk_sources"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := Open(path, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if err := legacy.SQL.QueryRow("SELECT rowid FROM chunks").Scan(&row); err != nil {
		t.Fatal(err)
	}
	got, err = legacy.Chunks(ctx, []int64{row})
	if err != nil || got[row].Content != "known evidence" || got[row].SourcePath != "" {
		t.Fatalf("legacy query required new schema: %+v err=%v", got, err)
	}
}
