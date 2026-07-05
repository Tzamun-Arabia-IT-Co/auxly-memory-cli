package cmd

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected, returning what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestShellWrapperInstallUninstall locks the same merge discipline hooks.go
// uses for settings.json, but for a plain-text rc file: existing content
// preserved, idempotent re-install, byte-identical restore on uninstall.
func TestShellWrapperInstallUninstall(t *testing.T) {
	rc := filepath.Join(t.TempDir(), "rc")
	t.Setenv("AUXLY_HOOK_RC", rc)
	existing := "# my existing rc\nalias ll='ls -la'\n"
	if err := os.WriteFile(rc, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	if err := installShellWrapper("gemini"); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, _ := os.ReadFile(rc)
	s := string(data)
	if !strings.Contains(s, existing) {
		t.Fatalf("existing rc content damaged:\n%s", s)
	}
	start, end := wrapperMarkers("gemini")
	if !strings.Contains(s, start) || !strings.Contains(s, end) {
		t.Fatalf("wrapper block missing:\n%s", s)
	}
	if !strings.Contains(s, "auxly capture --transcript") {
		t.Fatalf("wrapper doesn't route through auxly capture:\n%s", s)
	}
	installed, err := shellWrapperInstalled("gemini")
	if err != nil || !installed {
		t.Fatalf("shellWrapperInstalled = %v, %v", installed, err)
	}

	// Idempotent: second install must not touch the file at all.
	before := s
	if err := installShellWrapper("gemini"); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	after, _ := os.ReadFile(rc)
	if string(after) != before {
		t.Fatalf("double-install not idempotent:\nbefore:\n%s\nafter:\n%s", before, string(after))
	}

	// Uninstall restores the file byte-identical to before install.
	removed, err := uninstallShellWrapper("gemini")
	if err != nil || !removed {
		t.Fatalf("uninstall: removed=%v err=%v", removed, err)
	}
	final, _ := os.ReadFile(rc)
	if string(final) != existing {
		t.Fatalf("uninstall left rc file altered:\ngot:  %q\nwant: %q", string(final), existing)
	}

	// Uninstalling again is a clean no-op, not an error.
	removed, err = uninstallShellWrapper("gemini")
	if err != nil || removed {
		t.Fatalf("second uninstall: removed=%v err=%v", removed, err)
	}
}

// TestShellWrapperInstallFreshRC covers the no-existing-file case: install
// creates the rc file with just the block, uninstall empties it back out.
func TestShellWrapperInstallFreshRC(t *testing.T) {
	rc := filepath.Join(t.TempDir(), "new-rc")
	t.Setenv("AUXLY_HOOK_RC", rc)

	if err := installShellWrapper("kimi"); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(rc)
	if err != nil {
		t.Fatalf("rc file not created: %v", err)
	}
	start, _ := wrapperMarkers("kimi")
	if !strings.HasPrefix(string(data), start) {
		t.Fatalf("fresh rc should start with the marker, got:\n%s", data)
	}

	if _, err := uninstallShellWrapper("kimi"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	data, _ = os.ReadFile(rc)
	if len(data) != 0 {
		t.Fatalf("uninstall of a wrapper-only rc file should leave it empty, got:\n%s", data)
	}
}

// TestInstallShellWrapperRefusesUnreadableRC: an rc "file" that's actually a
// directory can never be safely appended to — install must refuse, not
// silently no-op or panic.
func TestInstallShellWrapperRefusesUnreadableRC(t *testing.T) {
	rcAsDir := filepath.Join(t.TempDir(), "rcdir")
	if err := os.Mkdir(rcAsDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_HOOK_RC", rcAsDir)
	if err := installShellWrapper("gemini"); err == nil {
		t.Fatal("expected an error for an unreadable rc path, got nil")
	}
}

// TestShellWrapperBlockPerGOOS locks both `script` argv variants: each names
// its platform and routes through the same capture invocation.
func TestShellWrapperBlockPerGOOS(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		block := shellWrapperBlock("kimi", goos)
		start, end := wrapperMarkers("kimi")
		if !strings.HasPrefix(block, start) {
			t.Fatalf("%s: block doesn't start with marker:\n%s", goos, block)
		}
		if !strings.Contains(block, end) {
			t.Fatalf("%s: block missing end marker:\n%s", goos, block)
		}
		if !strings.Contains(block, "auxly capture --transcript") || !strings.Contains(block, "--provider kimi") {
			t.Fatalf("%s: block doesn't route through capture --transcript:\n%s", goos, block)
		}
		if !strings.Contains(block, "script -q") {
			t.Fatalf("%s: block doesn't invoke script(1):\n%s", goos, block)
		}
	}
	// The two variants must actually differ (macOS vs Linux argv shape).
	if shellWrapperBlock("kimi", "darwin") == shellWrapperBlock("kimi", "linux") {
		t.Fatal("darwin and linux wrapper blocks should differ in the script invocation")
	}
}

// TestShellWrapperBlockLinuxQuotesArgsForRescript locks finding 2: the Linux
// `script -c` re-parse must re-quote each argument with printf %q rather
// than collapse them through a bare $* — otherwise `gemini "fix this; also
// X"` would execute `also X` as a second command (and backticks/$() in an
// argument would run arbitrary code) once script hands the string to a shell
// a second time.
func TestShellWrapperBlockLinuxQuotesArgsForRescript(t *testing.T) {
	block := shellWrapperBlock("gemini", "linux")
	if !strings.Contains(block, "printf '%q") {
		t.Fatalf("linux block must re-quote args with printf %%q before the -c re-parse:\n%s", block)
	}
	if strings.Contains(block, "$*") {
		t.Fatalf("linux block must not use bare $* (unsafe re-parse through a second shell):\n%s", block)
	}
}

// TestShellWrapperBlockNoFallbackReRun locks finding 7: no `|| command
// {{AGENT}}` after the script invocation — script propagates the wrapped
// command's exit status, so a fallback there would re-run (double-execute)
// the agent on ANY nonzero exit, including a deliberate failure from a
// destructive/paid operation. Instead, script(1)'s availability is checked
// upfront, once.
func TestShellWrapperBlockNoFallbackReRun(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		block := shellWrapperBlock("gemini", goos)
		if strings.Contains(block, "|| command") {
			t.Fatalf("%s: block re-runs the agent on any nonzero script exit via '|| command':\n%s", goos, block)
		}
		if !strings.Contains(block, "command -v script") {
			t.Fatalf("%s: block missing the upfront script(1) availability check:\n%s", goos, block)
		}
	}
}

// TestShellWrapperBlockPassesBashSyntaxCheck renders both GOOS variants and
// runs them through `bash -n` — a real syntax check that the printf %q
// re-quoting (finding 2) survives both the Go string literal AND actual
// shell parsing, not just "looks right" by eye.
func TestShellWrapperBlockPassesBashSyntaxCheck(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for _, goos := range []string{"darwin", "linux"} {
		block := shellWrapperBlock("kimi", goos)
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(block)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: rendered wrapper block failed `bash -n`: %v\n%s\nblock:\n%s", goos, err, out, block)
		}
	}
}

// TestShellWrapperBlockAntigravityShadowsAgy locks the CMD/AGENT split:
// antigravity's CLI binary is `agy`, not `antigravity`, so the wrapper
// function must shadow `agy` and wrap `command agy`, while the markers,
// mktemp label, and --provider attribution stay labeled "antigravity".
func TestShellWrapperBlockAntigravityShadowsAgy(t *testing.T) {
	block := shellWrapperBlock("antigravity", "darwin")
	if !strings.Contains(block, "agy() {") {
		t.Fatalf("antigravity block must define an agy() function (shadow the agy command):\n%s", block)
	}
	if !strings.Contains(block, "command agy \"$@\"") {
		t.Fatalf("antigravity block must wrap `command agy \"$@\"`:\n%s", block)
	}
	if !strings.Contains(block, "--provider antigravity") {
		t.Fatalf("antigravity block must attribute captures to --provider antigravity:\n%s", block)
	}
	start, end := wrapperMarkers("antigravity")
	if !strings.Contains(block, start) || !strings.Contains(block, end) {
		t.Fatalf("antigravity block must use antigravity-labeled markers:\n%s", block)
	}
	if strings.Contains(block, "antigravity()") {
		t.Fatalf("antigravity block must NOT define an antigravity() function (agy is the real command):\n%s", block)
	}

	// gemini must be unaffected by the CMD/AGENT split: function name, wrapped
	// command, and --provider all still read "gemini".
	geminiBlock := shellWrapperBlock("gemini", "darwin")
	if !strings.Contains(geminiBlock, "gemini() {") || !strings.Contains(geminiBlock, "--provider gemini") {
		t.Fatalf("gemini block regressed after CMD/AGENT split:\n%s", geminiBlock)
	}
}

// TestShellWrapperBlockAntigravityLinux covers the non-darwin util-linux
// `script -q -e -c` form: it must still wrap `command agy`, not `command
// antigravity`.
func TestShellWrapperBlockAntigravityLinux(t *testing.T) {
	block := shellWrapperBlock("antigravity", "linux")
	if !strings.Contains(block, `command agy $(printf '%q ' "$@")`) {
		t.Fatalf("linux antigravity block must wrap `command agy` via the util-linux script -c form:\n%s", block)
	}
	if !strings.Contains(block, "script -q -e -c") {
		t.Fatalf("linux antigravity block missing the util-linux script invocation:\n%s", block)
	}
}

// TestWrapperCommand locks the name mapping wrapperCommand implements:
// antigravity -> agy, everything else -> itself.
func TestWrapperCommand(t *testing.T) {
	cases := map[string]string{
		"antigravity": "agy",
		"gemini":      "gemini",
		"kimi":        "kimi",
	}
	for agent, want := range cases {
		if got := wrapperCommand(agent); got != want {
			t.Fatalf("wrapperCommand(%q) = %q, want %q", agent, got, want)
		}
	}
}

// TestShellRCPathUsesSHELLOverGOOS locks finding 8: $SHELL's basename picks
// the rc file, not GOOS — a Linux zsh user must get ~/.zshrc, not ~/.bashrc
// (which their zsh session never sources).
func TestShellRCPathUsesSHELLOverGOOS(t *testing.T) {
	t.Setenv("AUXLY_HOOK_RC", "") // don't let the override win
	for shell, want := range map[string]string{
		"/usr/bin/zsh": ".zshrc",
		"/bin/bash":    ".bashrc",
	} {
		t.Setenv("SHELL", shell)
		path, err := shellRCPath()
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(path) != want {
			t.Fatalf("SHELL=%s should resolve to %s regardless of GOOS, got %s", shell, want, path)
		}
	}
}

// TestPrintWrapperCmd covers `auxly hooks print-wrapper <agent>`: known
// agents print a block with both markers, unknown agents error.
func TestPrintWrapperCmd(t *testing.T) {
	out := captureStdout(t, func() {
		if err := hooksPrintWrapperCmd.RunE(hooksPrintWrapperCmd, []string{"kimi"}); err != nil {
			t.Fatal(err)
		}
	})
	start, end := wrapperMarkers("kimi")
	if !strings.Contains(out, start) || !strings.Contains(out, end) {
		t.Fatalf("print-wrapper output missing markers:\n%s", out)
	}

	if err := hooksPrintWrapperCmd.RunE(hooksPrintWrapperCmd, []string{"claude"}); err == nil {
		t.Fatal("expected an error for an agent with no shell wrapper")
	}
}

// TestInstallUninstallHookForAgentUnsupported locks the router's error path:
// an unknown --agent must fail loudly and name the supported ones.
func TestInstallUninstallHookForAgentUnsupported(t *testing.T) {
	if err := installHookForAgent("copilot"); err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Fatalf("installHookForAgent(copilot) = %v, want an error listing supported agents", err)
	}
	if err := uninstallHookForAgent("copilot"); err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Fatalf("uninstallHookForAgent(copilot) = %v, want an error listing supported agents", err)
	}
}

// TestCaptureInputPlainTextTranscriptRoutesThroughPipeline locks the honest
// gemini/kimi wrapper contract: a `script`-captured plain-text session (no
// JSON anywhere in it) is NOT dropped by readTranscriptJSONL — the whole file
// becomes the session text, same as it would for --stop-hook's JSONL path,
// and is long enough to clear captureMinChars.
//
// This stops short of invoking runCapture end-to-end: that would reach
// memory.ExtractCaptureFacts and make a real HTTP call to whatever LLM
// endpoint happens to be configured/reachable on the machine running the
// test (a real API key in the env, a local Ollama on 11434, ...) — exactly
// the seam the task flagged as unsafe to exercise directly. Asserting
// reachability up to that boundary (the shared captureInput() both
// --stop-hook and --transcript funnel through) covers the routing without
// that flakiness; runCapture's own fire-and-forget contract (LLM error ->
// stderr + exit 0) is unit-testable independently once ExtractCaptureFacts
// gets a seam of its own.
func TestCaptureInputPlainTextTranscriptRoutesThroughPipeline(t *testing.T) {
	dir := t.TempDir()
	txPath := filepath.Join(dir, "session.log")
	body := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 300)
	if err := os.WriteFile(txPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if len(body) < captureMinChars {
		t.Fatalf("test fixture too small: %d < %d", len(body), captureMinChars)
	}

	oldTranscript, oldStopHook := captureTranscript, captureStopHook
	t.Cleanup(func() { captureTranscript, captureStopHook = oldTranscript, oldStopHook })
	captureTranscript = txPath
	captureStopHook = false

	got, err := captureInput()
	if err != nil {
		t.Fatalf("captureInput: %v", err)
	}
	if got != body {
		t.Fatalf("plain-text transcript should pass through whole, got %d chars, want %d", len(got), len(body))
	}
	if len(got) < captureMinChars {
		t.Fatalf("routed text too short to clear captureMinChars: %d", len(got))
	}
}
