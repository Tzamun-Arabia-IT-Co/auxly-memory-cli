package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds git sync configuration from git.yaml.
type Config struct {
	AutoCommit          bool   `yaml:"auto_commit"`
	AutoPush            bool   `yaml:"auto_push"`
	CommitMessagePrefix string `yaml:"commit_message_prefix"`
	Branch              string `yaml:"branch"`
}

// LoadConfig reads git.yaml from the memory root.
func LoadConfig(memoryRoot string) (*Config, error) {
	path := filepath.Join(memoryRoot, "git.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{AutoCommit: true, AutoPush: false, CommitMessagePrefix: "auxly:", Branch: "main"}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// errTempDecryptInFlight is returned by AutoCommit/Sync when they refuse to
// touch git because a temp-decrypt is in flight (see tempDecryptInFlight).
var errTempDecryptInFlight = errors.New("skipped: a temporary decrypt is in progress")

// reencryptSentinelName mirrors internal/memory/cryptio.go's
// reencryptSentinelPath — duplicated here (not imported) to avoid a
// git↔memory dependency; both must agree that ".index/reencrypt-pending.json"
// under the vault root is the crash-recovery marker for an in-flight
// Store.TempDecryptForOrganize.
const reencryptSentinelName = ".index/reencrypt-pending.json"

// tempDecryptInFlight reports whether organize's "decrypt temporarily" escape
// hatch currently has plaintext vault files on disk.
//
// CRITICAL 1: TempDecryptForOrganize can leave a vault file decrypted on disk
// for the length of a whole CLI-agent run (minutes). `git add -A` stages
// EVERYTHING under memoryRoot with no notion of "this file is only
// temporarily plaintext" — a concurrent write/approve/sync (AutoCommit
// defaults on) racing that window would stage and push the plaintext into
// git history permanently and unrecoverably. So AutoCommit/Sync must refuse
// outright while the sentinel exists, full stop.
func tempDecryptInFlight(memoryRoot string) bool {
	_, err := os.Stat(filepath.Join(memoryRoot, reencryptSentinelName))
	return err == nil
}

// ensureIndexGitignored makes sure the vault's own bookkeeping directory
// (.index/ — the semantic-index DB and the temp-decrypt crash-recovery
// sentinel, which names files currently plaintext on disk) is never a commit
// candidate in the first place. Best-effort: `git check-ignore` first, so an
// existing exclusion (repo .gitignore, .git/info/exclude, a global
// gitignore) is respected instead of duplicated; only appends a vault-local
// .gitignore entry when nothing already covers it.
func ensureIndexGitignored(memoryRoot string) {
	check := exec.Command("git", "check-ignore", "-q", ".index")
	check.Dir = memoryRoot
	if err := check.Run(); err == nil {
		return // already ignored by something
	}

	giPath := filepath.Join(memoryRoot, ".gitignore")
	existing, _ := os.ReadFile(giPath)
	for _, line := range strings.Split(string(existing), "\n") {
		if t := strings.TrimSpace(line); t == ".index/" || t == ".index" || t == "/.index/" || t == "/.index" {
			return // already listed, just not yet effective (e.g. never git-added)
		}
	}

	content := string(existing)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += ".index/\n"
	_ = os.WriteFile(giPath, []byte(content), 0644)
}

// AutoCommit performs a git add + commit for a specific file change.
func AutoCommit(memoryRoot, file, reason string) error {
	cfg, err := LoadConfig(memoryRoot)
	if err != nil {
		return err
	}

	if !isGitRepo(memoryRoot) {
		return nil
	}

	// CRITICAL 1: callers (cmd/write.go, cmd/approve.go) invoke AutoCommit
	// fire-and-forget and ignore its error, so the warning must be visible on
	// its own — printing here is the only way the user sees it.
	if tempDecryptInFlight(memoryRoot) {
		fmt.Fprintln(os.Stderr, "⚠ auxly: skipped auto-commit: a temporary decrypt is in progress")
		return errTempDecryptInFlight
	}

	ensureIndexGitignored(memoryRoot)

	// git add
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = memoryRoot
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// git commit
	msg := fmt.Sprintf("%s %s — %s", cfg.CommitMessagePrefix, file, reason)
	commitCmd := exec.Command("git", "commit", "-m", msg, "--allow-empty")
	commitCmd.Dir = memoryRoot
	if err := commitCmd.Run(); err != nil {
		// Ignore "nothing to commit" errors
		return nil
	}

	return nil
}

// Sync performs git add + commit + push. Kept as the original error-only
// signature for existing callers (`auxly sync`); SyncStatus below is the same
// operation with a richer result.
func Sync(memoryRoot string) error {
	_, err := SyncStatus(memoryRoot)
	return err
}

// SyncResult reports what Sync/SyncStatus actually did in git terms. A nil
// error covers both "pushed new commits" and "nothing new to push" (git push
// exits 0 either way) — a caller that wants to tell those apart (the TUI's
// Ops panel) needs Pushed instead of always claiming "synced".
type SyncResult struct {
	Pushed bool
}

// SyncStatus performs git add + commit + push, same as Sync, plus reports
// whether the push actually moved refs vs there being nothing new to push.
func SyncStatus(memoryRoot string) (SyncResult, error) {
	cfg, err := LoadConfig(memoryRoot)
	if err != nil {
		return SyncResult{}, err
	}

	if !isGitRepo(memoryRoot) {
		return SyncResult{}, fmt.Errorf("memory folder is not a git repository. Run 'git init' in %s first", memoryRoot)
	}

	// CRITICAL 1: unlike AutoCommit, Sync is a foreground command
	// (`auxly sync`) whose caller prints "synced successfully" on a nil
	// error — so this MUST return an error rather than silently no-op, or
	// the user would be told a sync happened when it was actually skipped.
	if tempDecryptInFlight(memoryRoot) {
		return SyncResult{}, fmt.Errorf("skipped sync: a temporary decrypt is in progress — retry once the organize run finishes (or run `auxly doctor` if it looks stuck)")
	}

	ensureIndexGitignored(memoryRoot)

	// git add
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = memoryRoot
	if out, err := addCmd.CombinedOutput(); err != nil {
		return SyncResult{}, fmt.Errorf("git add failed: %s", out)
	}

	// git commit. No --allow-empty: an empty commit here would advance HEAD
	// on every sync even with zero vault changes, making "nothing to push"
	// unreachable (SyncResult.Pushed would always be true) and littering the
	// history with no-op commits. A "nothing to commit" exit is expected and
	// ignored — the push below still runs and correctly reports up-to-date.
	msg := fmt.Sprintf("%s sync", cfg.CommitMessagePrefix)
	commitCmd := exec.Command("git", "commit", "-m", msg)
	commitCmd.Dir = memoryRoot
	commitCmd.CombinedOutput() // ignore error (nothing to commit)

	// git push — sync always pushes.
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}
	pushCmd := exec.Command("git", "push", "origin", branch)
	pushCmd.Dir = memoryRoot
	out, err := pushCmd.CombinedOutput()
	if err != nil {
		return SyncResult{}, fmt.Errorf("git push failed: %s", out)
	}
	lower := strings.ToLower(string(out))
	pushed := !strings.Contains(lower, "up-to-date") && !strings.Contains(lower, "up to date")
	return SyncResult{Pushed: pushed}, nil
}

func isGitRepo(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}
