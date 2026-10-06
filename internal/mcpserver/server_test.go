package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testCore(t *testing.T, root string, readOnly bool) *rag.Core {
	t.Helper()
	dir := workspace.Store(root)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := rag.DefaultConfig()
	cfg.Documents = "documents"
	if err := workspace.AtomicJSON(filepath.Join(dir, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0700); err != nil {
		t.Fatal(err)
	}
	core, err := rag.Open(rag.Options{WorkspaceDir: root, ReadOnly: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	return core
}
func checkTools(t *testing.T, ctx context.Context, session *mcp.ClientSession, readOnly bool) {
	t.Helper()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Name == "rag_query" && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint != readOnly) {
			t.Fatal("wrong query read-only annotation")
		}
	}
	sort.Strings(names)
	want := []string{"rag_list_documents", "rag_query", "rag_status"}
	if !readOnly {
		want = append(want, "rag_rebuild", "rag_sync", "rag_zotero_sync", "rag_zotero_match", "rag_zotero_link")
		sort.Strings(want)
	}
	raw, _ := json.Marshal(names)
	expected, _ := json.Marshal(want)
	if string(raw) != string(expected) {
		t.Fatalf("tools %s != %s", raw, expected)
	}
	for _, tool := range []string{"rag_status", "rag_list_documents"} {
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{}})
		if err != nil || r.IsError || r.StructuredContent == nil {
			t.Fatalf("%s: %+v %v", tool, r, err)
		}
	}
	if readOnly {
		for _, tool := range []string{"rag_sync", "rag_rebuild", "rag_zotero_sync", "rag_zotero_match", "rag_zotero_link", "rag_index", "rag_clear", "rag_cleanup"} {
			r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"confirm": true}})
			if err == nil && !r.IsError {
				t.Fatal("readonly allowed " + tool)
			}
		}
	}
}
func TestHTTPReadOnlyToolBoundaryAndNoSync(t *testing.T) {
	root := t.TempDir()
	core := testCore(t, root, true)
	if err := os.WriteFile(filepath.Join(root, "documents", "new.txt"), []byte("pending evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(New(core)))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkTools(t, ctx, session, true)
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rag_query", Arguments: map[string]any{"query": "pending", "mode": "bm25"}})
	if err != nil || r.IsError {
		t.Fatalf("readonly query: %+v %v", r, err)
	}
	if _, err = os.Stat(filepath.Join(workspace.Store(root), "rag.db")); !os.IsNotExist(err) {
		t.Fatal("readonly query created DB")
	}
	status, err := core.Status(ctx)
	if err != nil || status.Files != 0 || !status.NeedsSync {
		t.Fatalf("readonly status: %+v %v", status, err)
	}
}
func TestLocalMCPToolsAndCancellation(t *testing.T) {
	core := testCore(t, t.TempDir(), false)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- New(core, ctx).Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkTools(t, ctx, session, false)
	if err := os.WriteFile(filepath.Join(core.WorkspaceDir(), "documents", "invalid.txt"), []byte{0, 1}, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rag_sync", Arguments: map[string]any{}})
	if err != nil || !r.IsError || r.StructuredContent == nil {
		t.Fatalf("missing partial failure report: %+v %v", r, err)
	}
	b, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var report rag.IndexResult
	if err = json.Unmarshal(b, &report); err != nil || report.Failed != 1 || len(report.Failures) != 1 {
		t.Fatalf("failure report: %s %v", b, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP shutdown blocked")
	}
}
func TestHTTPHostOriginBoundary(t *testing.T) {
	handler := Handler(New(testCore(t, t.TempDir(), true)))
	for _, c := range []struct{ host, origin string }{{"attacker.example", ""}, {"8.8.8.8:7331", ""}, {"127.0.0.1:7331", "https://attacker.example"}} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7331/mcp", nil)
		r.Host = c.host
		r.Header.Set("Origin", c.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("host/origin accepted: %+v %d", c, w.Code)
		}
	}
}

func statusRoot(t *testing.T, ctx context.Context, session *mcp.ClientSession, args map[string]any) (string, *mcp.CallToolResult) {
	t.Helper()
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rag_status", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		return "", r
	}
	b, _ := json.Marshal(r.StructuredContent)
	var status rag.Status
	if err = json.Unmarshal(b, &status); err != nil {
		t.Fatal(err)
	}
	return status.WorkspaceDir, r
}

func TestDynamicServerResolvesWorkspacePerCall(t *testing.T) {
	a, b := testCore(t, t.TempDir(), false).WorkspaceDir(), testCore(t, t.TempDir(), false).WorkspaceDir()
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = NewDynamic(false, ctx).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("server outside a workspace: %+v %v", tools, err)
	}
	if _, r := statusRoot(t, ctx, session, map[string]any{}); r == nil || !r.IsError {
		t.Fatal("call outside a workspace succeeded")
	}
	if root, r := statusRoot(t, ctx, session, map[string]any{"workspace": filepath.Join(a, "documents")}); root != a {
		t.Fatalf("subdirectory argument: %q %+v", root, r)
	}
	if root, r := statusRoot(t, ctx, session, map[string]any{"workspace": b}); root != b {
		t.Fatalf("second workspace: %q %+v", root, r)
	}
	t.Chdir(filepath.Join(b, "documents"))
	if root, r := statusRoot(t, ctx, session, map[string]any{}); root != b {
		t.Fatalf("working directory discovery: %q %+v", root, r)
	}
}

func TestFixedServerRejectsOtherWorkspaces(t *testing.T) {
	core, other := testCore(t, t.TempDir(), false), testCore(t, t.TempDir(), false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = New(core, ctx).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if root, _ := statusRoot(t, ctx, session, map[string]any{"workspace": core.WorkspaceDir()}); root != core.WorkspaceDir() {
		t.Fatal("own workspace rejected")
	}
	if _, r := statusRoot(t, ctx, session, map[string]any{"workspace": other.WorkspaceDir()}); r == nil || !r.IsError {
		t.Fatal("pinned server served another workspace")
	}
}
