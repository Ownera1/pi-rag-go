// Package tui is the interactive index and settings panel behind rag tui.
package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const cleanKeep = 3

var (
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	muted  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	warn   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	bad    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	good   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	bold   = lipgloss.NewStyle().Bold(true)
	tags   = [...]lipgloss.Style{good, accent, accent, warn}
)

type (
	statusMsg   struct{ status rag.Status }
	progressMsg rag.IndexProgress
	doneMsg     struct {
		task   string
		result any
		err    error
	}
	savedMsg struct {
		cfg model.Config
		err error
	}
	errMsg struct{ err error }
)

type Model struct {
	ctx          context.Context
	core         *rag.Core
	fields       []field
	saved, draft model.Config
	cursor       int
	editing      bool
	input        textinput.Model
	status       *rag.Status
	task         string
	cancel       context.CancelFunc
	prog         rag.IndexProgress
	bar          progress.Model
	confirm      string
	msg          string
	width        int
	height       int
	fullHelp     bool
}

// Run opens the workspace at dir (discovered when empty) and runs the panel
// on the terminal until the user quits.
func Run(ctx context.Context, dir string) error {
	var p *tea.Program
	core, err := rag.Open(rag.Options{WorkspaceDir: dir, Progress: func(x rag.IndexProgress) { p.Send(progressMsg(x)) }})
	if err != nil {
		return err
	}
	defer core.Close()
	m, err := New(ctx, core)
	if err != nil {
		return err
	}
	p = tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	_, err = p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func New(ctx context.Context, core *rag.Core) (*Model, error) {
	cfg, err := model.LoadConfig(configPath(core))
	if err != nil {
		return nil, err
	}
	return &Model{ctx: ctx, core: core, fields: fields(), saved: cfg, draft: clone(cfg),
		input: textinput.New(), bar: progress.New(progress.WithSolidFill("12"), progress.WithFillCharacters('=', '-'), progress.WithWidth(30))}, nil
}

func configPath(core *rag.Core) string {
	return filepath.Join(workspace.Store(core.WorkspaceDir()), "config.json")
}

// clone separates the Zotero block and pattern list so editing a draft never
// writes through to the saved configuration.
func clone(c model.Config) model.Config {
	if c.Zotero != nil {
		z := *c.Zotero
		c.Zotero = &z
	}
	c.ExcludePatterns = slices.Clone(c.ExcludePatterns)
	return c
}

func (m *Model) Init() tea.Cmd { return m.refresh() }

func (m *Model) refresh() tea.Cmd {
	return func() tea.Msg {
		s, err := m.core.Status(m.ctx)
		if err != nil {
			return errMsg{err}
		}
		return statusMsg{s}
	}
}

// changed lists the fields whose draft differs from the saved configuration.
func (m *Model) changed() []field {
	var out []field
	for _, f := range m.fields {
		if f.get(m.draft) != f.get(m.saved) {
			out = append(out, f)
		}
	}
	return out
}

// save writes cfg under the workspace write lock, refusing when the file no
// longer holds what the panel loaded so another writer's edit is never lost.
// It returns the configuration as the next operation will load it.
func save(ctx context.Context, core *rag.Core, loaded, cfg model.Config) (model.Config, error) {
	if err := cfg.Validate(); err != nil {
		return loaded, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	release, err := workspace.Lock(ctx, core.WorkspaceDir(), true)
	if err != nil {
		return loaded, fmt.Errorf("workspace is busy: %w", err)
	}
	defer release()
	disk, err := model.LoadConfig(configPath(core))
	if err != nil {
		return loaded, err
	}
	if !reflect.DeepEqual(disk, loaded) {
		return loaded, errors.New("config.json changed outside the panel; press esc to discard and reload")
	}
	if err = workspace.AtomicJSON(configPath(core), cfg); err != nil {
		return loaded, err
	}
	return model.LoadConfig(configPath(core))
}

func (m *Model) start(task string, fn func(context.Context) (any, error)) tea.Cmd {
	if len(m.changed()) > 0 {
		m.msg = warn.Render("先 ctrl+s 保存或 esc 放弃修改")
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.task, m.cancel, m.prog, m.msg = task, cancel, rag.IndexProgress{}, ""
	return func() tea.Msg {
		r, err := fn(ctx)
		return doneMsg{task, r, err}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		_, valueW := columns(m.width)
		m.input.Width = max(1, valueW-lipgloss.Width(m.input.Prompt)-1)
		m.bar.Width = max(10, min(50, m.width-9))
		// Terminals reflow or scroll their own content while resizing; clear
		// it so no stale rows survive under the repainted frame.
		return m, tea.ClearScreen
	case statusMsg:
		m.status = &msg.status
	case errMsg:
		m.msg = bad.Render(msg.err.Error())
	case progressMsg:
		m.prog = rag.IndexProgress(msg)
	case doneMsg:
		m.task, m.cancel = "", nil
		m.msg = done(msg)
		if r, ok := msg.result.(rag.CleanupResult); ok && msg.err == nil && r.DryRun && len(r.Removed) > 0 {
			m.confirm = "clean"
		}
		return m, m.refresh()
	case savedMsg:
		if msg.err != nil {
			m.msg = bad.Render("保存失败：" + msg.err.Error())
			return m, nil
		}
		rebuild := false
		for _, f := range m.fields {
			rebuild = rebuild || f.impact == onRebuild && f.get(msg.cfg) != f.get(m.saved)
		}
		// Edits made while saving stay in the draft.
		m.saved = msg.cfg
		m.msg = good.Render("已写入 config.json")
		if rebuild {
			m.msg += muted.Render(" | 索引需要重建，按 R rebuild")
		}
		return m, m.refresh()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.editing {
		switch k.String() {
		case "enter":
			if err := m.fields[m.cursor].set(&m.draft, m.input.Value()); err != nil {
				m.msg = bad.Render(err.Error())
				return m, nil
			}
			m.editing = false
			m.input.Blur()
		case "esc":
			m.editing = false
			m.input.Blur()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			return m, cmd
		}
		return m, nil
	}
	if m.task != "" {
		if k.String() == "esc" {
			m.cancel()
			m.msg = muted.Render("正在取消 " + m.task + "...")
		}
		return m, nil
	}
	confirm := m.confirm
	m.confirm, m.msg = "", ""
	switch k.String() {
	case "q":
		if len(m.changed()) > 0 && confirm != "quit" {
			m.confirm, m.msg = "quit", warn.Render("有未保存的修改，再按 q 放弃并退出")
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		m.cursor = (m.cursor + len(m.fields) - 1) % len(m.fields)
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(m.fields)
	case "left", "h", "right", "l":
		dir := 1
		if k.String() == "left" || k.String() == "h" {
			dir = -1
		}
		if err := m.fields[m.cursor].adjust(&m.draft, dir); err != nil {
			m.msg = bad.Render(err.Error())
		}
	case "enter":
		m.editing = true
		m.input.SetValue(m.fields[m.cursor].get(m.draft))
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "u":
		f := m.fields[m.cursor]
		_ = f.set(&m.draft, f.get(m.saved))
	case "esc":
		if confirm != "" {
			return m, nil
		}
		cfg, err := model.LoadConfig(configPath(m.core))
		if err != nil {
			m.msg, cfg = bad.Render(err.Error()), m.saved
		}
		m.saved, m.draft = cfg, clone(cfg)
	case "ctrl+s":
		if len(m.changed()) == 0 {
			return m, nil
		}
		loaded, cfg := m.saved, clone(m.draft)
		return m, func() tea.Msg {
			cfg, err := save(m.ctx, m.core, loaded, cfg)
			return savedMsg{cfg, err}
		}
	case "s":
		return m, m.start("sync", func(ctx context.Context) (any, error) { return m.core.Sync(ctx) })
	case "R":
		if confirm != "rebuild" {
			m.confirm, m.msg = "rebuild", warn.Render("rebuild 会重新解析并嵌入全部文档，期间旧索引仍可查询。再按 R 确认，esc 取消")
			return m, nil
		}
		return m, m.start("rebuild", func(ctx context.Context) (any, error) { return m.core.Rebuild(ctx) })
	case "c":
		dry := confirm != "clean"
		return m, m.start("clean", func(ctx context.Context) (any, error) { return m.core.Cleanup(ctx, cleanKeep, dry) })
	case "r":
		return m, m.refresh()
	case "?":
		m.fullHelp = !m.fullHelp
	}
	return m, nil
}

func done(d doneMsg) string {
	var s string
	switch r := d.result.(type) {
	case rag.IndexResult:
		s = fmt.Sprintf("%s：indexed %d | skipped %d | removed %d | failed %d | chunks %d", d.task, r.Indexed, r.Skipped, r.Removed, r.Failed, r.Chunks)
	case rag.CleanupResult:
		if d.err != nil {
			break
		}
		if r.DryRun {
			if len(r.Removed) == 0 {
				return muted.Render("clean：没有可删除的旧索引")
			}
			return warn.Render(fmt.Sprintf("clean 预览：保留 %d 代，将删除 %s。再按 c 确认", cleanKeep, strings.Join(names(r.Removed), ", ")))
		}
		s = fmt.Sprintf("clean：删除 %d 代旧索引", len(r.Removed))
	}
	switch {
	case errors.Is(d.err, context.Canceled):
		return warn.Render(d.task + " 已取消")
	case d.err != nil && s != "":
		return bad.Render(s + " | " + d.err.Error())
	case d.err != nil:
		return bad.Render(d.task + "：" + d.err.Error())
	}
	return good.Render(s)
}

func names(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// The panel never draws below this size; a smaller terminal gets a notice.
const minWidth, minHeight = 40, 14

// columns splits a terminal width into the name and value columns beside the
// fixed impact tag column, shrinking names first on narrow terminals.
func columns(w int) (nameW, valueW int) {
	nameW = min(29, max(12, w-2-tagW-14))
	return nameW, w - 2 - nameW - tagW
}

const tagW = 10

// Rendered text avoids East Asian Ambiguous characters (·, …, arrows, box
// drawing): terminals set to draw them double-width would wrap lines the
// renderer measured as fitting, scrolling the screen and leaving stale rows.

// fit cuts s to one line of display width w.
func fit(s string, w int) string { return ansi.Truncate(s, w, "...") }

// wrap breaks s into lines of display width at most w.
func wrap(s string, w int) []string { return strings.Split(ansi.Wrap(s, w, ""), "\n") }

func (m *Model) View() string {
	w, h := m.width, m.height
	if w == 0 {
		w = 80 // before the first size message; h == 0 leaves height unbounded
	}
	if h > 0 && (w < minWidth || h < minHeight) {
		return strings.Join(wrap(fmt.Sprintf("终端太小（%dx%d），请放大到至少 %dx%d", w, h, minWidth, minHeight), w), "\n")
	}
	rule := muted.Render(strings.Repeat("-", w))
	// Cut the workspace path from the left: its last segments name it.
	path := m.core.WorkspaceDir()
	if over := ansi.StringWidth(path) - (w - 9); over > 0 {
		path = ansi.TruncateLeft(path, over+3, "...")
	}
	head := append([]string{accent.Render("rag-go") + muted.Render(" | ") + path}, m.statusLines(w)...)
	msg, help := m.footerLines(w)
	room := -1
	if h > 0 {
		// On a short terminal, shed wrapped message text, then status
		// details, then help, so at least three settings rows stay visible.
		spare := h - 2 - 3 - len(head) - len(msg) - len(help)
		shrink := func(lines []string, keep int) []string {
			cut := min(-spare, len(lines)-keep)
			if cut <= 0 {
				return lines
			}
			spare += cut
			lines = lines[:len(lines)-cut]
			lines[len(lines)-1] = fit(lines[len(lines)-1], w-3) + "..."
			return lines
		}
		msg, head, help = shrink(msg, 1), shrink(head, 2), shrink(help, 1)
		room = h - 2 - len(head) - len(msg) - len(help)
	}
	lines := append(head, rule)
	lines = append(lines, m.fieldLines(w, room)...)
	lines = append(lines, rule)
	lines = append(lines, msg...)
	return strings.Join(append(lines, help...), "\n")
}

func (m *Model) statusLines(w int) []string {
	s := m.status
	if s == nil {
		return []string{muted.Render("读取状态...")}
	}
	lines := []string{fit(fmt.Sprintf("文件 %d | chunks %d | vectors %d | %s %dd", s.Files, s.Chunks, s.Vectors, s.EmbeddingModel, s.Dimensions), w)}
	switch {
	case s.NeedsRebuild:
		lines = append(lines, wrap(bad.Render("需要 rebuild："+s.RebuildReason), w)...)
	case s.NeedsSync:
		lines = append(lines, wrap(warn.Render("需要 sync：有文件新增、改动或删除"), w)...)
	default:
		lines = append(lines, good.Render("索引是最新的"))
	}
	if s.FreshnessError != "" {
		lines = append(lines, wrap(bad.Render("扫描错误："+s.FreshnessError), w)...)
	}
	if r := s.LastSync; r != nil {
		when := s.LastAttemptAt
		if t, err := time.Parse(time.RFC3339Nano, when); err == nil {
			when = t.Local().Format("2006-01-02 15:04")
		}
		lines = append(lines, muted.Render(fit(fmt.Sprintf("上次 sync %s | indexed %d | skipped %d | failed %d", when, r.Indexed, r.Skipped, r.Failed), w)))
	}
	for i, f := range s.FailedFiles {
		if i == 3 {
			lines = append(lines, muted.Render(fmt.Sprintf("  ...另有 %d 个失败文件", len(s.FailedFiles)-3)))
			break
		}
		lines = append(lines, bad.Render(fit(fmt.Sprintf("  ✗ %s [%s] %s", filepath.Base(f.Path), f.Stage, f.Error), w)))
	}
	return lines
}

// fieldLines renders the settings list, scrolled to keep the cursor visible
// within room lines; room < 0 means unbounded.
func (m *Model) fieldLines(w, room int) []string {
	nameW, valueW := columns(w)
	var lines []string
	at, group := 0, ""
	for i, f := range m.fields {
		if f.group != group {
			group = f.group
			lines = append(lines, bold.Render(group))
		}
		if i == m.cursor {
			at = len(lines)
		}
		cur, old := f.get(m.draft), f.get(m.saved)
		shown := cur
		if shown == "" {
			shown = "(空)"
		}
		value := shown
		switch {
		case m.editing && i == m.cursor:
			value = m.input.View()
		case cur != old:
			value = warn.Render(shown) + muted.Render(" (原 "+old+")")
		case cur == "":
			value = muted.Render(shown)
		}
		mark := "  "
		if i == m.cursor {
			mark = accent.Render("› ")
		}
		// Width pads by display width, so names with CJK text stay aligned.
		name := lipgloss.NewStyle().Width(nameW).Render(fit(f.name, nameW-1))
		lines = append(lines, mark+name+tags[f.impact].Width(tagW).Render("["+impactLabel[f.impact]+"]")+fit(value, valueW))
	}
	if room >= 0 && len(lines) > room {
		start := min(max(0, at-room/2), len(lines)-room)
		lines = lines[start : start+room]
	}
	return lines
}

// footerLines returns the message or progress lines and the key help.
func (m *Model) footerLines(w int) (lines, help []string) {
	switch {
	case m.task == "clean":
		lines = []string{accent.Render("clean...")}
	case m.task != "" && m.prog.Total == 0:
		lines = []string{fit(accent.Render(m.task)+muted.Render(" 扫描文档... | esc 取消"), w)}
	case m.task != "":
		p, r := m.prog, m.prog.Result
		lines = []string{
			fit(accent.Render(m.task)+" "+m.bar.ViewAs(float64(p.Done)/float64(p.Total)), w),
			muted.Render(fit(fmt.Sprintf("%d/%d 文件 | 嵌入 %d chunks | skipped %d | failed %d | %s | esc 取消", p.Done, p.Total, r.Chunks, r.Skipped, r.Failed, filepath.Base(p.Path)), w)),
		}
	case m.msg != "":
		lines = wrap(m.msg, w)
	default:
		lines = []string{""}
		if c := m.changed(); len(c) > 0 {
			worst := immediate
			for _, f := range c {
				worst = max(worst, f.impact)
			}
			effect := [...]string{"保存后立即生效", "保存后下次 sync 生效", "保存后下次 Zotero 同步生效", "保存后需要 rebuild"}[worst]
			lines = wrap(warn.Render(fmt.Sprintf("%d 项未保存 | %s", len(c), effect))+muted.Render(" | ctrl+s 保存 | esc 放弃"), w)
		}
	}
	keys := "方向键上下 选择 | 左右 调整 | enter 输入 | ctrl+s 保存 | s sync | R rebuild | ? 全部按键 | q 退出"
	if m.fullHelp {
		keys = "方向键上下或 j/k 选择 | 左右或 h/l 调整 | enter 输入 | u 还原此项 | ctrl+s 保存 | esc 放弃/取消 | s sync | R rebuild | c clean | r 刷新 | ? 收起 | q 退出"
	}
	return lines, wrap(muted.Render(keys), w)
}
