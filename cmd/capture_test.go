package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
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

// TestCaptureConfigPriority verifies: env beats settings; settings beat default; --agent none clears.
func TestCaptureConfigPriority(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv(captureAgentEnv, "")
	t.Setenv(captureModelEnv, "")

	// 1. Default (no env, no settings)
	agent, model, source := resolveCaptureBackend()
	if agent != "" || model != "" || source != "default" {
		t.Fatalf("expected default Direct LLM, got agent=%q model=%q source=%q", agent, model, source)
	}
	if got := formatCaptureBackendSummary(agent, model, source); got != "Direct LLM (default)" {
		t.Fatalf("expected 'Direct LLM (default)', got %q", got)
	}

	// 2. Settings beat default
	s := config.LoadSettings()
	s.CaptureAgent = "claude"
	s.CaptureModel = "sonnet"
	if err := config.SaveSettings(s); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	agent, model, source = resolveCaptureBackend()
	if agent != "claude" || model != "sonnet" || source != "settings" {
		t.Fatalf("expected settings claude/sonnet, got agent=%q model=%q source=%q", agent, model, source)
	}
	if got := formatCaptureBackendSummary(agent, model, source); got != "claude · sonnet (settings)" {
		t.Fatalf("expected 'claude · sonnet (settings)', got %q", got)
	}

	// 3. Env beats settings
	t.Setenv(captureAgentEnv, "codex")
	t.Setenv(captureModelEnv, "gpt-5.2")

	agent, model, source = resolveCaptureBackend()
	if agent != "codex" || model != "gpt-5.2" || source != "env" {
		t.Fatalf("expected env codex/gpt-5.2, got agent=%q model=%q source=%q", agent, model, source)
	}
	if got := formatCaptureBackendSummary(agent, model, source); got != "codex · gpt-5.2 (env)" {
		t.Fatalf("expected 'codex · gpt-5.2 (env)', got %q", got)
	}

	// Clear env for the next check
	t.Setenv(captureAgentEnv, "")
	t.Setenv(captureModelEnv, "")

	// 4. --agent none clears
	// Call RunE directly: Execute() on a subcommand runs the ROOT command with
	// os.Args, which launches the TUI and needs a TTY.
	if err := captureConfigCmd.Flags().Set("agent", "none"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = captureConfigCmd.Flags().Set("agent", ""); captureConfigAgent = "" })
	if err := runCaptureConfig(captureConfigCmd, nil); err != nil {
		t.Fatalf("capture config --agent none: %v", err)
	}

	saved := config.LoadSettings()
	if saved.CaptureAgent != "" || saved.CaptureModel != "" {
		t.Fatalf("expected cleared settings, got agent=%q model=%q", saved.CaptureAgent, saved.CaptureModel)
	}

	agent, model, source = resolveCaptureBackend()
	if agent != "" || source != "default" {
		t.Fatalf("expected Direct LLM default after clearing, got agent=%q source=%q", agent, source)
	}
}

func stubCaptureJudge(t *testing.T, fn func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error)) {
	t.Helper()
	old := captureJudge
	captureJudge = fn
	t.Cleanup(func() { captureJudge = old })
}

func writeTrustConfig(t *testing.T, memPath, provider, level string) {
	t.Helper()
	cfg := &trust.Config{
		Default: trust.LevelRequireApproval,
		Providers: map[string]trust.ProviderConfig{
			provider: {TrustLevel: level},
		},
	}
	if err := cfg.Save(memPath); err != nil {
		t.Fatalf("save trust.yaml: %v", err)
	}
}

func TestCaptureTrustProvider(t *testing.T) {
	if got := captureTrustProvider("capture:claude-code"); got != "claude" {
		t.Fatalf("captureTrustProvider(capture:claude-code) = %q, want claude", got)
	}
	if got := captureTrustProvider("claude-code"); got != "claude" {
		t.Fatalf("captureTrustProvider(claude-code) = %q, want claude", got)
	}
	if got := captureTrustProvider("capture:codex"); got != "codex" {
		t.Fatalf("captureTrustProvider(capture:codex) = %q, want codex", got)
	}
	if got := captureTrustProvider("gemini"); got != "gemini" {
		t.Fatalf("captureTrustProvider(gemini) = %q, want gemini", got)
	}
}

func TestStoreCaptureFacts_TrustAutoSavesDirectly(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		t.Fatal("judge must not be called when there are no neighbors")
		return nil, nil
	})

	out := captureStdout(t, func() {
		storeCaptureFacts(memPath, []memory.CaptureFact{
			{Category: "infra", Fact: "prod server is 192.0.2.24"},
		}, "claude-code")
	})

	data, err := os.ReadFile(filepath.Join(memPath, "infra.md"))
	if err != nil {
		t.Fatalf("expected infra.md to be written: %v", err)
	}
	if !strings.Contains(string(data), "prod server is 192.0.2.24") {
		t.Fatalf("fact missing from infra.md: %q", data)
	}
	items, err := pending.NewManager(memPath).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected nothing left in .pending, got %+v", items)
	}
	if !strings.Contains(out, "🪄 capture: 1 saved to memory") || strings.Contains(out, "held for review") {
		t.Fatalf("unexpected summary output: %q", out)
	}
	auditLog, _ := os.ReadFile(filepath.Join(memPath, ".audit.log"))
	if !strings.Contains(string(auditLog), "auto-captured (trusted provider)") {
		t.Fatalf("expected auto audit reason, got %q", auditLog)
	}
}

func TestStoreCaptureFacts_TrustRequireApprovalHoldsPending(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelRequireApproval)
	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		t.Fatal("judge must not be called when there are no neighbors")
		return nil, nil
	})

	out := captureStdout(t, func() {
		storeCaptureFacts(memPath, []memory.CaptureFact{
			{Category: "infra", Fact: "prod server is 192.0.2.24"},
		}, "claude-code")
	})

	if _, err := os.Stat(filepath.Join(memPath, "infra.md")); !os.IsNotExist(err) {
		t.Fatalf("infra.md must stay untouched under require_approval, stat err=%v", err)
	}
	items, err := pending.NewManager(memPath).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 entry in .pending, got %+v", items)
	}
	if !strings.Contains(out, "🪄 capture: 1 held for review (`auxly pending`)") || strings.Contains(out, "saved to memory") {
		t.Fatalf("unexpected summary output: %q", out)
	}
	auditLog, _ := os.ReadFile(filepath.Join(memPath, ".audit.log"))
	if !strings.Contains(string(auditLog), "held: provider trust is require_approval") {
		t.Fatalf("expected trust hold audit reason, got %q", auditLog)
	}
}

func TestStoreCaptureFacts_PersonalAndSecretHeldEvenWhenAuto(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		return map[int]bool{}, nil
	})

	captureStdout(t, func() {
		storeCaptureFacts(memPath, []memory.CaptureFact{
			{Category: "personal", Fact: "family vacation is in August"},
			{Category: "identity", Fact: "founder and principal architect"},
			{Category: "infra", Fact: "staging OpenAI key is sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz12"},
		}, "claude-code")
	})

	for _, f := range []string{"personal.md", "identity.md", "infra.md"} {
		if _, err := os.Stat(filepath.Join(memPath, f)); !os.IsNotExist(err) {
			t.Fatalf("%s must not be auto-written, stat err=%v", f, err)
		}
	}
	items, err := pending.NewManager(memPath).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 held entries in .pending, got %d", len(items))
	}
	auditLog, _ := os.ReadFile(filepath.Join(memPath, ".audit.log"))
	if !strings.Contains(string(auditLog), "held: personal/operational category") {
		t.Fatalf("missing personal/operational audit reason: %q", auditLog)
	}
	if !strings.Contains(string(auditLog), "held: possible secret") {
		t.Fatalf("missing secret audit reason: %q", auditLog)
	}
}

func TestStoreCaptureFacts_JudgeKnownDropped(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	initial := "# Infra\n- [2026-10-01] production postgres database runs on port 5432\n"
	if err := os.WriteFile(filepath.Join(memPath, "infra.md"), []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	called := 0
	stubCaptureJudge(t, func(_ context.Context, cands []memory.CaptureFact, neighbors [][]string) (map[int]bool, error) {
		called++
		if len(cands) != 1 || len(neighbors) != 1 || len(neighbors[0]) == 0 {
			t.Fatalf("unexpected judge batch: cands=%+v neighbors=%+v", cands, neighbors)
		}
		return map[int]bool{0: true}, nil
	})

	out := captureStdout(t, func() {
		storeCaptureFacts(memPath, []memory.CaptureFact{
			{Category: "infra", Fact: "prod postgres server runs on port 5432"},
		}, "claude-code")
	})

	if called != 1 {
		t.Fatalf("expected 1 judge call, got %d", called)
	}
	got, _ := os.ReadFile(filepath.Join(memPath, "infra.md"))
	if string(got) != initial {
		t.Fatalf("known candidate modified infra.md:\n%s", got)
	}
	items, _ := pending.NewManager(memPath).List()
	if len(items) != 0 {
		t.Fatalf("known candidate must not be queued, got %+v", items)
	}
	if out != "" {
		t.Fatalf("expected silent no-op when all facts dropped, got %q", out)
	}
}

func TestStoreCaptureFacts_JudgeErrorHoldsUncertainSavesNeighborless(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	initialInfra := "# Infra\n- [2026-10-01] production postgres database runs on port 5432\n"
	if err := os.WriteFile(filepath.Join(memPath, "infra.md"), []byte(initialInfra), 0644); err != nil {
		t.Fatal(err)
	}

	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		return nil, fmt.Errorf("judge backend offline")
	})

	out := captureStdout(t, func() {
		storeCaptureFacts(memPath, []memory.CaptureFact{
			{Category: "infra", Fact: "prod postgres server runs on port 5432"},   // has neighbor -> uncertain -> held
			{Category: "preferences", Fact: "always run gofmt before committing"}, // no neighbor -> auto-saved
		}, "claude-code")
	})

	// infra.md unchanged; its candidate is held in .pending.
	gotInfra, _ := os.ReadFile(filepath.Join(memPath, "infra.md"))
	if string(gotInfra) != initialInfra {
		t.Fatalf("uncertain fact must not auto-write infra.md:\n%s", gotInfra)
	}
	items, _ := pending.NewManager(memPath).List()
	if len(items) != 1 {
		t.Fatalf("expected 1 uncertain entry held in .pending, got %+v", items)
	}

	// Neighborless preference fact auto-saved directly.
	gotPrefs, err := os.ReadFile(filepath.Join(memPath, "preferences.md"))
	if err != nil {
		t.Fatalf("expected preferences.md to be auto-written: %v", err)
	}
	if !strings.Contains(string(gotPrefs), "always run gofmt before committing") {
		t.Fatalf("neighborless fact missing from preferences.md: %q", gotPrefs)
	}
	if !strings.Contains(out, "🪄 capture: 1 saved to memory, 1 held for review (`auxly pending`)") {
		t.Fatalf("unexpected summary output: %q", out)
	}
	auditLog, _ := os.ReadFile(filepath.Join(memPath, ".audit.log"))
	if !strings.Contains(string(auditLog), "held: duplicate check unavailable") {
		t.Fatalf("expected uncertain audit reason, got %q", auditLog)
	}
}

func TestStoreCaptureFacts_DropsAlreadyPendingExactDuplicate(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	mgr := pending.NewManager(memPath)
	if _, err := mgr.WriteFrom("infra.md", "+ - [2026-10-20] backup NAS lives at 192.0.2.50\n", "capture:claude-code"); err != nil {
		t.Fatal(err)
	}

	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		t.Fatal("exact pending duplicate must be dropped before the judge")
		return nil, nil
	})

	out := captureStdout(t, func() {
		storeCaptureFacts(memPath, []memory.CaptureFact{
			{Category: "infra", Fact: "Backup   NAS lives at 192.0.2.50"},
		}, "claude-code")
	})

	if _, err := os.Stat(filepath.Join(memPath, "infra.md")); !os.IsNotExist(err) {
		t.Fatalf("duplicate of pending entry must not write infra.md, stat err=%v", err)
	}
	items, _ := mgr.List()
	if len(items) != 1 {
		t.Fatalf("pending queue must stay at 1 entry, got %d", len(items))
	}
	if out != "" {
		t.Fatalf("expected silent drop for pending duplicate, got %q", out)
	}

	// Reworded variant of that pending entry uses the pending line as a neighbor for step 2c.
	sawPendingNeighbor := false
	stubCaptureJudge(t, func(_ context.Context, _ []memory.CaptureFact, neighbors [][]string) (map[int]bool, error) {
		if len(neighbors) == 1 && len(neighbors[0]) == 1 && strings.Contains(neighbors[0][0], "backup NAS lives at 192.0.2.50") {
			sawPendingNeighbor = true
		}
		return map[int]bool{0: true}, nil
	})
	storeCaptureFacts(memPath, []memory.CaptureFact{
		{Category: "infra", Fact: "backup NAS server is at 192.0.2.50"},
	}, "claude-code")
	if !sawPendingNeighbor {
		t.Fatal("pending addition line should count as a known line for neighbor retrieval")
	}
}

// TestStoreCaptureFacts_MultilineFactSanitizedSingleLine verifies that facts
// containing newlines are collapsed to a single line before saving to prevent
// embedded diff deletion attacks.
func TestStoreCaptureFacts_MultilineFactSanitizedSingleLine(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		return nil, nil
	})

	existingLine := "- [2026-01-01] existing fact"
	initialContent := "# Infrastructure\n" + existingLine + "\n"
	if err := os.WriteFile(filepath.Join(memPath, "infra.md"), []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}

	multilineFact := "new fact\n- [2026-01-01] existing fact"
	storeCaptureFacts(memPath, []memory.CaptureFact{
		{Category: "infra", Fact: multilineFact},
	}, "claude-code")

	data, err := os.ReadFile(filepath.Join(memPath, "infra.md"))
	if err != nil {
		t.Fatalf("expected infra.md to exist: %v", err)
	}
	content := string(data)

	// Existing line must not be removed.
	if !strings.Contains(content, existingLine) {
		t.Fatalf("existing line was removed from infra.md; content: %q", content)
	}

	// The saved line must be single-line.
	lines := strings.Split(content, "\n")
	foundNew := false
	for _, line := range lines {
		if strings.Contains(line, "new fact") {
			foundNew = true
			if strings.Contains(line, "\r") || strings.Contains(line, "\n") {
				t.Fatalf("saved line contains newline: %q", line)
			}
			if !strings.Contains(line, "new fact - [2026-01-01] existing fact") {
				t.Fatalf("expected fact to be collapsed to single line, got: %q", line)
			}
		}
	}
	if !foundNew {
		t.Fatalf("expected newly saved fact in infra.md; content: %q", content)
	}
}

// TestStoreCaptureFacts_ControlAndLineSeparatorSanitizedSingleLine tests FIX 1:
// a fact containing "\x1b[31m", "\r\n- [2026-01-01] existing fact", " - old" with
// trust auto never deletes the existing vault line and is saved as a single clean line.
func TestStoreCaptureFacts_ControlAndLineSeparatorSanitizedSingleLine(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		return nil, nil
	})

	existingLine := "- [2026-01-01] existing fact"
	initialContent := "# Infrastructure\n" + existingLine + "\n"
	if err := os.WriteFile(filepath.Join(memPath, "infra.md"), []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}

	fact := "\x1b[31m\r\n- [2026-01-01] existing fact\u2028- old"
	storeCaptureFacts(memPath, []memory.CaptureFact{
		{Category: "infra", Fact: fact},
	}, "claude-code")

	data, err := os.ReadFile(filepath.Join(memPath, "infra.md"))
	if err != nil {
		t.Fatalf("expected infra.md to exist: %v", err)
	}
	content := string(data)

	// Existing line in the vault must never be deleted.
	if !strings.Contains(content, existingLine) {
		t.Fatalf("existing line was removed from infra.md; content: %q", content)
	}

	// The saved line must be a single clean line without control characters.
	lines := strings.Split(content, "\n")
	foundClean := false
	expectedSnippet := "[31m - [2026-01-01] existing fact - old"
	for _, line := range lines {
		if strings.Contains(line, expectedSnippet) {
			foundClean = true
			if strings.Contains(line, "\x1b") || strings.Contains(line, "\r") || strings.Contains(line, "\u2028") {
				t.Fatalf("saved line contains control or line separator chars: %q", line)
			}
			if !strings.HasPrefix(strings.TrimSpace(line), "- [") {
				t.Fatalf("saved line should be a markdown bullet starting with '- [': %q", line)
			}
		}
	}
	if !foundClean {
		t.Fatalf("expected newly saved clean line in infra.md; content: %q", content)
	}

	// With trust auto, it should have been saved to memory, leaving nothing in pending.
	items, err := pending.NewManager(memPath).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 pending items after auto-save, got %d", len(items))
	}
}

// TestIsSingleAddLineDiff tests the defensive diff shape validator for FIX 1.
func TestIsSingleAddLineDiff(t *testing.T) {
	cases := []struct {
		diff string
		want bool
	}{
		{"+ - [2026-10-29] fact\n", true},
		{"+ - fact\n", true},
		{"+ - [2026-10-29] fact\n- [2026-01-01] old\n", false},
		{"+ - [2026-10-29] fact\n+ - next fact\n", false},
		{"+ - [2026-10-29] fact", false},
		{"- [2026-01-01] old\n", false},
		{"+ fact\n", false},
		{"\n", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isSingleAddLineDiff(tc.diff)
		if got != tc.want {
			t.Errorf("isSingleAddLineDiff(%q) = %v, want %v", tc.diff, got, tc.want)
		}
	}
}

// TestCaptureNeedsReviewCategory tests the helper for FIX 2.
func TestCaptureNeedsReviewCategory(t *testing.T) {
	cases := []struct {
		slug string
		want bool
	}{
		{"personal", true},
		{"inbox", true},
		{"tasks", true},
		{"identity", true},
		{"infra", false},
		{"preferences", false},
		{"products", false},
		{"projects", false},
		{"daily", false},
		{"business", false},
		{"agents", false},
		{"nonexistent-slug", true},
	}
	for _, tc := range cases {
		got := captureNeedsReviewCategory(tc.slug)
		if got != tc.want {
			t.Errorf("captureNeedsReviewCategory(%q) = %v, want %v", tc.slug, got, tc.want)
		}
	}
}

// TestStoreCaptureFacts_CategorySensitivityGate tests FIX 2:
// with trust auto, facts routed to "personal", "inbox", "tasks" and "identity"
// are all held; "infra" is auto-saved.
func TestStoreCaptureFacts_CategorySensitivityGate(t *testing.T) {
	memPath := t.TempDir()
	writeTrustConfig(t, memPath, "claude", trust.LevelAuto)
	stubCaptureJudge(t, func(context.Context, []memory.CaptureFact, [][]string) (map[int]bool, error) {
		return map[int]bool{}, nil
	})

	storeCaptureFacts(memPath, []memory.CaptureFact{
		{Category: "personal", Fact: "family vacation is in August"},
		{Category: "inbox", Fact: "quick note to follow up tomorrow"},
		{Category: "tasks", Fact: "buy more server racks for datacenter"},
		{Category: "identity", Fact: "founder and principal architect"},
		{Category: "infra", Fact: "prod server is 192.0.2.24"},
	}, "claude-code")

	// personal, inbox, tasks, identity are all held
	for _, f := range []string{"personal.md", "inbox.md", "tasks.md", "identity.md"} {
		if _, err := os.Stat(filepath.Join(memPath, f)); !os.IsNotExist(err) {
			t.Fatalf("%s must not be auto-written, stat err=%v", f, err)
		}
	}

	// infra is auto-saved
	infraData, err := os.ReadFile(filepath.Join(memPath, "infra.md"))
	if err != nil {
		t.Fatalf("expected infra.md to be auto-written: %v", err)
	}
	if !strings.Contains(string(infraData), "prod server is 192.0.2.24") {
		t.Fatalf("infra fact missing from infra.md: %q", infraData)
	}

	// 4 held entries in pending queue
	items, err := pending.NewManager(memPath).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 held entries in .pending, got %d", len(items))
	}

	auditLog, _ := os.ReadFile(filepath.Join(memPath, ".audit.log"))
	if !strings.Contains(string(auditLog), "held: personal/operational category") {
		t.Fatalf("missing personal/operational audit reason: %q", auditLog)
	}
}
