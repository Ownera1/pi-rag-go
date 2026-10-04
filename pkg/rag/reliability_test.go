package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func configuredCore(t *testing.T, root string, cfg Config, embedding EmbeddingProvider) *Core {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(Options{StoreDir: root, Embedder: embedding})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConcurrentIndexStatusAndConfigSnapshots(t *testing.T) {
	root := t.TempDir()
	c := openTest(t, filepath.Join(root, "store"), fakeEmbedding{})
	defer c.Close()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, err := c.Status(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				cfg := c.Config()
				if len(cfg.TrackedPaths) > 0 {
					cfg.TrackedPaths[0] = "mutated"
				}
			}
		}()
	}
	for i := 0; i < 12; i++ {
		p := filepath.Join(root, fmt.Sprintf("source-%d.txt", i))
		if err := os.WriteFile(p, []byte("stable evidence"), 0600); err != nil {
			t.Fatal(err)
		}
		if r, e := c.Index(context.Background(), []string{p}); e != nil || r.Failed > 0 {
			t.Fatalf("index: %+v %v", r, e)
		}
	}
	close(stop)
	wg.Wait()
	for _, p := range c.Config().TrackedPaths {
		if p == "mutated" {
			t.Fatal("Config exposed a mutable tracked-path slice")
		}
	}
}

func TestPartialIndexTracksRootAndRefreshRetriesAfterRestart(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "sources")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	good, bad := filepath.Join(source, "good.txt"), filepath.Join(source, "bad.tei.xml")
	if err := os.WriteFile(good, []byte("stable evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("<broken/>"), 0600); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "store")
	c := openTest(t, store, fakeEmbedding{})
	r, err := c.Index(context.Background(), []string{source})
	if err != nil || r.Indexed != 1 || r.Failed != 1 {
		t.Fatalf("partial: %+v %v", r, err)
	}
	s, err := c.Status(context.Background())
	if err != nil || len(s.TrackedPaths) != 1 || len(s.FailedFiles) != 1 || s.FailedFiles[0].Path != bad || s.Progress.Phase != "partial" {
		t.Fatalf("state: %+v %v", s, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c = openTest(t, store, fakeEmbedding{})
	defer c.Close()
	s, _ = c.Status(context.Background())
	if len(s.FailedFiles) != 1 {
		t.Fatal("failure state not persisted")
	}
	if err = os.WriteFile(bad, []byte(`<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><p>repaired evidence</p></body></text></TEI>`), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = c.Refresh(context.Background())
	if err != nil || r.Failed > 0 || r.Indexed != 1 || r.Skipped != 1 {
		t.Fatalf("retry: %+v %v", r, err)
	}
	s, _ = c.Status(context.Background())
	if s.Files != 2 || len(s.FailedFiles) != 0 {
		t.Fatalf("retry status: %+v", s)
	}
}

func TestHTTPClientTimeoutFallsBackButCallerCancellationDoesNot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
			Role  string   `json:"input_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		if in.Role == "query" {
			<-r.Context().Done()
			return
		}
		data := make([]map[string]any, len(in.Input))
		for i := range data {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Embedding = ProviderConfig{Type: "voyage", Model: "fake", Dimensions: 2, BaseURL: server.URL}
	cfg.Chunking.Mode = "legacy"
	cfg.HTTPTimeoutMs, cfg.HTTPMaxRetries = 1000, 0
	root := t.TempDir()
	c := configuredCore(t, filepath.Join(root, "store"), cfg, nil)
	defer c.Close()
	p := filepath.Join(root, "source.txt")
	if e := os.WriteFile(p, []byte("stable evidence"), 0600); e != nil {
		t.Fatal(e)
	}
	if r, e := c.Index(context.Background(), []string{p}); e != nil || r.Failed > 0 {
		t.Fatalf("index %+v %v", r, e)
	}
	q, err := c.Query(context.Background(), "stable evidence", QueryOptions{})
	if err != nil || q.Method != "bm25-fallback" || len(q.Hits) != 1 || q.Degraded == "" {
		t.Fatalf("timeout: %+v %v", q, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q, err = c.Query(ctx, "stable evidence", QueryOptions{})
	if !errors.Is(err, context.Canceled) || len(q.Hits) > 0 {
		t.Fatalf("cancellation: %+v %v", q, err)
	}
}

type blockingEmbedding struct {
	started chan struct{}
	once    sync.Once
}

func (p *blockingEmbedding) Model() string   { return "fake" }
func (p *blockingEmbedding) Dimensions() int { return 2 }
func (p *blockingEmbedding) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (p *blockingEmbedding) EmbedDocuments(ctx context.Context, _ []string) ([][]float32, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestIndexProgressAndCancellation(t *testing.T) {
	p := &blockingEmbedding{started: make(chan struct{})}
	cfg := DefaultConfig()
	cfg.Embedding.Model, cfg.Embedding.Dimensions = "fake", 2
	cfg.Chunking.Mode = "legacy"
	root := t.TempDir()
	c := configuredCore(t, filepath.Join(root, "store"), cfg, p)
	defer c.Close()
	file := filepath.Join(root, "source.txt")
	if e := os.WriteFile(file, []byte("stable evidence"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Index(ctx, []string{file}); done <- err }()
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("index not started")
	}
	s, e := c.Status(context.Background())
	if e != nil || !s.Progress.Running || s.Progress.Total != 1 || s.Progress.CurrentFile != file {
		t.Fatalf("progress: %+v %v", s, e)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("index cancellation blocked")
	}
	s, _ = c.Status(context.Background())
	if s.Progress.Running || s.Progress.Phase != "canceled" || len(s.FailedFiles) != 1 {
		t.Fatalf("final progress: %+v", s)
	}
}
