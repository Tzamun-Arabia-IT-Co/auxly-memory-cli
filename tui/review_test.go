package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
)

var errBoom = errors.New("boom")

func TestReviewAge(t *testing.T) {
	if got := reviewAge(time.Time{}); got != "undated" {
		t.Fatalf("zero time: got %q, want %q", got, "undated")
	}
	// Finding 11: the Age column reuses humanizeAgo's vocabulary ("Xd ago"),
	// same as Last Recall, instead of a bespoke "142d".
	got := reviewAge(time.Now().Add(-142 * 24 * time.Hour))
	if got != "142d ago" {
		t.Fatalf("142 days ago: got %q, want %q", got, "142d ago")
	}
}

func TestReviewLastRecall(t *testing.T) {
	if got := reviewLastRecall(time.Time{}); got != "never" {
		t.Fatalf("zero time: got %q, want %q", got, "never")
	}
	got := reviewLastRecall(time.Now().Add(-12 * 24 * time.Hour))
	if got != "12d ago" {
		t.Fatalf("12 days ago: got %q, want %q", got, "12d ago")
	}
}

func TestReviewRowTruncatesToWidth(t *testing.T) {
	f := memory.StaleFact{
		File: "a-very-long-category-filename.md",
		Line: "- This is a fact line that is much longer than the column budget allows for display",
	}
	row := reviewRow(f, 20)
	cols := strings.SplitN(row, " ", 2)
	if n := utf8.RuneCountInString(cols[0]); n != reviewFileW {
		t.Fatalf("file column width: got %d runes, want %d (row: %q)", n, reviewFileW, row)
	}
	if !strings.Contains(cols[0], "…") {
		t.Fatalf("expected the long filename to be truncated with an ellipsis: %q", cols[0])
	}
	if !strings.Contains(row, "…") {
		t.Fatalf("expected the long fact text to be truncated with an ellipsis: %q", row)
	}
	// The leading "- " bullet marker is stripped before display.
	if strings.Contains(row, "- This is a fact") {
		t.Fatalf("expected bullet prefix to be stripped: %q", row)
	}
}

func TestReviewRowShortFactNotTruncated(t *testing.T) {
	f := memory.StaleFact{File: "identity.md", Line: "- short fact"}
	row := reviewRow(f, 46)
	if strings.Contains(row, "…") {
		t.Fatalf("short fact should not be truncated: %q", row)
	}
	if !strings.Contains(row, "undated") || !strings.Contains(row, "never") {
		t.Fatalf("zero-value dates should render as undated/never: %q", row)
	}
}

func TestRemoveStaleFact(t *testing.T) {
	facts := []memory.StaleFact{
		{File: "a.md", Line: "- one"},
		{File: "b.md", Line: "- two"},
		{File: "c.md", Line: "- three"},
	}
	out := removeStaleFact(facts, "b.md", "- two")
	if len(out) != 2 {
		t.Fatalf("expected 2 facts left, got %d: %+v", len(out), out)
	}
	for _, f := range out {
		if f.File == "b.md" {
			t.Fatalf("b.md should have been removed: %+v", out)
		}
	}
	if out[0].File != "a.md" || out[1].File != "c.md" {
		t.Fatalf("unexpected order/content: %+v", out)
	}

	// No match: slice returned unchanged (by content).
	same := removeStaleFact(facts, "missing.md", "- nope")
	if len(same) != len(facts) {
		t.Fatalf("no-match removal should keep all facts, got %d", len(same))
	}

	// Only the FIRST exact file+line match is removed.
	dup := []memory.StaleFact{
		{File: "a.md", Line: "- dup"},
		{File: "a.md", Line: "- dup"},
	}
	out2 := removeStaleFact(dup, "a.md", "- dup")
	if len(out2) != 1 {
		t.Fatalf("expected exactly one duplicate removed, got %d", len(out2))
	}
}

func TestReviewActionMsgTransition_Success(t *testing.T) {
	m := reviewModel{
		facts: []memory.StaleFact{
			{File: "a.md", Line: "- one"},
			{File: "b.md", Line: "- two"},
		},
		cursor: 1,
	}
	m, cmd := m.Update(reviewActionMsg{kind: "restamp", file: "b.md", line: "- two"})
	if cmd != nil {
		t.Fatalf("successful action should not return a follow-up command")
	}
	if len(m.facts) != 1 || m.facts[0].File != "a.md" {
		t.Fatalf("expected the acted-on row removed, got %+v", m.facts)
	}
	if m.status != "re-stamped" {
		t.Fatalf("status: got %q, want %q", m.status, "re-stamped")
	}
	if m.cursor != 0 {
		t.Fatalf("cursor should clamp into range, got %d", m.cursor)
	}
}

func TestReviewActionMsgTransition_Error(t *testing.T) {
	m := reviewModel{
		facts:  []memory.StaleFact{{File: "a.md", Line: "- one"}},
		cursor: 0,
	}
	m, _ = m.Update(reviewActionMsg{kind: "archive", file: "a.md", line: "- one", err: errBoom})
	if len(m.facts) != 1 {
		t.Fatalf("row must stay on error: %+v", m.facts)
	}
	if !strings.Contains(m.status, "boom") {
		t.Fatalf("status should surface the error: %q", m.status)
	}
}

func TestReviewCursorKeepsClearOfMoveKeys(t *testing.T) {
	m := reviewModel{
		facts: []memory.StaleFact{
			{File: "a.md", Line: "- one"},
			{File: "b.md", Line: "- two"},
			{File: "c.md", Line: "- three"},
		},
	}
	jKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	m, _ = m.Update(jKey)
	if m.cursor != 1 {
		t.Fatalf("j should move down: got cursor %d", m.cursor)
	}
	m, _ = m.Update(jKey)
	if m.cursor != 2 {
		t.Fatalf("j should move down: got cursor %d", m.cursor)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.cursor != 1 {
		t.Fatalf("up should move up: got cursor %d", m.cursor)
	}
}

// TestReviewLowercaseKMovesCursor is Finding 6's regression: lowercase "k"
// must match the app-wide vim convention (up), not silently trigger the
// destructive re-stamp — a mis-key must never rewrite a fact.
func TestReviewLowercaseKMovesCursor(t *testing.T) {
	m := reviewModel{
		facts: []memory.StaleFact{
			{File: "a.md", Line: "- one"},
			{File: "b.md", Line: "- two"},
		},
		cursor: 1,
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if cmd != nil {
		t.Fatalf("lowercase k must not return a command (no restamp), got %v", cmd)
	}
	if m.cursor != 0 {
		t.Fatalf("lowercase k should move the cursor up: got %d", m.cursor)
	}
}

// TestReviewUppercaseKTriggersKeep is the other half of Finding 6: the
// keep/re-stamp action moves to uppercase "K".
func TestReviewUppercaseKTriggersKeep(t *testing.T) {
	m := reviewModel{
		facts:  []memory.StaleFact{{File: "a.md", Line: "- one"}},
		cursor: 0,
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	if cmd == nil {
		t.Fatalf("uppercase K should trigger the keep/re-stamp command")
	}
}

// TestReviewFactsMsgClearsStaleStatus is Finding 10's regression: a fresh
// scan must clear a leftover "archived"/"re-stamped" banner from a previous
// action — otherwise it hangs over facts it no longer describes.
func TestReviewFactsMsgClearsStaleStatus(t *testing.T) {
	m := reviewModel{status: "archived → .archive/ (never deleted)"}
	m, _ = m.Update(reviewFactsMsg{facts: []memory.StaleFact{{File: "a.md", Line: "- one"}}})
	if m.status != "" {
		t.Fatalf("fresh scan should clear the stale status banner, got %q", m.status)
	}
}

// TestReviewEmptyStateMatchesCLI is Finding 11's regression: the TUI and CLI
// empty-state copy must be byte-for-byte identical.
func TestReviewEmptyStateMatchesCLI(t *testing.T) {
	m := reviewModel{}
	view := m.View()
	want := "✅ Nothing stale — every fact is either fresh or recently recalled."
	if !strings.Contains(view, want) {
		t.Fatalf("TUI empty state should match the CLI's exact copy; got %q", view)
	}
}

// TestTruncateRuneSafe is Finding 7's regression: truncate (analytics.go) must
// cut on rune boundaries, not bytes — a byte-based cut can split a multi-byte
// UTF-8 character and render garbage.
func TestTruncateRuneSafe(t *testing.T) {
	s := "日本語のファイル名"
	got := truncate(s, 5)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("truncate(%q, 5) rune count = %d, want 5 (4 runes + ellipsis): %q", s, n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
	// Short strings pass through untouched.
	if got := truncate("hi", 5); got != "hi" {
		t.Fatalf("short string should not be truncated, got %q", got)
	}
}
