package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

func initTest(t *testing.T, root string, extra ...string) {
	t.Helper()
	args := append([]string{"init", root, "--offline"}, extra...)
	var out, stderr bytes.Buffer
	if err := Run(context.Background(), args, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatalf("init: %s %s %v", out.String(), stderr.String(), err)
	}
}
func TestInitDiscoveryNestedWorkspacesExternalDocsAndDamagedConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project with spaces")
	external := t.TempDir()
	initTest(t, root, "--docs", external)
	cfgPath := filepath.Join(workspace.Store(root), "config.json")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	initTest(t, root)
	after, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(before, after) {
		t.Fatal("reinitialization reset configuration")
	}
	nested := filepath.Join(root, "src", "nested")
	if err = os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	found, err := workspace.Discover("")
	if err != nil || found != root {
		t.Fatalf("discovery: %s %v", found, err)
	}
	var out, stderr bytes.Buffer
	if err = Run(context.Background(), []string{"status"}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var status struct {
		DocumentsRoot string `json:"documentsRoot"`
	}
	if err = json.Unmarshal(out.Bytes(), &status); err != nil || status.DocumentsRoot != external {
		t.Fatalf("external docs: %s %v", out.String(), err)
	}
	if _, err = workspace.Discover(nested); err == nil {
		t.Fatal("explicit root searched parents")
	}
	initTest(t, nested)
	found, err = workspace.Discover("")
	if err != nil || found != nested {
		t.Fatalf("nested isolation: %s %v", found, err)
	}
	damaged := filepath.Join(workspace.Store(nested), "config.json")
	bad := []byte("{bad")
	if err = os.WriteFile(damaged, bad, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = Run(context.Background(), []string{"init", "--offline"}, strings.NewReader(""), &out, &stderr); err == nil {
		t.Fatal("overwrote damaged config")
	}
	preserved, _ := os.ReadFile(damaged)
	if !bytes.Equal(bad, preserved) {
		t.Fatal("damaged file modified")
	}
	if err = Run(context.Background(), []string{"status"}, strings.NewReader(""), &out, &stderr); err == nil {
		t.Fatal("damaged nested workspace fell back")
	}
}
func TestInitUnknownStoreAndCredentialPrivacy(t *testing.T) {
	root := t.TempDir()
	dir := workspace.Store(root)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "rag.db"), false, 2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out, stderr bytes.Buffer
	if err = Run(context.Background(), []string{"init", root, "--offline"}, strings.NewReader(""), &out, &stderr); err == nil {
		t.Fatal("unknown DB accepted")
	}
	if _, err = os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("config published into unknown database")
	}
	root = t.TempDir()
	t.Setenv("RAG_TEST_INIT_KEY", "fixture-private-key")
	initTest(t, root, "--api-key-env", "RAG_TEST_INIT_KEY")
	credential := filepath.Join(workspace.Store(root), "credentials.json")
	st, err := os.Stat(credential)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("credential mode: %v %v", st, err)
	}
	b, _ := os.ReadFile(filepath.Join(workspace.Store(root), "config.json"))
	if strings.Contains(string(b), "fixture-private-key") {
		t.Fatal("key in config")
	}
}
func TestConnectCodexMergesProjectConfigAndProtectsConflicts(t *testing.T) {
	root := t.TempDir()
	initTest(t, root)
	directory := filepath.Join(root, ".codex")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	original := []byte("# keep this comment\nmodel = 'existing-model'\n[mcp_servers.other]\nurl = 'https://example.invalid/mcp'\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	unused := func(context.Context, string, string, ...string) ([]byte, error) {
		t.Fatal("Codex registration should be project scoped")
		return nil, nil
	}
	args := []string{"codex", "--workspace", root}
	exe := "/fixture/path with spaces/rag"
	if err := connect(context.Background(), args, &out, &stderr, unused, exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !bytes.HasPrefix(b, original) {
		t.Fatalf("project config rewritten:\n%s", b)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "existing-model" {
		t.Fatal("unrelated config lost")
	}
	servers := cfg["mcp_servers"].(map[string]any)
	if servers["other"] == nil {
		t.Fatal("other MCP lost")
	}
	entry := servers["rag-go"].(map[string]any)
	if entry["command"] != exe || !reflect.DeepEqual(stringArray(entry["args"]), []string{"mcp", "--workspace", root}) {
		t.Fatalf("wrong launch: %+v", entry)
	}
	if err := connect(context.Background(), args, &out, &stderr, unused, exe); err != nil {
		t.Fatal(err)
	}
	same, _ := os.ReadFile(path)
	if !bytes.Equal(b, same) {
		t.Fatal("idempotent connect rewrote config")
	}
	if err := connect(context.Background(), args, &out, &stderr, unused, "/different/rag"); err == nil {
		t.Fatal("conflict replaced silently")
	}
	if err := connect(context.Background(), append(args, "--replace"), &out, &stderr, unused, "/different/rag"); err != nil {
		t.Fatal(err)
	}
	bad := []byte("[broken")
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err := connect(context.Background(), args, &out, &stderr, unused, exe); err == nil {
		t.Fatal("bad TOML overwritten")
	}
	same, _ = os.ReadFile(path)
	if !bytes.Equal(bad, same) {
		t.Fatal("bad TOML changed")
	}
	// A symlinked config is edited through the link, not replaced.
	shared := filepath.Join(t.TempDir(), "shared.toml")
	if err := os.WriteFile(shared, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, path); err != nil {
		t.Fatal(err)
	}
	if err := connect(context.Background(), args, &out, &stderr, unused, exe); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(path); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	if b, _ := os.ReadFile(shared); !bytes.Contains(b, []byte("rag-go")) {
		t.Fatalf("shared config not updated: %s", b)
	}
}
func TestConnectClaudeUsesWorkspaceLocalScope(t *testing.T) {
	root := t.TempDir()
	initTest(t, root)
	calls := [][]string{}
	execute := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if dir != root || name != "claude" {
			t.Fatalf("scope cwd: %s %s", dir, name)
		}
		calls = append(calls, append([]string{}, args...))
		if args[1] == "get" {
			return []byte("No MCP server found"), errors.New("not found")
		}
		return nil, nil
	}
	var out, stderr bytes.Buffer
	if err := connect(context.Background(), []string{"claude", "--workspace", root}, &out, &stderr, execute, "/absolute/rag"); err != nil {
		t.Fatal(err)
	}
	want := []string{"mcp", "add", "--transport", "stdio", "--scope", "local", "rag-go", "--", "/absolute/rag", "mcp", "--workspace", root}
	if len(calls) != 2 || !reflect.DeepEqual(calls[1], want) {
		t.Fatalf("registration: %v", calls)
	}
}
func TestInitProviderProbeAndCLIErrorPaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 0}}}})
	}))
	defer server.Close()
	root := t.TempDir()
	var out, stderr bytes.Buffer
	args := []string{"init", root, "--embedding-type", "openai", "--model", "fixture", "--base-url", server.URL, "--dimensions", "2", "--api-key-env="}
	if err := Run(context.Background(), args, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"service"}, {"prep"}, {"add"}, {"query", "--workspace", root, "--mode", "invalid", "q"}, {"mcp", "--workspace", root, "--transport", "bad"}, {"mcp", "--workspace", root, "--transport", "http", "--listen", "0.0.0.0:0"}} {
		if err := Run(context.Background(), args, strings.NewReader(""), &out, &stderr); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
