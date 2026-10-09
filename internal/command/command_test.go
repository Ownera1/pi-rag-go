package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	// An existing, possibly cloned, config naming another endpoint gets no
	// environment key on a later init.
	cfg := map[string]any{}
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	embedding := cfg["embedding"].(map[string]any)
	embedding["baseUrl"], embedding["apiKeyEnv"] = "https://attacker.invalid/v1", "RAG_TEST_STOLEN_KEY"
	if err = workspace.AtomicJSON(filepath.Join(workspace.Store(root), "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAG_TEST_STOLEN_KEY", "fixture-stolen-key")
	initTest(t, root)
	if b, _ = os.ReadFile(credential); strings.Contains(string(b), "fixture-stolen-key") {
		t.Fatal("environment key saved for an untrusted endpoint")
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

func TestWorkspaceRegistryNamesAllAndRemove(t *testing.T) {
	t.Setenv("RAG_GO_CONFIG_DIR", t.TempDir())
	base := t.TempDir()
	a, b, c := filepath.Join(base, "x", "papers"), filepath.Join(base, "y", "papers"), filepath.Join(base, "notes")
	for _, root := range []string{a, b, c, a} {
		initTest(t, root)
	}
	run := func(args ...string) (string, error) {
		var out, stderr bytes.Buffer
		err := Run(context.Background(), args, strings.NewReader(""), &out, &stderr)
		return out.String(), err
	}
	names := func() []string {
		out, err := run("workspace", "list")
		if err != nil {
			t.Fatal(err)
		}
		var list []workspace.Entry
		if err = json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, e := range list {
			got = append(got, e.Name)
		}
		return got
	}
	if got := names(); !reflect.DeepEqual(got, []string{"x/papers", "y/papers", "notes"}) {
		t.Fatalf("names %v", got)
	}
	t.Chdir(base)
	if _, err := run("status"); !errors.Is(err, workspace.ErrNotFound) || !strings.Contains(err.Error(), "x/papers, y/papers, notes") {
		t.Fatalf("outside a workspace: %v", err)
	}
	if out, err := run("status", "-w", "notes"); err != nil || !strings.Contains(out, `"workspaceDir":"`+c+`"`) {
		t.Fatalf("status -w notes: %s %v", out, err)
	}
	if _, err := run("status", "--all", "-w", "notes"); err == nil {
		t.Fatal("accepted --all with -w")
	}
	if err := os.RemoveAll(workspace.Store(b)); err != nil {
		t.Fatal(err)
	}
	out, err := run("sync", "--all")
	if err == nil || err.Error() != "1 of 3 workspaces failed" {
		t.Fatalf("sync --all: %v", err)
	}
	var results []workspaceResult
	if err = json.Unmarshal([]byte(out), &results); err != nil || len(results) != 3 || results[0].Error != "" || results[1].Error == "" || results[2].Error != "" {
		t.Fatalf("sync --all results: %s %v", out, err)
	}
	if _, err = run("workspace", "remove", "y/papers"); err != nil {
		t.Fatal(err)
	}
	if got := names(); !reflect.DeepEqual(got, []string{"papers", "notes"}) {
		t.Fatalf("names after remove %v", got)
	}
	if _, err = run("sync", "--all"); err != nil {
		t.Fatal(err)
	}
	if _, err = run("workspace", "remove", "y/papers"); err == nil {
		t.Fatal("removed an unregistered workspace")
	}
}

func TestEvalReportEnvironmentAndDefaultModes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input, Documents []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, results := []any{}, []any{}
		for i := range body.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		for i := range body.Documents {
			results = append(results, map[string]any{"index": i, "relevance_score": 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "results": results})
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "paper.md"), []byte("# Method\n\nThe channel prior is updated each iteration.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	initTest(t, root, "--embedding-type", "openai", "--model", "fixture", "--base-url", server.URL, "--dimensions", "2", "--api-key-env=")
	line := `{"id":"q1","query":"channel prior","relevant":[{"pathSuffix":"paper.md","contains":"channel prior"}]}` + "\n"
	dataset := filepath.Join(t.TempDir(), "q.jsonl")
	if err := os.WriteFile(dataset, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"sync", "-w", root}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatalf("sync: %s %v", stderr.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"eval", "--workspace", root, "--dataset", dataset, "--modes", "bm25"}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatalf("eval: %s %v", stderr.String(), err)
	}
	var report struct {
		Environment struct {
			Version       string
			Config        struct{ Embedding struct{ Model string } }
			ActiveDB      string
			Documents     map[string]string
			DatasetSHA256 string
		}
		Summaries []struct{ RecallAtK float64 }
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	env := report.Environment
	if env.Version == "" || env.Config.Embedding.Model != "fixture" || env.ActiveDB == "" || len(env.Documents) != 1 || report.Summaries[0].RecallAtK != 1 {
		t.Fatalf("environment %+v summaries %+v", env, report.Summaries)
	}
	for _, v := range env.Documents {
		if v == "" {
			t.Fatal("empty document version")
		}
	}
	if env.DatasetSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(line))) {
		t.Fatalf("dataset hash %s", env.DatasetSHA256)
	}
	// Without --modes, rerank joins the baselines once a reranker is configured.
	modes := func() string {
		t.Helper()
		out.Reset()
		if err := Run(context.Background(), []string{"eval", "--workspace", root, "--dataset", dataset}, strings.NewReader(""), &out, &stderr); err != nil {
			t.Fatalf("eval: %s %v", stderr.String(), err)
		}
		var r struct{ Summaries []struct{ Mode string } }
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, s := range r.Summaries {
			got = append(got, s.Mode)
		}
		return strings.Join(got, ",")
	}
	if got := modes(); got != "bm25,vector,hybrid" {
		t.Fatalf("default modes without a reranker: %s", got)
	}
	path := filepath.Join(workspace.Store(root), "config.json")
	b, err := os.ReadFile(path)
	var cfg map[string]any
	if err == nil {
		err = json.Unmarshal(b, &cfg)
	}
	if err != nil {
		t.Fatal(err)
	}
	cfg["reranker"] = map[string]any{"type": "http", "model": "fixture", "baseUrl": server.URL}
	if b, err = json.Marshal(cfg); err == nil {
		err = os.WriteFile(path, b, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := modes(); got != "bm25,vector,hybrid,rerank" {
		t.Fatalf("default modes with a reranker: %s", got)
	}
}
