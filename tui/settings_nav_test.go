package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
)

// settingsModelFor builds a settings model with a fixed roster at a given size, on the
// General sub-tab, so navigation can be driven deterministically.
func settingsModelFor(t *testing.T, w, h int) settingsModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // toggles persist via config.SaveSettings (HOME-based)
	m := NewApp(t.TempDir()).settings
	m.agents = []detect.Agent{
		{Name: "Claude Desktop", Provider: "claude", Command: "x"},
		{Name: "Cursor IDE", Provider: "cursor", Command: "x"},
	}
	m.width, m.height = w, h
	m.subTab = 0
	return m
}

// TestLiveUsageAutoUpdateAreSeparateRows is the navigation fix: on a normal (non-compact)
// terminal, Live Usage and Auto-Update render on their OWN lines, so ↓ moves the cursor
// one visible row at a time instead of hopping sideways within a shared line. ↓ from the
// last agent lands on Live Usage; one more ↓ lands on Auto-Update; both are toggleable.
func TestLiveUsageAutoUpdateAreSeparateRows(t *testing.T) {
	m := settingsModelFor(t, 120, 50) // height ≥ 48 ⇒ non-compact ⇒ split rows
	n := len(m.getUniqueAgents())

	// Each toggle is its own line in the rendered view.
	var liveLines, autoLines int
	for _, ln := range strings.Split(stripANSI(m.View()), "\n") {
		hasBadge := strings.Contains(ln, "[ON]") || strings.Contains(ln, "[OFF]")
		if !hasBadge {
			continue
		}
		if strings.Contains(ln, "Live Usage") {
			liveLines++
		} else if strings.Contains(ln, "Auto-Update") {
			autoLines++
		}
	}
	if liveLines != 1 || autoLines != 1 {
		t.Fatalf("non-compact: Live Usage and Auto-Update must each be their own row (got live=%d auto=%d)", liveLines, autoLines)
	}

	// Drive the cursor to the bottom: Default Trust (0) → agents → Live Usage → Auto-Update.
	for i := 0; i < n+1; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.cursor != n+1 {
		t.Fatalf("after %d ↓ the cursor should be on Live Usage (%d), got %d", n+1, n+1, m.cursor)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != n+2 {
		t.Fatalf("one more ↓ should reach Auto-Update (%d), got %d", n+2, m.cursor)
	}
	// And ↓ at the bottom does not overshoot.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != n+2 {
		t.Errorf("cursor must clamp at Auto-Update (%d), got %d", n+2, m.cursor)
	}

	// Enter toggles Auto-Update (the row under the cursor), not Live Usage.
	before := m.autoUpdate
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.autoUpdate == before {
		t.Error("Enter on the Auto-Update row should toggle Auto-Update")
	}
}

// TestCompactKeepsTogglesOnOneLine guards the fit guarantee: on a short terminal the two
// toggles stay on a single line (so Settings still fits without scrolling).
func TestCompactKeepsTogglesOnOneLine(t *testing.T) {
	m := settingsModelFor(t, 120, 30) // height < 48 ⇒ compact ⇒ combined line
	combined := 0
	for _, ln := range strings.Split(stripANSI(m.View()), "\n") {
		hasBadge := strings.Contains(ln, "[ON]") || strings.Contains(ln, "[OFF]")
		if hasBadge && strings.Contains(ln, "Live Usage") && strings.Contains(ln, "Auto-Update") {
			combined++
		}
	}
	if combined != 1 {
		t.Errorf("compact: Live Usage + Auto-Update should share one line, got %d combined lines", combined)
	}
}
