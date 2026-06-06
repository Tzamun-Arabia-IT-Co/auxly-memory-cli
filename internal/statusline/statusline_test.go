package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestGitStatsSegment(t *testing.T) {
	// Not a repo (no commit) → empty.
	if s := gitStatsSegment(gitStats{}); s != "" {
		t.Errorf("no commit should yield empty segment, got %q", s)
	}
	// Clean tree → only the commit hash, no dirty counts.
	clean := stripANSIcodes(gitStatsSegment(gitStats{commit: "a1b2c3d"}))
	if !strings.Contains(clean, "a1b2c3d") {
		t.Errorf("clean segment should show the commit hash, got %q", clean)
	}
	if strings.ContainsAny(clean, "+-"+glyphFile) {
		t.Errorf("clean tree should show no dirty counts, got %q", clean)
	}
	// Dirty tree → files changed + added/removed + commit, all present and ordered.
	dirty := stripANSIcodes(gitStatsSegment(gitStats{commit: "deadbee", changed: 23, added: 1067, removed: 55}))
	for _, want := range []string{glyphFile + " 23", "+1067", "-55", "deadbee"} {
		if !strings.Contains(dirty, want) {
			t.Errorf("dirty segment missing %q, got %q", want, dirty)
		}
	}
	// A removal-free change must not render a "-0".
	noDel := stripANSIcodes(gitStatsSegment(gitStats{commit: "abc1234", changed: 1, added: 5}))
	if strings.Contains(noDel, "-0") {
		t.Errorf("zero removals should be omitted, got %q", noDel)
	}
	// Ahead/behind upstream + commit age all render; zero ahead/behind are omitted.
	full := stripANSIcodes(gitStatsSegment(gitStats{commit: "abc1234", commitAge: "2h", ahead: 2, behind: 1, changed: 3, added: 9}))
	for _, want := range []string{"↑2", "↓1", "· 2h"} {
		if !strings.Contains(full, want) {
			t.Errorf("full segment missing %q, got %q", want, full)
		}
	}
	noSync := stripANSIcodes(gitStatsSegment(gitStats{commit: "abc1234"}))
	if strings.ContainsAny(noSync, "↑↓") {
		t.Errorf("in-sync repo should show no ↑/↓, got %q", noSync)
	}
	if strings.Contains(noSync, "·") {
		t.Errorf("missing commit age should omit the '·' separator, got %q", noSync)
	}
}

func TestShortAgo(t *testing.T) {
	if shortAgo(0) != "" || shortAgo(-5) != "" {
		t.Error("non-positive timestamp should be empty")
	}
	now := time.Now()
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{3 * 24 * time.Hour, "3d"},
		{3 * 7 * 24 * time.Hour, "3w"},
	}
	for _, c := range cases {
		if got := shortAgo(now.Add(-c.ago).Unix()); got != c.want {
			t.Errorf("shortAgo(%s ago) = %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestCountAddedLines(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name, content string
		want          int
	}{
		{"trailing.txt", "a\nb\nc\n", 3}, // 3 newline-terminated lines
		{"notrail.txt", "a\nb\nc", 3},    // final line has no newline → still counted
		{"empty.txt", "", 0},             // empty file → 0
		{"oneline.txt", "solo", 1},       // single line, no newline
		{"binary.bin", "a\x00b\nc\n", 0}, // NUL in first chunk → binary → 0 like git
	}
	for _, c := range cases {
		p := write(c.name, c.content)
		if got := countAddedLines(p); got != c.want {
			t.Errorf("countAddedLines(%q) = %d, want %d", c.name, got, c.want)
		}
	}
	// Missing file → 0, never panics.
	if got := countAddedLines(filepath.Join(dir, "nope")); got != 0 {
		t.Errorf("missing file should count 0, got %d", got)
	}
}

// stripANSIcodes removes SGR escape sequences so a test can assert on visible text.
func stripANSIcodes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
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
	// remotes.yaml in the REAL list form `saveRemotes` writes (a YAML sequence: the
	// first key sits on the `- name:` line). A relay box's host is the tunnel
	// endpoint localhost, so the role must come from the NAME — this is the case that
	// regressed to "local" before yamlScalar learned to strip the sequence dash.
	os.WriteFile(filepath.Join(dir, "remotes.yaml"),
		[]byte("remotes:\n    - name: HLab-Mac-mini\n      method: rendezvous\n      user: lab\n      host: localhost\n      port: 2222\n"), 0o644)
	if role, remote := detectRole(dir); role != "remote→HLab-Mac-mini" || !remote {
		t.Errorf("relay box should be remote→HLab-Mac-mini, got %q remote=%v", role, remote)
	}
}

// TestInstallUninstallRoundTrip verifies the additive + reversible install: a prior
// non-Auxly command is backed up and restored verbatim on uninstall.
func TestInstallUninstallRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	orig := `bash ~/.claude/statusline.sh`
	seed := map[string]any{
		"statusLine": map[string]any{"type": "command", "command": orig},
		"model":      "opusplan",
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644)

	// Install (wrap) → command becomes auxly, original backed up, other keys kept.
	if err := Install(ProviderClaude, true); err != nil {
		t.Fatalf("install: %v", err)
	}
	st := CurrentState(ProviderClaude)
	if st.Mode != ModeWrap {
		t.Errorf("after wrap install, mode = %q, want wrap", st.Mode)
	}
	if OriginalCommand(ProviderClaude) != orig {
		t.Errorf("original not backed up; got %q", OriginalCommand(ProviderClaude))
	}
	// The unrelated "model" key must be preserved.
	m, _ := readSettings(filepath.Join(claudeDir, "settings.json"))
	if m["model"] != "opusplan" {
		t.Error("install clobbered an unrelated settings key")
	}

	// Re-install as full must NOT overwrite the real backup with the auxly command.
	if err := Install(ProviderClaude, false); err != nil {
		t.Fatalf("reinstall full: %v", err)
	}
	if OriginalCommand(ProviderClaude) != orig {
		t.Errorf("switching modes clobbered the backup; got %q", OriginalCommand(ProviderClaude))
	}
	if CurrentState(ProviderClaude).Mode != ModeFull {
		t.Error("re-install full did not set full mode")
	}

	// Uninstall restores the original verbatim and clears the backup.
	if err := Uninstall(ProviderClaude); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := CurrentState(ProviderClaude).Command; got != orig {
		t.Errorf("uninstall did not restore original; got %q", got)
	}
	if OriginalCommand(ProviderClaude) != "" {
		t.Error("backup should be cleared after restore")
	}
}
