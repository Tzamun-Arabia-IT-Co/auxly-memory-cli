package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutoWire_SkipsAbsentAgents locks the critical safety property: on an
// empty HOME (no ~/.claude, no ~/.codex config dir, no gemini/kimi/antigravity
// dir), autoWireCleanHooks must never CREATE either config file, nor write a
// shell wrapper rc file. claudeInstalled/codexInstalled gate on the config
// dir already existing rather than a binary being on PATH; PATH is emptied
// here so geminiInstalled/kimiInstalled/antigravityInstalled's LookPath check
// can't pick up a real gemini/kimi/agy binary that happens to be installed on
// the machine running the test.
func TestAutoWire_SkipsAbsentAgents(t *testing.T) {
	home := t.TempDir()
	rc := filepath.Join(t.TempDir(), "rc")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("AUXLY_HOOK_RC", rc)
	t.Setenv("PATH", "")

	if wired := autoWireCleanHooks(home); len(wired) != 0 {
		t.Fatalf("expected no agents wired on an empty HOME, got %v", wired)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
		t.Fatal("autoWireCleanHooks created ~/.claude/settings.json for an absent agent")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err == nil {
		t.Fatal("autoWireCleanHooks created ~/.codex/config.toml for an absent agent")
	}
	if _, err := os.Stat(rc); err == nil {
		t.Fatal("autoWireCleanHooks wrote a shell wrapper rc file for absent gemini/kimi/antigravity")
	}
}

// TestAutoWire_WiresPresentAndIdempotent: both config dirs pre-exist (as they
// would on a machine where the agent is actually installed) → both hooks get
// wired once, and re-running is a no-op.
func TestAutoWire_WiresPresentAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}

	wired := autoWireCleanHooks(home)
	got := map[string]bool{}
	for _, a := range wired {
		got[a] = true
	}
	if !got["claude"] || !got["codex"] {
		t.Fatalf("expected both claude and codex wired, got %v", wired)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Fatalf("claude hook file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatalf("codex hook file missing: %v", err)
	}

	if again := autoWireCleanHooks(home); len(again) != 0 {
		t.Fatalf("second call not idempotent, wired %v again", again)
	}
}

// TestAutoWire_WiresShellWrapperAgentsPresentAndIdempotent covers the new
// auto-wire-all behavior: gemini/kimi/antigravity get the ~/.zshrc (here,
// AUXLY_HOOK_RC) shell wrapper too, gated on actual presence — not just
// installShellWrapper's own idempotency. PATH is emptied so LookPath can't
// pick up a real `gemini` binary on the test machine; presence for kimi and
// antigravity is instead signaled the other way each detector supports: a
// config dir (kimiInstalled checks ~/.kimi-code/~/.kimi; antigravityInstalled
// checks ~/.antigravity).
func TestAutoWire_WiresShellWrapperAgentsPresentAndIdempotent(t *testing.T) {
	home := t.TempDir()
	rc := filepath.Join(t.TempDir(), "rc")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("AUXLY_HOOK_RC", rc)
	t.Setenv("PATH", "")
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".antigravity"), 0755); err != nil {
		t.Fatal(err)
	}

	wired := autoWireCleanHooks(home)
	got := map[string]bool{}
	for _, a := range wired {
		got[a] = true
	}
	if !got["kimi"] || !got["antigravity"] {
		t.Fatalf("expected kimi and antigravity wired, got %v", wired)
	}
	if got["gemini"] {
		t.Fatalf("gemini has no dir signal and PATH is empty, should not have wired: %v", wired)
	}

	data, err := os.ReadFile(rc)
	if err != nil {
		t.Fatalf("rc file not written: %v", err)
	}
	s := string(data)
	kimiStart, _ := wrapperMarkers("kimi")
	agyStart, _ := wrapperMarkers("antigravity")
	if !strings.Contains(s, kimiStart) {
		t.Fatalf("rc missing kimi wrapper block:\n%s", s)
	}
	if !strings.Contains(s, agyStart) {
		t.Fatalf("rc missing antigravity wrapper block:\n%s", s)
	}
	if !strings.Contains(s, "agy() {") {
		t.Fatalf("antigravity wrapper block should shadow agy, not antigravity:\n%s", s)
	}

	// Second call: both already installed, so idempotent — neither name
	// reappears in the returned slice.
	if again := autoWireCleanHooks(home); len(again) != 0 {
		t.Fatalf("second call not idempotent, wired %v again", again)
	}
}

// TestAutoWire_OptOut: AUXLY_NO_AUTO_HOOKS opts out even when both agents'
// config dirs are present.
func TestAutoWire_OptOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("AUXLY_NO_AUTO_HOOKS", "1")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}

	if wired := autoWireCleanHooks(home); wired != nil {
		t.Fatalf("expected nil with AUXLY_NO_AUTO_HOOKS=1, got %v", wired)
	}
}

// TestAutoWire_RespectsExplicitOptOut: a deliberate uninstall must stick — once
// recorded, auto-wire skips that agent on later setup/connect runs, and an
// explicit re-install clears the opt-out.
func TestAutoWire_RespectsExplicitOptOut(t *testing.T) {
	home := t.TempDir()
	// antigravity present via its config dir; neutralize PATH so a real binary
	// on the test box can't interfere.
	t.Setenv("PATH", "")
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".antigravity"), 0755); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	t.Setenv("AUXLY_HOOK_RC", rc)

	// Record an explicit opt-out, as `hooks uninstall --agent antigravity` would.
	recordAutoHookOptOut(home, "antigravity")

	wired := autoWireCleanHooks(home)
	for _, a := range wired {
		if a == "antigravity" {
			t.Fatalf("opted-out antigravity must NOT be auto-wired, got %v", wired)
		}
	}
	if data, _ := os.ReadFile(rc); strings.Contains(string(data), "auxly capture (antigravity)") {
		t.Fatal("opted-out antigravity wrapper must not be written to the rc")
	}

	// Explicit re-install clears the opt-out → auto-wire wires it again.
	clearAutoHookOptOut(home, "antigravity")
	if autoHookOptedOut(home, "antigravity") {
		t.Fatal("clearAutoHookOptOut should have removed the entry")
	}
	wired = autoWireCleanHooks(home)
	found := false
	for _, a := range wired {
		if a == "antigravity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("after clearing opt-out, antigravity must auto-wire, got %v", wired)
	}
}
