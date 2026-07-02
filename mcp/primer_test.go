package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func primerServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("identity.md", "# Identity\n- Wael, CEO of Tzamun\n- Based in Riyadh\n- third line must not appear\n")
	write("preferences.md", "# Prefs\n- Go for CLIs\n- ponytail minimalism\n")
	recent := time.Now().Format("2006-01-02")
	old := time.Now().Add(-30 * 24 * time.Hour).Format("2006-01-02")
	write("daily.md", fmt.Sprintf("# Daily\n- [%s] shipped sprint train\n- [%s] ancient entry\n", recent, old))
	s := NewServer(dir)
	s.store.WorkspaceRoot = ""
	return s
}

// TestSessionPrimerSlices locks the primer's composition: 2 identity lines max,
// preferences present, only last-7d journal entries, and the whole block
// capped — an agent's first minute of grounding, not a vault dump.
func TestSessionPrimerSlices(t *testing.T) {
	s := primerServer(t)
	p := s.sessionPrimer()
	if !strings.Contains(p, "Wael, CEO") || !strings.Contains(p, "Riyadh") {
		t.Fatalf("identity lines missing:\n%s", p)
	}
	if strings.Contains(p, "third line must not appear") {
		t.Fatalf("identity slice not capped at 2:\n%s", p)
	}
	if !strings.Contains(p, "ponytail minimalism") {
		t.Fatalf("preferences missing:\n%s", p)
	}
	if !strings.Contains(p, "shipped sprint train") {
		t.Fatalf("recent journal entry missing:\n%s", p)
	}
	if strings.Contains(p, "ancient entry") {
		t.Fatalf("30-day-old journal entry leaked into the 7d slice:\n%s", p)
	}
	if len(p) > primerMaxChars+100 {
		t.Fatalf("primer exceeds cap: %d chars", len(p))
	}
}

// TestSessionPrimerRespectsACLAndEnv locks the gates: a remote peer's primer
// only carries granted files, and AUXLY_PRIMER=off yields nothing.
func TestSessionPrimerRespectsACLAndEnv(t *testing.T) {
	t.Run("env off", func(t *testing.T) {
		t.Setenv("AUXLY_PRIMER", "off")
		if p := primerServer(t).sessionPrimer(); p != "" {
			t.Fatalf("primer produced despite AUXLY_PRIMER=off: %q", p)
		}
	})

	t.Run("remote ACL", func(t *testing.T) {
		s := primerServer(t)
		s.isRemote = true // nil share → default: non-personal readable
		// identity.md is shared-tier so it may appear; verify a personal grant
		// boundary instead: add personal.md and ensure it never shows.
		if err := os.WriteFile(filepath.Join(s.memoryPath, "personal.md"), []byte("- PRIVATE FACT\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if p := s.sessionPrimer(); strings.Contains(p, "PRIVATE FACT") {
			t.Fatalf("primer leaked personal.md to a remote: %q", p)
		}
	})
}

// TestSkillInitCarriesPrimer locks the delivery: onboarding output ends with
// the auto-recalled primer.
func TestSkillInitCarriesPrimer(t *testing.T) {
	s := primerServer(t)
	out := resultText(s.toolSkillInit())
	if !strings.Contains(out, "SESSION PRIMER") || !strings.Contains(out, "Wael, CEO") {
		t.Fatalf("skill_init missing primer:\n%s", out)
	}
}
