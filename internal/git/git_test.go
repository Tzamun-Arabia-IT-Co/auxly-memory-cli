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

// renameToMain points HEAD at refs/heads/main regardless of the test
// runner's init.defaultBranch config, so SyncStatus's "main" fallback branch
// (see LoadConfig) always matches the local branch actually being pushed.
func renameToMain(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "symbolic-ref", "HEAD", "refs/heads/main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git symbolic-ref: %v\n%s", err, out)
	}
}

// TestSyncStatus_DistinguishesPushedFromNothingToPush is the TUI Ops panel's
// dependency: a nil error from Sync/SyncStatus covers BOTH "pushed new
// commits" and "nothing new to push" (git push exits 0 either way), so
// SyncResult.Pushed must actually tell them apart against a real remote.
func TestSyncStatus_DistinguishesPushedFromNothingToPush(t *testing.T) {
	remoteDir := t.TempDir()
	bare := exec.Command("git", "init", "--bare", "-q")
	bare.Dir = remoteDir
	if out, err := bare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	dir := t.TempDir()
	initTestRepo(t, dir)
	renameToMain(t, dir)
	addRemote := exec.Command("git", "remote", "add", "origin", remoteDir)
	addRemote.Dir = dir
	if out, err := addRemote.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.md"), []byte("- x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := SyncStatus(dir)
	if err != nil {
		t.Fatalf("first SyncStatus: %v", err)
	}
	if !res.Pushed {
		t.Error("first sync (a new commit to push) should report Pushed=true")
	}

	res2, err := SyncStatus(dir)
	if err != nil {
		t.Fatalf("second SyncStatus: %v", err)
	}
	if res2.Pushed {
		t.Error("second sync (nothing new since) should report Pushed=false")
	}
}

// TestSyncStatus_NoRemoteConfigured covers the "sync isn't configured" case
// the TUI Ops panel must show cleanly: a git repo with no "origin" remote.
func TestSyncStatus_NoRemoteConfigured(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "identity.md"), []byte("- x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := SyncStatus(dir)
	if err == nil {
		t.Fatal("SyncStatus without a configured remote should error")
	}
	if !strings.Contains(err.Error(), "git push failed") {
		t.Fatalf("error = %v, want a git push failure (no origin remote)", err)
	}
}
