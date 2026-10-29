package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
	"gopkg.in/yaml.v3"
)

const (
	gitDefaultTimeout     = 20 * time.Second
	gitPushTimeout        = 60 * time.Second
	autoBackupMinInterval = 2 * time.Minute
	backupStateRelPath    = ".index/git-backup.json"
)

// Config holds git sync/backup configuration from git.yaml.
type Config struct {
	AutoCommit          bool   `yaml:"auto_commit"`
	AutoPush            bool   `yaml:"auto_push"`
	CommitMessagePrefix string `yaml:"commit_message_prefix"`
	Branch              string `yaml:"branch"`
}

func defaultConfig() Config {
	return Config{
		AutoCommit:          true,
		AutoPush:            false,
		CommitMessagePrefix: "auxly:",
		Branch:              "main",
	}
}

var validBranchRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// ValidBranchName reports whether b is a safe git branch name.
func ValidBranchName(b string) bool {
	if !validBranchRE.MatchString(b) {
		return false
	}
	if strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "@{") {
		return false
	}
	if strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") {
		return false
	}
	return true
}

// LoadConfig reads git.yaml from the memory root.
func LoadConfig(memoryRoot string) (*Config, error) {
	cfg := defaultConfig()
	path := filepath.Join(memoryRoot, "git.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.CommitMessagePrefix) == "" {
		cfg.CommitMessagePrefix = "auxly:"
	}
	if strings.TrimSpace(cfg.Branch) == "" {
		cfg.Branch = "main"
	} else if !ValidBranchName(cfg.Branch) {
		return nil, fmt.Errorf("git.yaml: invalid branch %q", cfg.Branch)
	}
	return &cfg, nil
}

// SaveConfig writes git.yaml atomically into the memory root.
func SaveConfig(memoryRoot string, cfg *Config) error {
	if cfg == nil {
		return errors.New("nil git config")
	}
	next := *cfg
	if strings.TrimSpace(next.CommitMessagePrefix) == "" {
		next.CommitMessagePrefix = "auxly:"
	}
	if strings.TrimSpace(next.Branch) == "" {
		next.Branch = "main"
	} else if !ValidBranchName(next.Branch) {
		return fmt.Errorf("git.yaml: invalid branch %q", next.Branch)
	}
	data, err := yaml.Marshal(&next)
	if err != nil {
		return fmt.Errorf("marshal git.yaml: %w", err)
	}
	return memory.AtomicWriteFile(filepath.Join(memoryRoot, "git.yaml"), data, 0o644)
}

// errTempDecryptInFlight is returned by AutoCommit/Sync/Setup/BackupNow when
// they refuse to touch git because a temp-decrypt is in flight.
var errTempDecryptInFlight = errors.New("skipped: a temporary decrypt is in progress")

// ErrPrePushBlocked is returned by Setup when pre-push checks find sensitive
// plaintext files (without Confirmed) or a public repository (without AllowPublic).
var ErrPrePushBlocked = errors.New("pre-push checks blocked backup setup")

var errEmbeddedCredentials = errors.New("remote URL must not contain credentials or tokens; log in with `gh auth login` or an SSH key instead")

// reencryptSentinelName mirrors internal/memory/cryptio.go's
// reencryptSentinelPath — both must agree that ".index/reencrypt-pending.json"
// under the vault root is the crash-recovery marker for an in-flight
// Store.TempDecryptForOrganize.
const reencryptSentinelName = ".index/reencrypt-pending.json"

// tempDecryptInFlight reports whether organize's "decrypt temporarily" escape
// hatch currently has plaintext vault files on disk.
//
// CRITICAL 1: TempDecryptForOrganize can leave a vault file decrypted on disk
// for the length of a whole CLI-agent run (minutes). `git add -A` stages
// EVERYTHING under memoryRoot with no notion of "this file is only
// temporarily plaintext" — a concurrent write/approve/sync/setup racing that
// window would stage and push the plaintext into git history permanently and
// unrecoverably. Every commit/push path must refuse outright while the
// sentinel exists, full stop.
func tempDecryptInFlight(memoryRoot string) bool {
	_, err := os.Stat(filepath.Join(memoryRoot, reencryptSentinelName))
	return err == nil
}

// runGit executes git non-interactively with a context timeout (60 s for push,
// 20 s for all other commands) and GIT_TERMINAL_PROMPT=0 + BatchMode SSH
// appended to the process environment so credential or host-key prompts can
// never hang the TUI, the MCP server, or a capture hook.
var runGit = func(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = gitDefaultTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(rctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
	)
	out, err := cmd.CombinedOutput()
	if err != nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
		sub := "command"
		if len(args) > 0 {
			sub = args[0]
		}
		return out, fmt.Errorf("git %s timed out after %s: %w", sub, timeout, rctx.Err())
	}
	return out, err
}

func isMissingGitIdentity(out []byte) bool {
	lower := strings.ToLower(string(out))
	for _, needle := range []string{
		"author identity unknown",
		"please tell me who you are",
		"unable to auto-detect email address",
		"empty ident name",
		"no email was given and auto-detection is disabled",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func isNothingToCommit(out []byte) bool {
	lower := strings.ToLower(string(out))
	return strings.Contains(lower, "nothing to commit") ||
		strings.Contains(lower, "nothing added to commit") ||
		strings.Contains(lower, "no changes added to commit")
}

// gitCommit runs `git commit <args...>` via runGit, retrying once with a
// fallback local Auxly author identity if git failed only because user.name /
// user.email are unconfigured on the machine.
func gitCommit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	commitArgs := append([]string{"commit"}, args...)
	out, err := runGit(ctx, dir, gitDefaultTimeout, commitArgs...)
	if err != nil && isMissingGitIdentity(out) {
		retryArgs := append([]string{"-c", "user.name=Auxly", "-c", "user.email=auxly@localhost", "commit"}, args...)
		return runGit(ctx, dir, gitDefaultTimeout, retryArgs...)
	}
	return out, err
}

// defaultGitignoreEntries are merged into .gitignore during Setup so internal
// indexes, pending queues, audit SQLite files, backups, and temp artifacts are
// never pushed to a backup remote.
var defaultGitignoreEntries = []string{
	".index/",
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
}

func gitignoreCoversEntry(existingLines []string, entry string) bool {
	base := strings.TrimSuffix(entry, "/")
	isDir := strings.HasSuffix(entry, "/")
	for _, raw := range existingLines {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if t == entry || t == "/"+entry {
			return true
		}
		if isDir && (t == base || t == "/"+base) {
			return true
		}
	}
	return false
}

// mergeGitignore appends any missing defaultGitignoreEntries to memoryRoot's
// .gitignore while preserving all existing user lines and comments verbatim.
func mergeGitignore(memoryRoot string) error {
	giPath := filepath.Join(memoryRoot, ".gitignore")
	existingBytes, err := os.ReadFile(giPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	existing := string(existingBytes)
	lines := strings.Split(existing, "\n")

	var missing []string
	for _, entry := range defaultGitignoreEntries {
		if !gitignoreCoversEntry(lines, entry) {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 && err == nil {
		return nil
	}

	var b strings.Builder
	b.WriteString(existing)
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		b.WriteByte('\n')
	}
	for _, entry := range missing {
		b.WriteString(entry)
		b.WriteByte('\n')
	}
	return memory.AtomicWriteFile(giPath, []byte(b.String()), 0o644)
}

// untrackIgnoredFiles removes from the git index any tracked files that are now
// matched by .gitignore (or standard git exclusions), while keeping the working
// tree files intact on disk.
func untrackIgnoredFiles(ctx context.Context, memoryRoot string) error {
	out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "ls-files", "-z", "-c", "-i", "--exclude-standard")
	if err != nil {
		return fmt.Errorf("git ls-files failed: %s", strings.TrimSpace(string(out)))
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	const batchSize = 200
	for start := 0; start < len(paths); start += batchSize {
		end := start + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		args := append([]string{"rm", "-r", "-q", "--cached", "--"}, paths[start:end]...)
		if rmOut, err := runGit(ctx, memoryRoot, gitDefaultTimeout, args...); err != nil {
			return fmt.Errorf("git rm --cached failed: %s", strings.TrimSpace(string(rmOut)))
		}
	}
	return nil
}

func userLooksLikeToken(user string) bool {
	u := strings.TrimSpace(user)
	if u == "" {
		return false
	}
	if memory.LooksLikeSecret(u) {
		return true
	}
	lower := strings.ToLower(u)
	for _, p := range []string{
		"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
		"glpat-", "sk-", "xox", "atok_", "pat_",
	} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	switch lower {
	case "token", "x-access-token", "oauth", "oauth2", "bearer", "apikey", "api-key", "api_key":
		return true
	}
	if len(u) >= 24 {
		tokenChars := true
		for _, r := range u {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
				tokenChars = false
				break
			}
		}
		if tokenChars {
			return true
		}
	}
	return false
}

// ValidateRemoteURL validates a git remote URL for backup setup.
// Accepted forms:
//   - SSH: git@host:owner/repo.git or ssh://[user@]host[:port]/owner/repo.git
//   - HTTPS: https://host/owner/repo.git
//   - Local (for tests and local mirrors): file:///abs/path or /abs/path
//
// Any URL carrying embedded credentials (a password or token in userinfo) is
// refused with guidance to use `gh auth login` or an SSH key instead.
func ValidateRemoteURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", errors.New("remote URL is required")
	}
	if strings.ContainsAny(u, " \t\r\n\x00") {
		return "", errors.New("remote URL must not contain whitespace or control characters")
	}
	if strings.HasPrefix(u, "-") {
		return "", errors.New("invalid remote URL")
	}

	if strings.HasPrefix(u, "http://") {
		if parsed, err := url.Parse(u); err == nil && parsed.User != nil {
			return "", errEmbeddedCredentials
		}
		return "", errors.New("insecure http:// remote URL is not supported; use https:// or an SSH URL")
	}

	if strings.HasPrefix(u, "https://") {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.Path == "" || parsed.Path == "/" {
			return "", fmt.Errorf("invalid https remote URL %q", u)
		}
		if parsed.User != nil || parsed.RawQuery != "" {
			return "", errEmbeddedCredentials
		}
		return u, nil
	}

	if strings.HasPrefix(u, "ssh://") {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.Path == "" || parsed.Path == "/" {
			return "", fmt.Errorf("invalid ssh remote URL %q", u)
		}
		if strings.HasPrefix(parsed.Hostname(), "-") || strings.HasPrefix(parsed.Host, "-") ||
			strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), "-") || strings.HasPrefix(parsed.Path, "-") {
			return "", fmt.Errorf("invalid ssh remote URL %q", u)
		}
		if parsed.User != nil {
			if _, hasPass := parsed.User.Password(); hasPass {
				return "", errEmbeddedCredentials
			}
			if userLooksLikeToken(parsed.User.Username()) {
				return "", errEmbeddedCredentials
			}
		}
		if parsed.RawQuery != "" {
			return "", errEmbeddedCredentials
		}
		return u, nil
	}

	if strings.HasPrefix(u, "file://") {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
			return "", fmt.Errorf("invalid file remote URL %q", u)
		}
		if parsed.User != nil || parsed.RawQuery != "" {
			return "", errEmbeddedCredentials
		}
		return u, nil
	}

	// Reject any other explicit URL scheme (e.g. git://, ftp://) before SCP parsing.
	if strings.Contains(u, "://") {
		return "", fmt.Errorf("unsupported remote URL scheme in %q; use ssh, https://, or a local path", u)
	}

	if filepath.IsAbs(u) && !strings.HasPrefix(u, "//") {
		return u, nil
	}

	// SCP-style SSH: user@host:path (e.g. git@github.com:owner/repo.git).
	if at := strings.IndexByte(u, '@'); at > 0 {
		userPart := u[:at]
		rest := u[at+1:]
		if colonRel := strings.IndexByte(rest, ':'); colonRel > 0 && colonRel < len(rest)-1 {
			hostPart := rest[:colonRel]
			pathPart := rest[colonRel+1:]
			if strings.Contains(userPart, ":") || userLooksLikeToken(userPart) {
				return "", errEmbeddedCredentials
			}
			if hostPart == "" || strings.ContainsAny(hostPart, "/@:") || pathPart == "" ||
				strings.HasPrefix(hostPart, "-") || strings.HasPrefix(strings.TrimPrefix(pathPart, "/"), "-") || strings.HasPrefix(pathPart, "-") {
				return "", fmt.Errorf("invalid SSH remote URL %q", u)
			}
			return u, nil
		}
	}

	return "", fmt.Errorf("unsupported remote URL %q; use git@host:owner/repo.git, https://..., or an absolute local path", u)
}

// StripCredentials removes any userinfo credentials/tokens or query parameters
// from a remote URL before displaying it in status output or the TUI.
func StripCredentials(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if strings.Contains(u, "://") {
		parsed, err := url.Parse(u)
		if err != nil {
			// Best-effort strip between :// and @ if url.Parse choked on raw chars.
			if schemeIdx := strings.Index(u, "://"); schemeIdx >= 0 {
				rest := u[schemeIdx+3:]
				if at := strings.IndexByte(rest, '@'); at >= 0 {
					return u[:schemeIdx+3] + rest[at+1:]
				}
			}
			return u
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		if parsed.Scheme == "ssh" && parsed.User != nil {
			uname := parsed.User.Username()
			if uname != "" && !userLooksLikeToken(uname) {
				parsed.User = url.User(uname)
			} else {
				parsed.User = nil
			}
		} else {
			parsed.User = nil
		}
		return parsed.String()
	}

	// SCP-style userinfo@host:path
	at := strings.IndexByte(u, '@')
	colon := strings.LastIndexByte(u, ':')
	if at > 0 && colon > at+1 {
		userPart := u[:at]
		rest := u[at+1:]
		if idx := strings.IndexByte(userPart, ':'); idx >= 0 {
			uname := userPart[:idx]
			if uname != "" && !userLooksLikeToken(uname) {
				return uname + "@" + rest
			}
			return rest
		}
		if userLooksLikeToken(userPart) {
			return rest
		}
	}
	return u
}

func remoteHost(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" || (filepath.IsAbs(u) && !strings.HasPrefix(u, "//")) {
		return ""
	}
	if strings.Contains(u, "://") {
		if parsed, err := url.Parse(u); err == nil {
			return parsed.Hostname()
		}
		return ""
	}
	if at := strings.IndexByte(u, '@'); at >= 0 {
		rest := u[at+1:]
		if colonRel := strings.IndexByte(rest, ':'); colonRel > 0 {
			return rest[:colonRel]
		}
	}
	return ""
}

func githubRepoSlug(raw string) (string, bool) {
	host := remoteHost(raw)
	if !strings.EqualFold(host, "github.com") {
		return "", false
	}
	u := strings.TrimSpace(raw)
	var pathPart string
	if strings.Contains(u, "://") {
		parsed, err := url.Parse(u)
		if err != nil {
			return "", false
		}
		pathPart = strings.TrimPrefix(parsed.Path, "/")
	} else if at := strings.IndexByte(u, '@'); at >= 0 {
		rest := u[at+1:]
		if colonRel := strings.IndexByte(rest, ':'); colonRel >= 0 {
			pathPart = strings.TrimPrefix(rest[colonRel+1:], "/")
		}
	}
	pathPart = strings.TrimSuffix(pathPart, "/")
	pathPart = strings.TrimSuffix(pathPart, ".git")
	parts := strings.Split(pathPart, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func ghUsesHTTPSCredentialHelper(ctx context.Context) bool {
	for _, key := range []string{"credential.https://github.com.helper", "credential.helper"} {
		out, err := runGit(ctx, "", gitDefaultTimeout, "config", "--get-all", key)
		if err != nil {
			continue
		}
		s := strings.ToLower(string(out))
		if strings.Contains(s, "gh auth git-credential") || strings.Contains(s, "/gh ") || strings.HasPrefix(strings.TrimSpace(s), "!gh ") {
			return true
		}
	}
	return false
}

var runGH = func(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(rctx, "gh", args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	out, err := cmd.CombinedOutput()
	if err != nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
		sub := "command"
		if len(args) > 0 {
			sub = args[0]
		}
		return out, fmt.Errorf("gh %s timed out after %s: %w", sub, timeout, rctx.Err())
	}
	return out, err
}

func ghRepoViewURL(ctx context.Context, repoName string) (string, error) {
	out, err := runGH(ctx, 20*time.Second, "repo", "view", repoName, "--json", "sshUrl,url")
	if err != nil {
		return "", fmt.Errorf("gh repo view failed: %s", strings.TrimSpace(string(out)))
	}
	var info struct {
		SSHURL string `json:"sshUrl"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("parse gh repo view output: %w", err)
	}
	httpsURL := strings.TrimSpace(info.URL)
	if httpsURL != "" && !strings.HasSuffix(httpsURL, ".git") {
		httpsURL += ".git"
	}
	sshURL := strings.TrimSpace(info.SSHURL)
	if ghUsesHTTPSCredentialHelper(ctx) && httpsURL != "" {
		return httpsURL, nil
	}
	if sshURL != "" {
		return sshURL, nil
	}
	if httpsURL != "" {
		return httpsURL, nil
	}
	return "", errors.New("gh repo view returned no repository URL")
}

// Package-level gh wrappers so tests can stub GitHub CLI calls without touching
// the network or a real `gh` installation.
var (
	ghAvailable = func() bool {
		if _, err := exec.LookPath("gh"); err != nil {
			return false
		}
		_, err := runGH(context.Background(), 10*time.Second, "auth", "status")
		return err == nil
	}

	ghCreatePrivate = func(ctx context.Context, name string) (string, error) {
		repoName := strings.TrimSpace(name)
		if repoName == "" {
			return "", errors.New("GitHub repository name is required")
		}
		if strings.HasPrefix(repoName, "-") || strings.ContainsAny(repoName, " \t\r\n\x00") {
			return "", fmt.Errorf("invalid GitHub repository name %q", repoName)
		}
		if ctx == nil {
			ctx = context.Background()
		}

		out, err := runGH(ctx, 30*time.Second, "repo", "create", repoName, "--private")
		if err != nil {
			// When the repo already exists on a retry (`gh repo view <name>` succeeds),
			// reuse it instead of failing on "Name already exists".
			if viewURL, viewErr := ghRepoViewURL(ctx, repoName); viewErr == nil {
				return viewURL, nil
			}
			return "", fmt.Errorf("gh repo create failed: %s", strings.TrimSpace(string(out)))
		}

		return ghRepoViewURL(ctx, repoName)
	}

	ghRepoIsPrivate = func(ctx context.Context, remoteURL string) (private bool, known bool) {
		slug, ok := githubRepoSlug(remoteURL)
		if !ok {
			return false, false
		}
		if !ghAvailable() {
			return false, false
		}
		if ctx == nil {
			ctx = context.Background()
		}
		out, err := runGH(ctx, 15*time.Second, "repo", "view", slug, "--json", "isPrivate")
		if err != nil {
			return false, false
		}
		var resp struct {
			IsPrivate bool `json:"isPrivate"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return false, false
		}
		return resp.IsPrivate, true
	}
)

// GHAvailable reports whether the GitHub CLI is installed and authenticated.
func GHAvailable() bool {
	return ghAvailable()
}

// SetGHStubsForTest replaces the package-level gh function variables for the
// duration of a test in external packages (cmd, tui) and returns a restore func.
func SetGHStubsForTest(
	avail func() bool,
	create func(context.Context, string) (string, error),
	isPriv func(context.Context, string) (bool, bool),
) func() {
	origAvail, origCreate, origPriv := ghAvailable, ghCreatePrivate, ghRepoIsPrivate
	if avail != nil {
		ghAvailable = avail
	}
	if create != nil {
		ghCreatePrivate = create
	}
	if isPriv != nil {
		ghRepoIsPrivate = isPriv
	}
	return func() {
		ghAvailable = origAvail
		ghCreatePrivate = origCreate
		ghRepoIsPrivate = origPriv
	}
}

// SensitiveFile records an unencrypted vault .md file containing one or more
// lines that match memory.LooksLikeSecret. Never stores the matching lines.
type SensitiveFile struct {
	File  string
	Count int
}

// Visibility describes what ghRepoIsPrivate determined about a remote URL.
type Visibility string

const (
	VisibilityUnknown Visibility = "unknown"
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

// Report holds the pre-push safety check results for a backup target.
type Report struct {
	SensitiveFiles  []SensitiveFile
	ExistingCommits int
	Visibility      Visibility
	Warning         string
}

// IsPublic reports whether the remote repository is known to be public.
func (r Report) IsPublic() bool {
	return r.Visibility == VisibilityPublic
}

// HasSensitiveFiles reports whether any unencrypted vault .md files matched
// memory.LooksLikeSecret.
func (r Report) HasSensitiveFiles() bool {
	return len(r.SensitiveFiles) > 0
}

// HasFindings reports whether there are blocking findings (sensitive files,
// existing commits, or a public remote).
func (r Report) HasFindings() bool {
	return r.HasSensitiveFiles() || r.ExistingCommits > 0 || r.IsPublic()
}

// BlockedBy reports whether opts still leaves a blocking pre-push finding
// unconfirmed.
func (r Report) BlockedBy(opts SetupOptions) bool {
	if (r.HasSensitiveFiles() || r.ExistingCommits > 0) && !opts.Confirmed {
		return true
	}
	if r.IsPublic() && !opts.AllowPublic {
		return true
	}
	return false
}

func scanSensitiveFiles(memoryRoot string) []SensitiveFile {
	var found []SensitiveFile
	_ = filepath.WalkDir(memoryRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path == memoryRoot {
				return nil
			}
			switch d.Name() {
			case ".git", ".index", ".pending", ".backup", ".archive":
				return filepath.SkipDir
			}
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || vaultcrypt.IsEncrypted(data) {
			return nil
		}
		count := 0
		for _, line := range strings.Split(string(data), "\n") {
			if memory.LooksLikeSecret(line) {
				count++
			}
		}
		if count > 0 {
			rel, rerr := filepath.Rel(memoryRoot, path)
			if rerr != nil {
				rel = d.Name()
			}
			found = append(found, SensitiveFile{
				File:  filepath.ToSlash(rel),
				Count: count,
			})
		}
		return nil
	})
	sort.Slice(found, func(i, j int) bool {
		return found[i].File < found[j].File
	})
	return found
}

func countExistingCommits(ctx context.Context, memoryRoot string) int {
	if !isGitRepo(memoryRoot) {
		return 0
	}
	out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "rev-list", "--count", "HEAD")
	if err != nil {
		return 0
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil || n < 0 {
		return 0
	}
	return n
}

// PrePushReport inspects memoryRoot for unencrypted .md files containing
// secret-like lines, counts existing commits, and checks whether remoteURL
// points to a private repo.
func PrePushReport(ctx context.Context, memoryRoot, remoteURL string) Report {
	rep := Report{
		SensitiveFiles:  scanSensitiveFiles(memoryRoot),
		ExistingCommits: countExistingCommits(ctx, memoryRoot),
	}
	priv, known := ghRepoIsPrivate(ctx, remoteURL)
	switch {
	case !known:
		rep.Visibility = VisibilityUnknown
		rep.Warning = "couldn't verify the repo is private"
	case priv:
		rep.Visibility = VisibilityPrivate
	default:
		rep.Visibility = VisibilityPublic
	}
	return rep
}

// SetupOptions controls confirmation gates on Setup and CreateGitHubAndSetup.
type SetupOptions struct {
	Confirmed   bool
	AllowPublic bool
}

// SetupResult reports what Setup / CreateGitHubAndSetup did and carries the
// pre-push safety report.
type SetupResult struct {
	RemoteURL string
	Branch    string
	Pushed    bool
	Blocked   bool
	Report    Report
}

func prePushBlockError(rep Report, opts SetupOptions) error {
	var reasons []string
	if rep.IsPublic() && !opts.AllowPublic {
		reasons = append(reasons, "remote repository is public (pass --allow-public to allow)")
	}
	if rep.HasSensitiveFiles() && !opts.Confirmed {
		reasons = append(reasons, fmt.Sprintf("%d unencrypted file(s) contain potential secrets", len(rep.SensitiveFiles)))
	}
	if rep.ExistingCommits > 0 && !opts.Confirmed {
		reasons = append(reasons, fmt.Sprintf("%d earlier commits will be pushed as they are; older commits are not checked for secrets", rep.ExistingCommits))
	}
	return fmt.Errorf("%w: %s", ErrPrePushBlocked, strings.Join(reasons, "; "))
}

func isAuthFailure(out string) bool {
	lower := strings.ToLower(out)
	for _, needle := range []string{
		"authentication failed",
		"permission denied",
		"could not read username",
		"could not read password",
		"terminal prompts disabled",
		"repository not found",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func formatPushError(remoteURL string, out []byte) error {
	text := strings.TrimSpace(string(out))
	if isAuthFailure(text) {
		host := remoteHost(remoteURL)
		if host == "" {
			host = "remote"
		}
		return fmt.Errorf("git couldn't log in to %s. Run `gh auth login` (GitHub) or add an SSH key, then try again.", host)
	}
	return fmt.Errorf("git push failed: %s", text)
}

// Setup initializes git backup in memoryRoot against remoteURL and pushes the
// initial backup commit. Pre-push checks run before staging/committing/pushing
// and block unless confirmed via opts.
func Setup(ctx context.Context, memoryRoot, remoteURL string, opts SetupOptions) (SetupResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Never create the vault here: a wrong or missing path would otherwise turn
	// into an empty repo that gets pushed as the "backup".
	if info, err := os.Stat(memoryRoot); err != nil || !info.IsDir() {
		return SetupResult{}, fmt.Errorf("memory folder %s not found; run `auxly init` first", memoryRoot)
	}
	if tempDecryptInFlight(memoryRoot) {
		return SetupResult{}, fmt.Errorf("skipped setup: a temporary decrypt is in progress — retry once the organize run finishes")
	}

	cleanURL, err := ValidateRemoteURL(remoteURL)
	if err != nil {
		return SetupResult{}, err
	}

	cfg, err := LoadConfig(memoryRoot)
	if err != nil {
		return SetupResult{}, err
	}
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}
	if !ValidBranchName(branch) {
		return SetupResult{}, fmt.Errorf("git.yaml: invalid branch %q", branch)
	}
	prefix := strings.TrimSpace(cfg.CommitMessagePrefix)
	if prefix == "" {
		prefix = "auxly:"
	}

	report := PrePushReport(ctx, memoryRoot, cleanURL)
	displayURL := StripCredentials(cleanURL)
	if report.BlockedBy(opts) {
		return SetupResult{
			RemoteURL: displayURL,
			Branch:    branch,
			Blocked:   true,
			Report:    report,
		}, prePushBlockError(report, opts)
	}

	if !isGitRepo(memoryRoot) {
		if out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "init"); err != nil {
			return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, fmt.Errorf("git init failed: %s", strings.TrimSpace(string(out)))
		}
		if out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
			return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, fmt.Errorf("git symbolic-ref failed: %s", strings.TrimSpace(string(out)))
		}
	} else {
		// Ensure the configured branch exists or HEAD points at it if unborn.
		if _, herr := runGit(ctx, memoryRoot, gitDefaultTimeout, "rev-parse", "--verify", "HEAD"); herr != nil {
			_, _ = runGit(ctx, memoryRoot, gitDefaultTimeout, "symbolic-ref", "HEAD", "refs/heads/"+branch)
		} else if _, berr := runGit(ctx, memoryRoot, gitDefaultTimeout, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); berr != nil {
			_, _ = runGit(ctx, memoryRoot, gitDefaultTimeout, "branch", "-M", branch)
		}
	}

	if err := mergeGitignore(memoryRoot); err != nil {
		return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, err
	}
	if err := untrackIgnoredFiles(ctx, memoryRoot); err != nil {
		return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, err
	}

	if _, rerr := runGit(ctx, memoryRoot, gitDefaultTimeout, "remote", "get-url", "origin"); rerr == nil {
		if out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "remote", "set-url", "origin", cleanURL); err != nil {
			return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, fmt.Errorf("git remote set-url failed: %s", strings.TrimSpace(string(out)))
		}
	} else {
		if out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "remote", "add", "origin", cleanURL); err != nil {
			return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, fmt.Errorf("git remote add failed: %s", strings.TrimSpace(string(out)))
		}
	}

	if out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "add", "-A"); err != nil {
		return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, fmt.Errorf("git add failed: %s", strings.TrimSpace(string(out)))
	}

	msg := fmt.Sprintf("%s initial backup", prefix)
	if out, err := gitCommit(ctx, memoryRoot, "-m", msg); err != nil && !isNothingToCommit(out) {
		return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
	}
	// If HEAD is still unborn (e.g. .gitignore was already committed? impossible
	// if unborn, unless repo was completely empty), ensure a commit exists to push.
	if _, herr := runGit(ctx, memoryRoot, gitDefaultTimeout, "rev-parse", "--verify", "HEAD"); herr != nil {
		if out, err := gitCommit(ctx, memoryRoot, "-m", msg, "--allow-empty"); err != nil {
			return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
		}
	}

	attemptAt := nowFunc()
	out, err := runGit(ctx, memoryRoot, gitPushTimeout, "push", "-u", "origin", "HEAD:refs/heads/"+branch)
	if err != nil {
		pushErr := formatPushError(cleanURL, out)
		recordBackupOutcome(memoryRoot, attemptAt, pushErr)
		return SetupResult{RemoteURL: displayURL, Branch: branch, Report: report}, pushErr
	}
	recordBackupOutcome(memoryRoot, attemptAt, nil)

	lower := strings.ToLower(string(out))
	pushed := !strings.Contains(lower, "up-to-date") && !strings.Contains(lower, "up to date")
	return SetupResult{
		RemoteURL: displayURL,
		Branch:    branch,
		Pushed:    pushed,
		Report:    report,
	}, nil
}

// CreateGitHubAndSetup creates a private GitHub repository via `gh` and sets it
// up as memoryRoot's git backup remote.
func CreateGitHubAndSetup(ctx context.Context, memoryRoot, name string, opts SetupOptions) (SetupResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if info, err := os.Stat(memoryRoot); err != nil || !info.IsDir() {
		return SetupResult{}, fmt.Errorf("memory folder %s not found; run `auxly init` first", memoryRoot)
	}
	if tempDecryptInFlight(memoryRoot) {
		return SetupResult{}, fmt.Errorf("skipped setup: a temporary decrypt is in progress — retry once the organize run finishes")
	}
	cfg, err := LoadConfig(memoryRoot)
	if err != nil {
		return SetupResult{}, err
	}
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}
	if !ValidBranchName(branch) {
		return SetupResult{}, fmt.Errorf("git.yaml: invalid branch %q", branch)
	}

	// Run local pre-push checks (sensitive files, existing commits) BEFORE
	// creating the GitHub repo, so a blocked run creates nothing.
	localReport := Report{
		SensitiveFiles:  scanSensitiveFiles(memoryRoot),
		ExistingCommits: countExistingCommits(ctx, memoryRoot),
		Visibility:      VisibilityUnknown,
	}
	if (localReport.HasSensitiveFiles() || localReport.ExistingCommits > 0) && !opts.Confirmed {
		return SetupResult{
			Branch:  branch,
			Blocked: true,
			Report:  localReport,
		}, prePushBlockError(localReport, opts)
	}

	remoteURL, err := ghCreatePrivate(ctx, name)
	if err != nil {
		return SetupResult{}, err
	}
	return Setup(ctx, memoryRoot, remoteURL, opts)
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

	if err := mergeGitignore(memoryRoot); err != nil {
		return err
	}
	if err := untrackIgnoredFiles(context.Background(), memoryRoot); err != nil {
		return err
	}

	if _, err := runGit(context.Background(), memoryRoot, gitDefaultTimeout, "add", "-A"); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	msg := fmt.Sprintf("%s %s — %s", cfg.CommitMessagePrefix, file, reason)
	if out, err := gitCommit(context.Background(), memoryRoot, "-m", msg, "--allow-empty"); err != nil && !isNothingToCommit(out) {
		return fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
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

// SyncResult reports what Sync/SyncStatus/BackupNow actually did in git terms.
// A nil error covers both "pushed new commits" and "nothing new to push" (git
// push exits 0 either way) — a caller that wants to tell those apart needs
// Pushed instead of always claiming "synced".
type SyncResult struct {
	Pushed bool
}

func originURL(ctx context.Context, memoryRoot string) string {
	out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func syncWithContext(ctx context.Context, memoryRoot string) (SyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := LoadConfig(memoryRoot)
	if err != nil {
		return SyncResult{}, err
	}

	if !isGitRepo(memoryRoot) {
		return SyncResult{}, fmt.Errorf("memory folder is not a git repository. Run 'git init' in %s first", memoryRoot)
	}

	// CRITICAL 1: unlike AutoCommit, Sync/BackupNow is a foreground command
	// (`auxly sync`) whose caller prints "synced successfully" on a nil
	// error — so this MUST return an error rather than silently no-op, or
	// the user would be told a sync happened when it was actually skipped.
	if tempDecryptInFlight(memoryRoot) {
		return SyncResult{}, fmt.Errorf("skipped sync: a temporary decrypt is in progress — retry once the organize run finishes (or run `auxly doctor` if it looks stuck)")
	}

	if err := mergeGitignore(memoryRoot); err != nil {
		return SyncResult{}, err
	}
	if err := untrackIgnoredFiles(ctx, memoryRoot); err != nil {
		return SyncResult{}, err
	}

	if out, err := runGit(ctx, memoryRoot, gitDefaultTimeout, "add", "-A"); err != nil {
		return SyncResult{}, fmt.Errorf("git add failed: %s", out)
	}

	// git commit. No --allow-empty: an empty commit here would advance HEAD
	// on every sync even with zero vault changes, making "nothing to push"
	// unreachable (SyncResult.Pushed would always be true) and littering the
	// history with no-op commits. A "nothing to commit" exit is expected and
	// ignored — the push below still runs and correctly reports up-to-date.
	msg := fmt.Sprintf("%s sync", cfg.CommitMessagePrefix)
	if out, cerr := gitCommit(ctx, memoryRoot, "-m", msg); cerr != nil && !isNothingToCommit(out) {
		return SyncResult{}, fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
	}

	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}
	if !ValidBranchName(branch) {
		return SyncResult{}, fmt.Errorf("git.yaml: invalid branch %q", branch)
	}
	out, err := runGit(ctx, memoryRoot, gitPushTimeout, "push", "origin", "HEAD:refs/heads/"+branch)
	if err != nil {
		return SyncResult{}, formatPushError(originURL(ctx, memoryRoot), out)
	}
	lower := strings.ToLower(string(out))
	pushed := !strings.Contains(lower, "up-to-date") && !strings.Contains(lower, "up to date")
	return SyncResult{Pushed: pushed}, nil
}

// SyncStatus performs git add + commit + push, same as Sync, plus reports
// whether the push actually moved refs vs there being nothing new to push.
func SyncStatus(memoryRoot string) (SyncResult, error) {
	return syncWithContext(context.Background(), memoryRoot)
}

type backupState struct {
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

var (
	backupMu     sync.Mutex
	autoBackupWG sync.WaitGroup
	nowFunc      = time.Now
)

func loadBackupStateLocked(memoryRoot string) backupState {
	data, err := os.ReadFile(filepath.Join(memoryRoot, backupStateRelPath))
	if err != nil {
		return backupState{}
	}
	var st backupState
	if json.Unmarshal(data, &st) != nil {
		return backupState{}
	}
	return st
}

func saveBackupStateLocked(memoryRoot string, st backupState) {
	// Guard against writing into a removed directory (e.g. a background
	// goroutine finishing right as a test TempDir cleans up).
	if info, err := os.Stat(memoryRoot); err != nil || !info.IsDir() {
		return
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	_ = memory.AtomicWriteFile(filepath.Join(memoryRoot, backupStateRelPath), append(data, '\n'), 0o600)
}

func recordBackupOutcome(memoryRoot string, attemptAt time.Time, runErr error) {
	backupMu.Lock()
	defer backupMu.Unlock()
	st := loadBackupStateLocked(memoryRoot)
	st.LastAttempt = attemptAt
	if runErr != nil {
		st.LastError = runErr.Error()
	} else {
		st.LastSuccess = nowFunc()
		st.LastError = ""
	}
	saveBackupStateLocked(memoryRoot, st)
}

// BackupNow runs SyncStatus via the non-interactive git helper with ctx and
// records the attempt/outcome in .index/git-backup.json.
func BackupNow(ctx context.Context, memoryRoot string) (SyncResult, error) {
	if tempDecryptInFlight(memoryRoot) {
		return SyncResult{}, fmt.Errorf("skipped sync: a temporary decrypt is in progress — retry once the organize run finishes (or run `auxly doctor` if it looks stuck)")
	}
	attemptAt := nowFunc()
	res, err := syncWithContext(ctx, memoryRoot)
	if isGitRepo(memoryRoot) {
		recordBackupOutcome(memoryRoot, attemptAt, err)
	}
	return res, err
}

func hasOriginRemote(memoryRoot string) bool {
	if data, err := os.ReadFile(filepath.Join(memoryRoot, ".git", "config")); err == nil {
		return strings.Contains(string(data), `[remote "origin"]`)
	}
	return originURL(context.Background(), memoryRoot) != ""
}

// MaybeAutoBackup triggers an asynchronous background BackupNow (60 s timeout)
// when memoryRoot is a git repository with an origin remote and git.yaml has
// auto_push enabled. Rate-limited to at most one attempt per 2 minutes via
// .index/git-backup.json and never blocks the caller.
func MaybeAutoBackup(memoryRoot string) {
	if strings.TrimSpace(memoryRoot) == "" || !isGitRepo(memoryRoot) {
		return
	}
	cfg, err := LoadConfig(memoryRoot)
	if err != nil || cfg == nil || !cfg.AutoPush {
		return
	}
	if tempDecryptInFlight(memoryRoot) {
		return
	}
	if !hasOriginRemote(memoryRoot) {
		return
	}

	backupMu.Lock()
	st := loadBackupStateLocked(memoryRoot)
	now := nowFunc()
	if !st.LastAttempt.IsZero() && now.Sub(st.LastAttempt) < autoBackupMinInterval {
		backupMu.Unlock()
		return
	}
	st.LastAttempt = now
	saveBackupStateLocked(memoryRoot, st)
	backupMu.Unlock()

	autoBackupWG.Add(1)
	go func() {
		defer autoBackupWG.Done()
		if tempDecryptInFlight(memoryRoot) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), gitPushTimeout)
		defer cancel()
		_, runErr := syncWithContext(ctx, memoryRoot)
		recordBackupOutcome(memoryRoot, now, runErr)
	}()
}

// Status summarizes the vault's git backup configuration and working state.
type Status struct {
	IsRepo         bool
	Configured     bool
	RemoteURL      string
	Branch         string
	AutoBackup     bool
	LastAttempt    time.Time
	LastSuccess    time.Time
	LastError      string
	UnpushedCount  int
	DirtyFileCount int
}

// BackupStatus returns the current git backup configuration, stripped remote
// URL, last attempt/success/error timestamps, unpushed commit count, and dirty
// file count for memoryRoot.
func BackupStatus(memoryRoot string) Status {
	var st Status
	if cfg, err := LoadConfig(memoryRoot); err == nil && cfg != nil {
		st.Branch = cfg.Branch
		st.AutoBackup = cfg.AutoPush
	}
	if st.Branch == "" {
		st.Branch = "main"
	}

	backupMu.Lock()
	bst := loadBackupStateLocked(memoryRoot)
	backupMu.Unlock()
	st.LastAttempt = bst.LastAttempt
	st.LastSuccess = bst.LastSuccess
	st.LastError = bst.LastError

	if !isGitRepo(memoryRoot) {
		return st
	}
	st.IsRepo = true

	if rawURL := originURL(context.Background(), memoryRoot); rawURL != "" {
		st.RemoteURL = StripCredentials(rawURL)
		st.Configured = true
	}

	if out, err := runGit(context.Background(), memoryRoot, gitDefaultTimeout, "symbolic-ref", "--short", "HEAD"); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" {
			st.Branch = b
		}
	}

	if out, err := runGit(context.Background(), memoryRoot, gitDefaultTimeout, "rev-list", "--count", "@{u}..HEAD"); err == nil {
		if n, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil && n >= 0 {
			st.UnpushedCount = n
		}
	}

	if out, err := runGit(context.Background(), memoryRoot, gitDefaultTimeout, "status", "--porcelain"); err == nil {
		trimmed := strings.TrimRight(string(out), "\r\n")
		if trimmed != "" {
			count := 0
			for _, line := range strings.Split(trimmed, "\n") {
				if strings.TrimSpace(line) != "" {
					count++
				}
			}
			st.DirtyFileCount = count
		}
	}

	return st
}

func isGitRepo(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}
