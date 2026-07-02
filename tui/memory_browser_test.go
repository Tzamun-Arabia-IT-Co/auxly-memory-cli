package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	tea "github.com/charmbracelet/bubbletea"
)

// TestOrderMemScanTaxonomyOrderAndProjectsNesting locks the tree-building
// contract: top-level files follow the canonical taxonomy order (not List's
// alphabetical order), non-taxonomy files (agent config, the aggregate) are
// excluded, and projects/<slug>.md children are split out name-sorted.
func TestOrderMemScanTaxonomyOrderAndProjectsNesting(t *testing.T) {
	files := []memory.FileInfo{
		{Name: "daily.md"},
		{Name: "identity.md"},
		{Name: "projects/zeta.md"},
		{Name: "CLAUDE.md"}, // not in the taxonomy — must be excluded entirely
		{Name: "personal.md"},
		{Name: "projects/alpha.md"},
		{Name: "preferences.md"},
	}
	top, projects := orderMemScan(files)

	var topNames []string
	for _, f := range top {
		topNames = append(topNames, f.Name)
	}
	want := "identity.md,personal.md,preferences.md,daily.md"
	if got := strings.Join(topNames, ","); got != want {
		t.Fatalf("top order = %q, want taxonomy order %q", got, want)
	}
	for _, n := range topNames {
		if n == "CLAUDE.md" {
			t.Fatalf("non-taxonomy file must not appear in the top-level tree: %v", topNames)
		}
	}
	if len(projects) != 2 || projects[0].Name != "projects/alpha.md" || projects[1].Name != "projects/zeta.md" {
		t.Fatalf("projects children not nested/name-sorted correctly: %+v", projects)
	}
}

// TestCountBullets checks the same bullet convention ("- "/"* ") used
// throughout the vault (StaleFacts, pending diffs) — headers, blanks, and a
// dash with no trailing space don't count.
func TestCountBullets(t *testing.T) {
	lines := []string{
		"# Header",
		"- fact one",
		"  * nested fact",
		"",
		"not a bullet",
		"-no space after dash",
	}
	if got := countBullets(lines); got != 2 {
		t.Fatalf("countBullets = %d, want 2", got)
	}
}

// TestHotFileSet locks the hot/cold classification: >=1 recall hit in the
// last 30 days is hot, 0 is cold — computed once per scan from
// RecallStatsByFile.
func TestHotFileSet(t *testing.T) {
	stats := []audit.RecallFileStats{
		{File: "identity.md", Hits30: 3},
		{File: "personal.md", Hits30: 0},
		{File: "daily.md", Hits30: 1},
	}
	hot := hotFileSet(stats)
	if !hot["identity.md"] || !hot["daily.md"] {
		t.Fatalf("expected identity.md and daily.md to be hot: %+v", hot)
	}
	if hot["personal.md"] {
		t.Fatalf("0 hits in 30d must be cold: %+v", hot)
	}
}

// TestTreeRowFileSkipsProjectsHeader locks the flattened tree-row addressing
// that both cursor movement and rendering rely on: the header row isn't a
// file, and collapsing hides its children from both the count and lookup.
func TestTreeRowFileSkipsProjectsHeader(t *testing.T) {
	m := memBrowserModel{
		top:          []memFile{{Name: "identity.md"}},
		projects:     []memFile{{Name: "projects/a.md"}, {Name: "projects/b.md"}},
		projectsOpen: true,
	}
	if got := m.treeRowCount(); got != 4 { // 1 top + 1 header + 2 children
		t.Fatalf("treeRowCount = %d, want 4", got)
	}
	if !m.treeRowIsProjectsHeader(1) {
		t.Fatalf("row 1 should be the projects header")
	}
	if _, ok := m.treeRowFile(1); ok {
		t.Fatalf("the projects header row must not resolve to a file")
	}
	f, ok := m.treeRowFile(2)
	if !ok || f.Name != "projects/a.md" {
		t.Fatalf("row 2 should be the first project child, got %+v ok=%v", f, ok)
	}

	m.projectsOpen = false
	if got := m.treeRowCount(); got != 2 { // children hidden while collapsed
		t.Fatalf("collapsed treeRowCount = %d, want 2", got)
	}
}

// TestMemSearchAllCaseInsensitive and TestMemSearchAllCap cover the live
// search's filtering contract: substring, case-insensitive, capped, and
// purely in-memory (no Store passed in at all).
func TestMemSearchAllCaseInsensitive(t *testing.T) {
	top := []memFile{{Name: "a.md", Lines: []string{"- I like CATS", "- something else"}}}
	hits := memSearchAll(top, nil, "cats", memSearchCap)
	if len(hits) != 1 || hits[0].File != "a.md" || hits[0].Line != 1 {
		t.Fatalf("expected one case-insensitive hit, got %+v", hits)
	}
}

func TestMemSearchAllCap(t *testing.T) {
	lines := make([]string, 250)
	for i := range lines {
		lines[i] = "- needle here"
	}
	big := []memFile{{Name: "big.md", Lines: lines}}
	hits := memSearchAll(big, nil, "needle", memSearchCap)
	if len(hits) != memSearchCap {
		t.Fatalf("expected exactly %d capped hits, got %d", memSearchCap, len(hits))
	}
}

func TestMemSearchAllEmptyQuery(t *testing.T) {
	top := []memFile{{Name: "a.md", Lines: []string{"- x"}}}
	if hits := memSearchAll(top, nil, "   ", memSearchCap); hits != nil {
		t.Fatalf("blank query should return no hits, got %+v", hits)
	}
}

// TestMemEditDiff/TestMemDeleteDiff/TestMemNewFactDiff lock the exact diff
// bodies WriteFrom queues — bullets already carry their own "- " marker, so
// the diff lines read "--"/"+-" (the diff's own -/+ plus the bullet's dash).
func TestMemEditDiff(t *testing.T) {
	got := memEditDiff("- I like cats", "- I like dogs")
	want := "-- I like cats\n+- I like dogs\n"
	if got != want {
		t.Fatalf("memEditDiff = %q, want %q", got, want)
	}
}

func TestMemDeleteDiff(t *testing.T) {
	got := memDeleteDiff("- I like cats")
	want := "-- I like cats\n"
	if got != want {
		t.Fatalf("memDeleteDiff = %q, want %q", got, want)
	}
}

func TestMemNewFactDiffCarriesTodaysStamp(t *testing.T) {
	today := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	got := memNewFactDiff("likes tea", today)
	want := "+- likes tea [2026-07-02]\n"
	if got != want {
		t.Fatalf("memNewFactDiff = %q, want %q", got, want)
	}
}

// TestUpdateNormalNonBulletGuardsEditAndDelete is the spec's explicit guard:
// e/d on a header or blank line must never start an edit/delete, just surface
// the hint status.
func TestUpdateNormalNonBulletGuardsEditAndDelete(t *testing.T) {
	m := memBrowserModel{
		top:     []memFile{{Name: "identity.md", Lines: []string{"## Header", "- a fact"}}},
		focused: "identity.md",
	}
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if cmd != nil || m2.editing {
		t.Fatalf("'e' on a non-bullet line must not start editing")
	}
	if m2.status != "only bullet facts are editable" {
		t.Fatalf("status = %q", m2.status)
	}

	m3, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd2 != nil || m3.confirmDelete {
		t.Fatalf("'d' on a non-bullet line must not start a delete confirm")
	}
	if m3.status != "only bullet facts are editable" {
		t.Fatalf("status = %q", m3.status)
	}
}

func TestUpdateNormalEditSeedsBulletLine(t *testing.T) {
	m := newMemBrowserModel(nil, nil, nil)
	m.top = []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}}
	m.focused = "identity.md"
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if !m2.editing {
		t.Fatalf("expected editing to start on a bullet line")
	}
	if m2.editInput.Value() != "- likes cats" {
		t.Fatalf("edit input seed = %q, want %q", m2.editInput.Value(), "- likes cats")
	}
}

// TestUpdateEditingEnterQueuesDiff exercises the full edit flow end-to-end
// against a real pending.Manager (temp dir): 'e' seeds, the input changes,
// Enter queues — and the queued entry's diff carries the expected lines.
func TestUpdateEditingEnterQueuesDiff(t *testing.T) {
	dir := t.TempDir()
	m := newMemBrowserModel(nil, nil, pending.NewManager(dir))
	m.top = []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}}
	m.focused = "identity.md"
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m2.editInput.SetValue("- likes dogs")
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("Enter while editing should queue the diff")
	}
	am, ok := cmd().(memActionMsg)
	if !ok || am.err != nil {
		t.Fatalf("expected a successful memActionMsg, got %#v", am)
	}
	entries, err := m.pendingMgr.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one pending entry, got %v err=%v", entries, err)
	}
	diff, err := m.pendingMgr.ViewDiff(entries[0].Name)
	if err != nil {
		t.Fatalf("ViewDiff: %v", err)
	}
	if !strings.Contains(diff, "-- likes cats") || !strings.Contains(diff, "+- likes dogs") {
		t.Fatalf("pending diff missing expected edit lines: %q", diff)
	}
}

// TestUpdateEditingStripsMarkerReAddsBulletDash is Finding 4's regression
// test: an edit that drops the leading "- "/"* " marker would make the fact
// invisible to bullet-based tooling (decay/recall hashes), so the marker is
// re-added automatically while preserving the user's text.
func TestUpdateEditingStripsMarkerReAddsBulletDash(t *testing.T) {
	dir := t.TempDir()
	m := newMemBrowserModel(nil, nil, pending.NewManager(dir))
	m.top = []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}}
	m.focused = "identity.md"
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m2.editInput.SetValue("no marker")
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("Enter while editing should queue the diff")
	}
	am, ok := cmd().(memActionMsg)
	if !ok || am.err != nil {
		t.Fatalf("expected a successful memActionMsg, got %#v", am)
	}
	entries, _ := m.pendingMgr.List()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one pending entry, got %d", len(entries))
	}
	diff, _ := m.pendingMgr.ViewDiff(entries[0].Name)
	if !strings.Contains(diff, "+- no marker") {
		t.Fatalf("pending diff missing re-added bullet marker: %q", diff)
	}
}

// TestEditAndCreateInputsTrackTypedText is Finding 3's regression test: the
// footer interpolates editInput/createInput.Value() live (see app.go
// renderFooter's screenMemory case, mirroring the search case) — this locks
// the underlying accessor so the footer wiring can't silently go stale.
func TestEditAndCreateInputsTrackTypedText(t *testing.T) {
	m := newMemBrowserModel(nil, nil, nil)
	m.top = []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}}
	m.focused = "identity.md"

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	if got := m3.editInput.Value(); got != "- likes cats!" {
		t.Fatalf("editInput.Value() = %q, want typed text reflected (footer reads this accessor)", got)
	}

	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m5, _ := m4.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m6, _ := m5.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m7, _ := m6.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m8, _ := m7.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if got := m8.createInput.Value(); got != "tea" {
		t.Fatalf("createInput.Value() = %q, want typed text reflected (footer reads this accessor)", got)
	}
}

func TestUpdateConfirmDeleteYesQueuesDiff(t *testing.T) {
	dir := t.TempDir()
	m := memBrowserModel{
		top:        []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}},
		focused:    "identity.md",
		pendingMgr: pending.NewManager(dir),
	}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !m2.confirmDelete || m2.status != "delete? y/n" {
		t.Fatalf("expected a delete confirm prompt, got confirmDelete=%v status=%q", m2.confirmDelete, m2.status)
	}
	m3, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m3.confirmDelete {
		t.Fatalf("confirmDelete should clear after y")
	}
	if cmd == nil {
		t.Fatalf("'y' should queue the delete diff")
	}
	am, ok := cmd().(memActionMsg)
	if !ok || am.err != nil {
		t.Fatalf("expected a successful memActionMsg, got %#v", am)
	}
	// Finding 5: don't stop at the memActionMsg — assert the materialized
	// pending entry itself (mirrors TestUpdateEditingEnterQueuesDiff).
	entries, err := m3.pendingMgr.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one pending entry, got %v err=%v", entries, err)
	}
	diff, err := m3.pendingMgr.ViewDiff(entries[0].Name)
	if err != nil {
		t.Fatalf("ViewDiff: %v", err)
	}
	if !strings.Contains(diff, "-- likes cats") {
		t.Fatalf("pending diff missing expected delete line: %q", diff)
	}
}

// TestUpdateConfirmDeleteDuplicateLineRefusesToQueue is Finding 1's
// regression test: pending.ApplyDiff removes EVERY line whose trimmed text
// matches the diff's "-" target, so a delete built from a non-unique line
// would silently take out every copy on approval. The browser must refuse to
// queue and surface why.
func TestUpdateConfirmDeleteDuplicateLineRefusesToQueue(t *testing.T) {
	dir := t.TempDir()
	m := memBrowserModel{
		top:        []memFile{{Name: "identity.md", Lines: []string{"- likes cats", "- likes cats"}}},
		focused:    "identity.md",
		pendingMgr: pending.NewManager(dir),
	}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !m2.confirmDelete {
		t.Fatalf("expected delete confirm prompt")
	}
	m3, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil {
		t.Fatalf("a duplicated line must not queue a diff")
	}
	if m3.confirmDelete {
		t.Fatalf("confirmDelete should still clear after y")
	}
	if !strings.Contains(m3.status, "appears 2×") {
		t.Fatalf("status = %q, want it to carry 'appears 2×'", m3.status)
	}
	entries, err := m3.pendingMgr.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected no pending entries queued, got %v err=%v", entries, err)
	}
}

// TestMemScanMsgCancelsInFlightConfirmDelete is Finding 2's regression test:
// an async rescan swaps the cached Lines slices under a stale
// confirmLineIdx/editLineIdx, so a scan landing mid-confirm must cancel the
// action rather than let a later 'y'/Enter queue against a line the user
// never saw.
func TestMemScanMsgCancelsInFlightConfirmDelete(t *testing.T) {
	dir := t.TempDir()
	m := memBrowserModel{
		top:        []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}},
		focused:    "identity.md",
		pendingMgr: pending.NewManager(dir),
	}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !m2.confirmDelete {
		t.Fatalf("expected delete confirm prompt")
	}
	m3, _ := m2.Update(memScanMsg{top: []memFile{{Name: "identity.md", Lines: []string{"- likes dogs"}}}})
	if m3.confirmDelete {
		t.Fatalf("memScanMsg must cancel an in-flight confirm-delete")
	}
	if !strings.Contains(m3.status, "vault rescanned") {
		t.Fatalf("status = %q, want the rescan-cancelled message", m3.status)
	}
	m4, cmd := m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil {
		t.Fatalf("'y' after a cancelling rescan must not queue anything")
	}
	entries, err := m4.pendingMgr.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected no pending entries, got %v err=%v", entries, err)
	}
}

func TestUpdateConfirmDeleteNoCancels(t *testing.T) {
	m := memBrowserModel{
		top:     []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}},
		focused: "identity.md",
	}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m3, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m3.confirmDelete || m3.status != "" || cmd != nil {
		t.Fatalf("'n' should cleanly cancel the delete confirm")
	}
}

func TestUpdateCreatingEnterQueuesDatedFact(t *testing.T) {
	dir := t.TempDir()
	m := newMemBrowserModel(nil, nil, pending.NewManager(dir))
	m.top = []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}}
	m.focused = "identity.md"
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if !m2.creating {
		t.Fatalf("'n' should enter new-fact input mode")
	}
	m2.createInput.SetValue("likes tea")
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("Enter while creating should queue the new fact")
	}
	am, ok := cmd().(memActionMsg)
	if !ok || am.err != nil {
		t.Fatalf("expected a successful memActionMsg, got %#v", am)
	}
	entries, _ := m.pendingMgr.List()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one pending entry, got %d", len(entries))
	}
	diff, _ := m.pendingMgr.ViewDiff(entries[0].Name)
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(diff, "+- likes tea ["+today+"]") {
		t.Fatalf("pending diff missing today's date stamp: %q", diff)
	}
}

func TestUpdateNewFactRequiresFocusedFile(t *testing.T) {
	m := memBrowserModel{}
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cmd != nil || m2.creating {
		t.Fatalf("'n' with no focused file must not start creating")
	}
	if m2.status != "select a file first" {
		t.Fatalf("status = %q", m2.status)
	}
}

// TestUpdateSearchingEnterJumpsAndExpandsProjects covers the search-result
// jump: it selects the file, positions the content cursor on the matched
// line, and expands the projects/ node if the hit lives inside it.
func TestUpdateSearchingEnterJumpsAndExpandsProjects(t *testing.T) {
	m := memBrowserModel{
		top:           []memFile{{Name: "identity.md", Lines: []string{"- a"}}},
		projects:      []memFile{{Name: "projects/zzz.md", Lines: []string{"- x", "- needle here"}}},
		searching:     true,
		searchResults: []memSearchHit{{File: "projects/zzz.md", Line: 2, Text: "- needle here"}},
		searchCursor:  0,
	}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m2.searching {
		t.Fatalf("Enter should exit search mode")
	}
	if m2.focused != "projects/zzz.md" || m2.contentCursor != 1 {
		t.Fatalf("expected jump to projects/zzz.md line 2 (idx 1), got focused=%q contentCursor=%d", m2.focused, m2.contentCursor)
	}
	if !m2.projectsOpen {
		t.Fatalf("jumping to a project child should expand the projects/ node")
	}
	if m2.focus != memPaneContent {
		t.Fatalf("expected focus to move to the content pane")
	}
}

func TestCapturesInput(t *testing.T) {
	var m memBrowserModel
	if m.capturesInput() {
		t.Fatalf("idle model must not capture input")
	}
	m.searching = true
	if !m.capturesInput() {
		t.Fatalf("searching must capture input")
	}
}

// TestRowRenderersGuardNegativeWidth is the spec's explicit panic guard: the
// row renderers must survive width 20 down through 0 and negative without
// panicking (a negative-count strings.Repeat would).
func TestRowRenderersGuardNegativeWidth(t *testing.T) {
	f := memFile{Name: "a-very-long-taxonomy-filename.md", FactCount: 12, Hot: true}
	for _, w := range []int{20, 1, 0, -5, -100} {
		_ = memTreeRow(f, true, true, false, w)
		_ = memTreeRow(f, false, false, true, w)
		_ = memProjectsHeaderRow(true, 3, false, false, w)
		_ = memContentRow("- a fact [2026-01-01] (updated 2026-01-02; was: old)", true, true, w)
		_ = memPad("hello world", w)
	}
}

func TestMemTreeRowFitsWidthBudget(t *testing.T) {
	f := memFile{Name: "a-very-long-taxonomy-filename-that-overflows.md", FactCount: 12}
	row := memTreeRow(f, false, false, false, 20)
	plain := strings.TrimPrefix(row, "  ")
	if n := utf8.RuneCountInString(plain); n != 20 {
		t.Fatalf("row body should be padded/truncated to exactly 20 runes, got %d: %q", n, row)
	}
}
