package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
)

// C1: the write handler must use SERVER-SIDE provider attribution and ignore a
// client-supplied "provider" arg. Here the server's real provider (AUXLY_PROVIDER)
// is read_only while the client claims "claude" (which the default would treat as
// auto). The write must be rejected as read_only — proving the client arg is ignored.
func TestWrite_IgnoresClientSuppliedProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := &trust.Config{
		Default: trust.LevelAuto,
		Providers: map[string]trust.ProviderConfig{
			"evilprov": {TrustLevel: trust.LevelReadOnly},
		},
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_PROVIDER", "evilprov")

	s := NewServer(dir)
	var buf bytes.Buffer
	s.outWriter = &buf

	params := `{"name":"auxly_memory_write","arguments":{"file":"identity.md","diff":"+pwned","reason":"r","provider":"claude"}}`
	s.handleToolCall(&jsonRPCRequest{ID: 1, Params: json.RawMessage(params)})

	out := buf.String()
	if !strings.Contains(out, "read_only") {
		t.Fatalf("write should be rejected read_only (client 'claude' must be ignored); got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "identity.md")); err == nil {
		t.Fatalf("read_only provider must not have written identity.md")
	}
}

// C2: approve/reject over MCP must NOT mutate the queue — they are redirected to
// the human-only local path. The pending entry stays put and the target is unwritten.
func TestSkillPending_ApproveRejectAreHumanOnly(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir)

	name, err := s.pendingMgr.Write("identity.md", "+secret")
	if err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"approve", "reject"} {
		out := resultText(s.toolSkillPending(action, name))
		low := strings.ToLower(out)
		if !strings.Contains(low, "human-only") && !strings.Contains(out, "auxly "+action) {
			t.Fatalf("%s should be redirected to the human-only path; got: %s", action, out)
		}
	}

	// The pending entry must be untouched (never approved/rejected via MCP).
	list, _ := s.pendingMgr.List()
	found := false
	for _, p := range list {
		if p.Name == name {
			found = true
		}
	}
	if !found {
		t.Fatal("pending entry was mutated via MCP approve/reject — must be untouched")
	}
	// The target file must NOT have been written by an MCP "approve".
	if _, err := os.Stat(filepath.Join(dir, "identity.md")); err == nil {
		t.Fatal("MCP approve must not write the target file")
	}
}

// C2: listing must still work over MCP (the only allowed action).
func TestSkillPending_ListStillWorks(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir)
	if _, err := s.pendingMgr.Write("identity.md", "+x"); err != nil {
		t.Fatal(err)
	}
	if res := s.toolSkillPending("list", ""); res.IsError {
		t.Fatalf("list should work over MCP: %s", resultText(res))
	}
}

// C1 (closure): auxly_skill_sync must be gated by server-side provider trust too —
// it must not route around resolveProvider via getProviderFromParent.
func TestSkillSync_GatedByServerProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := &trust.Config{Default: trust.LevelAuto, Providers: map[string]trust.ProviderConfig{
		"evilprov": {TrustLevel: trust.LevelReadOnly},
	}}
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_PROVIDER", "evilprov")
	s := NewServer(dir)

	out := resultText(s.toolSkillSync("Style: terse", "preferences", "global"))
	if !strings.Contains(out, "read_only") {
		t.Fatalf("skill_sync must be gated read_only by server-side provider; got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "preferences.md")); err == nil {
		t.Fatal("read_only provider must not write preferences.md via skill_sync")
	}
}

// C1 (closure): auxly_skill_forget must be trust-gated — a non-auto provider
// cannot prune (delete) memory content over MCP.
func TestSkillForget_GatedByServerProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := &trust.Config{Default: trust.LevelAuto, Providers: map[string]trust.ProviderConfig{
		"evilprov": {TrustLevel: trust.LevelReadOnly},
	}}
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "preferences.md"), []byte("- secret line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_PROVIDER", "evilprov")
	s := NewServer(dir)

	if res := s.toolSkillForget("secret"); !res.IsError {
		t.Fatalf("skill_forget must be gated for non-auto providers; got: %s", resultText(res))
	}
	data, _ := os.ReadFile(filepath.Join(dir, "preferences.md"))
	if !strings.Contains(string(data), "secret line") {
		t.Fatal("read_only provider pruned content via skill_forget — trust gate failed")
	}
}

func TestWrite_RefusesNonMemoryTargets(t *testing.T) {
	for _, level := range []string{trust.LevelRequireApproval, trust.LevelAuto} {
		t.Run(level, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &trust.Config{
				Default: level,
				Providers: map[string]trust.ProviderConfig{
					"testprov": {TrustLevel: level},
				},
			}
			if err := cfg.Save(dir); err != nil {
				t.Fatal(err)
			}
			origTrust, err := os.ReadFile(filepath.Join(dir, "trust.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("AUXLY_PROVIDER", "testprov")

			s := NewServer(dir)
			for _, bad := range []string{"trust.yaml", "git.yaml", ".gitignore", ".git/config", "../x.md", "unified_memory.md"} {
				var buf bytes.Buffer
				s.outWriter = &buf
				reqArgs, _ := json.Marshal(map[string]interface{}{
					"name": "auxly_memory_write",
					"arguments": map[string]interface{}{
						"file":   bad,
						"diff":   "+default: auto",
						"reason": "test",
					},
				})
				s.handleToolCall(&jsonRPCRequest{ID: 1, Params: reqArgs})

				var resp struct {
					Result toolResult `json:"result"`
				}
				if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
					t.Fatalf("unmarshal response for %q: %v (%s)", bad, err, buf.String())
				}
				if !resp.Result.IsError {
					t.Fatalf("auxly_memory_write(%q) should return IsError=true; got: %s", bad, resultText(resp.Result))
				}
				if txt := resultText(resp.Result); !strings.Contains(txt, bad) && !strings.Contains(strings.ToLower(txt), "invalid target") {
					t.Fatalf("auxly_memory_write(%q) error text = %q, want error describing invalid target", bad, txt)
				}
			}

			// Verify trust.yaml was untouched and nothing was queued in .pending/.
			afterTrust, err := os.ReadFile(filepath.Join(dir, "trust.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(afterTrust) != string(origTrust) {
				t.Fatalf("trust.yaml was modified!\nbefore:\n%s\nafter:\n%s", origTrust, afterTrust)
			}
			if list, _ := s.pendingMgr.List(); len(list) != 0 {
				t.Fatalf("expected 0 pending entries, got: %+v", list)
			}
		})
	}
}
