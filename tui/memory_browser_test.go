package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/sharing"
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

// ---- Sprint 18: recall PLAYGROUND mode ----

// TestMemScoreBar locks the score-bar math: round-to-nearest-block, clamped
// to [0,1] so a negative or >1 raw cosine can never produce a negative fill
// count (which would panic strings.Repeat).
func TestMemScoreBar(t *testing.T) {
	cases := []struct {
		score float32
		want  string
	}{
		{0.82, "▰▰▰▱"}, // round(0.82*4) = round(3.28) = 3
		{1.0, "▰▰▰▰"},
		{0.0, "▱▱▱▱"},
		{-0.4, "▱▱▱▱"}, // clamped up to 0
		{1.4, "▰▰▰▰"},  // clamped down to 1
		{0.5, "▰▰▱▱"},  // round(2.0) = 2
	}
	for _, c := range cases {
		if got := memScoreBar(c.score); got != c.want {
			t.Errorf("memScoreBar(%v) = %q, want %q", c.score, got, c.want)
		}
	}
}

// TestMemPlaygroundRowMarker locks the three-tier marker text the row
// styling keys off: accepted has none, above-floor-but-cut names k, below
// floor names the floor value.
func TestMemPlaygroundRowMarker(t *testing.T) {
	accepted := memory.RecallDebugHit{Accepted: true, AboveFloor: true}
	if got := memPlaygroundRowMarker(accepted, 8, 0.3); got != "" {
		t.Fatalf("accepted hit marker = %q, want empty", got)
	}
	cut := memory.RecallDebugHit{Accepted: false, AboveFloor: true}
	if got := memPlaygroundRowMarker(cut, 8, 0.3); got != "cut (k=8)" {
		t.Fatalf("cut hit marker = %q, want %q", got, "cut (k=8)")
	}
	below := memory.RecallDebugHit{Accepted: false, AboveFloor: false}
	if got := memPlaygroundRowMarker(below, 8, 0.3); got != "below floor 0.30" {
		t.Fatalf("below-floor hit marker = %q, want %q", got, "below floor 0.30")
	}
}

// TestMemPlaygroundRowRendering covers the row body itself (score bar, score,
// file:line, truncated text, trailing marker preserved intact) across all
// three tiers, plus the negative-width guard (spec constraint: never panic).
func TestMemPlaygroundRowRendering(t *testing.T) {
	h := memory.RecallDebugHit{File: "identity.md", LineStart: 12, Text: "likes cats a lot, more than anyone else", Score: 0.82, Accepted: true, AboveFloor: true}
	row := memPlaygroundRow(h, 8, 0.3, 40)
	if !strings.Contains(row, "▰▰▰▱ 0.82 · identity.md:12 ·") {
		t.Fatalf("accepted row missing expected prefix: %q", row)
	}
	if strings.Contains(row, "cut") || strings.Contains(row, "below floor") {
		t.Fatalf("accepted row must carry no marker: %q", row)
	}

	cut := h
	cut.Accepted = false
	cutRow := memPlaygroundRow(cut, 8, 0.3, 60)
	if !strings.HasSuffix(cutRow, "cut (k=8)") {
		t.Fatalf("cut row must end with its marker intact, got %q", cutRow)
	}

	below := h
	below.Accepted = false
	below.AboveFloor = false
	belowRow := memPlaygroundRow(below, 8, 0.3, 60)
	if !strings.HasSuffix(belowRow, "below floor 0.30") {
		t.Fatalf("below-floor row must end with its marker intact, got %q", belowRow)
	}

	for _, w := range []int{20, 1, 0, -5, -100} {
		_ = memPlaygroundRow(h, 8, 0.3, w) // must not panic
	}
}

// TestMemNextLensIndexCyclesInclEmptyClients locks the lens cycle order:
// local -> client1 -> ... -> clientN -> local, and the empty-clients case
// (nothing to cycle to, stays at local).
func TestMemNextLensIndexCyclesInclEmptyClients(t *testing.T) {
	if got := memNextLensIndex(0, 0); got != 0 {
		t.Fatalf("cycling with no clients configured must stay at local (0), got %d", got)
	}
	if got := memNextLensIndex(3, 0); got != 0 {
		t.Fatalf("cycling with no clients configured must reset to local (0), got %d", got)
	}
	lens := 0
	seq := []int{}
	for i := 0; i < 4; i++ { // 2 clients -> 3-slot cycle (local, c1, c2), walked twice
		lens = memNextLensIndex(lens, 2)
		seq = append(seq, lens)
	}
	want := []int{1, 2, 0, 1}
	for i, w := range want {
		if seq[i] != w {
			t.Fatalf("cycle sequence = %v, want %v", seq, want)
		}
	}
}

func TestMemLensLabel(t *testing.T) {
	clients := []clientRow{{Name: "box1"}, {Name: "box2"}}
	if got := memLensLabel(clients, 0); got != "local (all files)" {
		t.Fatalf("lens 0 label = %q, want local", got)
	}
	if got := memLensLabel(clients, 1); got != "box1" {
		t.Fatalf("lens 1 label = %q, want box1", got)
	}
	if got := memLensLabel(clients, 2); got != "box2" {
		t.Fatalf("lens 2 label = %q, want box2", got)
	}
	if got := memLensLabel(clients, 3); got != "local (all files)" {
		t.Fatalf("out-of-range lens label = %q, want local fallback", got)
	}
}

// TestMemPlaygroundAllowLocalLensIsUnfiltered locks lens 0's contract: nil
// allow, meaning Index.Load applies no ACL filter at all.
func TestMemPlaygroundAllowLocalLensIsUnfiltered(t *testing.T) {
	clients := []clientRow{{Name: "box1", SharedFiles: []string{"identity.md"}}}
	if allow := memPlaygroundAllow(clients, 0, []string{"identity.md", "daily.md"}); allow != nil {
		t.Fatalf("local lens must return a nil allow (unfiltered), got a closure")
	}
}

// TestMemPlaygroundAllowClientLensHonorsSharedFiles is the core "why can't
// the box see this fact" contract: a client lens's allowFn must agree
// EXACTLY with sharing.CanRead fed the same ClientShare directly — the
// playground has to use the real rule the MCP server filters that remote
// through, not an approximation of it.
func TestMemPlaygroundAllowClientLensHonorsSharedFiles(t *testing.T) {
	allFiles := []string{"identity.md", "personal.md"}
	clients := []clientRow{{Name: "box1", SharedFiles: []string{"identity.md"}}}
	allow := memPlaygroundAllow(clients, 1, allFiles)
	if allow == nil {
		t.Fatalf("client lens must return a non-nil allow closure")
	}
	share := &sharing.ClientShare{SharedFiles: []string{"identity.md"}}
	for _, f := range allFiles {
		want := sharing.CanRead(share, f, allFiles)
		if got := allow(f); got != want {
			t.Errorf("allow(%q) = %v, want %v (sharing.CanRead fed the same share directly)", f, got, want)
		}
	}
	if allow("personal.md") {
		t.Fatalf("personal.md excluded from SharedFiles must not be readable through the lens")
	}
	if !allow("identity.md") {
		t.Fatalf("identity.md included in SharedFiles must be readable through the lens")
	}
}

// TestUpdateNormalQuestionMarkEntersPlayground mirrors the "/" search-entry
// test: '?' in normal mode starts playground input and kicks the
// clients.yaml load (a non-nil tea.Cmd — every I/O this sprint's spec
// requires goes through a Cmd).
func TestUpdateNormalQuestionMarkEntersPlayground(t *testing.T) {
	m := newMemBrowserModel(nil, nil, nil)
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !m2.playground {
		t.Fatalf("'?' in normal mode should enter playground")
	}
	if cmd == nil {
		t.Fatalf("entering playground should kick off the clients.yaml load Cmd")
	}
	if m2.playgroundInput.Value() != "" {
		t.Fatalf("playground input should start empty")
	}
}

// TestQuestionMarkDuringSearchOrEditDoesNotTriggerPlayground is the spec's
// explicit precedence guard: '?' typed while another input-capturing mode
// already owns the keyboard must reach THAT mode's input, never start
// playground.
func TestQuestionMarkDuringSearchOrEditDoesNotTriggerPlayground(t *testing.T) {
	m := newMemBrowserModel(nil, nil, nil)
	m.searching = true
	m.searchInput.Focus()
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if m2.playground {
		t.Fatalf("'?' while searching must not start playground")
	}
	if !strings.Contains(m2.searchInput.Value(), "?") {
		t.Fatalf("'?' while searching should reach the search input, got %q", m2.searchInput.Value())
	}

	e := newMemBrowserModel(nil, nil, nil)
	e.editing = true
	e.editInput.Focus()
	e2, _ := e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if e2.playground {
		t.Fatalf("'?' while editing must not start playground")
	}
}

// TestUpdatePlaygroundEscRestoresNormalView locks the exit path: Esc clears
// playground and blurs its input, restoring the normal content/tree view.
func TestUpdatePlaygroundEscRestoresNormalView(t *testing.T) {
	m := newMemBrowserModel(nil, nil, nil)
	m.playground = true
	m.playgroundInput.Focus()
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m2.playground {
		t.Fatalf("esc should leave playground mode")
	}
	if cmd != nil {
		t.Fatalf("esc should not kick off any Cmd")
	}
}

// TestUpdatePlaygroundEnterRunsRecallDebugNeverRecall exercises the Enter
// path end-to-end against a nil store (deterministic, no network/embedding
// I/O): it must return a Cmd whose resulting message carries the query,
// proving the run goes through RecallDebug's message shape and not Recall's.
func TestUpdatePlaygroundEnterRunsRecallDebugNeverRecall(t *testing.T) {
	m := memBrowserModel{playground: true}
	m.playgroundInput.SetValue("cats")
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m2.playgroundRunning {
		t.Fatalf("Enter should mark a run in flight")
	}
	if cmd == nil {
		t.Fatalf("Enter with a non-empty query should return a Cmd")
	}
	msg, ok := cmd().(memPlaygroundMsg)
	if !ok {
		t.Fatalf("expected memPlaygroundMsg, got %#v", cmd())
	}
	if msg.query != "cats" {
		t.Fatalf("msg.query = %q, want %q", msg.query, "cats")
	}
	if msg.err == nil {
		t.Fatalf("nil store should surface an error, not silently succeed")
	}
}

func TestUpdatePlaygroundEnterEmptyQueryNoop(t *testing.T) {
	m := memBrowserModel{playground: true}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("Enter with an empty query must not fire a Cmd")
	}
}

// TestUpdatePlaygroundTabNoClientsShowsStatus is the spec's explicit
// no-remote-clients case: tab must not silently do nothing, it must surface
// why the lens can't move.
func TestUpdatePlaygroundTabNoClientsShowsStatus(t *testing.T) {
	m := memBrowserModel{playground: true}
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("tab with no clients configured must not fire a Cmd")
	}
	if m2.playgroundLens != 0 {
		t.Fatalf("lens must stay at local with no clients configured")
	}
	if m2.status != "no remote clients — lens is local only" {
		t.Fatalf("status = %q", m2.status)
	}
}

// TestUpdatePlaygroundTabCyclesLensAndReRunsQuery covers lens cycling with
// clients configured AND the auto-rerun-last-query contract.
func TestUpdatePlaygroundTabCyclesLensAndReRunsQuery(t *testing.T) {
	m := memBrowserModel{
		playground:        true,
		playgroundClients: []clientRow{{Name: "box1"}, {Name: "box2"}},
	}
	// No query run yet: tab moves the lens but fires no Cmd.
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m2.playgroundLens != 1 {
		t.Fatalf("lens = %d, want 1", m2.playgroundLens)
	}
	if cmd != nil {
		t.Fatalf("tab with no query run yet must not fire a Cmd")
	}

	// Simulate a completed run, then cycle: must re-fire with the SAME query.
	m2.playgroundQuery = "cats"
	m3, cmd3 := m2.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m3.playgroundLens != 2 {
		t.Fatalf("lens = %d, want 2", m3.playgroundLens)
	}
	if cmd3 == nil {
		t.Fatalf("tab with a run query must re-fire the recall Cmd")
	}
	msg, ok := cmd3().(memPlaygroundMsg)
	if !ok || msg.query != "cats" {
		t.Fatalf("re-run must carry the last RUN query, got %#v ok=%v", msg, ok)
	}

	// One more tab: 2 clients -> 3-slot cycle, back to local.
	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m4.playgroundLens != 0 {
		t.Fatalf("lens = %d, want back to local (0)", m4.playgroundLens)
	}
}

// TestMemPlaygroundMsgErrUnavailableSetsBanner locks the ErrUnavailable ->
// banner contract (spec point 5), mirroring toolRecall's fallback wording.
func TestMemPlaygroundMsgErrUnavailableSetsBanner(t *testing.T) {
	m := memBrowserModel{playground: true, playgroundRunning: true}
	wrapped := fmt.Errorf("recall unavailable: %w", embed.ErrUnavailable)
	m2, _ := m.Update(memPlaygroundMsg{query: "cats", err: wrapped})
	if m2.playgroundRunning {
		t.Fatalf("receiving the result must clear the in-flight flag")
	}
	if !errors.Is(wrapped, embed.ErrUnavailable) {
		t.Fatalf("test setup sanity: wrapped err should satisfy errors.Is ErrUnavailable")
	}
	want := "embeddings unavailable — agents get substring fallback here; check `auxly index status`"
	if m2.playgroundErrMsg != want {
		t.Fatalf("playgroundErrMsg = %q, want %q", m2.playgroundErrMsg, want)
	}
	if len(m2.playgroundHits) != 0 {
		t.Fatalf("an error result must not carry stale hits")
	}
}

// TestPlaygroundMsgDropsStaleGeneration is the lens-race regression test: a
// Tab-switch or second Enter while a run is in flight bumps playgroundGen
// before dispatching, so an overlapping run's result landing out of order
// must never overwrite the newer lens/query's state.
func TestPlaygroundMsgDropsStaleGeneration(t *testing.T) {
	m := memBrowserModel{playground: true}
	m.playgroundInput.SetValue("first")
	m1, cmd1 := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // gen -> 1, in flight
	m1.playgroundInput.SetValue("second")
	m2, cmd2 := m1.Update(tea.KeyMsg{Type: tea.KeyEnter}) // gen -> 2, in flight
	if cmd1 == nil || cmd2 == nil {
		t.Fatalf("expected both dispatches to fire a Cmd")
	}
	staleMsg, ok := cmd1().(memPlaygroundMsg)
	if !ok || staleMsg.query != "first" {
		t.Fatalf("unexpected stale msg: %#v ok=%v", staleMsg, ok)
	}
	freshMsg, ok := cmd2().(memPlaygroundMsg)
	if !ok || freshMsg.query != "second" {
		t.Fatalf("unexpected fresh msg: %#v ok=%v", freshMsg, ok)
	}

	// The gen-1 run lands AFTER the gen-2 run was already dispatched: it must
	// be dropped, not overwrite playgroundQuery/running with the old run's data.
	m3, _ := m2.Update(staleMsg)
	if m3.playgroundQuery != "" || !m3.playgroundRunning {
		t.Fatalf("stale-gen msg must be dropped, got query=%q running=%v", m3.playgroundQuery, m3.playgroundRunning)
	}

	// The gen-2 run lands: it must be applied.
	m4, _ := m3.Update(freshMsg)
	if m4.playgroundQuery != "second" || m4.playgroundRunning {
		t.Fatalf("current-gen msg must be applied, got query=%q running=%v", m4.playgroundQuery, m4.playgroundRunning)
	}
}

// TestPlaygroundTabMidFlightUsesTypedQuery covers the lens-race fix's second
// half: the user types, hits Enter, then Tab's before that run lands —
// playgroundQuery is still "" (it only updates when a msg is received), but
// the re-run under the new lens must still use the typed query, not skip it.
func TestPlaygroundTabMidFlightUsesTypedQuery(t *testing.T) {
	m := memBrowserModel{playground: true, playgroundClients: []clientRow{{Name: "box1"}}}
	m.playgroundInput.SetValue("cats")
	m1, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m1.playgroundRunning || m1.playgroundQuery != "" {
		t.Fatalf("setup: expected an in-flight run with playgroundQuery unlanded, got running=%v query=%q", m1.playgroundRunning, m1.playgroundQuery)
	}
	m2, cmd2 := m1.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m2.playgroundLens != 1 {
		t.Fatalf("lens = %d, want 1", m2.playgroundLens)
	}
	if cmd2 == nil {
		t.Fatalf("tab mid-flight with a typed query must re-fire the recall Cmd")
	}
	msg, ok := cmd2().(memPlaygroundMsg)
	if !ok || msg.query != "cats" {
		t.Fatalf("mid-flight re-run must use the typed query, got %#v ok=%v", msg, ok)
	}
}

// TestMemVaultFileNamesFreshSeesFileCreatedAfterScan is Finding 2's
// regression test: memPlaygroundCmd must build its ACL universe from a FRESH
// store.List() call made inside the Cmd, not the model's cached tab-enter
// scan snapshot — so a file created on disk after that scan is still visible
// to a client-lens run, exactly as the live MCP server (mcp/server.go's
// vaultFileNames) would show it.
func TestMemVaultFileNamesFreshSeesFileCreatedAfterScan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.md"), []byte("- x\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store := memory.NewStore(dir)
	store.WorkspaceRoot = ""

	// The model's stale tab-enter scan snapshot: only identity.md.
	staleList := []string{"identity.md"}
	clients := []clientRow{{Name: "box1"}} // empty SharedFiles -> default "share everything visible" tier

	// A file appears on disk AFTER the scan snapshot was taken (e.g. queued
	// via another session and approved while this tab sat open).
	if err := os.WriteFile(filepath.Join(dir, "daily.md"), []byte("- y\n"), 0o644); err != nil {
		t.Fatalf("add file: %v", err)
	}

	// Sanity check on the OLD bug: building the allow closure from the stale
	// cached list makes the new file invisible even though a default share
	// tier should cover it.
	staleAllow := memPlaygroundAllow(clients, 1, staleList)
	if staleAllow("daily.md") {
		t.Fatalf("test sanity: the stale list must not see the post-scan file")
	}

	// The fix: memPlaygroundCmd feeds memPlaygroundAllow a fresh
	// memVaultFileNames(store) call, so the same client-lens run now sees it.
	freshAllow := memPlaygroundAllow(clients, 1, memVaultFileNames(store))
	if !freshAllow("daily.md") {
		t.Fatalf("a fresh store.List() must make a post-scan file visible to a client-lens run, mirroring mcp/server.go's per-request vaultFileNames")
	}
}

// TestMemScanMsgDoesNotCancelPlayground is the Sprint 17 cancel-in-flight
// rule applied to playground: unlike editing/creating/confirmDelete,
// playground results are standalone copies (RecallDebugHit carries no index
// into m.top/m.projects' Lines slices), so a rescan landing mid-playground
// must leave the mode and its results untouched rather than cancelling.
func TestMemScanMsgDoesNotCancelPlayground(t *testing.T) {
	m := memBrowserModel{
		top:        []memFile{{Name: "identity.md", Lines: []string{"- likes cats"}}},
		playground: true,
		playgroundHits: []memory.RecallDebugHit{
			{File: "identity.md", Text: "likes cats", Score: 0.9, Accepted: true, AboveFloor: true},
		},
		playgroundQuery: "cats",
	}
	m2, _ := m.Update(memScanMsg{top: []memFile{{Name: "identity.md", Lines: []string{"- likes dogs"}}}})
	if !m2.playground {
		t.Fatalf("a rescan must not exit playground mode")
	}
	if len(m2.playgroundHits) != 1 {
		t.Fatalf("a rescan must not clear playground results, got %+v", m2.playgroundHits)
	}
	if strings.Contains(m2.status, "action cancelled") {
		t.Fatalf("playground must not be reported as a cancelled action, got status %q", m2.status)
	}
}

// MAJOR 11 regression: a file whose Store.View failed (e.g. encrypted, key
// unreachable) must render with an explicit "key unreachable" marker — never
// as an ordinary, empty, editable file — and edit/delete/create on it must
// be refused with a status message instead of silently acting on the
// misleadingly-empty Lines.
func TestUnreadableFile_MarkerShownAndEditDeleteCreateRefused(t *testing.T) {
	m := memBrowserModel{
		top:     []memFile{{Name: "business.md", Unreadable: true}},
		focused: "business.md",
	}

	row := memTreeRow(m.top[0], false, true, false, 40)
	if !strings.Contains(row, "key unreachable") {
		t.Fatalf("tree row missing the unreachable marker: %q", row)
	}

	content := m.renderContent(40)
	if !strings.Contains(content, "key unreachable") {
		t.Fatalf("content pane missing the unreachable message: %q", content)
	}

	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if cmd != nil || m2.editing {
		t.Fatal("'e' on an unreadable file must not start editing")
	}
	if !strings.Contains(m2.status, "key unreachable") {
		t.Fatalf("edit refusal status = %q", m2.status)
	}

	m3, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd2 != nil || m3.confirmDelete {
		t.Fatal("'d' on an unreadable file must not start a delete confirm")
	}
	if !strings.Contains(m3.status, "key unreachable") {
		t.Fatalf("delete refusal status = %q", m3.status)
	}

	m4, cmd3 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cmd3 != nil || m4.creating {
		t.Fatal("'n' on an unreadable file must not start create-new-fact")
	}
	if !strings.Contains(m4.status, "key unreachable") {
		t.Fatalf("create refusal status = %q", m4.status)
	}
}
