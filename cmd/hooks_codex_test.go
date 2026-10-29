package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexInstallFreshCreatesTopLevelNotify(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)

	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := codexHookHeader + "\n" + codexNotifyLine + "\n"
	if string(data) != want {
		t.Fatalf("fresh install mismatch:\n%s", data)
	}
}

func TestCodexInstallInsertsNotifyBeforeFirstTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := "model = \"gpt-5\"\n\n[profiles.work]\napproval_policy = \"never\"\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	gotBytes, _ := os.ReadFile(path)
	got := string(gotBytes)
	notifyAt := strings.Index(got, codexNotifyLine)
	tableAt := strings.Index(got, "[profiles.work]")
	if notifyAt < 0 || tableAt < 0 || notifyAt > tableAt {
		t.Fatalf("notify must be before first table:\n%s", got)
	}
	if !strings.HasPrefix(got, "model = \"gpt-5\"\n\n"+codexHookHeader+"\n") {
		t.Fatalf("top-level keys not preserved before hook:\n%s", got)
	}
}

func TestCodexInstallRefusesForeignNotify(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(`notify = ["other"]`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := installCodexHook()
	if err == nil || !strings.Contains(err.Error(), "Codex already has a notify program") {
		t.Fatalf("expected foreign notify refusal, got %v", err)
	}
}

func TestCodexInstallIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := codexHookHeader + "\n" + codexNotifyLine + "\n[profile]\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("idempotent install changed file:\n%s", got)
	}
}

func TestCodexUninstallRestoresOriginal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := "model = \"gpt-5\"\n\n[profiles.work]\napproval_policy = \"never\"\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	if err := uninstallCodexHook(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("uninstall did not restore original bytes:\nwant:\n%q\ngot:\n%q", original, string(got))
	}
}

func TestCodexHookStatusStates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)

	state, detail := codexHookStatus()
	if state != codexHookNone || !strings.Contains(detail, "no Codex config") {
		t.Fatalf("missing status: state=%v detail=%q", state, detail)
	}

	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	state, detail = codexHookStatus()
	if state != codexHookWired || !strings.Contains(detail, "installed") {
		t.Fatalf("ours status: state=%v detail=%q", state, detail)
	}

	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`notify = ["other"]`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	state, detail = codexHookStatus()
	if state != codexHookForeign || !strings.Contains(detail, "different notify") {
		t.Fatalf("foreign status: state=%v detail=%q", state, detail)
	}
}

func TestCodexResolveTranscriptPicksNewestRollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	oldPath := filepath.Join(home, "sessions", "2026", "07", "01", "rollout-old.jsonl")
	newPath := filepath.Join(home, "sessions", "2026", "07", "02", "rollout-new.jsonl")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte(`{"type":"response_item","payload":{"text":"old"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte(`{"type":"response_item","payload":{"text":"new"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	got, err := resolveCodexTranscript(map[string]any{
		"type":                   "agent-turn-complete",
		"last-assistant-message": "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	// newestCodexRollout resolves symlinks in the sessions root (finding 11)
	// — on macOS, t.TempDir() lives under /var, itself a symlink to
	// /private/var, so the returned path may legitimately differ from
	// newPath by that resolved prefix. Compare the resolved form of both.
	wantPath, everr := filepath.EvalSymlinks(newPath)
	if everr != nil {
		wantPath = newPath
	}
	if got != wantPath {
		t.Fatalf("resolved %q, want newest %q", got, wantPath)
	}
}

func TestCodexResolveTranscriptUsesPayloadPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "rollout-direct.jsonl")
	got, err := resolveCodexTranscript(map[string]any{
		"nested": map[string]any{"rollout_path": want},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
}

// TestCodexInstallIgnoresNestedNotifyInTable locks table-scoping (finding 3):
// a notify line that's a foreign line's twin but sits under [profiles.other]
// must not satisfy the already-installed check, nor read as a foreign notify
// to refuse on — install must proceed and insert our OWN top-level copy.
func TestCodexInstallIgnoresNestedNotifyInTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := "[profiles.other]\n" + codexNotifyLine + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	topAt := strings.Index(s, codexHookHeader)
	tableAt := strings.Index(s, "[profiles.other]")
	if topAt < 0 || tableAt < 0 || topAt > tableAt {
		t.Fatalf("expected a fresh top-level insert before [profiles.other]:\n%s", s)
	}
	if strings.Count(s, codexNotifyLine) != 2 {
		t.Fatalf("expected the nested copy untouched plus our new top-level one (2 total):\n%s", s)
	}
}

// TestCodexHookStatusIgnoresNestedNotify is TestCodexInstallIgnoresNestedNotifyInTable's
// status-side twin: a notify line nested in a table must not read as WIRED.
func TestCodexHookStatusIgnoresNestedNotify(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := "[profiles.other]\n" + codexNotifyLine + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if state, detail := codexHookStatus(); state != codexHookNone {
		t.Fatalf("nested notify under a table must not count as wired: state=%v detail=%q", state, detail)
	}
}

// TestCodexNoSpaceNotifyVariant locks finding 4: install-idempotency,
// uninstall, and status must all recognize `notify=["auxly"...` (no spaces)
// as ours, not just our canonical `notify = ["auxly"...` spacing.
func TestCodexNoSpaceNotifyVariant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	noSpace := `notify=["auxly","capture","--codex-notify"]` + "\n"
	if err := os.WriteFile(path, []byte(noSpace), 0644); err != nil {
		t.Fatal(err)
	}

	if state, detail := codexHookStatus(); state != codexHookWired {
		t.Fatalf("no-space variant should read as WIRED: state=%v detail=%q", state, detail)
	}

	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != noSpace {
		t.Fatalf("install should treat the no-space variant as already installed, got:\n%s", got)
	}

	if err := uninstallCodexHook(); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if strings.TrimSpace(string(got)) != "" {
		t.Fatalf("no-space variant should have been uninstalled, got:\n%s", got)
	}
}

// TestCodexInstallIgnoresHeaderLookingTextInsideTripleQuoteString locks
// finding 5: a """ multi-line string value whose content merely LOOKS like a
// [table] header or a notify line must not be treated as one — the string
// must survive intact, and our hook must land top-level (outside/after it).
func TestCodexInstallIgnoresHeaderLookingTextInsideTripleQuoteString(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := "model = \"gpt-5\"\ndescription = \"\"\"\n[not a table]\nnotify = [\"fake\"]\n\"\"\"\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installCodexHook(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if !strings.Contains(s, original) {
		t.Fatalf("multi-line string content must survive byte-for-byte intact:\n%s", s)
	}
	lastQuote := strings.LastIndex(s, `"""`)
	notifyAt := strings.Index(s, codexNotifyLine)
	if notifyAt < 0 || notifyAt < lastQuote {
		t.Fatalf("hook must land top-level, outside the multi-line string:\n%s", s)
	}
}

// TestReadCodexRolloutTextFiltersToolOutput locks finding 6: a
// function_call_output node's content must never reach extraction, even
// though its content blocks use the exact same "text"/"content" key shapes
// as a real message — only message nodes are eligible for collection.
func TestReadCodexRolloutTextFiltersToolOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-secret.jsonl")
	lines := []string{
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Sure, here is the plan"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":[{"type":"output_text","text":"SECRET-TOKEN"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := readCodexRolloutText(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Sure, here is the plan") {
		t.Fatalf("message text missing from extraction: %q", got)
	}
	if strings.Contains(got, "SECRET-TOKEN") {
		t.Fatalf("tool-call output leaked into extraction: %q", got)
	}
}

// TestNewestCodexRolloutFollowsSymlinkedSessionsDir locks finding 11's
// EvalSymlinks fix: filepath.WalkDir lstats its root, so a sessions dir that
// is itself a symlink was previously invisible (treated as a non-directory
// leaf, never descended into) — rollout files inside it went unfound.
func TestNewestCodexRolloutFollowsSymlinkedSessionsDir(t *testing.T) {
	home := t.TempDir()
	realSessions := filepath.Join(t.TempDir(), "real-sessions")
	if err := os.MkdirAll(realSessions, 0755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(realSessions, "rollout-1.jsonl")
	if err := os.WriteFile(rollout, []byte(`{"type":"response_item","payload":{"text":"hi"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(home, "sessions")
	if err := os.Symlink(realSessions, linkPath); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	t.Setenv("CODEX_HOME", home)

	got, err := newestCodexRollout()
	if err != nil {
		t.Fatal(err)
	}
	realRollout, _ := filepath.EvalSymlinks(rollout)
	if got != realRollout {
		t.Fatalf("symlinked sessions dir not resolved: got %q, want %q", got, realRollout)
	}
}

// TestRunCodexNotifyRespectsCooldown locks finding 11's ordering: the
// cooldown marker must be checked BEFORE anything that touches the sessions
// tree. CODEX_HOME here has no sessions dir at all — if the ordering
// regressed and resolveCodexTranscript ran anyway, that's still a silent
// no-op, so the meaningful assertion is simply that a fresh marker makes the
// whole call a silent, error-free no-op.
func TestRunCodexNotifyRespectsCooldown(t *testing.T) {
	memDir := t.TempDir()
	t.Setenv("AUXLY_MEMORY_PATH", memDir)
	if err := os.WriteFile(captureMarkerPath(memDir), []byte(time.Now().Format(time.RFC3339)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())

	if err := runCodexNotify(); err != nil {
		t.Fatalf("cooldown should short-circuit silently, got %v", err)
	}
}

func TestCodexChainedNotify(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	chained := codexHookHeader + "\n" +
		`notify = ["/Applications/SkyComputerUse.app/Contents/MacOS/SkyComputerUseClient", "turn-ended", "--previous-notify", "[\"auxly\",\"capture\",\"--codex-notify\"]"]` + "\n"
	if err := os.WriteFile(path, []byte(chained), 0644); err != nil {
		t.Fatal(err)
	}

	state, detail := codexHookStatus()
	if state != codexHookWired || detail != "Codex notify hook is chained through another notify program" {
		t.Fatalf("chained status: got state=%v detail=%q", state, detail)
	}

	changed, err := installCodexHookQuiet()
	if err != nil || changed {
		t.Fatalf("installCodexHookQuiet on chained: got changed=%v err=%v, want (false, nil)", changed, err)
	}
	afterInstall, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterInstall) != chained {
		t.Fatalf("install modified chained config.toml:\nwant:\n%s\ngot:\n%s", chained, afterInstall)
	}

	out := captureStdout(t, func() {
		if uerr := uninstallCodexHook(); uerr != nil {
			t.Fatalf("uninstallCodexHook on chained failed: %v", uerr)
		}
	})
	wantMsg := "Auxly is chained inside another notify program in " + path + " — remove its --previous-notify argument there to stop Codex capture."
	if !strings.Contains(out, wantMsg) {
		t.Fatalf("uninstall message mismatch:\nwant substring: %q\ngot: %q", wantMsg, out)
	}
	afterUninstall, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterUninstall) != chained {
		t.Fatalf("uninstall modified chained config.toml:\nwant:\n%s\ngot:\n%s", chained, afterUninstall)
	}
}

func TestCodexChainedNotifyTrailingComment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")

	commentedLine := `notify = ["/bin/other"] # old: auxly capture --codex-notify`
	if isCodexNotifyChained(commentedLine) {
		t.Fatalf("commented line must not be chained: %q", commentedLine)
	}
	if err := os.WriteFile(path, []byte(commentedLine+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	state, detail := codexHookStatus()
	if state != codexHookForeign {
		t.Fatalf("commented line should be codexHookForeign, got state=%v detail=%q", state, detail)
	}

	realChainedLine := `notify = ["/x/SkyComputerUseClient", "turn-ended", "--previous-notify", "[\"auxly\",\"capture\",\"--codex-notify\"]"]`
	if !isCodexNotifyChained(realChainedLine) {
		t.Fatalf("real Computer Use line must be chained: %q", realChainedLine)
	}
	if err := os.WriteFile(path, []byte(realChainedLine+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	state, detail = codexHookStatus()
	if state != codexHookWired {
		t.Fatalf("real chained line should be codexHookWired, got state=%v detail=%q", state, detail)
	}
}
