package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/width"
)

type fakeEmbedding struct{}

func (fakeEmbedding) Model() string   { return "fake" }
func (fakeEmbedding) Dimensions() int { return 2 }
func (fakeEmbedding) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (fakeEmbedding) EmbedDocuments(_ context.Context, in []string) ([][]float32, error) {
	out := make([][]float32, len(in))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

type fakeReranker struct{}

func (fakeReranker) Model() string { return "fake" }
func (fakeReranker) Rerank(context.Context, string, []model.RerankDoc, int) ([]model.RerankResult, error) {
	return nil, nil
}

// open opens a workspace with fake providers, as the panel opens listed ones.
func open(dir string) (*rag.Core, error) {
	return rag.Open(rag.Options{WorkspaceDir: dir, Embedder: fakeEmbedding{}, Reranker: fakeReranker{}})
}

// indexed returns a synced workspace whose providers are fakes, so changing
// provider settings exercises only configuration and compatibility.
func indexed(t *testing.T) (*rag.Core, model.Config) {
	t.Helper()
	root := t.TempDir()
	cfg := model.DefaultConfig()
	cfg.Embedding.Model, cfg.Embedding.Dimensions = "fake", 2
	cfg.Chunking.Mode = "legacy"
	cfg.Zotero = &model.ZoteroConfig{LibraryID: "1"}
	if err := os.MkdirAll(workspace.Store(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := workspace.AtomicJSON(filepath.Join(workspace.Store(root), "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Store(root), ".lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"documents", "other"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "note.txt"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	core, err := open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	if _, err = core.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, err = model.LoadConfig(configPath(core))
	if err != nil {
		t.Fatal(err)
	}
	return core, cfg
}

// The rebuild tag must match what the index actually checks, so a change to
// fingerprinting that is not reflected in the panel fails here.
func TestImpactTagsMatchIndexCompatibility(t *testing.T) {
	core, base := indexed(t)
	typed := map[string]string{
		"reranker.model": "other", "reranker.baseUrl": "https://example.test/v1", "reranker.apiKeyEnv": "OTHER_KEY",
		"embedding.model": "fake-2", "embedding.dimensions": "3", "embedding.baseUrl": "https://example.test/v1",
		"embedding.apiKeyEnv": "OTHER_KEY", "documents": "other", "excludePatterns": "drafts/**",
		"zotero.baseUrl": "http://localhost:23119/api/", "zotero.libraryId": "2",
	}
	for _, f := range fields(nil) {
		cfg := clone(base)
		var err error
		if v, ok := typed[f.name]; ok {
			err = f.set(&cfg, v)
		} else {
			err = f.adjust(&cfg, 1)
		}
		if err != nil || f.get(cfg) == f.get(base) {
			t.Fatalf("%s: no change (%v)", f.name, err)
		}
		if err = cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if err = workspace.AtomicJSON(configPath(core), cfg); err != nil {
			t.Fatal(err)
		}
		s, err := core.Status(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if s.NeedsRebuild != (f.impact == onRebuild) {
			t.Errorf("%s tagged %s but NeedsRebuild=%v (%s)", f.name, impactLabel[f.impact], s.NeedsRebuild, s.RebuildReason)
		}
	}
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "\r":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "\x1b":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, cmd := m.Update(keyMsg(k))
		if cmd != nil && k == "ctrl+s" {
			m.Update(cmd())
		}
	}
}

// run delivers msg and every message its commands produce, as the program
// would, except quitting.
func run(m *Model, msg tea.Msg) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				run(m, c())
			}
		}
		return
	}
	if _, ok := msg.(tea.QuitMsg); ok || msg == nil {
		return
	}
	if _, cmd := m.Update(msg); cmd != nil {
		run(m, cmd())
	}
}

// onScreen reports whether the cursor bar is drawn, here as its uncolored ">".
func onScreen(view string) bool {
	for _, l := range strings.Split(ansi.Strip(view), "\n") {
		if strings.HasPrefix(l, " > ") {
			return true
		}
	}
	return false
}

// Every state must fit the terminal at any supported size, keep the cursor
// on screen, and degrade to a notice below the minimum. Fitting also needs no
// East Asian Ambiguous characters, whose width depends on terminal settings.
func TestViewFitsTerminal(t *testing.T) {
	t.Setenv("RAG_GO_CONFIG_DIR", t.TempDir())
	core, _ := indexed(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("很长的工作区名字", 6))
	for _, dir := range []string{core.WorkspaceDir(), long} {
		if err := workspace.Register(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(context.Background(), core, open)
	if err != nil {
		t.Fatal(err)
	}
	run(m, m.Init()())
	m.draft.Documents = strings.Repeat("/very/long/documents/path", 8)
	m.msg = bad.Render(strings.Repeat("保存失败：config.json changed outside the panel ", 4))
	states := map[string]func(){
		"message": func() {},
		"pending": func() { m.msg = "" },
		"editing": func() { at(m, "documents"); press(t, m, "\r") },
		"searching": func() {
			press(t, m, "\x1b")
			at(m, "embedding.model")
			press(t, m, "\r", "voyage")
		},
		"progress": func() {
			press(t, m, "\x1b")
			m.task, m.prog = "rebuild", rag.IndexProgress{Done: 37, Total: 214, Path: strings.Repeat("长文件名", 20) + ".pdf", Result: rag.IndexResult{Chunks: 1204}}
		},
		"help": func() { m.task = ""; m.fullHelp = true; m.cursor = len(m.fields) - 1 },
		"list": func() { m.page = pageList },
		"list run": func() {
			m.task, m.runTotal, m.running = "sync", 2, long
			m.rows[long].state, m.rows[core.WorkspaceDir()].state = "syncing", "done"
			m.rows[core.WorkspaceDir()].failed = errors.New(strings.Repeat("embedding endpoint unreachable ", 5))
		},
		"list end": func() { m.task, m.runTotal = "", 0; m.at = 1 },
	}
	for _, name := range []string{"message", "pending", "editing", "searching", "progress", "help", "list", "list run", "list end"} {
		states[name]()
		for _, size := range [][2]int{{40, 14}, {66, 24}, {80, 30}, {120, 50}, {240, 80}} {
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			lines := strings.Split(m.View(), "\n")
			if len(lines) > size[1] {
				t.Errorf("%s %v: %d lines", name, size, len(lines))
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > size[0] {
					t.Errorf("%s %v: line too wide (%d): %s", name, size, ansi.StringWidth(l), ansi.Strip(l))
				}
				for _, r := range ansi.Strip(l) {
					if width.LookupRune(r).Kind() == width.EastAsianAmbiguous {
						t.Errorf("%s %v: ambiguous-width %q in %s", name, size, r, ansi.Strip(l))
					}
				}
			}
			if !onScreen(m.View()) && !m.editing {
				t.Errorf("%s %v: cursor scrolled off", name, size)
			}
			if name == "searching" && (!onScreen(m.View()) || !strings.Contains(m.View(), "voyage-4-lite")) {
				t.Errorf("%s %v: field or first match scrolled off", name, size)
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	if !strings.Contains(m.View(), "终端太小") {
		t.Fatal("no notice below minimum size")
	}
}

func TestSaveWritesDraftAndRefusesOutsideEdit(t *testing.T) {
	core, _ := indexed(t)
	m, err := New(context.Background(), core, open)
	if err != nil {
		t.Fatal(err)
	}
	press(t, m, "right", "ctrl+s") // topK 5 → 6
	if cfg, _ := model.LoadConfig(configPath(core)); cfg.TopK != 6 || m.saved.TopK != 6 || len(m.changed()) != 0 {
		t.Fatalf("disk %d saved %d changed %d: %s", cfg.TopK, m.saved.TopK, len(m.changed()), m.msg)
	}
	outside := m.saved
	outside.Alpha = 0.9
	if err = workspace.AtomicJSON(configPath(core), outside); err != nil {
		t.Fatal(err)
	}
	press(t, m, "right", "ctrl+s")
	if cfg, _ := model.LoadConfig(configPath(core)); cfg.TopK != 6 || cfg.Alpha != 0.9 || !strings.Contains(m.msg, "changed outside") {
		t.Fatalf("outside edit overwritten: %+v %s", cfg, m.msg)
	}
}

// at moves the cursor to the field named name.
func at(m *Model, name string) {
	m.cursor = slices.IndexFunc(m.fields, func(f field) bool { return f.name == name })
}

// The draft stays a valid configuration: a field with options takes no typed
// value, and an edit that breaks a limit is refused with the limit named.
func TestEditsKeepDraftValid(t *testing.T) {
	core, _ := indexed(t)
	m, err := New(context.Background(), core, open)
	if err != nil {
		t.Fatal(err)
	}
	at(m, "reranker.type")
	press(t, m, "\r")
	if m.editing || m.draft.Reranker.Type != "voyage" {
		t.Fatalf("enter on options: editing %v type %q", m.editing, m.draft.Reranker.Type)
	}
	at(m, "topK")
	for value, want := range map[string]string{"0": "topK (0) must be at least 1", "abc": "topK must be an integer"} {
		press(t, m, "\r")
		m.input.SetValue(value)
		press(t, m, "\r")
		if !m.editing || m.draft.TopK != 5 || !strings.Contains(ansi.Strip(m.msg), want) {
			t.Fatalf("typed %q: editing %v topK %d msg %q", value, m.editing, m.draft.TopK, m.msg)
		}
		press(t, m, "\x1b")
	}
	at(m, "candidateTopK")
	press(t, m, "h", "h", "h", "h", "h", "h")
	if m.draft.CandidateTopK != 5 || !strings.Contains(ansi.Strip(m.msg), "candidateTopK (0) must be at least topK (5)") {
		t.Fatalf("stepped past topK: %d %q", m.draft.CandidateTopK, m.msg)
	}
}

// The list syncs every workspace in turn, skipping missing ones, and opens a
// workspace only after confirming that unsaved settings are discarded.
func TestListSyncsAllAndOpensWorkspaces(t *testing.T) {
	t.Setenv("RAG_GO_CONFIG_DIR", t.TempDir())
	a, _ := indexed(t)
	b, _ := indexed(t)
	gone := filepath.Join(t.TempDir(), "gone")
	for _, dir := range []string{a.WorkspaceDir(), gone, b.WorkspaceDir()} {
		if err := workspace.Register(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(b.WorkspaceDir(), "documents", "more.txt"), []byte("more evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background(), nil, open)
	if err != nil {
		t.Fatal(err)
	}
	run(m, m.Init()())
	if m.page != pageList || m.rows[b.WorkspaceDir()].status == nil || !m.rows[b.WorkspaceDir()].status.NeedsSync {
		t.Fatalf("list not loaded: page %d rows %+v", m.page, m.rows)
	}
	press(t, m, "tab")
	if m.page != pageSettings || m.core == nil || m.core.WorkspaceDir() != a.WorkspaceDir() {
		t.Fatalf("tab without an open workspace did not open the selected one: page %d", m.page)
	}
	press(t, m, "1")
	if m.page != pageList {
		t.Fatalf("1 did not return to the list: page %d", m.page)
	}
	run(m, keyMsg("S"))
	ra, rb, rg := m.rows[a.WorkspaceDir()], m.rows[b.WorkspaceDir()], m.rows[gone]
	if ra.state != "done" || ra.failed != nil || rb.state != "done" || rb.result.Indexed != 1 || rg.state != "skipped" || m.task != "" {
		t.Fatalf("sync all: a %+v b %+v gone %+v task %q", ra, rb, rg, m.task)
	}
	if rb.status.NeedsSync || !strings.Contains(m.msg, "sync 完成 2 个") {
		t.Fatalf("after sync: %+v %s", rb.status, m.msg)
	}
	run(m, keyMsg("\r"))
	if m.page != pageSettings || m.core.WorkspaceDir() != a.WorkspaceDir() {
		t.Fatalf("enter did not open a: page %d", m.page)
	}
	press(t, m, "right", "tab", "down", "\r")
	if m.page != pageList || !strings.Contains(m.msg, "gone") {
		t.Fatalf("opened a missing workspace: page %d %s", m.page, m.msg)
	}
	press(t, m, "down", "\r")
	if m.core.WorkspaceDir() != a.WorkspaceDir() || !strings.Contains(m.msg, "再按 enter") {
		t.Fatalf("switched with unsaved changes: %s", m.msg)
	}
	run(m, keyMsg("\r"))
	if m.page != pageSettings || m.core.WorkspaceDir() != b.WorkspaceDir() || len(m.changed()) != 0 {
		t.Fatalf("second enter did not open b: page %d changed %d", m.page, len(m.changed()))
	}
}

// A provider preset sets the endpoint, credential name and model together and
// offers its models; typing searches them, and any typed name stays enterable.
func TestProviderPresetsAndModelSearch(t *testing.T) {
	t.Setenv("RAG_GO_CONFIG_DIR", t.TempDir())
	t.Setenv("DASHSCOPE_API_KEY", "")
	core, _ := indexed(t)
	m, err := New(context.Background(), core, open)
	if err != nil {
		t.Fatal(err)
	}
	at(m, "embedding.provider")
	press(t, m, "right")
	e := m.draft.Embedding
	if e.Type != "openai" || e.Model != "qwen3.7-text-embedding" || e.Dimensions != 1024 || e.APIKeyEnv != "DASHSCOPE_API_KEY" || m.draft.Indexing.EmbeddingBatchSize != 10 {
		t.Fatalf("dashscope preset: %+v batch %d", e, m.draft.Indexing.EmbeddingBatchSize)
	}
	if !strings.Contains(m.msg, "DASHSCOPE_API_KEY") {
		t.Fatalf("no missing-key warning: %q", m.msg)
	}
	at(m, "embedding.model")
	press(t, m, "right")
	if m.draft.Embedding.Model != "text-embedding-v4" {
		t.Fatalf("model choice: %q", m.draft.Embedding.Model)
	}
	at(m, "embedding.provider")
	press(t, m, "u")
	if m.draft.Embedding != m.saved.Embedding {
		t.Fatalf("restore: %+v", m.draft.Embedding)
	}

	at(m, "reranker.provider")
	press(t, m, "right") // none -> voyage
	at(m, "reranker.model")
	for _, c := range []struct {
		typed string
		downs int
		want  string
	}{
		{"", 0, "rerank-3"},               // nothing typed keeps the current model
		{"2.5-l", 0, "rerank-2.5-lite"},   // a hyphenated word
		{"LITE", 1, "rerank-2.5-lite"},    // second of two, ignoring case
		{"rerank-2", 2, "rerank-2"},       // the typed text after the matches
		{"my-reranker", 0, "my-reranker"}, // no match
	} {
		press(t, m, "\r")
		if c.typed != "" {
			press(t, m, c.typed)
		}
		for range c.downs {
			press(t, m, "down")
		}
		press(t, m, "\r")
		if m.editing || m.draft.Reranker.Model != c.want {
			t.Fatalf("typed %q: editing %v model %q", c.typed, m.editing, m.draft.Reranker.Model)
		}
	}

	at(m, "reranker.baseUrl")
	press(t, m, "\r")
	m.input.SetValue("https://example.test/v1")
	press(t, m, "\r")
	at(m, "reranker.model")
	if f := m.fields[m.cursor]; f.get(m.draft) == "" || f.offered(m.draft) != nil || m.fields[m.cursor-2].get(m.draft) != "custom" {
		t.Fatalf("custom endpoint still offers preset models")
	}
	press(t, m, "\r")
	if m.input.Value() != "my-reranker" || m.matches() != nil {
		t.Fatalf("custom model input %q matches %v", m.input.Value(), m.matches())
	}
}

func TestRecentKeepsTwoNewestGenerations(t *testing.T) {
	got := recent([]string{"voyage-4-lite", "voyage-3-large", "voyage-4", "voyage-3.5", "voyage-2", "voyage-code-3",
		"text-embedding-v4", "text-embedding-v3", "text-embedding-v2", "qwen3.7-text-embedding-flash", "qwen3-rerank", "qwen2.5-rerank", "jina-reranker"})
	want := []string{"voyage-4-lite", "voyage-4", "voyage-3.5", "voyage-code-3",
		"text-embedding-v4", "text-embedding-v3", "qwen3.7-text-embedding-flash", "qwen3-rerank", "jina-reranker"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

// A model field also offers what its endpoint lists for its role, newest two
// generations only, fetched once with the workspace's credential rules.
func TestModelFieldOffersListedModels(t *testing.T) {
	t.Setenv("RAG_GO_CONFIG_DIR", t.TempDir())
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[{"id":"rerank-x-1"},{"id":"rerank-x-2"},{"id":"chat-x-9"},{"id":"rerank-x-3"},{"id":"bge-reranker-v2-m3"}]}`))
	}))
	defer server.Close()
	core, cfg := indexed(t)
	cfg.Reranker = model.ProviderConfig{Type: "http", Model: "rerank-x-3", BaseURL: server.URL}
	if err := workspace.AtomicJSON(configPath(core), cfg); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Register(context.Background(), core.WorkspaceDir()); err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background(), core, open)
	if err != nil {
		t.Fatal(err)
	}
	run(m, m.Init()())
	at(m, "reranker.model")
	if got := m.fields[m.cursor].offered(m.draft); !slices.Equal(got, []string{"rerank-x-2", "rerank-x-3", "bge-reranker-v2-m3"}) {
		t.Fatalf("offered %v", got)
	}
	run(m, keyMsg("\r"))
	run(m, keyMsg("bge"))
	run(m, keyMsg("\r"))
	if m.draft.Reranker.Model != "bge-reranker-v2-m3" || requests != 1 {
		t.Fatalf("model %q after %d requests", m.draft.Reranker.Model, requests)
	}
}
