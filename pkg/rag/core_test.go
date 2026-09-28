package rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ownera1/pi-rag-go/internal/store"
)

type fakeEmbedding struct{ fail bool }

func (fakeEmbedding) Model() string   { return "fake" }
func (fakeEmbedding) Dimensions() int { return 2 }
func (f fakeEmbedding) EmbedQuery(ctx context.Context, s string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (f fakeEmbedding) EmbedDocuments(ctx context.Context, in []string) ([][]float32, error) {
	if f.fail {
		return nil, os.ErrDeadlineExceeded
	}
	out := make([][]float32, len(in))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}
func openTest(t *testing.T, storeDir string, provider EmbeddingProvider) *Core {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Embedding.Dimensions = 2
	cfg.Embedding.Model = "fake"
	b, _ := json.Marshal(cfg)
	if e := os.MkdirAll(storeDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(storeDir, "config.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	c, e := Open(Options{StoreDir: storeDir, Embedder: provider})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestIndexQueryRebuildFailureAndRestart(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(source, "paper.tei.xml")
	valid := `<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><div><head>Channel Method</head><p>original evidence searchable.</p></div></body></text></TEI>`
	if e := os.WriteFile(file, []byte(valid), 0600); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(base, "store")
	c := openTest(t, dir, fakeEmbedding{})
	result, e := c.Index(ctx, []string{source})
	if e != nil || result.Failed != 0 {
		t.Fatalf("index: %+v, %v", result, e)
	}
	q, e := c.Query(ctx, "original evidence", QueryOptions{})
	if e != nil || len(q.Hits) == 0 || !strings.Contains(q.Hits[0].Chunk.Content, "original evidence") {
		t.Fatalf("query: %+v, %v", q, e)
	}
	before, _ := c.Status(ctx)
	if before.Chunks != before.Vectors || before.Chunks == 0 {
		t.Fatalf("coverage: %+v", before)
	}
	if e = os.WriteFile(file, []byte("<not-tei/>"), 0600); e != nil {
		t.Fatal(e)
	}
	bad, e := c.Rebuild(ctx)
	if e == nil || bad.Failed == 0 {
		t.Fatalf("expected failed rebuild: %+v %v", bad, e)
	}
	again, e := c.Query(ctx, "original evidence", QueryOptions{})
	if e != nil || len(again.Hits) == 0 {
		t.Fatalf("active index changed: %+v %v", again, e)
	}
	c.Close()
	c = openTest(t, dir, fakeEmbedding{})
	defer c.Close()
	again, e = c.Query(ctx, "original evidence", QueryOptions{})
	if e != nil || len(again.Hits) == 0 {
		t.Fatalf("restart lost index: %+v %v", again, e)
	}
	if e = os.WriteFile(file, []byte(strings.ReplaceAll(valid, "original evidence", "replacement evidence")), 0600); e != nil {
		t.Fatal(e)
	}
	built, e := c.Rebuild(ctx)
	if e != nil || built.Failed != 0 {
		t.Fatalf("rebuild: %+v %v", built, e)
	}
	q, e = c.Query(ctx, "replacement evidence", QueryOptions{})
	if e != nil || len(q.Hits) == 0 {
		t.Fatalf("new generation: %+v %v", q, e)
	}
}

func TestRefreshKeepsIndexWhenTrackedRootUnavailable(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "sources")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(source, "note.txt")
	if e := os.WriteFile(path, []byte("known stable evidence"), 0600); e != nil {
		t.Fatal(e)
	}
	c := openTest(t, filepath.Join(base, "store"), fakeEmbedding{})
	defer c.Close()
	indexed, e := c.Index(ctx, []string{source})
	if e != nil || indexed.Failed != 0 {
		t.Fatalf("index: %+v %v", indexed, e)
	}
	if e = os.Rename(source, source+"-offline"); e != nil {
		t.Fatal(e)
	}
	refreshed, e := c.Refresh(ctx)
	if e != nil || refreshed.Failed == 0 {
		t.Fatalf("refresh: %+v %v", refreshed, e)
	}
	listed, e := c.ListDocuments(ctx)
	if e != nil || len(listed) != 1 || listed[0] != path {
		t.Fatalf("files pruned: %v %v", listed, e)
	}
}

func TestClearPublishesEmptyGeneration(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "sources")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(source, "note.txt")
	if e := os.WriteFile(path, []byte("clear marker evidence"), 0600); e != nil {
		t.Fatal(e)
	}
	c := openTest(t, filepath.Join(base, "store"), fakeEmbedding{})
	defer c.Close()
	if r, e := c.Index(ctx, []string{source}); e != nil || r.Failed != 0 {
		t.Fatalf("index: %+v %v", r, e)
	}
	if e := c.Clear(ctx); e != nil {
		t.Fatal(e)
	}
	s, e := c.Status(ctx)
	if e != nil || s.Chunks != 0 || len(s.TrackedPaths) != 1 {
		t.Fatalf("clear status: %+v %v", s, e)
	}
	if r, e := c.Query(ctx, "clear marker", QueryOptions{}); e != nil || len(r.Hits) != 0 {
		t.Fatalf("clear query: %+v %v", r, e)
	}
}

func TestOpenRefusesExistingUnstampedStore(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "rag.db"), false, 2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(Options{StoreDir: root}); err == nil || !strings.Contains(err.Error(), "--legacy-readonly") {
		t.Fatalf("unstamped database should be rejected before initialization: %v", err)
	}
}
