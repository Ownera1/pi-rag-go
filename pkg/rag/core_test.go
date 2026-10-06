package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	// Any shared term recalls the chunk, including questions and unmatched words.
	for _, query := range []string{"信道估计", "无人机", "信", "信道估计 pilot", "信道估计 absent", "如何进行信道估计？"} {
		q, err := c.Query(context.Background(), query, QueryOptions{Mode: "bm25"})
		if err != nil || len(q.Hits) != 1 || q.Hits[0].Chunk.Content != text {
			t.Fatalf("%s: %+v %v", query, q, err)
		}
	}
	q, err := c.Query(context.Background(), "卫星 absent", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("unrelated: %+v %v", q, err)
	}
	sourceFile(t, docPath(c, "note.txt"), []byte("无人机轨迹跟踪"))
	q, err = c.Query(context.Background(), "信道估计", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("stale Chinese terms: %+v %v", q, err)
	}
}

func TestBM25RecallsQuestionsAndRanksSharedTerms(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	sourceFile(t, docPath(c, "full.txt"), []byte("Channel estimation uses pilots in OFDM receivers."))
	sourceFile(t, docPath(c, "partial.txt"), []byte("Receivers decode each channel symbol after synchronization."))
	sourceFile(t, docPath(c, "full-cn.txt"), []byte("导频辅助的信道估计降低了误码率"))
	sourceFile(t, docPath(c, "partial-cn.txt"), []byte("信道编码提升了可靠性"))
	for query, best := range map[string]string{
		"how does channel estimation work with pilots?": "full.txt",
		"如何利用导频进行信道估计":                                  "full-cn.txt",
	} {
		q, err := c.Query(context.Background(), query, QueryOptions{Mode: "bm25", TopK: 5})
		// The weaker match is retained below the stronger one.
		if err != nil || len(q.Hits) != 2 || filepath.Base(q.Hits[0].Chunk.Path) != best {
			t.Fatalf("%s: %+v %v", query, q, err)
		}
	}
}

func TestSnapshotReusesFingerprintsOfUnchangedInputs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of permissions")
	}
	ctx := context.Background()
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	settled, fresh := docPath(c, "settled.txt"), docPath(c, "fresh.txt")
	sourceFile(t, settled, []byte("settled evidence"))
	sourceFile(t, fresh, []byte("fresh evidence"))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(settled, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(workspace.Store(c.WorkspaceDir()), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved savedState
	if err = json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.Fingerprints[settled]; !ok {
		t.Fatal("settled input not cached", string(b))
	}
	if _, ok := saved.Fingerprints[fresh]; ok {
		t.Fatal("recently modified input cached", string(b))
	}
	// An unreadable but unchanged file proves the cached hash is used.
	if err = os.Chmod(settled, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(settled, 0600)
	s, err := c.Status(ctx)
	if err != nil || s.NeedsSync || s.FreshnessError != "" {
		t.Fatalf("cached input reread: %+v %v", s, err)
	}
	if err = os.Chmod(settled, 0600); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, settled, []byte("changed evidence"))
	if s, err = c.Status(ctx); err != nil || !s.NeedsSync {
		t.Fatalf("change missed: %+v %v", s, err)
	}
}

func TestMinerUParagraphsChunkTogetherWithinPage(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	items := []string{}
	for page := range 2 {
		for i := range 4 {
			items = append(items, fmt.Sprintf(`{"type":"text","text":"page%d paragraph %d short evidence.","page_idx":%d}`, page, i, page))
		}
	}
	sourceFile(t, docPath(c, "paper/paper_content_list.json"), []byte("["+strings.Join(items, ",")+"]"))
	if _, err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	q, err := c.Query(context.Background(), "evidence", QueryOptions{Mode: "bm25", TopK: 10})
	if err != nil || len(q.Hits) != 2 {
		t.Fatalf("want one chunk per page: %+v %v", q, err)
	}
	for _, h := range q.Hits {
		page := *h.Chunk.PageStart
		if *h.Chunk.PageEnd != page || strings.Count(h.Chunk.Content, fmt.Sprintf("page%d", page-1)) != 4 {
			t.Fatalf("page provenance: %+v", h.Chunk)
		}
	}
}

// markerEmbedding places each document by a marker word, and the query at [1,0].
type markerEmbedding struct{ fakeEmbedding }

func (markerEmbedding) EmbedDocuments(_ context.Context, in []string) ([][]float32, error) {
	markers := map[string][]float32{"zc1": {1, 0}, "zb8": {0.8, 0.6}, "zd5": {0.5, 0.866}, "za0": {0, 1}}
	out := make([][]float32, len(in))
	for i, text := range in {
		for marker, v := range markers {
			if strings.Contains(text, marker) {
				out[i] = v
			}
		}
	}
	return out, nil
}

func TestHybridFusionRewardsAgreementAcrossRetrievers(t *testing.T) {
	c := openTest(t, t.TempDir(), markerEmbedding{})
	defer c.Close()
	// BM25 order: a, b. Vector order: c, b, d1, d2, a.
	for name, text := range map[string]string{
		"a.txt":  "kalman filter tracking kalman filter za0",
		"b.txt":  "kalman smoothing zb8",
		"c.txt":  "recursive state estimation zc1",
		"d1.txt": "orbit propagation zd5",
		"d2.txt": "orbit maneuver zd5",
	} {
		sourceFile(t, docPath(c, name), []byte(text))
	}
	top := func(alpha float64) Hit {
		t.Helper()
		q, err := c.Query(context.Background(), "kalman filter", QueryOptions{Alpha: &alpha})
		if err != nil || q.Method != "hybrid" || len(q.Hits) == 0 {
			t.Fatalf("alpha %v: %+v %v", alpha, q, err)
		}
		return q.Hits[0]
	}
	// b ranks second in both lists and beats each single-list winner.
	if h := top(0.5); filepath.Base(h.Chunk.Path) != "b.txt" || h.BM25 <= 0 || math.Abs(h.Vector-0.8) > 1e-3 {
		t.Fatalf("agreement: %+v", h)
	}
	if h := top(1); filepath.Base(h.Chunk.Path) != "a.txt" {
		t.Fatalf("BM25 weight: %+v", h)
	}
	if h := top(0); filepath.Base(h.Chunk.Path) != "c.txt" {
		t.Fatalf("vector weight: %+v", h)
	}
}

type concurrentEmbedding struct {
	fakeEmbedding
	active, peak atomic.Int32
}

func (p *concurrentEmbedding) EmbedDocuments(ctx context.Context, in []string) ([][]float32, error) {
	n := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		old := p.peak.Load()
		if n <= old || p.peak.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
	return p.fakeEmbedding.EmbedDocuments(ctx, in)
}

func TestIndexingEmbedsDocumentsConcurrentlyWithinLimit(t *testing.T) {
	p := &concurrentEmbedding{}
	cfg := DefaultConfig()
	cfg.Embedding.Model, cfg.Embedding.Dimensions = "fake", 2
	cfg.Chunking.Mode = "legacy"
	cfg.Indexing.EmbeddingWorkers = 3
	c := configuredCore(t, t.TempDir(), cfg, p)
	defer c.Close()
	for i := range 8 {
		sourceFile(t, docPath(c, fmt.Sprintf("note-%d.txt", i)), []byte(fmt.Sprintf("evidence %d", i)))
	}
	r, err := c.Sync(context.Background())
	if err != nil || r.Indexed != 8 {
		t.Fatalf("%+v %v", r, err)
	}
	if peak := p.peak.Load(); peak < 2 || peak > 3 {
		t.Fatalf("embedding concurrency %d, want 2..3", peak)
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
