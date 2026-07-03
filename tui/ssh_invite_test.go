package tui

import (
	"strings"
	"testing"
)

// TestInviteTTLCyclesAndWraps guards [i]'s state transition: it walks
// inviteTTLIdx through inviteTTLPresets in order and wraps back to the start,
// surfacing the pending choice in m.status (rendered in the action-bar area).
func TestInviteTTLCyclesAndWraps(t *testing.T) {
	m := sshModel{}
	if got := inviteTTLPresets[m.inviteTTLIdx]; got != "1h" {
		t.Fatalf("default invite TTL = %q, want 1h (index 0)", got)
	}

	for _, want := range []string{"24h", "7d", "1h"} {
		m, _ = m.Update(keyRunes("i"))
		if got := inviteTTLPresets[m.inviteTTLIdx]; got != want {
			t.Fatalf("after [i] TTL = %q, want %q", got, want)
		}
		if !strings.Contains(m.status, want) {
			t.Fatalf("status = %q, want it to mention the pending TTL %q", m.status, want)
		}
	}
}

// TestInviteMintStartsCapturedRun guards [I]'s state transition: it starts
// the same captured-run pipeline every other host action here uses (mode ->
// sshModeProgress, a titled run in flight), shelling `auxly host invite --ttl
// <pending preset>` rather than a bespoke SSH/process path.
func TestInviteMintStartsCapturedRun(t *testing.T) {
	m := sshModel{}
	m, _ = m.Update(keyRunes("i")) // pick "24h"

	m, cmd := m.Update(keyRunes("I"))
	if m.mode != sshModeProgress {
		t.Fatalf("[I] mode = %q, want %q (captured run in flight)", m.mode, sshModeProgress)
	}
	if !strings.Contains(m.progressTitle, "Minting invite") || !strings.Contains(m.progressTitle, "24h") {
		t.Fatalf("progressTitle = %q, want it to mention minting the pending TTL", m.progressTitle)
	}
	if cmd == nil {
		t.Fatal("[I] should return a non-nil command to drive the captured run")
	}
	// Idle-mode-only keys must not leak into the modal while it's minting.
	if _, ok := m.cursorOnRemote(); ok {
		t.Fatal("progress mode should not resolve to a remote row")
	}
}

// TestInviteKeysAvailableWithoutHostSetup guards the design deviation from
// the relay panel: invite minting works even when this machine never ran
// `auxly host setup` (hostOK == false) — Sprint 21's direct-SSH pairing has
// no relay dependency.
func TestInviteKeysAvailableWithoutHostSetup(t *testing.T) {
	m := sshModel{hostOK: false}
	m, cmd := m.Update(keyRunes("I"))
	if m.mode != sshModeProgress || cmd == nil {
		t.Fatalf("mint should work without hostOK: mode=%q cmd=%v", m.mode, cmd)
	}
}
