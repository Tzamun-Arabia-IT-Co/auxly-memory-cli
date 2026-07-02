package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/session"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/mcp"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	defaultSSHPort     = 22
	defaultProviderID  = "claude-code"
	remoteInstallURL   = "https://auxly.io/cli"
	remoteInstallPS    = "https://auxly.io/cli.ps1"
	connectAuditAgent  = "auxly-connect"
	connectMCPArgsName = "connect-mcp"
)

// remoteProfile describes a single SSH-linked memory host. The sibling TUI
// reader expects the top-level `remotes:` key with `name:` and `host:` set.
type remoteProfile struct {
	Name    string   `yaml:"name"`
	Method  string   `yaml:"method"`       // "lan" | "vpn" | "bastion" | "public"
	OS      string   `yaml:"os,omitempty"` // "linux" | "darwin" | "windows"
	User    string   `yaml:"user"`
	Host    string   `yaml:"host"`
	Port    int      `yaml:"port,omitempty"`
	Jump    string   `yaml:"jump,omitempty"`
	SSHArgs []string `yaml:"ssh_args,omitempty"`
	// MemPath, when set, is passed to the host as `--path` so the remote
	// mcp-server serves a specific vault folder instead of the host's default
	// (~/.auxly/memory). Useful when the host stores memory outside $HOME.
	MemPath string `yaml:"mem_path,omitempty"`
	// HostBin is the absolute path to `auxly` ON THE HOST. Needed because a
	// non-interactive SSH command runs with a minimal PATH (e.g. macOS omits
	// /usr/local/bin), so a bare `auxly` may not resolve. Defaults to "auxly".
	HostBin string `yaml:"host_bin,omitempty"`

	// noMux, when set, forces this command onto its OWN SSH connection (no shared
	// ControlMaster). Unexported on purpose — runtime-only, never persisted. Used
	// for the long-lived install and the readiness poll during provisioning: if a
	// Windows install session lingers, isolating it means it can't wedge the shared
	// master that the follow-up steps (key-authorize, wire) reuse. See withoutMux.
	noMux bool `yaml:"-"`
}

// withoutMux returns a copy of p that runs on its own SSH connection (no
// ControlMaster multiplexing). Value copy — the original profile is untouched.
func withoutMux(p remoteProfile) remoteProfile {
	p.noMux = true
	return p
}

// hostAuxlyBin returns the command used to invoke auxly on the host — the
// profile's absolute HostBin when set, otherwise a bare "auxly" (PATH lookup).
func hostAuxlyBin(p remoteProfile) string {
	if strings.TrimSpace(p.HostBin) != "" {
		return p.HostBin
	}
	return "auxly"
}

type remotesConfig struct {
	Remotes []remoteProfile `yaml:"remotes"`
}

// ---------------------------------------------------------------------------
// Profile persistence (~/.auxly/remotes.yaml)
// ---------------------------------------------------------------------------

// auxlyDir resolves the ~/.auxly directory.
func auxlyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, config.DefaultDir), nil
}

// remotesPath returns the absolute path to remotes.yaml.
func remotesPath() (string, error) {
	dir, err := auxlyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remotes.yaml"), nil
}

// loadRemotes reads remotes.yaml, tolerating a missing file (empty config).
func loadRemotes() (remotesConfig, error) {
	var cfg remotesConfig
	path, err := remotesPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("failed to read remotes config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse remotes config %s: %w", path, err)
	}
	return cfg, nil
}

// saveRemotes writes remotes.yaml (0644), creating ~/.auxly first.
func saveRemotes(cfg remotesConfig) error {
	dir, err := auxlyDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal remotes config: %w", err)
	}
	path := filepath.Join(dir, "remotes.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write remotes config %s: %w", path, err)
	}
	return nil
}

// findRemote returns the profile matching name and whether it was found.
func findRemote(name string) (remoteProfile, bool) {
	cfg, err := loadRemotes()
	if err != nil {
		return remoteProfile{}, false
	}
	for _, p := range cfg.Remotes {
		if p.Name == name {
			return p, true
		}
	}
	return remoteProfile{}, false
}

// sameRemote reports whether two profiles refer to the same remote: either the
// same name, or the same connection identity (user@host:port + method). The
// latter is what stops a second row being created for a host that already
// exists under a different name — re-adding the same IP+method updates it in
// place instead of duplicating.
func sameRemote(a, b remoteProfile) bool {
	if a.Name == b.Name {
		return true
	}
	return a.Host == b.Host && a.Method == b.Method && a.User == b.User && a.Port == b.Port
}

// upsertRemote adds or replaces a profile and saves. A match is by name OR by
// connection identity (host+method+user+port), so the same target is never
// duplicated under a different name. Any pre-existing duplicates that match are
// collapsed into the single updated entry, preserving first-seen position.
func upsertRemote(p remoteProfile) error {
	cfg, err := loadRemotes()
	if err != nil {
		return err
	}
	replaced := false
	out := make([]remoteProfile, 0, len(cfg.Remotes)+1)
	for _, existing := range cfg.Remotes {
		if sameRemote(existing, p) {
			if !replaced {
				out = append(out, p) // update in place at the first match
				replaced = true
			}
			continue // drop any further duplicates
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, p)
	}
	// Feed the consumer link guard: remember this box has been wired to the
	// host, so a future silent loss of the profile is detected and surfaced
	// instead of quietly serving the stale local vault.
	mcp.RecordRemoteHistory(p.Name)
	return saveRemotes(remotesConfig{Remotes: out})
}

// persistDetectedOS records an auto-detected OS back onto a SAVED profile so later
// operations that cannot probe the host (e.g. installing the SSH key over a
// password session) know which shell family to use. It is a best-effort no-op for
// transient/unsaved profiles, or when the profile already declares an OS.
func persistDetectedOS(p remoteProfile, osName string) {
	if osName == "" || strings.TrimSpace(p.OS) != "" {
		return
	}
	saved, ok := findRemote(p.Name)
	if !ok || strings.TrimSpace(saved.OS) != "" {
		return
	}
	saved.OS = osName
	_ = upsertRemote(saved) // best-effort: on failure we simply re-detect next time
}

// ---------------------------------------------------------------------------
// SSH helpers
// ---------------------------------------------------------------------------

// connTarget renders the user@host segment of an ssh command.
func connTarget(p remoteProfile) string {
	if p.User != "" {
		return p.User + "@" + p.Host
	}
	return p.Host
}

// sshConnArgs returns the base ssh option args (BatchMode/ConnectTimeout/-J/-p
// plus user@host) reused by the launcher, doctor, and test paths.
// sshControlPath returns a per-target socket path for SSH connection multiplexing,
// or "" if the directory can't be prepared. Kept short (Unix socket paths cap near
// 104 bytes) and made unique via ssh's %C token (a hash of the connection 4-tuple).
func sshControlPath(p remoteProfile) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".ssh", "auxly-cm")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ""
	}
	return filepath.Join(dir, "%C")
}

func sshConnArgs(p remoteProfile) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
	}
	// Reuse ONE underlying SSH connection across the many short commands a connect/
	// provision runs (OS probe, key check, install, verify, hostname, wire). Without
	// multiplexing, each is a fresh pre-auth handshake, and a burst of them trips the
	// remote sshd's MaxStartups (Windows default 10) → "Connection reset by peer" and
	// a failed, half-saved provision. ControlMaster collapses the burst to a single
	// handshake. It is unsupported on a Windows *client*, so enable it only off-
	// Windows — and the only Windows-as-client case (a box dialing the host) makes a
	// single connection anyway, so it loses nothing.
	if runtime.GOOS != "windows" && !p.noMux {
		if cp := sshControlPath(p); cp != "" {
			args = append(args,
				"-o", "ControlMaster=auto",
				"-o", "ControlPath="+cp,
				"-o", "ControlPersist=30s",
			)
		}
	}
	// Through a relay/tunnel the endpoint is localhost:<reverse-port>, whose host
	// key is first-seen. Under BatchMode an unknown key would hard-fail, so accept
	// it on first contact (reachability is already gated by relay + key auth).
	if p.Method == "rendezvous" || p.Jump != "" {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	if p.Jump != "" {
		args = append(args, "-J", p.Jump)
	}
	if p.Port != 0 && p.Port != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(p.Port))
	}
	args = append(args, p.SSHArgs...)
	// "--" terminates ssh option processing so the target can never be parsed
	// as a flag, even if it somehow slipped past validateForExec.
	args = append(args, "--", connTarget(p))
	return args
}

// sshKeyAuthOK reports whether non-interactive (key-based) SSH to the target
// succeeds. It runs `exit 0`, a no-op that returns 0 on BOTH POSIX shells and
// Windows cmd.exe — unlike `true`, which is POSIX-only and makes cmd.exe error,
// false-negating key-auth on a Windows host.
func sshKeyAuthOK(p remoteProfile) bool {
	_, err := runSSH(p, "exit", "0")
	return err == nil
}

// defaultSSHTimeout bounds every non-interactive remote command so a remote that
// hangs (e.g. a Windows install/wire that leaves the SSH session lingering) can
// never block the CLI forever. All runSSH calls are short commands (probes,
// install, wire, hostname); 2 minutes is well above any legitimate runtime.
const defaultSSHTimeout = 120 * time.Second

// runSSH runs a remote command non-interactively and returns trimmed stdout,
// bounded by defaultSSHTimeout. For an explicit deadline, use runSSHCtx.
func runSSH(p remoteProfile, remoteCmd ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultSSHTimeout)
	defer cancel()
	return runSSHCtx(ctx, p, remoteCmd...)
}

// Host-reachability probe retry policy. When a box is freshly added, the host's
// keep-alive supervisor dials the box's reverse tunnel a few seconds later (it
// reconciles host.yaml on an interval), so the box's FIRST `--version` probe
// through localhost:<port> can land before the port is listening. Retrying across
// the startup window turns that race into a non-event instead of a skipped wire.
const (
	hostProbeAttempts = 6
	hostProbeBackoff  = 2 * time.Second
	// hostProbeTimeout caps EACH attempt so a probe that stalls (e.g. a relay
	// tunnel still coming up, or a slow non-interactive SSH context) fails fast and
	// the loop moves on, instead of stacking full-length runSSH timeouts. Worst
	// case ≈ attempts×timeout + backoffs, then callers wire anyway.
	hostProbeTimeout = 6 * time.Second
)

// retryProbe calls probe up to attempts times, sleeping backoff between tries,
// returning nil on the first success or the last error after exhausting attempts.
// Pure driver (probe injected via closure) so the retry policy is unit-testable
// without real SSH; pass backoff 0 in tests.
func retryProbe(probe func() error, attempts int, backoff time.Duration) error {
	var lastErr error
	for i := 1; i <= attempts; i++ {
		if err := probe(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if i < attempts {
			time.Sleep(backoff)
		}
	}
	return lastErr
}

// Install-readiness poll policy. After a (possibly detached, on Windows) install
// is kicked off, the box needs a few seconds to finish downloading + placing the
// binary and for a fresh SSH login to pick up the updated PATH. Polling
// `auxly --version` across that window is the real readiness signal — it works
// whether the install ran synchronously (POSIX) or detached in the background
// (Windows), so the connect never has to WAIT on the lingering install session.
const (
	installPollTimeout  = 150 * time.Second
	installPollInterval = 2 * time.Second
	installProbeTimeout = 8 * time.Second
	// installRunTimeout bounds the install session itself. It outlasts the poll so a
	// genuinely-slow install isn't reaped before readiness can be confirmed; on the
	// common path the poll succeeds far sooner and cancels it early.
	installRunTimeout = 180 * time.Second
)

// pollVerifyAuxly polls `auxly --version` on the box until it returns a non-empty
// version or the timeout elapses, returning the first line of the version output
// on success. Each individual probe is bounded by installProbeTimeout so a single
// stalled SSH session can never wedge the whole loop. This replaces the old single
// out-of-band verify that could block behind a lingering install on the shared
// ControlMaster socket.
func pollVerifyAuxly(p remoteProfile, timeout, interval time.Duration) (string, error) {
	return pollVerifyWith(func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), installProbeTimeout)
		defer cancel()
		return runSSHCtx(ctx, p, hostAuxlyBin(p), "--version")
	}, timeout, interval)
}

// pollVerifyWith is the pure driver behind pollVerifyAuxly: it calls probe until it
// returns a non-empty (whitespace-trimmed) string with no error, or the timeout
// elapses, returning the first non-empty line on success and the last error on
// failure. The probe is injected so the poll policy is unit-testable without real
// SSH (pass interval 0 in tests).
func pollVerifyWith(probe func() (string, error), timeout, interval time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := probe()
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(firstLine(out)), nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(interval)
	}
	if lastErr == nil {
		return "", fmt.Errorf("auxly did not become ready within %s", timeout)
	}
	return "", fmt.Errorf("auxly did not become ready within %s: %w", timeout, lastErr)
}

// probeHostReachable confirms the host answers `<hostbin> --version` over the
// profile's SSH, retrying across the tunnel-startup window (see hostProbeAttempts).
func probeHostReachable(p remoteProfile) error {
	return retryProbe(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), hostProbeTimeout)
		defer cancel()
		_, err := runSSHCtx(ctx, p, hostAuxlyBin(p), "--version")
		return err
	}, hostProbeAttempts, hostProbeBackoff)
}

// localHostname returns this machine's hostname (best effort).
func localHostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// parseHostSpec parses [user@]host[:port].
func parseHostSpec(spec string) (user, host string, port int, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", 0, fmt.Errorf("empty host specification")
	}
	if at := strings.LastIndex(spec, "@"); at >= 0 {
		user = spec[:at]
		spec = spec[at+1:]
	}
	switch {
	case strings.HasPrefix(spec, "["):
		// Bracketed IPv6: [2001:db8::1] or [2001:db8::1]:2222.
		end := strings.Index(spec, "]")
		if end < 0 {
			return "", "", 0, fmt.Errorf("invalid IPv6 host %q: missing closing ']'", spec)
		}
		host = spec[1:end]
		if rest := spec[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", "", 0, fmt.Errorf("invalid host %q: expected ':port' after ']'", spec)
			}
			p, perr := strconv.Atoi(rest[1:])
			if perr != nil {
				return "", "", 0, fmt.Errorf("invalid port %q: %w", rest[1:], perr)
			}
			port = p
		}
	case strings.Count(spec, ":") > 1:
		// Bare IPv6 is ambiguous — is the last group a port or part of the
		// address? Require brackets instead of guessing wrong silently.
		return "", "", 0, fmt.Errorf("ambiguous IPv6 host %q: use brackets — [%s] or [addr]:port", spec, spec)
	case strings.Contains(spec, ":"):
		colon := strings.LastIndex(spec, ":")
		host = spec[:colon]
		portStr := spec[colon+1:]
		p, perr := strconv.Atoi(portStr)
		if perr != nil {
			return "", "", 0, fmt.Errorf("invalid port %q: %w", portStr, perr)
		}
		port = p
	default:
		host = spec
	}
	if host == "" {
		return "", "", 0, fmt.Errorf("missing host in %q", spec)
	}
	// Reject leading '-' so a host/user can never be smuggled into ssh as a flag
	// (e.g. "-oProxyCommand=...") — see validateForExec for the use-site guard.
	if strings.HasPrefix(host, "-") {
		return "", "", 0, fmt.Errorf("invalid host %q: must not begin with '-'", host)
	}
	if strings.HasPrefix(user, "-") {
		return "", "", 0, fmt.Errorf("invalid user %q: must not begin with '-'", user)
	}
	return user, host, port, nil
}

// validateForExec is the use-site guard against argv flag smuggling. Profiles
// loaded from remotes.yaml bypass parseHostSpec, so every ssh-executing path
// re-validates: no host/user/jump may begin with '-', and no ssh_args entry may
// carry a command-executing option (ProxyCommand / LocalCommand /
// PermitLocalCommand) sourced from the YAML file.
func validateForExec(p remoteProfile) error {
	for label, v := range map[string]string{"host": p.Host, "user": p.User, "jump": p.Jump} {
		if strings.HasPrefix(strings.TrimSpace(v), "-") {
			return fmt.Errorf("refusing to use %s %q: must not begin with '-' (argv flag smuggling)", label, v)
		}
	}
	for _, a := range p.SSHArgs {
		// Validation applies to USER-SUPPLIED ssh_args only. Auxly's own
		// ControlMaster/ControlPath/ControlPersist and ProxyJump are generated in
		// sshConnArgs and never routed through here, so blocking them in user args
		// is regression-safe (M3).
		// Strip ALL whitespace (not just ASCII space) before matching: ssh accepts
		// a tab/space between `-o` and its keyword (`-o\tInclude=…`), so a space-only
		// strip would let `-o<TAB>Include` slip past the adjacency checks below.
		low := strings.ToLower(strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, a))
		// Command-executing options, or options that hijack the multiplex control
		// socket (Control*). These have no legitimate path-component use, so a plain
		// substring match is safe.
		for _, bad := range []string{"proxycommand", "localcommand", "permitlocalcommand", "controlmaster", "controlpath", "controlpersist"} {
			if strings.Contains(low, bad) {
				return fmt.Errorf("refusing ssh_args entry %q: %q is not allowed in remote profiles (loads external config or executes commands)", a, bad)
			}
		}
		// The SSH `Include` directive loads an EXTERNAL config (which could itself
		// carry a ProxyCommand). Match it as a directive only — `-oInclude=...`,
		// `-o Include ...` (one arg, spaces stripped → "-oinclude…") and a raw
		// `Include …` element — NOT the word "include" inside a legitimate path such
		// as `-i ~/include/id_ed25519` or `-oIdentityFile=/srv/include/key`.
		if strings.HasPrefix(low, "include") || strings.Contains(low, "-oinclude") {
			return fmt.Errorf("refusing ssh_args entry %q: the SSH Include directive is not allowed in remote profiles (loads external config)", a)
		}
		// -F (alternate config file) and -S (control socket), in both "-F file" and
		// "-Ffile" forms.
		t := strings.TrimSpace(a)
		if t == "-F" || t == "-S" || strings.HasPrefix(t, "-F") || strings.HasPrefix(t, "-S") {
			return fmt.Errorf("refusing ssh_args entry %q: alternate-config (-F) and control-socket (-S) flags are not allowed in remote profiles", a)
		}
	}
	// MemPath and HostBin are interpolated into the remote command line (re-parsed
	// by the host shell), so reject argv-flag smuggling and shell metacharacters.
	if mp := strings.TrimSpace(p.MemPath); mp != "" {
		if strings.HasPrefix(mp, "-") {
			return fmt.Errorf("refusing mem_path %q: must not begin with '-'", mp)
		}
		if strings.ContainsAny(mp, " \t\n;|&$`<>(){}*?!\"'\\") {
			return fmt.Errorf("refusing mem_path %q: must be a plain path with no whitespace or shell metacharacters", mp)
		}
	}
	// HostBin (H1): same anti-smuggling treatment. The metacharacter set excludes
	// backslash and colon so legitimate Windows host paths
	// (C:\...\auxly.exe) are still accepted; a bare "auxly" passes too.
	if hb := strings.TrimSpace(p.HostBin); hb != "" {
		if strings.HasPrefix(hb, "-") {
			return fmt.Errorf("refusing host_bin %q: must not begin with '-'", hb)
		}
		if strings.ContainsAny(hb, " \t\n;|&$`<>(){}*?!\"'") {
			return fmt.Errorf("refusing host_bin %q: must be a plain path with no whitespace or shell metacharacters", hb)
		}
	}
	return nil
}

// isPrivateHost is a cheap heuristic: RFC1918 / loopback / .local hostnames
// are treated as LAN, everything else as public.
func isPrivateHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	switch {
	case strings.HasPrefix(h, "10."),
		strings.HasPrefix(h, "192.168."),
		strings.HasPrefix(h, "127."),
		strings.HasPrefix(h, "169.254."):
		return true
	}
	if strings.HasPrefix(h, "172.") {
		parts := strings.Split(h, ".")
		if len(parts) >= 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil && n >= 16 && n <= 31 {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Doctor
// ---------------------------------------------------------------------------

// runDoctor verifies the local SSH client, probes the host over SSH, and
// silently provisions auxly on darwin/linux hosts when missing.
func runDoctor(p remoteProfile) error {
	fmt.Println("🩺 Running connection doctor...")

	// 1. Local SSH client (detect only; client ships with the OS).
	if _, err := exec.LookPath("ssh"); err != nil {
		printSSHClientGuidance()
		return fmt.Errorf("ssh client not found on this machine")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		printSSHClientGuidance()
		return fmt.Errorf("ssh-keygen not found on this machine")
	}
	fmt.Println("   ✓ Local SSH client present")

	fam, detail, derr := detectRemoteOS(p)
	if derr != nil {
		printConnectionFailureGuidance(p, derr)
		return fmt.Errorf("could not reach %s over SSH: %w", p.Host, derr)
	}

	switch fam {
	case osWindows:
		fmt.Printf("   ✓ Host reachable (Windows: %s)\n", detail)
		stepLine("connect", "ok")
		persistDetectedOS(p, "windows")
		if out, verErr := runSSH(p, "auxly", "--version"); verErr == nil {
			fmt.Println("   ✓ auxly present on host (Windows)")
			ensureRemoteCurrentAndWired(p, out, remoteUpdateOptIn())
			return nil
		}
		// The Windows box's default ssh shell is cmd.exe, so a unix `curl | sh`
		// can't run — but runRemoteScript routes through powershell -EncodedCommand,
		// which CAN install silently.
		fmt.Println("   ⬇ auxly not found on Windows host — installing via PowerShell...")
		// The installer over SSH can complete on the box yet leave the PowerShell
		// session lingering, so bound it and let the version re-probe below decide.
		if _, instErr := runRemoteScriptTimeout(p, osWindows, "", winInstallCmd(remoteInstallPS), 90*time.Second); instErr != nil && !errors.Is(instErr, context.DeadlineExceeded) {
			fmt.Printf("     • Auto-install failed. On the host, in PowerShell, run:  irm %s | iex\n", remoteInstallPS)
			return fmt.Errorf("failed to install auxly on Windows host %s: %w", p.Host, instErr)
		}
		// Re-probe in a fresh ssh session. A new session re-reads PATH from the
		// registry; if it's not live yet, fall back to the conventional install path.
		out, verErr := runSSH(p, "auxly", "--version")
		if verErr != nil {
			if out2, e2 := runRemoteScript(p, osWindows, "", `& "`+winAuxlyAbsPath+`" --version`); e2 == nil {
				out, verErr = strings.TrimSpace(out2), nil
			}
		}
		if verErr != nil {
			return fmt.Errorf("auxly still missing on Windows host %s after install (open a new shell to refresh PATH, then retry): %w", p.Host, verErr)
		}
		fmt.Println("   ✓ auxly installed on Windows host")
		recordProvision(p)
		ensureRemoteCurrentAndWired(p, out, remoteUpdateOptIn())
		return nil

	case osUnix:
		fmt.Printf("   ✓ Host reachable (uname: %s)\n", detail)
		stepLine("connect", "ok")
		if strings.Contains(strings.ToLower(detail), "darwin") {
			persistDetectedOS(p, "darwin")
		} else {
			persistDetectedOS(p, "linux")
		}
		if out, verErr := runSSH(p, "auxly", "--version"); verErr == nil {
			fmt.Println("   ✓ auxly present on host")
			ensureRemoteCurrentAndWired(p, out, remoteUpdateOptIn())
			return nil
		}
		fmt.Println("   ⬇ auxly not found on host — installing silently...")
		stepLine("install", "start")
		// Preflight curl: `curl | sh` conflates "curl missing" with every real
		// install failure. Name the actual problem.
		if _, cerr := runSSH(p, "command", "-v", "curl"); cerr != nil {
			return fmt.Errorf("curl is missing on %s — install it there first (e.g. `sudo apt install curl`), then retry", p.Host)
		}
		if _, instErr := runRemoteScript(p, osUnix, "curl -fsSL "+remoteInstallURL+" | sh", ""); instErr != nil {
			return fmt.Errorf("failed to install auxly on host %s: %w", p.Host, instErr)
		}
		out, verErr := runSSH(p, "auxly", "--version")
		if verErr != nil {
			return fmt.Errorf("auxly still missing on host %s after install attempt: %w", p.Host, verErr)
		}
		fmt.Println("   ✓ auxly installed on host")
		stepLine("install", "ok")
		recordProvision(p)
		// A fresh silent install already lands the latest binary, so the version check
		// is a no-op here — but still wire the remote statusline when opted in.
		ensureRemoteCurrentAndWired(p, out, remoteUpdateOptIn())
		return nil

	default: // osUnknown — detectRemoteOS sets derr in this case, so this is a safety net
		printConnectionFailureGuidance(p, fmt.Errorf("unrecognized remote OS"))
		return fmt.Errorf("could not determine OS of host %s over SSH", p.Host)
	}
}

// stepLine emits the machine-readable progress contract consumed by the TUI:
// AUXLY_STEP:<step>:<state>. Human lines around it stay; this one is stable.
func stepLine(step, state string) {
	fmt.Printf("AUXLY_STEP:%s:%s\n", step, state)
}

// checkTwoWay verifies the box can reach THIS machine back over SSH — that is
// the memory link's runtime path (the box's agents SSH here to read the vault).
// It returns the address the box proved reachable, which provisionRemote then
// wires, so the check validates the exact route that will be used. If the
// return path is missing, we stop with real fixes instead of a dead config.
func checkTwoWay(p remoteProfile) (string, error) {
	fmt.Println("🔁 Checking the memory-link return path (the box must reach this machine)...")
	addrs := localCandidateAddrs()
	if len(addrs) == 0 {
		fmt.Println("   ⚠ Could not determine this machine's IP addresses — skipping the return-path check.")
		return "", nil
	}
	if reachAddr, ok := hostCanReachBack(p, addrs); ok {
		fmt.Printf("   ✓ Return path OK — %s can reach this machine at %s:22\n", p.Host, reachAddr)
		stepLine("return-path", "ok")
		return reachAddr, nil
	}
	printTwoWayFailureGuidance(p, addrs)
	stepLine("return-path", "fail")
	// Machine-readable token so the TUI can offer the relay ([h]) / method-retry ([m]).
	fmt.Println("AUXLY_TWOWAY_FAILED:" + p.Name)
	return "", fmt.Errorf("no return path on '%s' — enable Remote Login/sshd on THIS machine, or set up the relay with `auxly host setup`", p.Method)
}

// localCandidateAddrs returns this machine's non-loopback IPv4 addresses — the
// addresses a remote might use to reach back to us.
func localCandidateAddrs() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			out = append(out, ip4.String())
		}
	}
	return out
}

// hostCanReachBack probes, FROM the host, whether it can open a TCP connection to
// any of our candidate addresses on port 22. Returns the first reachable one.
func hostCanReachBack(p remoteProfile, addrs []string) (string, bool) {
	fam, _, err := detectRemoteOS(p)
	if err != nil {
		fam = osUnix // best-effort: assume unix if detection is inconclusive
	}
	for _, a := range addrs {
		posix := fmt.Sprintf("nc -z -w3 %s 22 >/dev/null 2>&1 || timeout 3 bash -c 'echo > /dev/tcp/%s/22' >/dev/null 2>&1", a, a)
		powershell := fmt.Sprintf("if((Test-NetConnection -ComputerName %s -Port 22 -WarningAction SilentlyContinue).TcpTestSucceeded){exit 0}else{exit 1}", a)
		if _, err := runRemoteScript(p, fam, posix, powershell); err == nil {
			return a, true
		}
	}
	return "", false
}

func printTwoWayFailureGuidance(p remoteProfile, addrs []string) {
	fmt.Printf("   ✗ %s can't reach this machine back over '%s' — the memory link needs a return path.\n", p.Host, p.Method)
	fmt.Println("     Fixes, in order:")
	fmt.Println("     1. Enable the SSH server on THIS machine (the box dials back in to read memory):")
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("        macOS: System Settings → General → Sharing → Remote Login (turn ON)")
	case "linux":
		fmt.Println("        Linux: sudo systemctl enable --now ssh")
	case "windows":
		fmt.Println("        Windows: Settings → Apps → Optional Features → add 'OpenSSH Server'")
	}
	if len(addrs) > 0 {
		fmt.Printf("        …then re-run; the box will be probed against %s\n", strings.Join(addrs, ", "))
	}
	fmt.Println("     2. If a firewall/NAT still blocks it: run `auxly host setup` on THIS machine —")
	fmt.Println("        it dials OUT to a relay you control, and the box connects through that.")
}

// recordProvision logs the silent host install to the audit trail (best effort).
func recordProvision(p remoteProfile) {
	logger, err := audit.NewLogger(getMemoryPath())
	if err != nil {
		return
	}
	_, _ = logger.LogWithSource(
		connectAuditAgent,
		"system",
		"provision",
		"auxly@"+p.Host,
		"",
		"silent OS-aware install of auxly on remote host",
		"auto",
		audit.SourceMeta{Source: "local"},
	)
}

func printSSHClientGuidance() {
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("   ✗ SSH client missing. macOS ships with OpenSSH; reinstall the Command Line Tools:")
		fmt.Println("     xcode-select --install")
	case "linux":
		fmt.Println("   ✗ SSH client missing. Install OpenSSH client via your package manager, e.g.:")
		fmt.Println("     sudo apt install openssh-client   # Debian/Ubuntu")
		fmt.Println("     sudo dnf install openssh-clients  # Fedora/RHEL")
	case "windows":
		fmt.Println("   ✗ SSH client missing. Enable the OpenSSH Client optional feature:")
		fmt.Println("     Settings → Apps → Optional Features → Add 'OpenSSH Client'")
	default:
		fmt.Println("   ✗ SSH client missing. Install an OpenSSH client for your platform.")
	}
}

func printConnectionFailureGuidance(p remoteProfile, cause error) {
	fmt.Printf("   ✗ Could not reach %s over SSH (%v).\n", p.Host, cause)
	fmt.Println("     The SSH server (sshd) may be disabled on the host. Enable it on the host:")
	switch strings.ToLower(strings.TrimSpace(p.OS)) {
	case "darwin":
		fmt.Println("     • macOS: System Settings → General → Sharing → Remote Login")
	case "linux":
		fmt.Println("     • Linux: sudo systemctl enable --now ssh")
	case "windows":
		fmt.Println("     • Windows: enable the OpenSSH Server optional feature (Settings → Apps →")
		fmt.Printf("                Optional Features), then install auxly on the host: irm %s | iex\n", remoteInstallPS)
	default:
		// Target OS wasn't specified — show all so the user can act.
		fmt.Println("     • macOS:   System Settings → General → Sharing → Remote Login")
		fmt.Println("     • Linux:   sudo systemctl enable --now ssh")
		fmt.Println("     • Windows: enable the OpenSSH Server optional feature (Settings → Apps →")
		fmt.Printf("                Optional Features), then install auxly on the host: irm %s | iex\n", remoteInstallPS)
	}
}

// ---------------------------------------------------------------------------
// Launcher: connect-mcp <name> --provider <id>
// ---------------------------------------------------------------------------

var connectMCPProvider string

var connectMCPCmd = &cobra.Command{
	Use:    "connect-mcp <name>",
	Short:  "Transparent SSH stdio launcher for a remote auxly memory host",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE:   runConnectMCP,
}

func runConnectMCP(cmd *cobra.Command, args []string) error {
	name := args[0]
	p, ok := findRemote(name)
	if !ok {
		// Friendly error to stderr only; nothing on stdout (JSON-RPC stream).
		return fmt.Errorf("remote profile %q not found in remotes.yaml", name)
	}

	if err := validateForExec(p); err != nil {
		return err
	}

	if connectMCPSelftest {
		return runConnectSelftest(p)
	}

	provider := connectMCPProvider
	if provider == "" {
		provider = defaultProviderID
	}

	sshArgs := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		// Keepalives so an idle or briefly-flaky link doesn't silently die — the
		// memory session (and the box's "connected" status) stays up as long as the
		// agent runs. ~3×30s of unanswered probes before ssh declares it dead.
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "TCPKeepAlive=yes",
		"-C",
	}
	// Through a relay/tunnel the endpoint is localhost:<reverse-port>, whose host
	// key is first-seen. BatchMode would otherwise hard-fail on an unknown key, so
	// accept it on first contact (reachability is already gated by relay+key auth).
	if p.Method == "rendezvous" || p.Jump != "" {
		sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=accept-new")
	}
	if p.Jump != "" {
		sshArgs = append(sshArgs, "-J", p.Jump)
	}
	if p.Port != 0 && p.Port != defaultSSHPort {
		sshArgs = append(sshArgs, "-p", strconv.Itoa(p.Port))
	}
	sshArgs = append(sshArgs, p.SSHArgs...)
	// "--" terminates ssh option processing before the target.
	sshArgs = append(sshArgs, "--", connTarget(p))
	serverArgs := []string{
		"mcp-server",
		"--provider", provider,
		"--source", "ssh-remote",
		"--remote-os", runtime.GOOS,
		"--remote-host", localHostname(),
	}
	if p.MemPath != "" {
		serverArgs = append(serverArgs, "--path", p.MemPath)
	}

	// Resilient launch: when the ssh transport itself fails (exit 255 — the relay
	// tunnel isn't up yet at agent launch, or a transient blip), the remote
	// mcp-server never started and no stdin was consumed, so it's safe to retry
	// while the tunnel comes up. Once the remote command has actually run, we do
	// NOT retry: a mid-session drop is stateful (the MCP `initialize` handshake
	// would be lost), so we surface it and let the client respawn connect-mcp,
	// which re-establishes and re-handshakes cleanly.
	//
	// Exit 127 is different: the remote shell ran but the auxly binary at
	// host_bin is GONE (the "provisioned against a dev binary that later moved"
	// incident). That fails instantly, so we walk a fallback chain — bare PATH
	// lookup, then the standard install locations — and persist whichever path
	// worked back into the profile so the next launch goes straight there.
	const (
		maxLauncherAttempts = 3
		launcherBackoff     = time.Second
	)
	candidates := hostBinCandidates(p)
	originalDead := false // candidate #1 (the configured host_bin) proven gone via 127
	for ci, bin := range candidates {
		remote := append([]string{bin}, serverArgs...)
		for attempt := 1; ; attempt++ {
			launch := exec.Command("ssh", append(append([]string{}, sshArgs...), remote...)...)
			launch.Stdin = os.Stdin
			launch.Stdout = os.Stdout
			launch.Stderr = os.Stderr
			started := time.Now()
			err := launch.Run()
			// Persist a repair ONLY when the configured host_bin was actually
			// observed dead (127) AND the replacement is an absolute path — a
			// bare "auxly" must never be persisted (non-interactive SSH PATH on
			// macOS omits /usr/local/bin, so it is strictly less reliable than
			// an absolute path and is already the implicit fallback).
			ranForReal := err == nil || time.Since(started) > 30*time.Second
			if ranForReal && originalDead && bin != hostAuxlyBin(p) && strings.HasPrefix(bin, "/") {
				repairHostBin(p, bin) // this candidate served a session — remember it
			}
			if err == nil {
				return nil // clean exit — the client closed the stream
			}
			code, isExit := sshExitCode(err)
			// 127 is the POSIX not-found convention; a Windows host reports a
			// different code, so the chain (and repairHostBin) is POSIX-hosts
			// only — a Windows host fails straight through with the raw error.
			if isExit && code == 127 && ci < len(candidates)-1 {
				if ci == 0 {
					originalDead = true
				}
				fmt.Fprintf(os.Stderr, "auxly connect-mcp: %q not found on host — trying %q\n", bin, candidates[ci+1])
				break // next candidate
			}
			if shouldRetryLauncher(code, isExit, attempt, maxLauncherAttempts) {
				time.Sleep(launcherBackoff) // transport failure → tunnel may be coming up
				continue
			}
			return fmt.Errorf("ssh launcher to %s failed (attempt %d): %w", p.Host, attempt, err)
		}
	}
	return fmt.Errorf("auxly not found on host %s at any known location — reinstall it there or fix host_bin in remotes.yaml", p.Host)
}

// hostBinCandidates is the launch order for the auxly binary on the host: the
// profile's host_bin first, then PATH, then the standard install locations.
// $HOME expands in the remote POSIX shell. The chain only advances on POSIX
// exit 127 — a Windows host's missing-command code differs, so Windows hosts
// get candidate #1 only and surface the raw error (see the launcher loop).
func hostBinCandidates(p remoteProfile) []string {
	cands := []string{}
	if strings.TrimSpace(p.HostBin) != "" {
		cands = append(cands, p.HostBin)
	}
	for _, c := range []string{"auxly", "$HOME/.bun/bin/auxly", "/usr/local/bin/auxly", "$HOME/.local/bin/auxly"} {
		dup := false
		for _, have := range cands {
			if have == c {
				dup = true
			}
		}
		if !dup {
			cands = append(cands, c)
		}
	}
	return cands
}

// repairHostBin persists a working binary path into the profile after the
// configured one turned out dead, so every future launch (and the agents'
// configs that reference this profile) skips the broken path. Best-effort.
func repairHostBin(p remoteProfile, workingBin string) {
	p.HostBin = workingBin
	if err := upsertRemote(p); err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "auxly connect-mcp: repaired host_bin for %q → %s\n", p.Name, workingBin)
	if logger, err := audit.NewLogger(getMemoryPath()); err == nil {
		defer logger.Close()
		logger.Log("connect-mcp", "system", "hostbin_repair", "remotes.yaml", "",
			fmt.Sprintf("profile %s: host_bin was dead, now %s", p.Name, workingBin), "auto")
	}
}

// sshExitCode extracts the ssh process exit code from a launch error. isExit is
// false when the error isn't a normal process exit (e.g. ssh failed to start).
func sshExitCode(err error) (code int, isExit bool) {
	var ee *exec.ExitError
	if err != nil && errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}

// shouldRetryLauncher reports whether a connect-mcp ssh failure is safe to retry:
// only an ssh transport failure (exit 255, before the remote mcp-server ran) and
// only while attempts remain. A non-255 exit means the remote command actually
// executed — stdin may have been consumed and the session was live — so retrying
// would corrupt the stateful MCP stream. Pure — unit-testable with plain ints.
func shouldRetryLauncher(exitCode int, isExit bool, attempt, maxAttempts int) bool {
	return isExit && exitCode == 255 && attempt < maxAttempts
}

// ---------------------------------------------------------------------------
// connect command tree
// ---------------------------------------------------------------------------

var connectCmd = &cobra.Command{
	Use:     "connect [host]",
	Aliases: []string{"remote"},
	// Don't dump usage text on a RunE error — keeps the TUI's captured output clean.
	SilenceUsage: true,
	Short:        "Link this machine to a remote Auxly memory host over SSH",
	Long: `connect links this (remote/agent) machine to a memory HOST over SSH.

Run with no arguments for an interactive wizard, or pass [user@]host[:port]
to add a profile non-interactively. SSH is the only transport — there is no
daemon, port, or token.`,
	Args: cobra.ArbitraryArgs,
	RunE: runConnect,
}

var connectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured remote memory hosts",
	RunE:  runConnectList,
}

var connectRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a configured remote memory host",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnectRemove,
}

var connectTestCmd = &cobra.Command{
	Use:   "test <name>",
	Short: "Run the reachability + host-dependency doctor for a remote",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnectTest,
}

var connectPrintCmd = &cobra.Command{
	Use:   "print <name>",
	Short: "Print the MCP JSON block for manual paste",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnectPrint,
}

var connectUseCmd = &cobra.Command{
	Use:          "use <name>",
	Short:        "Use a host's memory FROM this machine (consumer direction; works through NAT)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConnectUse,
}

var connectDisconnectCmd = &cobra.Command{
	Use:          "disconnect <name>",
	Short:        "Remove a host's launcher/profile from this machine (leave no trace)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConnectDisconnect,
}

var connectAutoCmd = &cobra.Command{
	Use:          "auto [name]",
	Short:        "Connect to a host advertised on this relay — no flags (used by /auxly-remote-connect)",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runConnectAuto,
}

// Consumer-direction flags for `connect use` (create-on-the-fly) and the
// `connect disconnect` purge toggle.
var (
	useHost         string
	useJump         string
	useMethod       string
	useMemPath      string
	useHostBin      string
	disconnectPurge bool
)

// connect add — flag-driven, non-interactive add used by the TUI Remote tab.
// The TUI collects the form natively, then runs this; the only terminal prompt
// is the SSH password during first-time key install (ssh reads it from /dev/tty).
var (
	addName    string
	addMethod  string
	addOS      string
	addHost    string
	addJump    string
	addMemPath string
	addBatch   bool
	// addStandalone preserves the pre-v1.2 behavior: give the box its own local
	// vault instead of wiring it to this machine's memory.
	addStandalone bool
)

// normalizeOS maps user input to the canonical target OS, defaulting to linux.
func normalizeOS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mac", "macos", "osx", "darwin":
		return "darwin"
	case "win", "windows":
		return "windows"
	case "linux", "":
		return "linux"
	default:
		return "linux"
	}
}

// keyAuthWorks reports whether key-based SSH auth to the host already succeeds.
func keyAuthWorks(p remoteProfile) bool {
	return sshKeyAuthOK(p)
}

var connectAddCmd = &cobra.Command{
	Use:    "add",
	Short:  "Add a remote from flags (key bootstrap + doctor + save + IDE config)",
	Hidden: true,
	RunE:   runConnectAdd,
}

func init() {
	connectMCPCmd.Flags().StringVar(&connectMCPProvider, "provider", "", "provider id used for attribution (default: claude-code)")
	connectMCPCmd.Flags().BoolVar(&connectMCPSelftest, "selftest", false, "probe the end-to-end memory link and exit")

	connectAddCmd.Flags().StringVar(&addName, "name", "", "profile name (defaults to host)")
	connectAddCmd.Flags().StringVar(&addMethod, "method", "", "reachability: lan|vpn|bastion|public")
	connectAddCmd.Flags().StringVar(&addOS, "os", "", "target host OS: linux|darwin|windows (default linux)")
	connectAddCmd.Flags().StringVar(&addHost, "host", "", "[user@]host[:port] of the memory host")
	connectAddCmd.Flags().StringVar(&addJump, "jump", "", "jump host ([user@]host) for the bastion method")
	connectAddCmd.Flags().StringVar(&addMemPath, "mem-path", "", "host memory folder to serve (passed as --path; default: host's ~/.auxly/memory)")
	connectAddCmd.Flags().BoolVar(&addBatch, "batch", false, "non-interactive: never prompt for an SSH password (fail fast if key auth is missing)")
	connectAddCmd.Flags().BoolVar(&addStandalone, "standalone", false, "give the box its own local vault instead of wiring it to this machine's memory")
	connectCmd.Flags().BoolVar(&addStandalone, "standalone", false, "give the box its own local vault instead of wiring it to this machine's memory")

	connectUseCmd.Flags().StringVar(&useHost, "host", "", "[user@]host[:port] to create the profile if it doesn't exist yet")
	connectUseCmd.Flags().StringVar(&useJump, "jump", "", "jump/relay host ([user@]host) — for rendezvous reachability through a relay")
	connectUseCmd.Flags().StringVar(&useMethod, "method", "", "reachability label: lan|vpn|bastion|public|rendezvous")
	connectUseCmd.Flags().StringVar(&useMemPath, "mem-path", "", "host memory folder to serve (passed as --path)")
	connectUseCmd.Flags().StringVar(&useHostBin, "host-bin", "", "absolute path to auxly ON THE HOST (when not on the host's SSH PATH, e.g. macOS /usr/local/bin)")
	connectDisconnectCmd.Flags().BoolVar(&disconnectPurge, "purge", false, "also remove the installed /auxly-* skills from this machine")

	// Opt-in remote maintenance: bump an older auxly on the host (and ensure its
	// statusline) as part of connecting, over the same SSH. Persistent so every
	// connect subcommand that runs the doctor honours it; otherwise the persisted
	// UpdateRemotesOnConnect setting decides.
	connectCmd.PersistentFlags().BoolVar(&connectUpdateRemote, "update-remote", false, "if the host's auxly is older, update it in place over SSH (skips a host mid-session)")

	connectCmd.AddCommand(connectListCmd)
	connectCmd.AddCommand(connectRemoveCmd)
	connectCmd.AddCommand(connectTestCmd)
	connectCmd.AddCommand(connectPrintCmd)
	connectCmd.AddCommand(connectUseCmd)
	connectCmd.AddCommand(connectDisconnectCmd)
	connectCmd.AddCommand(connectAutoCmd)
	connectCmd.AddCommand(connectAddCmd)

	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(connectMCPCmd)
}

func runConnectAdd(cmd *cobra.Command, args []string) error {
	if addHost == "" {
		return fmt.Errorf("--host is required (e.g. --host user@mac-mini.local)")
	}
	user, host, port, err := parseHostSpec(addHost)
	if err != nil {
		return fmt.Errorf("invalid --host: %w", err)
	}
	name := addName
	if name == "" {
		name = host
	}
	method := addMethod
	if method == "" {
		method = "public"
		if isPrivateHost(host) {
			method = "lan"
		}
	}
	p := remoteProfile{Name: name, Method: method, OS: normalizeOS(addOS), User: user, Host: host, Port: port, Jump: addJump, MemPath: addMemPath}

	if addBatch {
		// Batch mode (the TUI runs the doctor captured in-pane): never block on a
		// password. If key auth isn't set up yet, fail fast with a token the TUI
		// detects to offer a terminal-based key-setup step.
		if !keyAuthWorks(p) {
			// Return nil (not an error) so rootCmd's help banner isn't dumped into
			// the TUI's captured output. The TUI keys off the token line below.
			fmt.Printf("⚠️  SSH key authentication to %s is not set up yet.\n", p.Host)
			fmt.Println("   The host could not be reached with your existing keys.")
			fmt.Println("AUXLY_KEY_REQUIRED")
			return nil
		}
	} else if err := bootstrapKeyAuth(p); err != nil {
		fmt.Printf("⚠️  Key setup skipped/failed: %v\n", err)
	}
	if err := runDoctor(p); err != nil {
		return err
	}
	if err := connectTest(p); err != nil {
		return err
	}
	// Save the profile BEFORE the two-way check: it's a valid consumer profile
	// either way (this machine → host works), and saving it first means the [u]
	// "use this host's memory" fallback can find it when two-way fails.
	if err := upsertRemote(p); err != nil {
		return err
	}
	fmt.Printf("💾 Saved remote profile %q (%s)\n", p.Name, p.Method)
	backAddr, err := checkTwoWay(p)
	if err != nil {
		return err
	}
	if err := provisionRemote(p, backAddr); err != nil {
		// The connect's actual goal is the memory link — a wiring failure must
		// never hide behind a green summary (the box may be entirely unconfigured).
		fmt.Printf("✗ memory-link wiring failed: %v\n", err)
		return err
	}
	printConnectSummary(p)
	return nil
}

// bootstrapKeyAuth ensures key-based SSH auth works, generating an ed25519 key and
// installing it on the host if needed. Non-interactive (assumes consent — the TUI
// form already confirmed intent); the SSH password during key install is read by
// ssh from /dev/tty, so it works under tea.ExecProcess.
func bootstrapKeyAuth(p remoteProfile) error {
	if sshKeyAuthOK(p) {
		return nil // key auth already works
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to resolve home directory: %w", err)
	}
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	pubPath := keyPath + ".pub"
	if _, statErr := os.Stat(keyPath); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); mkErr != nil {
			return fmt.Errorf("failed to create ~/.ssh: %w", mkErr)
		}
		fmt.Println("🔑 Generating ed25519 key…")
		gen := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath)
		gen.Stdin, gen.Stdout, gen.Stderr = os.Stdin, os.Stdout, os.Stderr
		if genErr := gen.Run(); genErr != nil {
			return fmt.Errorf("ssh-keygen failed: %w", genErr)
		}
	}
	return installPubKey(p, pubPath)
}

func runConnect(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return runConnectWizard()
	}

	// Non-interactive add. Optional trailing `-- <ssh args...>`.
	dash := cmd.ArgsLenAtDash()
	var spec string
	var extraSSH []string
	if dash >= 0 {
		if dash == 0 {
			return fmt.Errorf("missing host specification before --")
		}
		spec = args[0]
		extraSSH = args[dash:]
	} else {
		spec = args[0]
	}

	user, host, port, err := parseHostSpec(spec)
	if err != nil {
		return fmt.Errorf("invalid host specification: %w", err)
	}

	method := "public"
	if isPrivateHost(host) {
		method = "lan"
	}

	p := remoteProfile{
		Name:    host,
		Method:  method,
		User:    user,
		Host:    host,
		Port:    port,
		SSHArgs: extraSSH,
	}

	if err := runDoctor(p); err != nil {
		return err
	}
	if err := connectTest(p); err != nil {
		return err
	}
	if err := upsertRemote(p); err != nil {
		return err
	}
	fmt.Printf("💾 Saved remote profile %q (%s)\n", p.Name, p.Method)
	backAddr, err := checkTwoWay(p)
	if err != nil {
		return err
	}
	if err := provisionRemote(p, backAddr); err != nil {
		// The connect's actual goal is the memory link — a wiring failure must
		// never hide behind a green summary (the box may be entirely unconfigured).
		fmt.Printf("✗ memory-link wiring failed: %v\n", err)
		return err
	}
	printConnectSummary(p)
	return nil
}

func runConnectList(cmd *cobra.Command, args []string) error {
	cfg, _ := loadRemotes()        // outbound: this machine -> memory hosts
	clients, _ := loadClients()    // inbound: boxes using this machine's memory
	liveHosts := liveRemoteHosts() // hosts with a live ssh-remote session now

	if len(cfg.Remotes) == 0 && len(clients) == 0 {
		fmt.Println("No connections yet.")
		fmt.Println("  • Connect to a memory host:        auxly connect")
		fmt.Println("  • Serve your memory to other boxes: auxly host setup")
		return nil
	}

	// Outbound — memory hosts this machine reads/writes through.
	fmt.Println("🌐 Memory hosts (this machine → host):")
	if len(cfg.Remotes) == 0 {
		fmt.Println("   (none — run `auxly connect` to add one)")
	} else {
		for _, p := range cfg.Remotes {
			target := connTarget(p)
			if p.Port != 0 && p.Port != defaultSSHPort {
				target = fmt.Sprintf("%s:%d", target, p.Port)
			}
			fmt.Printf("   • %-20s %-30s [%s]\n", p.Name, target, p.Method)
		}
	}

	// Inbound — boxes wired to use THIS machine's memory (host side). Mirrors
	// the TUI Remote tab / `auxly host clients`, so the CLI and TUI agree.
	fmt.Println("\n🖥  Connected boxes (using this machine's memory):")
	if len(clients) == 0 {
		fmt.Println("   (none — run `auxly host setup` to share your memory)")
	} else {
		for _, c := range clients {
			status := "○ idle"
			if clientIsLive(liveHosts, c) {
				status = "● live"
			}
			fmt.Printf("   • %-20s %-22s [%s]  %s\n", c.Name, c.Target, c.Method, status)
		}
	}
	return nil
}

// liveRemoteHosts returns the set of client hostnames (lowercased) that have a
// live ssh-remote MCP session right now, from the session registry (pruning
// records whose process has died).
func liveRemoteHosts() map[string]bool {
	records := session.List()
	pids := make([]int, 0, len(records))
	for _, r := range records {
		pids = append(pids, r.PID)
	}
	alive := session.PidsAlive(pids)

	live := map[string]bool{}
	for _, r := range records {
		if r.Source == "ssh-remote" && r.RemoteHost != "" && alive[r.PID] {
			live[strings.ToLower(r.RemoteHost)] = true
		}
	}
	return live
}

func runConnectRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := loadRemotes()
	if err != nil {
		return err
	}
	out := make([]remoteProfile, 0, len(cfg.Remotes))
	found := false
	for _, p := range cfg.Remotes {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("remote profile %q not found", name)
	}
	if err := saveRemotes(remotesConfig{Remotes: out}); err != nil {
		return err
	}
	mcp.ForgetRemoteHistory(name) // deliberate removal — no "link lost" banner
	fmt.Printf("🗑️  Removed remote profile %q\n", name)
	return nil
}

func runConnectTest(cmd *cobra.Command, args []string) error {
	name := args[0]
	p, ok := findRemote(name)
	if !ok {
		return fmt.Errorf("remote profile %q not found", name)
	}
	if err := runDoctor(p); err != nil {
		return err
	}
	if _, err := checkTwoWay(p); err != nil {
		return err
	}
	fmt.Printf("✅ Remote %q passed all checks.\n", name)
	return nil
}

func runConnectPrint(cmd *cobra.Command, args []string) error {
	name := args[0]
	if _, ok := findRemote(name); !ok {
		return fmt.Errorf("remote profile %q not found", name)
	}
	fmt.Printf(`{"mcpServers":{"auxly-memory":{"command":"auxly","args":["connect-mcp","%s","--provider","claude-code"]}}}`+"\n", name)
	return nil
}

// runConnectUse configures THIS machine to USE the host's memory (consumer
// direction: this machine → host). This works even when the host can't reach
// back (NAT), because this machine dials out. It injects the connect-mcp launcher
// into this machine's IDE configs and installs the skills locally.
func runConnectUse(cmd *cobra.Command, args []string) error {
	name := args[0]
	p, ok := findRemote(name)
	if !ok {
		var err error
		p, err = createConsumerProfile(name)
		if err != nil {
			return err
		}
	}
	fmt.Printf("🔗 Configuring THIS machine to use %s's memory...\n", connTarget(p))
	// Confirm the outbound direction works (this machine → host), using the host's
	// absolute auxly path when known (a bare `auxly` may not be on the host's
	// minimal non-interactive SSH PATH). Retry across the tunnel-startup window —
	// and crucially, wire the agent EVEN IF the probe never succeeds: the launcher,
	// skills, and statusline are local config writes that take effect at runtime
	// under the agent's full environment, so a transient probe miss must never leave
	// the box half-configured (the silent half-wire bug).
	if err := probeHostReachable(p); err != nil {
		fmt.Printf("   ⚠ Couldn't confirm the host is reachable yet (%v)\n", err)
		fmt.Println("   Wiring the agent anyway — the launcher connects at runtime once the tunnel is up.")
	} else {
		fmt.Println("   ✓ Reached the host (this direction works)")
	}
	injectRemoteConfigs(p.Name)
	installAuxlySkills(remoteBanner())
	if wired := statusline.AutoInstallMissing(); len(wired) > 0 {
		fmt.Printf("   ✓ Installed the Auxly statusline for: %s\n", strings.Join(wired, ", "))
	}
	fmt.Println()
	fmt.Printf("🎉 This machine now uses %s's memory.\n", connTarget(p))
	fmt.Println("   • connect-mcp launcher injected into your IDEs/agents")
	fmt.Println("   • /auxly-* skills installed (shared-vault banner)")
	fmt.Println("👉 Restart your IDE / agent; /auxly-remote-connect will show the live link.")
	return nil
}

// createConsumerProfile builds and saves a consumer-direction profile from the
// `connect use` flags when no profile with that name exists yet. For the
// rendezvous flow it bootstraps this machine's key onto BOTH the relay (the
// jump) and the host (the final hop through the relay) — the one-time setup.
func createConsumerProfile(name string) (remoteProfile, error) {
	if strings.TrimSpace(useHost) == "" {
		return remoteProfile{}, fmt.Errorf("remote profile %q not found (pass --host [user@]host[:port] to create it)", name)
	}
	user, host, port, err := parseHostSpec(useHost)
	if err != nil {
		return remoteProfile{}, fmt.Errorf("invalid --host: %w", err)
	}
	method := useMethod
	if method == "" {
		if useJump != "" {
			method = "rendezvous"
		} else if isPrivateHost(host) {
			method = "lan"
		} else {
			method = "public"
		}
	}
	p := remoteProfile{
		Name:    name,
		Method:  method,
		User:    user,
		Host:    host,
		Port:    port,
		Jump:    useJump,
		MemPath: useMemPath,
		HostBin: strings.TrimSpace(useHostBin),
	}

	// One-time key bootstrap. The relay must trust this machine's key BEFORE we
	// can jump through it to the host, so install onto the relay first.
	if p.Jump != "" {
		if ju, jh, jp, jerr := parseHostSpec(p.Jump); jerr == nil {
			relay := remoteProfile{Name: "relay", Method: "public", User: ju, Host: jh, Port: jp}
			if err := bootstrapKeyAuth(relay); err != nil {
				fmt.Printf("⚠️  Key setup to the relay failed: %v\n", err)
			}
		}
	}
	if err := bootstrapKeyAuth(p); err != nil {
		fmt.Printf("⚠️  Key setup to the host failed: %v\n", err)
	}
	if err := upsertRemote(p); err != nil {
		return remoteProfile{}, err
	}
	fmt.Printf("💾 Saved remote profile %q (%s)\n", p.Name, p.Method)
	return p, nil
}

// offersDir returns ~/.auxly/offers (where the host publishes relayOffers).
func offersDir() (string, error) {
	dir, err := auxlyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "offers"), nil
}

// loadLocalOffers reads every relayOffer descriptor the host published locally.
func loadLocalOffers() ([]relayOffer, error) {
	dir, err := offersDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var offers []relayOffer
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var o relayOffer
		if yaml.Unmarshal(data, &o) == nil && o.Name != "" && o.ReversePort != 0 {
			offers = append(offers, o)
		}
	}
	return offers, nil
}

// localPubKey ensures this machine has an ed25519 key and returns its public
// half (generating one non-interactively if absent).
func localPubKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	pubPath := keyPath + ".pub"
	if _, statErr := os.Stat(keyPath); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); mkErr != nil {
			return "", mkErr
		}
		gen := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath)
		if genErr := gen.Run(); genErr != nil {
			return "", fmt.Errorf("ssh-keygen failed: %w", genErr)
		}
	}
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// runConnectAuto wires this box to a host advertised in a local relayOffer — the
// flag-free path. It never prompts: if the box's key isn't authorized on the host
// yet, it prints the key to add and stops (so it's safe to run from an agent).
func runConnectAuto(cmd *cobra.Command, args []string) error {
	offers, err := loadLocalOffers()
	if err != nil {
		return err
	}
	if len(offers) == 0 {
		return fmt.Errorf("no connect offers found in ~/.auxly/offers — run `auxly host setup` on the memory host first")
	}

	// Select the offer: by name if given, else the only one, else list and stop.
	offer := offers[0]
	if len(args) > 0 {
		matched := false
		for _, o := range offers {
			if o.Name == args[0] {
				offer, matched = o, true
				break
			}
		}
		if !matched {
			return fmt.Errorf("no offer named %q (available: %s)", args[0], offerNames(offers))
		}
	} else if len(offers) > 1 {
		return fmt.Errorf("multiple hosts offered (%s) — run `auxly connect auto <name>`", offerNames(offers))
	}

	p := remoteProfile{
		Name:    offer.Name,
		Method:  "rendezvous",
		User:    offer.HostUser,
		Host:    "localhost",
		Port:    offer.ReversePort,
		HostBin: offer.HostBin,
	}
	fmt.Printf("🔗 Connecting this machine to %s's memory (via the relay tunnel)...\n", offer.Name)

	// Reachability + key check. If auth fails, warn but wire anyway — on a
	// reconnect the tunnel may be temporarily down; the profile/skills/statusline
	// are all local writes that activate once the tunnel comes back up.
	if !sshKeyAuthOK(p) {
		fmt.Printf("   ⚠ Can't reach %s via tunnel yet — wiring anyway (will connect once the tunnel is back up).\n", offer.Name)
		if pub, perr := localPubKey(); perr == nil && pub != "" {
			fmt.Println("   (First time? Add this box's public key to the host's ~/.ssh/authorized_keys:)")
			fmt.Printf("   %s\n", pub)
		}
	}

	// Retry across the tunnel-startup window; wire even if it never answers (the
	// launcher/skills/statusline are local writes that work at runtime under the
	// agent's env). A transient probe miss must never skip wiring.
	if err := probeHostReachable(p); err != nil {
		fmt.Printf("   ⚠ Couldn't confirm %s is reachable yet (%v)\n", offer.Name, err)
		fmt.Println("   Wiring the agent anyway — it connects at runtime once the tunnel is up.")
	}
	if err := upsertRemote(p); err != nil {
		return err
	}
	fmt.Printf("   ✓ Saved the profile for %s\n", offer.Name)

	injectRemoteConfigs(p.Name)
	installAuxlySkills(remoteBanner())
	// Carry the statusline preference onto this machine too: auto-wire the Auxly
	// statusline for any detected agent that has none yet (idempotent — a machine
	// with its own statusline is left alone). Makes the statusline a follows-you
	// setting instead of a per-box manual step.
	if wired := statusline.AutoInstallMissing(); len(wired) > 0 {
		fmt.Printf("   ✓ Installed the Auxly statusline for: %s\n", strings.Join(wired, ", "))
	}
	fmt.Println()
	fmt.Printf("🎉 This machine now uses %s's memory.\n", offer.Name)
	fmt.Println("👉 RESTART your IDE / agent to load it — then /auxly-remote-connect shows the live link.")
	return nil
}

// offerNames renders a comma-separated list of offer names for messages.
func offerNames(offers []relayOffer) string {
	names := make([]string, 0, len(offers))
	for _, o := range offers {
		names = append(names, o.Name)
	}
	return strings.Join(names, ", ")
}

// runConnectDisconnect removes a host's launcher (and optionally skills + the
// saved profile) from THIS machine so a shared box is left with no trace of the
// connection. No memory is ever copied locally, so there is nothing else to wipe.
func runConnectDisconnect(cmd *cobra.Command, args []string) error {
	name := args[0]
	fmt.Printf("🧹 Disconnecting %q from this machine...\n", name)

	removed := removeRemoteConfigs(name)
	if len(removed) > 0 {
		fmt.Println("   ✓ Removed the MCP launcher from:")
		for _, app := range removed {
			fmt.Printf("     ↳ %s\n", app)
		}
	} else {
		fmt.Println("   • No injected MCP launcher found for this host")
	}

	if disconnectPurge {
		n := removeAuxlySkills()
		fmt.Printf("   ✓ Removed Auxly skills from %d location(s)\n", n)
	}

	if _, ok := findRemote(name); ok {
		if err := deleteRemoteProfile(name); err != nil {
			fmt.Printf("   ⚠ Could not remove saved profile: %v\n", err)
		} else {
			fmt.Printf("   ✓ Removed saved profile %q (relay/host coordinates)\n", name)
		}
	}

	// Deliberate disconnect: clear the link-guard history so the local MCP
	// server never shows a false "MEMORY LINK LOST" banner for a link the user
	// intentionally removed.
	mcp.ForgetRemoteHistory(name)

	fmt.Println("👉 Restart your IDE/agent to drop the connection. No memory was stored on this machine.")
	return nil
}

// deleteRemoteProfile drops a profile from remotes.yaml by name.
func deleteRemoteProfile(name string) error {
	cfg, err := loadRemotes()
	if err != nil {
		return err
	}
	out := make([]remoteProfile, 0, len(cfg.Remotes))
	for _, p := range cfg.Remotes {
		if p.Name != name {
			out = append(out, p)
		}
	}
	return saveRemotes(remotesConfig{Remotes: out})
}

// removeRemoteConfigs strips the auxly-memory launcher entry (matching this
// host's connect-mcp profile) from every known IDE/agent config on this machine.
func removeRemoteConfigs(name string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var removed []string
	seen := map[string]bool{}
	for _, t := range knownIDETargets(home) {
		if seen[t.Path] {
			continue
		}
		seen[t.Path] = true
		if removeAuxlyEntry(t.Path, name) {
			removed = append(removed, t.AppName)
		}
	}
	return removed
}

// removeAuxlyEntry deletes the "auxly-memory" server from a single JSON config
// file when it is OUR connect-mcp launcher for the given profile name. Returns
// true if the file was modified.
func removeAuxlyEntry(path, name string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg map[string]interface{}
	if json.Unmarshal(data, &cfg) != nil || cfg == nil {
		return false
	}
	changed := false
	if servers, ok := cfg["mcpServers"].(map[string]interface{}); ok {
		if def, ok := servers["auxly-memory"]; ok && launcherMatches(def, name) {
			delete(servers, "auxly-memory")
			changed = true
		}
	}
	if def, ok := cfg["auxly-memory"]; ok && launcherMatches(def, name) {
		delete(cfg, "auxly-memory")
		changed = true
	}
	if !changed {
		return false
	}
	newData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false
	}
	// IDE/agent MCP config JSON (Claude Desktop, Cursor, Copilot, …) is not secret
	// and `auxly setup` writes it 0644 — match that so connect/disconnect doesn't
	// leave the file at a different, more-restrictive mode than setup created it.
	return os.WriteFile(path, newData, 0644) == nil
}

// launcherMatches reports whether a server definition is our connect-mcp
// launcher for the given profile name (guards against nuking a local
// mcp-server entry or a different host's launcher).
func launcherMatches(def interface{}, name string) bool {
	m, ok := def.(map[string]interface{})
	if !ok {
		return false
	}
	rawArgs, ok := m["args"].([]interface{})
	if !ok {
		return false
	}
	hasLauncher, hasName := false, false
	for _, a := range rawArgs {
		s, _ := a.(string)
		if s == connectMCPArgsName {
			hasLauncher = true
		}
		if s == name {
			hasName = true
		}
	}
	return hasLauncher && hasName
}

// removeAuxlySkills deletes the installed /auxly-* skill folders from every
// agent skills dir. Returns the count of locations touched.
func removeAuxlySkills() int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	skillDirs := []string{
		filepath.Join(home, ".claude", "skills"),
		".claude/skills",
		filepath.Join(home, ".codex", "skills"),
		".codex/skills",
		filepath.Join(home, ".gemini", "config", "skills"),
	}
	count := 0
	for _, base := range skillDirs {
		touched := false
		for skillName := range getSkillsMap() {
			dir := filepath.Join(base, skillName)
			if _, statErr := os.Stat(dir); statErr == nil {
				if os.RemoveAll(dir) == nil {
					touched = true
				}
			}
		}
		if touched {
			count++
		}
	}
	return count
}

// winAuxlyAbsPath is the installer's known per-user location for auxly on Windows.
// Invoking it directly is PATH-independent, so a version/wire probe still resolves
// even when a non-interactive SSH session hasn't picked up the freshly-added PATH.
const winAuxlyAbsPath = `$env:LOCALAPPDATA\Programs\auxly\auxly.exe`

// remoteAuxlyVersionBanner returns the box's `auxly --version` banner. It tries a
// bare PATH lookup first and, on failure, falls back to the known Windows install
// path via PowerShell (PATH-independent). Runs on a FRESH connection (withoutMux)
// so it never reuses a ControlMaster opened before auxly was installed — the stale
// session carries the pre-install PATH and would falsely report the box missing.
// On a non-Windows box the fallback simply errors (no powershell), so the original
// failure is preserved.
func remoteAuxlyVersionBanner(p remoteProfile) (string, error) {
	if out, err := runSSH(withoutMux(p), hostAuxlyBin(p), "--version"); err == nil {
		return out, nil
	}
	out, err := runRemoteScript(withoutMux(p), osWindows, "", `& "`+winAuxlyAbsPath+`" --version`)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// connectTest runs the lightweight `auxly --version` reachability check.
func connectTest(p remoteProfile) error {
	fmt.Println("🔌 Testing remote auxly availability...")
	out, err := remoteAuxlyVersionBanner(p)
	if err != nil {
		return fmt.Errorf("remote `auxly --version` failed: %w", err)
	}
	fmt.Printf("   ✓ %s\n", firstLine(out))
	return nil
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}

// remoteBanner is appended to installed skills to flag the shared remote vault.
func remoteBanner() string {
	return "\n\nNOTE: You are connected to a shared Auxly memory host over SSH — reads/writes are central and audited on the host; other agents may share this vault."
}

// injectRemoteConfigs writes the connect-mcp MCP entry into every known IDE
// target for the given remote profile name.
func injectRemoteConfigs(name string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("⚠️  Could not resolve home directory for config injection: %v\n", err)
		return
	}
	var configured []string
	for _, t := range knownIDETargets(home) {
		serverDef := map[string]interface{}{
			"command": "auxly",
			"args":    []interface{}{connectMCPArgsName, name, "--provider", t.ProviderID},
		}
		app, werr := writeMCPConfigEntry(t, serverDef)
		if werr != nil {
			// Same fail-loud contract as `auxly setup`: a config-write failure
			// must never be silently dropped from the "configured" list.
			fmt.Printf("⚠️  %s: MCP config write failed — %v\n", t.AppName, werr)
			continue
		}
		if app != "" {
			configured = append(configured, app)
		}
	}
	if len(configured) > 0 {
		fmt.Println("🧩 Injected remote MCP config for:")
		for _, app := range configured {
			fmt.Printf("   ↳ %s\n", app)
		}
	}
}

// provisionRemote turns the freshly-reachable host into a fully Auxly-enabled
// node. The host already has the binary (the doctor installed it); this runs
// `auxly setup` ON the host over SSH so the host's OWN binary injects the MCP
// config and installs the skills into every agent it detects — correctly for
// the host's own OS and home dir. Honors p.MemPath via the global --path flag
// so the host serves a specific vault folder. (runSSH validates the profile.)
// provisionRemote is the last step of `auxly connect <box>`: it makes the box's
// agents READ THIS MACHINE'S memory over the direct route (defect D1 fix — the
// old behavior gave the box its own standalone vault, which is now opt-in via
// --standalone). backAddr is the address the box proved it can reach us at
// (from checkTwoWay).
func provisionRemote(p remoteProfile, backAddr string) error {
	if addStandalone {
		return provisionStandalone(p)
	}
	if backAddr == "" {
		// No proven return path — direct wiring cannot work; say so rather than
		// silently falling back to a standalone vault the user didn't ask for.
		return fmt.Errorf("no return path for the memory link — use the relay (`auxly host setup`) or `--standalone` for a local-only setup on the box")
	}

	fmt.Println("🔗 Wiring the box's agents to THIS machine's memory...")

	// The box reaches back here at runtime as this login, key-authenticated:
	// fetch (or create) the box's key and authorize it locally.
	if kerr := authorizeRemoteKeyLocally(p); kerr != nil {
		fmt.Printf("   ⚠ could not authorize the box's key on this machine: %v\n", kerr)
	} else {
		fmt.Println("   ✓ Authorized the box's SSH key on this machine")
		stepLine("key-auth", "ok")
	}

	hostBin := "auxly"
	if exe, e := os.Executable(); e == nil && exe != "" {
		hostBin = exe
	}
	boxHostname := ""
	if out, herr := runSSH(p, "hostname"); herr == nil {
		boxHostname = strings.TrimSpace(firstLine(out))
	}

	// Record the connection BEFORE wiring (same rationale as the relay flow: a
	// stalled wire must not leave an unmanaged box) — this also puts direct
	// boxes into clients.yaml so the hourly reconciler covers them.
	if err := upsertClient(clientEntry{Name: p.Name, Target: connTarget(p), Method: "direct", Hostname: boxHostname}); err == nil {
		fmt.Printf("   ✓ Saved connection %q (manage with `auxly host clients`)\n", p.Name)
	}

	// Wire on a FRESH connection: the shared ControlMaster may predate the
	// auxly install on the box (stale PATH).
	target := currentLogin() + "@" + backAddr
	wireArgs := []string{"auxly", "connect", "use", offerName(), "--method", "public", "--host", target, "--host-bin", hostBin}
	if p.MemPath != "" {
		wireArgs = append(wireArgs, "--mem-path", p.MemPath)
	}
	stepLine("wire", "start")
	if out, werr := runSSH(withoutMux(p), wireArgs...); werr != nil {
		stepLine("wire", "fail")
		fmt.Printf("   ⚠ box wiring failed: %v\n   %s\n", werr, firstLine(out))
		return fmt.Errorf("memory-link wiring failed on %s (connection saved — retry from `auxly host clients`): %w", p.Name, werr)
	}
	stepLine("wire", "ok")
	fmt.Println("   ✓ Box agents wired — they now read this machine's memory")

	// Success is PROVEN by a real read, never claimed from config writes: run
	// the end-to-end selftest on the box (it execs the exact launcher the
	// agents will use and calls auxly_memory_list through it).
	fmt.Println("🔎 Verifying the box can actually read this memory...")
	stepLine("selftest", "start")
	if out, serr := runSSH(withoutMux(p), "auxly", "connect-mcp", offerName(), "--selftest"); serr == nil {
		stepLine("selftest", "ok")
		fmt.Printf("   ✅ box read this machine's memory: %s\n", firstLine(out))
	} else {
		stepLine("selftest", "fail")
		fmt.Printf("   ⚠ link selftest failed: %s — fix and re-run from `auxly host clients`\n", firstLine(out))
		return fmt.Errorf("memory link wired but the proving read failed on %s: %s", p.Name, firstLine(out))
	}
	fmt.Println("👉 RESTART the agent on the box to load its memory link.")
	return nil
}

// provisionStandalone is the pre-v1.2 behavior: give the box its own local
// Auxly vault (bare `auxly setup` there). Explicit opt-in via --standalone.
func provisionStandalone(p remoteProfile) error {
	fmt.Println("📦 Provisioning the host (installing MCP + skills for its agents)...")
	remoteCmd := []string{"auxly"}
	if p.MemPath != "" {
		remoteCmd = append(remoteCmd, "--path", p.MemPath)
	}
	remoteCmd = append(remoteCmd, "setup")

	out, err := runSSH(p, remoteCmd...)
	if err != nil {
		fmt.Printf("   ⚠ remote setup failed: %v\n", err)
		if out != "" {
			for _, line := range strings.Split(out, "\n") {
				fmt.Printf("     %s\n", strings.TrimRight(line, "\r"))
			}
		}
		return err
	}
	// Echo the host's "configured" lines so the user sees which agents got wired.
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimRight(line, "\r")
		if strings.Contains(l, "↳") || strings.Contains(l, "Successfully") || strings.Contains(l, "configured") {
			fmt.Printf("   %s\n", strings.TrimSpace(l))
		}
	}
	fmt.Println("   ✓ Host agents provisioned (skills + MCP installed on the host).")
	return nil
}

func printConnectSummary(p remoteProfile) {
	fmt.Println()
	fmt.Println("🎉 Remote host provisioned!")
	fmt.Printf("   Profile : %s\n", p.Name)
	fmt.Printf("   Host    : %s\n", connTarget(p))
	fmt.Printf("   Method  : %s\n", p.Method)
	fmt.Println("   • auxly binary installed on the host")
	fmt.Println("   • MCP + skills installed for the host's own agents")
	if p.MemPath != "" {
		fmt.Printf("   • Memory vault on host: %s\n", p.MemPath)
	}
	fmt.Println()
	fmt.Println("👉 Restart the agents ON THE HOST to load Auxly's tools and /auxly-* skills.")
}

// ---------------------------------------------------------------------------
// Wizard
// ---------------------------------------------------------------------------

func runConnectWizard() error {
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Println("🧙 Auxly Remote Connect Wizard")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("Link this machine to a shared Auxly memory host over SSH.")
	fmt.Println()

	// Step 1: target OS (decides install method + guidance — no guessing).
	targetOS := wizardSelectOS(reader)

	// Step 2: method.
	method := wizardSelectMethod(reader)

	// Cheap pre-fill discovery.
	suggestHostAliases()
	if method == "vpn" {
		suggestTailscalePeers()
	}

	// user@host.
	spec := promptLine(reader, "Enter the host as [user@]host[:port]: ")
	user, host, port, err := parseHostSpec(spec)
	if err != nil {
		return fmt.Errorf("invalid host specification: %w", err)
	}

	var jump string
	if method == "bastion" {
		jump = promptLine(reader, "Enter the jump host ([user@]host): ")
	}

	name := promptLine(reader, fmt.Sprintf("Profile name [%s]: ", host))
	if name == "" {
		name = host
	}

	p := remoteProfile{
		Name:   name,
		Method: method,
		OS:     targetOS,
		User:   user,
		Host:   host,
		Port:   port,
		Jump:   jump,
	}

	// Step 2: SSH key auth.
	if err := ensureKeyAuth(reader, p); err != nil {
		fmt.Printf("⚠️  Key setup skipped/failed: %v\n", err)
	}

	// Step 3: doctor.
	if err := runDoctor(p); err != nil {
		return err
	}

	// Step 4: test.
	if err := connectTest(p); err != nil {
		return err
	}

	// Step 5: save FIRST — valid consumer profile either way, and lets the [u]
	// fallback find it if the two-way check fails.
	if err := upsertRemote(p); err != nil {
		return err
	}
	fmt.Printf("💾 Saved remote profile %q\n", p.Name)

	// Step 5b: return path (the box must reach this machine back).
	backAddr, err := checkTwoWay(p)
	if err != nil {
		return err
	}

	// Step 6: wire the box's agents to this machine's memory (or --standalone).
	if err := provisionRemote(p, backAddr); err != nil {
		fmt.Printf("✗ memory-link wiring failed: %v\n", err)
		return err
	}

	// Step 7: summary.
	printConnectSummary(p)
	return nil
}

func wizardSelectOS(reader *bufio.Scanner) string {
	fmt.Println("What OS is the target host?")
	fmt.Println("  1) Linux")
	fmt.Println("  2) macOS")
	fmt.Println("  3) Windows")
	choice := promptLine(reader, "Select [1-3]: ")
	switch strings.TrimSpace(choice) {
	case "2":
		return "darwin"
	case "3":
		return "windows"
	default:
		return "linux"
	}
}

func wizardSelectMethod(reader *bufio.Scanner) string {
	fmt.Println("How do you reach the memory host?")
	fmt.Println("  1) Same network (LAN)")
	fmt.Println("  2) Over a VPN (e.g. Tailscale)")
	fmt.Println("  3) Jump host / bastion")
	fmt.Println("  4) Public / custom")
	choice := promptLine(reader, "Select [1-4]: ")
	switch strings.TrimSpace(choice) {
	case "1":
		return "lan"
	case "2":
		return "vpn"
	case "3":
		return "bastion"
	default:
		return "public"
	}
}

// suggestHostAliases prints Host aliases found in ~/.ssh/config (best effort).
func suggestHostAliases() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return
	}
	var aliases []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Host") {
			for _, alias := range fields[1:] {
				if alias != "*" && !strings.ContainsAny(alias, "*?") {
					aliases = append(aliases, alias)
				}
			}
		}
	}
	if len(aliases) > 0 {
		fmt.Printf("   💡 Known SSH hosts: %s\n", strings.Join(aliases, ", "))
	}
}

// suggestTailscalePeers runs `tailscale status` only if the binary exists.
func suggestTailscalePeers() {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return
	}
	out, err := exec.Command("tailscale", "status").CombinedOutput()
	if err != nil {
		return
	}
	fmt.Println("   💡 Tailscale peers:")
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fmt.Printf("      %s\n", line)
		count++
		if count >= 10 {
			break
		}
	}
}

// ensureKeyAuth checks key-based auth and offers to generate + install a key.
func ensureKeyAuth(reader *bufio.Scanner, p remoteProfile) error {
	// If BatchMode auth already works, nothing to do.
	if sshKeyAuthOK(p) {
		fmt.Println("   ✓ Key-based SSH auth already works")
		return nil
	}

	ans := promptLine(reader, "No working SSH key auth. Generate an ed25519 key and install it on the host? [Y/n]: ")
	if strings.EqualFold(strings.TrimSpace(ans), "n") {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to resolve home directory: %w", err)
	}
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	pubPath := keyPath + ".pub"

	if _, statErr := os.Stat(keyPath); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); mkErr != nil {
			return fmt.Errorf("failed to create ~/.ssh: %w", mkErr)
		}
		fmt.Println("   🔑 Generating ed25519 key...")
		gen := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath)
		gen.Stdin = os.Stdin
		gen.Stdout = os.Stdout
		gen.Stderr = os.Stderr
		if genErr := gen.Run(); genErr != nil {
			return fmt.Errorf("ssh-keygen failed: %w", genErr)
		}
	} else {
		fmt.Println("   🔑 Using existing key at ~/.ssh/id_ed25519")
	}

	return installPubKey(p, pubPath)
}

// installPubKey installs the public key on the host (ssh-copy-id if present,
// otherwise appends to ~/.ssh/authorized_keys over one password SSH).
func installPubKey(p remoteProfile, pubPath string) error {
	if err := validateForExec(p); err != nil {
		return err
	}
	target := connTarget(p)
	if _, err := exec.LookPath("ssh-copy-id"); err == nil {
		fmt.Println("   📤 Installing public key via ssh-copy-id (you may be prompted for a password)...")
		// accept-new so a first-time host key never adds a yes/no prompt — the only
		// interactive prompt is then the password (important for the TUI's PTY flow).
		args := []string{"-o", "StrictHostKeyChecking=accept-new", "-i", pubPath}
		// ssh-copy-id has no -J flag; pass the jump as an -o ProxyJump option so the
		// rendezvous flow (key onto the host through the relay) still works.
		if p.Jump != "" {
			args = append(args, "-o", "ProxyJump="+p.Jump)
		}
		if p.Port != 0 && p.Port != defaultSSHPort {
			args = append(args, "-p", strconv.Itoa(p.Port))
		}
		args = append(args, target)
		copyCmd := exec.Command("ssh-copy-id", args...)
		copyCmd.Stdin = os.Stdin
		copyCmd.Stdout = os.Stdout
		copyCmd.Stderr = os.Stderr
		if err := copyCmd.Run(); err == nil {
			return nil
		}
		// ssh-copy-id is flaky through ProxyJump (and on odd remote shells) —
		// fall through to the manual authorized_keys append, same as when the
		// tool is absent, instead of dead-ending the whole connect.
		fmt.Println("   ⚠ ssh-copy-id failed — falling back to a manual key append")
	}

	// Manual fallback: append pubkey over one interactive (password) SSH.
	pub, err := os.ReadFile(pubPath)
	if err != nil {
		return fmt.Errorf("failed to read public key %s: %w", pubPath, err)
	}
	fmt.Println("   📤 Installing public key over SSH (you may be prompted for a password)...")
	pubKey := strings.TrimSpace(string(pub))
	fam := classifyOS(p.OS)
	if fam == osUnknown && strings.TrimSpace(p.OS) == "" {
		fmt.Println("   ⚠ Host OS not specified in the profile — assuming a POSIX host for key install. If this is Windows, re-run the wizard and select Windows.")
	}

	// remoteArgs is the per-OS remote command appended after "-- target".
	var remoteArgs []string
	if fam == osWindows {
		// On Windows the authorized_keys location differs for admins. Detect admin
		// at runtime and write to the correct file with the correct ACL.
		ps := `$ErrorActionPreference='Stop'; ` +
			`$key=` + psQuote(pubKey) + `; ` +
			`$admin=([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator); ` +
			`if($admin){ $f="$env:ProgramData\ssh\administrators_authorized_keys"; ` +
			`New-Item -ItemType File -Force -Path $f | Out-Null; ` +
			`Add-Content -Path $f -Value $key; ` +
			`takeown /F $f /A | Out-Null; ` +
			`icacls $f /inheritance:r /grant 'Administrators:F' /grant 'SYSTEM:F' | Out-Null } ` +
			`else { $d="$env:USERPROFILE\.ssh"; New-Item -ItemType Directory -Force -Path $d | Out-Null; ` +
			`Add-Content -Path "$d\authorized_keys" -Value $key }`
		remoteArgs = []string{"powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(ps)}
	} else {
		remoteScript := "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo " +
			shellQuote(pubKey) +
			" >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
		remoteArgs = []string{"sh", "-c", shellQuote(remoteScript)}
	}

	args := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if p.Jump != "" {
		args = append(args, "-J", p.Jump)
	}
	if p.Port != 0 && p.Port != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(p.Port))
	}
	// "--" terminates ssh option processing before the target. ssh-copy-id (the
	// preferred path above) has no "--" support, so it relies on validateForExec.
	args = append(args, "--", target)
	args = append(args, remoteArgs...)
	pkCmd := exec.Command("ssh", args...)
	pkCmd.Stdin = os.Stdin
	pkCmd.Stdout = os.Stdout
	pkCmd.Stderr = os.Stderr
	if err := pkCmd.Run(); err != nil {
		return fmt.Errorf("failed to append public key on host: %w", err)
	}
	return nil
}

// shellQuote single-quotes a string for safe POSIX shell embedding.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func promptLine(reader *bufio.Scanner, prompt string) string {
	fmt.Print(prompt)
	if !reader.Scan() {
		return ""
	}
	return strings.TrimSpace(reader.Text())
}
