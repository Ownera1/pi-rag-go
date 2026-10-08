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

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// embeddingServer answers OpenAI-compatible embedding requests with unit vectors.
func embeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		data := []any{}
		for i := range body.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)
	return server
}

type fakeClaude struct {
	calls      [][]string
	registered string // claude mcp get output once registered
}

func (f *fakeClaude) execute(_ context.Context, _, name string, args ...string) ([]byte, error) {
	if name != "claude" {
		return nil, errors.New("unexpected " + name)
	}
	f.calls = append(f.calls, append([]string{}, args...))
	switch args[1] {
	case "get":
		if f.registered == "" {
			return []byte(`No MCP server named "rag-go".`), errors.New("exit 1")
		}
		return []byte(f.registered), nil
	case "add":
		f.registered = "rag-go:\n  Scope: User config (available in all your projects)\n  Command: " + args[len(args)-2] + "\n  Args: " + args[len(args)-1] + "\n"
	case "remove":
		f.registered = ""
	}
	return nil, nil
}

func testHost(t *testing.T, claude *fakeClaude) host {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RAG_GO_CONFIG_DIR", filepath.Join(home, "rag-config"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	found := func(string) (string, error) { return "/bin/agent", nil }
	return host{execute: claude.execute, exe: "/opt/rag path/rag", home: home, lookPath: found}
}

func providerArgs(server *httptest.Server) []string {
	return []string{"--embedding-type", "openai", "--model", "fixture", "--base-url", server.URL, "--dimensions", "2", "--api-key-env", "RAG_TEST_INSTALL_KEY"}
}

func TestInstallSavesDefaultsAndRegistersAgentsIdempotently(t *testing.T) {
	claude := &fakeClaude{}
	h := testHost(t, claude)
	codex := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	if err := os.MkdirAll(filepath.Dir(codex), 0700); err != nil {
		t.Fatal(err)
	}
	original := "# my settings\nmodel = \"gpt\"\n\n[mcp_servers.other]\ncommand = \"other\" # keep\n\n[mcp_servers.rag-go]\ncommand = \"/old/rag\"\nargs = [\"mcp\", \"--workspace\", \"/old\"]\n\n[mcp_servers.rag-go.env]\nX = \"1\"\n\n[profiles.fast]\nmodel = \"mini\"\n"
	if err := os.WriteFile(codex, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAG_TEST_INSTALL_KEY", "fixture-private-key")
	var out, stderr bytes.Buffer
	if err := install(context.Background(), providerArgs(embeddingServer(t)), strings.NewReader(""), &out, &stderr, h); err != nil {
		t.Fatalf("%s %s %v", out.String(), stderr.String(), err)
	}
	var report struct {
		Agents map[string]string `json:"agents"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Agents["claude"] != "registered" || report.Agents["codex"] != "updated" {
		t.Fatalf("report: %s %v", out.String(), err)
	}
	dir, _ := workspace.GlobalDir()
	cfg, err := model.LoadConfig(filepath.Join(dir, "config.json"))
	if err != nil || cfg.Embedding.Model != "fixture" || cfg.Embedding.Dimensions != 2 {
		t.Fatalf("global config: %+v %v", cfg.Embedding, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.json")); strings.Contains(string(b), "fixture-private-key") {
		t.Fatal("key in config")
	}
	if st, err := os.Stat(filepath.Join(dir, "credentials.json")); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("credentials: %v %v", st, err)
	}
	want := []string{"mcp", "add", "--transport", "stdio", "--scope", "user", "rag-go", "--", h.exe, "mcp"}
	if len(claude.calls) != 2 || !reflect.DeepEqual(claude.calls[1], want) {
		t.Fatalf("claude: %v", claude.calls)
	}
	b, _ := os.ReadFile(codex)
	text := string(b)
	for _, kept := range []string{"# my settings", `command = "other" # keep`, "[profiles.fast]"} {
		if !strings.Contains(text, kept) {
			t.Fatalf("lost %q:\n%s", kept, text)
		}
	}
	if strings.Contains(text, "/old") || strings.Contains(text, "rag-go.env") {
		t.Fatalf("stale entry kept:\n%s", text)
	}
	var parsed map[string]any
	if err := toml.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	entry := parsed["mcp_servers"].(map[string]any)["rag-go"].(map[string]any)
	if entry["command"] != h.exe || !reflect.DeepEqual(stringArray(entry["args"]), []string{"mcp"}) {
		t.Fatalf("codex entry: %+v", entry)
	}
	if st, _ := os.Stat(codex); st.Mode().Perm() != 0640 {
		t.Fatalf("codex mode changed: %v", st.Mode())
	}
	// A second run changes nothing and needs no key in the environment.
	t.Setenv("RAG_TEST_INSTALL_KEY", "")
	out.Reset()
	if err := install(context.Background(), []string{"--offline"}, strings.NewReader(""), &out, &stderr, h); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(codex); !bytes.Equal(again, b) || !strings.Contains(out.String(), `"claude":"already registered"`) {
		t.Fatalf("not idempotent: %s", out.String())
	}
}

func TestInstallRejectsBrokenCodexConfigAndFailedProbe(t *testing.T) {
	h := testHost(t, &fakeClaude{})
	codex := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	if err := os.MkdirAll(filepath.Dir(codex), 0700); err != nil {
		t.Fatal(err)
	}
	bad := []byte("[broken")
	if err := os.WriteFile(codex, bad, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAG_TEST_INSTALL_KEY", "key")
	var out, stderr bytes.Buffer
	if err := install(context.Background(), append(providerArgs(embeddingServer(t)), "--agents", "codex"), strings.NewReader(""), &out, &stderr, h); err == nil {
		t.Fatal("broken Codex config accepted")
	}
	if same, _ := os.ReadFile(codex); !bytes.Equal(same, bad) {
		t.Fatal("broken Codex config rewritten")
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer failing.Close()
	os.RemoveAll(os.Getenv("RAG_GO_CONFIG_DIR"))
	if err := install(context.Background(), append(providerArgs(failing), "--agents", "none"), strings.NewReader(""), &out, &stderr, h); err == nil {
		t.Fatal("failed probe accepted")
	}
	if _, _, err := workspace.GlobalConfig(); err != nil {
		t.Fatal(err)
	}
	if _, installed, _ := workspace.GlobalConfig(); installed {
		t.Fatal("failed probe saved configuration")
	}
}

func TestInitInheritsInstallAndIndexesExistingDocuments(t *testing.T) {
	h := testHost(t, &fakeClaude{})
	server := embeddingServer(t)
	t.Setenv("RAG_TEST_INSTALL_KEY", "fixture-private-key")
	var out, stderr bytes.Buffer
	if err := install(context.Background(), append(providerArgs(server), "--agents", "none"), strings.NewReader(""), &out, &stderr, h); err != nil {
		t.Fatal(err)
	}
	// Later shells need not export the key; the user-wide credential serves.
	t.Setenv("RAG_TEST_INSTALL_KEY", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "note.txt"), []byte("channel estimation evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"init", root}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatalf("%s %s %v", out.String(), stderr.String(), err)
	}
	var report struct {
		Sync struct{ Indexed int } `json:"sync"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Sync.Indexed != 1 {
		t.Fatalf("initial index: %s %v", out.String(), err)
	}
	if strings.Contains(stderr.String(), "rag install") {
		t.Fatal("install tip shown after install")
	}
	cfg, err := model.LoadConfig(filepath.Join(workspace.Store(root), "config.json"))
	if err != nil || cfg.Embedding.Model != "fixture" || cfg.Embedding.BaseURL != server.URL {
		t.Fatalf("inherited config: %+v %v", cfg.Embedding, err)
	}
	if _, err = os.Stat(filepath.Join(workspace.Store(root), "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("user-wide key copied into the workspace")
	}
	out.Reset()
	if err = Run(context.Background(), []string{"query", "--workspace", root, "channel"}, strings.NewReader(""), &out, &stderr); err != nil || !strings.Contains(out.String(), "note.txt") {
		t.Fatalf("query with user-wide key: %s %v", out.String(), err)
	}
	// An empty project initializes without indexing.
	empty := t.TempDir()
	out.Reset()
	if err = Run(context.Background(), []string{"init", empty}, strings.NewReader(""), &out, &stderr); err != nil || strings.Contains(out.String(), `"sync"`) {
		t.Fatalf("empty project: %s %v", out.String(), err)
	}
}

func TestUninstallRemovesRegistrationsAndPurges(t *testing.T) {
	claude := &fakeClaude{}
	h := testHost(t, claude)
	t.Setenv("RAG_TEST_INSTALL_KEY", "key")
	var out, stderr bytes.Buffer
	if err := install(context.Background(), append(providerArgs(embeddingServer(t)), "--offline"), strings.NewReader(""), &out, &stderr, h); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	out.Reset()
	if err := uninstall(context.Background(), []string{"--purge"}, &out, &stderr, h); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"claude":"removed"`) || !strings.Contains(out.String(), `"codex":"removed"`) || claude.registered != "" {
		t.Fatalf("uninstall: %s", out.String())
	}
	if b, _ := os.ReadFile(codex); strings.Contains(string(b), "rag-go") {
		t.Fatalf("codex entry kept: %s", b)
	}
	dir, _ := workspace.GlobalDir()
	for _, name := range []string{"config.json", "credentials.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s not purged", name)
		}
	}
	out.Reset()
	if err := uninstall(context.Background(), nil, &out, &stderr, h); err != nil || !strings.Contains(out.String(), "not registered") {
		t.Fatalf("second uninstall: %s %v", out.String(), err)
	}
}

func TestJSONAgentsRegisterKeepOtherSettingsAndUninstall(t *testing.T) {
	h := testHost(t, &fakeClaude{})
	pi := jsonAgentPath("pi", h)
	if err := os.MkdirAll(filepath.Dir(pi), 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"theme":"<dark>","mcpServers":{"other":{"command":"x"},"rag-go":{"command":"/old/rag","args":["mcp"]}}}`
	if err := os.WriteFile(pi, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	agents, err := selectAgents("auto", h)
	if err != nil || !reflect.DeepEqual(agents, []string{"claude", "codex", "pi"}) {
		t.Fatalf("auto: %v %v", agents, err)
	}
	for _, want := range []string{"updated", "already registered"} {
		if status, err := register(context.Background(), "pi", h, true); err != nil || status != want {
			t.Fatalf("register: %q %v", status, err)
		}
	}
	var cfg struct {
		Theme      string                    `json:"theme"`
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	b, _ := os.ReadFile(pi)
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.Theme != "<dark>" || cfg.MCPServers["other"]["command"] != "x" || cfg.MCPServers["rag-go"]["command"] != h.exe {
		t.Fatalf("config: %s %v", b, err)
	}
	if st, _ := os.Stat(pi); st.Mode().Perm() != 0640 {
		t.Fatalf("mode changed: %v", st.Mode())
	}
	desktop := jsonAgentPath("claude-desktop", h)
	if status, err := register(context.Background(), "claude-desktop", h, true); err != nil || status != "registered" {
		t.Fatalf("new file: %q %v", status, err)
	}
	for _, path := range []string{pi, desktop} {
		agent := map[string]string{pi: "pi", desktop: "claude-desktop"}[path]
		if status, err := register(context.Background(), agent, h, false); err != nil || status != "removed" {
			t.Fatalf("unregister %s: %q %v", agent, status, err)
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), "rag-go") {
			t.Fatalf("entry kept: %s", b)
		}
	}
	if b, _ := os.ReadFile(pi); !strings.Contains(string(b), `"other"`) {
		t.Fatalf("other server lost: %s", b)
	}
	if err := os.WriteFile(pi, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := register(context.Background(), "pi", h, true); err == nil {
		t.Fatal("accepted non-object config")
	}
	for _, null := range []string{"null", `{"mcpServers":null}`} {
		if err := os.WriteFile(pi, []byte(null), 0600); err != nil {
			t.Fatal(err)
		}
		if status, err := register(context.Background(), "pi", h, true); err != nil || status != "registered" {
			t.Fatalf("%s: %q %v", null, status, err)
		}
	}
}
