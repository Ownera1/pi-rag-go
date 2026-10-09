package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
	tea "github.com/charmbracelet/bubbletea"
)

// row is one listed workspace's status and its step in a sync run.
type row struct {
	status *rag.Status
	err    error
	// state is "", waiting, syncing, done or skipped during a sync run, and
	// keeps the run's outcome until the list is refreshed.
	state  string
	result rag.IndexResult
	failed error
}

type rowMsg struct {
	path   string
	status *rag.Status
	err    error
}

// list is the Workspaces page: the registered workspaces, their status and
// the sync run started from it, one workspace at a time.
type list struct {
	entries  []workspace.Entry
	listErr  error
	rows     map[string]*row
	at       int
	queue    []string
	running  string
	runDone  int
	runTotal int
	runFail  int
}

// loadEntries rereads the registry and forgets every row's status and run.
func (m *Model) loadEntries() {
	m.entries, m.listErr = workspace.Workspaces()
	m.rows = map[string]*row{}
	for _, e := range m.entries {
		m.rows[e.Path] = &row{}
	}
	m.at = min(m.at, max(0, len(m.entries)-1))
}

func (m *Model) loadRows() tea.Cmd {
	cmds := []tea.Cmd{}
	for _, e := range m.entries {
		if !e.Missing {
			cmds = append(cmds, m.loadRow(e.Path))
		}
	}
	return tea.Batch(cmds...)
}

// loadRow reads one listed workspace's status through its own core.
func (m *Model) loadRow(path string) tea.Cmd {
	if m.rows[path] == nil || m.open == nil {
		return nil
	}
	open, ctx := m.open, m.ctx
	return func() tea.Msg {
		core, err := open(path)
		if err != nil {
			return rowMsg{path: path, err: err}
		}
		defer core.Close()
		s, err := core.Status(ctx)
		if err != nil {
			return rowMsg{path: path, err: err}
		}
		return rowMsg{path: path, status: &s}
	}
}

// name is a workspace's registered name, or its directory's when unlisted.
func (m *Model) name(path string) string {
	for _, e := range m.entries {
		if e.Path == path {
			return e.Name
		}
	}
	return filepath.Base(path)
}

func (m *Model) missing() int {
	n := 0
	for _, e := range m.entries {
		if e.Missing {
			n++
		}
	}
	return n
}

func (m *Model) listKey(k, confirm string) tea.Cmd {
	n := len(m.entries)
	if n == 0 || m.open == nil {
		if k == "r" {
			m.loadEntries()
			return m.loadRows()
		}
		return nil
	}
	e := m.entries[m.at]
	switch k {
	case "up", "k":
		m.at = (m.at + n - 1) % n
	case "down", "j":
		m.at = (m.at + 1) % n
	case "enter":
		if e.Missing {
			m.msg = warn.Render("配置已不存在，用 rag workspace remove " + e.Name + " 移除")
			return nil
		}
		if m.core != nil && m.core.WorkspaceDir() == e.Path {
			m.page = pageSettings
			return nil
		}
		if len(m.changed()) > 0 && confirm != "open" {
			m.confirm, m.msg = "open", warn.Render("有未保存的修改，再按 enter 放弃并打开 "+e.Name)
			return nil
		}
		core, err := m.open(e.Path)
		if err == nil {
			if err = m.setCore(core); err != nil {
				core.Close()
			}
		}
		if err != nil {
			m.msg = bad.Render(err.Error())
			return nil
		}
		m.page = pageSettings
		return m.refresh()
	case "s":
		if e.Missing {
			m.msg = warn.Render("配置已不存在，用 rag workspace remove " + e.Name + " 移除")
			return nil
		}
		return m.syncRun([]string{e.Path})
	case "S":
		return m.syncRun(nil)
	case "r":
		m.loadEntries()
		return m.loadRows()
	}
	return nil
}

// syncRun syncs paths one after another, or every listed workspace when paths
// is nil, skipping missing ones.
func (m *Model) syncRun(paths []string) tea.Cmd {
	if len(m.changed()) > 0 {
		m.msg = warn.Render("先 ctrl+s 保存或 esc 放弃修改")
		return nil
	}
	all := paths == nil
	for _, e := range m.entries {
		r := m.rows[e.Path]
		r.state = ""
		switch {
		case all && e.Missing:
			r.state = "skipped"
		case all:
			paths = append(paths, e.Path)
		}
	}
	if len(paths) == 0 {
		m.msg = muted.Render("没有可以 sync 的 workspace")
		return nil
	}
	for _, p := range paths {
		m.rows[p].state = "waiting"
	}
	m.queue, m.runDone, m.runTotal, m.runFail = paths, 0, len(paths), 0
	return m.next()
}

func (m *Model) next() tea.Cmd {
	path := m.queue[0]
	m.queue, m.running = m.queue[1:], path
	m.rows[path].state = "syncing"
	open := m.open
	return m.start("sync", path, func(ctx context.Context) (any, error) {
		core, err := open(path)
		if err != nil {
			return rag.IndexResult{}, err
		}
		defer core.Close()
		return core.Sync(ctx)
	})
}

// synced records one workspace's sync and starts the next, or ends the run
// with a summary; a cancellation ends the whole run.
func (m *Model) synced(d doneMsg) tea.Cmd {
	canceled := errors.Is(d.err, context.Canceled)
	if r := m.rows[d.path]; r != nil {
		r.state, r.failed = "done", d.err
		r.result, _ = d.result.(rag.IndexResult)
	}
	if d.err != nil && !canceled {
		m.runFail++
	}
	cmds := []tea.Cmd{m.loadRow(d.path)}
	if m.core != nil && m.core.WorkspaceDir() == d.path {
		cmds = append(cmds, m.refresh())
	}
	if !canceled {
		m.runDone++
	}
	if len(m.queue) > 0 && !canceled {
		return tea.Batch(append(cmds, m.next())...)
	}
	for _, p := range m.queue {
		m.rows[p].state = ""
	}
	switch {
	case canceled:
		m.msg = warn.Render(fmt.Sprintf("sync 已取消，完成 %d/%d", m.runDone, m.runTotal))
	case m.runFail > 0:
		m.msg = bad.Render(fmt.Sprintf("sync 完成 %d 个，失败 %d 个", m.runTotal, m.runFail))
	default:
		m.msg = good.Render(fmt.Sprintf("sync 完成 %d 个 workspace", m.runTotal))
	}
	m.queue, m.runTotal = nil, 0
	return tea.Batch(cmds...)
}

// listLines renders the workspace cards, scrolled to keep the cursor visible
// within room lines; room < 0 means unbounded.
func (m *Model) listLines(w, room int) []string {
	var lines []string
	at := 0
	switch {
	case m.listErr != nil:
		lines = wrap("   "+bad.Render(m.listErr.Error()), w)
	case len(m.entries) == 0:
		lines = []string{"   还没有登记的 workspace", muted.Render(fit("   运行 rag init 新建，或 rag workspace add PATH 登记已有的", w))}
	}
	for i, e := range m.entries {
		if i == m.at {
			at = len(lines)
		}
		lines = append(lines, m.rowCard(w, i == m.at, e)...)
		if i < len(m.entries)-1 {
			lines = append(lines, "")
		}
	}
	if m.core != nil && m.rows[m.core.WorkspaceDir()] == nil {
		lines = append(lines, "", muted.Render(fit("   当前 workspace 未登记；运行 rag workspace add 后出现在这里", w)))
	}
	if room >= 0 && len(lines) > room {
		start := min(max(0, at-room/2), len(lines)-room)
		lines = lines[start : start+room]
	}
	return lines
}

func (m *Model) rowCard(w int, selected bool, e workspace.Entry) []string {
	r := m.rows[e.Path]
	name := e.Name
	switch {
	case selected:
		name = accent.Bold(true).Render(name)
	case e.Missing:
		name = muted.Render(name)
	}
	var b string
	switch {
	case r.state == "waiting":
		b = badge("等待", "8")
	case r.state == "syncing":
		b = badge("sync 中", "4")
	case r.state == "skipped":
		b = badge("跳过", "8")
	case r.state == "done" && errors.Is(r.failed, context.Canceled):
		b = badge("已取消", "3")
	case r.state == "done" && r.failed != nil:
		b = badge("失败", "1")
	case r.state == "done":
		b = badge("已 sync", "2")
	case e.Missing:
		b = badge("missing", "8")
	case r.err != nil:
		b = badge("出错", "1")
	default:
		b = statusBadge(r.status)
	}
	style := muted
	if selected {
		style = accent
	}
	var d string
	switch {
	case r.state == "done":
		res := r.result
		d = fmt.Sprintf("indexed %d | skipped %d | removed %d | failed %d", res.Indexed, res.Skipped, res.Removed, res.Failed)
		switch {
		case errors.Is(r.failed, context.Canceled):
			style = warn
		case r.failed != nil:
			d, style = d+" | "+r.failed.Error(), bad
		default:
			style = good
		}
	case e.Missing:
		d = detail(e.Path, "配置已不存在，用 rag workspace remove "+e.Name+" 移除")
	case r.err != nil:
		d, style = detail(e.Path, r.err.Error()), bad
	case r.status == nil:
		d = detail(e.Path)
	default:
		s := r.status
		last := "未 sync"
		if s.LastSync != nil {
			last = when(*s, "01-02 15:04")
		}
		d = detail(e.Path, fmt.Sprintf("%d 文件", s.Files), fmt.Sprintf("%d chunks", s.Chunks), last)
	}
	return card(w, selected, name, b, style.Render(shorten(d, w-4)))
}
