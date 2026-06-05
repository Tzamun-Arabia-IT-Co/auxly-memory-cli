package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestDashboardForceUpdateKey is the fix for "[f] not on the dashboard": when boxes
// are outdated, the banner offers BOTH [B] (idle only) and [f] (force, incl. live),
// and pressing each starts an update sweep. Without [f], a fleet whose outdated boxes
// are all live can never be updated from the dashboard ([B] skips them all).
func TestDashboardForceUpdateKey(t *testing.T) {
	m := populatedDashboard(t).dashboard
	m.boxesOutdated = 3

	// The banner advertises both keys.
	banner := stripANSI(m.renderBoxUpdateBanner())
	if !strings.Contains(banner, "[B]") || !strings.Contains(banner, "[f]") {
		t.Fatalf("banner must offer both [B] and [f], got %q", banner)
	}

	// [f] starts a force sweep.
	fm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if !fm.boxesUpdating || cmd == nil {
		t.Error("[f] with outdated boxes must start a force-update sweep")
	}

	// [B] starts the idle-only sweep too.
	bm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}})
	if !bm.boxesUpdating || cmd == nil {
		t.Error("[B] with outdated boxes must start an update sweep")
	}

	// Neither key does anything once a sweep is in flight (no double-trigger).
	busy := m
	busy.boxesUpdating = true
	if _, c := busy.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); c != nil {
		t.Error("[f] must be a no-op while a sweep is already running")
	}

	// With nothing outdated, [f] is inert.
	none := populatedDashboard(t).dashboard
	none.boxesOutdated = 0
	if _, c := none.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); c != nil {
		t.Error("[f] must be inert when no box is outdated")
	}
}

// TestUpdateBannersShowLoadingBar verifies the dashboard's in-flight update banners
// (self-update and box-update) render the shared ▰/▱ marquee, so every loading state in
// the tool shows a consistent moving bar instead of a bare "⏳ …" text line.
func TestUpdateBannersShowLoadingBar(t *testing.T) {
	m := populatedDashboard(t).dashboard

	m.updating = true
	if !strings.ContainsAny(stripANSI(m.renderUpdateBanner()), "▰▱") {
		t.Error("the self-update banner must show the shared loading bar while updating")
	}
	m.updating = false

	m.boxesOutdated = 2
	m.boxesUpdating = true
	if !strings.ContainsAny(stripANSI(m.renderBoxUpdateBanner()), "▰▱") {
		t.Error("the box-update banner must show the shared loading bar while updating")
	}
}
