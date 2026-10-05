package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/pkg/rag"
)

type embedding struct{}

func (embedding) Model() string                                         { return "test" }
func (embedding) Dimensions() int                                       { return 2 }
func (embedding) EmbedQuery(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }
func (embedding) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func testCore(t *testing.T) (*rag.Core, string) {
	t.Helper()
	root := t.TempDir()
	cfg := rag.DefaultConfig()
	cfg.Embedding.Model = "test"
	cfg.Embedding.Dimensions = 2
	cfg.Chunking.Mode = "legacy"
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0600)
	core, err := rag.Open(rag.Options{StoreDir: root, Embedder: embedding{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	return core, root
}
func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition did not settle")
}

func TestPDFPreparerCacheAndCanonicalPriority(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	pdf := filepath.Join(t.TempDir(), "paper.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.0 test"), 0600)
	counter := filepath.Join(root, "counter")
	converter := filepath.Join(root, "pdftotext")
	os.WriteFile(converter, []byte("#!/bin/sh\nprintf x >> '"+counter+"'\nprintf 'searchable evidence\\f'\n"), 0700)
	p := PDFPreparer{root, model.PDFConfig{Backend: "pdftotext", Command: converter}}
	one, err := p.Prepare(ctx, []string{pdf})
	if err != nil || len(one.Sources) != 1 || len(one.Failures) != 0 {
		t.Fatal(one, err)
	}
	if _, err = p.Prepare(ctx, []string{pdf}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(counter)
	if string(b) != "x" {
		t.Fatal("reconverted", string(b))
	}
	chosen, err := p.Prepare(ctx, []string{pdf, one.Sources[0].DocumentPath})
	if err != nil || len(chosen.Sources) != 1 {
		t.Fatal(chosen, err)
	}
	os.WriteFile(pdf, []byte("%PDF-1.0 changed"), 0600)
	if _, err = p.Prepare(ctx, []string{pdf}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(counter)
	if string(b) != "xx" {
		t.Fatal(string(b))
	}
}

func TestWatcherNestedCreateRenameDeleteRestartAndRootLoss(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	core, store := testCore(t)
	source := t.TempDir()
	core.Index(ctx, []string{source})
	watcher := NewWatcher(core, store, model.AutoRefreshConfig{Enabled: true, DebounceMs: 30, RescanMs: 150})
	go watcher.Run(ctx)
	file := filepath.Join(source, "first.txt")
	os.WriteFile(file, []byte("first evidence"), 0600)
	await(t, func() bool { s, _ := core.Status(ctx); return s.Files == 1 })
	nested := filepath.Join(source, "nested")
	os.Mkdir(nested, 0700)
	second := filepath.Join(nested, "second.txt")
	os.WriteFile(second, []byte("second evidence"), 0600)
	await(t, func() bool { s, _ := core.Status(ctx); return s.Files == 2 })
	renamed := filepath.Join(nested, "renamed.txt")
	os.Rename(second, renamed)
	os.Remove(file)
	await(t, func() bool { docs, _ := core.ListDocuments(ctx); return len(docs) == 1 && docs[0] == renamed })
	cancel()
	watcher.Wait()
	os.WriteFile(filepath.Join(source, "offline.txt"), []byte("offline evidence"), 0600)
	ctx, cancel = context.WithCancel(context.Background())
	watcher = NewWatcher(core, store, model.AutoRefreshConfig{Enabled: true, DebounceMs: 30, RescanMs: 100})
	go watcher.Run(ctx)
	defer func() { cancel(); watcher.Wait() }()
	await(t, func() bool { s, _ := core.Status(ctx); return s.Files == 2 })
	missing := source + "-away"
	os.Rename(source, missing)
	await(t, func() bool { return watcher.Status().LastError != "" })
	s, _ := core.Status(ctx)
	if s.Files != 2 {
		t.Fatal("pruned unavailable root", s)
	}
	os.Rename(missing, source)
	os.Remove(renamed)
	await(t, func() bool { s, _ := core.Status(ctx); return s.Files == 1 })
}
