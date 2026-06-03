package memory

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestOrganizeE2E_RealAgent_TempVault runs the FULL CLI-agent organize path
// (build argv → exec real agent → extractJSON → parse → write) against a tiny
// THROWAWAY temp vault. It never touches the user's real ~/.auxly/memory.
//
// Guarded: only runs when AUXLY_E2E=1 and AUXLY_E2E_AGENT/BIN are set, so it
// never fires in normal `go test ./...` (it makes a real, billable agent call).
//
//	AUXLY_E2E=1 AUXLY_E2E_AGENT="Antigravity CLI" AUXLY_E2E_BIN=agy \
//	  go test ./internal/memory/ -run TestOrganizeE2E_RealAgent_TempVault -v -timeout 180s
func TestOrganizeE2E_RealAgent_TempVault(t *testing.T) {
	if os.Getenv("AUXLY_E2E") != "1" {
		t.Skip("set AUXLY_E2E=1 (+ AUXLY_E2E_AGENT, AUXLY_E2E_BIN) to run the real-agent E2E")
	}
	agentName := os.Getenv("AUXLY_E2E_AGENT")
	bin := os.Getenv("AUXLY_E2E_BIN")
	path, err := exec.LookPath(bin)
	if err != nil {
		t.Fatalf("agent binary %q not on PATH: %v", bin, err)
	}

	store := NewStore(t.TempDir())
	if err := store.Write("identity.md", "# Identity\n- Name: Test User\n- Role: Engineer\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("projects.md", "# Projects\n- Project Zeta: a sample project\n- Name: Test User (duplicate fact)\n"); err != nil {
		t.Fatal(err)
	}

	res := store.OrganizeVaultWithAgent(agentName, path)
	t.Logf("Success=%v\nMessage=%s\nDiff=%s", res.Success, res.Message, res.Diff)

	// If this provider isn't yet on the verified-safe allowlist, it must be a safe
	// refusal, never a real (unsafe) run. The zero-loss/contamination checks below
	// are the acceptance gate that QUALIFIES a provider for the allowlist.
	if !organizeAgentSafe(agentName) {
		if res.Success || !strings.Contains(res.Message, "isn't available") {
			t.Fatalf("unverified agent must be safety-gated; got Success=%v msg=%s", res.Success, res.Message)
		}
		t.Logf("unverified agent correctly gated: %s", res.Message)
		return
	}

	if !res.Success {
		t.Fatalf("organize FAILED via %s: %s", agentName, res.Message)
	}

	// Zero-loss spot check: the distinct facts must survive the rewrite.
	all := ""
	files, _ := store.List()
	for _, f := range files {
		c, _ := store.View(f.Name)
		all += c + "\n"
	}
	for _, fact := range []string{"Test User", "Project Zeta", "Engineer"} {
		if !strings.Contains(all, fact) {
			t.Errorf("zero-loss violation: %q missing after organize", fact)
		}
	}
}
