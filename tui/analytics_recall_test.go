package tui

import (
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
)

// TestRecallFetchAndRenderShowsSeededFile drives recallFetchCmd end-to-end
// (the way Refresh() wires it in) against a real audit.Logger seeded with an
// accepted recall hit, feeds the resulting msg through Update, and checks the
// rendered panel surfaces the seeded file plus the fallback-rate line.
func TestRecallFetchAndRenderShowsSeededFile(t *testing.T) {
	dir := t.TempDir()
	logger, err := audit.NewLogger(dir)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	if err := logger.RecordRecall(audit.RecallMeta{Provider: "codex", QueryHash: "aaaaaaaaaaaaaaaa", Fallback: false}, []audit.RecallHitRecord{
		{File: "projects.md", LineHash: "bbbbbbbbbbbbbbbb", Score: 0.92, Rank: 0, Accepted: true},
	}); err != nil {
		t.Fatalf("RecordRecall failed: %v", err)
	}

	store := memory.NewStore(dir)
	m := newAnalyticsModel(logger, nil, store)

	msg := recallFetchCmd(logger, store)()
	m, _ = m.Update(msg)

	if m.recall == nil {
		t.Fatal("recall data not populated after Update")
	}

	got := m.renderRecallPanel(120)
	if !strings.Contains(got, "projects.md") {
		t.Fatalf("renderRecallPanel output missing seeded file %q:\n%s", "projects.md", got)
	}
	if !strings.Contains(got, "Fallback rate") {
		t.Fatalf("renderRecallPanel output missing fallback-rate line:\n%s", got)
	}
}

// TestAnalyticsSubTabCycles3Way locks the ←/→ toggle contract: with three
// sub-tabs (Activity/Usage/Recall) it must wrap 0->1->2->0, not the old 2-way
// XOR toggle.
func TestAnalyticsSubTabCycles3Way(t *testing.T) {
	var m analyticsModel
	if m.activeTab != 0 {
		t.Fatalf("initial activeTab = %d, want 0", m.activeTab)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.activeTab != 1 {
		t.Fatalf("after 1 right: activeTab = %d, want 1", m.activeTab)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.activeTab != 2 {
		t.Fatalf("after 2 right: activeTab = %d, want 2", m.activeTab)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.activeTab != 0 {
		t.Fatalf("after 3 right: activeTab = %d, want 0 (wrapped)", m.activeTab)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.activeTab != 2 {
		t.Fatalf("after 1 left from 0: activeTab = %d, want 2 (wrapped)", m.activeTab)
	}
}
