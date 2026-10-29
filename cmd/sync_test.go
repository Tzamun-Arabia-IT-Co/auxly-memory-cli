package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
)

func execAuxlyCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	defer func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		cfgPath = ""
	}()
	err := rootCmd.Execute()
	return buf.String(), err
}

func initBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return dir
}

func TestSyncSetupCLI_StopsOnSensitiveFindingsWithoutYes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	restore := git.SetGHStubsForTest(
		func() bool { return false },
		func(context.Context, string) (string, error) { return "", errors.New("no gh") },
		func(context.Context, string) (bool, bool) { return false, false },
	)
	defer restore()

	bareDir := initBareRemote(t)
	vaultDir := t.TempDir()
	secret := "sk-live-9876543210abcdefghijklmnop"
	if err := os.WriteFile(filepath.Join(vaultDir, "infra.md"), []byte("- token "+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := execAuxlyCLI(t, "--path", vaultDir, "sync", "setup", bareDir)
	if err == nil {
		t.Fatalf("expected non-zero exit when findings exist without --yes, output:\n%s", out)
	}
	if !strings.Contains(out, "infra.md") {
		t.Errorf("expected pre-push report to name infra.md, got:\n%s", out)
	}
	if strings.Contains(out, secret) || strings.Contains(err.Error(), secret) {
		t.Fatal("CLI output or error leaked the secret line")
	}

	// Nothing pushed to bare remote.
	refsCmd := exec.Command("git", "for-each-ref")
	refsCmd.Dir = bareDir
	refsOut, _ := refsCmd.Output()
	if strings.TrimSpace(string(refsOut)) != "" {
		t.Fatalf("expected nothing pushed without --yes, got refs:\n%s", refsOut)
	}

	// Passing --yes allows setup to complete and push.
	outYes, errYes := execAuxlyCLI(t, "--path", vaultDir, "sync", "setup", bareDir, "--yes")
	if errYes != nil {
		t.Fatalf("sync setup --yes failed: %v\n%s", errYes, outYes)
	}
	refsCmd2 := exec.Command("git", "for-each-ref")
	refsCmd2.Dir = bareDir
	refsOut2, _ := refsCmd2.Output()
	if !strings.Contains(string(refsOut2), "refs/heads/main") {
		t.Fatalf("expected refs/heads/main in bare remote after --yes, got:\n%s", refsOut2)
	}
}

func TestSyncSetupCLI_PublicRepoRequiresAllowPublic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	restore := git.SetGHStubsForTest(
		func() bool { return true },
		nil,
		func(context.Context, string) (bool, bool) { return false, true }, // public
	)
	defer restore()

	bareDir := initBareRemote(t)
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := execAuxlyCLI(t, "--path", vaultDir, "sync", "setup", bareDir, "--yes")
	if err == nil {
		t.Fatalf("expected public repo setup without --allow-public to fail, got:\n%s", out)
	}
	if !strings.Contains(out, "PUBLIC") || !strings.Contains(out, "--allow-public") {
		t.Errorf("expected PUBLIC / --allow-public notice in output:\n%s", out)
	}

	outAllow, errAllow := execAuxlyCLI(t, "--path", vaultDir, "sync", "setup", bareDir, "--allow-public")
	if errAllow != nil {
		t.Fatalf("sync setup --allow-public failed: %v\n%s", errAllow, outAllow)
	}
}

func TestSyncCLI_CreateGitHubAutoAndStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	bareDir := initBareRemote(t)
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "preferences.md"), []byte("- dark mode\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := git.SetGHStubsForTest(
		func() bool { return true },
		func(_ context.Context, name string) (string, error) {
			if name != "my-private-vault" {
				t.Errorf("create name = %q, want my-private-vault", name)
			}
			return bareDir, nil
		},
		func(context.Context, string) (bool, bool) { return true, true },
	)
	defer restore()

	out, err := execAuxlyCLI(t, "--path", vaultDir, "sync", "setup", "--create-github", "my-private-vault")
	if err != nil {
		t.Fatalf("sync setup --create-github: %v\n%s", err, out)
	}

	if outAuto, errAuto := execAuxlyCLI(t, "--path", vaultDir, "sync", "auto", "on"); errAuto != nil || !strings.Contains(outAuto, "ON") {
		t.Fatalf("sync auto on: err=%v out=%q", errAuto, outAuto)
	}

	// `sync auto on` wrote git.yaml, so Dirty files is 1 until backed up.
	statusOut, errStatus := execAuxlyCLI(t, "--path", vaultDir, "sync", "status")
	if errStatus != nil {
		t.Fatalf("sync status: %v\n%s", errStatus, statusOut)
	}
	for _, want := range []string{bareDir, "main", "ON", "Unpushed commits: 0", "Dirty files:      1"} {
		if !strings.Contains(statusOut, want) {
			t.Errorf("sync status missing %q:\n%s", want, statusOut)
		}
	}

	// `auxly sync` commits + pushes git.yaml, returning Dirty files to 0.
	if outSync, errSync := execAuxlyCLI(t, "--path", vaultDir, "sync"); errSync != nil {
		t.Fatalf("sync: %v\n%s", errSync, outSync)
	}
	statusClean, errClean := execAuxlyCLI(t, "--path", vaultDir, "sync", "status")
	if errClean != nil {
		t.Fatalf("sync status after sync: %v\n%s", errClean, statusClean)
	}
	if !strings.Contains(statusClean, "Dirty files:      0") {
		t.Errorf("expected Dirty files: 0 after sync:\n%s", statusClean)
	}

	if outOff, errOff := execAuxlyCLI(t, "--path", vaultDir, "sync", "auto", "off"); errOff != nil || !strings.Contains(outOff, "OFF") {
		t.Fatalf("sync auto off: err=%v out=%q", errOff, outOff)
	}
}

func TestSyncSetupCLI_StopsOnExistingCommitsWithoutYes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	restore := git.SetGHStubsForTest(
		func() bool { return false },
		func(context.Context, string) (string, error) { return "", errors.New("no gh") },
		func(context.Context, string) (bool, bool) { return false, false },
	)
	defer restore()

	bareDir := initBareRemote(t)
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- prior commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "identity.md"},
		{"commit", "-q", "-m", "prior"},
	} {
		c := exec.Command("git", args...)
		c.Dir = vaultDir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	out, err := execAuxlyCLI(t, "--path", vaultDir, "sync", "setup", bareDir)
	if err == nil {
		t.Fatalf("expected sync setup to block on existing commits without --yes, got:\n%s", out)
	}
	if !strings.Contains(out, "1 earlier commits will be pushed as they are; older commits are not checked for secrets") {
		t.Errorf("CLI output missing existing-commits warning:\n%s", out)
	}

	outYes, errYes := execAuxlyCLI(t, "--path", vaultDir, "sync", "setup", bareDir, "--yes")
	if errYes != nil {
		t.Fatalf("sync setup --yes failed: %v\n%s", errYes, outYes)
	}
}
