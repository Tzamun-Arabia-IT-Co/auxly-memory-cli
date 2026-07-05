package memory

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBuildAgentArgs_HeadlessInvocationPerProvider locks the verified, NON-interactive
// invocation for each agent CLI. The original bug: every non-Claude agent received a
// bare prompt and either errored (Codex needs `exec`) or opened interactive mode and
// hung to the timeout (Antigravity/Gemini/Cursor). Every case must (a) NOT be a bare
// single-arg prompt and (b) end with the prompt as the final argument.
func TestBuildAgentArgs_HeadlessInvocationPerProvider(t *testing.T) {
	const prompt = "ORGANIZE THIS VAULT"

	tests := []struct {
		name             string
		agentName        string
		model            string
		mustHave         []string // flags/subcommands that must appear before the prompt
		mustNotHave      []string
		expectModelValue string
	}{
		{"claude is headless + MCP-isolated + tool-disabled + model-pinned", "Claude Code / CLI",
			"sonnet", []string{"-p", "--strict-mcp-config", "--mcp-config", "--tools", "--model"}, nil, "sonnet"},
		{"claude empty model defaults to sonnet", "Claude Code / CLI",
			"", []string{"-p", "--model", "--effort", "low"}, nil, "sonnet"},
		{"codex uses the exec subcommand under a read-only sandbox, outside a git repo", "Codex IDE Desktop",
			"gpt-5.2-codex", []string{"exec", "--sandbox", "read-only", "--skip-git-repo-check", "--model"}, nil, "gpt-5.2-codex"},
		{"codex omits model flag when empty", "Codex IDE Desktop",
			"", []string{"exec", "--sandbox", "read-only", "--skip-git-repo-check"}, []string{"--model"}, ""},
		{"antigravity uses --print and ignores model", "Antigravity CLI", "ignored-model", []string{"--print"}, []string{"--model", "-m", "ignored-model"}, ""},
		{"gemini is headless with optional model", "Gemini CLI", "gemini-2.5-flash", []string{"-p", "-m"}, nil, "gemini-2.5-flash"},
		{"gemini omits model flag when empty", "Gemini CLI", "", []string{"-p"}, []string{"-m"}, ""},
		{"cursor is headless text output with optional model", "Cursor IDE", "sonnet-4", []string{"-p", "--output-format", "text", "--model"}, nil, "sonnet-4"},
		{"cursor omits model flag when empty", "Cursor IDE", "", []string{"-p", "--output-format", "text"}, []string{"--model"}, ""},
		{"unknown falls back to -p, never bare", "Some Future Agent", "ignored", []string{"-p"}, []string{"--model", "-m", "ignored"}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := buildAgentArgs(tc.agentName, tc.model, prompt)

			// The prompt must be the final argument.
			if len(args) == 0 || args[len(args)-1] != prompt {
				t.Fatalf("prompt must be the last arg; got %#v", args)
			}

			// Never the bare-prompt form that caused the hang/error.
			if len(args) == 1 {
				t.Fatalf("%s produced a bare prompt (the original bug); got %#v", tc.agentName, args)
			}

			joined := strings.Join(args[:len(args)-1], " ")
			for _, want := range tc.mustHave {
				if !containsArg(args[:len(args)-1], want) {
					t.Errorf("expected flag %q in invocation, got: %s", want, joined)
				}
			}
			for _, banned := range tc.mustNotHave {
				if containsArg(args[:len(args)-1], banned) {
					t.Errorf("did not expect arg %q in invocation, got: %s", banned, joined)
				}
			}
			if tc.expectModelValue != "" && !containsArg(args[:len(args)-1], tc.expectModelValue) {
				t.Errorf("expected model value %q in invocation, got: %s", tc.expectModelValue, joined)
			}

			// SECURITY: the consolidation embeds user-vault content (prompt-injection
			// vector) and needs no tools — never pass a permission/sandbox-bypass flag
			// that would let an injected instruction run commands or edit files.
			for _, banned := range []string{
				"--dangerously-skip-permissions", "--yolo", "-y", "--force",
				"--dangerously-bypass-approvals-and-sandbox",
			} {
				if containsArg(args, banned) {
					t.Errorf("SECURITY: bypass flag %q must not be passed; got: %s", banned, strings.Join(args[:len(args)-1], " "))
				}
			}

			// cursor REQUIRES --trust to run headless (it otherwise blocks on a
			// Workspace Trust prompt in the empty temp dir). --trust is acceptable
			// ONLY because it is paired with read-only `--mode ask`, which strips every
			// tool — so an injected instruction has nothing to abuse. Enforce the
			// pairing: --trust without --mode ask is a security regression.
			if containsArg(args, "--trust") {
				if !containsArg(args, "ask") {
					t.Errorf("SECURITY: --trust passed without read-only `--mode ask`; got: %s", strings.Join(args[:len(args)-1], " "))
				}
			}
		})
	}
}

// TestProviderKeyAndAllowlist locks the provider mapping.
func TestProviderKeyAndAllowlist(t *testing.T) {
	cases := map[string]string{
		"Claude Code / CLI": "claude",
		"Codex IDE Desktop": "codex",
		"Antigravity CLI":   "antigravity",
		"Gemini CLI":        "gemini",
		"Cursor IDE":        "cursor",
		"Some Future Agent": "",
	}
	for name, want := range cases {
		if got := providerKey(name); got != want {
			t.Errorf("providerKey(%q)=%q want %q", name, got, want)
		}
	}
}

func TestOrganizeAgent_NotGated(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Write("identity.md", "# Identity\n- Name: Test\n"); err != nil {
		t.Fatal(err)
	}
	_, res := store.PlanOrganizeWithAgent(context.Background(), "Claude Code / CLI", "/bin/echo", "", false)
	if strings.Contains(res.Message, "isn't available") {
		t.Errorf("agent plan path must not be gated, got: %s", res.Message)
	}
}

// CRITICAL 3 regression: organize via a CLI agent must be refused outright
// when any gathered file is encrypted at rest — decrypted content riding the
// spawned subprocess's argv stays ps-visible for the whole run. The
// nonexistent agentPath proves no subprocess was ever spawned: if the guard
// were bypassed, runOrganizeModel's os.Stat check would instead fail with a
// "not runnable" exec error.
func TestPlanOrganize_RefusesCLIAgentWithEncryptedFile(t *testing.T) {
	store := NewStore(t.TempDir())
	testVaultIdentity(t)
	if err := store.Write("identity.md", "# Identity\n- Name: Test\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.EncryptFile("identity.md"); err != nil {
		t.Fatal(err)
	}

	_, res := store.PlanOrganizeWithAgent(context.Background(), "Claude Code / CLI", "/definitely/not/a/real/binary", "", false)
	if res.Success {
		t.Fatal("organize via a CLI agent succeeded despite an encrypted file present")
	}
	if !strings.Contains(res.Message, "command line") {
		t.Fatalf("refusal message should explain the argv-exposure risk, got: %s", res.Message)
	}
	if strings.Contains(res.Message, "not runnable") {
		t.Fatal("guard was bypassed — got an exec-failure message instead of the pre-exec refusal (a subprocess was spawned)")
	}
}

// TestGatherOrganizeFiles_SkipEncryptedExcludes locks skipEncrypted's
// contract: an excluded file is neither gathered (so its plaintext never
// leaves the vault) nor silently dropped from view — its name comes back in
// skipped so callers can report it.
func TestGatherOrganizeFiles_SkipEncryptedExcludes(t *testing.T) {
	s := NewStore(t.TempDir())
	testVaultIdentity(t)
	if err := s.Write("identity.md", "# Identity\n- Name: Test\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("personal.md", "# Personal\n- secret\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.EncryptFile("personal.md"); err != nil {
		t.Fatal(err)
	}

	files, skipped, err := s.gatherOrganizeFiles(true)
	if err != nil {
		t.Fatalf("gatherOrganizeFiles(true): %v", err)
	}
	for _, f := range files {
		if f.Name == "personal.md" {
			t.Fatal("personal.md was not excluded with skipEncrypted=true")
		}
	}
	if len(skipped) != 1 || skipped[0] != "personal.md" {
		t.Fatalf("skipped = %v, want [personal.md]", skipped)
	}

	filesAll, skippedAll, err := s.gatherOrganizeFiles(false)
	if err != nil {
		t.Fatalf("gatherOrganizeFiles(false): %v", err)
	}
	if len(skippedAll) != 0 {
		t.Fatalf("skipped = %v with skipEncrypted=false, want none", skippedAll)
	}
	found := false
	for _, f := range filesAll {
		if f.Name == "personal.md" {
			found = true
		}
	}
	if !found {
		t.Fatal("personal.md missing when skipEncrypted=false")
	}
}

// TestPlanOrganize_SkipEncryptedBypassesRefusal proves skipEncrypted lets a
// CLI-agent plan proceed instead of hitting the CRITICAL 3 refusal, and that
// the skipped file is surfaced on the resulting proposal.
func TestPlanOrganize_SkipEncryptedBypassesRefusal(t *testing.T) {
	store := NewStore(t.TempDir())
	testVaultIdentity(t)
	if err := store.Write("identity.md", "# Identity\n- Name: Test\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("personal.md", "# Personal\n- secret\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.EncryptFile("personal.md"); err != nil {
		t.Fatal(err)
	}

	exec := func(c context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		return organizeRun{jsonContent: `{"files":[{"name":"identity.md","content":"# Identity\n- Name: Test\n"}]}`}, OrganizeResult{}, true
	}
	prop, res := store.planOrganize(context.Background(), "/bin/echo", true, exec)
	if !res.Success {
		t.Fatalf("plan with skipEncrypted should not be refused, got: %s", res.Message)
	}
	if len(prop.SkippedEncrypted) != 1 || prop.SkippedEncrypted[0] != "personal.md" {
		t.Fatalf("SkippedEncrypted = %v, want [personal.md]", prop.SkippedEncrypted)
	}
}

func TestPlanOrganize_DoesNotWrite(t *testing.T) {
	store := NewStore(t.TempDir())
	store.WorkspaceRoot = ""
	if err := store.Write("identity.md", "# Identity\n- Name: Test\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("projects.md", "# Projects\n- Alpha\n"); err != nil {
		t.Fatal(err)
	}

	_, _ = store.PlanOrganizeWithAgent(context.Background(), "Some Future Agent", "/bin/echo", "", false)
	if c, _ := store.View("identity.md"); c != "# Identity\n- Name: Test\n" {
		t.Errorf("planning must not write identity.md; got: %q", c)
	}
	if c, _ := store.View("projects.md"); c != "# Projects\n- Alpha\n" {
		t.Errorf("planning must not write projects.md; got: %q", c)
	}
}

func TestResolveOrganizeProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("AUXLY_LLM_KEY", "")

	baseURL, apiKey, res, ok := resolveOrganizeProvider("ollama", "")
	if !ok || baseURL != "http://localhost:11434" || apiKey != "" {
		t.Fatalf("ollama = (%q, %q, %v, %v), want localhost/no-key/ok", baseURL, apiKey, res, ok)
	}

	_, _, res, ok = resolveOrganizeProvider("openai", "")
	if ok || !strings.Contains(res.Message, "OPENAI_API_KEY not set") {
		t.Fatalf("openai without key should fail clearly, got ok=%v message=%q", ok, res.Message)
	}
	t.Setenv("OPENAI_API_KEY", "sk-test")
	baseURL, apiKey, _, ok = resolveOrganizeProvider("openai", "")
	if !ok || baseURL != "https://api.openai.com" || apiKey != "sk-test" {
		t.Fatalf("openai with key = (%q, %q, ok=%v)", baseURL, apiKey, ok)
	}

	_, _, res, ok = resolveOrganizeProvider("gemini", "")
	if ok || !strings.Contains(res.Message, "GEMINI_API_KEY not set") {
		t.Fatalf("gemini without key should fail clearly, got ok=%v message=%q", ok, res.Message)
	}
	t.Setenv("GEMINI_API_KEY", "gem-test")
	baseURL, apiKey, _, ok = resolveOrganizeProvider("gemini", "")
	if !ok || baseURL != "https://generativelanguage.googleapis.com/v1beta/openai" || apiKey != "gem-test" {
		t.Fatalf("gemini with key = (%q, %q, ok=%v)", baseURL, apiKey, ok)
	}

	_, _, res, ok = resolveOrganizeProvider("custom", "")
	if ok || !strings.Contains(res.Message, "Custom endpoint URL is empty") {
		t.Fatalf("custom empty URL should fail clearly, got ok=%v message=%q", ok, res.Message)
	}
	t.Setenv("AUXLY_LLM_KEY", "custom-key")
	baseURL, apiKey, _, ok = resolveOrganizeProvider("custom", "http://example.test/")
	if !ok || baseURL != "http://example.test" || apiKey != "custom-key" {
		t.Fatalf("custom with key = (%q, %q, ok=%v)", baseURL, apiKey, ok)
	}
}

// TestScrubbedOrganizeEnv_DropsAuxlyVars proves the child env can't leak a vault
// pointer to the spawned agent.
func TestScrubbedOrganizeEnv_DropsAuxlyVars(t *testing.T) {
	t.Setenv("AUXLY_MEMORY_PATH", "/home/user/.auxly/memory")
	t.Setenv("AUXLY_PROVIDER", "claude")
	for _, kv := range scrubbedOrganizeEnv() {
		if strings.HasPrefix(kv, "AUXLY_") {
			t.Errorf("scrubbed env still contains an AUXLY_ var: %s", kv)
		}
	}
}

// TestProposeThenApply_NoWriteUntilApproved is the keystone of the preview+confirm
// flow: buildProposalFromJSON must NOT touch disk, and ApplyOrganizeChanges must
// write ONLY the approved changes (honoring user edits) and skip rejected ones.
func TestProposeThenApply_NoWriteUntilApproved(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Write("identity.md", "# Identity\n- Name: Old\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("projects.md", "# Projects\n- Zeta\n"); err != nil {
		t.Fatal(err)
	}

	payload := `{"files":[
		{"name":"identity.md","content":"# Identity\n- Name: New\n"},
		{"name":"projects.md","content":"# Projects\n- Zeta\n- Added\n"}
	]}`
	prop, res := store.buildProposalFromJSON(payload, "test-model", 0)
	if !res.Success {
		t.Fatalf("buildProposalFromJSON failed: %s", res.Message)
	}
	if len(prop.Changes) != 2 {
		t.Fatalf("want 2 proposed changes, got %d", len(prop.Changes))
	}

	// 1) PLAN MUST NOT WRITE — both files unchanged on disk.
	if c, _ := store.View("identity.md"); !strings.Contains(c, "Name: Old") {
		t.Errorf("buildProposalFromJSON must not write; identity.md changed to: %q", c)
	}
	if c, _ := store.View("projects.md"); strings.Contains(c, "Added") {
		t.Errorf("buildProposalFromJSON must not write; projects.md changed to: %q", c)
	}

	// 2) APPLY ONLY THE APPROVED + EDITED change (approve identity, edit it, reject projects).
	approved := prop.Changes[0]
	approved.NewContent = "# Identity\n- Name: Edited\n" // user edit before approval
	store.ApplyOrganizeChanges([]ProposedChange{approved})

	if c, _ := store.View("identity.md"); !strings.Contains(c, "Name: Edited") {
		t.Errorf("approved+edited change not applied; got: %q", c)
	}
	if c, _ := store.View("projects.md"); strings.Contains(c, "Added") {
		t.Errorf("rejected change must NOT be applied; got: %q", c)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestOrganizeTimeout_EnvOverride verifies the timeout default and the env override.
func TestOrganizeTimeout_EnvOverride(t *testing.T) {
	t.Setenv("AUXLY_ORGANIZE_TIMEOUT", "")
	if got := organizeTimeout(); got != defaultOrganizeTimeout {
		t.Errorf("default: want %s, got %s", defaultOrganizeTimeout, got)
	}

	t.Setenv("AUXLY_ORGANIZE_TIMEOUT", "120")
	if got := organizeTimeout(); got != 120*time.Second {
		t.Errorf("override: want 120s, got %s", got)
	}

	// Invalid values fall back to the default.
	for _, bad := range []string{"0", "-5", "abc"} {
		t.Setenv("AUXLY_ORGANIZE_TIMEOUT", bad)
		if got := organizeTimeout(); got != defaultOrganizeTimeout {
			t.Errorf("invalid %q: want default %s, got %s", bad, defaultOrganizeTimeout, got)
		}
	}
	_ = os.Unsetenv("AUXLY_ORGANIZE_TIMEOUT")
}

// TestIsOrganizableFile locks which files the organize pass may touch: user-memory
// taxonomy files only — never agents.md or non-taxonomy setup/instruction files.
func TestIsOrganizableFile(t *testing.T) {
	organizable := []string{"identity.md", "personal.md", "preferences.md", "infra.md", "products.md", "projects.md", "daily.md", "business.md"}
	for _, f := range organizable {
		if !IsOrganizableFile(f) {
			t.Errorf("%s should be organizable", f)
		}
	}
	excluded := []string{"agents.md", "AGENTS.md", "CLAUDE.md", "CODEX.md", "GEMINI.md", "providers.md", "unified_memory.md", "random.txt"}
	for _, f := range excluded {
		if IsOrganizableFile(f) {
			t.Errorf("%s must NOT be organizable (setup/agent/non-memory file)", f)
		}
	}
}

// TestBuildProposalDropsSetupFiles proves the output-side guard: even if the model
// returns a setup file or agents.md, it never becomes a proposed change.
func TestBuildProposalDropsSetupFiles(t *testing.T) {
	store := NewStore(t.TempDir())
	payload := `{"files":[
		{"name":"identity.md","content":"# Identity\n- Name: New\n"},
		{"name":"agents.md","content":"# Agents\n- rewritten setup\n"},
		{"name":"CLAUDE.md","content":"# Claude rules\n- tampered\n"}
	]}`
	prop, res := store.buildProposalFromJSON(payload, "test", 0)
	if !res.Success {
		t.Fatalf("buildProposalFromJSON failed: %s", res.Message)
	}
	for _, c := range prop.Changes {
		if c.Name == "agents.md" || c.Name == "CLAUDE.md" {
			t.Errorf("proposal must not include setup file %q", c.Name)
		}
	}
	if len(prop.Changes) != 1 || prop.Changes[0].Name != "identity.md" {
		t.Errorf("expected only identity.md in proposal, got %+v", prop.Changes)
	}
}

// TestExtractJSON_StripsFencesAndTrailingProse verifies the extractor unwraps a
// ```json fence and returns only the first balanced object, dropping trailing prose
// even when that prose itself contains braces (a common agent-CLI epilogue).
func TestExtractJSON_StripsFencesAndTrailingProse(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", `{"files":[]}`, `{"files":[]}`},
		{"fenced", "```json\n{\"files\":[]}\n```", `{"files":[]}`},
		{"trailing-prose", `{"files":[]}` + "\nLet me know if you need anything else!", `{"files":[]}`},
		{"trailing-braces", `{"files":[]}` + "\nLet me know if {you} want more.", `{"files":[]}`},
		{"leading-noise", "Loading workspace...\n" + `{"files":[]}`, `{"files":[]}`},
		{"brace-in-string", `{"files":[{"name":"a.md","content":"use {braces} here"}]}`, `{"files":[{"name":"a.md","content":"use {braces} here"}]}`},
	}
	for _, c := range cases {
		if got := extractJSON(c.in); got != c.want {
			t.Errorf("%s: extractJSON = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestBuildProposalFromJSON_RepairsUnescapedQuotes reproduces the antigravity/Gemini
// failure (an unescaped " inside a content value, e.g. "Loading") and asserts the
// lenient repair fallback salvages a usable proposal instead of failing the run.
func TestBuildProposalFromJSON_RepairsUnescapedQuotes(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Write("identity.md", "# Identity\n"); err != nil {
		t.Fatal(err)
	}
	// The model wrote the word Loading wrapped in straight quotes without escaping
	// them, which Go's json rejects with: invalid character 'L' after key:value pair.
	bad := `{"files":[{"name":"identity.md","content":"# Identity\n- Status: "Loading" now"}]}`
	if err := json.Unmarshal([]byte(bad), &struct {
		Files []struct{ Name, Content string }
	}{}); err == nil {
		t.Fatal("test premise broken: payload should be invalid JSON")
	}
	prop, res := store.buildProposalFromJSON(bad, "antigravity", 0)
	if !res.Success {
		t.Fatalf("repair fallback should salvage the payload, got: %s", res.Message)
	}
	if len(prop.Changes) != 1 || prop.Changes[0].Name != "identity.md" {
		t.Fatalf("want 1 change for identity.md, got %+v", prop.Changes)
	}
	if !strings.Contains(prop.Changes[0].NewContent, `"Loading"`) {
		t.Errorf("repaired content should preserve the quoted word, got: %q", prop.Changes[0].NewContent)
	}
}

// TestRepairAgentJSON_PreservesValidStructure ensures the repair pass leaves real
// string delimiters intact (quotes followed by structural tokens) and only escapes
// genuine interior quotes and raw control characters.
func TestRepairAgentJSON_PreservesValidStructure(t *testing.T) {
	valid := `{"files":[{"name":"a.md","content":"clean"}]}`
	if got := repairAgentJSON(valid); got != valid {
		t.Errorf("repair altered valid JSON: %q", got)
	}
	if err := json.Unmarshal([]byte(repairAgentJSON(valid)), &struct{}{}); err != nil {
		t.Errorf("repaired valid JSON no longer parses: %v", err)
	}
}
