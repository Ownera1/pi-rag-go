package rag

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHanBM25RefreshAndOriginalText(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := openTest(t, filepath.Join(root, "store"), fakeEmbedding{})
	defer c.Close()
	file := filepath.Join(root, "source.txt")
	text := "本文研究多无人机信道估计方法 pilot"
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if r, e := c.Index(ctx, []string{file}); e != nil || r.Failed > 0 {
		t.Fatalf("index: %+v %v", r, e)
	}
	for _, query := range []string{"信道估计", "无人机", "信", "信道估计 pilot"} {
		q, err := c.Query(ctx, query, QueryOptions{Mode: "bm25", DisableRerank: true})
		if err != nil || len(q.Hits) != 1 || q.Hits[0].Chunk.Content != text {
			t.Fatalf("query %q: %+v %v", query, q, err)
		}
	}
	q, err := c.Query(ctx, "信道估计 absent", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("mixed query did not require English term: %+v %v", q, err)
	}
	if err = os.WriteFile(file, []byte("无人机轨迹跟踪"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, e := c.Refresh(ctx); e != nil || r.Failed > 0 {
		t.Fatalf("refresh: %+v %v", r, e)
	}
	q, err = c.Query(ctx, "信道估计", QueryOptions{Mode: "bm25"})
	if err != nil || len(q.Hits) != 0 {
		t.Fatalf("stale Han entry: %+v %v", q, err)
	}
}

func TestChangedChunkingRequiresRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	cfg := DefaultConfig()
	cfg.Embedding.Model, cfg.Embedding.Dimensions = "fake", 2
	c := configuredCore(t, dir, cfg, fakeEmbedding{})
	file := filepath.Join(root, "source.txt")
	if err := os.WriteFile(file, []byte("stable evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, e := c.Index(ctx, []string{file}); e != nil || r.Failed > 0 {
		t.Fatalf("index: %+v %v", r, e)
	}
	c.Close()
	cfg.Chunking.SemanticTarget = 300
	c = configuredCore(t, dir, cfg, fakeEmbedding{})
	defer c.Close()
	s, err := c.Status(ctx)
	if err != nil || !s.NeedsRebuild {
		t.Fatalf("missing rebuild boundary: %+v %v", s, err)
	}
	if _, err = c.Query(ctx, "stable evidence", QueryOptions{}); err == nil {
		t.Fatal("queried mismatched processing contract")
	}
	if r, e := c.Rebuild(ctx); e != nil || r.Failed > 0 {
		t.Fatalf("rebuild: %+v %v", r, e)
	}
	s, err = c.Status(ctx)
	if err != nil || s.NeedsRebuild {
		t.Fatalf("rebuild compatibility: %+v %v", s, err)
	}
}

func TestEmptyStoreStillValidatesQueryAndModes(t *testing.T) {
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	defer c.Close()
	for _, opts := range []QueryOptions{{Mode: "invalid"}, {TopK: -1}, {RequireRerank: true}} {
		if _, err := c.Query(context.Background(), "evidence", opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
}
