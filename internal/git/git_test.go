package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
}

// TestAutoCommit_SkipsWhileTempDecryptSentinelExists is CRITICAL 1's
// regression: a concurrent write's AutoCommit must never stage the vault
// while organize's temp-decrypt sentinel says a file is plaintext on disk,
// or that plaintext gets baked into git history permanently.
func TestAutoCommit_SkipsWhileTempDecryptSentinelExists(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)

	if err := os.MkdirAll(filepath.Join(dir, ".index"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".index", "reencrypt-pending.json"), []byte(`{"files":["personal.md"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "personal.md"), []byte("- plaintext secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := AutoCommit(dir, "personal.md", "test write")
	if err == nil {
		t.Fatal("AutoCommit should refuse while the temp-decrypt sentinel exists")
	}
	if !strings.Contains(err.Error(), "temporary decrypt") {
		t.Fatalf("error = %v, want it to mention a temporary decrypt in progress", err)
	}

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = dir
	out, serr := statusCmd.Output()
	if serr != nil {
		t.Fatal(serr)
	}
	if strings.Contains(string(out), "A  personal.md") {
		t.Fatalf("AutoCommit staged personal.md despite the sentinel:\n%s", out)
	}
	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = dir
	if lo, lerr := logCmd.CombinedOutput(); lerr == nil {
		t.Fatalf("AutoCommit created a commit despite the sentinel:\n%s", lo)
	}
}

// TestSync_RefusesWhileTempDecryptSentinelExists mirrors the AutoCommit
// regression for `auxly sync`: Sync must return a real error (never a nil
// that lets the caller print "synced successfully" while actually skipping).
func TestSync_RefusesWhileTempDecryptSentinelExists(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".index"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".index", "reencrypt-pending.json"), []byte(`{"files":["personal.md"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Sync(dir)
	if err == nil {
		t.Fatal("Sync should refuse while the temp-decrypt sentinel exists")
	}
	if !strings.Contains(err.Error(), "temporary decrypt") {
		t.Fatalf("error = %v, want it to mention a temporary decrypt in progress", err)
	}
}

// TestAutoCommit_GitignoresIndexDir covers CRITICAL 1's second half: .index/
// (the sentinel + the semantic-index DB) must be excluded from the vault repo
// so a normal commit can never scoop it up.
func TestAutoCommit_GitignoresIndexDir(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "identity.md"), []byte("- x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AutoCommit(dir, "identity.md", "test"); err != nil {
		t.Fatalf("AutoCommit: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("expected AutoCommit to create/update a .gitignore: %v", err)
	}
	if !strings.Contains(string(data), ".index/") {
		t.Fatalf(".gitignore missing .index/ entry: %q", data)
	}
}
