// Package tui is the interactive workspace list and settings panel behind
// rag tui.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	"github.com/muesli/termenv"
)

const cleanKeep = 3

// Status colors are the terminal's 16 ANSI colors, so they follow its theme.
// The accent is a fixed muted blue instead: themes map ANSI magenta to
// anything up to crimson.
var (
	accentColor = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#81A1C1"}
	accent      = lipgloss.NewStyle().Foreground(accentColor)
	muted       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	warn        = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	bad         = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	good        = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	info        = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	bold        = lipgloss.NewStyle().Bold(true)
	tags        = [...]lipgloss.Style{good, info, info, warn}
	cursorBar   = lipgloss.NewStyle().Background(accentColor)
	// Text on a colored background is black on dark terminals and white on
	// light ones, where the ANSI colors are darker.
	onColor   = lipgloss.AdaptiveColor{Light: "15", Dark: "0"}
	activeTab = lipgloss.NewStyle().Background(accentColor).Foreground(onColor)
)

// maxWidth caps the layout so wide terminals do not push badges and tags
// far from what they describe.
const maxWidth = 100

const (
	pageList = iota
	pageSettings
)

// mark is the three-column gutter that carries the cursor: a solid bar of
// background color, or ">" where colors are off.
func mark(selected bool) string {
	switch {
	case !selected:
		return "   "
	case lipgloss.ColorProfile() == termenv.Ascii:
		return " > "
	}
	return " " + cursorBar.Render(" ") + " "
}

// badge renders a short status label on a colored background.
func badge(text, bg string) string {
	var fg lipgloss.TerminalColor = onColor
	if bg == "8" {
		fg = lipgloss.Color("15")
	}
	return lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(fg).Render(" " + text + " ")
}

func statusBadge(s *rag.Status) string {
	switch {
	case s == nil:
		return badge("读取中", "8")
	case s.NeedsRebuild:
		return badge("需 rebuild", "1")
	case s.NeedsSync:
		return badge("需 sync", "3")
	}
	return badge("最新", "2")
}

type (
	statusMsg struct {
		core   *rag.Core
		status rag.Status
	}
	progressMsg rag.IndexProgress
	// doneMsg ends a task; path is set for a sync started from the list.
	doneMsg struct {
		task, path string
		result     any
		err        error
	}
	savedMsg struct {
		cfg model.Config
		err error
	}
	errMsg struct{ err error }
)

type Model struct {
	ctx  context.Context
	open func(dir string) (*rag.Core, error)
	page int
	list
	// core is the workspace on the settings page, nil until one is opened.
	core         *rag.Core
	fields       []field
	saved, draft model.Config
	cursor       int
	editing      bool
	input        textinput.Model
	pick         int // the highlighted entry of matches while typing
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

// Run opens the workspace at dir (discovered when empty) on the settings page
// and runs the panel on the terminal until the user quits. Outside any
// workspace, with dir empty, it starts on the workspace list.
func Run(ctx context.Context, dir string) error {
	var p *tea.Program
	open := func(dir string) (*rag.Core, error) {
		return rag.Open(rag.Options{WorkspaceDir: dir, Progress: func(x rag.IndexProgress) { p.Send(progressMsg(x)) }})
	}
	core, err := open(dir)
	if dir == "" && errors.Is(err, workspace.ErrNotFound) {
		core, err = nil, nil
	}
	if err != nil {
		return err
	}
	// Query the background once now: asked mid-render, the terminal's reply
	// would race the program's input reader.
	lipgloss.HasDarkBackground()
	m, err := New(ctx, core, open)
	if err != nil {
		return err
	}
	defer func() {
		if m.core != nil {
			m.core.Close()
		}
	}()
	p = tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	_, err = p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// New starts on the settings page of core, or on the workspace list when core
// is nil. open opens a listed workspace.
func New(ctx context.Context, core *rag.Core, open func(string) (*rag.Core, error)) (*Model, error) {
	fill := accentColor.Dark
	if !lipgloss.HasDarkBackground() {
		fill = accentColor.Light
	}
	m := &Model{ctx: ctx, open: open, fields: fields(), page: pageList,
		input: textinput.New(), bar: progress.New(progress.WithSolidFill(fill), progress.WithFillCharacters('=', '-'), progress.WithWidth(30))}
	if core != nil {
		if err := m.setCore(core); err != nil {
			return nil, err
		}
		m.page = pageSettings
	}
	m.loadEntries()
	for i, e := range m.entries {
		if core != nil && e.Path == core.WorkspaceDir() {
			m.at = i
		}
	}
	return m, nil
}

// setCore shows core on the settings page with its saved configuration.
func (m *Model) setCore(core *rag.Core) error {
	cfg, err := model.LoadConfig(configPath(core))
	if err != nil {
		return err
	}
	if m.core != nil {
		m.core.Close()
	}
	m.core, m.saved, m.draft = core, cfg, clone(cfg)
	m.cursor, m.status, m.editing = 0, nil, false
	return nil
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

func (m *Model) Init() tea.Cmd { return tea.Batch(m.refresh(), m.loadRows()) }

func (m *Model) refresh() tea.Cmd {
	if m.core == nil {
		return nil
	}
	core := m.core
	return func() tea.Msg {
		s, err := core.Status(m.ctx)
		if err != nil {
			return errMsg{err}
		}
		return statusMsg{core, s}
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

// apply edits the draft, keeping the change only when the configuration stays
// valid. The saved configuration is valid, so a refusal always concerns the
// value just changed or a setting it is bounded by.
func (m *Model) apply(edit func(*model.Config) error) error {
	next := clone(m.draft)
	if err := edit(&next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	m.draft = next
	m.msg = m.missingKey()
	return nil
}

// missingKey warns when the draft points a provider somewhere new that has no
// credential to send, which rag init asks for once the change is saved.
func (m *Model) missingKey() string {
	for _, p := range [][2]model.ProviderConfig{{m.draft.Embedding, m.saved.Embedding}, {m.draft.Reranker, m.saved.Reranker}} {
		if p[0] == p[1] || p[0].Type == "none" || p[0].APIKeyEnv == "" {
			continue
		}
		if key, err := workspace.Credential(m.core.WorkspaceDir(), p[0]); err == nil && key == "" {
			return warn.Render("工作区没有 " + p[0].APIKeyEnv + "，查询会失败；保存后在终端运行 rag init 存入")
		}
	}
	return ""
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

func (m *Model) start(task, path string, fn func(context.Context) (any, error)) tea.Cmd {
	if len(m.changed()) > 0 {
		m.msg = warn.Render("先 ctrl+s 保存或 esc 放弃修改")
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.task, m.cancel, m.prog, m.msg = task, cancel, rag.IndexProgress{}, ""
	return func() tea.Msg {
		r, err := fn(ctx)
		return doneMsg{task, path, r, err}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		w := min(m.width, maxWidth)
		_, valueW := columns(w)
		m.input.Width = max(1, valueW-lipgloss.Width(m.input.Prompt)-1)
		m.bar.Width = max(10, min(50, w-9))
		// Terminals reflow or scroll their own content while resizing; clear
		// it so no stale rows survive under the repainted frame.
		return m, tea.ClearScreen
	case statusMsg:
		if msg.core == m.core {
			m.status = &msg.status
		}
	case rowMsg:
		if r := m.rows[msg.path]; r != nil {
			r.status, r.err = msg.status, msg.err
		}
	case errMsg:
		m.msg = bad.Render(msg.err.Error())
	case progressMsg:
		m.prog = rag.IndexProgress(msg)
	case doneMsg:
		m.task, m.cancel = "", nil
		if msg.path != "" {
			return m, m.synced(msg)
		}
		m.msg = done(msg)
		if r, ok := msg.result.(rag.CleanupResult); ok && msg.err == nil && r.DryRun && len(r.Removed) > 0 {
			m.confirm = "clean"
		}
		return m, tea.Batch(m.refresh(), m.loadRow(m.core.WorkspaceDir()))
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
		case "up":
			if n := len(m.matches()); n > 0 {
				m.pick = (m.pick + n - 1) % n
			}
		case "down":
			if n := len(m.matches()); n > 0 {
				m.pick = (m.pick + 1) % n
			}
		case "enter":
			f, v := m.fields[m.cursor], m.input.Value()
			if c := m.matches(); len(c) > 0 {
				v = c[min(m.pick, len(c)-1)]
			}
			if err := m.apply(func(c *model.Config) error { return f.set(c, v) }); err != nil {
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
			m.pick = 0
			return m, cmd
		}
		return m, nil
	}
	// tab switches pages, 1 and 2 pick one. Settings without an open
	// workspace opens the one selected in the list.
	if page, ok := map[string]int{"tab": 1 - m.page, "1": pageList, "2": pageSettings}[k.String()]; ok {
		m.confirm = ""
		if page == pageSettings && m.core == nil {
			m.page = pageList
			return m, m.listKey("enter", "")
		}
		m.page = page
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
	case "?":
		m.fullHelp = !m.fullHelp
		return m, nil
	}
	if m.page == pageList {
		return m, m.listKey(k.String(), confirm)
	}
	switch k.String() {
	case "up", "k":
		m.cursor = (m.cursor + len(m.fields) - 1) % len(m.fields)
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(m.fields)
	case "left", "h", "right", "l":
		dir := 1
		if k.String() == "left" || k.String() == "h" {
			dir = -1
		}
		f := m.fields[m.cursor]
		if err := m.apply(func(c *model.Config) error { return f.adjust(c, dir) }); err != nil {
			m.msg = bad.Render(err.Error())
		}
	case "enter":
		// A field with options takes no typed value; enter picks the next one.
		if f := m.fields[m.cursor]; len(f.options) > 0 {
			if err := m.apply(func(c *model.Config) error { return f.adjust(c, 1) }); err != nil {
				m.msg = bad.Render(err.Error())
			}
			return m, nil
		}
		// A field with choices starts empty to search them; enter on
		// nothing typed keeps the current value.
		f := m.fields[m.cursor]
		m.editing, m.pick, m.input.Placeholder = true, 0, ""
		m.input.SetValue(f.get(m.draft))
		if f.offered(m.draft) != nil {
			m.input.Placeholder = f.get(m.draft)
			m.input.SetValue("")
		}
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "u":
		f, saved := m.fields[m.cursor], m.saved
		if err := m.apply(func(c *model.Config) error {
			if f.restore != nil {
				f.restore(c, saved)
				return nil
			}
			return f.set(c, f.get(saved))
		}); err != nil {
			m.msg = bad.Render(err.Error())
		}
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
		return m, m.start("sync", "", func(ctx context.Context) (any, error) { return m.core.Sync(ctx) })
	case "R":
		if confirm != "rebuild" {
			m.confirm, m.msg = "rebuild", warn.Render("rebuild 会重新解析并嵌入全部文档，期间旧索引仍可查询。再按 R 确认，esc 取消")
			return m, nil
		}
		return m, m.start("rebuild", "", func(ctx context.Context) (any, error) { return m.core.Rebuild(ctx) })
	case "c":
		dry := confirm != "clean"
		return m, m.start("clean", "", func(ctx context.Context) (any, error) { return m.core.Cleanup(ctx, cleanKeep, dry) })
	case "r":
		return m, m.refresh()
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

// columns splits a terminal width, after the cursor gutter, into the name and
// value columns before the fixed impact tag column, shrinking names first on
// narrow terminals.
func columns(w int) (nameW, valueW int) {
	nameW = min(29, max(12, w-3-tagW-14))
	return nameW, w - 3 - nameW - tagW
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
	w = min(w, maxWidth)
	head := []string{m.tabs(w)}
	if m.page == pageSettings {
		head = append(append(head, ""), m.cardLines(w)...)
	}
	msg, help := m.footerLines(w)
	room := -1
	if h > 0 {
		// On a short terminal, shed wrapped message text, then card
		// details, then help, so at least three body rows stay visible.
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
		msg, head, help = shrink(msg, 1), shrink(head, min(3, len(head))), shrink(help, 1)
		room = h - 2 - len(head) - len(msg) - len(help)
	}
	body := m.listLines(w, room)
	if m.page == pageSettings {
		body = m.fieldLines(w, room)
	}
	lines := append(head, "")
	lines = append(lines, body...)
	lines = append(lines, "")
	lines = append(lines, msg...)
	return strings.Join(append(lines, help...), "\n")
}

// tabs renders the title and page tabs, with the list's counts at the right.
func (m *Model) tabs(w int) string {
	tab := func(name string, on bool) string {
		if on {
			return activeTab.Render(" " + name + " ")
		}
		return muted.Render(" " + name + " ")
	}
	settings := "设置"
	if m.core != nil {
		settings += ": " + m.name(m.core.WorkspaceDir())
	}
	line := " " + accent.Bold(true).Render("rag-go") + "   " + tab("Workspaces", m.page == pageList) + "  " + tab(settings, m.page == pageSettings)
	if m.page == pageList {
		count := fmt.Sprintf("%d 个", len(m.entries))
		if n := m.missing(); n > 0 {
			count += fmt.Sprintf(" | %d missing", n)
		}
		if pad := w - 1 - ansi.StringWidth(line) - ansi.StringWidth(count); pad >= 2 {
			line += strings.Repeat(" ", pad) + muted.Render(count)
		}
	}
	return fit(line, w)
}

// card renders a workspace as a name line with its badge at the right edge
// and detail lines below, all behind the cursor gutter.
func card(w int, selected bool, name, badge string, details ...string) []string {
	avail := w - 4
	name = fit(name, avail-ansi.StringWidth(badge)-1)
	pad := max(1, avail-ansi.StringWidth(name)-ansi.StringWidth(badge))
	lines := []string{mark(selected) + name + strings.Repeat(" ", pad) + badge}
	for _, d := range details {
		lines = append(lines, mark(selected)+fit(d, avail))
	}
	return lines
}

// detail joins a workspace path and its facts, shortening the path from the
// left so the facts stay visible.
func detail(path string, facts ...string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(filepath.Separator)) {
		path = "~" + path[len(home):]
	}
	return strings.Join(append([]string{path}, facts...), " | ")
}

// shorten cuts the path that starts s from the left until s fits width w.
func shorten(s string, w int) string {
	path, rest, _ := strings.Cut(s, " | ")
	if rest != "" {
		rest = " | " + rest
	}
	if over := ansi.StringWidth(s) - w; over > 0 && ansi.StringWidth(path)-over-3 >= 8 {
		path = ansi.TruncateLeft(path, over+3, "...")
	}
	return path + rest
}

func when(s rag.Status, layout string) string {
	if t, err := time.Parse(time.RFC3339Nano, s.LastAttemptAt); err == nil {
		return t.Local().Format(layout)
	}
	return s.LastAttemptAt
}

// cardLines heads the settings page: the open workspace's card, then any
// rebuild reason, scan error and failed files.
func (m *Model) cardLines(w int) []string {
	dir := m.core.WorkspaceDir()
	s := m.status
	if s == nil {
		return card(w, true, bold.Render(m.name(dir)), statusBadge(nil), muted.Render(shorten(detail(dir), w-4)))
	}
	details := []string{muted.Render(shorten(detail(dir, fmt.Sprintf("%d 文件", s.Files), fmt.Sprintf("%d chunks", s.Chunks), fmt.Sprintf("%s %dd", s.EmbeddingModel, s.Dimensions)), w-4))}
	if r := s.LastSync; r != nil {
		details = append(details, muted.Render(fmt.Sprintf("上次 sync %s | indexed %d | skipped %d | failed %d", when(*s, "2006-01-02 15:04"), r.Indexed, r.Skipped, r.Failed)))
	}
	lines := card(w, true, bold.Render(m.name(dir)), statusBadge(s), details...)
	indent := func(l []string) []string {
		for i := range l {
			l[i] = "   " + l[i]
		}
		return l
	}
	if s.NeedsRebuild {
		lines = append(lines, indent(wrap(bad.Render("需要 rebuild："+s.RebuildReason), w-3))...)
	}
	if s.FreshnessError != "" {
		lines = append(lines, indent(wrap(bad.Render("扫描错误："+s.FreshnessError), w-3))...)
	}
	for i, f := range s.FailedFiles {
		if i == 3 {
			lines = append(lines, muted.Render(fmt.Sprintf("   ...另有 %d 个失败文件", len(s.FailedFiles)-3)))
			break
		}
		lines = append(lines, bad.Render(fit(fmt.Sprintf("   ✗ %s [%s] %s", filepath.Base(f.Path), f.Stage, f.Error), w)))
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
			lines = append(lines, " "+bold.Render(group))
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
		if !(m.editing && i == m.cursor) {
			if c := f.offered(m.draft); slices.Contains(c, cur) {
				value += muted.Render(fmt.Sprintf(" %d/%d", slices.Index(c, cur)+1, len(c)))
			}
		}
		name := fit(f.name, nameW-1)
		if i == m.cursor {
			name = accent.Bold(true).Render(name)
		}
		// Width pads by display width, so names with CJK text stay aligned.
		name = lipgloss.NewStyle().Width(nameW).Render(name)
		value = lipgloss.NewStyle().Width(valueW).Render(fit(value, valueW-1))
		lines = append(lines, mark(i == m.cursor)+name+value+tags[f.impact].Render(impactLabel[f.impact]))
		if i == m.cursor {
			lines = append(lines, m.matchLines(nameW, valueW)...)
		}
	}
	if room >= 0 && len(lines) > room {
		// Center the cursor, but show the matches below it when they fit.
		end := at + 1 + len(m.matches())
		start := min(max(0, at-room/2, end-room), at, len(lines)-room)
		lines = lines[start : start+room]
	}
	return lines
}

// matchLines renders the matches under the field being typed, the picked one
// highlighted, with the typed text underlined in each.
func (m *Model) matchLines(nameW, valueW int) []string {
	q := strings.ToLower(strings.TrimSpace(m.input.Value()))
	matches := m.matches()
	var lines []string
	for i, c := range matches {
		style, lead := lipgloss.NewStyle(), "  "
		if i == m.pick {
			style, lead = accent, accent.Render("> ")
		}
		s := style.Render(c)
		// Byte offsets of the lowercased name hold only when lowercasing kept
		// its length, as for the ASCII model names.
		if j := strings.Index(strings.ToLower(c), q); q != "" && j >= 0 && len(strings.ToLower(c)) == len(c) {
			s = style.Render(c[:j]) + style.Underline(true).Render(c[j:j+len(q)]) + style.Render(c[j+len(q):])
		}
		if q != "" && i == len(matches)-1 && !slices.Contains(m.fields[m.cursor].offered(m.draft), c) {
			s += muted.Render(" 按原文写入")
		}
		// Matches carry no tag, so they may use its column.
		lines = append(lines, strings.Repeat(" ", 3+nameW)+fit(lead+s, valueW+tagW-1))
	}
	return lines
}

// keys renders key help as highlighted keys followed by muted labels after
// lead, wrapping only between pairs.
func keys(w int, lead string, pairs ...string) []string {
	lines, line := []string{}, " "+lead
	for i := 0; i+1 < len(pairs); i += 2 {
		part := accent.Render(pairs[i]) + " " + muted.Render(pairs[i+1])
		switch {
		case strings.TrimSpace(line) == "":
			line += part
		case ansi.StringWidth(line)+2+ansi.StringWidth(part) > w:
			lines, line = append(lines, line), " "+part
		default:
			line += "  " + part
		}
	}
	lines = append(lines, line)
	for i := range lines {
		lines[i] = fit(lines[i], w)
	}
	return lines
}

// footerLines returns the message or progress lines and the key help.
func (m *Model) footerLines(w int) (lines, help []string) {
	task := m.task
	if m.runTotal > 0 {
		task += fmt.Sprintf(" [%d/%d] %s", m.runDone+1, m.runTotal, m.name(m.running))
	}
	switch {
	case m.task == "clean":
		lines = []string{" " + accent.Render("clean...")}
	case m.task != "" && m.prog.Total == 0:
		lines = []string{fit(" "+accent.Render(task)+muted.Render(" 扫描文档..."), w)}
	case m.task != "":
		p, r := m.prog, m.prog.Result
		lines = []string{
			fit(" "+accent.Render(task)+" "+m.bar.ViewAs(float64(p.Done)/float64(p.Total)), w),
			muted.Render(fit(fmt.Sprintf(" %d/%d 文件 | 嵌入 %d chunks | skipped %d | failed %d | %s", p.Done, p.Total, r.Chunks, r.Skipped, r.Failed, filepath.Base(p.Path)), w)),
		}
	case m.msg != "":
		lines = wrap(" "+m.msg, w)
	default:
		lines = []string{""}
		if c := m.changed(); len(c) > 0 {
			worst := immediate
			for _, f := range c {
				worst = max(worst, f.impact)
			}
			effect := [...]string{"保存后立即生效", "保存后下次 sync 生效", "保存后下次 Zotero 同步生效", "保存后需要 rebuild"}[worst]
			lines = keys(w, warn.Render(fmt.Sprintf("%d 项未保存 | %s", len(c), effect)), "ctrl+s", "保存", "esc", "放弃")
		}
	}
	switch {
	case m.task != "":
		help = keys(w, "", "esc", "取消", "tab", "切换页签", "ctrl+c", "退出")
	case m.matches() != nil:
		help = keys(w, "", "上下", "选择候选", "enter", "选中", "esc", "取消")
	case m.page == pageList:
		help = keys(w, "", "上下", "选择", "enter", "打开", "s", "sync", "S", "全部 sync", "r", "刷新", "tab", "设置", "q", "退出")
	case m.fullHelp:
		help = keys(w, "", "上下或 j/k", "选择", "左右或 h/l", "调整", "enter", "输入/切换", "u", "还原此项", "ctrl+s", "保存", "esc", "放弃/取消", "s", "sync", "R", "rebuild", "c", "clean", "r", "刷新", "tab 或 1/2", "切换页签", "?", "收起", "q", "退出")
	default:
		help = keys(w, "", "上下", "选择", "左右", "调整", "enter", "输入/切换", "ctrl+s", "保存", "s", "sync", "R", "rebuild", "tab", "列表", "?", "全部", "q", "退出")
	}
	return lines, help
}
