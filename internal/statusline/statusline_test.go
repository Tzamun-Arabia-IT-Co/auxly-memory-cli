package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThresholdBarAndColors(t *testing.T) {
	cases := []struct {
		pct   int
		color string
	}{
		{10, cGreen}, {49, cGreen}, {50, cAmber}, {79, cAmber}, {80, cRed}, {100, cRed},
	}
	for _, c := range cases {
		if got := levelColor(c.pct); got != c.color {
			t.Errorf("levelColor(%d) wrong color", c.pct)
		}
		bar := thresholdBar(c.pct)
		if !strings.Contains(bar, "▰") && c.pct > 4 {
			t.Errorf("bar for %d%% should have filled cells: %q", c.pct, bar)
		}
		if !strings.HasPrefix(bar, c.color) {
			t.Errorf("bar for %d%% should start with its level color", c.pct)
		}
	}
	// Unknown (negative) renders an all-empty dim bar.
	if b := thresholdBar(-1); !strings.HasPrefix(b, cDim) || strings.Contains(b, "▰") {
		t.Errorf("unknown pct should be an empty dim bar, got %q", b)
	}
}

func TestFmtTokensAndCtx(t *testing.T) {
	if fmtTokens(0) != "?" || fmtTokens(950) != "950" || fmtTokens(107000) != "107.0k" {
		t.Error("fmtTokens wrong")
	}
	if fmtCtx(0) != "?" || fmtCtx(1000000) != "1000k" || fmtCtx(200000) != "200k" {
		t.Error("fmtCtx wrong")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"":                                ModeNone,
		"bash /Users/x/.claude/sl.sh":     ModeOther,
		"/usr/local/bin/auxly statusline": ModeFull,
		"auxly statusline --wrap":         ModeWrap,
	}
	for cmd, want := range cases {
		if got := classify(cmd); got != want {
			t.Errorf("classify(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestDetectRole(t *testing.T) {
	dir := t.TempDir()
	// No host.yaml / remotes.yaml → local.
	if role, remote := detectRole(dir); role != "local" || remote {
		t.Errorf("empty dir should be local, got %q remote=%v", role, remote)
	}
	// host.yaml present → local host.
	os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("listen: x\n"), 0o644)
	if role, remote := detectRole(dir); role != "local" || remote {
		t.Errorf("host.yaml should be local, got %q", role)
	}
	os.Remove(filepath.Join(dir, "host.yaml"))
	// remotes.yaml with a named host → remote→name.
	os.WriteFile(filepath.Join(dir, "remotes.yaml"), []byte("name: prod-box\nhost: root@10.0.0.5:22\n"), 0o644)
	if role, remote := detectRole(dir); role != "remote→prod-box" || !remote {
		t.Errorf("remotes.yaml should be remote→prod-box, got %q remote=%v", role, remote)
	}
}

// TestInstallUninstallRoundTrip verifies the additive + reversible install: a prior
// non-Auxly command is backed up and restored verbatim on uninstall.
func TestInstallUninstallRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	orig := `bash /Users/lab/.claude/statusline.sh`
	seed := map[string]any{
		"statusLine": map[string]any{"type": "command", "command": orig},
		"model":      "opusplan",
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644)

	// Install (wrap) → command becomes auxly, original backed up, other keys kept.
	if err := Install(true); err != nil {
		t.Fatalf("install: %v", err)
	}
	st := CurrentState()
	if st.Mode != ModeWrap {
		t.Errorf("after wrap install, mode = %q, want wrap", st.Mode)
	}
	if OriginalCommand() != orig {
		t.Errorf("original not backed up; got %q", OriginalCommand())
	}
	// The unrelated "model" key must be preserved.
	m, _ := readSettings()
	if m["model"] != "opusplan" {
		t.Error("install clobbered an unrelated settings key")
	}

	// Re-install as full must NOT overwrite the real backup with the auxly command.
	if err := Install(false); err != nil {
		t.Fatalf("reinstall full: %v", err)
	}
	if OriginalCommand() != orig {
		t.Errorf("switching modes clobbered the backup; got %q", OriginalCommand())
	}
	if CurrentState().Mode != ModeFull {
		t.Error("re-install full did not set full mode")
	}

	// Uninstall restores the original verbatim and clears the backup.
	if err := Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if currentCommand() != orig {
		t.Errorf("uninstall did not restore original; got %q", currentCommand())
	}
	if OriginalCommand() != "" {
		t.Error("backup should be cleared after restore")
	}
}
