package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ownera1/pi-rag-go/internal/evaluate"
	"github.com/Ownera1/pi-rag-go/internal/mcpserver"
	"github.com/Ownera1/pi-rag-go/pkg/rag"
)

// This deterministic model only proves the evaluator's HTTP/MCP integration.
// Its one-hot vectors are not evidence of real embedding or reranker quality.
func TestSampleThroughHTTPModelsAndMCP(t *testing.T) {
	dataset := filepath.Join("..", "..", "evaluation", "sample", "questions.jsonl")
	f, err := os.Open(dataset)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := evaluate.ReadCases(f)
	f.Close()
	if err != nil || len(cases) != 20 {
		t.Fatalf("sample: %d cases, %v", len(cases), err)
	}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embeddings" {
			var input struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				return
			}
			data := []map[string]any{}
			for index, text := range input.Input {
				vector := make([]float32, len(cases))
				for i, c := range cases {
					if strings.Contains(text, c.Query) {
						vector[i] = 1
					}
				}
				data = append(data, map[string]any{"index": index, "embedding": vector})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		if r.URL.Path == "/rerank" {
			var input struct {
				Query     string   `json:"query"`
				Documents []string `json:"documents"`
				TopN      int      `json:"top_n"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				return
			}
			results := []map[string]any{}
			for i, text := range input.Documents {
				if strings.Contains(text, input.Query) {
					results = append(results, map[string]any{"index": i, "relevance_score": 1})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
			return
		}
		http.NotFound(w, r)
	}))
	defer modelServer.Close()
	root := t.TempDir()
	cfg := rag.DefaultConfig()
	cfg.Embedding = rag.ProviderConfig{Type: "openai", Model: "synthetic", Dimensions: len(cases), BaseURL: modelServer.URL}
	cfg.Reranker = rag.ProviderConfig{Type: "http", Model: "synthetic", BaseURL: modelServer.URL}
	cfg.Indexing.EmbeddingBatchSize = 3
	b, _ := json.Marshal(cfg)
	if err = os.WriteFile(filepath.Join(root, "config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	core, err := rag.Open(rag.Options{StoreDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	indexed, err := core.Index(context.Background(), []string{filepath.Join("..", "..", "evaluation", "sample", "corpus")})
	if err != nil || indexed.Failed != 0 || indexed.Indexed != 20 {
		t.Fatalf("index: %+v %v", indexed, err)
	}
	server := httptest.NewServer(mcpserver.Handler(mcpserver.New(core)))
	defer server.Close()
	var stdout bytes.Buffer
	output := filepath.Join(t.TempDir(), "nested", "report.json")
	if err = run([]string{"--endpoint", server.URL, "--dataset", dataset, "--modes", "bm25,vector,hybrid,rerank", "--output", output}, &stdout); err != nil {
		t.Fatal(err)
	}
	var report evaluate.Report
	if err = json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Summaries) != 4 {
		t.Fatalf("report: %+v", report)
	}
	for _, summary := range report.Summaries {
		if summary.Cases != 20 || summary.Failed != 0 || summary.RecallAtK != 1 || summary.MRR != 1 || summary.Degraded != 0 {
			t.Fatalf("mode %s: %+v", summary.Mode, summary)
		}
		if summary.Mode == "bm25" && (summary.Usage.EmbeddingCalls != 0 || summary.Usage.RerankCalls != 0) {
			t.Fatal("BM25 called model")
		}
		if summary.Mode != "bm25" && summary.Usage.EmbeddingCalls != 20 {
			t.Fatalf("embedding usage: %+v", summary.Usage)
		}
		if summary.Mode == "rerank" && summary.Usage.RerankCalls != 20 {
			t.Fatalf("rerank usage: %+v", summary.Usage)
		}
	}
	saved, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(saved, stdout.Bytes()) {
		t.Fatalf("saved report differs: %v", err)
	}
}
