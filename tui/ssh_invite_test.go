package tui

import (
	"errors"
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

// TestMintedInviteToken guards the output-parsing contract: it must find the
// token on `host invite`'s own "auxly join <token>" hint line and ignore
// everything else, including other indented lines in the same block.
func TestMintedInviteToken(t *testing.T) {
	out := []string{
		"🎫 Auxly invite minted — copy this to the joining machine:",
		"",
		"   auxly1-abc123",
		"",
		"   Expires : 2026-01-01 00:00 (in 1h)",
		"   Pairs   : box:22 (this machine's SSH key: SHA256:x)",
		"",
		"👉 On the joining machine (it must already have SSH login to this box):",
		"   auxly join auxly1-abc123",
		"",
		"   Single-use — consumed automatically on the first successful join.",
	}
	if got := mintedInviteToken(out); got != "auxly1-abc123" {
		t.Fatalf("mintedInviteToken() = %q, want %q", got, "auxly1-abc123")
	}
	if got := mintedInviteToken([]string{"nothing invite-shaped here"}); got != "" {
		t.Fatalf("mintedInviteToken() on unrelated output = %q, want empty", got)
	}
}

// TestInviteMintTokenHeldAndCopyHotkeyCopiesIt covers the full mint -> hold ->
// copy loop: a successful `host invite` mint (simulated via the same
// progressEvent the real captured subprocess emits) leaves the token on the
// model even after the result panel is dismissed, and [y] copies it using the
// injected copy func (never a real clipboard tool in tests).
func TestInviteMintTokenHeldAndCopyHotkeyCopiesIt(t *testing.T) {
	orig := copyInvite
	defer func() { copyInvite = orig }()
	var gotToken string
	copyInvite = func(tok string) error { gotToken = tok; return nil }

	m := sshModel{}
	out := "🎫 Auxly invite minted — copy this to the joining machine:\n\n   auxly1-abc123\n\n" +
		"👉 On the joining machine (it must already have SSH login to this box):\n   auxly join auxly1-abc123\n\n" +
		"   Single-use — consumed automatically on the first successful join.\n"
	m, _ = m.Update(progressEvent{done: true, out: out})

	if m.inviteToken != "auxly1-abc123" {
		t.Fatalf("inviteToken = %q, want %q right after a successful mint", m.inviteToken, "auxly1-abc123")
	}
	if m.mode != sshModeResult {
		t.Fatalf("mode = %q, want %q right after a successful mint", m.mode, sshModeResult)
	}

	// Dismissing the result panel (any key) must not clear the held token.
	m, _ = m.Update(keyRunes("z"))
	if m.mode != sshModeList {
		t.Fatalf("mode after dismiss = %q, want %q", m.mode, sshModeList)
	}
	if m.inviteToken == "" {
		t.Fatal("inviteToken was cleared just by dismissing the result panel")
	}

	m, _ = m.Update(keyRunes("y"))
	if gotToken != "auxly1-abc123" {
		t.Fatalf("copyInvite received %q, want the held token", gotToken)
	}
	if !strings.Contains(m.status, "copied") {
		t.Fatalf("status = %q, want a copy confirmation", m.status)
	}
}

// TestInviteCopyHotkeyUnavailableClipboard covers the failure path: [y] must
// surface the dim, non-alarming fallback rather than an error, mirroring the
// CLI's own auto-copy behavior.
func TestInviteCopyHotkeyUnavailableClipboard(t *testing.T) {
	orig := copyInvite
	defer func() { copyInvite = orig }()
	copyInvite = func(string) error { return errors.New("no clipboard tool found on PATH") }

	m := sshModel{inviteToken: "auxly1-abc123"}
	m, _ = m.Update(keyRunes("y"))
	if !strings.Contains(m.status, "clipboard unavailable") {
		t.Fatalf("status = %q, want the clipboard-unavailable fallback", m.status)
	}
}

// TestInviteCopyHotkeyNoopWithoutToken guards against a stray [y] doing
// anything (or crashing) when no invite has been minted yet.
func TestInviteCopyHotkeyNoopWithoutToken(t *testing.T) {
	orig := copyInvite
	defer func() { copyInvite = orig }()
	called := false
	copyInvite = func(string) error { called = true; return nil }

	m := sshModel{}
	m, _ = m.Update(keyRunes("y"))
	if called {
		t.Fatal("copyInvite was called with no token held")
	}
	if m.status != "" {
		t.Fatalf("status = %q, want untouched", m.status)
	}
}
