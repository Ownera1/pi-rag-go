package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	for _, f := range fields() {
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
		"editing": func() { m.cursor = 24; press(t, m, "\r") },
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
	for _, name := range []string{"message", "pending", "editing", "progress", "help", "list", "list run", "list end"} {
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
