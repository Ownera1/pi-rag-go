package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ownera1/rag-go/internal/workspace"
)

func TestProcessWorker(t *testing.T) {
	mode := os.Getenv("RAG_TEST_OP")
	if mode == "" {
		return
	}
	c, err := Open(Options{WorkspaceDir: os.Getenv("RAG_TEST_WORKSPACE")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch mode {
	case "sync":
		_, err = c.Sync(ctx)
	case "query":
		_, err = c.Query(ctx, "shared", QueryOptions{Mode: "bm25"})
	case "generation":
		before, e := c.Status(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(os.Getenv("RAG_TEST_READY"), nil, 0600); e != nil {
			t.Fatal(e)
		}
		for {
			if _, e = os.Stat(os.Getenv("RAG_TEST_RELEASE")); e == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
		q, e := c.Query(ctx, "newgeneration", QueryOptions{Mode: "bm25", DisableSync: true})
		if e != nil || len(q.Hits) != 1 {
			t.Fatalf("new generation: %+v %v", q, e)
		}
		after, e := c.Status(ctx)
		if e != nil || after.ActiveDB == before.ActiveDB {
			t.Fatalf("generation did not refresh: %+v %v", after, e)
		}
	case "read-lock":
		release, e := workspace.Lock(ctx, c.WorkspaceDir(), false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		if e = os.WriteFile(os.Getenv("RAG_TEST_READY"), nil, 0600); e != nil {
			t.Fatal(e)
		}
		for {
			if _, e = os.Stat(os.Getenv("RAG_TEST_RELEASE")); e == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}
func child(t *testing.T, root, mode string, extra ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestProcessWorker$")
	cmd.Env = append(os.Environ(), "RAG_TEST_OP="+mode, "RAG_TEST_WORKSPACE="+root)
	cmd.Env = append(cmd.Env, extra...)
	return cmd
}
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not become ready")
}

func TestMultipleProcessesShareReadersSerializeSyncAndCancelWaiting(t *testing.T) {
	var embeddings atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		embeddings.Add(1)
		time.Sleep(80 * time.Millisecond)
		items := make([]map[string]any, len(input.Input))
		for i := range items {
			items[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	defer modelServer.Close()
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.Chunking.Mode = "legacy"
	cfg.Embedding = ProviderConfig{Type: "openai", Model: "fake", Dimensions: 2, BaseURL: modelServer.URL}
	cfg.HTTPMaxRetries = 0
	c := configuredCore(t, root, cfg, nil)
	defer c.Close()
	sourceFile(t, docPath(c, "note.txt"), []byte("shared evidence"))
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := child(t, root, "sync").CombinedOutput(); err != nil {
				t.Errorf("child: %s %v", out, err)
			}
		}()
	}
	wg.Wait()
	if embeddings.Load() != 1 {
		t.Fatalf("duplicate embedding calls: %d", embeddings.Load())
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := child(t, root, "query").CombinedOutput(); err != nil {
				t.Errorf("query child: %s %v", out, err)
			}
		}()
	}
	wg.Wait()
	// An already-open peer must neither block rebuild while idle nor retain the
	// old database after publication and cleanup.
	peerReady, peerRelease := filepath.Join(root, "peer-ready"), filepath.Join(root, "peer-release")
	peer := child(t, root, "generation", "RAG_TEST_READY="+peerReady, "RAG_TEST_RELEASE="+peerRelease)
	var output bytes.Buffer
	peer.Stdout, peer.Stderr = &output, &output
	if err := peer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Process.Kill(); _ = peer.Wait() }()
	waitFile(t, peerReady)
	sourceFile(t, docPath(c, "note.txt"), []byte("newgeneration shared evidence"))
	if _, err := c.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cleanup(context.Background(), 1, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := peer.Wait(); err != nil {
		t.Fatalf("idle peer: %s %v", &output, err)
	}
	ready1, ready2, release := filepath.Join(root, "ready1"), filepath.Join(root, "ready2"), filepath.Join(root, "release")
	first := child(t, root, "read-lock", "RAG_TEST_READY="+ready1, "RAG_TEST_RELEASE="+release)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	second := child(t, root, "read-lock", "RAG_TEST_READY="+ready2, "RAG_TEST_RELEASE="+release)
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.WriteFile(release, nil, 0600); _ = first.Wait(); _ = second.Wait() }()
	waitFile(t, ready1)
	waitFile(t, ready2)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Rebuild(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("writer did not wait/cancel behind readers: %v", err)
	}
	if _, err := c.Cleanup(ctx, 1, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("clean ignored readers: %v", err)
	}
}
