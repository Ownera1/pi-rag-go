package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
)

func TestProviderErrorRedactsCredentialBeforeTruncation(t *testing.T) {
	key := strings.Repeat("private-credential-", 15)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte("invalid key: " + key))
	}))
	defer server.Close()
	p, err := NewHTTP(model.ProviderConfig{Type: "openai", Model: "test", Dimensions: 2, BaseURL: server.URL, APIKeyEnv: "RAG_TEST_PROVIDER_SECRET"}, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	p.SetCredential(key)
	_, err = p.EmbedQuery(context.Background(), "test")
	if err == nil || strings.Contains(err.Error(), "private-credential") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatal(err)
	}
}

func TestVoyageRolesAndOutOfOrderEmbeddings(t *testing.T) {
	roles := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
			Role  string   `json:"input_type"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
		}
		roles = append(roles, req.Role)
		if req.Role == "query" {
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[3,0]}]}`))
		} else {
			_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[0,4]},{"index":0,"embedding":[3,0]}]}`))
		}
	}))
	defer server.Close()
	p, e := NewHTTP(model.ProviderConfig{Type: "voyage", Model: "fake", Dimensions: 2, BaseURL: server.URL}, 3000, 0)
	if e != nil {
		t.Fatal(e)
	}
	q, e := p.EmbedQuery(context.Background(), "query")
	if e != nil || q[0] != 1 {
		t.Fatalf("query %v %v", q, e)
	}
	docs, e := p.EmbedDocuments(context.Background(), []string{"a", "b"})
	if e != nil || docs[0][0] != 1 || docs[1][1] != 1 {
		t.Fatalf("docs %v %v", docs, e)
	}
	if len(roles) != 2 || roles[0] != "query" || roles[1] != "document" {
		t.Fatal(roles)
	}
}

func TestConfiguredEmbeddingBatchSize(t *testing.T) {
	var mu sync.Mutex
	sizes := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		sizes = append(sizes, len(input.Input))
		mu.Unlock()
		data := []map[string]any{}
		for i := range input.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	cfg := model.ProviderConfig{Type: "openai", Model: "fake", Dimensions: 2, BaseURL: server.URL}
	p, err := NewHTTP(cfg, 3000, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := p.EmbedDocuments(context.Background(), []string{"1", "2", "3", "4", "5", "6", "7", "8"})
	mu.Lock()
	defer mu.Unlock()
	if err != nil || len(vectors) != 8 || !reflect.DeepEqual(sizes, []int{3, 3, 2}) {
		t.Fatalf("batching: %v %v", sizes, err)
	}
	for _, size := range []int{0, 257} {
		if _, err = NewHTTP(cfg, 3000, 0, size); err == nil {
			t.Fatalf("accepted batch %d", size)
		}
	}
}
