package cmd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// relayOffer is the small descriptor the host drops on the relay so a remote box
// can connect flag-free (`auxly connect auto`). It carries no secrets — just the
// coordinates needed to build the consumer profile.
type relayOffer struct {
	Name        string `yaml:"name"`
	ReversePort int    `yaml:"reverse_port"`
	HostUser    string `yaml:"host_user"`
	HostBin     string `yaml:"host_bin"`
}

// offerName returns this host's offer/profile name (its hostname, sanitized).
func offerName() string {
	name := localHostname()
	if name == "" || name == "unknown" {
		name = "host"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// writeRelayOffer publishes this host's offer to the relay's ~/.auxly/offers/
// so the consumer box (often the relay itself) can auto-discover it.
func writeRelayOffer(hc hostConfig) error {
	hu := hc.HostUser
	if hu == "" {
		hu = currentLogin()
	}
	hostBin := "auxly"
	if exe, err := os.Executable(); err == nil && exe != "" {
		hostBin = exe
	}
	offer := relayOffer{Name: offerName(), ReversePort: hc.ReversePort, HostUser: hu, HostBin: hostBin}
	data, err := yaml.Marshal(offer)
	if err != nil {
		return fmt.Errorf("failed to marshal offer: %w", err)
	}

	user, host, parsedPort, perr := parseHostSpec(hc.Rendezvous)
	if perr != nil {
		return perr
	}
	port := parsedPort
	if hc.RendezvousPort != 0 {
		port = hc.RendezvousPort
	}

	target := host
	if user != "" {
		target = user + "@" + host
	}
	relay := remoteProfile{Name: "relay-" + host, Method: "public", User: user, Host: host, Port: port}

	fam, _, derr := detectRemoteOS(relay)
	if derr != nil {
		return fmt.Errorf("detect relay OS: %w", derr)
	}

	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new"}
	if port != 0 && port != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(port))
	}

	switch fam {
	case osWindows:
		encoded := base64.StdEncoding.EncodeToString(data)
		ps := strings.Join([]string{
			"$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + psQuote(encoded) + "))",
			"$dir=\"$env:USERPROFILE\\.auxly\\offers\"",
			"New-Item -ItemType Directory -Force $dir | Out-Null",
			"$path=Join-Path $dir " + psQuote(offer.Name+".yaml"),
			"[IO.File]::WriteAllText($path, $d, (New-Object Text.UTF8Encoding $false))",
		}, "; ")

		argv, aerr := remoteShellArgv(osWindows, "", ps)
		if aerr != nil {
			return aerr
		}
		args = append(args, "--", target)
		args = append(args, argv...)
		c := exec.Command("ssh", args...)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to publish offer to relay: %w: %s", err, strings.TrimSpace(string(out)))
		}

	case osUnix, osUnknown:
		script := "mkdir -p ~/.auxly/offers && cat > ~/.auxly/offers/" + offer.Name + ".yaml"
		argv, aerr := remoteShellArgv(osUnix, script, "")
		if aerr != nil {
			return aerr
		}
		args = append(args, "--", target)
		args = append(args, argv...)
		c := exec.Command("ssh", args...)
		c.Stdin = bytes.NewReader(data)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to publish offer to relay: %w: %s", err, strings.TrimSpace(string(out)))
		}

	default:
		return fmt.Errorf("unsupported remote OS family: %d", fam)
	}

	return nil
}

// ---------------------------------------------------------------------------
// `auxly host` — make THIS machine's memory reachable to a NAT'd remote box
// through a public rendezvous, using only outbound SSH (no inbound port, no
// VPN on the remote). The Mac (host) dials OUT to a public relay and opens a
// reverse tunnel (ssh -R) so the relay can forward back to this machine's sshd.
// A small keep-alive service (launchd / systemd-user) reconnects the tunnel
// whenever the host is awake. The shared box then reaches this memory by
// jumping through the relay — see `auxly host remote` for its one-liner.
// ---------------------------------------------------------------------------

const (
	defaultReversePort = 2222
	launchdLabel       = "io.auxly.host"
	systemdUnitName    = "auxly-host.service"
	windowsTaskName    = "Auxly-Host"
)

// hostConfig is persisted at ~/.auxly/host.yaml. It is NOT secret (no keys,
// no credentials) — just the rendezvous coordinates and the ports involved.
type hostConfig struct {
	// Rendezvous is the public relay as [user@]host (port stored separately).
	Rendezvous     string `yaml:"rendezvous"`
	RendezvousPort int    `yaml:"rendezvous_port,omitempty"`
	// ReversePort is the port opened ON the relay that forwards to this host.
	ReversePort int `yaml:"reverse_port"`
	// LocalSSHPort is this machine's sshd port (default 22).
	LocalSSHPort int `yaml:"local_ssh_port,omitempty"`
	// HostUser is the local login a remote agent authenticates as over the
	// tunnel (defaults to the current user).
	HostUser string `yaml:"host_user,omitempty"`
}

func hostConfigPath() (string, error) {
	dir, err := auxlyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "host.yaml"), nil
}

// hostFile is the on-disk shape of host.yaml: a LIST of relays this machine
// serves its memory to. Each relay is an independent reverse tunnel, so many
// boxes stay connected at once. The legacy single-relay form (top-level
// rendezvous/… with no `relays:` key) is still read and migrated on next save.
type hostFile struct {
	Relays []hostConfig `yaml:"relays"`
}

// loadHostConfigs returns every configured relay. The bool reports whether the
// file exists. A legacy single-relay file is returned as a one-element slice so
// older configs keep working and get migrated to the list form on next save.
func loadHostConfigs() ([]hostConfig, bool, error) {
	path, err := hostConfigPath()
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to read host config %s: %w", path, err)
	}
	// Prefer the new list form.
	var hf hostFile
	if err := yaml.Unmarshal(data, &hf); err == nil && len(hf.Relays) > 0 {
		return hf.Relays, true, nil
	}
	// Fall back to the legacy single-relay form.
	var hc hostConfig
	if err := yaml.Unmarshal(data, &hc); err != nil {
		return nil, true, fmt.Errorf("failed to parse host config %s: %w", path, err)
	}
	if strings.TrimSpace(hc.Rendezvous) == "" {
		return nil, true, nil
	}
	return []hostConfig{hc}, true, nil
}

// saveHostConfigs writes the relay list (always the new `relays:` form).
func saveHostConfigs(relays []hostConfig) error {
	dir, err := auxlyDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	data, err := yaml.Marshal(hostFile{Relays: relays})
	if err != nil {
		return fmt.Errorf("failed to marshal host config: %w", err)
	}
	path := filepath.Join(dir, "host.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write host config %s: %w", path, err)
	}
	return nil
}

// upsertHostConfig adds a relay, or replaces an existing one with the same
// rendezvous. This is the fix for the singleton bug: connecting a new box no
// longer tears down its siblings' tunnels — it just appends another.
func upsertHostConfig(hc hostConfig) error {
	relays, _, err := loadHostConfigs()
	if err != nil {
		return err
	}
	out := make([]hostConfig, 0, len(relays)+1)
	replaced := false
	for _, r := range relays {
		if strings.EqualFold(strings.TrimSpace(r.Rendezvous), strings.TrimSpace(hc.Rendezvous)) {
			out = append(out, hc)
			replaced = true
		} else {
			out = append(out, r)
		}
	}
	if !replaced {
		out = append(out, hc)
	}
	return saveHostConfigs(out)
}

// removeHostConfig drops the relay with the given rendezvous and returns how
// many relays remain configured.
func removeHostConfig(rendezvous string) (int, error) {
	relays, _, err := loadHostConfigs()
	if err != nil {
		return 0, err
	}
	out := make([]hostConfig, 0, len(relays))
	for _, r := range relays {
		if !strings.EqualFold(strings.TrimSpace(r.Rendezvous), strings.TrimSpace(rendezvous)) {
			out = append(out, r)
		}
	}
	if err := saveHostConfigs(out); err != nil {
		return 0, err
	}
	return len(out), nil
}

// loadHostConfig returns the FIRST configured relay — a convenience for the
// status/remote/offer printers that describe "the relay". Multi-relay callers
// use loadHostConfigs.
func loadHostConfig() (hostConfig, bool, error) {
	relays, ok, err := loadHostConfigs()
	if err != nil {
		return hostConfig{}, false, err
	}
	if !ok || len(relays) == 0 {
		return hostConfig{}, false, nil
	}
	return relays[0], true, nil
}

func (hc hostConfig) localPort() int {
	if hc.LocalSSHPort != 0 {
		return hc.LocalSSHPort
	}
	return defaultSSHPort
}

// validateRendezvous guards the rendezvous spec against empty/flag-like values
// before it is handed to ssh (mirrors validateForExec for remote profiles).
func validateRendezvous(hc hostConfig) error {
	r := strings.TrimSpace(hc.Rendezvous)
	if r == "" {
		return fmt.Errorf("rendezvous host is empty — run `auxly host setup` first")
	}
	if strings.HasPrefix(r, "-") {
		return fmt.Errorf("rendezvous host %q looks like a flag", r)
	}
	if hc.ReversePort <= 0 || hc.ReversePort > 65535 {
		return fmt.Errorf("reverse_port %d is out of range", hc.ReversePort)
	}
	return nil
}

// provisionConsumer drives the FULL remote setup from the Mac: it installs/updates
// auxly on the relay box, authorizes that box's SSH key on THIS machine (so the
// runtime tunnel auth works), and wires its agent to use this Mac's memory. This
// is the "connect from the Mac and everything is ready" path — setup is push
// (Mac→box, reachable for SSH), runtime is pull (box→Mac via the tunnel).
func provisionConsumer(hc hostConfig) error {
	user, host, port, err := parseHostSpec(hc.Rendezvous)
	if err != nil {
		return err
	}
	relay := remoteProfile{Name: "relay", Method: "public", User: user, Host: host, Port: port}

	hostUser := hc.HostUser
	if hostUser == "" {
		hostUser = currentLogin()
	}
	macBin := "auxly"
	if exe, e := os.Executable(); e == nil && exe != "" {
		macBin = exe
	}
	name := offerName()

	// 1. Install / update auxly on the box (OS-aware: POSIX curl|sh or PowerShell irm|iex).
	fmt.Println("📦 Installing/updating auxly on the remote box...")
	relayOS, _, derr := detectRemoteOS(relay)
	if derr != nil {
		fmt.Printf("   ⚠ remote OS detection failed: %v\n", derr)
		return derr
	}
	installPosix := "curl -fsSL " + update.BaseURL() + "/cli | sh"
	installPS := winInstallCmd(update.BaseURL() + "/cli.ps1")
	if out, ierr := runRemoteScript(relay, relayOS, installPosix, installPS); ierr != nil {
		fmt.Printf("   ⚠ remote install failed: %v\n   %s\n", ierr, firstLine(out))
		return ierr
	}
	fmt.Println("   ✓ auxly installed on the box")

	// 2. Authorize the box's key on THIS Mac so the runtime tunnel auth works.
	if kerr := authorizeRemoteKeyLocally(relay); kerr != nil {
		fmt.Printf("   ⚠ could not authorize the box's key on this Mac: %v\n", kerr)
	} else {
		fmt.Println("   ✓ Authorized the box's SSH key on this Mac")
	}

	// 3. Wire the box's agent to this Mac's memory (explicit params — no offer
	//    dependency; the relay endpoint is localhost:<port> on the box itself).
	target := fmt.Sprintf("%s@localhost:%d", hostUser, hc.ReversePort)
	fmt.Println("🔗 Wiring the box's agent to this Mac's memory...")
	wireArgs := []string{"auxly", "connect", "use", name, "--method", "rendezvous", "--host", target, "--host-bin", macBin}
	if out, werr := runSSH(relay, wireArgs...); werr != nil {
		fmt.Printf("   ⚠ remote wiring failed: %v\n   %s\n", werr, firstLine(out))
		return werr
	}
	fmt.Println("   ✓ MCP launcher + skills wired on the box")

	// Capture the box's own hostname so its live ssh-remote session (which reports
	// that hostname as RemoteHost) maps back to this box row instead of surfacing
	// as a phantom duplicate. Best-effort — an empty result just falls back to
	// name/target matching.
	boxHostname := ""
	if out, herr := runSSH(relay, "hostname"); herr == nil {
		boxHostname = strings.TrimSpace(firstLine(out))
	}

	// Record the connection so the user can manage it (disconnect / reconnect /
	// rename / remove) from `auxly host clients` or the TUI.
	clientName := strings.TrimSpace(hostClientName)
	if clientName == "" {
		clientName = host
	}
	if err := upsertClient(clientEntry{Name: clientName, Target: hc.Rendezvous, Method: "relay", Hostname: boxHostname}); err == nil {
		fmt.Printf("   ✓ Saved connection \"%s\" (manage with `auxly host clients`)\n", clientName)
	}
	fmt.Println("👉 RESTART the agent on the box to load its memory link.")
	return nil
}

// authorizeRemoteKeyLocally fetches the remote box's ed25519 public key (creating
// one if absent) and appends it to THIS machine's ~/.ssh/authorized_keys so the
// box can reach back over the tunnel without a password.
func authorizeRemoteKeyLocally(p remoteProfile) error {
	posix := "test -f ~/.ssh/id_ed25519 || (mkdir -p ~/.ssh && chmod 700 ~/.ssh && ssh-keygen -t ed25519 -N '' -f ~/.ssh/id_ed25519 >/dev/null 2>&1); cat ~/.ssh/id_ed25519.pub"
	powershell := strings.Join([]string{
		"$dir=Join-Path $env:USERPROFILE '.ssh'",
		"$key=Join-Path $dir 'id_ed25519'",
		"$pub=$key+'.pub'",
		"if(-not(Test-Path -LiteralPath $key)){",
		"New-Item -ItemType Directory -Force -Path $dir | Out-Null",
		"& ssh-keygen -t ed25519 -N '\"\"' -f $key *> $null",
		"if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}",
		"}",
		"Get-Content -LiteralPath $pub -Raw",
	}, "; ")

	fam, _, derr := detectRemoteOS(p)
	if derr != nil {
		return fmt.Errorf("detect remote OS: %w", derr)
	}

	pub, err := runRemoteScript(p, fam, posix, powershell)
	if err != nil {
		return err
	}
	pub = strings.TrimSpace(firstLine(pub))
	if !strings.HasPrefix(pub, "ssh-") {
		return fmt.Errorf("unexpected public key output from the remote box")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	akPath := filepath.Join(home, ".ssh", "authorized_keys")
	if existing, rerr := os.ReadFile(akPath); rerr == nil && strings.Contains(string(existing), pub) {
		return nil // already authorized
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(akPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(pub + "\n")
	return err
}

// tunnelArgs builds the `ssh -R` argv for the keep-alive tunnel.
func tunnelArgs(hc hostConfig) []string {
	args := []string{
		"-N", "-T",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new",
		"-R", fmt.Sprintf("%d:localhost:%d", hc.ReversePort, hc.localPort()),
	}
	if hc.RendezvousPort != 0 && hc.RendezvousPort != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(hc.RendezvousPort))
	}
	args = append(args, "--", hc.Rendezvous)
	return args
}

// ---------------------------------------------------------------------------
// Command tree
// ---------------------------------------------------------------------------

var hostCmd = &cobra.Command{
	Use:          "host",
	SilenceUsage: true,
	Short:        "Serve this machine's memory to a NAT'd remote via a public relay",
	Long: `host makes THIS machine's Auxly memory reachable from a remote/shared box
that can't dial in directly (NAT), using only outbound SSH.

This machine opens a reverse tunnel to a public relay you control; a keep-alive
service reconnects it whenever this machine is awake. The remote box then reaches
your memory by jumping through the relay — no VPN and no inbound port here.

Run ` + "`auxly host setup`" + ` once, then ` + "`auxly host remote`" + ` to get the
command to paste on the shared box.`,
	RunE: runHostStatus,
}

var (
	hostRendezvous   string
	hostReversePort  int
	hostLocalSSHPort int
	hostUserFlag     string
	hostAssumeYes    bool
	hostSetupBatch   bool
	hostProvision    bool
	hostClientName   string
)

var hostSetupCmd = &cobra.Command{
	Use:          "setup",
	Short:        "Configure the relay, install a key, and start the keep-alive tunnel",
	SilenceUsage: true,
	RunE:         runHostSetup,
}

var hostTunnelCmd = &cobra.Command{
	Use:          "tunnel",
	Short:        "Run the reverse tunnel in the foreground (used by the keep-alive)",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runHostTunnel,
}

var hostUpCmd = &cobra.Command{
	Use:          "up",
	Short:        "Install/start the keep-alive tunnel service",
	SilenceUsage: true,
	RunE:         runHostUp,
}

var hostDownCmd = &cobra.Command{
	Use:          "down",
	Aliases:      []string{"stop"},
	Short:        "Stop and remove the keep-alive tunnel service",
	SilenceUsage: true,
	RunE:         runHostDown,
}

var hostStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show the host tunnel configuration and live state",
	SilenceUsage: true,
	RunE:         runHostStatus,
}

var hostRemoteCmd = &cobra.Command{
	Use:          "remote",
	Short:        "Print the command to run on the shared/remote box to connect here",
	SilenceUsage: true,
	RunE:         runHostRemote,
}

var hostOfferCmd = &cobra.Command{
	Use:          "offer",
	Short:        "(Re)publish this host's connect offer to the relay",
	SilenceUsage: true,
	RunE:         runHostOffer,
}

var hostProvisionCmd = &cobra.Command{
	Use:          "provision",
	Short:        "Install auxly on the relay box and wire its agent to this Mac's memory",
	SilenceUsage: true,
	RunE:         runHostProvision,
}

func init() {
	hostSetupCmd.Flags().StringVar(&hostRendezvous, "rendezvous", "", "public relay as [user@]host[:port] (you control it)")
	hostSetupCmd.Flags().IntVar(&hostReversePort, "reverse-port", defaultReversePort, "port to open on the relay that forwards back to this machine")
	hostSetupCmd.Flags().IntVar(&hostLocalSSHPort, "local-ssh-port", defaultSSHPort, "this machine's sshd port")
	hostSetupCmd.Flags().StringVar(&hostUserFlag, "host-user", "", "local login the remote agent authenticates as (default: current user)")
	hostSetupCmd.Flags().BoolVarP(&hostAssumeYes, "yes", "y", false, "don't prompt before installing the keep-alive service")
	hostSetupCmd.Flags().BoolVar(&hostSetupBatch, "batch", false, "non-interactive (TUI): never prompt; if the relay key isn't set up, emit AUXLY_KEY_REQUIRED and stop")
	hostSetupCmd.Flags().BoolVar(&hostProvision, "provision", false, "also install auxly on the relay box and wire its agent to this Mac's memory")
	hostSetupCmd.Flags().StringVar(&hostClientName, "name", "", "friendly name for the provisioned box (defaults to its host)")
	hostProvisionCmd.Flags().StringVar(&hostClientName, "name", "", "friendly name for this connection (defaults to the box's host)")

	hostCmd.AddCommand(hostSetupCmd)
	hostCmd.AddCommand(hostTunnelCmd)
	hostCmd.AddCommand(hostUpCmd)
	hostCmd.AddCommand(hostDownCmd)
	hostCmd.AddCommand(hostStatusCmd)
	hostCmd.AddCommand(hostRemoteCmd)
	hostCmd.AddCommand(hostOfferCmd)
	hostCmd.AddCommand(hostProvisionCmd)
	hostCmd.AddCommand(hostClientsCmd)
	hostCmd.AddCommand(hostDisconnectCmd)
	hostCmd.AddCommand(hostReconnectCmd)
	hostCmd.AddCommand(hostForgetCmd)

	hostVersionsCmd.Flags().BoolVar(&hostVersionsJSON, "json", false, "emit machine-readable JSON (used by the TUI)")
	hostUpdateCmd.Flags().BoolVar(&hostUpdateAll, "all", false, "update every connected box that is outdated and idle")
	hostUpdateCmd.Flags().BoolVar(&hostUpdateForce, "force", false, "update even a box that is serving a live session (single-box only)")
	hostStatuslineCmd.Flags().BoolVar(&hostStatuslineAll, "all", false, "push the statusline to every connected box")
	hostCmd.AddCommand(hostVersionsCmd)
	hostCmd.AddCommand(hostUpdateCmd)
	hostCmd.AddCommand(hostStatuslineCmd)

	rootCmd.AddCommand(hostCmd)
}

// currentLogin returns the current user's login name (best effort).
func currentLogin() string {
	for _, env := range []string{"USER", "LOGNAME", "USERNAME"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// setup
// ---------------------------------------------------------------------------

func runHostSetup(cmd *cobra.Command, args []string) error {
	reader := bufio.NewScanner(os.Stdin)

	spec := strings.TrimSpace(hostRendezvous)
	if spec == "" {
		fmt.Println("🛰️  Auxly host setup")
		fmt.Println("   A public relay (a server with a public IP you control) lets a NAT'd")
		fmt.Println("   remote box reach this machine's memory. You already use one for testing.")
		spec = promptLine(reader, "   Relay [user@]host[:port]: ")
	}
	user, host, port, err := parseHostSpec(spec)
	if err != nil {
		return fmt.Errorf("invalid relay specification: %w", err)
	}
	relayTarget := host
	if user != "" {
		relayTarget = user + "@" + host
	}

	hostUser := strings.TrimSpace(hostUserFlag)
	if hostUser == "" {
		hostUser = currentLogin()
	}

	hc := hostConfig{
		Rendezvous:     relayTarget,
		RendezvousPort: port,
		ReversePort:    hostReversePort,
		LocalSSHPort:   hostLocalSSHPort,
		HostUser:       hostUser,
	}
	if hc.ReversePort == 0 {
		hc.ReversePort = defaultReversePort
	}
	if err := validateRendezvous(hc); err != nil {
		return err
	}

	// Warn early if this machine isn't actually accepting SSH — the tunnel is
	// useless without a local sshd to forward to.
	checkLocalSSHD(hc.localPort())

	// Persist before doing anything else so `tunnel`/`status` can read it. Upsert
	// (not overwrite) so connecting this box keeps every previously-connected box's
	// tunnel alive — the keep-alive supervises one tunnel per relay.
	if err := upsertHostConfig(hc); err != nil {
		return err
	}
	fmt.Printf("💾 Saved relay config (%s, reverse port %d → local :%d)\n", relayTarget, hc.ReversePort, hc.localPort())

	// One-time, unavoidable: install our key on the relay so the tunnel needs
	// no password. Reuses the remote-profile key bootstrap (idempotent — a no-op
	// if key auth already works, which it does for your existing relay).
	relayProfile := remoteProfile{Name: "relay", Method: "public", User: user, Host: host, Port: port}
	if hostSetupBatch {
		// Captured (non-PTY) run from the TUI: a password prompt would hang. If key
		// auth isn't already set up, stop with a token the TUI keys off to guide a
		// one-time terminal run, rather than blocking.
		if !sshKeyAuthOK(relayProfile) {
			fmt.Printf("⚠️  Passwordless SSH to the relay %s isn't set up yet.\n", relayTarget)
			fmt.Println("   Run `auxly host setup` once in a terminal to install the key (it'll ask for the relay password).")
			fmt.Println("AUXLY_KEY_REQUIRED")
			return nil
		}
		fmt.Println("   ✓ Passwordless SSH to the relay confirmed")
	} else if err := bootstrapKeyAuth(relayProfile); err != nil {
		fmt.Printf("⚠️  Could not confirm key auth to the relay: %v\n", err)
		fmt.Println("   The tunnel needs passwordless SSH to the relay. Fix this, then run `auxly host up`.")
	} else {
		fmt.Println("   ✓ Passwordless SSH to the relay confirmed")
	}

	// Confirmation before installing a background service (user asked for this).
	if !hostAssumeYes && !hostSetupBatch {
		fmt.Printf("\nInstall a keep-alive service so the tunnel auto-reconnects while this machine is on? [Y/n]: ")
		ans := ""
		if reader.Scan() {
			ans = strings.ToLower(strings.TrimSpace(reader.Text()))
		}
		if ans == "n" || ans == "no" {
			fmt.Println("Skipped. Start it later with `auxly host up`.")
			printConsumerCommand(hc)
			return nil
		}
	}

	if err := installKeepAlive(); err != nil {
		return err
	}
	recordHostProvision(hc, "host tunnel keep-alive installed")
	fmt.Println("   ✓ Keep-alive tunnel service installed and started")

	// Give the tunnel a moment, then report whether the relay sees the port.
	time.Sleep(2 * time.Second)
	reportTunnelLive(hc)

	// Publish the offer so a remote box can connect flag-free via `auxly connect
	// auto` (or just `/auxly-remote-connect` inside its agent).
	if err := writeRelayOffer(hc); err != nil {
		fmt.Printf("   ⚠ Couldn't publish the connect offer to the relay: %v\n", err)
	} else {
		fmt.Println("   ✓ Published connect offer to the relay (remote box can use `auxly connect auto`)")
	}

	// Full Mac-driven provisioning of the relay box: install auxly there, authorize
	// its key here, and wire its agent — so nothing has to be run on the box.
	doProvision := hostProvision
	if !doProvision && !hostSetupBatch {
		fmt.Printf("\nAlso set up the relay box (%s) to USE this Mac's memory now — install auxly there and wire its agent? [Y/n]: ", hc.Rendezvous)
		ans := ""
		if reader.Scan() {
			ans = strings.ToLower(strings.TrimSpace(reader.Text()))
		}
		doProvision = ans != "n" && ans != "no"
	}
	if doProvision {
		fmt.Println()
		if err := provisionConsumer(hc); err != nil {
			fmt.Printf("   ⚠ Remote provisioning incomplete: %v\n", err)
		}
	} else {
		printConsumerCommand(hc)
	}
	return nil
}

func runHostProvision(cmd *cobra.Command, args []string) error {
	relays, ok, err := loadHostConfigs()
	if err != nil {
		return err
	}
	if !ok || len(relays) == 0 {
		return fmt.Errorf("no host config — run `auxly host setup` first")
	}
	var firstErr error
	for _, hc := range relays {
		if err := provisionConsumer(hc); err != nil {
			fmt.Printf("   ⚠ provisioning %s incomplete: %v\n", hc.Rendezvous, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// checkLocalSSHD warns if nothing is listening on this machine's sshd port.
func checkLocalSSHD(port int) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		fmt.Printf("⚠️  No SSH server detected on localhost:%d — the tunnel needs one.\n", port)
		switch runtime.GOOS {
		case "darwin":
			fmt.Println("   Enable it: System Settings → General → Sharing → Remote Login")
		case "linux":
			fmt.Println("   Enable it: sudo systemctl enable --now ssh")
		case "windows":
			fmt.Println("   Enable the OpenSSH Server optional feature, then start the 'sshd' service.")
		}
		return
	}
	_ = conn.Close()
}

// ---------------------------------------------------------------------------
// tunnel (foreground; invoked by the keep-alive service)
// ---------------------------------------------------------------------------

func runHostTunnel(cmd *cobra.Command, args []string) error {
	relays, ok, err := loadHostConfigs()
	if err != nil {
		return err
	}
	if !ok || len(relays) == 0 {
		return fmt.Errorf("no host config — run `auxly host setup` first")
	}
	// Supervise one reverse tunnel per relay, each with its own restart loop, so a
	// flap (or a box going offline) on one tunnel never drops the others. This
	// process is owned by the per-OS keep-alive (launchd/systemd/Task Scheduler)
	// and blocks forever once at least one tunnel is supervised.
	started := 0
	for _, hc := range relays {
		if verr := validateRendezvous(hc); verr != nil {
			fmt.Fprintf(os.Stderr, "skipping invalid relay %q: %v\n", hc.Rendezvous, verr)
			continue
		}
		go superviseTunnel(hc)
		started++
	}
	if started == 0 {
		return fmt.Errorf("no valid relay to tunnel — run `auxly host setup` first")
	}
	select {} // block forever; the keep-alive service owns our lifecycle
}

// superviseTunnel runs one relay's reverse tunnel, restarting it with a short
// backoff whenever it exits, forever. Each relay gets its own goroutine so the
// tunnels are fully independent.
func superviseTunnel(hc hostConfig) {
	for {
		c := exec.Command("ssh", tunnelArgs(hc)...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		_ = c.Run() // returns when the tunnel drops; loop reconnects
		time.Sleep(5 * time.Second)
	}
}

// ---------------------------------------------------------------------------
// up / down
// ---------------------------------------------------------------------------

func runHostUp(cmd *cobra.Command, args []string) error {
	if _, ok, err := loadHostConfig(); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no host config — run `auxly host setup` first")
	}
	if err := installKeepAlive(); err != nil {
		return err
	}
	fmt.Println("✓ Keep-alive tunnel service installed and started")
	return nil
}

func runHostDown(cmd *cobra.Command, args []string) error {
	if err := uninstallKeepAlive(); err != nil {
		return err
	}
	fmt.Println("✓ Keep-alive tunnel service stopped and removed")
	return nil
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func runHostStatus(cmd *cobra.Command, args []string) error {
	relays, ok, err := loadHostConfigs()
	if err != nil {
		return err
	}
	if !ok || len(relays) == 0 {
		fmt.Println("No host tunnel configured. Run `auxly host setup` to make this")
		fmt.Println("machine's memory reachable from a remote box through a relay.")
		return nil
	}
	fmt.Printf("🛰️  Auxly memory host — serving %d box(es)\n", len(relays))

	loaded, detail := keepAliveStatus()
	if loaded {
		fmt.Printf("   Keep-alive   : ✓ %s\n", detail)
	} else {
		fmt.Printf("   Keep-alive   : ✗ %s\n", detail)
	}

	for i, hc := range relays {
		fmt.Printf("\n   [%d] Relay      : %s", i+1, hc.Rendezvous)
		if hc.RendezvousPort != 0 && hc.RendezvousPort != defaultSSHPort {
			fmt.Printf(":%d", hc.RendezvousPort)
		}
		fmt.Println()
		fmt.Printf("       Reverse    : %d (on the relay → this machine :%d)\n", hc.ReversePort, hc.localPort())
		if hc.HostUser != "" {
			fmt.Printf("       Host login : %s\n", hc.HostUser)
		}
		reportTunnelLive(hc)
	}
	return nil
}

// reportTunnelLive best-effort checks whether the relay has the reverse port
// bound (i.e. the tunnel is actually up). Bounded so it never hangs.
func reportTunnelLive(hc hostConfig) {
	if err := validateRendezvous(hc); err != nil {
		return
	}
	rUser, rHost, rPort, rErr := parseHostSpec(hc.Rendezvous)
	if rErr != nil {
		fmt.Printf("   Tunnel       : ? (couldn't reach the relay to check)\n")
		return
	}
	if hc.RendezvousPort != 0 {
		rPort = hc.RendezvousPort
	}
	rTarget := rHost
	if rUser != "" {
		rTarget = rUser + "@" + rHost
	}
	relayProf := remoteProfile{Name: "relay-" + rHost, Method: "public", User: rUser, Host: rHost, Port: rPort}
	fam, _, osErr := detectRemoteOS(relayProf)
	if osErr != nil {
		fmt.Printf("   Tunnel       : ? (couldn't reach the relay to check)\n")
		return
	}

	// Check for a listener on the reverse port: ss/netstat on POSIX, Get-NetTCPConnection on Windows.
	probe := fmt.Sprintf(
		"(command -v ss >/dev/null 2>&1 && ss -ltn || netstat -ltn 2>/dev/null) | grep -q ':%d ' && echo UP || echo DOWN",
		hc.ReversePort,
	)
	probePS := fmt.Sprintf(
		"if(Get-NetTCPConnection -State Listen -LocalPort %d -ErrorAction SilentlyContinue){'UP'}else{'DOWN'}",
		hc.ReversePort,
	)
	argv, aerr := remoteShellArgv(fam, probe, probePS)
	if aerr != nil {
		fmt.Printf("   Tunnel       : ? (couldn't reach the relay to check)\n")
		return
	}

	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=8",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if rPort != 0 && rPort != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(rPort))
	}
	args = append(args, "--", rTarget)
	args = append(args, argv...)

	out, err := exec.Command("ssh", args...).CombinedOutput()
	state := strings.TrimSpace(string(out))
	switch {
	case err != nil:
		fmt.Printf("   Tunnel       : ? (couldn't reach the relay to check)\n")
	case strings.Contains(state, "UP"):
		fmt.Printf("   Tunnel       : ✓ live on the relay (port %d bound)\n", hc.ReversePort)
	default:
		fmt.Printf("   Tunnel       : ✗ not bound on the relay (is this machine awake / keep-alive running?)\n")
	}
}

// ---------------------------------------------------------------------------
// remote — the command to paste on the shared box
// ---------------------------------------------------------------------------

func runHostRemote(cmd *cobra.Command, args []string) error {
	relays, ok, err := loadHostConfigs()
	if err != nil {
		return err
	}
	if !ok || len(relays) == 0 {
		return fmt.Errorf("no host config — run `auxly host setup` first")
	}
	for _, hc := range relays {
		printConsumerCommand(hc)
	}
	return nil
}

func runHostOffer(cmd *cobra.Command, args []string) error {
	relays, ok, err := loadHostConfigs()
	if err != nil {
		return err
	}
	if !ok || len(relays) == 0 {
		return fmt.Errorf("no host config — run `auxly host setup` first")
	}
	for _, hc := range relays {
		if err := writeRelayOffer(hc); err != nil {
			fmt.Printf("⚠ Couldn't publish the offer to %s: %v\n", hc.Rendezvous, err)
			continue
		}
		fmt.Printf("✓ Published connect offer %q to the relay (%s).\n", offerName(), hc.Rendezvous)
	}
	fmt.Println("  On each remote box: `auxly connect auto` (or /auxly-remote-connect in its agent).")
	return nil
}

// printConsumerCommand prints the exact `auxly connect use` invocation the
// shared/remote box runs to reach this machine through the relay.
func printConsumerCommand(hc hostConfig) {
	name := localHostname()
	if name == "unknown" || name == "" {
		name = "mac"
	}
	relay := hc.Rendezvous
	if hc.RendezvousPort != 0 && hc.RendezvousPort != defaultSSHPort {
		relay = fmt.Sprintf("%s:%d", relay, hc.RendezvousPort)
	}
	hostUser := hc.HostUser
	if hostUser == "" {
		hostUser = currentLogin()
	}
	target := fmt.Sprintf("localhost:%d", hc.ReversePort)
	if hostUser != "" {
		target = hostUser + "@" + target
	}
	// The remote launcher runs `auxly mcp-server` on THIS machine over SSH, where
	// the non-interactive PATH may omit our install dir (e.g. macOS /usr/local/bin).
	// Pass our absolute path so the launcher always resolves it.
	hostBin := "auxly"
	if exe, err := os.Executable(); err == nil && exe != "" {
		hostBin = exe
	}

	fmt.Println()
	fmt.Println("👉 On the shared/remote box (the relay), connect with ONE command — no flags:")
	fmt.Println()
	fmt.Println("   auxly connect auto")
	fmt.Println()
	fmt.Println("   …or just type /auxly-remote-connect inside that box's agent — it detects")
	fmt.Println("   this offer and wires everything up, then asks you to restart the agent.")
	fmt.Println()
	fmt.Printf("   When you're done, leave no trace:  auxly connect disconnect %s\n", name)
	fmt.Println()
	fmt.Println("   (Explicit form, if you ever need it:)")
	fmt.Printf("   auxly connect use %s --method rendezvous --jump %s --host %s --host-bin %s\n", name, relay, target, hostBin)
	fmt.Println("   One-time: that box's SSH key must be authorized on this machine.")
}

func recordHostProvision(hc hostConfig, action string) {
	logger, err := audit.NewLogger(getMemoryPath())
	if err != nil {
		return
	}
	_, _ = logger.LogWithSource(
		connectAuditAgent,
		"system",
		"host-tunnel",
		hc.Rendezvous,
		"",
		action,
		"auto",
		audit.SourceMeta{Source: "local"},
	)
}

// ---------------------------------------------------------------------------
// Keep-alive service (per-OS)
// ---------------------------------------------------------------------------

func installKeepAlive() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to resolve auxly binary path: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchAgent(exe)
	case "linux":
		return installSystemdUser(exe)
	case "windows":
		return installWindowsTask(exe)
	default:
		return fmt.Errorf("keep-alive not supported on %s; run `auxly host tunnel` manually", runtime.GOOS)
	}
}

func uninstallKeepAlive() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchAgent()
	case "linux":
		return uninstallSystemdUser()
	case "windows":
		return uninstallWindowsTask()
	default:
		return nil
	}
}

// keepAliveStatus reports whether the service is loaded and a human detail.
func keepAliveStatus() (bool, string) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
		if err != nil {
			return false, "not loaded (start with `auxly host up`)"
		}
		_ = out
		return true, "loaded (launchd)"
	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-active", systemdUnitName).CombinedOutput()
		state := strings.TrimSpace(string(out))
		if err != nil || state != "active" {
			if state == "" {
				state = "inactive"
			}
			return false, state + " (start with `auxly host up`)"
		}
		return true, "active (systemd --user)"
	case "windows":
		out, err := exec.Command("schtasks", "/Query", "/TN", windowsTaskName).CombinedOutput()
		if err != nil {
			return false, "not registered (start with `auxly host up`)"
		}
		if strings.Contains(string(out), "Running") {
			return true, "running (Task Scheduler)"
		}
		return true, "registered (Task Scheduler)"
	default:
		return false, "unmanaged on this OS"
	}
}

// --- macOS: launchd LaunchAgent ---

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func installLaunchAgent(exe string) error {
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents dir: %w", err)
	}
	dir, _ := auxlyDir()
	logPath := filepath.Join(dir, "host-tunnel.log")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>host</string>
    <string>tunnel</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchdLabel, exe, logPath, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("failed to write LaunchAgent plist: %w", err)
	}
	// Reload: unload (ignore "not loaded") then load -w to (re)register + start.
	_ = exec.Command("launchctl", "unload", "-w", plistPath).Run()
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallLaunchAgent() error {
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", "-w", plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove LaunchAgent plist: %w", err)
	}
	return nil
}

// --- Linux: systemd --user unit ---

func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName), nil
}

func installSystemdUser(exe string) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("failed to create systemd user dir: %w", err)
	}
	unit := fmt.Sprintf(`[Unit]
Description=Auxly memory host reverse tunnel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s host tunnel
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, exe)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("failed to write systemd unit: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user enable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallSystemdUser() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnitName).Run()
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove systemd unit: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// --- Windows: Task Scheduler ---

// installWindowsTask registers a per-user logon task that keeps the reverse
// tunnel up, then starts it immediately. A current-user ONLOGON task does not
// require elevation (unlike /RL HIGHEST), so this runs without admin.
func installWindowsTask(exe string) error {
	// /TR must be a single argument: the quoted exe path followed by its args.
	action := fmt.Sprintf(`"%s" host tunnel`, exe)
	create := exec.Command("schtasks", "/Create",
		"/SC", "ONLOGON",
		"/TN", windowsTaskName,
		"/TR", action,
		"/F", // overwrite an existing task of the same name
	)
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks create failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Start it now so the tunnel is live without waiting for the next logon.
	if out, err := exec.Command("schtasks", "/Run", "/TN", windowsTaskName).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks run failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallWindowsTask() error {
	out, err := exec.Command("schtasks", "/Delete", "/TN", windowsTaskName, "/F").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// Deleting a task that doesn't exist is not an error for our purposes.
		if strings.Contains(strings.ToLower(msg), "cannot find") || strings.Contains(strings.ToLower(msg), "does not exist") {
			return nil
		}
		return fmt.Errorf("schtasks delete failed: %w: %s", err, msg)
	}
	return nil
}
