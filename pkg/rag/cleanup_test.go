package rag

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ownera1/rag-go/internal/store"
)

// fakeGeneration writes a minimal recognized store at dir/rag.db.
func fakeGeneration(t *testing.T, dir string) string {
	t.Helper()
	db, err := store.Open(filepath.Join(dir, "rag.db"), false, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.SQL.Exec("INSERT OR REPLACE INTO metadata(key,value) VALUES('go_storage_version','1')"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCleanupPreviewRetainsActiveAndUnknownFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	c := openTest(t, dir, fakeEmbedding{})
	defer c.Close()
	file := docPath(c, "source.txt")
	if err := os.WriteFile(file, []byte("stable evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, e := c.Sync(ctx); e != nil || r.Failed > 0 {
		t.Fatalf("index: %+v %v", r, e)
	}
	// Each rebuild keeps the previous generation, the first sync's database
	// counting as one, and removes older ones.
	legacy := filepath.Join(dir, ".rag-go", "rag.db")
	generations := []string{}
	for i := 0; i < 3; i++ {
		if _, err := c.Rebuild(ctx); err != nil {
			t.Fatal(err)
		}
		s, err := c.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		generations = append(generations, filepath.Dir(s.ActiveDB))
		if _, err = os.Stat(legacy); (err == nil) != (i == 0) {
			t.Fatalf("rebuild %d: first sync database %v", i, err)
		}
	}
	for _, path := range []string{generations[0], legacy + "-wal", legacy + "-shm"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rebuild kept %s: %v", path, err)
		}
	}
	if _, err := os.Stat(generations[1]); err != nil {
		t.Fatal("rebuild removed the previous generation", err)
	}
	// Directory mtime can change during read-only SQLite inspections and must
	// not determine which generation is newest.
	older := fakeGeneration(t, filepath.Join(filepath.Dir(generations[1]), "1-0000000000000001"))
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(older, future, future); err != nil {
		t.Fatal(err)
	}
	previewTwo, err := c.Cleanup(ctx, 2, true)
	if err != nil || len(previewTwo.Removed) != 1 || previewTwo.Removed[0] != older {
		t.Fatalf("retention order: %+v %v", previewTwo, err)
	}
	// A generation of another fingerprint leaves its directory empty.
	stale := fakeGeneration(t, filepath.Join(dir, ".rag-go", "indexes", "1111111111-1111111111", "1-0000000000000002"))
	s, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	active := s.ActiveDB
	unknown := filepath.Join(dir, ".rag-go", "indexes", "0000000000-0000000000", "1-0000000000000000")
	if err = os.MkdirAll(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(unknown, "personal.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	// A rebuild killed mid-way leaves its staging database behind.
	orphan := filepath.Join(dir, ".rag-go", "staging", "1-0123456789abcdef")
	if err = os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(orphan, "rag.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	linked := filepath.Join(dir, ".rag-go", "indexes", "linked")
	if err = os.Symlink(out, linked); err != nil {
		t.Fatal(err)
	}
	preview, err := c.Cleanup(ctx, 1, true)
	if err != nil || len(preview.Removed) != 4 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	for _, path := range preview.Removed {
		if _, err = os.Stat(filepath.Join(path, "rag.db")); err != nil {
			t.Fatalf("preview deleted %s", path)
		}
	}
	removed, err := c.Cleanup(ctx, 1, false)
	if err != nil || len(removed.Removed) != 4 {
		t.Fatalf("cleanup: %+v %v", removed, err)
	}
	for _, path := range removed.Removed {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("not removed: %s %v", path, err)
		}
	}
	if _, err = os.Stat(active); err != nil {
		t.Fatal("active index removed", err)
	}
	if _, err = os.Stat(filepath.Join(unknown, "personal.txt")); err != nil {
		t.Fatal("unknown file removed", err)
	}
	if _, err = os.Lstat(linked); err != nil {
		t.Fatal("symlink removed", err)
	}
	if _, err = os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Fatalf("empty fingerprint directory kept: %v", err)
	}
	q, err := c.Query(ctx, "stable evidence", QueryOptions{})
	if err != nil || len(q.Hits) == 0 {
		t.Fatalf("active query: %+v %v", q, err)
	}
	c.Close()
	c = openTest(t, dir, fakeEmbedding{})
	defer c.Close()
	q, err = c.Query(ctx, "stable evidence", QueryOptions{})
	if err != nil || len(q.Hits) == 0 {
		t.Fatalf("restart: %+v %v", q, err)
	}
}

func TestClosedCoreRejectsWrites(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	c.Close()
	if _, err := c.Sync(context.Background()); err == nil {
		t.Fatal("Index after Close succeeded")
	}
	if _, err := c.Rebuild(context.Background()); err == nil {
		t.Fatal("Rebuild after Close succeeded")
	}
	if _, err := c.Cleanup(context.Background(), 1, false); err == nil {
		t.Fatal("Cleanup after Close succeeded")
	}
}

func TestCleanupRefusesSymlinkedStaging(t *testing.T) {
	root := t.TempDir()
	c := openTest(t, filepath.Join(root, "store"), fakeEmbedding{})
	defer c.Close()
	victim := filepath.Join(root, "outside", "1-0123456789abcdef")
	if err := os.MkdirAll(victim, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(victim), filepath.Join(root, "store", ".rag-go", "staging")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cleanup(context.Background(), 1, false); err == nil {
		t.Fatal("followed a symlinked staging directory")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal(err)
	}
}
