package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestVaultInitKeypairBranch guards the keypair path of [i] init: choosing
// [1] goes straight to a hard-warning confirm (no password prompt), and
// confirming dispatches the generate command without executing it (no real
// keychain/file write here — see TestVaultInitCompletionShowsBackupKeyOnce
// for the result-side state transition, driven by a synthetic message).
func TestVaultInitKeypairBranch(t *testing.T) {
	m := vaultModel{memoryPath: t.TempDir(), loaded: true, keyExists: false}

	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if m.mode != vaultModeInitChoice {
		t.Fatalf("mode after [i] = %q, want %q", m.mode, vaultModeInitChoice)
	}

	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.mode != vaultModeConfirmKey {
		t.Fatalf("mode after [1] = %q, want %q", m.mode, vaultModeConfirmKey)
	}

	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.busy {
		t.Fatal("[y] on the keypair confirm should enter the busy state")
	}
	if cmd == nil {
		t.Fatal("[y] on the keypair confirm should dispatch the generate command")
	}
	if !strings.Contains(m.busyLabel, "Generating") {
		t.Fatalf("busyLabel = %q, want it to mention generating a key", m.busyLabel)
	}
}

// TestVaultInitPassphraseBranch guards the passphrase path of [i] init: it
// requires >= minVaultPassphraseLen characters, a matching confirmation, and
// only then reaches the hard no-recovery warning before dispatching.
func TestVaultInitPassphraseBranch(t *testing.T) {
	m := vaultModel{memoryPath: t.TempDir(), loaded: true, keyExists: false}
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m.mode != vaultModePass1 {
		t.Fatalf("mode after [2] = %q, want %q", m.mode, vaultModePass1)
	}

	// Too short: rejected, stays on pass1.
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("short")})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != vaultModePass1 || m.passErr == "" {
		t.Fatalf("short passphrase: mode=%q passErr=%q, want pass1 with an error", m.mode, m.passErr)
	}

	// Long enough: advances to the confirmation entry.
	m.passBuf1, m.passErr = "", ""
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("correct-horse-battery")})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != vaultModePass2 {
		t.Fatalf("mode after a valid first entry = %q, want %q", m.mode, vaultModePass2)
	}

	// Mismatch: bounced back to pass1 with buffers cleared, not silently accepted.
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("does-not-match")})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != vaultModePass1 || m.passErr == "" || m.passBuf1 != "" {
		t.Fatalf("mismatched confirm: mode=%q passErr=%q passBuf1=%q, want pass1/error/cleared",
			m.mode, m.passErr, m.passBuf1)
	}

	// Redo, matching this time: reaches the hard no-recovery warning.
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("correct-horse-battery")})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("correct-horse-battery")})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != vaultModeConfirmPass {
		t.Fatalf("mode after matching entries = %q, want %q", m.mode, vaultModeConfirmPass)
	}

	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy || cmd == nil {
		t.Fatalf("confirming the passphrase warning should dispatch: busy=%v cmd=%v", m.busy, cmd)
	}
	if m.passBuf1 != "" || m.passBuf2 != "" {
		t.Fatal("passphrase buffers must be cleared once handed off to the command")
	}
}

// TestVaultInitEscCancelsAtEveryStep guards that esc always backs out
// (never silently discards without returning control), and that the idle
// [i] key is inert once a key already exists (encrypt init refuses a
// clobber, same as vaultcrypt.ErrKeyExists) so an existing vault can't be
// re-initialized by accident.
func TestVaultInitEscCancelsAtEveryStep(t *testing.T) {
	m := vaultModel{memoryPath: t.TempDir(), loaded: true, keyExists: false}
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != "" {
		t.Fatalf("esc from initChoice: mode = %q, want idle", m.mode)
	}

	already := vaultModel{memoryPath: t.TempDir(), loaded: true, keyExists: true}
	already, _ = already.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if already.mode != "" {
		t.Fatalf("[i] with a key already present must be a no-op, got mode = %q", already.mode)
	}
}

// TestVaultInitCompletionShowsBackupKeyOnce guards the result-side state
// transition: a successful keypair init shows the backup key exactly once,
// [c] copies it via the injected clipboard seam (never a real clipboard tool
// in tests, mirrors ssh.go's copyInvite pattern), and dismissing clears it.
func TestVaultInitCompletionShowsBackupKeyOnce(t *testing.T) {
	orig := copyVaultKey
	defer func() { copyVaultKey = orig }()
	var gotKey string
	copyVaultKey = func(k string) error { gotKey = k; return nil }

	m := vaultModel{memoryPath: t.TempDir(), loaded: true, busy: true, mode: vaultModeConfirmKey}
	m, _ = m.Update(vaultActionMsg{kind: "init", ok: true, backupKey: "AGE-SECRET-KEY-1TESTONLY"})
	if m.busy {
		t.Fatal("busy should clear once the result lands")
	}
	if m.mode != vaultModeBackupKey || m.backupKey != "AGE-SECRET-KEY-1TESTONLY" {
		t.Fatalf("mode=%q backupKey=%q, want backupKey mode holding the generated key", m.mode, m.backupKey)
	}

	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if gotKey != "AGE-SECRET-KEY-1TESTONLY" {
		t.Fatalf("copyVaultKey received %q, want the held backup key", gotKey)
	}

	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != "" || m.backupKey != "" {
		t.Fatalf("dismissing should clear the key from memory: mode=%q backupKey=%q", m.mode, m.backupKey)
	}
	if cmd == nil {
		t.Fatal("dismissing should re-refresh status (key now exists)")
	}
}

// TestVaultDecryptConfirmGate guards decrypt's confirm gate: [d] on an
// encrypted row only ARMS the confirm, [n] backs out untouched, and only
// [y] dispatches the actual decrypt command.
func TestVaultDecryptConfirmGate(t *testing.T) {
	m := vaultModel{
		memoryPath: t.TempDir(), loaded: true, keyExists: true,
		files: []string{"business.md"}, fileEnc: map[string]bool{"business.md": true},
	}

	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if m.mode != vaultModeConfirmDecry || m.decryptTarget != "business.md" {
		t.Fatalf("mode=%q decryptTarget=%q, want the confirm armed on business.md", m.mode, m.decryptTarget)
	}
	if cmd != nil {
		t.Fatal("[d] must not itself decrypt — only arm the confirm")
	}

	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.mode != "" || m.decryptTarget != "" {
		t.Fatalf("[n] should cancel the confirm: mode=%q decryptTarget=%q", m.mode, m.decryptTarget)
	}

	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.busy || cmd == nil {
		t.Fatalf("[y] should dispatch the decrypt: busy=%v cmd=%v", m.busy, cmd)
	}
	if m.mode != "" {
		t.Fatalf("mode after dispatch = %q, want idle (busy owns the display)", m.mode)
	}

	// Result-side: a synthetic completion clears busy and refreshes.
	m, cmd = m.Update(vaultActionMsg{kind: "decrypt", ok: true})
	if m.busy {
		t.Fatal("busy should clear once the decrypt result lands")
	}
	if !strings.Contains(m.status, "plaintext") {
		t.Fatalf("status = %q, want a plaintext confirmation", m.status)
	}
	if cmd == nil {
		t.Fatal("a completed decrypt should re-refresh status")
	}
}

// TestVaultEncryptGatedOnKeyAndPlaintext guards [e]: it's inert on an
// already-encrypted row and inert with no key initialized, and only dispatches
// when both conditions are met.
func TestVaultEncryptGatedOnKeyAndPlaintext(t *testing.T) {
	base := vaultModel{
		memoryPath: t.TempDir(), loaded: true,
		files: []string{"projects.md"}, fileEnc: map[string]bool{"projects.md": false},
	}

	noKey := base
	noKey.keyExists = false
	noKey, cmd := noKey.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if noKey.busy || cmd != nil {
		t.Fatal("[e] without an initialized key must be a no-op")
	}

	alreadyEnc := base
	alreadyEnc.keyExists = true
	alreadyEnc.fileEnc = map[string]bool{"projects.md": true}
	alreadyEnc, cmd = alreadyEnc.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if alreadyEnc.busy || cmd != nil {
		t.Fatal("[e] on an already-encrypted file must be a no-op")
	}

	ready := base
	ready.keyExists = true
	ready, cmd = ready.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if !ready.busy || cmd == nil {
		t.Fatalf("[e] on a plaintext file with a key present should dispatch: busy=%v cmd=%v", ready.busy, cmd)
	}
	if !strings.Contains(ready.busyLabel, "Encrypting") {
		t.Fatalf("busyLabel = %q, want it to mention encrypting", ready.busyLabel)
	}
}

// TestVaultRebuildGatedOnEmbedder guards [r]: it's inert when no embedder is
// available (nothing to rebuild with) and dispatches otherwise. No real
// embedder call happens in this test — only the dispatch decision and the
// result-side transition (via a synthetic vaultActionMsg) are checked.
func TestVaultRebuildGatedOnEmbedder(t *testing.T) {
	off := vaultModel{memoryPath: t.TempDir(), loaded: true, embedEnabled: false}
	off, cmd := off.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if off.busy || cmd != nil {
		t.Fatal("[r] without an available embedder must be a no-op")
	}

	on := vaultModel{memoryPath: t.TempDir(), loaded: true, embedEnabled: true}
	on, cmd = on.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !on.busy || cmd == nil {
		t.Fatal("[r] with an available embedder should dispatch the rebuild")
	}

	on, cmd = on.Update(vaultActionMsg{kind: "rebuild", ok: true, chunks: 42})
	if on.busy {
		t.Fatal("busy should clear once the rebuild result lands")
	}
	if !strings.Contains(on.status, "42") {
		t.Fatalf("status = %q, want it to report the chunk count", on.status)
	}
	if cmd == nil {
		t.Fatal("a completed rebuild should re-refresh status")
	}

	// An unreachable embedding endpoint is an optional-feature-not-set-up state,
	// so it's marked ⚠ (amber), NOT a red ✗ crash — the whole point of the fix.
	unavail := vaultModel{memoryPath: t.TempDir(), loaded: true, busy: true}
	unavail, _ = unavail.Update(vaultActionMsg{kind: "rebuild", ok: false, err: errors.New("embedding endpoint unavailable")})
	if !strings.HasPrefix(unavail.status, "⚠") {
		t.Fatalf("status = %q, want the ⚠ optional-not-available marker", unavail.status)
	}
	// A GENUINE rebuild failure (not an availability issue) still gets ✗.
	broke := vaultModel{memoryPath: t.TempDir(), loaded: true, busy: true}
	broke, _ = broke.Update(vaultActionMsg{kind: "rebuild", ok: false, err: errors.New("write index: disk full")})
	if !strings.HasPrefix(broke.status, "✗") {
		t.Fatalf("status = %q, want a ✗ failure marker for a real error", broke.status)
	}
}

// TestVaultBusyFreezesInput guards that no key (besides the ones the
// spinner tick itself handles) reaches the handler while a command is in
// flight — otherwise a stray keypress mid-keychain-call could double-fire it.
func TestVaultBusyFreezesInput(t *testing.T) {
	m := vaultModel{memoryPath: t.TempDir(), loaded: true, busy: true, mode: vaultModeConfirmKey}
	m2, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil || m2.mode != vaultModeConfirmKey {
		t.Fatal("input must be frozen while busy")
	}
}

// TestVaultSubTabReachableViaRing guards that Settings' section switcher
// actually reaches the Vault sub-tab (subTab 3) and that its panel renders,
// closing the "every gap must be reachable from the TUI" audit requirement
// for the new sub-tab itself, not just its individual actions.
func TestVaultSubTabReachableViaRing(t *testing.T) {
	m := settingsModelFor(t, 120, 50)
	m.subTab = 0
	// General -> Agents -> Vault (l/right twice).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.subTab != 3 {
		t.Fatalf("subTab after General->Agents->Vault = %d, want 3", m.subTab)
	}
	// The l/right transition itself only dispatches the refresh command (real
	// disk/keychain IO) — fold in a synthetic result here, same seam as every
	// other vault test, so the panel renders its loaded content.
	m.vault, _ = m.vault.Update(vaultRefreshMsg{})
	if !strings.Contains(stripANSI(m.View()), "Vault Encryption") {
		t.Error("Vault view should render the encryption panel")
	}
}
