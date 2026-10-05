package rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type mappedPreparer struct {
	manifest string
	fail     bool
}

func (p *mappedPreparer) Prepare(ctx context.Context, paths []string) (PreparationResult, error) {
	r := PreparationResult{}
	for _, path := range paths {
		if p.fail {
			r.Failures = append(r.Failures, FileFailure{Path: path, Stage: "convert", Error: "conversion failed"})
		} else {
			r.Sources = append(r.Sources, PreparedSource{SourcePath: path, DocumentPath: p.manifest})
		}
	}
	return r, nil
}
func TestPreparedSourceDeleteFailureAndRestart(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	pdf := filepath.Join(source, "paper.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.0 source"), 0600)
	store := t.TempDir()
	packageDir := filepath.Join(store, "prepared", "paper")
	os.MkdirAll(packageDir, 0700)
	artifact := filepath.Join(packageDir, "body.txt")
	os.WriteFile(artifact, []byte("original searchable evidence"), 0600)
	manifest := filepath.Join(packageDir, "rag-source.json")
	os.WriteFile(manifest, []byte(`{"version":1,"format":"text","contentPath":"body.txt","sourcePath":"`+pdf+`"}`), 0600)
	preparer := &mappedPreparer{manifest: manifest}
	core := openTest(t, store, fakeEmbedding{})
	core.preparer = preparer
	if r, err := core.Index(ctx, []string{source}); err != nil || r.Failed != 0 || r.Indexed != 1 {
		t.Fatal(r, err)
	}
	preparer.fail = true
	if r, err := core.Refresh(ctx); err != nil || r.Failed != 1 {
		t.Fatal(r, err)
	}
	s, _ := core.Status(ctx)
	if s.Files != 1 || s.FailedFiles[0].Path != pdf {
		t.Fatal(s)
	}
	core.Close()
	core, err := Open(Options{StoreDir: store, Embedder: fakeEmbedding{}, SourcePreparer: preparer})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	preparer.fail = false
	if r, err := core.Refresh(ctx); err != nil || r.Failed != 0 {
		t.Fatal(r, err)
	}
	s, _ = core.Status(ctx)
	if len(s.FailedFiles) != 0 {
		t.Fatal("stale failure", s)
	}
	os.Remove(pdf)
	if _, err := core.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ = core.Status(ctx)
	if s.Files != 0 {
		t.Fatal("deleted PDF returned", s)
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal("cache removed")
	}
}
func TestRemoveOverlappingRootsAndPersistence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	os.Mkdir(nested, 0700)
	os.WriteFile(filepath.Join(root, "outer.txt"), []byte("outer evidence"), 0600)
	os.WriteFile(filepath.Join(nested, "inner.txt"), []byte("inner evidence"), 0600)
	store := t.TempDir()
	c := openTest(t, store, fakeEmbedding{})
	if _, err := c.Index(ctx, []string{root, nested}); err != nil {
		t.Fatal(err)
	}
	removed, err := c.Remove(ctx, []string{root})
	if err != nil || removed.RemovedDocuments != 1 {
		t.Fatal(removed, err)
	}
	s, _ := c.Status(ctx)
	if s.Files != 1 || len(s.TrackedPaths) != 1 || s.TrackedPaths[0] != nested {
		t.Fatal(s)
	}
	c.Close()
	c = openTest(t, store, fakeEmbedding{})
	defer c.Close()
	s, _ = c.Status(ctx)
	if len(s.TrackedPaths) != 1 {
		t.Fatal(s)
	}
	if _, err := c.Remove(ctx, []string{root}); err == nil {
		t.Fatal("removed unregistered root")
	}
}
func TestStoreContentsExcludedFromTrackedParent(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	c := openTest(t, store, fakeEmbedding{})
	defer c.Close()
	os.WriteFile(filepath.Join(base, "source.txt"), []byte("real source"), 0600)
	secret, _ := json.Marshal(map[string]string{"API_KEY": "secret"})
	os.WriteFile(filepath.Join(store, "credentials.json"), secret, 0600)
	r, err := c.Index(context.Background(), []string{base})
	if err != nil || r.Indexed != 1 || r.Failed != 0 {
		t.Fatal(r, err)
	}
}
