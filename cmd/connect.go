package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/session"
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
	if err := os.WriteFile(path, data, 0644); err != nil {
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

// upsertRemote adds or replaces a profile (matched by name) and saves.
func upsertRemote(p remoteProfile) error {
	cfg, err := loadRemotes()
	if err != nil {
		return err
	}
	replaced := false
	out := make([]remoteProfile, 0, len(cfg.Remotes)+1)
	for _, existing := range cfg.Remotes {
		if existing.Name == p.Name {
			out = append(out, p)
			replaced = true
		} else {
			out = append(out, existing)
		}
	}
	if !replaced {
		out = append(out, p)
	}
	return saveRemotes(remotesConfig{Remotes: out})
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
func sshConnArgs(p remoteProfile) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
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

// runSSH runs a remote command non-interactively and returns trimmed stdout.
func runSSH(p remoteProfile, remoteCmd ...string) (string, error) {
	if err := validateForExec(p); err != nil {
		return "", err
	}
	args := append(sshConnArgs(p), remoteCmd...)
	cmd := exec.Command("ssh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("ssh %s: %w", strings.Join(remoteCmd, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
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
	if colon := strings.LastIndex(spec, ":"); colon >= 0 {
		host = spec[:colon]
		portStr := spec[colon+1:]
		p, perr := strconv.Atoi(portStr)
		if perr != nil {
			return "", "", 0, fmt.Errorf("invalid port %q: %w", portStr, perr)
		}
		port = p
	} else {
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
		low := strings.ToLower(strings.ReplaceAll(a, " ", ""))
		if strings.Contains(low, "proxycommand") ||
			strings.Contains(low, "localcommand") ||
			strings.Contains(low, "permitlocalcommand") {
			return fmt.Errorf("refusing ssh_args entry %q: command-executing ssh options are not allowed in remote profiles", a)
		}
	}
	// MemPath is interpolated into the remote command line (re-parsed by the
	// host shell), so reject argv-flag smuggling and shell metacharacters.
	if mp := strings.TrimSpace(p.MemPath); mp != "" {
		if strings.HasPrefix(mp, "-") {
			return fmt.Errorf("refusing mem_path %q: must not begin with '-'", mp)
		}
		if strings.ContainsAny(mp, " \t\n;|&$`<>(){}*?!\"'\\") {
			return fmt.Errorf("refusing mem_path %q: must be a plain path with no whitespace or shell metacharacters", mp)
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

	targetOS := strings.ToLower(strings.TrimSpace(p.OS))

	// Windows host: there is no `uname`, and a `curl | sh` silent install can't
	// run in the host's cmd.exe/PowerShell shell. So we probe auxly directly and,
	// if it's not available, guide the user with the PowerShell one-liner.
	if targetOS == "windows" {
		if _, verErr := runSSH(p, "auxly", "--version"); verErr == nil {
			fmt.Println("   ✓ auxly present on host (Windows)")
			return nil
		}
		fmt.Printf("   ⚠ auxly is not reachable on the Windows host %s.\n", p.Host)
		fmt.Println("     • If SSH itself failed: enable the OpenSSH Server optional feature on the host")
		fmt.Println("       (Settings → Apps → Optional Features).")
		fmt.Printf("     • Then install auxly ON THE HOST in PowerShell:  irm %s | iex\n", remoteInstallPS)
		return fmt.Errorf("auxly not available on Windows host %s; enable sshd if needed, run `irm %s | iex` on the host, then retry", p.Host, remoteInstallPS)
	}

	// Unix host (linux/darwin — or unspecified, which we treat as unix and confirm
	// via uname). Probe reachability + arch with uname.
	uname, unameErr := runSSH(p, "uname", "-sm")
	if unameErr != nil {
		printConnectionFailureGuidance(p, unameErr)
		return fmt.Errorf("could not reach %s over SSH: %w", p.Host, unameErr)
	}
	fmt.Printf("   ✓ Host reachable (uname: %s)\n", uname)

	// auxly already present?
	if _, verErr := runSSH(p, "auxly", "--version"); verErr == nil {
		fmt.Println("   ✓ auxly present on host")
		return nil
	}

	// Silent install on the (confirmed) unix host.
	fmt.Println("   ⬇ auxly not found on host — installing silently...")
	if _, instErr := runSSH(p, "sh", "-c", "'curl -fsSL "+remoteInstallURL+" | sh'"); instErr != nil {
		return fmt.Errorf("failed to install auxly on host %s: %w", p.Host, instErr)
	}
	if _, verErr := runSSH(p, "auxly", "--version"); verErr != nil {
		return fmt.Errorf("auxly still missing on host %s after install attempt: %w", p.Host, verErr)
	}
	fmt.Println("   ✓ auxly installed on host")
	recordProvision(p)
	return nil
}

// checkTwoWay verifies the HOST can reach THIS machine back over SSH. When this
// machine is the memory host, the remote must be able to SSH in to it at runtime
// (that's how its mcp-server launches). If the return path is missing, we stop
// with a clear explanation + alternatives instead of leaving a dead config.
func checkTwoWay(p remoteProfile) error {
	fmt.Println("🔁 Checking two-way connectivity (the host must reach this machine back)...")
	addrs := localCandidateAddrs()
	if len(addrs) == 0 {
		fmt.Println("   ⚠ Could not determine this machine's IP addresses — skipping two-way check.")
		return nil
	}
	if reachAddr, ok := hostCanReachBack(p, addrs); ok {
		fmt.Printf("   ✓ Two-way OK — %s can reach this machine at %s:22\n", p.Host, reachAddr)
		return nil
	}
	printTwoWayFailureGuidance(p, addrs)
	// Machine-readable token so the TUI can offer the relay ([h]) / method-retry ([m]).
	fmt.Println("AUXLY_TWOWAY_FAILED:" + p.Name)
	return fmt.Errorf("no direct return path on '%s' — set up the relay tunnel with `auxly host setup` (recommended)", p.Method)
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
	for _, a := range addrs {
		// Prefer nc; fall back to bash's /dev/tcp. addrs are our own numeric IPs.
		script := fmt.Sprintf("nc -z -w3 %s 22 >/dev/null 2>&1 || timeout 3 bash -c 'echo > /dev/tcp/%s/22' >/dev/null 2>&1", a, a)
		if _, err := runSSH(p, "sh", "-c", "'"+script+"'"); err == nil {
			return a, true
		}
	}
	return "", false
}

func printTwoWayFailureGuidance(p remoteProfile, addrs []string) {
	fmt.Printf("   ✗ %s can't reach this Mac back over '%s' (NAT) — no direct return path.\n", p.Host, p.Method)
	fmt.Println("     This Mac is your memory HOST. The fix is a relay tunnel, not another method:")
	fmt.Println("     → Run `auxly host setup` on THIS Mac — it dials out to a public relay you")
	fmt.Println("       control, then prints the `auxly connect use --jump …` command for the host.")
	fmt.Println("     (Another method only helps if it gives a real return path, e.g. same-LAN/Tailscale.)")
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

	provider := connectMCPProvider
	if provider == "" {
		provider = defaultProviderID
	}

	sshArgs := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-C"}
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
	sshArgs = append(sshArgs,
		hostAuxlyBin(p), "mcp-server",
		"--provider", provider,
		"--source", "ssh-remote",
		"--remote-os", runtime.GOOS,
		"--remote-host", localHostname(),
	)
	if p.MemPath != "" {
		sshArgs = append(sshArgs, "--path", p.MemPath)
	}

	launch := exec.Command("ssh", sshArgs...)
	launch.Stdin = os.Stdin
	launch.Stdout = os.Stdout
	launch.Stderr = os.Stderr
	if err := launch.Run(); err != nil {
		// Only emit to stderr on launch failure; happy path stays silent.
		return fmt.Errorf("ssh launcher to %s failed: %w", p.Host, err)
	}
	return nil
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
	_, err := runSSH(p, "true")
	return err == nil
}

var connectAddCmd = &cobra.Command{
	Use:    "add",
	Short:  "Add a remote from flags (key bootstrap + doctor + save + IDE config)",
	Hidden: true,
	RunE:   runConnectAdd,
}

func init() {
	connectMCPCmd.Flags().StringVar(&connectMCPProvider, "provider", "", "provider id used for attribution (default: claude-code)")

	connectAddCmd.Flags().StringVar(&addName, "name", "", "profile name (defaults to host)")
	connectAddCmd.Flags().StringVar(&addMethod, "method", "", "reachability: lan|vpn|bastion|public")
	connectAddCmd.Flags().StringVar(&addOS, "os", "", "target host OS: linux|darwin|windows (default linux)")
	connectAddCmd.Flags().StringVar(&addHost, "host", "", "[user@]host[:port] of the memory host")
	connectAddCmd.Flags().StringVar(&addJump, "jump", "", "jump host ([user@]host) for the bastion method")
	connectAddCmd.Flags().StringVar(&addMemPath, "mem-path", "", "host memory folder to serve (passed as --path; default: host's ~/.auxly/memory)")
	connectAddCmd.Flags().BoolVar(&addBatch, "batch", false, "non-interactive: never prompt for an SSH password (fail fast if key auth is missing)")

	connectUseCmd.Flags().StringVar(&useHost, "host", "", "[user@]host[:port] to create the profile if it doesn't exist yet")
	connectUseCmd.Flags().StringVar(&useJump, "jump", "", "jump/relay host ([user@]host) — for rendezvous reachability through a relay")
	connectUseCmd.Flags().StringVar(&useMethod, "method", "", "reachability label: lan|vpn|bastion|public|rendezvous")
	connectUseCmd.Flags().StringVar(&useMemPath, "mem-path", "", "host memory folder to serve (passed as --path)")
	connectUseCmd.Flags().StringVar(&useHostBin, "host-bin", "", "absolute path to auxly ON THE HOST (when not on the host's SSH PATH, e.g. macOS /usr/local/bin)")
	connectDisconnectCmd.Flags().BoolVar(&disconnectPurge, "purge", false, "also remove the installed /auxly-* skills from this machine")

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
	if err := checkTwoWay(p); err != nil {
		return err
	}
	_ = provisionRemote(p)
	printConnectSummary(p)
	return nil
}

// bootstrapKeyAuth ensures key-based SSH auth works, generating an ed25519 key and
// installing it on the host if needed. Non-interactive (assumes consent — the TUI
// form already confirmed intent); the SSH password during key install is read by
// ssh from /dev/tty, so it works under tea.ExecProcess.
func bootstrapKeyAuth(p remoteProfile) error {
	if _, err := runSSH(p, "true"); err == nil {
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
	if err := checkTwoWay(p); err != nil {
		return err
	}
	_ = provisionRemote(p)
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
			if liveHosts[strings.ToLower(c.Name)] {
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
	if err := checkTwoWay(p); err != nil {
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
	// minimal non-interactive SSH PATH).
	if _, err := runSSH(p, hostAuxlyBin(p), "--version"); err != nil {
		return fmt.Errorf("can't reach %s from here (`%s --version` failed): %w", p.Host, hostAuxlyBin(p), err)
	}
	fmt.Println("   ✓ Reached the host (this direction works)")
	injectRemoteConfigs(p.Name)
	installAuxlySkills(remoteBanner())
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

	// Reachability + key check — never prompt. If auth fails, guide and stop.
	if _, err := runSSH(p, "true"); err != nil {
		pub, perr := localPubKey()
		if perr != nil {
			return fmt.Errorf("can't reach the host and couldn't read this box's public key: %w", perr)
		}
		fmt.Println("⚠️  This machine isn't authorized on the host yet (one-time step).")
		fmt.Println("   Add this box's public key to the host's ~/.ssh/authorized_keys:")
		fmt.Printf("   %s\n", pub)
		fmt.Println("   Then run `auxly connect auto` again.")
		return fmt.Errorf("ssh key not yet authorized on the host")
	}

	if _, err := runSSH(p, hostAuxlyBin(p), "--version"); err != nil {
		return fmt.Errorf("reached the host but `%s --version` failed (is auxly installed there?): %w", hostAuxlyBin(p), err)
	}
	if err := upsertRemote(p); err != nil {
		return err
	}
	fmt.Printf("   ✓ Reached %s and saved the profile\n", offer.Name)

	injectRemoteConfigs(p.Name)
	installAuxlySkills(remoteBanner())
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

// connectTest runs the lightweight `auxly --version` reachability check.
func connectTest(p remoteProfile) error {
	fmt.Println("🔌 Testing remote auxly availability...")
	out, err := runSSH(p, "auxly", "--version")
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
		if app := writeMCPConfigEntry(t, serverDef); app != "" {
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
func provisionRemote(p remoteProfile) error {
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

	// Step 5b: two-way connectivity (host must reach this machine back).
	if err := checkTwoWay(p); err != nil {
		return err
	}

	// Step 6: provision the remote host (install skills + MCP on the host itself).
	_ = provisionRemote(p)

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
	if _, err := runSSH(p, "true"); err == nil {
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
		if err := copyCmd.Run(); err != nil {
			return fmt.Errorf("ssh-copy-id failed: %w", err)
		}
		return nil
	}

	// Manual fallback: append pubkey over one interactive (password) SSH.
	pub, err := os.ReadFile(pubPath)
	if err != nil {
		return fmt.Errorf("failed to read public key %s: %w", pubPath, err)
	}
	fmt.Println("   📤 Installing public key over SSH (you may be prompted for a password)...")
	remoteScript := "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo " +
		shellQuote(strings.TrimSpace(string(pub))) +
		" >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"

	args := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if p.Jump != "" {
		args = append(args, "-J", p.Jump)
	}
	if p.Port != 0 && p.Port != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(p.Port))
	}
	// "--" terminates ssh option processing before the target. ssh-copy-id (the
	// preferred path above) has no "--" support, so it relies on validateForExec.
	args = append(args, "--", target, "sh", "-c", shellQuote(remoteScript))
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
