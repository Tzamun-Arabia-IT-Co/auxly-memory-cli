package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallCursorPreservesExtras verifies that wiring Cursor preserves its statusLine
// extras (padding/updateIntervalMs/timeoutMs) and every unrelated cli-config.json key,
// backs up the user's hand-rolled statusline.sh, and restores it verbatim on uninstall.
func TestInstallCursorPreservesExtras(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cursorDir := filepath.Join(home, ".cursor")
	os.MkdirAll(cursorDir, 0o755)
	cfg := filepath.Join(cursorDir, "cli-config.json")
	origCmd := "/Users/x/.cursor/statusline.sh"
	seed, _ := json.MarshalIndent(map[string]any{
		"statusLine": map[string]any{
			"type": "command", "command": origCmd,
			"padding": float64(2), "updateIntervalMs": float64(300), "timeoutMs": float64(2000),
		},
		"model":       map[string]any{"modelId": "composer-2.5"},
		"permissions": map[string]any{"allow": []any{"Shell(ls)"}},
	}, "", "  ")
	os.WriteFile(cfg, seed, 0o644)

	if err := Install(ProviderCursor, false); err != nil {
		t.Fatalf("install cursor: %v", err)
	}
	m, _ := readSettings(cfg)
	sl, _ := m["statusLine"].(map[string]any)
	if cmd, _ := sl["command"].(string); !strings.Contains(cmd, "statusline") || !strings.Contains(cmd, "--provider cursor") {
		t.Errorf("cursor command not wired with --provider cursor: %q", cmd)
	}
	for k, want := range map[string]float64{"padding": 2, "updateIntervalMs": 300, "timeoutMs": 2000} {
		if sl[k] != want {
			t.Errorf("cursor extra %q not preserved: got %v want %v", k, sl[k], want)
		}
	}
	if _, ok := m["model"]; !ok {
		t.Error("unrelated 'model' key dropped from cli-config.json")
	}
	if _, ok := m["permissions"]; !ok {
		t.Error("unrelated 'permissions' key dropped from cli-config.json")
	}
	if OriginalCommand(ProviderCursor) != origCmd {
		t.Errorf("cursor statusline.sh not backed up; got %q", OriginalCommand(ProviderCursor))
	}

	if err := Uninstall(ProviderCursor); err != nil {
		t.Fatalf("uninstall cursor: %v", err)
	}
	if got := CurrentState(ProviderCursor).Command; got != origCmd {
		t.Errorf("uninstall didn't restore cursor's statusline.sh; got %q", got)
	}
}

// TestInstallAntigravityPreservesEnabled verifies the Antigravity target writes to the
// antigravity-cli settings file, preserves its `enabled` flag, and uses --provider.
func TestInstallAntigravityPreservesEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agDir := filepath.Join(home, ".gemini", "antigravity-cli")
	os.MkdirAll(agDir, 0o755)
	cfg := filepath.Join(agDir, "settings.json")
	seed, _ := json.MarshalIndent(map[string]any{
		"statusLine": map[string]any{"type": "command", "command": "bash /x/sl.sh", "enabled": true},
		"model":      map[string]any{"name": "gemini"},
	}, "", "  ")
	os.WriteFile(cfg, seed, 0o644)

	if err := Install(ProviderAntigravity, false); err != nil {
		t.Fatalf("install antigravity: %v", err)
	}
	m, _ := readSettings(cfg)
	sl, _ := m["statusLine"].(map[string]any)
	if sl["enabled"] != true {
		t.Errorf("antigravity 'enabled' flag not preserved: %+v", sl)
	}
	if cmd, _ := sl["command"].(string); !strings.Contains(cmd, "--provider antigravity") {
		t.Errorf("antigravity command not wired with --provider antigravity: %q", cmd)
	}
	if _, ok := m["model"]; !ok {
		t.Error("unrelated 'model' key dropped from antigravity settings.json")
	}
}

// TestAutoInstallMissing verifies the follows-you behavior used by `connect auto`:
// it wires only agents with NO statusline yet (ModeNone), never touches an agent that
// already runs its own (ModeOther), skips not-installed agents, and is idempotent.
func TestAutoInstallMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Claude: dir present, no statusLine → ModeNone (should be wired).
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	// Cursor: dir present with the user's OWN statusline → ModeOther (leave alone).
	cursorDir := filepath.Join(home, ".cursor")
	os.MkdirAll(cursorDir, 0o755)
	seed, _ := json.Marshal(map[string]any{"statusLine": map[string]any{
		"type": "command", "command": "/x/.cursor/statusline.sh",
	}})
	os.WriteFile(filepath.Join(cursorDir, "cli-config.json"), seed, 0o644)
	// Antigravity: dir absent → not available → skipped.

	wired := AutoInstallMissing()
	if len(wired) != 1 || wired[0] != "Claude Code" {
		t.Fatalf("expected only Claude Code wired, got %v", wired)
	}
	if CurrentState(ProviderClaude).Mode != ModeFull {
		t.Error("claude (no statusline) should be ModeFull after auto-install")
	}
	if CurrentState(ProviderCursor).Mode != ModeOther {
		t.Error("cursor's own statusline must be left untouched (ModeOther)")
	}
	if again := AutoInstallMissing(); len(again) != 0 {
		t.Errorf("second AutoInstallMissing must be a no-op, got %v", again)
	}
}

func TestTargetsAndSelfCommand(t *testing.T) {
	c, ok := TargetByName("claude")
	if !ok {
		t.Fatal("claude target should exist")
	}
	// --provider is baked in for EVERY agent (incl. claude) so render is deterministic
	// and never auto-detects (which can misread a Claude payload as Cursor).
	if !strings.Contains(c.selfCommand(false), "--provider claude") {
		t.Errorf("claude command must carry --provider claude for determinism: %q", c.selfCommand(false))
	}
	cur, _ := TargetByName("cursor")
	if !strings.Contains(cur.selfCommand(false), "--provider cursor") {
		t.Errorf("cursor command needs --provider cursor: %q", cur.selfCommand(false))
	}
	if !strings.Contains(cur.selfCommand(true), "--wrap") {
		t.Errorf("wrap flag missing: %q", cur.selfCommand(true))
	}
	if _, ok := TargetByName("bogus"); ok {
		t.Error("unknown agent should not resolve")
	}
	// Empty defaults to claude.
	if tgt, ok := TargetByName(""); !ok || tgt.Name != ProviderClaude {
		t.Errorf("empty name should default to claude, got %q", tgt.Name)
	}
}
