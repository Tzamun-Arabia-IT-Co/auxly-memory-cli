package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
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
// VPN on the remote). This machine (host) dials OUT to a public relay and opens a
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
	if err := os.WriteFile(path, data, 0600); err != nil {
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

// provisionConsumer drives the FULL remote setup from this machine: it installs/updates
// auxly on the relay box, authorizes that box's SSH key on THIS machine (so the
// runtime tunnel auth works), and wires its agent to use this machine's memory. This
// is the "connect from this machine and everything is ready" path — setup is push
// (machine→box, reachable for SSH), runtime is pull (box→machine via the tunnel).
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

	// 1. Install / update auxly on the box (OS-aware: POSIX curl|sh or PowerShell
	//    irm|iex), then confirm readiness by polling `auxly --version`. Readiness —
	//    not the install call's clean exit — is the source of truth, because a
	//    Windows irm|iex install over SSH completes on the box yet can leave the
	//    session lingering. The install AND the poll run on their OWN connection
	//    (withoutMux): if the install session lingers, isolating it means it can't
	//    wedge the shared ControlMaster socket that the later steps (key-authorize,
	//    hostname, wire) reuse — that socket-poisoning was what made the verify
	//    hang forever before. Bounded timeout reaps the lingering session; the
	//    install itself has already finished on the box by then.
	fmt.Println("📦 Installing/updating auxly on the remote box...")
	relayOS, _, derr := detectRemoteOS(relay)
	if derr != nil {
		fmt.Printf("   ⚠ remote OS detection failed: %v\n", derr)
		return derr
	}
	installP := withoutMux(relay)
	installPosix := "curl -fsSL " + update.BaseURL() + "/cli | sh"
	installPS := winInstallCmd(update.BaseURL() + "/cli.ps1")

	// Fire the install and poll for readiness CONCURRENTLY. The install runs on its
	// own connection; the poll runs on others. As soon as `auxly --version` answers,
	// we stop waiting on the install — so a lingering Windows session never makes the
	// connect SIT on "Installing…" the way it did before. cancelInstall reaps that
	// lingering session once the outcome is known.
	installCtx, cancelInstall := context.WithTimeout(context.Background(), installRunTimeout)
	defer cancelInstall()
	installDone := make(chan error, 1)
	go func() {
		_, ierr := runRemoteScriptCtx(installCtx, installP, relayOS, installPosix, installPS)
		installDone <- ierr
	}()

	ver, verifyErr := pollVerifyAuxly(installP, installPollTimeout, installPollInterval)
	cancelInstall()
	if verifyErr == nil && ver != "" {
		fmt.Printf("   ✓ auxly ready on the box (%s)\n", ver)
	} else {
		// Poll never saw a ready auxly — the install genuinely failed. Enrich the
		// error with the install call's own result (it has finished by now, since the
		// poll outlasts the install timeout).
		installErr := <-installDone
		if installErr != nil && !errors.Is(installErr, context.DeadlineExceeded) && !errors.Is(installErr, context.Canceled) {
			fmt.Printf("   ⚠ remote install failed: %v\n", installErr)
			return fmt.Errorf("remote install did not verify: install: %w; verify %s --version: %v", installErr, hostAuxlyBin(relay), verifyErr)
		}
		fmt.Printf("   ⚠ remote install did not verify: %v\n", verifyErr)
		return fmt.Errorf("remote install did not verify with %s --version: %w", hostAuxlyBin(relay), verifyErr)
	}

	// 2. Authorize the box's key on THIS machine so the runtime tunnel auth works.
	if kerr := authorizeRemoteKeyLocally(relay); kerr != nil {
		fmt.Printf("   ⚠ could not authorize the box's key on this machine: %v\n", kerr)
	} else {
		fmt.Println("   ✓ Authorized the box's SSH key on this machine")
	}

	// Capture the box's own hostname so its live ssh-remote session (which reports
	// that hostname as RemoteHost) maps back to this box row instead of surfacing
	// as a phantom duplicate. Best-effort — an empty result just falls back to
	// name/target matching. Done before wiring so it's recorded even if wiring stalls.
	boxHostname := ""
	if out, herr := runSSH(relay, "hostname"); herr == nil {
		boxHostname = strings.TrimSpace(firstLine(out))
	}

	// Record the connection NOW — BEFORE the (sometimes slow, esp. on Windows)
	// agent wiring — so a stalled or failed wire never leaves a relay with no
	// matching client row (the "2 relays / 1 box" mismatch). The box is already
	// reachable over the relay with its key authorized; wiring its agent config is
	// a convenience that can be retried with [r] reconnect / [u] update.
	clientName := strings.TrimSpace(hostClientName)
	if clientName == "" {
		clientName = host
	}
	warnIfKnownMachine(boxHostname, clientName)
	if err := upsertClient(clientEntry{Name: clientName, Target: hc.Rendezvous, Method: "relay", Hostname: boxHostname}); err == nil {
		fmt.Printf("   ✓ Saved connection \"%s\" (manage with `auxly host clients`)\n", clientName)
	}

	// 3. Wire the box's agent to this machine's memory (explicit params — no offer
	//    dependency; the relay endpoint is localhost:<port> on the box itself).
	//    If this fails/stalls, the connection is already recorded above — surface a
	//    warning and let the user retry, rather than discarding the whole provision.
	target := fmt.Sprintf("%s@localhost:%d", hostUser, hc.ReversePort)
	fmt.Println("🔗 Wiring the box's agent to this machine's memory...")
	// Run on a FRESH connection (withoutMux): the shared ControlMaster was opened
	// by the OS probe BEFORE auxly was installed, so its session still carries the
	// pre-install PATH and a reused `auxly …` resolves to nothing ('auxly is not
	// recognized'). A fresh post-install session sees the updated PATH — the same
	// reason the readiness poll resolves auxly while the muxed wire did not.
	wireArgs := []string{hostAuxlyBin(relay), "connect", "use", name, "--method", "rendezvous", "--host", target, "--host-bin", macBin}
	if out, werr := runSSH(withoutMux(relay), wireArgs...); werr != nil {
		fmt.Printf("   ⚠ remote wiring failed: %v\n   %s\n", werr, firstLine(out))
		fmt.Printf("   The connection %q is saved — re-run wiring with [r] reconnect on the box row, or `/auxly-remote-connect` on the box.\n", clientName)
		// The box is reachable with its key authorized, but its agent isn't wired —
		// the connect's actual goal isn't met, so surface this as a failure (the
		// header reads "Finished with issues", not a misleading green "Done"). The
		// connection row is already saved above, so [r] reconnect resumes cleanly.
		return fmt.Errorf("remote agent wiring failed (connection %q saved, retry with [r] reconnect): %w", clientName, werr)
	}
	fmt.Println("   ✓ MCP launcher + skills wired on the box")

	// Prove the link with a real read before claiming success (same truth
	// signal as the health table and doctor).
	fmt.Println("🔎 Verifying the box can actually read this memory...")
	if out, serr := runSSH(withoutMux(relay), hostAuxlyBin(relay), "connect-mcp", name, "--selftest"); serr == nil {
		fmt.Printf("   ✅ box read this machine's memory: %s\n", firstLine(out))
	} else {
		fmt.Printf("   ⚠ link selftest failed: %s — retry with [r] reconnect\n", firstLine(out))
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
	Short:        "Install auxly on the relay box and wire its agent to this machine's memory",
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
	hostSetupCmd.Flags().BoolVar(&hostProvision, "provision", false, "also install auxly on the relay box and wire its agent to this machine's memory")
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
	hostVersionsCmd.Flags().BoolVar(&hostVersionsHealth, "health", false, "also verify wiring + run the end-to-end memory-link selftest per box")
	hostUpdateCmd.Flags().BoolVar(&hostUpdateAll, "all", false, "update every connected box that is outdated and idle")
	hostUpdateCmd.Flags().BoolVar(&hostUpdateForce, "force", false, "update even a box that is serving a live session (single-box only)")
	hostStatuslineCmd.Flags().BoolVar(&hostStatuslineAll, "all", false, "push the statusline to every connected box")
	hostCmd.AddCommand(hostVersionsCmd)
	hostCmd.AddCommand(hostUpdateCmd)
	hostCmd.AddCommand(hostStatuslineCmd)

	hostInviteCmd.Flags().StringVar(&hostInviteTTL, "ttl", "15m", "how long the invite stays valid (e.g. 15m, 1h, 24h)")
	hostInviteCmd.Flags().StringVar(&hostInviteHost, "host", "", "address the joiner should connect to (default: this machine's hostname)")
	hostInviteCmd.Flags().IntVar(&hostInvitePort, "port", defaultSSHPort, "this machine's sshd port")
	hostInviteCmd.Flags().BoolVar(&hostInviteList, "list", false, "list pending invites instead of minting one")
	hostInviteCmd.Flags().StringVar(&hostInviteRevoke, "revoke", "", "revoke a pending invite by id instead of minting one")
	hostConsumeCmd.Flags().StringVar(&hostConsumeClient, "client", "", "friendly name for the joining box (default: invited-<id>)")
	hostConsumeCmd.Flags().StringVar(&hostConsumeHostname, "hostname", "", "the joining box's self-reported hostname")
	hostCmd.AddCommand(hostInviteCmd)
	hostCmd.AddCommand(hostConsumeCmd)

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

	if loaded, _ := keepAliveStatus(); loaded {
		// Supervisor already running — it hot-reloads host.yaml within a few
		// seconds and dials the new relay WITHOUT restarting the existing tunnels,
		// so the other boxes' live sessions stay up. No reinstall (which would
		// bounce every tunnel) needed.
		recordHostProvision(hc, "relay added (supervisor hot-reloaded)")
		fmt.Println("   ✓ Relay added — existing box tunnels stay live (hot-reloaded)")
	} else if err := installKeepAlive(); err != nil {
		return err
	} else {
		recordHostProvision(hc, "host tunnel keep-alive installed")
		fmt.Println("   ✓ Keep-alive tunnel service installed and started")
	}

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

	// Full machine-driven provisioning of the relay box: install auxly there, authorize
	// its key here, and wire its agent — so nothing has to be run on the box.
	doProvision := hostProvision
	if !doProvision && !hostSetupBatch {
		fmt.Printf("\nAlso set up the relay box (%s) to USE this machine's memory now — install auxly there and wire its agent? [Y/n]: ", hc.Rendezvous)
		ans := ""
		if reader.Scan() {
			ans = strings.ToLower(strings.TrimSpace(reader.Text()))
		}
		doProvision = ans != "n" && ans != "no"
	}
	if doProvision {
		fmt.Println()
		if err := provisionConsumer(hc); err != nil {
			// Surface as a non-zero exit so callers (the TUI captured-run, scripts)
			// see the failure instead of a misleading success. The relay config is
			// already persisted above, so re-running picks up where this left off.
			fmt.Printf("   ⚠ Remote provisioning incomplete: %v\n", err)
			return fmt.Errorf("remote provisioning incomplete: %w", err)
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
	// Supervise one reverse tunnel per relay AND hot-reload from host.yaml, so
	// adding or removing a box never restarts the other boxes' tunnels. Blocks
	// forever; the per-OS keep-alive (launchd/systemd/Task Scheduler) owns us.
	superviseRelays()
	return nil
}

// relayReconcileInterval is how often the supervisor re-reads host.yaml to pick
// up added/removed relays without restarting the running tunnels.
const relayReconcileInterval = 5 * time.Second

// relayKey uniquely identifies a relay tunnel so the reconcile loop can tell
// which tunnels to keep, start, or stop across config reloads. Any param change
// (rendezvous, ports) yields a new key, so the old tunnel is replaced cleanly.
func relayKey(hc hostConfig) string {
	return fmt.Sprintf("%s|%d|%d|%d", hc.Rendezvous, hc.RendezvousPort, hc.ReversePort, hc.localPort())
}

// reconcileRelays computes which relay keys to start and which to stop given the
// currently-active set and the desired set. Pure — unit-testable.
func reconcileRelays(active, want map[string]bool) (start, stop []string) {
	for key := range want {
		if !active[key] {
			start = append(start, key)
		}
	}
	for key := range active {
		if !want[key] {
			stop = append(stop, key)
		}
	}
	return start, stop
}

// superviseRelays runs each configured relay's reverse tunnel and HOT-RELOADS:
// it re-reads host.yaml on an interval and reconciles the running tunnels —
// starting goroutines for newly-added relays and cancelling ONLY the removed
// ones. Existing, unchanged tunnels are never disturbed, so connecting or
// removing one box never drops the other boxes' live sessions. Blocks forever.
func superviseRelays() {
	active := map[string]context.CancelFunc{} // relayKey → cancel its tunnel
	defer func() {
		for _, cancel := range active {
			cancel()
		}
	}()

	reconcile := func() {
		relays, ok, err := loadHostConfigs()
		if err != nil {
			return // transient read error — keep current tunnels, retry next tick
		}
		want := map[string]hostConfig{}
		if ok {
			for _, hc := range relays {
				if validateRendezvous(hc) != nil {
					continue
				}
				want[relayKey(hc)] = hc
			}
		}
		activeKeys := make(map[string]bool, len(active))
		for k := range active {
			activeKeys[k] = true
		}
		wantKeys := make(map[string]bool, len(want))
		for k := range want {
			wantKeys[k] = true
		}
		start, stop := reconcileRelays(activeKeys, wantKeys)
		for _, key := range stop {
			active[key]() // cancel — drops only this relay's tunnel
			delete(active, key)
		}
		for _, key := range start {
			ctx, cancel := context.WithCancel(context.Background())
			active[key] = cancel
			go superviseTunnel(ctx, want[key])
		}
	}

	reconcile()           // start the initial set immediately
	go reconcileClients() // and verify every box's wiring once at startup
	lastClientPass := time.Now()
	ticker := time.NewTicker(relayReconcileInterval)
	defer ticker.Stop()
	for range ticker.C {
		reconcile()
		// Piggyback the hourly client-wiring reconcile on the relay ticker; it
		// runs in the background so a slow box never delays tunnel supervision.
		if time.Since(lastClientPass) >= clientReconcileInterval {
			lastClientPass = time.Now()
			go reconcileClients()
		}
	}
}

// superviseTunnel runs one relay's reverse tunnel, restarting it whenever it
// exits — until its context is cancelled (the relay was removed). Each relay
// runs in its own goroutine so the tunnels are independent.
//
// Retries escalate 5s → 30s → 2m → 10m (reset after a tunnel that actually
// held) so an auth-dead or firewalled relay is not hammered every 5s forever —
// that pattern trips MaxAuthTries/fail2ban on the relay and turns a config
// problem into a lockout. After 5 consecutive failures ONE audit entry records
// the last stderr line (Permission denied vs timeout vs port in use), instead
// of per-attempt log spam.
func superviseTunnel(ctx context.Context, hc hostConfig) {
	fails := 0
	audited := false
	for {
		if ctx.Err() != nil {
			return
		}
		tail := &tailBuffer{}
		c := exec.CommandContext(ctx, "ssh", tunnelArgs(hc)...)
		c.Stdout = os.Stdout
		c.Stderr = io.MultiWriter(os.Stderr, tail)
		started := time.Now()
		_ = c.Run() // returns when the tunnel drops OR the context is cancelled
		if ctx.Err() != nil {
			return // relay removed — stop quietly, leaving sibling tunnels alone
		}

		if time.Since(started) > time.Minute {
			fails, audited = 0, false // it held — this drop is a disconnect, not a broken config
		}
		fails++
		stderrLine := tail.LastLine()

		if fails >= 5 && !audited {
			audited = true
			logTunnelFailure(hc, fails, stderrLine)
		}

		// Stale reverse-port listener on the relay (a dead prior tunnel holding
		// the bind): one best-effort cleanup so the NEXT retry binds instead of
		// failing until sshd times the ghost out.
		if strings.Contains(stderrLine, "remote port forwarding failed") {
			cleanupStaleRelayPort(ctx, hc)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(nextTunnelBackoff(fails)):
		}
	}
}

// nextTunnelBackoff maps consecutive failures to the retry delay:
// 5s, 30s, 2m, then 10m for everything after.
func nextTunnelBackoff(consecutiveFails int) time.Duration {
	steps := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	if consecutiveFails < 1 {
		consecutiveFails = 1
	}
	if consecutiveFails > len(steps) {
		return steps[len(steps)-1]
	}
	return steps[consecutiveFails-1]
}

// tailBuffer keeps only the last 4 KB written to it — enough for ssh's final
// error lines without growing unbounded on a chatty long-lived tunnel.
type tailBuffer struct {
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

// LastLine returns the last non-empty line seen.
func (t *tailBuffer) LastLine() string {
	lines := strings.Split(strings.TrimRight(string(t.buf), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// logTunnelFailure writes the single escalation audit entry for a relay that
// keeps failing, with the distinguishing stderr line as the reason.
func logTunnelFailure(hc hostConfig, fails int, stderrLine string) {
	reason := fmt.Sprintf("relay %s: %d consecutive tunnel failures — last error: %s", hc.Rendezvous, fails, stderrLine)
	fmt.Fprintf(os.Stderr, "⚠ %s (retrying with escalating backoff, up to 10m)\n", reason)
	if logger, err := audit.NewLogger(getMemoryPath()); err == nil {
		defer logger.Close()
		logger.Log("host-tunnel", "system", "tunnel_failure", hc.Rendezvous, "", reason, "auto")
	}
}

// cleanupStaleRelayPort best-effort kills whatever still holds the reverse port
// on the relay. ponytail: POSIX relays only (fuser); a Windows relay just waits
// out sshd's own timeout as before.
func cleanupStaleRelayPort(ctx context.Context, hc hostConfig) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"-T",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
	}
	if hc.RendezvousPort != 0 && hc.RendezvousPort != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(hc.RendezvousPort))
	}
	args = append(args, "--", hc.Rendezvous, fmt.Sprintf("fuser -k %d/tcp >/dev/null 2>&1 || true", hc.ReversePort))
	_ = exec.CommandContext(cctx, "ssh", args...).Run()
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

// selfHealKeepAlive re-installs the keep-alive service when relays are
// configured but the service is not loaded — the July 2026 field incident:
// launchd silently dropped the agent and every tunnel stayed down for two days
// with zero signal. Any long-lived auxly entrypoint (TUI, MCP server) calls
// this on start, so the first thing that runs after the drop repairs it.
// Idempotent, best-effort, opt-out via AUXLY_HOST_SELFHEAL=off.
func selfHealKeepAlive() {
	if os.Getenv("AUXLY_HOST_SELFHEAL") == "off" {
		return
	}
	relays, ok, err := loadHostConfigs()
	if err != nil || !ok || len(relays) == 0 {
		return // not a host — nothing to heal
	}
	if loaded, _ := keepAliveStatus(); loaded {
		return
	}
	// Every agent session on a host spawns its own mcp-server PROCESS, and a
	// keep-alive drop is exactly when many reconnect at once — serialize the
	// check+install across processes or their unload/load calls interleave and
	// re-bounce the tunnel supervisor during the recovery window.
	dir, derr := auxlyDir()
	if derr != nil {
		return
	}
	unlock, lerr := memory.LockVault(filepath.Join(dir, ".selfheal"))
	if lerr != nil {
		return // someone else is healing (or lock unavailable) — done either way
	}
	defer unlock()
	if loaded, _ := keepAliveStatus(); loaded {
		return // the process that held the lock before us already healed it
	}
	if err := installKeepAlive(); err != nil {
		return // best-effort: broken service manager shouldn't break the TUI/MCP
	}
	if logger, lerr := audit.NewLogger(getMemoryPath()); lerr == nil {
		defer logger.Close()
		logger.Log("host-tunnel", "system", "keepalive_selfheal", "host.yaml", "",
			"keep-alive service was not loaded with relays configured — reinstalled automatically", "auto")
	}
}

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
