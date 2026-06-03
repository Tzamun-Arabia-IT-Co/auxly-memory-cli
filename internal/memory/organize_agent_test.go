package memory

import (
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
		name      string
		agentName string
		mustHave  []string // flags/subcommands that must appear before the prompt
	}{
		{"claude is headless + MCP-isolated + tool-disabled + model-pinned", "Claude Code / CLI",
			[]string{"-p", "--strict-mcp-config", "--mcp-config", "--tools", "--model", "haiku"}},
		{"codex uses the exec subcommand under a read-only sandbox", "Codex IDE Desktop",
			[]string{"exec", "--sandbox", "read-only"}},
		{"antigravity uses --print", "Antigravity CLI", []string{"--print"}},
		{"gemini is headless", "Gemini CLI", []string{"-p"}},
		{"cursor is headless text output", "Cursor IDE", []string{"-p", "--output-format", "text"}},
		{"unknown falls back to -p, never bare", "Some Future Agent", []string{"-p"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := buildAgentArgs(tc.agentName, prompt)

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

			// SECURITY: the consolidation embeds user-vault content (prompt-injection
			// vector) and needs no tools — never pass a permission/sandbox-bypass flag.
			for _, banned := range []string{
				"--dangerously-skip-permissions", "--yolo", "-y", "--trust",
				"--dangerously-bypass-approvals-and-sandbox",
			} {
				if containsArg(args, banned) {
					t.Errorf("SECURITY: bypass flag %q must not be passed; got: %s", banned, strings.Join(args[:len(args)-1], " "))
				}
			}
		})
	}
}

// TestProviderKeyAndAllowlist locks the provider mapping and the verified-safe set.
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
	// No agent is on the verified-safe allowlist yet — agentic isolation is unproven,
	// so every CLI agent must be gated to the tool-less Direct LLM / Custom paths.
	for _, name := range []string{"Claude Code / CLI", "Antigravity CLI", "Codex IDE Desktop", "Gemini CLI", "Cursor IDE", "Some Future Agent"} {
		if organizeAgentSafe(name) {
			t.Errorf("%q must not be safe yet (agentic isolation unproven)", name)
		}
	}
}

// TestOrganizeVaultWithAgent_UnverifiedAgentGated: an unverified agent must be
// refused BEFORE any exec. /bin/echo exists, so a failed gate would surface an
// empty-output error rather than the refusal — proving we short-circuit early.
func TestOrganizeVaultWithAgent_UnverifiedAgentGated(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Write("identity.md", "# Identity\n- Name: Test\n"); err != nil {
		t.Fatal(err)
	}
	res := store.OrganizeVaultWithAgent("Some Future Agent", "/bin/echo")
	if res.Success {
		t.Fatalf("expected a safety refusal, got success: %s", res.Message)
	}
	if !strings.Contains(res.Message, "isn't available") {
		t.Errorf("expected a safety-refusal message, got: %s", res.Message)
	}
}

// TestScrubbedOrganizeEnv_DropsAuxlyVars proves the child env can't leak a vault
// pointer to the spawned agent.
func TestScrubbedOrganizeEnv_DropsAuxlyVars(t *testing.T) {
	t.Setenv("AUXLY_MEMORY_PATH", "/Users/lab/.auxly/memory")
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
