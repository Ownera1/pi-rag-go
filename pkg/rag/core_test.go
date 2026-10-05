package rag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
)

type fakeEmbedding struct{ fail bool }

func (fakeEmbedding) Model() string   { return "fake" }
func (fakeEmbedding) Dimensions() int { return 2 }
func (fakeEmbedding) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (f fakeEmbedding) EmbedDocuments(ctx context.Context, in []string) ([][]float32, error) {
	if f.fail {
		return nil, os.ErrDeadlineExceeded
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float32, len(in))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}
func configuredCore(t *testing.T, root string, cfg Config, p EmbeddingProvider) *Core {
	t.Helper()
	dir := workspace.Store(root)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := workspace.AtomicJSON(filepath.Join(dir, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	docs := cfg.Documents
	if !filepath.IsAbs(docs) {
		docs = filepath.Join(root, docs)
	}
	if err := os.MkdirAll(docs, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := Open(Options{WorkspaceDir: root, Embedder: p})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func openTest(t *testing.T, root string, p EmbeddingProvider) *Core {
	cfg := DefaultConfig()
	cfg.Embedding.Model, cfg.Embedding.Dimensions = "fake", 2
	cfg.Chunking.Mode = "legacy"
	return configuredCore(t, root, cfg, p)
}
func docPath(c *Core, name string) string { return filepath.Join(c.WorkspaceDir(), "documents", name) }

type countedEmbedding struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (*countedEmbedding) Model() string   { return "fake" }
func (*countedEmbedding) Dimensions() int { return 2 }
func (*countedEmbedding) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (p *countedEmbedding) EmbedDocuments(ctx context.Context, in []string) ([][]float32, error) {
	p.calls.Add(1)
	return fakeEmbedding{fail: p.fail.Load()}.EmbedDocuments(ctx, in)
}

func TestAutomaticSyncHashChangesNoSyncAndDelete(t *testing.T) {
	ctx := context.Background()
	p := &countedEmbedding{}
	c := openTest(t, t.TempDir(), p)
	defer c.Close()
	file := docPath(c, "note.txt")
	sourceFile(t, file, []byte("original evidence"))
	q, err := c.Query(ctx, "original", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 || q.Freshness != "fresh" || q.Sync == nil || q.Sync.Indexed != 1 {
		t.Fatalf("initial: %+v %v", q, err)
	}
	calls := p.calls.Load()
	if r, err := c.Sync(ctx); err != nil || r.Skipped != 1 || p.calls.Load() != calls {
		t.Fatalf("unchanged: %+v %v", r, err)
	}
	st, _ := os.Stat(file)
	sourceFile(t, file, []byte("replaced evidence"))
	if err = os.Chtimes(file, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	q, err = c.Query(ctx, "original", QueryOptions{Mode: "bm25", DisableSync: true})
	if err != nil || len(q.Hits) != 1 || q.Freshness != "stale" || p.calls.Load() != calls {
		t.Fatalf("no sync: %+v %v", q, err)
	}
	ro, err := Open(Options{WorkspaceDir: c.WorkspaceDir(), ReadOnly: true, Embedder: p})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	q, err = ro.Query(ctx, "original", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 || q.Freshness != "stale" || p.calls.Load() != calls {
		t.Fatalf("readonly: %+v %v", q, err)
	}
	if _, err = ro.Sync(ctx); err == nil {
		t.Fatal("readonly writer")
	}
	q, err = c.Query(ctx, "replaced", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 || q.Freshness != "fresh" || p.calls.Load() != calls+1 {
		t.Fatalf("hash change: %+v %v", q, err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	r, err := c.Sync(ctx)
	if err != nil || r.Removed != 1 {
		t.Fatalf("delete: %+v %v", r, err)
	}
	status, err := c.Status(ctx)
	if err != nil || status.Files != 0 || status.NeedsSync {
		t.Fatalf("status: %+v %v", status, err)
	}
}

func TestSyncFailureCooldownManualRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	p := &countedEmbedding{}
	c := openTest(t, t.TempDir(), p)
	root := c.WorkspaceDir()
	file := docPath(c, "note.txt")
	sourceFile(t, file, []byte("original evidence"))
	if _, err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, file, []byte("replaced evidence"))
	p.fail.Store(true)
	q, err := c.Query(ctx, "original", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 || q.Freshness != "stale" || q.SyncError == "" {
		t.Fatalf("stale: %+v %v", q, err)
	}
	calls := p.calls.Load()
	c.Close()
	c, err = Open(Options{WorkspaceDir: root, Embedder: p})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	q, err = c.Query(ctx, "original", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 || p.calls.Load() != calls || !strings.Contains(q.SyncError, "cooling") {
		t.Fatalf("cooldown after restart: %+v %v", q, err)
	}
	p.fail.Store(false)
	if r, err := c.Sync(ctx); err != nil || r.Indexed != 1 {
		t.Fatalf("manual retry: %+v %v", r, err)
	}
	status, _ := c.Status(ctx)
	if status.NeedsSync || len(status.FailedFiles) != 0 {
		t.Fatalf("repaired: %+v", status)
	}
}

func TestFirstIndexFailureAndUnavailableRootKeepExistingIndex(t *testing.T) {
	ctx := context.Background()
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	bad := docPath(c, "broken.tei.xml")
	sourceFile(t, bad, []byte("<broken/>"))
	if _, err := c.Query(ctx, "evidence", QueryOptions{Mode: "bm25"}); err == nil {
		t.Fatal("first indexing failure was hidden")
	}
	sourceFile(t, bad, []byte(`<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><p>repaired evidence</p></body></text></TEI>`))
	q, err := c.Query(ctx, "repaired", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 {
		t.Fatalf("changed failure input: %+v %v", q, err)
	}
	docs := filepath.Dir(bad)
	if err = os.Rename(docs, docs+"-offline"); err != nil {
		t.Fatal(err)
	}
	q, err = c.Query(ctx, "repaired", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 || q.Freshness != "unknown" || q.SyncError == "" {
		t.Fatalf("unavailable root: %+v %v", q, err)
	}
	listed, _ := c.ListDocuments(ctx)
	if len(listed) != 1 {
		t.Fatal("pruned inaccessible root")
	}
}

func TestCanonicalSwitchIsAtomicDespiteAnotherFailedDocument(t *testing.T) {
	ctx := context.Background()
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	md := docPath(c, "paper/full.md")
	sourceFile(t, md, []byte("oldmarker evidence"))
	if _, err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	jsonPath := docPath(c, "paper/paper_content_list.json")
	sourceFile(t, jsonPath, []byte(`[{"type":"text","text":"newmarker evidence","page_idx":2}]`))
	sourceFile(t, docPath(c, "bad.tei.xml"), []byte("<broken/>"))
	r, err := c.Sync(ctx)
	if err == nil || r.Failed != 1 || r.Indexed != 1 {
		t.Fatalf("partial canonical switch: %+v %v", r, err)
	}
	listed, _ := c.ListDocuments(ctx)
	if len(listed) != 1 || listed[0] != jsonPath {
		t.Fatalf("duplicate artifacts: %v", listed)
	}
	q, err := c.Query(ctx, "newmarker", QueryOptions{Mode: "bm25", DisableSync: true})
	if err != nil || len(q.Hits) != 1 || q.Hits[0].Chunk.PageStart == nil || *q.Hits[0].Chunk.PageStart != 3 {
		t.Fatalf("provenance: %+v %v", q, err)
	}
	q, err = c.Query(ctx, "oldmarker", QueryOptions{Mode: "bm25", DisableSync: true})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("old representation: %+v %v", q, err)
	}
}

func TestConfigReloadRebuildFailureAndGenerationReload(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := openTest(t, root, fakeEmbedding{})
	defer c.Close()
	peer, err := Open(Options{WorkspaceDir: root, Embedder: fakeEmbedding{}})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	file := docPath(c, "paper.tei.xml")
	valid := `<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><p>original evidence</p></body></text></TEI>`
	sourceFile(t, file, []byte(valid))
	if _, err = c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := c.Status(ctx)
	sourceFile(t, file, []byte("<broken/>"))
	if _, err = c.Rebuild(ctx); err == nil {
		t.Fatal("failed rebuild accepted")
	}
	after, _ := c.Status(ctx)
	if after.ActiveDB != before.ActiveDB {
		t.Fatal("failed rebuild replaced active DB")
	}
	cfg := DefaultConfig()
	cfg.Embedding.Model, cfg.Embedding.Dimensions = "fake", 2
	cfg.Chunking.Mode = "legacy"
	cfg.Chunking.SemanticTarget = 300
	if err = workspace.AtomicJSON(filepath.Join(workspace.Store(root), "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	status, err := peer.Status(ctx)
	if err != nil || !status.NeedsRebuild {
		t.Fatalf("config reload: %+v %v", status, err)
	}
	if _, err = peer.Query(ctx, "original", QueryOptions{Mode: "bm25"}); err == nil {
		t.Fatal("incompatible query accepted")
	}
	sourceFile(t, file, []byte(strings.ReplaceAll(valid, "original", "replacement")))
	if _, err = c.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	q, err := peer.Query(ctx, "replacement", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 1 {
		t.Fatalf("peer generation reload: %+v %v", q, err)
	}
	s, err := peer.Status(ctx)
	if err != nil || s.NeedsRebuild || s.Chunks != s.Vectors {
		t.Fatalf("coverage: %+v %v", s, err)
	}
}

func TestUnknownDatabaseRejectedWithoutMigration(t *testing.T) {
	root := t.TempDir()
	c := openTest(t, root, fakeEmbedding{})
	defer c.Close()
	db, err := store.Open(filepath.Join(workspace.Store(root), "rag.db"), false, 2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = c.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "recognized Go") {
		t.Fatal("unmarked database accepted", err)
	}
}

func TestInvalidQueryDoesNotSynchronizeAndCancellationStops(t *testing.T) {
	p := &countedEmbedding{}
	c := openTest(t, t.TempDir(), p)
	defer c.Close()
	sourceFile(t, docPath(c, "note.txt"), []byte("evidence"))
	for _, opts := range []QueryOptions{{Mode: "bad"}, {TopK: -1}, {RequireRerank: true}} {
		if _, err := c.Query(context.Background(), "evidence", opts); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	if p.calls.Load() != 0 {
		t.Fatal("invalid query embedded documents")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Query(ctx, "evidence", QueryOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c.Close()
	if _, err := c.Sync(context.Background()); err == nil {
		t.Fatal("closed writer accepted")
	}
}

func TestChineseBM25OriginalText(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	text := "本文研究多无人机信道估计方法 pilot"
	sourceFile(t, docPath(c, "note.txt"), []byte(text))
	for _, query := range []string{"信道估计", "无人机", "信", "信道估计 pilot"} {
		q, err := c.Query(context.Background(), query, QueryOptions{Mode: "bm25"})
		if err != nil || len(q.Hits) != 1 || q.Hits[0].Chunk.Content != text {
			t.Fatalf("%s: %+v %v", query, q, err)
		}
	}
	q, err := c.Query(context.Background(), "信道估计 absent", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("mixed: %+v %v", q, err)
	}
	sourceFile(t, docPath(c, "note.txt"), []byte("无人机轨迹跟踪"))
	q, err = c.Query(context.Background(), "信道估计", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("stale Chinese terms: %+v %v", q, err)
	}
}

func TestSavedStateHasOnlyWorkspaceInputs(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	sourceFile(t, docPath(c, "note.txt"), []byte("evidence"))
	if _, err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(workspace.Store(c.WorkspaceDir()), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["inputs"] == nil || fields["trackedPaths"] != nil || fields["sourcePaths"] != nil {
		t.Fatal(string(b))
	}
}
