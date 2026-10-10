package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	c := configuredCore(t, root, cfg, nil)
	defer c.Close()
	p := docPath(c, "source.txt")
	sourceFile(t, p, []byte("stable evidence"))
	if r, e := c.Sync(context.Background()); e != nil || r.Failed > 0 {
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

func TestQueryEmbeddingFailureFallsBackOnlyWhenTransient(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		fallback bool
	}{
		{"server error", http.StatusServiceUnavailable, true},
		{"client error naming a connection", http.StatusBadRequest, false},
		{"refused connection", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
					http.Error(w, "invalid connection settings", tc.status)
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
			c := configuredCore(t, root, cfg, nil)
			defer c.Close()
			sourceFile(t, docPath(c, "source.txt"), []byte("stable evidence"))
			if r, e := c.Sync(context.Background()); e != nil || r.Failed > 0 {
				t.Fatalf("index %+v %v", r, e)
			}
			if tc.status == 0 {
				server.Close()
			}
			q, err := c.Query(context.Background(), "stable evidence", QueryOptions{})
			if fell := err == nil && q.Method == "bm25-fallback"; fell != tc.fallback {
				t.Fatalf("fallback %v, want %v: %+v %v", fell, tc.fallback, q, err)
			}
		})
	}
}
