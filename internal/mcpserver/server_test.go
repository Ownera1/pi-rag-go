package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Ownera1/pi-rag-go/pkg/rag"
)

func TestHTTPClientCanCallStatus(t *testing.T) {
	core, e := rag.Open(rag.Options{StoreDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer core.Close()
	server := httptest.NewServer(Handler(New(core)))
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL, DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	tools, e := session.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("got %d tools", len(tools.Tools))
	}
	result, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "rag_status", Arguments: map[string]any{}})
	if e != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("status: %+v %v", result, e)
	}
	for _, args := range []map[string]any{{}, {"dry_run": true, "confirm": true}, {"confirm": true}} {
		result, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "rag_cleanup", Arguments: args})
		if e != nil || result.IsError {
			t.Fatalf("cleanup: %+v %v", result, e)
		}
		b, _ := json.Marshal(result.StructuredContent)
		var output rag.CleanupResult
		if e = json.Unmarshal(b, &output); e != nil {
			t.Fatal(e)
		}
		wantPreview := args["confirm"] != true || args["dry_run"] == true
		if output.DryRun != wantPreview {
			t.Fatalf("cleanup preview: %+v, args=%+v", output, args)
		}
	}
}

func TestHTTPIndexAndQueryWithModelService(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
			t.Error(e)
		}
		items := make([]map[string]any, len(input.Input))
		for i := range items {
			items[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	defer modelServer.Close()
	root := t.TempDir()
	sources := t.TempDir()
	path := filepath.Join(sources, "source.tei.xml")
	xml := `<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><div><head>Method</head>` +
		`<p>channel evidence for shared agents</p></div></body></text></TEI>`
	if e := os.WriteFile(path, []byte(xml), 0600); e != nil {
		t.Fatal(e)
	}
	cfg := rag.DefaultConfig()
	cfg.Embedding = rag.ProviderConfig{Type: "openai", Model: "fake", Dimensions: 2, BaseURL: modelServer.URL}
	b, _ := json.Marshal(cfg)
	if e := os.WriteFile(filepath.Join(root, "config.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	core, e := rag.Open(rag.Options{StoreDir: root})
	if e != nil {
		t.Fatal(e)
	}
	defer core.Close()
	server := httptest.NewServer(Handler(New(core)))
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL, DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	indexed, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "rag_index", Arguments: map[string]any{"paths": []string{sources}}})
	if e != nil || indexed.IsError {
		t.Fatalf("index: %+v %v", indexed, e)
	}
	queried, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "rag_query", Arguments: map[string]any{"query": "channel evidence"}})
	if e != nil || queried.IsError {
		t.Fatalf("query: %s %v", queried.Content[0].(*mcp.TextContent).Text, e)
	}
	raw, _ := json.Marshal(queried.StructuredContent)
	if !strings.Contains(string(raw), "channel evidence for shared agents") {
		t.Fatalf("missing source: %s", raw)
	}
}

func TestProxyForwardsRemoteTools(t *testing.T) {
	core, err := rag.Open(rag.Options{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	remote := httptest.NewServer(Handler(New(core)))
	defer remote.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- proxy(ctx, remote.URL, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-proxy", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("proxy tools=%+v err=%v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rag_status", Arguments: map[string]any{}})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("proxy status=%+v err=%v", result, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not stop")
	}
}
