package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

// seedClients writes a minimal clients.yaml so readClients() returns boxes.
func seedClients(t *testing.T, home string, names ...string) {
	t.Helper()
	dir := filepath.Join(home, ".auxly")
	os.MkdirAll(dir, 0o755)
	var b strings.Builder
	b.WriteString("clients:\n")
	for _, n := range names {
		b.WriteString("  - name: " + n + "\n    target: root@10.0.0.1\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSyncPanelSelectionPersists drives the sub-panel: open it, toggle the master
// switch and a box, and confirm both round-trip through settings.json.
func TestSyncPanelSelectionPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedClients(t, home, "BoxA", "BoxB")

	var m customizationsModel
	m = m.openSync()
	if len(m.syncBoxes) != 2 {
		t.Fatalf("want 2 boxes, got %d", len(m.syncBoxes))
	}

	// Row 0 is the master toggle; space turns auto-sync on and persists.
	m, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if !config.LoadSettings().SyncStatuslineToRemotes {
		t.Error("space on the master row should enable + persist auto-sync")
	}

	// Move to BoxA (row 1) and select it.
	m, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	got := config.LoadSettings().StatuslineSyncBoxes
	if len(got) != 1 || got[0] != "BoxA" {
		t.Errorf("BoxA should be the only selected box, got %v", got)
	}

	// 'a' selects all.
	m, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !m.allSelected() || len(config.LoadSettings().StatuslineSyncBoxes) != 2 {
		t.Error("'a' should select every box")
	}
	// 'a' again clears all.
	m, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.allSelected() || len(config.LoadSettings().StatuslineSyncBoxes) != 0 {
		t.Error("'a' again should clear the selection")
	}

	// 's' closes the panel.
	m, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if m.syncOpen {
		t.Error("'s' should close the sync panel")
	}
}

// TestSyncPanelRepaintsInViewport is the regression for the "cursor stuck" bug: the
// Customizations capturesInput path early-returns in the app Update, so it must still
// refresh the content viewport — otherwise navigating the sync box list advances the
// cursor in the model but the screen keeps showing a stale frame.
func TestSyncPanelRepaintsInViewport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedClients(t, home, "B1", "B2", "B3", "B4", "B5")

	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}}) // Settings
	m = u.(model)
	// Land on Customizations (← from General wraps to 2) and open the sync panel.
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = u.(model)
	if !m.settings.cust.syncOpen {
		t.Fatal("'s' should open the sync sub-panel")
	}

	before := stripANSI(m.contentVP.View())
	// Press down several times — the cursor must advance through the box rows.
	for i := 0; i < 4; i++ {
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = u.(model)
	}
	if m.settings.cust.syncCursor != 4 {
		t.Fatalf("down should advance the cursor to row 4, got %d", m.settings.cust.syncCursor)
	}
	after := stripANSI(m.contentVP.View())
	if after == before {
		t.Error("viewport did NOT repaint after navigating the sync panel — stale frame (the reported bug)")
	}
}

// TestAutoSyncCmdGating verifies the post-apply hook only fires when auto-sync is on
// AND at least one box is selected — never unexpectedly.
func TestAutoSyncCmdGating(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Off by default → no command.
	if autoSyncStatuslineCmd() != nil {
		t.Error("auto-sync must be a no-op when disabled")
	}
	// On but no boxes selected → still no command.
	config.SaveSettings(config.Settings{SyncStatuslineToRemotes: true})
	if autoSyncStatuslineCmd() != nil {
		t.Error("auto-sync must be a no-op with no boxes selected")
	}
	// On + a box selected → a command is produced.
	config.SaveSettings(config.Settings{SyncStatuslineToRemotes: true, StatuslineSyncBoxes: []string{"BoxA"}})
	if autoSyncStatuslineCmd() == nil {
		t.Error("auto-sync should fire when enabled with a selected box")
	}
}
