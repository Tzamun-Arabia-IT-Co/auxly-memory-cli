package tui

import (
	"strings"
	"testing"
)

// TestOrgRunningHasCreepingBar verifies Memory Org's "Organizing" screen shows the same
// creeping ▰/▱ loading bar as the Remote tab: it renders a bar, and each spin tick
// advances it (the agent run is opaque, so the bar must show motion, not sit frozen).
func TestOrgRunningHasCreepingBar(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.mode = orgRunning
	m.runProgress = 20
	m.runProvider = "claude-code"

	// The running view renders a bar made of the shared glyphs.
	view := stripANSI(m.runningView())
	if !strings.ContainsAny(view, "▰▱") {
		t.Fatalf("the Organizing screen must render the shared ▰/▱ loading bar:\n%s", view)
	}

	// A spin tick advances the bar (creep) while running.
	before := m.runProgress
	um, _ := m.Update(orgSpinTickMsg{})
	if um.runProgress <= before {
		t.Errorf("a spin tick must creep the Memory Org bar forward (%d → %d)", before, um.runProgress)
	}

	// The creep never reaches 100% on its own (only the result/transition completes it).
	p := um.runProgress
	for i := 0; i < 300; i++ {
		um, _ = um.Update(orgSpinTickMsg{})
		p = um.runProgress
	}
	if p >= 100 {
		t.Errorf("the creeping bar must stay below 100%% until the run completes, reached %d", p)
	}
}
