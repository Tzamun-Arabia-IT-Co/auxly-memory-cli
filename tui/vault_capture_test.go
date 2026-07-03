package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestVaultCapturesInputBlocksGlobalKeys is the regression for the CRITICAL
// finding: app.go's global key switch (digits -> gotoScreen, q/ctrl+c ->
// tea.Quit) used to fire even while the Settings -> Vault panel owned the
// keyboard (passphrase entry, the init picker, a busy op), so typing a
// passphrase containing a digit switched tabs mid-entry and 'q' quit the app
// instead of reaching the buffer. The new guard in app.go (mirroring
// m.settings.cust.capturesInput()) must route these keys to vault instead.
func TestVaultCapturesInputBlocksGlobalKeys(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	m.screen = screenSettings
	m.settings.subTab = 3
	m.settings.vault.loaded = true
	m.settings.vault.mode = vaultModePass1

	for _, key := range []tea.KeyMsg{keyRunes("1"), keyRunes("q"), {Type: tea.KeyCtrlC}} {
		result, cmd := m.Update(key)
		next, ok := result.(model)
		if !ok {
			t.Fatalf("Update returned %T, want model", result)
		}
		if next.screen != screenSettings {
			t.Fatalf("key %q changed screen to %v, want it to stay on Settings", key.String(), next.screen)
		}
		if cmd != nil {
			t.Fatalf("key %q dispatched a command (e.g. tea.Quit) instead of being absorbed by the vault buffer", key.String())
		}
		m = next
	}

	if m.settings.vault.passBuf1 != "1q" {
		t.Fatalf("passBuf1 = %q, want the digit and 'q' to have reached the vault buffer (\"1q\")", m.settings.vault.passBuf1)
	}
}

// TestVaultInitChoiceReachesVaultThroughTopLevel guards the keypair-vs-
// passphrase init picker: [1]/[2] used to be eaten by the global tab switcher
// (mapped to gotoScreen) before ever reaching vaultModel. Both must reach
// vault and select the right branch, without changing the active screen.
func TestVaultInitChoiceReachesVaultThroughTopLevel(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	m.screen = screenSettings
	m.settings.subTab = 3
	m.settings.vault.loaded = true
	m.settings.vault.mode = vaultModeInitChoice

	u, _ = m.Update(keyRunes("1"))
	m = u.(model)
	if m.settings.vault.mode != vaultModeConfirmKey {
		t.Fatalf("[1] on the init picker: mode = %q, want %q", m.settings.vault.mode, vaultModeConfirmKey)
	}
	if m.screen != screenSettings {
		t.Fatalf("[1] on the init picker must not switch tabs, screen = %v", m.screen)
	}

	m.settings.vault.mode = vaultModeInitChoice
	u, _ = m.Update(keyRunes("2"))
	m = u.(model)
	if m.settings.vault.mode != vaultModePass1 {
		t.Fatalf("[2] on the init picker: mode = %q, want %q", m.settings.vault.mode, vaultModePass1)
	}
}

// TestVaultCompletionClearsBusyAfterLeavingSettings is the regression for the
// MAJOR finding: app.go used to route messages only to the current screen's
// submodel, so a vault tea.Cmd (encrypt/decrypt/init/rebuild) that completed
// after the user switched away from Settings dropped its result on the floor
// — vault.busy never cleared, freezing the panel on return. The completion
// message must now reach settingsModel regardless of the active screen.
func TestVaultCompletionClearsBusyAfterLeavingSettings(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	m.settings.vault.loaded = true
	m.settings.vault.busy = true
	m.settings.vault.busyLabel = "Encrypting business.md"

	// Leave Settings while the encrypt op is still in flight.
	m.screen = screenDashboard

	u, _ = m.Update(vaultActionMsg{kind: "encrypt", ok: true})
	m = u.(model)

	if m.settings.vault.busy {
		t.Fatal("busy did not clear: the completion message was dropped while away from Settings")
	}
	if m.screen != screenDashboard {
		t.Fatalf("routing the completion message must not itself change the active screen, got %v", m.screen)
	}
}

// TestLeavingSettingsClearsVaultKeyMaterial is the regression for the other
// half of the MAJOR finding: a one-time backup key or a typed passphrase must
// not linger in model state (and re-render) once the user leaves Settings.
func TestLeavingSettingsClearsVaultKeyMaterial(t *testing.T) {
	m := NewApp(t.TempDir())
	m.gotoScreen(screenSettings)
	m.settings.vault.backupKey = "AGE-SECRET-KEY-1TESTONLY"
	m.settings.vault.passBuf1 = "correct-horse-battery"
	m.settings.vault.passBuf2 = "correct-horse-batter"

	m.gotoScreen(screenDashboard)

	if m.settings.vault.backupKey != "" || m.settings.vault.passBuf1 != "" || m.settings.vault.passBuf2 != "" {
		t.Fatalf("vault key material survived leaving Settings: backupKey=%q passBuf1=%q passBuf2=%q",
			m.settings.vault.backupKey, m.settings.vault.passBuf1, m.settings.vault.passBuf2)
	}
}
