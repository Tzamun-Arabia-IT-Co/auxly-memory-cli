package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

// TestMethodRelayIsFirst locks the reordered method picker: pressing "1" on the
// method step now selects relay (the primary flow), not lan.
func TestMethodRelayIsFirst(t *testing.T) {
	m := sshModel{mode: sshModeForm, formStep: formStepMethod}
	m, _ = m.handleFormKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.formMethod != "relay" {
		t.Errorf("method key 1 should select relay, got %q", m.formMethod)
	}
	if m.formStep != formStepHost {
		t.Errorf("choosing a method should advance to the host step, got %q", m.formStep)
	}

	// "2" is direct — the wizard no longer asks lan/vpn/public; the address
	// decides at submit (privateHostTUI), and the OS question is gone entirely.
	m2 := sshModel{mode: sshModeForm, formStep: formStepMethod}
	m2, _ = m2.handleFormKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m2.formMethod != "direct" {
		t.Errorf("method key 2 should select direct, got %q", m2.formMethod)
	}
	// Direct host step advances straight to name (no OS step in the chain).
	m2.formStep = formStepHost
	m2.formHost = "10.0.0.7"
	m2, _ = m2.advanceForm()
	if m2.formStep != formStepName {
		t.Errorf("direct host step should advance to name, got %q", m2.formStep)
	}
}

// TestDirectMethodResolvesByAddress locks the auto-resolution: private targets
// become lan, public ones public — the user never answers a networking question.
func TestDirectMethodResolvesByAddress(t *testing.T) {
	cases := []struct {
		spec    string
		private bool
	}{
		{"root@10.0.0.7", true},
		{"192.168.1.24:2222", true},
		{"box.local", true},
		{"user@203.0.113.9", false},
		{"build.example.com", false},
		{"root@[fd12:3456:789a::1]:22", true},
		{"[2001:db8::1]:2222", false},
	}
	for _, c := range cases {
		if got := privateHostTUI(c.spec); got != c.private {
			t.Errorf("privateHostTUI(%q) = %v, want %v", c.spec, got, c.private)
		}
	}
}

// TestRelayWizardReachesPermissionsStep locks the requested flow: for relay, the name
// step advances to the in-wizard permissions picker (seeded with sane defaults) before
// any connect; a consumer method submits straight from the name step instead.
func TestRelayWizardReachesPermissionsStep(t *testing.T) {
	// Relay: name → permissions step, seeded read-only for non-personal, personal Off.
	m := sshModel{mode: sshModeForm, formStep: formStepName, formMethod: "relay", formHost: "relay.example:22", formName: "BoxX"}
	m, _ = m.advanceForm()
	if m.formStep != formStepShare {
		t.Fatalf("relay name step should advance to the permissions step, got %q", m.formStep)
	}
	if len(m.shareFiles) == 0 || m.shareState == nil {
		t.Fatal("the permissions step must seed the file list + tri-state")
	}
	if m.shareState["personal.md"] != shareOff {
		t.Error("personal.md must default to Off in the permissions step")
	}
	// Non-personal files default to Read+Write (a connected box is a full peer).
	first := m.shareFiles[0]
	if memory.IsPersonalFile(first) {
		first = m.shareFiles[1]
	}
	if m.shareState[first] != shareReadWrite {
		t.Errorf("non-personal files must default to Read+Write, got %d for %q", m.shareState[first], first)
	}

	// ←/→ cycles the highlighted row to a different state (here Read+Write → Off).
	m.shareCursor = 0
	before := m.shareState[m.shareFiles[0]]
	m, _ = m.handleFormShareKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.shareState[m.shareFiles[0]] == before {
		t.Error("→ should change the highlighted file's access state")
	}

	// Consumer method: the name step submits, never reaching the permissions step.
	c := sshModel{mode: sshModeForm, formStep: formStepName, formMethod: "lan", formHost: "user@10.0.0.5", formOS: "linux", formName: "Srv"}
	c, _ = c.advanceForm()
	if c.formStep == formStepShare {
		t.Error("a consumer method must not enter the permissions step")
	}
}

// TestShareSelectionSplitsTriState verifies the tri-state map collapses to the
// shared/writable lists clients.yaml stores.
func TestShareSelectionSplitsTriState(t *testing.T) {
	files := []string{"identity.md", "projects.md", "personal.md"}
	state := map[string]int{"identity.md": shareRead, "projects.md": shareReadWrite, "personal.md": shareOff}
	shared, writes := shareSelection(files, state)
	if len(shared) != 2 || shared[0] != "identity.md" || shared[1] != "projects.md" {
		t.Errorf("shared should be the two non-Off files in order, got %v", shared)
	}
	if len(writes) != 1 || writes[0] != "projects.md" {
		t.Errorf("writes should be only the Read+Write file, got %v", writes)
	}
}

// TestNewlyAddedClient covers the diff used to attach the post-connect share step to
// the freshly provisioned box: a brand-new name is found by diff; a re-add (name was
// already present) falls back to an exact name match; nothing matches → ok=false.
func TestNewlyAddedClient(t *testing.T) {
	before := clientNameSet([]clientRow{{Name: "BoxA"}})
	after := []clientRow{{Name: "BoxA"}, {Name: "BoxB", Target: "root@1.2.3.4"}}

	nc, ok := newlyAddedClient(after, before, "BoxB")
	if !ok || nc.Name != "BoxB" {
		t.Fatalf("the new client BoxB should be found by diff, got %+v ok=%v", nc, ok)
	}

	// Re-add: BoxA already existed, so the diff is empty — fall back to the typed name.
	nc, ok = newlyAddedClient([]clientRow{{Name: "BoxA"}}, before, "BoxA")
	if !ok || nc.Name != "BoxA" {
		t.Fatalf("a re-add should resolve via the name fallback, got %+v ok=%v", nc, ok)
	}

	// Nothing new and no usable name → no client to share with.
	if _, ok := newlyAddedClient([]clientRow{{Name: "BoxA"}}, before, ""); ok {
		t.Error("with no new client and no name, newlyAddedClient must report ok=false")
	}
}

// TestMilestonePctAdvancesForHostUpdate is the fix for the progress bar stuck at 0%
// during a box update: the host-update flow's streamed lines must move the bar, not
// just the connect/doctor flow's. Asserts the actual lines `host update` emits map to
// rising, non-zero milestones (and stay monotonic, the way the caller applies them).
func TestMilestonePctAdvancesForHostUpdate(t *testing.T) {
	// The exact line from the screenshot must NOT map to 0%.
	if got := milestonePct("   ⬆ Updating ERPAI (1.0.9 → 1.0.10)..."); got == 0 {
		t.Fatalf("the 'Updating …' line must advance the bar, got 0%%")
	}

	flow := []string{
		"   ⬆ Updating ERPAI (1.0.9 → 1.0.10)...",
		"   ✓ ERPAI updated to 1.0.10",
		"   ✓ statusline applied on ERPAI (mirrors your mode + usage refreshed now)",
	}
	prev := 0
	for _, ln := range flow {
		// Mirror the caller: the bar only ever rises.
		p := milestonePct(ln)
		if p > prev {
			prev = p
		}
		if prev == 0 {
			t.Errorf("line %q left the bar at 0%%", ln)
		}
	}
	if prev < 90 {
		t.Errorf("by the end of an update the bar should be near complete, got %d%%", prev)
	}
}

// TestCreepProgressAlwaysAdvances is the fix for the bar that sat at one number then
// jumped to done: it ramps briskly while low, keeps the number inching during the slow
// crawl, never exceeds the ceiling, and converges there (the done event fills 100%).
func TestCreepProgressAlwaysAdvances(t *testing.T) {
	// Fast ramp: below the ramp top, every tick strictly advances regardless of frame.
	for _, start := range []int{0, 35, 60, creepRampTop - 1} {
		if next := creepProgress(start, 1); next <= start {
			t.Errorf("fast-ramp creep from %d must advance, got %d", start, next)
		}
	}
	// Simulate a full run tick-by-tick (frame advances each tick): climb to the ceiling,
	// never overshoot, never go backwards.
	pct := 0
	for frame := 0; frame < 2000; frame++ {
		next := creepProgress(pct, frame)
		if next < pct {
			t.Fatalf("creep went backwards: %d → %d", pct, next)
		}
		pct = next
	}
	if pct != progressCreepCeiling {
		t.Errorf("creep should converge to the ceiling %d, reached %d", progressCreepCeiling, pct)
	}
	// During the slow crawl the number keeps moving over time, but only intermittently —
	// that intermittency is what makes it a crawl rather than a sprint.
	held, advanced := false, false
	for frame := 0; frame < 30; frame++ {
		if creepProgress(creepRampTop+2, frame) == creepRampTop+2 {
			held = true
		} else {
			advanced = true
		}
	}
	if !held || !advanced {
		t.Errorf("the crawl phase should advance intermittently (held=%v advanced=%v)", held, advanced)
	}
	// At/above the ceiling it holds (only a milestone or done may push higher).
	if creepProgress(progressCreepCeiling, 0) != progressCreepCeiling {
		t.Error("creep must hold at the ceiling, not exceed it")
	}
}

// TestReconnectCompletionIsAMilestone ensures the reconnect's final line snaps the bar
// near-complete (it previously matched nothing, leaving the bar at 35% until done).
func TestReconnectCompletionIsAMilestone(t *testing.T) {
	if got := milestonePct("   ✓ Re-wired ERPAI to this machine's memory"); got < 90 {
		t.Errorf("the reconnect 're-wired' line should be a near-complete milestone, got %d%%", got)
	}
}
