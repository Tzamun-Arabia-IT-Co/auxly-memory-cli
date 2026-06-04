package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	tea "github.com/charmbracelet/bubbletea"
)

// TestCustomizationsStatuslineFlow drives the Customizations sub-tab against a temp
// HOME so it never touches the real ~/.claude/settings.json: it pre-selects the
// wrap default when the user has their own statusline, confirms before applying,
// and the in-process apply installs + reverses through the same code the CLI uses.
func TestCustomizationsStatuslineFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	orig := "bash /custom/statusline.sh"
	seed, _ := json.MarshalIndent(map[string]any{
		"statusLine": map[string]any{"type": "command", "command": orig},
	}, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), seed, 0o644)

	var m customizationsModel
	m.refresh()
	// The option cursor always starts on ① (replace), regardless of current mode, so
	// the selection is predictable as you switch agents.
	if m.optionIdx != 0 {
		t.Errorf("option cursor should always default to ① (0), got %d", m.optionIdx)
	}

	// The panel shows the Claude-Code-only banner and the three options.
	view := stripANSI(m.panel())
	for _, w := range []string{"Cursor CLI", "Use the Auxly statusline", "Add Auxly to my current", "None"} {
		if !strings.Contains(view, w) {
			t.Errorf("customizations panel missing %q", w)
		}
	}

	// Move to option ① (replace) and press enter → confirm dialog, no write yet.
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.confirming || !m.capturesInput() {
		t.Fatal("enter on a replace/wrap option must open the confirm dialog and capture input")
	}
	if statusline.CurrentState(statusline.ProviderClaude).Mode != statusline.ModeOther {
		t.Error("opening the confirm must NOT write settings yet")
	}

	// Confirm with 'y' → enters the in-progress state and dispatches a command;
	// the write has NOT happened yet (it runs in the returned command).
	var cmd tea.Cmd
	m, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.applying || !m.capturesInput() {
		t.Fatal("confirming should enter the in-progress (applying) state and capture input")
	}
	if cmd == nil {
		t.Fatal("confirming should dispatch an apply command")
	}
	if statusline.CurrentState(statusline.ProviderClaude).Mode != statusline.ModeOther {
		t.Error("the write must NOT happen until the apply command runs")
	}
	if v := stripANSI(m.panel()); !strings.Contains(v, "Applying") {
		t.Error("the applying state should render an in-progress panel")
	}

	// Run the command → it returns the result, which folds the write in.
	applied, ok := cmd().(statuslineAppliedMsg)
	if !ok {
		t.Fatalf("apply command should return a statuslineAppliedMsg")
	}
	m = m.handleApplied(applied)
	if m.applying {
		t.Error("the applied result should clear the in-progress state")
	}
	if statusline.CurrentState(statusline.ProviderClaude).Mode != statusline.ModeFull {
		t.Errorf("the apply should install the full statusline; mode=%s", statusline.CurrentState(statusline.ProviderClaude).Mode)
	}

	// Select ③ None → applies (no confirm) but still routes through the in-progress
	// command before restoring the original.
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.applying || cmd == nil {
		t.Fatal("None should also enter the in-progress state and dispatch a command")
	}
	applied, ok = cmd().(statuslineAppliedMsg)
	if !ok {
		t.Fatalf("None apply command should return a statuslineAppliedMsg")
	}
	m = m.handleApplied(applied)
	if statusline.CurrentState(statusline.ProviderClaude).Command != orig {
		t.Errorf("None should restore the original statusline; got %q", statusline.CurrentState(statusline.ProviderClaude).Command)
	}
}

// TestCustomizationsAgentSwitcher verifies the per-agent switcher: three agents are
// listed, `a` cycles the focus, the panel reflects the focused agent (label + CLI
// flag), and the stale Claude-only banner is gone.
func TestCustomizationsAgentSwitcher(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".cursor"), 0o755) // make Cursor "available"

	var m customizationsModel
	m.refresh()
	if len(m.agents) != 3 {
		t.Fatalf("want 3 statusline agents, got %d", len(m.agents))
	}
	if m.focusedAgent().Name != statusline.ProviderClaude {
		t.Errorf("default focus should be claude, got %q", m.focusedAgent().Name)
	}

	// 'a' cycles the focused agent to Cursor.
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.focusedAgent().Name != statusline.ProviderCursor {
		t.Fatalf("after 'a', focus should be cursor, got %q", m.focusedAgent().Name)
	}
	view := stripANSI(m.panel())
	if !strings.Contains(view, "Cursor CLI") || !strings.Contains(view, "--agent cursor") {
		t.Errorf("cursor panel should show its label + CLI flag:\n%s", view)
	}
	if strings.Contains(view, "Claude Code only") {
		t.Error("stale 'Claude Code only' banner is still present")
	}

	// 'A' cycles back to Claude.
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if m.focusedAgent().Name != statusline.ProviderClaude {
		t.Errorf("after 'A', focus should wrap back to claude, got %q", m.focusedAgent().Name)
	}
}

// TestSettingsReachesCustomizationsTab verifies the sub-tab is reachable from the
// Settings screen via the section switcher and renders its header.
func TestSettingsReachesCustomizationsTab(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}}) // Settings
	m = u.(model)
	// From General, ← moves to Customizations (wraps 0 → 2).
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = u.(model)
	if m.settings.subTab != 2 {
		t.Fatalf("← from General should land on Customizations (2), got %d", m.settings.subTab)
	}
	if v := stripANSI(m.settings.View()); !strings.Contains(v, "Customizations") || !strings.Contains(v, "statusline") {
		t.Error("Customizations view should show the section bar + statusline panel")
	}
}
