package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCaptureCodexNotifyFlagRoutesToHandler locks finding 1 (CRITICAL):
// --codex-notify must be a registered flag on captureCmd, and setting it
// must route runCapture to the codex handler BEFORE any other mode — not
// fail with cobra's "unknown flag" (the bug: the flag was never registered
// at all, so this exact invocation errored out before doing anything).
//
// The codex handler's own payload parsing reads the raw process os.Args
// (matching how Codex actually invokes `notify = ["auxly","capture",
// "--codex-notify"]`, appending the JSON payload as the final argv element)
// rather than cobra's parsed args — so os.Args is set here to an invalid
// JSON payload, which makes codexNotifyPayload fail deterministically and
// captureCodexNotify print its own distinctive stderr line. That's the
// observable proof this ran the CODEX path (not the default stdin path,
// which would produce no output at all) — entirely before ever reaching an
// LLM (runCodexNotify only calls out after resolving a transcript).
func TestCaptureCodexNotifyFlagRoutesToHandler(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir()) // empty sessions dir — nothing to walk
	t.Setenv("AUXLY_MEMORY_PATH", t.TempDir())

	oldFlag := captureCodexNotifyFlag
	t.Cleanup(func() { captureCodexNotifyFlag = oldFlag })

	oldArgs := os.Args
	os.Args = []string{"auxly", "capture", "--codex-notify", "not-json"}
	t.Cleanup(func() { os.Args = oldArgs })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	captureCmd.SetArgs([]string{"--codex-notify", "{}"})
	t.Cleanup(func() { captureCmd.SetArgs(nil) })
	execErr := captureCmd.Execute()
	w.Close()
	out, _ := io.ReadAll(r)

	if execErr != nil {
		t.Fatalf("--codex-notify must be a registered flag and fire-and-forget (exit 0), got %v", execErr)
	}
	if !captureCodexNotifyFlag {
		t.Fatal("--codex-notify did not parse into captureCodexNotifyFlag — flag not wired to runCapture's routing")
	}
	if !strings.Contains(string(out), "auxly codex notify:") {
		t.Fatalf("expected the codex-notify handler to run and log its own error prefix, got stderr=%q", out)
	}
}

// TestInCaptureCooldown locks the shared cooldown helper every capture entry
// point (Stop-hook, codex notify, shell wrapper) now routes through.
func TestInCaptureCooldown(t *testing.T) {
	memPath := t.TempDir()
	if inCaptureCooldown(memPath) {
		t.Fatal("no marker written yet — must not report cooldown active")
	}
	if err := os.WriteFile(captureMarkerPath(memPath), []byte(time.Now().Format(time.RFC3339)), 0600); err != nil {
		t.Fatal(err)
	}
	if !inCaptureCooldown(memPath) {
		t.Fatal("fresh marker — must report cooldown active")
	}
	old := time.Now().Add(-captureCooldown - time.Minute)
	if err := os.Chtimes(captureMarkerPath(memPath), old, old); err != nil {
		t.Fatal(err)
	}
	if inCaptureCooldown(memPath) {
		t.Fatal("stale marker — must not report cooldown active")
	}
}

// TestReadTranscriptJSONLFallsBackWhenExtractionEmpty locks finding 9: a lone
// embedded JSON line (`{}`) inside an otherwise plain-text transcript must
// not veto the raw-content fallback — only whether anything was actually
// EXTRACTED should decide that.
func TestReadTranscriptJSONLFallsBackWhenExtractionEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := strings.Repeat("plain terminal output, no JSON turns here. ", 50) +
		"\n{}\n" +
		strings.Repeat("more plain text after the stray JSON line. ", 50)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := readTranscriptJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != body {
		t.Fatalf("a lone embedded {} line must not veto the raw fallback:\ngot:  %q\nwant: %q", got, body)
	}
}

// TestRecordClearCaptureError locks the breadcrumb contract: a failed capture
// (hooks discard stderr) writes .capture-last-error, and the next successful
// run clears it.
func TestRecordClearCaptureError(t *testing.T) {
	memPath := t.TempDir()
	if _, err := os.Stat(captureErrorPath(memPath)); !os.IsNotExist(err) {
		t.Fatalf("no run yet — breadcrumb must not exist, got stat err %v", err)
	}

	recordCaptureError(memPath, fmt.Errorf("capture LLM status 404: model not found"))
	data, err := os.ReadFile(captureErrorPath(memPath))
	if err != nil {
		t.Fatalf("recordCaptureError must write the breadcrumb: %v", err)
	}
	if !strings.Contains(string(data), "capture LLM status 404: model not found") {
		t.Fatalf("breadcrumb must carry the error text, got %q", data)
	}
	if _, err := time.Parse(time.RFC3339, strings.Fields(string(data))[0]); err != nil {
		t.Fatalf("breadcrumb must start with an RFC3339 stamp, got %q", data)
	}

	clearCaptureError(memPath)
	if _, err := os.Stat(captureErrorPath(memPath)); !os.IsNotExist(err) {
		t.Fatalf("clearCaptureError must remove the breadcrumb, got stat err %v", err)
	}
	clearCaptureError(memPath) // idempotent — removing a missing file is fine

	// A hostile server body must not reach the terminal as escape sequences.
	recordCaptureError(memPath, fmt.Errorf("status 500: \x1b]0;pwned\x07\x1b[2J%s", strings.Repeat("x", 900)))
	data, err = os.ReadFile(captureErrorPath(memPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(strings.TrimSuffix(string(data), "\n"), "\x1b\x07\n\r") {
		t.Fatalf("breadcrumb must not carry control characters, got %q", data)
	}
	if n := len([]rune(string(data))); n > 600 {
		t.Fatalf("breadcrumb must be capped, got %d runes", n)
	}
}

// TestReadTranscriptJSONLStripsANSIFromRawFallback locks finding 12: the raw
// fallback path must strip terminal color/cursor-control sequences before
// returning — a `script`-captured session carries them, and they're noise
// for the extraction LLM, not signal.
func TestReadTranscriptJSONLStripsANSIFromRawFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	colored := "\x1b[32mgreen text\x1b[0m and \x1b]0;window title\x07 plain text here. " +
		strings.Repeat("filler to keep this realistic. ", 20)
	if err := os.WriteFile(path, []byte(colored), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := readTranscriptJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ANSI escape survived the raw fallback: %q", got)
	}
	if !strings.Contains(got, "green text") || !strings.Contains(got, "plain text here") {
		t.Fatalf("plain text content lost while stripping ANSI: %q", got)
	}
}
