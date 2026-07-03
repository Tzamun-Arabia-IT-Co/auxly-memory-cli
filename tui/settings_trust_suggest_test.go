package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	trustcfg "github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
)

// trustSuggestModel builds a settings model on the General sub-tab with a
// fixed set of pending suggestions, so the panel's state machine can be
// driven deterministically without a real audit.db.
func trustSuggestModel(t *testing.T) settingsModel {
	t.Helper()
	m := settingsModelFor(t, 120, 50)
	m.memoryPath = t.TempDir()
	m.trust = trustcfg.Config{Default: trustcfg.LevelRequireApproval, Providers: map[string]trustcfg.ProviderConfig{}}
	m.suggestions = []trustcfg.Suggestion{
		{Provider: "claude", Current: "require_approval", Suggested: "auto", Evidence: "62/62 approved over 90d"},
		{Provider: "cursor", Current: "require_approval", Suggested: "read_only", Evidence: "5/12 rejected over 90d"},
	}
	return m
}

// TestTrustSuggestKeyOpensAndClosesPanel guards [t]'s entry point: it only
// opens when there's something to review, and esc/t close it again.
func TestTrustSuggestKeyOpensAndClosesPanel(t *testing.T) {
	m := trustSuggestModel(t)
	m, _ = m.Update(keyRunes("t"))
	if !m.suggestOpen {
		t.Fatal("[t] with pending suggestions should open the panel")
	}
	if !strings.Contains(stripANSI(m.View()), "Trust Suggestions") {
		t.Error("view should render the suggestions panel while open")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.suggestOpen {
		t.Fatal("esc should close the panel")
	}

	// No suggestions -> [t] is a no-op (nothing to review).
	empty := trustSuggestModel(t)
	empty.suggestions = nil
	empty, _ = empty.Update(keyRunes("t"))
	if empty.suggestOpen {
		t.Fatal("[t] with zero suggestions must not open the panel")
	}
}

// TestTrustSuggestConfirmGate guards the confirm-before-apply requirement:
// [a]/enter only ARMS a confirm, it must not apply by itself, and [n]/esc
// backs out of the confirm without touching trust.Config.
func TestTrustSuggestConfirmGate(t *testing.T) {
	m := trustSuggestModel(t)
	m.suggestOpen = true

	m, cmd := m.Update(keyRunes("a"))
	if !m.suggestConfirm {
		t.Fatal("[a] should arm the confirm dialog")
	}
	if cmd != nil {
		t.Fatal("[a] must not itself dispatch the apply — that needs an explicit [y]")
	}

	// Cancel: back to the list, nothing applied.
	m, _ = m.Update(keyRunes("n"))
	if m.suggestConfirm {
		t.Fatal("[n] should cancel the confirm")
	}
	if !m.suggestOpen {
		t.Fatal("[n] should return to the list, not close the whole panel")
	}
	if len(m.trust.Providers) != 0 {
		t.Fatalf("trust.Providers = %v, want untouched after cancel", m.trust.Providers)
	}

	// Re-arm and confirm: this DOES dispatch a command, applying suggestion 0
	// (claude: require_approval -> auto).
	m, _ = m.Update(keyRunes("a"))
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.suggestApplying {
		t.Fatal("[enter] on the confirm should enter the applying state")
	}
	if cmd == nil {
		t.Fatal("[enter] on the confirm should dispatch the apply command")
	}

	// Run the real command (local trust.yaml IO in a tmpdir — no keychain/
	// network involved) and fold its result back in, the same way Update
	// would when bubbletea delivers it.
	msg := cmd()
	applied, ok := msg.(trustSuggestAppliedMsg)
	if !ok {
		t.Fatalf("command returned %T, want trustSuggestAppliedMsg", msg)
	}
	if applied.trust.Providers["claude"].TrustLevel != "auto" {
		t.Fatalf("applied trust level = %q, want auto", applied.trust.Providers["claude"].TrustLevel)
	}
	m, _ = m.Update(applied)
	if m.suggestApplying {
		t.Fatal("applying should clear once the result lands")
	}
	if !strings.Contains(m.suggestStatus, "✓") {
		t.Fatalf("suggestStatus = %q, want a success confirmation", m.suggestStatus)
	}

	// Confirm it persisted through the exact same Config.Save `auxly trust
	// set` uses — not just held in the in-memory model.
	onDisk, err := trustcfg.Load(m.memoryPath)
	if err != nil {
		t.Fatalf("reload trust.yaml: %v", err)
	}
	if onDisk.Providers["claude"].TrustLevel != "auto" {
		t.Fatalf("trust.yaml on disk: claude = %q, want auto", onDisk.Providers["claude"].TrustLevel)
	}
}
