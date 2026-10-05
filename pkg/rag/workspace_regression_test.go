package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
)

type pausedEmbedding struct {
	fakeEmbedding
	pause   atomic.Bool
	started chan struct{}
	resume  chan struct{}
}

func (p *pausedEmbedding) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if p.pause.Load() {
		close(p.started)
		select {
		case <-p.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.fakeEmbedding.EmbedDocuments(ctx, texts)
}

func TestInputChangedDuringEmbeddingAndSyncCancellationRetainOldDocument(t *testing.T) {
	for _, cancelSync := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "canceled"}[cancelSync], func(t *testing.T) {
			p := &pausedEmbedding{started: make(chan struct{}), resume: make(chan struct{})}
			c := openTest(t, t.TempDir(), p)
			defer c.Close()
			file := docPath(c, "note.txt")
			sourceFile(t, file, []byte("original evidence"))
			if _, err := c.Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			sourceFile(t, file, []byte("replacement evidence"))
			p.pause.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type response struct {
				result IndexResult
				err    error
			}
			done := make(chan response, 1)
			go func() { r, err := c.Sync(ctx); done <- response{r, err} }()
			select {
			case <-p.started:
			case <-ctx.Done():
				t.Fatal("embedding did not start")
			}
			if cancelSync {
				cancel()
			} else {
				sourceFile(t, file, []byte("latest evidence"))
				close(p.resume)
			}
			r := <-done
			if r.err == nil || r.result.Indexed != 0 || r.result.Failed != 1 {
				t.Fatalf("sync: %+v %v", r.result, r.err)
			}
			if cancelSync && !errors.Is(r.err, context.Canceled) {
				t.Fatal(r.err)
			}
			q, err := c.Query(context.Background(), "original", QueryOptions{Mode: "bm25", DisableSync: true})
			if err != nil || len(q.Hits) != 1 || q.Freshness != "stale" {
				t.Fatalf("old index: %+v %v", q, err)
			}
			p.pause.Store(false)
			if _, err = c.Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceCredentialsAreIsolatedAndEnvironmentWins(t *testing.T) {
	const envName = "RAG_TEST_ISOLATED_CREDENTIAL"
	t.Setenv(envName, "")
	var global atomic.Bool
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		want := "Bearer " + body.Input[0]
		if global.Load() {
			want = "Bearer environment"
		}
		if r.Header.Get("Authorization") != want {
			t.Errorf("authorization crossed workspaces: %q != %q", r.Header.Get("Authorization"), want)
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 0}}}})
	}))
	defer server.Close()
	cores := []*Core{}
	for _, key := range []string{"one", "two"} {
		cfg := DefaultConfig()
		cfg.Chunking.Mode = "legacy"
		cfg.Embedding = ProviderConfig{Type: "openai", Model: "fixture", Dimensions: 2, BaseURL: server.URL, APIKeyEnv: envName}
		c := configuredCore(t, t.TempDir(), cfg, nil)
		defer c.Close()
		cores = append(cores, c)
		if err := workspace.AtomicJSON(filepath.Join(workspace.Store(c.WorkspaceDir()), "credentials.json"), map[string]string{envName: key}); err != nil {
			t.Fatal(err)
		}
		sourceFile(t, docPath(c, "note.txt"), []byte(key))
	}
	syncBoth := func() {
		errors := make(chan error, len(cores))
		for _, c := range cores {
			go func() { _, err := c.Sync(context.Background()); errors <- err }()
		}
		for range cores {
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
		}
	}
	syncBoth()
	if os.Getenv(envName) != "" {
		t.Fatal("Core modified process environment")
	}
	t.Setenv(envName, "environment")
	global.Store(true)
	for _, c := range cores {
		sourceFile(t, docPath(c, "note.txt"), []byte("updated"))
	}
	syncBoth()
	if calls.Load() != 4 {
		t.Fatalf("provider calls: %d", calls.Load())
	}
}

func TestCanonicalArtifactRemovalRetiresOldRepresentationOnPartialFailure(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	artifact := docPath(c, "paper/paper_content_list.json")
	sourceFile(t, artifact, []byte(`[{"type":"text","text":"canonicalold evidence","page_idx":0}]`))
	if _, err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, docPath(c, "paper/full.md"), []byte("canonicalnew evidence"))
	sourceFile(t, docPath(c, "invalid.txt"), []byte{0, 1})
	r, err := c.Sync(context.Background())
	if err == nil || r.Indexed != 1 || r.Failed != 1 {
		t.Fatalf("partial sync: %+v %v", r, err)
	}
	q, err := c.Query(context.Background(), "canonicalold", QueryOptions{Mode: "bm25", DisableSync: true})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("duplicate artifact: %+v %v", q, err)
	}
	files, err := c.ListDocuments(context.Background())
	if err != nil || len(files) != 1 || files[0] != docPath(c, "paper/full.md") {
		t.Fatalf("canonical paths: %v %v", files, err)
	}
}

func TestInitialDatabaseStampCancellationCanRetryAndMetadataIsAtomic(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	s := &session{root: workspace.Store(c.WorkspaceDir()), docs: filepath.Dir(docPath(c, "note.txt")), cfg: DefaultConfig()}
	s.cfg.Embedding.Dimensions = 2
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ensureDB(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "rag.db")); !os.IsNotExist(err) || s.db != nil {
		t.Fatal("canceled initial stamp left a partial store")
	}
	sourceFile(t, docPath(c, "note.txt"), []byte("retry evidence"))
	if _, err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "rag.db"), false, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.SQL.Exec(`CREATE TRIGGER fail_stamp BEFORE INSERT ON metadata WHEN NEW.key='embedding_dimensions' BEGIN SELECT RAISE(ABORT, 'stamp failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = s.stamp(context.Background(), db); err == nil {
		t.Fatal("failed stamp succeeded")
	}
	var count int
	if err = db.SQL.QueryRow("SELECT COUNT(*) FROM metadata").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial metadata: %d %v", count, err)
	}
}
