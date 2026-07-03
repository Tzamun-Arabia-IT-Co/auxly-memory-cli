package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestJoinKeyOpensModal guards [J]'s entry point: it opens the paste-token
// modal and grabs the keyboard (editingHost) so app.go routes every key here
// — [j] is already ↓ in this list, so the mint/join pair uses J/I casing
// (mirrors [i]/[I] on the same tab).
func TestJoinKeyOpensModal(t *testing.T) {
	m := sshModel{}
	m, _ = m.Update(keyRunes("J"))
	if m.mode != sshModeJoin {
		t.Fatalf("mode = %q, want %q", m.mode, sshModeJoin)
	}
	if !m.editingHost {
		t.Fatal("[J] must set editingHost so the paste buffer captures every key")
	}
}

// TestJoinModalTypeAndSubmit guards the paste -> run transition: typed runes
// accumulate in joinToken, and Enter with a non-empty token starts the same
// captured-run pipeline every other host action on this tab uses (mode ->
// sshModeProgress via beginCapturedSub), shelling `auxly join <token>` rather
// than reimplementing cmd/join.go's logic.
func TestJoinModalTypeAndSubmit(t *testing.T) {
	m := sshModel{mode: sshModeJoin, editingHost: true}
	m, _ = m.Update(keyRunes("auxly1-abc123"))
	if m.joinToken != "auxly1-abc123" {
		t.Fatalf("joinToken = %q, want %q", m.joinToken, "auxly1-abc123")
	}

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != sshModeProgress {
		t.Fatalf("mode after enter = %q, want %q", m.mode, sshModeProgress)
	}
	if !strings.Contains(m.progressTitle, "Joining") {
		t.Fatalf("progressTitle = %q, want it to mention joining", m.progressTitle)
	}
	if cmd == nil {
		t.Fatal("submitting a token should return a non-nil command to drive the captured run")
	}
	if m.editingHost {
		t.Fatal("editingHost should be released once the run starts")
	}
	if m.joinToken != "" {
		t.Fatal("joinToken should be cleared once handed off to the captured run")
	}
}

// TestJoinModalEmptyEnterNoop guards against submitting a blank token.
func TestJoinModalEmptyEnterNoop(t *testing.T) {
	m := sshModel{mode: sshModeJoin, editingHost: true}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != sshModeJoin || cmd != nil {
		t.Fatalf("empty enter should be a no-op: mode=%q cmd=%v", m.mode, cmd)
	}
}

// TestJoinModalEscCancels guards the cancel path: esc clears the buffer and
// releases the keyboard back to the list.
func TestJoinModalEscCancels(t *testing.T) {
	m := sshModel{mode: sshModeJoin, editingHost: true, joinToken: "partial"}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != sshModeList || m.editingHost || m.joinToken != "" {
		t.Fatalf("esc should reset to list mode with a cleared buffer: mode=%q editingHost=%v joinToken=%q",
			m.mode, m.editingHost, m.joinToken)
	}
}

// TestJoinCompletionSuccessAndFailure guards the honest success/partial/
// failure distinction: the captured subprocess's own stdout (cmd/join.go's
// joinCompletionMessage) carries the wording that tells partial success
// ("Joined, but the local link isn't verified") apart from a hard failure —
// the TUI surfaces it verbatim (progressOut) rather than collapsing it to a
// single pass/fail badge. No real subprocess runs here — completion is
// simulated the same way TestInviteMintTokenHeldAndCopyHotkeyCopiesIt does,
// by feeding the exact progressEvent a captured `auxly join` would emit.
func TestJoinCompletionSuccessAndFailure(t *testing.T) {
	m := sshModel{mode: sshModeProgress}
	successOut := "✓ Host identity verified\n🎉 Joined box:22's memory.\n"
	m, _ = m.Update(progressEvent{done: true, err: nil, out: successOut})
	if m.mode != sshModeResult || !m.progressOK {
		t.Fatalf("successful join: mode=%q progressOK=%v, want result/true", m.mode, m.progressOK)
	}
	if !strings.Contains(strings.Join(m.progressOut, "\n"), "🎉 Joined") {
		t.Fatalf("progressOut = %v, want the success message", m.progressOut)
	}

	m2 := sshModel{mode: sshModeProgress}
	partialOut := "⚠ Joined box:22, but the local link isn't verified working (probe failed).\n"
	m2, _ = m2.Update(progressEvent{
		done: true,
		err:  errors.New("join completed but the local selftest failed — see above"),
		out:  partialOut,
	})
	if m2.mode != sshModeResult || m2.progressOK {
		t.Fatalf("partial join: mode=%q progressOK=%v, want result/false", m2.mode, m2.progressOK)
	}
	if !strings.Contains(strings.Join(m2.progressOut, "\n"), "isn't verified working") {
		t.Fatalf("progressOut = %v, want the partial-success wording (not a generic failure)", m2.progressOut)
	}
}
