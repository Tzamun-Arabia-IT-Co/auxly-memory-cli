package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRemoteScreenAutoRefreshes locks the live-refresh fix: entering the Remote
// screen seeds a periodic data tick so inbound connections appear without an input
// event, and the tick re-arms on every fire — but it only re-reads data in the
// passive list mode, never disrupting an in-progress sub-mode (e.g. the connect
// wizard the user is typing into).
func TestRemoteScreenAutoRefreshes(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)

	// Enter the Remote tab via the real key path; entry must return a command so the
	// data tick is seeded alongside the one-shot refresh.
	u, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	m = u.(model)
	if m.screen != screenSSH {
		t.Fatalf("'8' should open the Remote screen, got %v", m.screen)
	}
	if cmd == nil {
		t.Fatal("entering Remote must seed a command (refresh + data tick batch)")
	}

	// A tick in the passive list mode re-arms the loop and stays in list mode.
	m.ssh.mode = sshModeList
	listModel, listCmd := m.ssh.Update(sshDataTickMsg{})
	if listCmd == nil {
		t.Error("sshDataTickMsg must re-arm the tick (non-nil cmd) so polling continues")
	}
	if listModel.mode != sshModeList {
		t.Errorf("list-mode tick must stay in list mode, got %q", listModel.mode)
	}

	// A tick mid-interaction (the connect wizard) must re-arm WITHOUT disrupting the
	// sub-mode — this is what keeps the background poll from yanking the user out of
	// a form they're typing into.
	m.ssh.mode = sshModeForm
	formModel, formCmd := m.ssh.Update(sshDataTickMsg{})
	if formCmd == nil {
		t.Error("the tick must re-arm even while in a sub-mode")
	}
	if formModel.mode != sshModeForm {
		t.Errorf("the tick must not interrupt the wizard (mode changed to %q)", formModel.mode)
	}
}
