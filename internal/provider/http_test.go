package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

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
