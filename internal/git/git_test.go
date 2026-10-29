package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
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

func initBareRepo(t *testing.T) string {
	t.Helper()
	remoteDir := t.TempDir()
	bare := exec.Command("git", "init", "--bare", "-q")
	bare.Dir = remoteDir
	if out, err := bare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return remoteDir
}

func stubNoGH(t *testing.T) {
	t.Helper()
	restore := SetGHStubsForTest(
		func() bool { return false },
		func(context.Context, string) (string, error) { return "", errors.New("gh unavailable in test") },
		func(context.Context, string) (bool, bool) { return false, false },
	)
	t.Cleanup(restore)
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
	remoteDir := initBareRepo(t)

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

func TestValidateRemoteURL(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantOK      bool
		wantAuthMsg bool
	}{
		{name: "scp ssh", raw: "git@github.com:owner/auxly-vault.git", wantOK: true},
		{name: "ssh scheme", raw: "ssh://git@github.com/owner/auxly-vault.git", wantOK: true},
		{name: "ssh scheme with port", raw: "ssh://deploy@git.example.com:2222/owner/vault.git", wantOK: true},
		{name: "https clean", raw: "https://github.com/owner/auxly-vault.git", wantOK: true},
		{name: "file url", raw: "file:///private/tmp/auxly-bare.git", wantOK: true},
		{name: "abs local path", raw: "/private/tmp/auxly-bare.git", wantOK: true},

		{name: "https user and token", raw: "https://x:" + fakeGitHubToken + "@github.com/owner/repo.git", wantOK: false, wantAuthMsg: true},
		{name: "https bare token user", raw: "https://TOKEN@github.com/owner/repo.git", wantOK: false, wantAuthMsg: true},
		{name: "https pat user", raw: "https://github_pat_11ABCDEF0123456789_abcdefghijklmnop@github.com/owner/repo.git", wantOK: false, wantAuthMsg: true},
		{name: "ssh password", raw: "ssh://git:hunter2@github.com/owner/repo.git", wantOK: false, wantAuthMsg: true},
		{name: "scp password", raw: "git:hunter2@github.com:owner/repo.git", wantOK: false, wantAuthMsg: true},
		{name: "scp token user", raw: fakeGitHubToken + "@github.com:owner/repo.git", wantOK: false, wantAuthMsg: true},
		{name: "https query token", raw: "https://github.com/owner/repo.git?token=secret", wantOK: false, wantAuthMsg: true},

		{name: "insecure http", raw: "http://github.com/owner/repo.git", wantOK: false},
		{name: "relative path", raw: "relative/repo.git", wantOK: false},
		{name: "option injection", raw: "--upload-pack=evil", wantOK: false},
		{name: "scp host with leading dash", raw: "git@-oProxyCommand=evil:owner/repo.git", wantOK: false},
		{name: "scp path with leading dash", raw: "git@github.com:-upload-pack=evil", wantOK: false},
		{name: "ssh host with leading dash", raw: "ssh://-oProxyCommand=evil/owner/repo.git", wantOK: false},
		{name: "ssh host with user and leading dash", raw: "ssh://git@-oProxyCommand=evil/owner/repo.git", wantOK: false},
		{name: "ssh path with leading dash", raw: "ssh://git@github.com/-upload-pack=evil", wantOK: false},
		{name: "empty", raw: "   ", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateRemoteURL(tc.raw)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("ValidateRemoteURL(%q) unexpected error: %v", tc.raw, err)
				}
				if got != strings.TrimSpace(tc.raw) {
					t.Fatalf("ValidateRemoteURL(%q) = %q, want %q", tc.raw, got, strings.TrimSpace(tc.raw))
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateRemoteURL(%q) succeeded with %q, want error", tc.raw, got)
			}
			if tc.wantAuthMsg {
				if !strings.Contains(err.Error(), "gh auth login") || !strings.Contains(err.Error(), "SSH key") {
					t.Fatalf("error = %q, want guidance mentioning `gh auth login` and SSH key", err.Error())
				}
			}
		})
	}
}

func TestMergeGitignore_KeepsUserLinesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	seed := "# user comment\ncustom-notes.tmp\n.index\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeGitignore(dir); err != nil {
		t.Fatalf("first mergeGitignore: %v", err)
	}
	firstBytes, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	first := string(firstBytes)

	if !strings.HasPrefix(first, seed) {
		t.Fatalf(".gitignore did not preserve user lines at top:\n%s", first)
	}
	if strings.Contains(first, ".index/") {
		t.Fatalf(".index was already covered by user line, should not duplicate .index/:\n%s", first)
	}
	for _, want := range []string{
		".pending/",
		".backup/",
		".archive/",
		".audit.log",
		"audit.db",
		"audit.db-shm",
		"audit.db-wal",
		".capture-last*",
		".git-backup-last-error",
		".DS_Store",
		"*.bak-*",
		"*.damaged-*",
	} {
		if !strings.Contains(first, want+"\n") {
			t.Errorf(".gitignore missing %q:\n%s", want, first)
		}
	}

	if err := mergeGitignore(dir); err != nil {
		t.Fatalf("second mergeGitignore: %v", err)
	}
	secondBytes, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(secondBytes) != first {
		t.Fatalf("second mergeGitignore was not idempotent:\ngot:\n%s\nwant:\n%s", secondBytes, first)
	}
}

func TestSetup_EndToEndLocalBareRepo(t *testing.T) {
	stubNoGH(t)
	// Ensure git has no global identity in this isolated HOME so gitCommit's
	// fallback (-c user.name=Auxly -c user.email=auxly@localhost) is exercised.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- Prefer concise Go tests\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Bookkeeping files that must be ignored rather than pushed.
	if err := os.MkdirAll(filepath.Join(vaultDir, ".pending"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".pending", "p1.diff"), []byte("+ x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "audit.db"), []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{})
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}
	if !res.Pushed {
		t.Error("expected Setup to push initial backup commit")
	}
	if res.Branch != "main" {
		t.Errorf("Branch = %q, want main", res.Branch)
	}

	// Verify commit landed in the bare remote on refs/heads/main.
	showCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "refs/heads/main")
	showCmd.Dir = bareDir
	treeOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare ls-tree: %v\n%s", err, treeOut)
	}
	files := string(treeOut)
	if !strings.Contains(files, "identity.md") || !strings.Contains(files, ".gitignore") {
		t.Fatalf("remote tree missing expected files:\n%s", files)
	}
	if strings.Contains(files, "audit.db") || strings.Contains(files, ".pending") {
		t.Fatalf("remote tree leaked ignored files:\n%s", files)
	}

	// Verify upstream tracking branch is set (`@{u}` resolves cleanly).
	upCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	upCmd.Dir = vaultDir
	upOut, err := upCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("upstream not set after Setup: %v\n%s", err, upOut)
	}
	if strings.TrimSpace(string(upOut)) != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", strings.TrimSpace(string(upOut)))
	}

	st := BackupStatus(vaultDir)
	if !st.Configured || st.RemoteURL != bareDir || st.UnpushedCount != 0 || st.DirtyFileCount != 0 {
		t.Fatalf("unexpected BackupStatus after clean Setup: %+v", st)
	}
	if st.LastSuccess.IsZero() {
		t.Error("expected Setup to record LastSuccess")
	}
}

func TestPrePushReportAndSetup_BlocksPlaintextSecretWithoutConfirmed(t *testing.T) {
	stubNoGH(t)
	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()

	secretLine := "api key is sk-abcdefghijklmnopqrstuvwxyz123456"
	if err := os.WriteFile(filepath.Join(vaultDir, "infra.md"), []byte("- normal line\n- "+secretLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An age-encrypted file containing a secret in plaintext MUST NOT be flagged.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := vaultcrypt.Encrypt([]byte("- "+secretLine+"\n"), id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "personal.md"), ct, 0o600); err != nil {
		t.Fatal(err)
	}

	rep := PrePushReport(context.Background(), vaultDir, bareDir)
	if len(rep.SensitiveFiles) != 1 {
		t.Fatalf("SensitiveFiles = %+v, want 1 entry (infra.md only)", rep.SensitiveFiles)
	}
	if rep.SensitiveFiles[0].File != "infra.md" || rep.SensitiveFiles[0].Count != 1 {
		t.Fatalf("SensitiveFiles[0] = %+v, want {File:infra.md Count:1}", rep.SensitiveFiles[0])
	}

	res, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{})
	if !errors.Is(err, ErrPrePushBlocked) {
		t.Fatalf("Setup error = %v, want ErrPrePushBlocked", err)
	}
	if strings.Contains(err.Error(), secretLine) {
		t.Fatal("error leaked secret content")
	}
	if !res.Blocked || len(res.Report.SensitiveFiles) != 1 {
		t.Fatalf("SetupResult = %+v, want Blocked=true with SensitiveFiles", res)
	}

	// Confirm nothing was pushed to bareDir.
	refsCmd := exec.Command("git", "for-each-ref")
	refsCmd.Dir = bareDir
	refsOut, _ := refsCmd.Output()
	if strings.TrimSpace(string(refsOut)) != "" {
		t.Fatalf("expected nothing pushed when blocked, got refs:\n%s", refsOut)
	}

	// With Confirmed=true, Setup proceeds and pushes.
	res2, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{Confirmed: true})
	if err != nil {
		t.Fatalf("Setup with Confirmed=true failed: %v", err)
	}
	if !res2.Pushed {
		t.Error("expected push to succeed once Confirmed=true")
	}
}

func TestSetup_PublicRepoBlocksWithoutAllowPublic(t *testing.T) {
	restore := SetGHStubsForTest(
		func() bool { return true },
		nil,
		func(ctx context.Context, remoteURL string) (bool, bool) {
			return false, true // known public!
		},
	)
	defer restore()

	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- safe fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Even with Confirmed=true, a public repo must block unless AllowPublic=true.
	res, err := Setup(context.Background(), vaultDir, "https://github.com/owner/public-vault.git", SetupOptions{Confirmed: true})
	if !errors.Is(err, ErrPrePushBlocked) {
		t.Fatalf("Setup error = %v, want ErrPrePushBlocked for public repo", err)
	}
	if !res.Report.IsPublic() {
		t.Fatalf("Report.Visibility = %q, want public", res.Report.Visibility)
	}
	if isGitRepo(vaultDir) {
		t.Fatal("blocked public Setup should stop before initializing/pushing repo")
	}
}

func TestCreateGitHubAndSetup_StubbedGH(t *testing.T) {
	bareDir := initBareRepo(t)
	createdName := ""
	restore := SetGHStubsForTest(
		func() bool { return true },
		func(ctx context.Context, name string) (string, error) {
			createdName = name
			return bareDir, nil
		},
		func(ctx context.Context, remoteURL string) (bool, bool) {
			return true, true
		},
	)
	defer restore()

	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "preferences.md"), []byte("- vim keys\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := CreateGitHubAndSetup(context.Background(), vaultDir, "auxly-vault", SetupOptions{})
	if err != nil {
		t.Fatalf("CreateGitHubAndSetup: %v", err)
	}
	if createdName != "auxly-vault" {
		t.Errorf("ghCreatePrivate called with %q, want auxly-vault", createdName)
	}
	if !res.Pushed {
		t.Error("expected initial backup push to succeed")
	}
}

func TestSetup_AuthFailureReturnsHelpfulLoginMessage(t *testing.T) {
	stubNoGH(t)
	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "push" {
			return []byte("fatal: Authentication failed for 'https://github.com/owner/vault.git/'"), errors.New("exit status 128")
		}
		return origRunGit(ctx, dir, timeout, args...)
	}

	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Setup(context.Background(), vaultDir, "https://github.com/owner/vault.git", SetupOptions{})
	if err == nil {
		t.Fatal("expected auth failure error")
	}
	want := "git couldn't log in to github.com. Run `gh auth login` (GitHub) or add an SSH key, then try again."
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  = %q", err.Error(), want)
	}
}

func TestTempDecryptInFlight_BlocksSetupAndBackupNow(t *testing.T) {
	stubNoGH(t)
	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".index"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".index", "reencrypt-pending.json"), []byte(`{"files":["personal.md"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "personal.md"), []byte("- decrypted secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{Confirmed: true}); err == nil || !strings.Contains(err.Error(), "temporary decrypt") {
		t.Fatalf("Setup should refuse while tempDecryptInFlight, got: %v", err)
	}

	initTestRepo(t, vaultDir)
	renameToMain(t, vaultDir)
	addRemote := exec.Command("git", "remote", "add", "origin", bareDir)
	addRemote.Dir = vaultDir
	if out, err := addRemote.CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v\n%s", err, out)
	}

	if _, err := BackupNow(context.Background(), vaultDir); err == nil || !strings.Contains(err.Error(), "temporary decrypt") {
		t.Fatalf("BackupNow should refuse while tempDecryptInFlight, got: %v", err)
	}
}

func TestMaybeAutoBackup_NoOpWhenOffAndRateLimited(t *testing.T) {
	stubNoGH(t)
	origNow := nowFunc
	defer func() {
		autoBackupWG.Wait()
		nowFunc = origNow
	}()
	cur := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return cur }

	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Write a new change while auto_push is OFF (default): MaybeAutoBackup must be a no-op.
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	MaybeAutoBackup(vaultDir)
	autoBackupWG.Wait()

	if st := BackupStatus(vaultDir); st.DirtyFileCount == 0 {
		t.Fatal("MaybeAutoBackup pushed/committed even though auto_push is false")
	}

	// Turn auto_push ON and advance clock past Setup's initial timestamp.
	if err := SaveConfig(vaultDir, &Config{AutoCommit: true, AutoPush: true, CommitMessagePrefix: "auxly:", Branch: "main"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	cur = cur.Add(3 * time.Minute)

	MaybeAutoBackup(vaultDir)
	autoBackupWG.Wait()
	if st := BackupStatus(vaultDir); st.DirtyFileCount != 0 || st.UnpushedCount != 0 {
		t.Fatalf("first MaybeAutoBackup should have committed + pushed v2, got status: %+v", st)
	}

	// Second call within 2 minutes (30 s later) must do nothing.
	cur = cur.Add(30 * time.Second)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	MaybeAutoBackup(vaultDir)
	autoBackupWG.Wait()
	if st := BackupStatus(vaultDir); st.DirtyFileCount == 0 {
		t.Fatal("second MaybeAutoBackup within 2 minutes should have been rate-limited")
	}

	// Once 2 minutes have elapsed since the first attempt, MaybeAutoBackup pushes v3.
	cur = cur.Add(2 * time.Minute)
	MaybeAutoBackup(vaultDir)
	autoBackupWG.Wait()
	if st := BackupStatus(vaultDir); st.DirtyFileCount != 0 || st.UnpushedCount != 0 {
		t.Fatalf("MaybeAutoBackup after 2-min window should push v3, got: %+v", st)
	}
}

func TestStripCredentials_InBackupStatus(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)
	renameToMain(t, dir)
	addRemote := exec.Command("git", "remote", "add", "origin", "https://x:ghp_secret123456789012345678901234@github.com/owner/vault.git")
	addRemote.Dir = dir
	if out, err := addRemote.CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v\n%s", err, out)
	}

	st := BackupStatus(dir)
	if strings.Contains(st.RemoteURL, "ghp_secret") || strings.Contains(st.RemoteURL, "x:") {
		t.Fatalf("BackupStatus leaked credentials in RemoteURL: %q", st.RemoteURL)
	}
	if st.RemoteURL != "https://github.com/owner/vault.git" {
		t.Fatalf("RemoteURL = %q, want https://github.com/owner/vault.git", st.RemoteURL)
	}
}

func TestValidBranchName(t *testing.T) {
	cases := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"release/1.6", true},
		{"feature-x", true},
		{"v1_0.2", true},
		{strings.Repeat("a", 100), true},

		{"", false},
		{"-x", false},
		{"--receive-pack=evil", false},
		{"a..b", false},
		{"a/", false},
		{"a.", false},
		{"x.lock", false},
		{"a//b", false},
		{"@{u}", false},
		{"a@{u}", false},
		{".hidden", false},
		{"/lead", false},
		{strings.Repeat("a", 101), false},
	}
	for _, tc := range cases {
		if got := ValidBranchName(tc.branch); got != tc.want {
			t.Errorf("ValidBranchName(%q) = %v, want %v", tc.branch, got, tc.want)
		}
	}
}

func TestLoadAndSaveConfig_InvalidBranchFailsClosedWithoutRunningGit(t *testing.T) {
	stubNoGH(t)
	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	gitCalls := 0
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		gitCalls++
		t.Fatalf("unexpected git invocation with invalid branch config: %v", args)
		return nil, errors.New("should not run")
	}

	vaultDir := t.TempDir()
	bareDir := t.TempDir()
	badBranch := "--receive-pack=touch /tmp/pwned"
	rawYAML := "auto_commit: true\nauto_push: true\nbranch: " + `"` + badBranch + `"` + "\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "git.yaml"), []byte(rawYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(vaultDir)
	if err == nil {
		t.Fatalf("LoadConfig succeeded with %+v, want error", cfg)
	}
	if !strings.Contains(err.Error(), "git.yaml") || !strings.Contains(err.Error(), badBranch) {
		t.Fatalf("LoadConfig error = %q, want it to name git.yaml and %q", err.Error(), badBranch)
	}

	if _, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{Confirmed: true}); err == nil {
		t.Fatal("Setup should fail when git.yaml has an invalid branch")
	}
	if err := Sync(vaultDir); err == nil {
		t.Fatal("Sync should fail when git.yaml has an invalid branch")
	}
	if _, err := BackupNow(context.Background(), vaultDir); err == nil {
		t.Fatal("BackupNow should fail when git.yaml has an invalid branch")
	}
	if err := AutoCommit(vaultDir, "identity.md", "test"); err == nil {
		t.Fatal("AutoCommit should fail when git.yaml has an invalid branch")
	}
	if gitCalls != 0 {
		t.Fatalf("gitCalls = %d, want 0", gitCalls)
	}

	// SaveConfig must also refuse invalid branch names.
	saveDir := t.TempDir()
	if err := SaveConfig(saveDir, &Config{Branch: "--receive-pack=evil"}); err == nil {
		t.Fatal("SaveConfig should refuse an invalid branch")
	}
	if _, err := os.Stat(filepath.Join(saveDir, "git.yaml")); err == nil {
		t.Fatal("SaveConfig wrote git.yaml despite invalid branch")
	}

	// Empty branch in git.yaml still defaults to "main".
	if err := os.WriteFile(filepath.Join(saveDir, "git.yaml"), []byte("auto_commit: true\nbranch: \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defCfg, err := LoadConfig(saveDir)
	if err != nil {
		t.Fatalf("LoadConfig empty branch: %v", err)
	}
	if defCfg.Branch != "main" {
		t.Fatalf("LoadConfig empty branch = %q, want main", defCfg.Branch)
	}
}

func TestSetup_PushesViaRefspecAndRemoteBranchExists(t *testing.T) {
	stubNoGH(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	var seen [][]string
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		cp := append([]string(nil), args...)
		seen = append(seen, cp)
		return origRunGit(ctx, dir, timeout, args...)
	}

	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	if err := SaveConfig(vaultDir, &Config{AutoCommit: true, Branch: "release/1.6"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- refspec test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !res.Pushed || res.Branch != "release/1.6" {
		t.Fatalf("unexpected SetupResult: %+v", res)
	}

	sawInitPlain := false
	sawSymbolicRef := false
	sawSetupRefspec := false
	for _, args := range seen {
		if len(args) >= 2 && args[0] == "init" && args[1] == "-b" {
			t.Fatalf("Setup must not call `git init -b`: %v", args)
		}
		if len(args) == 1 && args[0] == "init" {
			sawInitPlain = true
		}
		if len(args) == 3 && args[0] == "symbolic-ref" && args[1] == "HEAD" && args[2] == "refs/heads/release/1.6" {
			sawSymbolicRef = true
		}
		if len(args) == 4 && args[0] == "push" && args[1] == "-u" && args[2] == "origin" && args[3] == "HEAD:refs/heads/release/1.6" {
			sawSetupRefspec = true
		}
	}
	if !sawInitPlain || !sawSymbolicRef || !sawSetupRefspec {
		t.Fatalf("missing expected git commands (init=%v symbolic-ref=%v push-refspec=%v), saw: %v",
			sawInitPlain, sawSymbolicRef, sawSetupRefspec, seen)
	}

	showRef := exec.Command("git", "show-ref", "--verify", "refs/heads/release/1.6")
	showRef.Dir = bareDir
	if out, err := showRef.CombinedOutput(); err != nil {
		t.Fatalf("remote branch refs/heads/release/1.6 missing: %v\n%s", err, out)
	}

	// Subsequent SyncStatus must also push via explicit refspec.
	seen = nil
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- refspec test 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncStatus(vaultDir); err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	sawSyncRefspec := false
	for _, args := range seen {
		if len(args) == 3 && args[0] == "push" && args[1] == "origin" && args[2] == "HEAD:refs/heads/release/1.6" {
			sawSyncRefspec = true
		}
	}
	if !sawSyncRefspec {
		t.Fatalf("SyncStatus did not push via HEAD:refs/heads/release/1.6; saw: %v", seen)
	}
}

func TestSetup_BlocksExistingCommitsAndUntracksBookkeepingFiles(t *testing.T) {
	stubNoGH(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	initTestRepo(t, vaultDir)
	renameToMain(t, vaultDir)

	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- safe note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "audit.db"), []byte("sqlite-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".audit.log"), []byte("audit-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	gitRun(vaultDir, "add", "-f", "identity.md", "audit.db", ".audit.log")
	gitRun(vaultDir, "commit", "-q", "-m", "earlier manual commit")

	// Blocked without Confirmed (even when AllowPublic is true).
	res, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{AllowPublic: true})
	if !errors.Is(err, ErrPrePushBlocked) {
		t.Fatalf("Setup error = %v, want ErrPrePushBlocked", err)
	}
	if !res.Blocked || res.Report.ExistingCommits != 1 {
		t.Fatalf("SetupResult = %+v, want Blocked=true and ExistingCommits=1", res)
	}
	if !strings.Contains(err.Error(), "1 earlier commits will be pushed as they are; older commits are not checked for secrets") {
		t.Fatalf("unexpected block error message: %v", err)
	}
	if refs := strings.TrimSpace(gitRun(bareDir, "for-each-ref")); refs != "" {
		t.Fatalf("nothing should be pushed when blocked, got:\n%s", refs)
	}

	// Confirmed=true clears ExistingCommits, untracks audit.db and .audit.log, keeps them on disk, and pushes clean tip.
	res2, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{Confirmed: true})
	if err != nil {
		t.Fatalf("Setup with Confirmed=true: %v", err)
	}
	if !res2.Pushed {
		t.Fatal("expected Setup with Confirmed=true to push")
	}

	tracked := gitRun(vaultDir, "ls-files")
	if strings.Contains(tracked, "audit.db") || strings.Contains(tracked, ".audit.log") {
		t.Fatalf("audit.db / .audit.log still tracked in git ls-files:\n%s", tracked)
	}
	if !strings.Contains(tracked, "identity.md") {
		t.Fatalf("identity.md missing from git ls-files:\n%s", tracked)
	}

	// Working-tree files must remain on disk.
	if data, err := os.ReadFile(filepath.Join(vaultDir, "audit.db")); err != nil || string(data) != "sqlite-bytes" {
		t.Fatalf("audit.db missing or altered on disk: err=%v data=%q", err, data)
	}
	if data, err := os.ReadFile(filepath.Join(vaultDir, ".audit.log")); err != nil || string(data) != "audit-line\n" {
		t.Fatalf(".audit.log missing or altered on disk: err=%v data=%q", err, data)
	}

	// Pushed tip on bare remote must not contain audit.db or .audit.log.
	remoteTip := gitRun(bareDir, "ls-tree", "-r", "--name-only", "refs/heads/main")
	if strings.Contains(remoteTip, "audit.db") || strings.Contains(remoteTip, ".audit.log") {
		t.Fatalf("pushed remote tip still contains bookkeeping files:\n%s", remoteTip)
	}
	if !strings.Contains(remoteTip, "identity.md") {
		t.Fatalf("pushed remote tip missing identity.md:\n%s", remoteTip)
	}
}

func TestSync_UntracksAlreadyTrackedIgnoredFiles(t *testing.T) {
	stubNoGH(t)
	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	initTestRepo(t, vaultDir)
	renameToMain(t, vaultDir)

	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	gitRun(vaultDir, "remote", "add", "origin", bareDir)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "audit.db"), []byte("db-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".audit.log"), []byte("log-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(vaultDir, "add", "-f", "identity.md", "audit.db", ".audit.log")
	gitRun(vaultDir, "commit", "-q", "-m", "seed with tracked audit files")

	if _, err := BackupNow(context.Background(), vaultDir); err != nil {
		t.Fatalf("BackupNow: %v", err)
	}

	tracked := gitRun(vaultDir, "ls-files")
	if strings.Contains(tracked, "audit.db") || strings.Contains(tracked, ".audit.log") {
		t.Fatalf("sync path left audit files tracked:\n%s", tracked)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "audit.db")); err != nil {
		t.Fatalf("audit.db deleted from disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, ".audit.log")); err != nil {
		t.Fatalf(".audit.log deleted from disk: %v", err)
	}
	remoteTip := gitRun(bareDir, "ls-tree", "-r", "--name-only", "refs/heads/main")
	if strings.Contains(remoteTip, "audit.db") || strings.Contains(remoteTip, ".audit.log") {
		t.Fatalf("pushed tip after BackupNow contains audit files:\n%s", remoteTip)
	}
}

func TestSync_CommitFailureWithExistingHeadReturnsErrorAndDoesNotPush(t *testing.T) {
	stubNoGH(t)
	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	initTestRepo(t, vaultDir)
	renameToMain(t, vaultDir)

	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	gitRun(vaultDir, "remote", "add", "origin", bareDir)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(vaultDir, "add", "-A")
	gitRun(vaultDir, "commit", "-m", "initial commit")
	gitRun(vaultDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "commit" {
			_ = os.WriteFile(filepath.Join(dir, ".git", "index.lock"), []byte("locked"), 0o644)
		}
		return origRunGit(ctx, dir, timeout, args...)
	}

	res, err := SyncStatus(vaultDir)
	if err == nil {
		t.Fatal("SyncStatus should fail when commit fails due to index.lock")
	}
	if !strings.Contains(err.Error(), "git commit failed") {
		t.Fatalf("expected git commit failed error, got: %v", err)
	}
	if res.Pushed {
		t.Fatal("commit failed, so nothing should have been pushed")
	}

	tipContent := gitRun(vaultDir, "show", "origin/main:identity.md")
	if strings.Contains(tipContent, "modified") {
		t.Fatal("remote received commit despite commit failure")
	}
}

func TestAutoCommit_CommitFailureReturnsError(t *testing.T) {
	vaultDir := t.TempDir()
	initTestRepo(t, vaultDir)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "commit" {
			_ = os.WriteFile(filepath.Join(dir, ".git", "index.lock"), []byte("locked"), 0o644)
		}
		return origRunGit(ctx, dir, timeout, args...)
	}

	err := AutoCommit(vaultDir, "identity.md", "testing failure")
	if err == nil {
		t.Fatal("AutoCommit should return error when git commit fails")
	}
	if !strings.Contains(err.Error(), "git commit failed") {
		t.Fatalf("expected git commit failed error, got: %v", err)
	}
}

func TestCreateGitHubAndSetup_PrePushBlockedCreatesNothing(t *testing.T) {
	vaultDir := t.TempDir()
	initTestRepo(t, vaultDir)
	if err := os.WriteFile(filepath.Join(vaultDir, "infra.md"), []byte("- api key sk-live-1234567890abcdefghijklmnop\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ghCalled := false
	restore := SetGHStubsForTest(
		func() bool { return true },
		func(ctx context.Context, name string) (string, error) {
			ghCalled = true
			return "/tmp/fake.git", nil
		},
		func(ctx context.Context, remoteURL string) (bool, bool) {
			return true, true
		},
	)
	defer restore()

	res, err := CreateGitHubAndSetup(context.Background(), vaultDir, "my-secret-vault", SetupOptions{Confirmed: false})
	if !errors.Is(err, ErrPrePushBlocked) {
		t.Fatalf("expected ErrPrePushBlocked, got: %v", err)
	}
	if !res.Blocked {
		t.Fatal("expected res.Blocked to be true")
	}
	if !res.Report.HasSensitiveFiles() {
		t.Fatal("expected report to have sensitive files")
	}
	if ghCalled {
		t.Fatal("ghCreatePrivate was called despite pre-push check failure; blocked run must create nothing")
	}
}

func TestCreateGitHubAndSetup_ReusesExistingRepo(t *testing.T) {
	bareDir := initBareRepo(t)
	vaultDir := t.TempDir()
	initTestRepo(t, vaultDir)
	renameToMain(t, vaultDir)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- clean fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origRunGH := runGH
	defer func() { runGH = origRunGH }()

	runGH = func(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "create" {
			return []byte("GraphQL: Name already exists on this account (createRepository)\n"), errors.New("exit status 1")
		}
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for i, a := range args {
				if a == "--json" && i+1 < len(args) && strings.Contains(args[i+1], "isPrivate") {
					return []byte(`{"isPrivate": true}`), nil
				}
				if a == "--json" && i+1 < len(args) && strings.Contains(args[i+1], "sshUrl") {
					return []byte(fmt.Sprintf(`{"sshUrl": "%s", "url": "%s"}`, bareDir, bareDir)), nil
				}
			}
		}
		return origRunGH(ctx, timeout, args...)
	}

	res, err := CreateGitHubAndSetup(context.Background(), vaultDir, "existing-vault", SetupOptions{Confirmed: true})
	if err != nil {
		t.Fatalf("CreateGitHubAndSetup failed to reuse existing repo: %v", err)
	}
	if !res.Pushed {
		t.Fatal("expected push to succeed when reusing existing repo")
	}
}

// fakeGitHubToken is built at runtime so secret scanners skip the fixture.
var fakeGitHubToken = "gh" + "p_" + strings.Repeat("a1", 18)

func TestCreateGitHubAndSetup_ExistingPublicRepoBlocks(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- clean fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origRunGH, origAvail := runGH, ghAvailable
	defer func() { runGH, ghAvailable = origRunGH, origAvail }()
	ghAvailable = func() bool { return true }
	runGH = func(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "create" {
			return []byte("GraphQL: Name already exists on this account (createRepository)\n"), errors.New("exit status 1")
		}
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for i, a := range args {
				if a == "--json" && i+1 < len(args) && strings.Contains(args[i+1], "isPrivate") {
					return []byte(`{"isPrivate": false}`), nil
				}
				if a == "--json" && i+1 < len(args) && strings.Contains(args[i+1], "sshUrl") {
					return []byte(`{"sshUrl": "git@github.com:someone/existing-vault.git", "url": "https://github.com/someone/existing-vault"}`), nil
				}
			}
		}
		return nil, errors.New("unexpected gh call")
	}

	// Confirmed covers the local findings only; an existing public repo must still block.
	res, err := CreateGitHubAndSetup(context.Background(), vaultDir, "existing-vault", SetupOptions{Confirmed: true})
	if err == nil || !res.Blocked || !res.Report.IsPublic() {
		t.Fatalf("expected public-repo block, got res=%+v err=%v", res, err)
	}
	if isGitRepo(vaultDir) {
		out, _ := exec.Command("git", "-C", vaultDir, "log", "--oneline").CombinedOutput()
		t.Fatalf("blocked setup must not create commits: %s", out)
	}
}

func TestGHCreatePrivate_OtherErrorsDoNotReuse(t *testing.T) {
	origRunGH := runGH
	defer func() { runGH = origRunGH }()
	viewed := false
	runGH = func(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "create" {
			return []byte("HTTP 403: Resource not accessible by integration\n"), errors.New("exit status 1")
		}
		viewed = true
		return []byte(`{"sshUrl": "git@github.com:o/r.git", "url": "https://github.com/o/r"}`), nil
	}
	if _, err := ghCreatePrivate(context.Background(), "r"); err == nil || viewed {
		t.Fatalf("a non-'already exists' create error must fail without reusing (err=%v viewed=%v)", err, viewed)
	}
}

func TestScanSensitiveFiles_ReportAndIgnoreRules(t *testing.T) {
	vaultDir := t.TempDir()

	// 1. Secret in notes.txt -> reported
	if err := os.WriteFile(filepath.Join(vaultDir, "notes.txt"), []byte("api_key = \"sk-123456789012345678901234567890\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. Secret in config.json -> reported
	if err := os.WriteFile(filepath.Join(vaultDir, "config.json"), []byte("{\"token\": \"ghp_123456789012345678901234567890\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Secret in .env dotfile at root -> reported
	if err := os.WriteFile(filepath.Join(vaultDir, ".env"), []byte("SECRET=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 4. Secret inside .pending/ -> NOT reported
	if err := os.MkdirAll(filepath.Join(vaultDir, ".pending"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".pending", "change.diff"), []byte("api_key = \"sk-123456789012345678901234567890\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 5. Secret inside .index/ -> NOT reported
	if err := os.MkdirAll(filepath.Join(vaultDir, ".index"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".index", "cached.txt"), []byte("api_key = \"sk-123456789012345678901234567890\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 6. Secret inside audit.db -> NOT reported
	if err := os.WriteFile(filepath.Join(vaultDir, "audit.db"), []byte("api_key = \"sk-123456789012345678901234567890\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 7. Encrypted file containing a secret -> NOT reported
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	encData, err := vaultcrypt.Encrypt([]byte("api_key = \"sk-123456789012345678901234567890\"\n"), id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "personal.md"), encData, 0o600); err != nil {
		t.Fatal(err)
	}

	// 8. Binary file with NUL byte in first 8 KiB -> NOT reported
	binData := append([]byte("header\x00data\n"), []byte("api_key = \"sk-123456789012345678901234567890\"\n")...)
	if err := os.WriteFile(filepath.Join(vaultDir, "image.png"), binData, 0o644); err != nil {
		t.Fatal(err)
	}

	// 9. File larger than 2 MiB -> still reported (git add -A stages it)
	largeData := make([]byte, 2*1024*1024+10)
	for i := range largeData {
		largeData[i] = 'a'
	}
	copy(largeData[len(largeData)-40:], []byte("api_key = \"sk-123456789012345678901234567890\"\n"))
	if err := os.WriteFile(filepath.Join(vaultDir, "large.txt"), largeData, 0o644); err != nil {
		t.Fatal(err)
	}

	found := scanSensitiveFiles(vaultDir)
	if len(found) != 4 {
		t.Fatalf("scanSensitiveFiles returned %d files, want exactly 4: %+v", len(found), found)
	}

	wantFiles := []string{".env", "config.json", "large.txt", "notes.txt"}
	for i, want := range wantFiles {
		if found[i].File != want {
			t.Errorf("found[%d].File = %q, want %q", i, found[i].File, want)
		}
		if found[i].Count != 1 {
			t.Errorf("found[%d].Count = %d, want 1", i, found[i].Count)
		}
	}
}

func TestTempDecryptRace_Setup_SentinelBetweenAddAndCommit(t *testing.T) {
	vaultDir := t.TempDir()
	bareDir := initBareRepo(t)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- clean fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	sentinelCreated := false
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		out, err := origRunGit(ctx, dir, timeout, args...)
		if len(args) >= 2 && args[0] == "add" && args[1] == "-A" {
			// Create sentinel right after add -A, before commit runs.
			if serr := os.MkdirAll(filepath.Join(dir, ".index"), 0o755); serr != nil {
				t.Fatalf("mkdir .index: %v", serr)
			}
			if serr := os.WriteFile(filepath.Join(dir, ".index", "reencrypt-pending.json"), []byte(`{"files":["personal.md"]}`), 0o600); serr != nil {
				t.Fatalf("write sentinel: %v", serr)
			}
			sentinelCreated = true
		}
		return out, err
	}

	res, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{})
	if err == nil || !strings.Contains(err.Error(), "temporary decrypt") {
		t.Fatalf("expected temporary decrypt error, got res=%+v err=%v", res, err)
	}
	if !sentinelCreated {
		t.Fatal("sentinel was never created during the test run")
	}

	// Verify no commit was made: repo has no HEAD commit.
	if isGitRepo(vaultDir) {
		checkCmd := exec.Command("git", "-C", vaultDir, "rev-parse", "--verify", "HEAD")
		if out, err := checkCmd.CombinedOutput(); err == nil {
			t.Fatalf("expected no commit to be created, but HEAD exists: %s", out)
		}
	}

	// Verify nothing is staged: git diff --staged is empty.
	diffStaged, err := exec.Command("git", "-C", vaultDir, "diff", "--staged").CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --staged failed: %v\n%s", err, diffStaged)
	}
	if strings.TrimSpace(string(diffStaged)) != "" {
		t.Fatalf("expected nothing staged after git reset -q, got:\n%s", diffStaged)
	}
}

func TestTempDecryptRace_Sync_SentinelBetweenAddAndCommit(t *testing.T) {
	vaultDir := t.TempDir()
	bareDir := initBareRepo(t)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- clean initial fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Initial setup cleanly commits and pushes.
	if _, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{}); err != nil {
		t.Fatalf("initial Setup failed: %v", err)
	}

	// Make a change in the vault.
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- clean initial fact\n- new fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	headBefore, err := exec.Command("git", "-C", vaultDir, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD before: %v\n%s", err, headBefore)
	}

	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	sentinelCreated := false
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		out, err := origRunGit(ctx, dir, timeout, args...)
		if len(args) >= 2 && args[0] == "add" && args[1] == "-A" {
			// Create sentinel right after add -A, before commit runs.
			if serr := os.MkdirAll(filepath.Join(dir, ".index"), 0o755); serr != nil {
				t.Fatalf("mkdir .index: %v", serr)
			}
			if serr := os.WriteFile(filepath.Join(dir, ".index", "reencrypt-pending.json"), []byte(`{"files":["personal.md"]}`), 0o600); serr != nil {
				t.Fatalf("write sentinel: %v", serr)
			}
			sentinelCreated = true
		}
		return out, err
	}

	res, err := SyncStatus(vaultDir)
	if err == nil || !strings.Contains(err.Error(), "temporary decrypt") {
		t.Fatalf("expected temporary decrypt error, got res=%+v err=%v", res, err)
	}
	if !sentinelCreated {
		t.Fatal("sentinel was never created during the test run")
	}

	// Verify no new commit was made: HEAD is unchanged.
	headAfter, err := exec.Command("git", "-C", vaultDir, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD after: %v\n%s", err, headAfter)
	}
	if string(headAfter) != string(headBefore) {
		t.Fatalf("HEAD changed from %s to %s; no commit should be created", headBefore, headAfter)
	}

	// Verify nothing is staged: git diff --staged is empty.
	diffStaged, err := exec.Command("git", "-C", vaultDir, "diff", "--staged").CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --staged failed: %v\n%s", err, diffStaged)
	}
	if strings.TrimSpace(string(diffStaged)) != "" {
		t.Fatalf("expected nothing staged after git reset -q, got:\n%s", diffStaged)
	}

	// Verify working tree modification was NOT destroyed.
	content, err := os.ReadFile(filepath.Join(vaultDir, "identity.md"))
	if err != nil || !strings.Contains(string(content), "- new fact") {
		t.Fatalf("working tree modification should remain intact, got: %s", content)
	}
}

func TestTempDecryptRace_Push_SentinelBeforePush(t *testing.T) {
	vaultDir := t.TempDir()
	bareDir := initBareRepo(t)
	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- clean initial fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(context.Background(), vaultDir, bareDir, SetupOptions{}); err != nil {
		t.Fatalf("initial Setup failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(vaultDir, "identity.md"), []byte("- clean initial fact\n- new fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origRunGit := runGit
	defer func() { runGit = origRunGit }()
	sentinelCreated := false
	runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
		out, err := origRunGit(ctx, dir, timeout, args...)
		if len(args) >= 1 && args[0] == "commit" {
			// Create sentinel right after commit finishes, before push runs.
			if serr := os.MkdirAll(filepath.Join(dir, ".index"), 0o755); serr != nil {
				t.Fatalf("mkdir .index: %v", serr)
			}
			if serr := os.WriteFile(filepath.Join(dir, ".index", "reencrypt-pending.json"), []byte(`{"files":["personal.md"]}`), 0o600); serr != nil {
				t.Fatalf("write sentinel: %v", serr)
			}
			sentinelCreated = true
		}
		return out, err
	}

	res, err := SyncStatus(vaultDir)
	if err == nil || !strings.Contains(err.Error(), "temporary decrypt") {
		t.Fatalf("expected temporary decrypt error, got res=%+v err=%v", res, err)
	}
	if !sentinelCreated {
		t.Fatal("sentinel was never created during the test run")
	}

	// Verify nothing was pushed to bareDir on this run: refs/heads/main on bare remote should only have 1 commit.
	logCmd := exec.Command("git", "-C", bareDir, "log", "--oneline", "refs/heads/main")
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log bare remote failed: %v\n%s", err, logOut)
	}
	lines := strings.Split(strings.TrimSpace(string(logOut)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected bare remote to have 1 commit, got %d:\n%s", len(lines), logOut)
	}
}

func TestScanSensitiveFiles_LargeFileScanned(t *testing.T) {
	vaultDir := t.TempDir()
	big := strings.Repeat("- plain fact line\n", 200000) + "token " + fakeGitHubToken + "\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "big.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	found := scanSensitiveFiles(vaultDir)
	if len(found) != 1 || found[0].File != "big.md" {
		t.Fatalf("expected big.md (>2 MiB) to be scanned, got %+v", found)
	}
}
