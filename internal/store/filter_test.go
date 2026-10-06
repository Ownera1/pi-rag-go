package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
)

func TestPrefilterBeforeTopKLargeSetsAndReadOnlyMain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")
	db, err := Open(path, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		key := fmt.Sprintf("doc-%d", i)
		p := fmt.Sprintf("/%d.txt", i)
		doc := model.Document{ID: key, DocumentKey: key, Path: p, Title: key}
		ch := model.Chunk{ID: key, Path: p, Content: "signal 信道 estimation"}
		vector := []float32{1, 0}
		if i == 204 {
			vector = []float32{0, 1}
		}
		if err = db.Replace(ctx, doc, []model.Chunk{ch}, [][]float32{vector}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	db, err = Open(path, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys := []string{"doc-204"}
	for i := 0; i < 5000; i++ {
		keys = append(keys, fmt.Sprintf("missing-%d", i))
	}
	if err = db.SetDocumentFilter(ctx, keys); err != nil {
		t.Fatal(err)
	}
	fts, err := db.FTS(ctx, "signal", 1, true)
	if err != nil || len(fts) != 1 {
		t.Fatalf("FTS %v %v", fts, err)
	}
	vec, err := db.Vectors(ctx, []float32{1, 0}, 1, true)
	if err != nil || len(vec) != 1 || vec[0].RowID != fts[0].RowID {
		t.Fatalf("pre-top-k vector filter %v %v", vec, err)
	}
	if err = db.SetDocumentFilter(ctx, []string{}); err != nil {
		t.Fatal(err)
	}
	fts, err = db.FTS(ctx, "signal", 1, true)
	if err != nil || len(fts) != 0 {
		t.Fatal("empty filter is unrestricted")
	}
	vec, err = db.Vectors(ctx, []float32{1, 0}, 1, true)
	if err != nil || len(vec) != 0 {
		t.Fatal("empty vector filter is unrestricted")
	}
	if _, err = db.SQL.Exec("DELETE FROM main.files"); err == nil {
		t.Fatal("read-only main accepted a write")
	}
}
