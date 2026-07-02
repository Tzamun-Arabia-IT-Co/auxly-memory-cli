package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCaptureHookInstallUninstall locks the merge discipline: existing hooks
// preserved, idempotent re-install, exact removal, valid JSON throughout.
func TestCaptureHookInstallUninstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{
  "model": "opus",
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "echo done"}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "true"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := installCaptureHook(path)
	if err != nil || !changed {
		t.Fatalf("install: changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(path)
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings no longer valid JSON: %v", err)
	}
	s := string(data)
	for _, want := range []string{"echo done", "PreToolUse", `"model": "opus"`, captureHookCommand} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q after install:\n%s", want, s)
		}
	}

	// Idempotent: second install is a no-op.
	if changed, err := installCaptureHook(path); err != nil || changed {
		t.Fatalf("re-install not idempotent: changed=%v err=%v", changed, err)
	}

	// Uninstall removes exactly ours.
	removed, err := uninstallCaptureHook(path)
	if err != nil || !removed {
		t.Fatalf("uninstall: removed=%v err=%v", removed, err)
	}
	data, _ = os.ReadFile(path)
	s = string(data)
	if strings.Contains(s, captureHookCommand) {
		t.Fatalf("capture hook survived uninstall:\n%s", s)
	}
	for _, want := range []string{"echo done", "PreToolUse"} {
		if !strings.Contains(s, want) {
			t.Fatalf("uninstall damaged existing hook %q:\n%s", want, s)
		}
	}
}

// TestInstallRefusesInvalidJSON locks the never-clobber rule.
func TestInstallRefusesInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("{broken"), 0644)
	if _, err := installCaptureHook(path); err == nil {
		t.Fatal("install accepted invalid JSON — would have clobbered user settings")
	}
}

// TestReadTranscriptJSONLSkipsToolNoise locks the extraction: user/assistant
// text kept, tool dumps and non-message lines dropped, string+block content
// shapes both handled.
func TestReadTranscriptJSONLSkipsToolNoise(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"I prefer Go for CLIs"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Noted — Go it is"},{"type":"tool_use","name":"Bash","input":{"command":"secret-tool-dump"}}]}}`,
		`{"type":"progress","data":"noise"}`,
		`not even json`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)

	got, err := readTranscriptJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "I prefer Go for CLIs") || !strings.Contains(got, "Noted — Go it is") {
		t.Fatalf("text turns missing: %q", got)
	}
	if strings.Contains(got, "secret-tool-dump") {
		t.Fatalf("tool dump leaked into transcript window: %q", got)
	}
	// Missing transcript file is a silent no-op (already cleaned up).
	if out, err := readTranscriptJSONL(filepath.Join(t.TempDir(), "gone.jsonl")); err != nil || out != "" {
		t.Fatalf("missing transcript should no-op: %q %v", out, err)
	}
}
