package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// memTreeWidth is the fixed left-pane width (spec: "Left (28 cols)"). Not
// window-responsive — the Review tab's own fixed-width table set the
// precedent for a list column that doesn't reflow with the terminal.
const memTreeWidth = 28

// memSearchCap bounds live search results — a large vault searched on every
// keystroke must never become an unbounded scan/render.
const memSearchCap = 200

// memPane is which of the two panes (tree / content) currently owns j/k/↑/↓.
type memPane int

const (
	memPaneTree memPane = iota
	memPaneContent
)

// memFile is one scanned memory file: its full line-split content (cached so
// search and rendering never re-read the vault) plus the two facts computed
// once per scan.
type memFile struct {
	Name      string
	Lines     []string
	FactCount int
	Hot       bool // RecallStatsByFile: Hits30 >= 1
}

// memSearchHit is one live-search match against the scan's cached content.
type memSearchHit struct {
	File string
	Line int // 1-based
	Text string
}

// memBrowserModel is the "Memory" tab: a two-pane vault browser (taxonomy
// file tree + focused file content) with inline edit/delete/new-fact actions
// that queue PENDING changes — it never writes the vault directly (see
// memQueueCmd). Scans on tab-enter only, mirroring reviewModel (Sprint 13).
type memBrowserModel struct {
	store      *memory.Store
	logger     *audit.Logger
	pendingMgr *pending.Manager

	top      []memFile // taxonomy-ordered top-level files
	projects []memFile // projects/<slug>.md children, name-sorted

	scanned      bool // true after the first completed scan (default-expand gate)
	projectsOpen bool
	scanning     bool
	scanErr      string

	focus         memPane
	treeCursor    int
	focused       string // name of the file shown in the right pane; "" = none
	contentCursor int

	searching     bool
	searchInput   textinput.Model
	searchResults []memSearchHit
	searchCursor  int

	editing     bool
	editInput   textinput.Model
	editLineIdx int

	creating    bool
	createInput textinput.Model

	confirmDelete  bool
	confirmLineIdx int

	status string
}

func newMemBrowserModel(store *memory.Store, logger *audit.Logger, pendingMgr *pending.Manager) memBrowserModel {
	search := textinput.New()
	search.Prompt = ""
	edit := textinput.New()
	edit.Prompt = ""
	create := textinput.New()
	create.Prompt = ""
	return memBrowserModel{
		store:       store,
		logger:      logger,
		pendingMgr:  pendingMgr,
		searchInput: search,
		editInput:   edit,
		createInput: create,
	}
}

// capturesInput reports whether the Memory tab owns every keystroke right
// now (typing a search/edit/new-fact value, or a pending y/n confirm) — the
// app-wide switcher must not steal these keys, same contract as
// organizeModel.capturesInput.
func (m memBrowserModel) capturesInput() bool {
	return m.searching || m.editing || m.creating || m.confirmDelete
}

// memScanMsg carries one completed vault scan: the taxonomy tree plus
// per-file fact counts and hot/cold, computed off the UI thread.
type memScanMsg struct {
	top      []memFile
	projects []memFile
	err      error
}

// memActionMsg carries the result of queueing a pending change.
type memActionMsg struct {
	err error
}

// Refresh fires the scan. Called by app.go's refreshCurrentScreen on
// tab-enter only, per the same cost contract as reviewModel.Refresh.
func (m memBrowserModel) Refresh() tea.Cmd {
	return m.scanCmd()
}

// scanCmd loads the tree (Store.List + one View per file for line content and
// fact counts) plus RecallStatsByFile for the hot/cold marker — all the I/O
// this tab needs, done once per tab-enter/rescan off the UI thread.
func (m memBrowserModel) scanCmd() tea.Cmd {
	store := m.store
	logger := m.logger
	return func() tea.Msg {
		if store == nil {
			return memScanMsg{}
		}
		files, err := store.List()
		if err != nil {
			return memScanMsg{err: err}
		}
		topInfo, projInfo := orderMemScan(files)

		var hot map[string]bool
		if logger != nil {
			if stats, serr := logger.RecallStatsByFile(); serr == nil {
				hot = hotFileSet(stats)
			}
		}

		build := func(list []memory.FileInfo) []memFile {
			out := make([]memFile, 0, len(list))
			for _, f := range list {
				content, _ := store.View(f.Name)
				lines := strings.Split(content, "\n")
				out = append(out, memFile{
					Name:      f.Name,
					Lines:     lines,
					FactCount: countBullets(lines),
					Hot:       hot[f.Name],
				})
			}
			return out
		}

		return memScanMsg{top: build(topInfo), projects: build(projInfo)}
	}
}

// orderMemScan splits the vault's file list into taxonomy-ordered top-level
// files and the projects/<slug>.md children (name-sorted) — a pure function
// so the ordering is directly testable without touching disk. Files not in
// the taxonomy (CLAUDE.md, providers.md, unified_memory.md, …) are excluded:
// this tab edits the user's own facts, not agent config.
func orderMemScan(files []memory.FileInfo) (top, projects []memory.FileInfo) {
	rank := map[string]int{}
	for i, name := range memory.OrderedFiles() {
		rank[name] = i
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name, "projects/") {
			projects = append(projects, f)
			continue
		}
		if _, ok := rank[f.Name]; ok {
			top = append(top, f)
		}
	}
	sort.Slice(top, func(i, j int) bool { return rank[top[i].Name] < rank[top[j].Name] })
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return top, projects
}

// hotFileSet reduces RecallStatsByFile to the "hot" set: >=1 hit in the last
// 30 days. A pure reduction, so hot/cold classification is testable without
// a live audit DB.
func hotFileSet(stats []audit.RecallFileStats) map[string]bool {
	hot := make(map[string]bool, len(stats))
	for _, s := range stats {
		if s.Hits30 >= 1 {
			hot[s.File] = true
		}
	}
	return hot
}

// countBullets counts bullet-fact lines ("- " / "* ", same convention as
// internal/memory's stale-fact scan) in a file's content.
func countBullets(lines []string) int {
	n := 0
	for _, l := range lines {
		if memIsBullet(l) {
			n++
		}
	}
	return n
}

func memIsBullet(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")
}

// memQueueCmd queues a diff for human approval via pending.Manager.WriteFrom —
// every Memory-tab mutation goes through this, never a direct vault write.
func memQueueCmd(pendingMgr *pending.Manager, file, diff string) tea.Cmd {
	return func() tea.Msg {
		if pendingMgr == nil {
			return memActionMsg{err: fmt.Errorf("no pending manager configured")}
		}
		_, err := pendingMgr.WriteFrom(file, diff, "dashboard")
		return memActionMsg{err: err}
	}
}

// memEditDiff/memDeleteDiff/memNewFactDiff build the WriteFrom diff bodies.
// Bullet lines already carry their own "- " marker, so an edit/delete diff
// line reads "-- old fact" / "+- new fact" (the diff's leading -/+ plus the
// fact's own bullet dash) — pending.ApplyDiff matches on TrimSpace, so the
// double dash is exactly what a bullet's own "-" round-trips to.
func memEditDiff(oldLine, newLine string) string {
	return "-" + oldLine + "\n+" + newLine + "\n"
}

func memDeleteDiff(line string) string {
	return "-" + line + "\n"
}

func memNewFactDiff(text string, today time.Time) string {
	return fmt.Sprintf("+- %s [%s]\n", strings.TrimSpace(text), today.Format("2006-01-02"))
}

// memCountOccurrences counts lines whose trimmed text equals target.
// pending.ApplyDiff's "-" deletion matches by TrimSpace CONTENT across the
// whole file, not by line number — if a line's text isn't unique, an
// edit/delete diff built from it would remove/replace every copy on
// approval, not just the one the user selected. Callers use this to refuse
// queueing when the target isn't unique (see updateEditing/updateConfirmDelete).
func memCountOccurrences(lines []string, target string) int {
	n := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == target {
			n++
		}
	}
	return n
}

// memDuplicateLineStatus is the shared refusal message for a non-unique
// edit/delete target.
func memDuplicateLineStatus(n int) string {
	return fmt.Sprintf("this line appears %d× in the file — approving would change every copy; dedupe via `auxly organize` first", n)
}

// ---- tree row addressing ----

// treeRowCount is the number of flattened, currently-visible tree rows: every
// top-level file, plus (when there are any project files) one header row and
// its children iff expanded.
func (m memBrowserModel) treeRowCount() int {
	n := len(m.top)
	if len(m.projects) > 0 {
		n++
		if m.projectsOpen {
			n += len(m.projects)
		}
	}
	return n
}

func (m memBrowserModel) treeRowIsProjectsHeader(i int) bool {
	return len(m.projects) > 0 && i == len(m.top)
}

// treeRowFile returns the file backing a flattened row index, or ok=false for
// the projects header row or an out-of-range index.
func (m memBrowserModel) treeRowFile(i int) (memFile, bool) {
	if i < 0 || i >= m.treeRowCount() {
		return memFile{}, false
	}
	if i < len(m.top) {
		return m.top[i], true
	}
	if m.treeRowIsProjectsHeader(i) {
		return memFile{}, false
	}
	return m.projects[i-len(m.top)-1], true
}

// treeRowIndexForFile finds the flattened row for a file by name (used by
// search-jump to move the tree cursor onto the matched file).
func (m memBrowserModel) treeRowIndexForFile(name string) (int, bool) {
	for i, f := range m.top {
		if f.Name == name {
			return i, true
		}
	}
	for i, f := range m.projects {
		if f.Name == name {
			return len(m.top) + 1 + i, true
		}
	}
	return 0, false
}

func (m memBrowserModel) findFile(name string) (memFile, bool) {
	for _, f := range m.top {
		if f.Name == name {
			return f, true
		}
	}
	for _, f := range m.projects {
		if f.Name == name {
			return f, true
		}
	}
	return memFile{}, false
}

func (m memBrowserModel) focusedLines() []string {
	f, ok := m.findFile(m.focused)
	if !ok {
		return nil
	}
	return f.Lines
}

// syncFocusFromTree updates the right-pane's focused file when the tree
// cursor lands on a file row. Landing on the projects header row leaves the
// previous selection sticky (it isn't a file).
func (m *memBrowserModel) syncFocusFromTree() {
	if f, ok := m.treeRowFile(m.treeCursor); ok && f.Name != m.focused {
		m.focused = f.Name
		m.contentCursor = 0
	}
}

// ---- Update ----

func (m memBrowserModel) Update(msg tea.Msg) (memBrowserModel, tea.Cmd) {
	switch msg := msg.(type) {
	case memScanMsg:
		m.scanning = false
		if msg.err != nil {
			m.scanErr = msg.err.Error()
			return m, nil
		}
		m.scanErr = ""
		m.top = msg.top
		m.projects = msg.projects
		if !m.scanned && len(m.projects) > 0 {
			m.projectsOpen = true
		}
		m.scanned = true
		if n := m.treeRowCount(); m.treeCursor >= n {
			m.treeCursor = n - 1
		}
		if m.treeCursor < 0 {
			m.treeCursor = 0
		}
		if m.focused != "" {
			if _, ok := m.findFile(m.focused); !ok {
				m.focused = ""
				m.contentCursor = 0
			}
		}
		m.syncFocusFromTree()
		// A rescan replaces the cached Lines slices under any in-flight
		// edit/delete: editLineIdx/confirmLineIdx would then address content
		// the user never saw (and content-match queueing would apply
		// cleanly against the wrong line). Cancel the action instead of
		// risking that — the user re-selects and re-issues it.
		if m.editing || m.creating || m.confirmDelete {
			m.editing = false
			m.editInput.Blur()
			m.editInput.SetValue("")
			m.creating = false
			m.createInput.Blur()
			m.createInput.SetValue("")
			m.confirmDelete = false
			m.status = "vault rescanned — action cancelled, re-select the fact"
		} else {
			m.status = ""
		}
		return m, nil

	case memActionMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
		} else {
			m.status = "queued for approval — see Approvals tab"
		}
		return m, nil

	case tea.KeyMsg:
		switch {
		case m.searching:
			return m.updateSearching(msg)
		case m.editing:
			return m.updateEditing(msg)
		case m.creating:
			return m.updateCreating(msg)
		case m.confirmDelete:
			return m.updateConfirmDelete(msg)
		default:
			return m.updateNormal(msg)
		}
	}
	return m, nil
}

func (m memBrowserModel) updateNormal(msg tea.KeyMsg) (memBrowserModel, tea.Cmd) {
	switch msg.String() {
	case "tab":
		if m.focus == memPaneTree {
			m.focus = memPaneContent
		} else {
			m.focus = memPaneTree
		}
		m.status = ""
	case "h":
		m.focus = memPaneTree
	case "l":
		m.focus = memPaneContent
	case "j", "down":
		if m.focus == memPaneTree {
			if m.treeCursor < m.treeRowCount()-1 {
				m.treeCursor++
				m.syncFocusFromTree()
			}
		} else if lines := m.focusedLines(); m.contentCursor < len(lines)-1 {
			m.contentCursor++
		}
		m.status = ""
	case "k", "up":
		if m.focus == memPaneTree {
			if m.treeCursor > 0 {
				m.treeCursor--
				m.syncFocusFromTree()
			}
		} else if m.contentCursor > 0 {
			m.contentCursor--
		}
		m.status = ""
	case "enter":
		if m.focus == memPaneTree {
			if m.treeRowIsProjectsHeader(m.treeCursor) {
				m.projectsOpen = !m.projectsOpen
			} else {
				m.focus = memPaneContent
			}
		}
	case "/":
		m.searching = true
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		m.searchResults = nil
		m.searchCursor = 0
		m.status = ""
	case "e":
		line, ok := m.currentContentLine()
		if !ok {
			break
		}
		if !memIsBullet(line) {
			m.status = "only bullet facts are editable"
			break
		}
		m.editing = true
		m.editLineIdx = m.contentCursor
		m.editInput.SetValue(line)
		m.editInput.CursorEnd()
		m.editInput.Focus()
		m.status = ""
	case "d":
		line, ok := m.currentContentLine()
		if !ok {
			break
		}
		if !memIsBullet(line) {
			m.status = "only bullet facts are editable"
			break
		}
		m.confirmDelete = true
		m.confirmLineIdx = m.contentCursor
		m.status = "delete? y/n"
	case "n":
		if m.focused == "" {
			m.status = "select a file first"
			break
		}
		m.creating = true
		m.createInput.SetValue("")
		m.createInput.Focus()
		m.status = ""
	case "r":
		m.scanning = true
		m.status = ""
		m.scanErr = ""
		return m, m.Refresh()
	}
	return m, nil
}

// currentContentLine returns the trimmed line under the content cursor, or
// ok=false when there is no focused file / the file has no lines.
func (m memBrowserModel) currentContentLine() (string, bool) {
	lines := m.focusedLines()
	if m.contentCursor < 0 || m.contentCursor >= len(lines) {
		return "", false
	}
	return strings.TrimSpace(lines[m.contentCursor]), true
}

func (m memBrowserModel) updateSearching(msg tea.KeyMsg) (memBrowserModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.searching = false
		m.searchInput.Blur()
		m.searchResults = nil
		m.status = ""
		return m, nil
	case tea.KeyEnter:
		if m.searchCursor >= 0 && m.searchCursor < len(m.searchResults) {
			hit := m.searchResults[m.searchCursor]
			m.focused = hit.File
			m.contentCursor = hit.Line - 1
			if strings.HasPrefix(hit.File, "projects/") {
				m.projectsOpen = true
			}
			if idx, ok := m.treeRowIndexForFile(hit.File); ok {
				m.treeCursor = idx
			}
			m.focus = memPaneContent
		}
		m.searching = false
		m.searchInput.Blur()
		m.searchResults = nil
		return m, nil
	case tea.KeyUp:
		if m.searchCursor > 0 {
			m.searchCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.searchCursor < len(m.searchResults)-1 {
			m.searchCursor++
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		m.searchResults = memSearchAll(m.top, m.projects, m.searchInput.Value(), memSearchCap)
		m.searchCursor = 0
		return m, cmd
	}
}

func (m memBrowserModel) updateEditing(msg tea.KeyMsg) (memBrowserModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.editing = false
		m.editInput.Blur()
		m.status = ""
		return m, nil
	case tea.KeyEnter:
		m.editing = false
		m.editInput.Blur()
		lines := m.focusedLines()
		if m.editLineIdx < 0 || m.editLineIdx >= len(lines) {
			return m, nil
		}
		oldLine := strings.TrimSpace(lines[m.editLineIdx])
		newLine := strings.TrimSpace(m.editInput.Value())
		if newLine == "" {
			m.status = "no change"
			return m, nil
		}
		// Bullet-based tooling (decay scans, recall hashes) keys off the
		// leading "- "/"* " marker — an edit that drops it would make the
		// fact invisible to those scans, so re-add the marker while
		// preserving whatever the user typed.
		if !strings.HasPrefix(newLine, "- ") && !strings.HasPrefix(newLine, "* ") {
			newLine = "- " + newLine
		}
		if newLine == oldLine {
			m.status = "no change"
			return m, nil
		}
		if n := memCountOccurrences(lines, oldLine); n > 1 {
			m.status = memDuplicateLineStatus(n)
			return m, nil
		}
		file := m.focused
		return m, memQueueCmd(m.pendingMgr, file, memEditDiff(oldLine, newLine))
	default:
		var cmd tea.Cmd
		m.editInput, cmd = m.editInput.Update(msg)
		return m, cmd
	}
}

func (m memBrowserModel) updateCreating(msg tea.KeyMsg) (memBrowserModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.creating = false
		m.createInput.Blur()
		m.status = ""
		return m, nil
	case tea.KeyEnter:
		m.creating = false
		m.createInput.Blur()
		text := strings.TrimSpace(m.createInput.Value())
		if text == "" {
			m.status = "empty fact discarded"
			return m, nil
		}
		file := m.focused
		return m, memQueueCmd(m.pendingMgr, file, memNewFactDiff(text, time.Now()))
	default:
		var cmd tea.Cmd
		m.createInput, cmd = m.createInput.Update(msg)
		return m, cmd
	}
}

func (m memBrowserModel) updateConfirmDelete(msg tea.KeyMsg) (memBrowserModel, tea.Cmd) {
	switch msg.String() {
	case "y":
		m.confirmDelete = false
		lines := m.focusedLines()
		if m.confirmLineIdx < 0 || m.confirmLineIdx >= len(lines) {
			m.status = ""
			return m, nil
		}
		line := strings.TrimSpace(lines[m.confirmLineIdx])
		if n := memCountOccurrences(lines, line); n > 1 {
			m.status = memDuplicateLineStatus(n)
			return m, nil
		}
		file := m.focused
		return m, memQueueCmd(m.pendingMgr, file, memDeleteDiff(line))
	case "n", "esc":
		m.confirmDelete = false
		m.status = ""
	}
	return m, nil
}

// memSearchAll scans every cached file's lines for a case-insensitive
// substring match, capped at `cap` total hits — pure and I/O-free so it can
// run on every keystroke.
func memSearchAll(top, projects []memFile, query string, capN int) []memSearchHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var hits []memSearchHit
	add := func(files []memFile) {
		for _, f := range files {
			for i, line := range f.Lines {
				if len(hits) >= capN {
					return
				}
				if strings.Contains(strings.ToLower(line), q) {
					hits = append(hits, memSearchHit{File: f.Name, Line: i + 1, Text: strings.TrimSpace(line)})
				}
			}
		}
	}
	add(top)
	if len(hits) < capN {
		add(projects)
	}
	return hits
}

// ---- rendering ----

var (
	memDateStampRE = regexp.MustCompile(`\[\d{4}-\d{2}-\d{2}\]`)
	memUpdatedRE   = regexp.MustCompile(`\(updated[^)]*\)`)
	memDimStyle    = lipgloss.NewStyle().Foreground(ColorDim)
	memDimItalic   = lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	memWarnStyle   = lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
)

// memPad truncates (rune-safe) and pads s to exactly width visible columns,
// never calling strings.Repeat with a negative count — width<=0 returns "".
func memPad(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return fmt.Sprintf("%-*s", width, truncate(s, width))
}

// memRowCursor prefixes a row with a cursor marker and applies the shared
// selected-row look: full highlight when its pane is focused, a plain bold
// cursor when the row is selected but the OTHER pane currently has focus
// (mirrors organize.go's orgSelRow two-pane convention).
func memRowCursor(row string, selected, paneFocused bool) string {
	if !selected {
		return "  " + row
	}
	if paneFocused {
		return StyleSelectedRow.Render("▸ " + row)
	}
	return lipgloss.NewStyle().Bold(true).Render("▸ " + row)
}

// memTreeRow renders one plain file row ("<marker> <name> (<count>)"),
// truncated/padded to width, then wrapped with selection/personal styling.
// indent shifts a projects/ child two columns in. width<=0 never panics.
func memTreeRow(f memFile, selected, paneFocused, indent bool, width int) string {
	marker := "○"
	if f.Hot {
		marker = "●"
	}
	prefix := ""
	if indent {
		prefix = "  "
	}
	count := fmt.Sprintf("(%d)", f.FactCount)
	budget := width - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(marker) - 2 - utf8.RuneCountInString(count)
	if budget < 1 {
		budget = 1
	}
	row := memPad(prefix+marker+" "+truncate(f.Name, budget)+" "+count, width)
	if memory.IsPersonalFile(f.Name) {
		row = memWarnStyle.Render(row)
	}
	return memRowCursor(row, selected, paneFocused)
}

// memProjectsHeaderRow renders the expandable "projects/" node.
func memProjectsHeaderRow(open bool, n int, selected, paneFocused bool, width int) string {
	arrow := "▸"
	if open {
		arrow = "▾"
	}
	row := memPad(fmt.Sprintf("%s projects/ (%d)", arrow, n), width)
	return memRowCursor(row, selected, paneFocused)
}

// memContentRow renders one right-pane line: truncated to width BEFORE any
// styling is applied (styling embeds ANSI escapes, which truncate() must
// never see — see memTreeRow's plain-then-style ordering for the same rule),
// then date-stamp/updated-trace dimming, then the cursor wrap.
func memContentRow(line string, selected, paneFocused bool, width int) string {
	if width < 0 {
		width = 0
	}
	plain := truncate(line, width)
	styled := memUpdatedRE.ReplaceAllStringFunc(plain, func(s string) string { return memDimItalic.Render(s) })
	styled = memDateStampRE.ReplaceAllStringFunc(styled, func(s string) string { return memDimStyle.Render(s) })
	return memRowCursor(styled, selected, paneFocused)
}

func (m memBrowserModel) renderTree(width int) string {
	inner := width - 2 // memRowCursor always prepends a 2-col cursor/indent marker
	if inner < 1 {
		inner = 1
	}
	var rows []string
	for i, f := range m.top {
		rows = append(rows, memTreeRow(f, i == m.treeCursor, m.focus == memPaneTree, false, inner))
	}
	if len(m.projects) > 0 {
		headerIdx := len(m.top)
		rows = append(rows, memProjectsHeaderRow(m.projectsOpen, len(m.projects), headerIdx == m.treeCursor, m.focus == memPaneTree, inner))
		if m.projectsOpen {
			for j, f := range m.projects {
				idx := headerIdx + 1 + j
				rows = append(rows, memTreeRow(f, idx == m.treeCursor, m.focus == memPaneTree, true, inner))
			}
		}
	}
	if len(rows) == 0 {
		return memDimStyle.Render("(no memory files)")
	}
	return strings.Join(rows, "\n")
}

func (m memBrowserModel) renderContent(width int) string {
	if m.focused == "" {
		return memDimStyle.Render("select a file in the tree")
	}
	lines := m.focusedLines()
	var rows []string
	if memory.IsPersonalFile(m.focused) {
		rows = append(rows, memWarnStyle.Render("⚠ personal.md — private: not shared automatically, viewing locally is fine"), "")
	}
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	for i, line := range lines {
		rows = append(rows, memContentRow(line, i == m.contentCursor, m.focus == memPaneContent, inner))
	}
	return strings.Join(rows, "\n")
}

func (m memBrowserModel) renderSearch(width int) string {
	if len(m.searchResults) == 0 {
		return memDimStyle.Render("no matches")
	}
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	var rows []string
	for i, h := range m.searchResults {
		text := fmt.Sprintf("%s:%d  %s", h.File, h.Line, h.Text)
		rows = append(rows, memRowCursor(truncate(text, inner), i == m.searchCursor, true))
	}
	if len(m.searchResults) >= memSearchCap {
		rows = append(rows, memDimStyle.Render(fmt.Sprintf("… capped at %d matches", memSearchCap)))
	}
	return strings.Join(rows, "\n")
}

func (m memBrowserModel) View(width int) string {
	title := StyleTitle.Render("🧠 Memory")

	if m.scanning {
		return title + "\n\n" + memDimStyle.Render("scanning…")
	}
	if m.scanErr != "" {
		return title + "\n\n" + lipgloss.NewStyle().Foreground(ColorDanger).Render("⚠ "+m.scanErr)
	}
	if len(m.top) == 0 && len(m.projects) == 0 {
		return title + "\n\nNo memory files found. Run 'auxly init' first."
	}

	header := memDimStyle.Render("(no file selected)")
	if m.focused != "" {
		header = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(m.focused)
	}

	leftW := memTreeWidth
	if width > 0 && leftW > width-6 {
		leftW = width - 6
	}
	if leftW < 0 {
		leftW = 0
	}
	gutter := 3
	rightW := width - leftW - gutter
	if rightW < 0 {
		rightW = 0
	}

	left := m.renderTree(leftW)
	var right string
	if m.searching {
		right = m.renderSearch(rightW)
	} else {
		right = m.renderContent(rightW)
	}
	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gutter), right)

	var b strings.Builder
	b.WriteString(title + "\n")
	b.WriteString(header + "\n\n")
	b.WriteString(panes)
	if m.status != "" {
		color := ColorWarning
		if strings.HasPrefix(m.status, "queued for approval") {
			color = ColorSuccess
		}
		b.WriteString("\n\n" + lipgloss.NewStyle().Foreground(color).Render(m.status))
	}
	return b.String()
}
