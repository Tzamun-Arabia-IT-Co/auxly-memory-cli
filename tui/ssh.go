package tui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/clipboard"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

// copyInvite is a package-level var (not a direct clipboard.Copy call) so
// tests can stub it out — the real thing shells out to a platform tool
// that isn't guaranteed present on a CI box.
var copyInvite = clipboard.Copy

// ─────────────────────────────────────────────────────────────────
//  Remote Memory over SSH — interactive management surface.
//
//  The old "SSH Bridge" stack (reverse tunnel + localhost daemon on
//  port 7357 + session token + cross-compile/scp) is gone. The model
//  is now plain SSH: the memory HOST runs `auxly mcp-server` and this
//  machine launches it over SSH. This tab lists configured remotes and
//  drives the same `auxly connect` CLI for add/test/remove (shelling
//  out so SSH password/keygen prompts work), plus an in-TUI config
//  preview. `auxly connect` on its own keeps working unchanged.
// ─────────────────────────────────────────────────────────────────

// remoteEntry is one configured remote read from ~/.auxly/remotes.yaml.
type remoteEntry struct {
	Name   string `yaml:"name"`
	Method string `yaml:"method"`
	OS     string `yaml:"os"`
	User   string `yaml:"user"`
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
}

// spec rebuilds the [user@]host[:port] form used by the add form.
func (r remoteEntry) spec() string {
	s := r.Host
	if r.User != "" {
		s = r.User + "@" + r.Host
	}
	if r.Port != 0 && r.Port != 22 {
		s = fmt.Sprintf("%s:%d", s, r.Port)
	}
	return s
}

// remotesFile is the shape we read from ~/.auxly/remotes.yaml. A missing
// file is tolerated silently.
type remotesFile struct {
	Remotes []remoteEntry `yaml:"remotes"`
}

// ssh interaction modes.
const (
	sshModeList     = ""
	sshModeConfirm  = "confirm"
	sshModePrint    = "print"
	sshModeForm     = "form"
	sshModeProgress = "progress" // a captured connect command is running
	sshModeResult   = "result"   // showing its captured output
	sshModePassword = "password" // masked SSH-password entry (PTY key install)
	sshModeRename   = "rename"   // inline rename of a selected connection
	sshModeShare    = "share"    // per-remote file-sharing checklist (§10)
	sshModeJoin     = "join"     // paste-token modal for `auxly join <token>`
)

// inviteTTLPresets are the TTL choices [i] cycles through before minting an
// invite with [I]. Sprint 21 §3 asks for a "minimal mint invite modal" with
// these presets; rather than a bespoke picker UI, [i]/[I] drive the SAME
// beginCapturedSub pipeline every other host action here uses (`auxly host
// invite --ttl <preset>`), so the result — the token, expiry, and join
// command — renders in the existing sshModeResult panel with no new render
// code. That panel already reads like a modal (bordered, dismiss-on-any-key).
var inviteTTLPresets = []string{"1h", "24h", "7d"}

// form steps.
const (
	formStepMethod = "method"
	formStepHost   = "host"
	formStepJump   = "jump"
	formStepName   = "name"
	formStepShare  = "share" // relay only: pick what the box may read/write, pre-connect
)

type sshModel struct {
	remotes []remoteEntry
	clients []clientRow // remote boxes wired to use THIS machine (host side) — managed list
	host    hostInfo    // this machine's relay-host config (host.yaml)
	hostOK  bool        // true when this machine is set up as a relay host
	cursor  int
	rename  string // transient buffer for the rename action (sshModeRename)
	mode    string
	preview string // MCP JSON shown in print mode
	status  string // transient feedback after an action
	width   int
	height  int

	// add-host form state (mode == sshModeForm)
	formStep   string
	formOS     string
	formMethod string
	formHost   string
	formJump   string
	formName   string

	// captured-run state (sshModeProgress / sshModeResult)
	progressTitle  string
	progressOut    []string
	progressLast   string // most recent streamed line (current status)
	progressOK     bool
	progressPct    int                // milestone-based progress (0–100)
	spin           int                // spinner frame counter
	progressCh     chan progressEvent // live line stream from the running command
	progressNeeded bool               // first-time SSH key setup required
	pendingKeySub  string             // subcommand for the key re-run ("connect" or "host")
	pendingKeyArgs []string           // non-batch args for the key-setup fallback ([p]/[K])
	password       string             // transient masked SSH password (sshModePassword)
	twoWayFailed   bool               // host can't reach back; offer [u] consumer direction
	twoWayHost     string             // profile name to use when [u] is pressed

	// editingHost drives app.go's key-routing contract: when true, ALL keys are
	// delivered to this model (so the in-TUI add-host form can capture text). It
	// is set only while the form is open.
	editingHost bool

	// per-box update status (#1/#2): keyed by lowercased box name, populated async
	// by probeRemoteVersionsCmd. nil until the first sweep completes. defaultWrite
	// mirrors the DefaultRemoteWrite setting so the permission column shows the
	// effective access. versionsProbed guards against re-kicking the SSH sweep.
	versions       map[string]clientVersionStatus
	defaultWrite   bool
	versionsProbed bool
	lastUpdateBox  string // box targeted by the last [u] update, for [f] force-retry

	// per-remote file-sharing modal state (mode == sshModeShare, §10)
	shareOpen   bool           // modal visible
	shareClient clientRow      // the inbound client being edited
	shareFiles  []string       // rows, in taxonomy order
	shareState  map[string]int // file → shareOff | shareRead | shareReadWrite
	shareCursor int            // highlighted row within the modal

	// Post-connect share step (relay flow). When pendingShare is set, a successful
	// add opens the per-file sharing modal for the newly provisioned inbound client
	// as the FINAL wizard step — relay makes THIS machine the host, so choosing what the
	// box may read/write is the natural last move. preShareNames snapshots the client
	// names that existed before the run so the new one is found by diff; pendingShareNm
	// is the friendly name the user typed (a fallback match for a re-add).
	pendingShare   bool
	pendingShareNm string
	preShareNames  map[string]bool

	// inviteTTLIdx selects the pending TTL (into inviteTTLPresets) for the
	// next [I] mint — this machine is always eligible to host an invite
	// (Sprint 21's direct-SSH pairing needs no prior `host setup`/relay), so
	// unlike the relay panel above it isn't gated on hostOK.
	inviteTTLIdx int

	// inviteToken holds the most recently minted invite string so [y] can
	// re-copy it after the mint result panel is dismissed, without having to
	// mint again. It's a secret, so it's cleared (never left lying around in
	// memory) the moment the user leaves the Remote tab or mints a new one —
	// see gotoScreen in app.go and the progressEvent handling below.
	inviteToken string

	// joinToken is the transient paste buffer for the [J] "join a host" modal
	// (mode == sshModeJoin) — the consumer-side counterpart to [i]/[I] mint.
	// Cleared the moment the modal closes (submit or cancel), same discipline
	// as inviteToken above.
	joinToken string
}

// Per-file sharing tri-state, cycled with ←/→ in the share modal.
const (
	shareOff       = 0 // not shared
	shareRead      = 1 // shared, read-only
	shareReadWrite = 2 // shared, read & write
)

// cycleShareState advances a tri-state Off→Read→Read+Write→Off (forward) or the
// reverse, so ←/→ both walk the same loop in opposite directions.
func cycleShareState(cur int, forward bool) int {
	if forward {
		return (cur + 1) % 3
	}
	return (cur + 2) % 3
}

// sshRefreshMsg carries the freshly read remotes list + host config into Update.
type sshRefreshMsg struct {
	remotes []remoteEntry
	clients []clientRow
	host    hostInfo
	hostOK  bool
}

// sshExecDoneMsg is returned after a SUSPENDED (ExecProcess) `auxly connect …`
// finishes — used only for the key-setup step that needs a terminal password.
type sshExecDoneMsg struct {
	status string
}

// progressEvent is one streamed line from a running `auxly connect …` command,
// or (done=true) the terminal event with the full captured output. It lets the
// doctor/setup steps render live inside the TUI.
type progressEvent struct {
	line     string
	done     bool
	err      error
	out      string
	needsKey bool
}

// sshSpinTickMsg animates the progress spinner.
type sshSpinTickMsg struct{}

// sshDataTickMsg drives a periodic re-read of the remotes/clients/host so inbound
// connections (and drops) appear on the Remote screen automatically, without the
// user having to generate an input event. Mirrors the dashboard/activity ticks.
type sshDataTickMsg struct{}

func sshDataTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return sshDataTickMsg{} })
}

// ─────────────────────────────────────────────────────────────────
//  Constructor / data
// ─────────────────────────────────────────────────────────────────

func newSSHModel() sshModel {
	h, ok := readHostInfo()
	return sshModel{
		remotes:      readRemotes(),
		clients:      readClients(),
		host:         h,
		hostOK:       ok,
		defaultWrite: config.LoadSettings().DefaultRemoteWrite,
	}
}

func (m sshModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		h, ok := readHostInfo()
		return sshRefreshMsg{remotes: readRemotes(), clients: readClients(), host: h, hostOK: ok}
	}
}

func remotesConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".auxly", "remotes.yaml")
}

func readRemotes() []remoteEntry {
	path := remotesConfigPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed remotesFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	return parsed.Remotes
}

// hostInfo mirrors the host.yaml this machine writes when it acts as a memory
// HOST served to NAT'd/remote boxes through a relay (the relay/`host setup` flow).
type hostInfo struct {
	Rendezvous     string `yaml:"rendezvous"`
	RendezvousPort int    `yaml:"rendezvous_port"`
	ReversePort    int    `yaml:"reverse_port"`
	HostUser       string `yaml:"host_user"`
	RelayCount     int    `yaml:"-"` // number of relays served (1 unless multi-relay)
	TunnelUp       bool   `yaml:"-"` // a reverse-tunnel process is actually running
}

// hostTunnelsLive (defined per-OS in ssh_tunnels_unix.go / ssh_tunnels_windows.go)
// reports whether at least one reverse-tunnel process (`ssh … -R <port>:localhost:…`)
// is currently running on this machine. host.yaml existing only means this box is
// CONFIGURED as a relay host — the tunnels are a separate keep-alive process that can
// be down (crashed, never started, killed on logout). Checking the live process avoids
// the TUI showing "● serving" while every box is actually cut off. It is split per-OS
// because Windows has no pgrep (the old single impl always returned false there, so a
// Windows host's TUI permanently — and falsely — showed "● tunnels down").

// clientRow mirrors an entry in ~/.auxly/clients.yaml — a remote box this machine
// (as a host) has wired to use its memory. These are the managed connections.
type clientRow struct {
	Name     string `yaml:"name"`
	Target   string `yaml:"target"`
	Method   string `yaml:"method"`
	Hostname string `yaml:"hostname"` // box's self-reported hostname (matches session RemoteHost)
	// Per-remote file-sharing selection (§10). SharedFiles is the explicit set of
	// memory files this inbound client may read; an empty/unset slice means "use
	// the default" (all non-personal files, personal off). WriteFiles is the
	// per-file writable subset (each also in SharedFiles). Access is the legacy
	// global write flag, superseded by WriteFiles but kept for back-compat.
	SharedFiles []string `yaml:"shared_files,omitempty"`
	WriteFiles  []string `yaml:"write_files,omitempty"`
	Access      string   `yaml:"access,omitempty"`
}

type clientsYAML struct {
	Clients []clientRow `yaml:"clients"`
}

func readClients() []clientRow {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".auxly", "clients.yaml"))
	if err != nil {
		return nil
	}
	var f clientsYAML
	if yaml.Unmarshal(data, &f) != nil {
		return nil
	}
	return f.Clients
}

// readHostInfo loads ~/.auxly/host.yaml; ok is false when this machine is not
// configured as a relay host. It reads the multi-relay list form (and the legacy
// single-relay form), reporting the first relay plus the total count.
func readHostInfo() (hostInfo, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return hostInfo{}, false
	}
	data, err := os.ReadFile(filepath.Join(home, ".auxly", "host.yaml"))
	if err != nil {
		return hostInfo{}, false
	}
	// New multi-relay form: a list of relays under `relays:`.
	var list struct {
		Relays []hostInfo `yaml:"relays"`
	}
	if yaml.Unmarshal(data, &list) == nil && len(list.Relays) > 0 {
		h := list.Relays[0]
		h.RelayCount = len(list.Relays)
		h.TunnelUp = hostTunnelsLive()
		return h, true
	}
	// Legacy single-relay form.
	var h hostInfo
	if yaml.Unmarshal(data, &h) != nil || strings.TrimSpace(h.Rendezvous) == "" {
		return hostInfo{}, false
	}
	h.RelayCount = 1
	h.TunnelUp = hostTunnelsLive()
	return h, true
}

// The Remote tab stacks up to two selectable lists: the connected boxes this
// machine HOSTS (shown when hostOK) followed by the memory hosts it CONSUMES
// (remotes.yaml). A single cursor indexes them as one virtual list — clients
// first, then remotes — so a machine that is BOTH a host and a consumer can
// still select and manage its consumer remotes (not just its boxes).

// clientCount is the number of navigable connected-box rows. Boxes are only
// selectable when this machine is configured as a host (the section is hidden
// otherwise), so this is 0 unless hostOK.
func (m sshModel) clientCount() int {
	// Count configured clients regardless of host state. When the host tunnel is
	// down they're unreachable, but they're still saved in clients.yaml and MUST
	// stay selectable so the user can see and remove them (otherwise a "deleted"
	// box silently lingers, reappearing the moment the host comes back up).
	return len(m.clients)
}

// cursorOnClient returns the connected box under the cursor, when the cursor is
// in the clients region (the first clientCount() slots).
func (m sshModel) cursorOnClient() (clientRow, bool) {
	if m.cursor >= 0 && m.cursor < m.clientCount() {
		return m.clients[m.cursor], true
	}
	return clientRow{}, false
}

// cursorOnRemote returns the consumer remote under the cursor, when the cursor
// is in the remotes region (the slots after the clients).
func (m sshModel) cursorOnRemote() (remoteEntry, bool) {
	i := m.cursor - m.clientCount()
	if i >= 0 && i < len(m.remotes) {
		return m.remotes[i], true
	}
	return remoteEntry{}, false
}

func (m sshModel) selectedName() string {
	if r, ok := m.cursorOnRemote(); ok {
		return r.Name
	}
	return ""
}

// selectedClient returns the highlighted connected box (host side) and whether
// the cursor is on one.
func (m sshModel) selectedClient() (clientRow, bool) {
	return m.cursorOnClient()
}

func exePath() string {
	if bin, err := os.Executable(); err == nil && bin != "" {
		return bin
	}
	return "auxly"
}

// runConnect SUSPENDS the TUI (tea.ExecProcess) to run `auxly connect …` against
// the real terminal — used only when an SSH password prompt is unavoidable
// (first-time key install).
func runConnect(args ...string) tea.Cmd {
	return runSub("connect", args...)
}

// runSub suspends the TUI and runs `auxly <sub> <args…>` attached to the real
// terminal, so an interactive password prompt (e.g. relay `host setup`) works.
func runSub(sub string, args ...string) tea.Cmd {
	if sub == "" {
		sub = "connect"
	}
	c := exec.Command(exePath(), append([]string{sub}, args...)...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sshExecDoneMsg{status: "⚠ " + sub + " " + strings.Join(args, " ") + " exited: " + err.Error()}
		}
		return sshExecDoneMsg{status: ""}
	})
}

// runHost SUSPENDS the TUI to run an interactive `auxly host …` (e.g. `host
// setup`, which prompts for the relay). This is the relay-tunnel escape hatch
// when the host can't dial back to a NAT'd host.
func runHost(args ...string) tea.Cmd {
	c := exec.Command(exePath(), append([]string{"host"}, args...)...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sshExecDoneMsg{status: "⚠ host " + strings.Join(args, " ") + " exited: " + err.Error()}
		}
		return sshExecDoneMsg{status: "✓ Relay configured. Run `auxly host remote` in a terminal to (re)print the command to paste on the server."}
	})
}

// startCapturedRun spawns `auxly connect …` and streams its output line-by-line
// into ch (no TUI suspend). Safe only for non-interactive runs (no password) —
// add uses --batch to guarantee that. The PTY variant (startPTYRun) handles the
// password case.
func startCapturedRun(ch chan progressEvent, sub string, args ...string) {
	go func() {
		c := exec.Command(exePath(), append([]string{sub}, args...)...)
		pr, pw, err := os.Pipe()
		if err != nil {
			ch <- progressEvent{done: true, err: err, out: "pipe error: " + err.Error()}
			return
		}
		c.Stdout = pw
		c.Stderr = pw
		if err := c.Start(); err != nil {
			pw.Close()
			pr.Close()
			ch <- progressEvent{done: true, err: err, out: "start error: " + err.Error()}
			return
		}
		pw.Close() // parent's copy; the child keeps its own dup
		var all strings.Builder
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			all.WriteString(line)
			all.WriteByte('\n')
			ch <- progressEvent{line: line}
		}
		werr := c.Wait()
		pr.Close()
		out := all.String()
		ch <- progressEvent{done: true, err: werr, out: out, needsKey: strings.Contains(out, "AUXLY_KEY_REQUIRED")}
	}()
}

// waitProgress blocks for the next streamed event from ch.
func waitProgress(ch chan progressEvent) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// spinTick drives the progress spinner animation.
func spinTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return sshSpinTickMsg{} })
}

// beginRun resets progress state and starts the stream+spinner loops.
func (m sshModel) beginRun(title string, ch chan progressEvent) (sshModel, tea.Cmd) {
	m.progressCh = ch
	m.progressOut = nil
	m.progressLast = ""
	m.progressPct = 0
	m.spin = 0
	m.progressTitle = title
	m.twoWayFailed = false
	m.twoWayHost = ""
	m.mode = sshModeProgress
	return m, tea.Batch(waitProgress(ch), spinTick())
}

func (m sshModel) beginCaptured(title string, args ...string) (sshModel, tea.Cmd) {
	return m.beginCapturedSub(title, "connect", args...)
}

// beginCapturedSub streams any `auxly <sub> …` subcommand (e.g. "host setup")
// into the progress pane, so the relay flow runs natively in-TUI.
func (m sshModel) beginCapturedSub(title, sub string, args ...string) (sshModel, tea.Cmd) {
	ch := make(chan progressEvent, 128)
	startCapturedRun(ch, sub, args...)
	return m.beginRun(title, ch)
}

// mintedInviteToken pulls the encoded invite token out of `host invite`'s
// captured output by matching its own "auxly join <token>" hint line —
// reusing the plain human-readable output instead of adding a second
// machine-readable marker, so `auxly host invite` run directly in a
// terminal stays unchanged. Pure (a line slice in, a string out) so it's
// testable without a captured subprocess.
func mintedInviteToken(lines []string) string {
	const prefix = "auxly join "
	for _, l := range lines {
		if s := strings.TrimSpace(l); strings.HasPrefix(s, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(s, prefix))
		}
	}
	return ""
}

func (m sshModel) beginPTY(title, password, sub string, args ...string) (sshModel, tea.Cmd) {
	if sub == "" {
		sub = "connect"
	}
	ch := make(chan progressEvent, 128)
	startPTYRun(ch, password, sub, args...)
	return m.beginRun(title, ch)
}

// stepPct maps the CLI's machine-readable AUXLY_STEP:<step>:<state> contract to
// bar positions — the stable signal. The fuzzy human-line matching below stays
// as fallback for older binaries and host-side actions.
var stepPct = map[string]int{
	"connect:ok":     30,
	"install:start":  40,
	"install:ok":     55,
	"return-path:ok": 62,
	"key-auth:ok":    70,
	"wire:start":     75,
	"wire:ok":        85,
	"selftest:start": 90,
	"selftest:ok":    97,
}

// milestonePct maps a streamed line to a coarse completion percentage so the bar
// advances through the recognisable stages of whichever flow is running — the
// connect/setup doctor AND the host-side box actions (update / reconnect / forget /
// provision). The caller only ever raises the bar, so order matters solely when one
// line could match two cases; the later-stage markers are listed first.
func milestonePct(line string) int {
	if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "AUXLY_STEP:"); ok {
		return stepPct[strings.TrimSpace(rest)] // unknown/fail states: 0 (never snaps)
	}
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "configured") || strings.Contains(l, "injected") || strings.Contains(l, "restart your") || strings.Contains(l, "onboard"):
		return 95
	// host-action completion markers (update / reconnect / disconnect / forget / wire)
	case strings.Contains(l, "statusline applied") || strings.Contains(l, "statusline ensured") ||
		strings.Contains(l, "restart the agent") || strings.Contains(l, "restart that box"):
		return 95
	case strings.Contains(l, "updated to") || strings.Contains(l, "saved connection") ||
		strings.Contains(l, "disconnected") || strings.Contains(l, "reconnected") ||
		strings.Contains(l, "re-wired") || strings.Contains(l, "rewired") || strings.Contains(l, "wired to") ||
		strings.Contains(l, "removed"):
		return 90
	case strings.Contains(l, "saved remote profile"):
		return 85
	case strings.Contains(l, "wiring") || strings.Contains(l, "authorized"):
		return 80
	case strings.Contains(l, "auxly present on host") || strings.Contains(l, "auxly installed"):
		return 75
	// in-flight markers — "Updating X (a → b)…", "Installing auxly…", reconnect/forget starts
	case strings.Contains(l, "installing") || strings.Contains(l, "updating"):
		return 55
	case strings.Contains(l, "host reachable"):
		return 45
	case strings.Contains(l, "disconnecting") || strings.Contains(l, "reconnecting") || strings.Contains(l, "removing"):
		return 35
	case strings.Contains(l, "local ssh client"):
		return 25
	case strings.Contains(l, "doctor"):
		return 10
	}
	return 0
}

// progressCreepCeiling is how far the bar may auto-advance between observed milestones.
// It stays below 100 so the bar never claims "done" before the terminal event; a real
// milestone line can still snap it past this, and done fills it to 100.
const progressCreepCeiling = 97

// creepRampTop is where the fast initial ramp ends and the slow crawl begins.
const creepRampTop = 80

// creepProgress nudges the bar forward one spinner tick (frame is the spinner counter).
// It has two phases so the bar both gets going quickly AND keeps the number visibly
// inching through a long opaque wait instead of slamming to the ceiling and sitting:
//   - below creepRampTop: a brisk ramp so the bar gets moving right away;
//   - up to the ceiling: a slow frame-throttled crawl (~one cell every few ticks).
//
// Real milestone lines still snap it forward; only the done event fills it to 100.
func creepProgress(pct, frame int) int {
	if pct >= progressCreepCeiling {
		return pct
	}
	if pct < creepRampTop {
		step := (creepRampTop + 5 - pct) / 8
		if step < 1 {
			step = 1
		}
		return pct + step
	}
	// Slow crawl: advance one cell every 6th tick (~0.7s at the 120ms spinner cadence),
	// so the number keeps creeping up during a minute-long model run.
	if frame%6 == 0 {
		return pct + 1
	}
	return pct
}

func spinnerFrame(i int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[((i%len(frames))+len(frames))%len(frames)]
}

func progressBar(pct, width int) string {
	if width < 4 {
		width = 4
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return renderMeter(pct*width/100, width, ColorPrimary)
}

func mcpConfigJSON(name string) string {
	return fmt.Sprintf(`{
  "mcpServers": {
    "auxly-memory": {
      "command": "auxly",
      "args": ["connect-mcp", "%s", "--provider", "claude-code"]
    }
  }
}`, name)
}

// ─────────────────────────────────────────────────────────────────
//  Update
// ─────────────────────────────────────────────────────────────────

func (m sshModel) Update(msg tea.Msg) (sshModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case sshRefreshMsg:
		m.remotes = msg.remotes
		m.clients = msg.clients
		m.host = msg.host
		m.hostOK = msg.hostOK
		m.cursor = clampCursor(m.cursor, m.listLen())
		m.mode = sshModeList
		return m, nil

	case sshExecDoneMsg:
		m.status = msg.status
		m.mode = sshModeList
		m.versionsProbed = false // re-sweep versions after any action
		// The [K]-to-terminal key path leaves the captured-run flow, so drop any armed
		// share intent — it must not carry over and pop open on a later, unrelated run.
		m.pendingShare, m.pendingShareNm, m.preShareNames = false, "", nil
		return m, m.Refresh()

	case progressEvent:
		if msg.done {
			var out []string
			m.twoWayFailed = false
			m.twoWayHost = ""
			for _, l := range strings.Split(strings.TrimRight(msg.out, "\n"), "\n") {
				if strings.Contains(l, "AUXLY_KEY_REQUIRED") {
					continue // internal token, not for display
				}
				if idx := strings.Index(l, "AUXLY_TWOWAY_FAILED:"); idx >= 0 {
					m.twoWayFailed = true
					m.twoWayHost = strings.TrimSpace(l[idx+len("AUXLY_TWOWAY_FAILED:"):])
					continue // internal token, not for display
				}
				out = append(out, l)
			}
			if len(out) > 0 {
				m.progressOut = out
			}
			m.progressOK = msg.err == nil
			m.progressNeeded = msg.needsKey
			// A successful `host invite` mint prints "auxly join <token>" — grab
			// the token so [y] can (re)copy it later. Any other action's output
			// won't contain that line, so this only ever fires for a real mint,
			// and it naturally replaces (never blank-clears) a token already held.
			if m.progressOK {
				if tok := mintedInviteToken(out); tok != "" {
					m.inviteToken = tok
				}
			}
			m.progressPct = 100
			m.progressCh = nil
			m.mode = sshModeResult
			m.remotes = readRemotes() // reload lists behind the result panel
			m.clients = readClients()
			m.host, m.hostOK = readHostInfo()
			m.versionsProbed = false // an update may have changed box versions
			m.cursor = clampCursor(m.cursor, m.listLen())
			// A relay add that succeeded (and didn't stall on a key) produces a new
			// inbound client — apply the permissions the user chose in the wizard's
			// share step to its clients.yaml entry, and confirm it in the result panel.
			if m.pendingShare && m.progressOK && !m.progressNeeded {
				if nc, ok := newlyAddedClient(m.clients, m.preShareNames, m.pendingShareNm); ok {
					shared, writes := shareSelection(m.shareFiles, m.shareState)
					saveClientSharing(nc, shared, writes)
					m.clients = readClients()
					m.cursor = clampCursor(m.cursor, m.listLen())
					m.progressOut = append(m.progressOut,
						fmt.Sprintf("✓ Permissions applied to %s — %d readable, %d writable", nc.Name, len(shared), len(writes)))
					m.status = "Configured access for " + nc.Name
				}
			}
			// Keep the share intent alive while a key step is pending ([p]/[K] on the
			// result panel re-runs the provision); clear it only once no retry is queued.
			if !m.progressNeeded {
				m.pendingShare, m.pendingShareNm, m.preShareNames = false, "", nil
			}
			return m, nil
		}
		if line := strings.TrimRight(msg.line, "\r"); strings.TrimSpace(line) != "" {
			if p := milestonePct(line); p > m.progressPct {
				m.progressPct = p
			}
			// Machine step lines drive the bar but are noise in the log pane.
			if !strings.HasPrefix(strings.TrimSpace(line), "AUXLY_STEP:") {
				m.progressLast = line
				m.progressOut = append(m.progressOut, line)
			}
		}
		if m.progressCh != nil {
			return m, waitProgress(m.progressCh)
		}
		return m, nil

	case sshSpinTickMsg:
		if m.mode == sshModeProgress {
			m.spin++
			// Creep the bar forward each tick so it shows continuous motion through
			// phases the TUI can't observe — e.g. a box's `connect auto` runs entirely
			// server-side over SSH, its output captured not streamed. Milestone lines
			// still snap it forward; only the done event fills it to 100%.
			m.progressPct = creepProgress(m.progressPct, m.spin)
			return m, spinTick()
		}
		return m, nil

	case remoteVersionsMsg:
		m.versions = versionsByName(msg.statuses)
		return m, nil

	case sshDataTickMsg:
		// Only refresh the passive list view. In any sub-mode (form/password/rename/
		// share/progress/result) leave state untouched so typing and selection are
		// never interrupted by the background poll — but always re-arm the tick so it
		// keeps running while the Remote screen is open.
		if m.mode == sshModeList {
			m.remotes = readRemotes()
			m.clients = readClients()
			m.host, m.hostOK = readHostInfo()
			m.defaultWrite = config.LoadSettings().DefaultRemoteWrite
			m.cursor = clampCursor(m.cursor, m.listLen())
		}
		// Kick a one-shot version sweep the first time we're on the screen with boxes
		// (and again after an update resets the flag). SSH-bound, so never on a hot path.
		if !m.versionsProbed && m.hostOK && len(m.clients) > 0 {
			m.versionsProbed = true
			return m, tea.Batch(sshDataTickCmd(), probeRemoteVersionsCmd(true))
		}
		return m, sshDataTickCmd()

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// listLen is the number of selectable rows across both stacked lists: the
// connected boxes (shown when hostOK) plus the consumer remotes.
func (m sshModel) listLen() int {
	return m.clientCount() + len(m.remotes)
}

func clampCursor(c, n int) int {
	if c >= n {
		c = n - 1
	}
	if c < 0 {
		c = 0
	}
	return c
}

// listAnchorY is the screen row (0-based) where the first selectable connection
// row is drawn — used to map a mouse click to a list index. It tracks the View
// layout: banner + tab strip + panel border/padding + the fixed header lines
// above the list.
func (m sshModel) listAnchorY() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	// Mirror activity.go's offset model: panel top border at tabRow+4, then
	// +2 for the border+padding, then the fixed header lines before the list.
	contentTop := strings.Count(renderBanner(w), "\n") + 4 + 2
	if m.hostOK {
		// REMOTE(0) intro(1) press-c(2) blank(3) HOSThdr(4) relay(5) boxes(6)
		// blank(7) CONNECTEDhdr(8) blank(9) firstRow(10)
		return contentTop + 10
	}
	// REMOTE(0) intro(1) press-c(2) blank(3) HOSTShdr(4) blank(5) firstRow(6)
	return contentTop + 6
}

// handleMouse lets the user click a connection row to highlight it.
func (m sshModel) handleMouse(msg tea.MouseMsg) (sshModel, tea.Cmd) {
	if m.mode != sshModeList {
		return m, nil
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}
	anchor := m.listAnchorY()
	nc := m.clientCount()
	if m.hostOK {
		// Client rows (or a single "None yet" placeholder when there are none)
		// start at the anchor.
		if nc > 0 {
			if cidx := msg.Y - anchor; cidx >= 0 && cidx < nc {
				m.cursor = cidx
				return m, nil
			}
		}
		// Consumer remotes are drawn below the client block: the boxes (or the
		// placeholder), then a blank + the "HOSTS THIS MACHINE CONNECTS TO" header
		// + a blank (3 lines). Rare live-only rows can shift this; keyboard ↑/↓ is
		// always exact regardless.
		clientBlock := nc
		if clientBlock == 0 {
			clientBlock = 1
		}
		rAnchor := anchor + clientBlock + 3
		if ridx := msg.Y - rAnchor; ridx >= 0 && ridx < len(m.remotes) {
			m.cursor = nc + ridx
		}
		return m, nil
	}
	// Pure consumer: the anchor points straight at the first remote row.
	if idx := msg.Y - anchor; idx >= 0 && idx < len(m.remotes) {
		m.cursor = idx
	}
	return m, nil
}

func (m sshModel) handleKey(msg tea.KeyMsg) (sshModel, tea.Cmd) {
	switch m.mode {
	case sshModePrint:
		// Any key dismisses the config preview.
		m.mode = sshModeList
		return m, nil

	case sshModeConfirm:
		switch msg.String() {
		case "y", "Y", "enter":
			if c, ok := m.cursorOnClient(); ok {
				return m.beginCapturedSub("Removing "+c.Name, "host", "forget", c.Name)
			}
			if r, ok := m.cursorOnRemote(); ok {
				return m.beginCaptured("Removing "+r.Name, "remove", r.Name)
			}
			m.mode = sshModeList
		case "n", "N", "esc":
			m.mode = sshModeList
		}
		return m, nil

	case sshModeRename:
		return m.handleRenameKey(msg)

	case sshModeJoin:
		return m.handleJoinKey(msg)

	case sshModeShare:
		return m.handleShareKey(msg)

	case sshModeForm:
		return m.handleFormKey(msg)

	case sshModeProgress:
		return m, nil // ignore input while a captured run is in flight

	case sshModeResult:
		if m.twoWayFailed && m.twoWayHost != "" {
			switch msg.String() {
			case "h", "H":
				// Relay tunnel — the real fix for a NAT'd host. Suspends the TUI to
				// run the interactive `auxly host setup` (asks for the relay).
				m.twoWayFailed = false
				return m, runHost("setup")
			case "m", "M":
				// Re-open the method picker for this host, keeping it as the host.
				// Pre-fill OS/host/name from the saved profile so the user only
				// re-chooses the connection method (try VPN/bastion/public).
				name := m.twoWayHost
				for _, r := range m.remotes {
					if r.Name == name {
						m.formName = r.Name
						m.formHost = r.spec()
						m.formOS = r.OS
						m.formMethod = r.Method
						break
					}
				}
				m.twoWayFailed = false
				m.mode = sshModeForm
				m.formStep = formStepMethod
				m.editingHost = true
				return m, nil
			}
		}
		if m.progressNeeded && len(m.pendingKeyArgs) > 0 {
			switch msg.String() {
			case "p", "P":
				// Enter the password right here (PTY-backed key install).
				m.mode = sshModePassword
				m.password = ""
				m.editingHost = true
				return m, nil
			case "k", "K":
				// Fallback: finish key setup in a real terminal.
				args := m.pendingKeyArgs
				sub := m.pendingKeySub
				m.mode = sshModeList
				m.progressNeeded = false
				return m, runSub(sub, args...)
			}
		}
		// A live box was skipped: offer an in-TUI force-update (ends its live
		// session by replacing the binary). Only when the last action was a skip.
		if m.lastUpdateBox != "" && updateResultKind(m.progressOut) == "skipped" {
			switch msg.String() {
			case "f", "F":
				box := m.lastUpdateBox
				return m.beginCapturedSub("Force-updating "+box, "host", "update", box, "--force")
			}
		}
		m.mode = sshModeList // any other key closes the result panel
		m.lastUpdateBox = ""
		return m, nil

	case sshModePassword:
		return m.handlePasswordKey(msg)

	default: // list mode
		// One cursor spans both stacked lists (boxes then remotes); ↑/↓ moves
		// across the whole thing so every row is reachable.
		switch msg.String() {
		case "j", "down":
			if m.cursor < m.listLen()-1 {
				m.cursor++
			}
			return m, nil
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "c":
			m.mode = sshModeForm
			m.formStep = formStepMethod
			m.formOS, m.formMethod, m.formHost, m.formJump, m.formName = "", "", "", "", ""
			m.status = ""
			m.editingHost = true // capture all keys for the in-TUI form
			return m, nil
		case "i":
			// Cycle the pending invite TTL; [I] confirms and mints.
			m.inviteTTLIdx = (m.inviteTTLIdx + 1) % len(inviteTTLPresets)
			m.status = fmt.Sprintf("Invite TTL: %s — press [I] to mint a pairing token (`auxly join <token>`)", inviteTTLPresets[m.inviteTTLIdx])
			return m, nil
		case "I":
			ttl := inviteTTLPresets[m.inviteTTLIdx]
			m.status = ""
			return m.beginCapturedSub("Minting invite ("+ttl+")", "host", "invite", "--ttl", ttl)
		case "J":
			// Uppercase: lowercase "j" is already ↓ in this list. Pairs with
			// [i]/[I] mint on the host side — this is the consumer side of the
			// same pairing flow (paste a token minted by another machine).
			m.joinToken = ""
			m.mode = sshModeJoin
			m.editingHost = true // capture all keys for the paste buffer
			m.status = ""
			return m, nil
		case "y":
			// [c] is already "Connect new" in this list, so the invite-token
			// copy hotkey lives on [y] (yank) instead — see the footer, which
			// only advertises it while a token is actually held.
			if m.inviteToken != "" {
				if err := copyInvite(m.inviteToken); err != nil {
					m.status = "(clipboard unavailable — copy the token above manually)"
				} else {
					m.status = "invite copied ✓"
				}
			}
			return m, nil
		}
		// Row actions dispatch on WHAT the cursor is on, so a machine that is both
		// a host and a consumer can act on either list. Connected boxes (host side):
		if c, ok := m.cursorOnClient(); ok {
			switch msg.String() {
			case "d":
				m.status = ""
				return m.beginCapturedSub("Disconnecting "+c.Name, "host", "disconnect", c.Name)
			case "r":
				m.status = ""
				return m.beginCapturedSub("Reconnecting "+c.Name, "host", "reconnect", c.Name)
			case "e":
				m.rename = c.Name
				m.mode = sshModeRename
				m.editingHost = true
				return m, nil
			case "s":
				return m.openShare(c), nil
			case "u":
				// One-click update (#2): bump this box's auxly over SSH. The host-side
				// command skips it if the box is serving a live session — the result
				// panel then offers [f] to force it (see sshModeResult).
				m.status = ""
				m.lastUpdateBox = c.Name
				return m.beginCapturedSub("Updating "+c.Name, "host", "update", c.Name)
			case "x":
				m.mode = sshModeConfirm
				return m, nil
			}
			return m, nil
		}
		// Consumer remotes (this machine connects TO another host):
		if r, ok := m.cursorOnRemote(); ok {
			switch msg.String() {
			case "t":
				m.status = ""
				return m.beginCaptured("Testing "+r.Name, "test", r.Name)
			case "p", "enter":
				m.preview = mcpConfigJSON(r.Name)
				m.mode = sshModePrint
				return m, nil
			case "d", "x":
				m.mode = sshModeConfirm
				return m, nil
			}
		}
		return m, nil
	}
}

// ── add-host form ─────────────────────────────────────────────────

func (m sshModel) handleFormKey(msg tea.KeyMsg) (sshModel, tea.Cmd) {
	// The permissions step is a tri-state picker, not text entry — route it to its
	// own handler (arrows/space cycle, enter submits, esc cancels the whole form).
	if m.formStep == formStepShare {
		return m.handleFormShareKey(msg)
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = sshModeList
		m.editingHost = false
		return m, nil
	case tea.KeyEnter:
		return m.advanceForm()
	case tea.KeyBackspace, tea.KeyCtrlH:
		m.formTrim()
		return m, nil
	case tea.KeyTab:
		if m.formStep == formStepHost {
			if next := completeHostField(m.formHost); next != "" {
				m.formHost = next
			}
		}
		return m, nil
	case tea.KeySpace:
		m.formAppend(" ")
		return m, nil
	case tea.KeyRunes:
		s := string(msg.Runes)
		if m.formStep == formStepMethod {
			// One decision, three answers. "direct" resolves to lan/public
			// automatically from the address; OS is auto-probed by the doctor —
			// the wizard never asks what the machine can discover itself.
			switch s {
			case "1":
				m.formMethod = "relay"
			case "2":
				m.formMethod = "direct"
			case "3":
				m.formMethod = "bastion"
			default:
				return m, nil
			}
			m.formStep = formStepHost
			return m, nil
		}
		m.formAppend(s)
		return m, nil
	}
	return m, nil
}

func (m sshModel) advanceForm() (sshModel, tea.Cmd) {
	switch m.formStep {
	case formStepMethod:
		if m.formMethod == "" {
			m.formMethod = "direct"
		}
		m.formStep = formStepHost
	case formStepHost:
		if strings.TrimSpace(m.formHost) == "" {
			return m, nil // host/relay is required
		}
		if m.formMethod == "bastion" {
			m.formStep = formStepJump
		} else {
			m.formStep = formStepName // OS is auto-probed; name, then submit
		}
	case formStepJump:
		m.formStep = formStepName
	case formStepName:
		// Relay makes THIS machine the host serving the box, so the next step is choosing
		// what the box may access. Consumer methods only read the remote's memory —
		// there's nothing to share — so they submit straight away.
		if m.formMethod == "relay" {
			m.shareFiles = memory.OrderedFiles()
			m.shareState = defaultShareState(m.shareFiles)
			m.shareCursor = 0
			m.formStep = formStepShare
			return m, nil
		}
		return m.submitForm()
	case formStepShare:
		return m.submitForm()
	}
	return m, nil
}

func (m sshModel) submitForm() (sshModel, tea.Cmd) {
	host := strings.TrimSpace(m.formHost)
	if host == "" {
		m.formStep = formStepHost
		return m, nil
	}
	// Relay method: configure THIS machine as a memory host reachable through a public
	// rendezvous. Runs `auxly host setup` captured in-pane; the result pane shows
	// the `auxly connect use --jump …` command to paste on the remote box.
	if m.formMethod == "relay" {
		m.editingHost = false
		// --provision drives the FULL remote setup from here: install auxly on the
		// box, authorize its key on this machine, and wire its agent — nothing to run
		// on the box.
		args := []string{"setup", "--rendezvous", host, "--yes", "--batch", "--provision"}
		// Stash the interactive (non-batch) variant so that if the relay key
		// isn't installed yet, the result panel can offer [p] to enter the relay
		// password right here instead of dropping to a terminal.
		keyArgs := []string{"setup", "--rendezvous", host, "--yes", "--provision"}
		if name := strings.TrimSpace(m.formName); name != "" {
			args = append(args, "--name", name)
			keyArgs = append(keyArgs, "--name", name)
		}
		m.pendingKeySub = "host"
		m.pendingKeyArgs = keyArgs
		// The permissions step already captured what the box may access (m.shareState).
		// Arm its application: once the box is provisioned (it lands as a new inbound
		// client), write that selection to its clients.yaml entry.
		m.pendingShare = true
		m.pendingShareNm = strings.TrimSpace(m.formName)
		m.preShareNames = clientNameSet(m.clients)
		return m.beginCapturedSub("Setting up relay via "+host, "host", args...)
	}
	// OS is deliberately NOT passed: normalizeOS("") keeps the profile's OS
	// empty so runDoctor's probe persists the real one (persistDetectedOS).
	method := m.formMethod
	if method == "direct" {
		method = "public"
		if privateHostTUI(host) {
			method = "lan"
		}
	}
	base := []string{"add", "--method", method, "--host", host}
	if os := strings.TrimSpace(m.formOS); os != "" {
		// Only the edit/retry flow pre-fills formOS (from the saved profile) —
		// a fresh wizard leaves it empty for the doctor's auto-probe. Without
		// this, retrying a method would wipe a declared OS back to empty.
		base = append(base, "--os", os)
	}
	if name := strings.TrimSpace(m.formName); name != "" {
		base = append(base, "--name", name)
	}
	if jump := strings.TrimSpace(m.formJump); jump != "" {
		base = append(base, "--jump", jump)
	}
	// Stash the non-batch args for the key-setup fallback ([p]/[K] on the result
	// panel), then stream the doctor/setup in-pane via --batch (never prompts here).
	m.pendingKeySub = "connect"
	m.pendingKeyArgs = append([]string{}, base...)
	m.editingHost = false
	return m.beginCaptured("Connecting to "+host, append(base, "--batch")...)
}

func (m *sshModel) formAppend(s string) {
	switch m.formStep {
	case formStepHost:
		m.formHost += s
	case formStepJump:
		m.formJump += s
	case formStepName:
		m.formName += s
	}
}

// handlePasswordKey captures the masked SSH password and, on Enter, runs the
// key install through a PTY (runConnectPTY) so ssh's prompt is answered in-TUI.
func (m sshModel) handlePasswordKey(msg tea.KeyMsg) (sshModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.password = ""
		m.editingHost = false
		m.mode = sshModeResult
		return m, nil
	case tea.KeyEnter:
		if m.password == "" {
			return m, nil // require a password
		}
		pw := m.password
		args := m.pendingKeyArgs
		sub := m.pendingKeySub
		m.password = "" // drop it from the model immediately
		m.editingHost = false
		m.progressNeeded = false
		title := "Installing key & connecting"
		if sub == "host" {
			title = "Installing relay key & connecting"
		}
		return m.beginPTY(title, pw, sub, args...)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if r := []rune(m.password); len(r) > 0 {
			m.password = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeySpace:
		m.password += " "
		return m, nil
	case tea.KeyRunes:
		m.password += string(msg.Runes)
		return m, nil
	}
	return m, nil
}

func (m *sshModel) formTrim() {
	trim := func(s string) string {
		r := []rune(s)
		if len(r) == 0 {
			return s
		}
		return string(r[:len(r)-1])
	}
	switch m.formStep {
	case formStepHost:
		m.formHost = trim(m.formHost)
	case formStepJump:
		m.formJump = trim(m.formJump)
	case formStepName:
		m.formName = trim(m.formName)
	}
}

// handleRenameKey captures the new friendly name and, on Enter, rewrites the
// selected connection's label in clients.yaml.
func (m sshModel) handleRenameKey(msg tea.KeyMsg) (sshModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.rename, m.editingHost, m.mode = "", false, sshModeList
		return m, nil
	case tea.KeyEnter:
		newName := strings.TrimSpace(m.rename)
		old := ""
		if c, ok := m.selectedClient(); ok {
			old = c.Name
		}
		if newName != "" && old != "" && newName != old {
			renameClient(old, newName)
			m.clients = readClients()
			m.cursor = clampCursor(m.cursor, m.listLen())
			m.status = "Renamed to " + newName
		}
		m.rename, m.editingHost, m.mode = "", false, sshModeList
		return m, nil
	case tea.KeyBackspace, tea.KeyCtrlH:
		if r := []rune(m.rename); len(r) > 0 {
			m.rename = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeySpace:
		m.rename += " "
		return m, nil
	case tea.KeyRunes:
		m.rename += string(msg.Runes)
		return m, nil
	}
	return m, nil
}

// renameClient rewrites a connection's friendly label in clients.yaml. The
// target + the consumer-side profile name are unchanged, so disconnect/reconnect
// keep working.
func renameClient(old, newName string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".auxly", "clients.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f clientsYAML
	if yaml.Unmarshal(data, &f) != nil {
		return
	}
	for i := range f.Clients {
		if f.Clients[i].Name == old {
			f.Clients[i].Name = newName
		}
	}
	if out, merr := yaml.Marshal(f); merr == nil {
		_ = os.WriteFile(path, out, 0600)
	}
}

// handleJoinKey captures a pasted invite token and, on Enter, runs `auxly
// join <token>` through the same captured-subprocess pipeline every other
// action on this tab uses (beginCapturedSub) — so the honest success/
// partial/failure distinction cmd/join.go already prints (see
// joinCompletionMessage) shows up verbatim in the result panel, with no
// duplicated logic here.
func (m sshModel) handleJoinKey(msg tea.KeyMsg) (sshModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.joinToken, m.editingHost, m.mode = "", false, sshModeList
		return m, nil
	case tea.KeyEnter:
		tok := strings.TrimSpace(m.joinToken)
		if tok == "" {
			return m, nil // require a token
		}
		m.joinToken, m.editingHost = "", false
		return m.beginCapturedSub("Joining a host", "join", tok)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if r := []rune(m.joinToken); len(r) > 0 {
			m.joinToken = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeySpace:
		m.joinToken += " "
		return m, nil
	case tea.KeyRunes:
		m.joinToken += string(msg.Runes)
		return m, nil
	}
	return m, nil
}

// ─────────────────────────────────────────────────────────────────
//  Per-remote file sharing (§10)
// ─────────────────────────────────────────────────────────────────

// clientNameSet snapshots the set of client names, used to diff the inbound-client
// list before and after an add run so the freshly provisioned box can be found.
func clientNameSet(cs []clientRow) map[string]bool {
	s := make(map[string]bool, len(cs))
	for _, c := range cs {
		s[c.Name] = true
	}
	return s
}

// newlyAddedClient finds the client that appeared after an add run: the first whose
// name wasn't in the pre-run snapshot. Falls back to an exact match on the friendly
// name the user typed (covers a re-add, where the name already existed and the diff
// is empty). Returns ok=false when neither resolves a client.
func newlyAddedClient(cs []clientRow, before map[string]bool, name string) (clientRow, bool) {
	for _, c := range cs {
		if !before[c.Name] {
			return c, true
		}
	}
	if name != "" {
		for _, c := range cs {
			if c.Name == name {
				return c, true
			}
		}
	}
	return clientRow{}, false
}

// defaultShareState seeds the per-file tri-state for a brand-new selection (the
// wizard's permissions step, where no client exists yet): every non-personal file
// Read+Write, the personal tier Off so it's exposed only by a deliberate host choice.
// Read+Write is the default because a connected box is normally a full peer of your
// memory; downgrade any file with ←/→ (or `a` for all-read) before connecting.
func defaultShareState(files []string) map[string]int {
	state := make(map[string]int, len(files))
	for _, f := range files {
		if memory.IsPersonalFile(f) {
			state[f] = shareOff
		} else {
			state[f] = shareReadWrite
		}
	}
	return state
}

// shareSelection splits a tri-state map into the shared (readable) and writable file
// lists clients.yaml stores: Read+Write files appear in both, Read-only in shared
// only, Off in neither. Order follows files so the output is stable.
func shareSelection(files []string, state map[string]int) (shared, writes []string) {
	for _, f := range files {
		switch state[f] {
		case shareReadWrite:
			shared = append(shared, f)
			writes = append(writes, f)
		case shareRead:
			shared = append(shared, f)
		}
	}
	return shared, writes
}

// handleFormShareKey drives the in-wizard permissions step (formStepShare): the same
// tri-state picker as the standalone share modal, but Enter submits the whole form
// (starting the connect) and Esc cancels it. The selection is applied to the box once
// it's provisioned (see the progressEvent done handler).
func (m sshModel) handleFormShareKey(msg tea.KeyMsg) (sshModel, tea.Cmd) {
	cycle := func(forward bool) {
		if m.shareCursor < 0 || m.shareCursor >= len(m.shareFiles) {
			return
		}
		f := m.shareFiles[m.shareCursor]
		m.shareState[f] = cycleShareState(m.shareState[f], forward)
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = sshModeList
		m.editingHost = false
		return m, nil
	case tea.KeyEnter:
		return m.advanceForm() // formStepShare → submitForm
	case tea.KeyUp:
		if m.shareCursor > 0 {
			m.shareCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.shareCursor < len(m.shareFiles)-1 {
			m.shareCursor++
		}
		return m, nil
	case tea.KeyLeft:
		cycle(false)
		return m, nil
	case tea.KeyRight, tea.KeySpace:
		cycle(true)
		return m, nil
	}
	switch msg.String() {
	case "k":
		if m.shareCursor > 0 {
			m.shareCursor--
		}
	case "j":
		if m.shareCursor < len(m.shareFiles)-1 {
			m.shareCursor++
		}
	case "h":
		cycle(false)
	case "l":
		cycle(true)
	case "a": // all non-personal → read-only
		for _, f := range m.shareFiles {
			if !memory.IsPersonalFile(f) {
				m.shareState[f] = shareRead
			}
		}
	case "n": // none
		for _, f := range m.shareFiles {
			m.shareState[f] = shareOff
		}
	}
	return m, nil
}

// openShare builds the file-sharing modal state for the given inbound client and
// returns the updated model. Each file is seeded to Off/Read/Read+Write from the
// client's explicit SharedFiles + WriteFiles (or the §10 default when unset). The
// personal tier defaults to Off but the host may deliberately share it; the modal
// shows an exposure warning so that choice is made consciously.
func (m sshModel) openShare(c clientRow) sshModel {
	files := memory.OrderedFiles()
	sharedSet := map[string]bool{}
	writeSet := map[string]bool{}
	for _, f := range c.SharedFiles {
		sharedSet[f] = true
	}
	for _, f := range c.WriteFiles {
		writeSet[f] = true
	}
	defaulting := len(c.SharedFiles) == 0
	legacyWrite := len(c.WriteFiles) == 0 && strings.EqualFold(strings.TrimSpace(c.Access), "write")

	state := map[string]int{}
	for _, f := range files {
		shared := sharedSet[f]
		if defaulting && !memory.IsPersonalFile(f) {
			// Default share = every non-personal file, read-only. The private tier
			// stays Off so it is only ever exposed by a deliberate host choice.
			shared = true
		}
		switch {
		case !shared:
			state[f] = shareOff
		case writeSet[f] || legacyWrite:
			state[f] = shareReadWrite
		default:
			state[f] = shareRead
		}
	}

	m.shareOpen = true
	m.shareClient = c
	m.shareFiles = files
	m.shareState = state
	m.shareCursor = 0
	m.mode = sshModeShare
	m.editingHost = true // route ALL keys here so digits/letters don't leak to app.go
	m.status = ""
	return m
}

// closeShare clears the modal state and returns to the list.
func (m sshModel) closeShare() sshModel {
	m.shareOpen = false
	m.shareState = nil
	m.shareFiles = nil
	m.shareCursor = 0
	m.editingHost = false
	m.mode = sshModeList
	return m
}

// handleShareKey drives the per-file tri-state sharing modal. ↑/↓ (or j/k) move
// between files; ←/→ (or space) cycle the highlighted file Off → Read →
// Read+Write. Personal-tier rows cycle too — sharing them is the host's call —
// but render with an exposure warning.
func (m sshModel) handleShareKey(msg tea.KeyMsg) (sshModel, tea.Cmd) {
	cycle := func(forward bool) {
		if m.shareCursor < 0 || m.shareCursor >= len(m.shareFiles) {
			return
		}
		f := m.shareFiles[m.shareCursor]
		m.shareState[f] = cycleShareState(m.shareState[f], forward)
	}
	switch msg.Type {
	case tea.KeyEsc:
		return m.closeShare(), nil
	case tea.KeyEnter:
		return m.saveShare()
	case tea.KeyUp:
		if m.shareCursor > 0 {
			m.shareCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.shareCursor < len(m.shareFiles)-1 {
			m.shareCursor++
		}
		return m, nil
	case tea.KeyLeft:
		cycle(false)
		return m, nil
	case tea.KeyRight, tea.KeySpace:
		cycle(true)
		return m, nil
	}
	switch msg.String() {
	case "k":
		if m.shareCursor > 0 {
			m.shareCursor--
		}
	case "j":
		if m.shareCursor < len(m.shareFiles)-1 {
			m.shareCursor++
		}
	case "h":
		cycle(false)
	case "l":
		cycle(true)
	case "a": // all non-personal → read-only
		for _, f := range m.shareFiles {
			if !memory.IsPersonalFile(f) {
				m.shareState[f] = shareRead
			}
		}
	case "n": // none
		for _, f := range m.shareFiles {
			m.shareState[f] = shareOff
		}
	}
	return m, nil
}

// saveShare writes the modal's selection back to ~/.auxly/clients.yaml (round-trip
// safe: only the matching entry's shared_files + access are mutated) and reloads
// the in-memory client list.
func (m sshModel) saveShare() (sshModel, tea.Cmd) {
	shared, writes := shareSelection(m.shareFiles, m.shareState)
	name := m.shareClient.Name
	saveClientSharing(m.shareClient, shared, writes)
	m.clients = readClients()
	m.cursor = clampCursor(m.cursor, m.listLen())
	m.status = "Updated sharing for " + name
	return m.closeShare(), nil
}

// saveClientSharing updates one inbound client's shared_files + write_files in
// clients.yaml, preserving every other field and entry untouched. The match is by
// Target first (stable), falling back to Name. The legacy global access flag is
// pinned to "read" so an older auxly that ignores write_files fails closed
// (read-only) rather than granting blanket writes.
func saveClientSharing(c clientRow, shared, writes []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".auxly", "clients.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// Round-trip via yaml.Node so unknown-to-us keys on each entry survive.
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return
	}
	root := doc.Content[0]
	clientsSeq := mappingValue(root, "clients")
	if clientsSeq == nil || clientsSeq.Kind != yaml.SequenceNode {
		return
	}
	for _, entry := range clientsSeq.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		target := scalarValue(entry, "target")
		name := scalarValue(entry, "name")
		match := (c.Target != "" && target == c.Target) || (c.Target == "" && name == c.Name)
		if !match {
			continue
		}
		setSequenceField(entry, "shared_files", shared)
		setSequenceField(entry, "write_files", writes)
		setScalarField(entry, "access", "read")
		break
	}
	out, merr := yaml.Marshal(&doc)
	if merr != nil {
		return
	}
	_ = os.WriteFile(path, out, 0600)
}

// mappingValue returns the value node for key within a mapping node, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// scalarValue returns the string value for key in a mapping node, or "".
func scalarValue(m *yaml.Node, key string) string {
	if v := mappingValue(m, key); v != nil {
		return v.Value
	}
	return ""
}

// setScalarField sets (or inserts) a scalar key on a mapping node.
func setScalarField(m *yaml.Node, key, val string) {
	if v := mappingValue(m, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = "!!str"
		v.Value = val
		v.Content = nil
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val},
	)
}

// setSequenceField sets (or inserts) a string-sequence key on a mapping node.
func setSequenceField(m *yaml.Node, key string, vals []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range vals {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	if v := mappingValue(m, key); v != nil {
		*v = *seq
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
}

// ─────────────────────────────────────────────────────────────────
//  View
// ─────────────────────────────────────────────────────────────────

func (m sshModel) View() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	accent := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

	width := m.width
	if width <= 0 {
		width = 80
	}
	bodyWidth := width - 8
	if bodyWidth < 44 {
		bodyWidth = 44
	}

	var lines []string
	lines = append(lines, cyan.Render("REMOTE MEMORY OVER SSH"))
	lines = append(lines, "")
	// Single line (deterministic height for mouse mapping; clamped by finalizer).
	lines = append(lines, "Use your Auxly memory on another machine over SSH — no daemon, no open port, no token.")
	lines = append(lines, dim.Render("Press ")+accent.Render("c")+dim.Render(" to connect — choose how the machines reach each other; Auxly wires the rest."))
	lines = append(lines, "")

	selRow := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

	if m.hostOK {
		// ── This machine as a HOST + the boxes connected to it ───────
		ok := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess)
		warn := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
		relay := m.host.Rendezvous
		if m.host.RendezvousPort != 0 && m.host.RendezvousPort != 22 {
			relay = fmt.Sprintf("%s:%d", relay, m.host.RendezvousPort)
		}
		status := ok.Render("● serving")
		if !m.host.TunnelUp {
			status = warn.Render("● tunnels down")
		}
		lines = append(lines, cyan.Render("THIS MACHINE IS A MEMORY HOST")+"  "+status)
		if m.host.RelayCount > 1 {
			lines = append(lines, "  "+dim.Render(fmt.Sprintf("Relays  %d boxes", m.host.RelayCount))+dim.Render("   each tunnels back to this machine (independent)"))
		} else {
			lines = append(lines, "  "+dim.Render("Relay   ")+relay+dim.Render(fmt.Sprintf("   reverse port %d → local :22", m.host.ReversePort)))
		}
		if !m.host.TunnelUp {
			lines = append(lines, "  "+warn.Render("⚠ no reverse tunnel running")+dim.Render(" — boxes can't reach your memory. Start it with ")+accent.Render("auxly host up"))
		}
		lines = append(lines, "  "+dim.Render("Boxes below use your memory through it.  Down it with ")+accent.Render("auxly host down"))
		lines = append(lines, "")
	}

	// ── Boxes connected to this machine (clients.yaml) ───────────────
	// Rendered whenever clients are configured, even if the host tunnel is
	// currently down — so a "deleted" box never silently lingers unseen, and
	// stays selectable for removal.
	if m.hostOK || len(m.clients) > 0 {
		lines = append(lines, cyan.Render("CONNECTED BOXES")+dim.Render("   (using your memory)"))
		if !m.hostOK && len(m.clients) > 0 {
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(ColorWarning).Render("⚠ host tunnel down")+dim.Render(" — these boxes can't reach your memory until you run ")+accent.Render("auxly host setup"))
		}
		lines = append(lines, "")
		if len(m.clients) == 0 {
			lines = append(lines, "  "+dim.Render("None yet — press ")+accent.Render("c")+dim.Render(" and pick 'relay' to connect a box."))
		} else {
			// A box is "live" when one of its agents currently holds an SSH-remote
			// session through this host (ground truth from the session registry).
			green := lipgloss.NewStyle().Foreground(ColorSuccess)
			ok := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess) // write-access permission style
			liveBox := map[string]bool{}
			type liveRemote struct{ host, provider string }
			var lives []liveRemote
			for _, s := range gatherSessions() {
				if s.Remote && s.Host != "" {
					liveBox[strings.ToLower(s.Host)] = true
					lives = append(lives, liveRemote{s.Host, s.Provider})
				}
			}
			boxKeys := map[string]bool{}
			for _, c := range m.clients {
				boxKeys[strings.ToLower(c.Name)] = true
				boxKeys[strings.ToLower(targetHost(c.Target))] = true
				if c.Hostname != "" {
					boxKeys[strings.ToLower(c.Hostname)] = true
				}
			}
			for i, c := range m.clients {
				name := c.Name
				if name == "" {
					name = "(unnamed)"
				}
				dot := dim.Render("○")
				// Pad the status as plain text BEFORE styling so columns align.
				status := dim.Render(fmt.Sprintf("%-9s", "idle"))
				if boxIsLive(liveBox, c) {
					dot = green.Render("●")
					status = green.Render(fmt.Sprintf("%-9s", "connected"))
				}
				// Permission column (#5): effective memory access for this box.
				permText, isWrite := permissionLabel(c, m.defaultWrite)
				permStyle := dim
				if isWrite {
					permStyle = ok
				}
				perm := permStyle.Render(fmt.Sprintf("%-13s", permText))
				row := fmt.Sprintf("%s %-18s %-22s %s %s %s", dot, truncate(name, 18), truncate(c.Target, 22), status, dim.Render(fmt.Sprintf("%-7s", "["+c.Method+"]")), perm)
				// Version cell (#1) from the async sweep: shows each box's actual
				// version — "✓ 1.0.9" when current, "1.0.0 ⬆1.0.9" when behind.
				if st, ok2 := m.versions[strings.ToLower(c.Name)]; ok2 {
					if text, kind := versionCell(st); text != "" {
						vs := dim
						switch kind {
						case "outdated":
							vs = lipgloss.NewStyle().Foreground(ColorWarning)
						case "current":
							vs = green
						}
						row += " " + vs.Render(text)
					}
					// Link cell from the health sweep: the selftest's verdict is
					// the one signal that means "this box can actually read the
					// memory right now" — config says nothing, the probe proves it.
					if text, kind := linkCell(st); text != "" {
						ls := dim
						switch kind {
						case "ok":
							ls = green
						case "slow":
							ls = lipgloss.NewStyle().Foreground(ColorWarning)
						case "fail":
							ls = lipgloss.NewStyle().Foreground(ColorDanger)
						}
						row += " " + ls.Render(text)
					}
				}
				if i == m.cursor {
					lines = append(lines, accent.Render("▸ ")+selRow.Render(row))
				} else {
					lines = append(lines, "  "+row)
				}
			}
			// Live SSH-remote sessions whose self-reported hostname doesn't match a
			// configured box name (e.g. box "BOX1" connecting as "node-a").
			for _, lr := range lives {
				if boxKeys[strings.ToLower(lr.host)] {
					continue
				}
				lines = append(lines, "  "+green.Render("● ")+lipgloss.NewStyle().Bold(true).Render(truncate(lr.host, 18))+"  "+dim.Render(lr.provider+" · connected"))
			}
		}
		lines = append(lines, "")
	}

	// ── This machine as a CONSUMER of other hosts (remotes.yaml) ───
	if !m.hostOK || len(m.remotes) > 0 {
		lines = append(lines, cyan.Render("HOSTS THIS MACHINE CONNECTS TO"))
		lines = append(lines, "")
		if len(m.remotes) == 0 {
			lines = append(lines, "  "+dim.Render("No remotes yet — press ")+accent.Render("c")+dim.Render(" to connect to a memory host."))
		} else {
			for i, r := range m.remotes {
				name := r.Name
				if name == "" {
					name = "(unnamed)"
				}
				target := r.Host
				if r.User != "" {
					target = r.User + "@" + r.Host
				}
				if r.Port != 0 && r.Port != 22 {
					target = fmt.Sprintf("%s:%d", target, r.Port)
				}
				method := r.Method
				if method == "" {
					method = "—"
				}
				row := fmt.Sprintf("%-20s %-24s %s", truncate(name, 20), truncate(target, 24), dim.Render("["+method+"]"))
				// Highlight when the unified cursor lands on this remote (the slots
				// after the connected boxes).
				if i == m.cursor-m.clientCount() {
					lines = append(lines, accent.Render("▸ ")+selRow.Render(row))
				} else {
					lines = append(lines, "  "+row)
				}
			}
		}
		lines = append(lines, "")
	}

	// ── Modal area: confirm / print / action bar ───────────────────
	lines = append(lines, dim.Render(strings.Repeat("─", bodyWidth)))
	switch m.mode {
	case sshModeConfirm:
		warn := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
		what := m.selectedName()
		verb := "Remove remote"
		if c, ok := m.selectedClient(); ok && len(m.clients) > 0 {
			what, verb = c.Name, "Disconnect + remove"
		}
		lines = append(lines, warn.Render(fmt.Sprintf("%s %q?  ", verb, what))+
			accent.Render("[y]")+dim.Render(" yes   ")+accent.Render("[n]")+dim.Render(" cancel"))
	case sshModeRename:
		lines = append(lines, cyan.Render("RENAME CONNECTION")+dim.Render("   Enter: save · esc: cancel"))
		lines = append(lines, "")
		lines = append(lines, "  "+dim.Render("New name:  ")+m.rename+accent.Render("▌"))
	case sshModeJoin:
		lines = append(lines, cyan.Render("JOIN A HOST")+dim.Render("   Enter: join · esc: cancel"))
		lines = append(lines, "")
		lines = append(lines, dim.Render("  Paste the invite token from ")+accent.Render("auxly host invite")+dim.Render(" on the other machine:"))
		lines = append(lines, "")
		lines = append(lines, "  "+m.joinToken+accent.Render("▌"))
		lines = append(lines, "")
		lines = append(lines, dim.Render("  This machine must already have a working SSH login to the host."))
	case sshModeShare:
		warn := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
		ok := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess)
		target := m.shareClient.Target
		if target == "" {
			target = m.shareClient.Hostname
		}
		name := m.shareClient.Name
		if name == "" {
			name = "(unnamed)"
		}
		title := "Share files with: " + name
		if target != "" {
			title += " (" + target + ")"
		}
		lines = append(lines, cyan.Render(title))
		lines = append(lines, dim.Render("  Use ")+accent.Render("←/→")+dim.Render(" to set each file:  ")+
			dim.Render("Off")+dim.Render(" → ")+accent.Render("Read")+dim.Render(" → ")+ok.Render("Read+Write"))
		// Caution line — emphasized while the private tier is actually shared.
		if m.shareState["personal.md"] != shareOff {
			lines = append(lines, "  "+warn.Render("⚠ personal.md is SHARED — your private life (family, health, finances) will be exposed to "+name))
		} else {
			lines = append(lines, "  "+dim.Render("Heads-up: personal.md is private and off by default — sharing it exposes your personal info to the remote machine."))
		}
		lines = append(lines, "")
		// Per-file tri-state list.
		for i, f := range m.shareFiles {
			label := fmt.Sprintf("%-16s", f)
			var badge string
			switch m.shareState[f] {
			case shareReadWrite:
				badge = ok.Render("● Read + Write")
			case shareRead:
				badge = accent.Render("◐ Read only  ")
			default:
				badge = dim.Render("○ Off        ")
			}
			row := badge + "  " + label
			if memory.IsPersonalFile(f) {
				if m.shareState[f] == shareOff {
					row += "  " + dim.Render("private — off by default")
				} else {
					row += "  " + warn.Render("⚠ EXPOSES YOUR PRIVATE LIFE")
				}
			}
			if i == m.shareCursor {
				lines = append(lines, accent.Render("▸ ")+selRow.Render(row))
			} else {
				lines = append(lines, "  "+row)
			}
		}
		lines = append(lines, "")
		lines = append(lines, accent.Render("←/→")+dim.Render(" cycle access · ")+accent.Render("↑/↓")+dim.Render(" move · a all-read · n none · ")+accent.Render("enter")+dim.Render(" save · esc cancel"))
	case sshModeProgress:
		spin := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render(spinnerFrame(m.spin))
		lines = append(lines, spin+"  "+cyan.Render(m.progressTitle))
		bar := renderLoadingBar(m.progressPct, 30, m.spin, ColorPrimary) // glint shows live activity at the ceiling
		lines = append(lines, "  "+bar+dim.Render(fmt.Sprintf("  %3d%%", m.progressPct)))
		lines = append(lines, "")
		tail := m.progressOut
		start := 0
		if len(tail) > 7 {
			start = len(tail) - 7
		}
		if len(tail) == 0 {
			lines = append(lines, dim.Render("   starting…"))
		}
		for i := start; i < len(tail); i++ {
			// Wrap each captured line to the panel width so a long command line
			// (e.g. the ssh-copy-id hint) can't push the bordered box past the
			// terminal width and mangle the whole layout.
			for _, wl := range wrapText(tail[i], bodyWidth-3) {
				if i == len(tail)-1 {
					lines = append(lines, "   "+wl) // current line, in its own colors
				} else {
					lines = append(lines, "   "+dim.Render(wl))
				}
			}
		}
	case sshModeResult:
		// The relay/host setup can exit 0 having only *saved config* while still
		// needing a manual key-install step. Detect that so we don't claim "Done"
		// when the connection isn't actually live yet.
		actionNeeded := false
		for _, l := range m.progressOut {
			if strings.Contains(l, "isn't set up yet") ||
				strings.Contains(l, "host setup`") ||
				strings.Contains(l, "host setup' ") {
				actionNeeded = true
				break
			}
		}
		// An update action exits 0 even when a box is SKIPPED (live) or already
		// current, so a blanket "✓ Done" would misread a skip as a successful
		// update. Derive the real outcome from the command's output markers.
		updateOutcome := updateResultKind(m.progressOut)
		head := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("✓ Done")
		switch {
		case !m.progressOK:
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render("Finished with issues")
		case updateOutcome == "failed":
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorDanger).Render("✗ Update failed")
		case updateOutcome == "skipped":
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render("⏭ Skipped — box is live, NOT updated")
		case updateOutcome == "updated":
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("✓ Updated")
		case updateOutcome == "current":
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("✓ Already up to date")
		case actionNeeded:
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render("⚠ One more step — not connected yet")
		}
		lines = append(lines, head+dim.Render("  ("+m.progressTitle+")"))
		out := m.progressOut
		if len(out) > 12 {
			lines = append(lines, dim.Render(fmt.Sprintf("  … %d earlier lines", len(out)-12)))
			out = out[len(out)-12:]
		}
		for _, l := range out {
			// Wrap to panel width — long captured lines must not widen the box.
			for _, wl := range wrapText(l, bodyWidth-2) {
				lines = append(lines, "  "+wl)
			}
		}
		lines = append(lines, "")
		switch {
		case m.twoWayFailed && m.twoWayHost != "":
			lines = append(lines, accent.Render("[h]")+dim.Render(" set up the relay tunnel on this machine (recommended for a NAT'd host)"))
			lines = append(lines, accent.Render("[m]")+dim.Render(" try a different connection method   ·   any other key: close"))
		case m.progressNeeded && len(m.pendingKeyArgs) > 0:
			pwLabel := " enter SSH password here   "
			if m.pendingKeySub == "host" {
				pwLabel = " enter the relay password here   "
			}
			lines = append(lines, accent.Render("[p]")+dim.Render(pwLabel)+accent.Render("[K]")+dim.Render(" use a terminal instead   ·   any other key: close"))
		case actionNeeded:
			lines = append(lines, accent.Render("Next:")+dim.Render(" run ")+accent.Render("auxly host setup")+dim.Render(" in a terminal to install the relay key, then reconnect."))
			lines = append(lines, dim.Render("Press any key to close."))
		case m.lastUpdateBox != "" && updateOutcome == "skipped":
			lines = append(lines, accent.Render("[f]")+dim.Render(" force the update now ")+lipgloss.NewStyle().Foreground(ColorWarning).Render("(ends "+m.lastUpdateBox+"'s live session)")+dim.Render("   ·   any other key: close"))
		default:
			lines = append(lines, dim.Render("Press any key to close."))
		}
	case sshModePassword:
		host := "the host"
		for i, a := range m.pendingKeyArgs {
			if a == "--host" && i+1 < len(m.pendingKeyArgs) {
				host = m.pendingKeyArgs[i+1]
			}
		}
		lines = append(lines, cyan.Render("SSH PASSWORD")+dim.Render("  (one-time — installs your key, then key auth is used)"))
		lines = append(lines, "")
		dots := strings.Repeat("•", len([]rune(m.password)))
		lines = append(lines, "  "+dim.Render("Password for ")+host+dim.Render(":  ")+dots+accent.Render("▌"))
		lines = append(lines, "")
		lines = append(lines, dim.Render("  Enter: submit · esc: cancel · the password is sent to ssh and not stored"))
	case sshModeForm:
		boldAccent := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
		primary := lipgloss.NewStyle().Foreground(ColorPrimary)
		check := lipgloss.NewStyle().Foreground(ColorSuccess).Render("✓")
		star := lipgloss.NewStyle().Foreground(ColorWarning).Render(" ★")
		caret := accent.Render("▌")

		lines = append(lines, cyan.Render("CONNECT MEMORY")+dim.Render("     esc cancels  ·  ↵ next / save"))
		lines = append(lines, "")

		// ── Step 1 · Method (the key decision, shown first) ──────────────
		methodOpts := []struct{ key, val, desc string }{
			{"1", "relay", "Serve THIS machine to a NAT'd / shared box"},
			{"2", "direct", "Direct over SSH — LAN/VPN/public picked automatically"},
			{"3", "bastion", "Through a jump host / bastion gateway"},
		}
		if m.formStep == formStepMethod {
			lines = append(lines, "  "+boldAccent.Render("How does this machine reach the box?")+dim.Render("   press 1–3"))
			for _, o := range methodOpts {
				marker, name, desc := dim.Render("○"), dim.Render(fmt.Sprintf("%-8s", o.val)), dim.Render(o.desc)
				if m.formMethod == o.val {
					marker, name, desc = accent.Render("●"), boldAccent.Render(fmt.Sprintf("%-8s", o.val)), primary.Render(o.desc)
				}
				row := "   " + marker + " " + dim.Render(o.key) + "  " + name + " " + desc
				if o.val == "relay" {
					row += star
				}
				lines = append(lines, row)
			}
		} else {
			d := ""
			for _, o := range methodOpts {
				if o.val == m.formMethod {
					d = o.desc
				}
			}
			lines = append(lines, "  "+check+" "+dim.Render("Method   ")+boldAccent.Render(m.formMethod)+dim.Render("   "+d))
		}
		lines = append(lines, "")

		// ── Step 2 · Host / Relay address ────────────────────────────────
		isRelay := m.formMethod == "relay"
		hostTitle, hostHintTxt := "Host address", "[user@]host[:port] — the machine to reach"
		if isRelay {
			hostTitle, hostHintTxt = "Relay server", "[user@]host[:port] — a public box this machine dials out to"
		}
		switch {
		case m.formStep == formStepHost:
			lines = append(lines, "  "+boldAccent.Render(hostTitle)+dim.Render("   "+hostHintTxt))
			lines = append(lines, "   "+m.formHost+caret)
			if hint := hostFieldHint(m.formHost); hint != "" {
				lines = append(lines, "   "+dim.Render(hint))
			}
		case strings.TrimSpace(m.formHost) != "":
			lines = append(lines, "  "+check+" "+dim.Render(hostTitle+"   ")+m.formHost)
		default:
			lines = append(lines, "  "+dim.Render("· "+hostTitle))
		}

		// ── Step 2b · Jump host (bastion only) ───────────────────────────
		if m.formMethod == "bastion" {
			switch {
			case m.formStep == formStepJump:
				lines = append(lines, "  "+boldAccent.Render("Jump host")+dim.Render("   [user@]gateway"))
				lines = append(lines, "   "+m.formJump+caret)
			case m.formJump != "":
				lines = append(lines, "  "+check+" "+dim.Render("Jump host   ")+m.formJump)
			}
		}

		// ── Step 4 · Friendly name (all methods) ─────────────────────────
		nameHint := "a label for this host (optional)"
		if isRelay {
			nameHint = "a friendly name for this connection (optional)"
		}
		switch {
		case m.formStep == formStepName:
			lines = append(lines, "  "+boldAccent.Render("Name")+dim.Render("   "+nameHint))
			shown := m.formName
			if shown == "" {
				shown = dim.Render("(defaults to host)")
			}
			lines = append(lines, "   "+shown+caret)
		case m.formName != "" || m.formStep == formStepShare:
			shownName := m.formName
			if shownName == "" {
				shownName = dim.Render("(defaults to host)")
			}
			lines = append(lines, "  "+check+" "+dim.Render("Name   ")+shownName)
		}

		// ── Step 5 · Permissions (relay only): what this box may access ──
		if m.formStep == formStepShare {
			warnS := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
			okS := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess)
			lines = append(lines, "")
			lines = append(lines, "  "+boldAccent.Render("What can this box access?")+dim.Render("   ←/→ cycle  Off · Read · Read+Write"))
			if m.shareState["personal.md"] != shareOff {
				lines = append(lines, "  "+warnS.Render("⚠ personal.md is SHARED — exposes your private life (family, health, finances)"))
			} else {
				lines = append(lines, "  "+dim.Render("personal.md is private and Off by default — share it only on purpose."))
			}
			for i, f := range m.shareFiles {
				var badge string
				switch m.shareState[f] {
				case shareReadWrite:
					badge = okS.Render("● Read + Write")
				case shareRead:
					badge = accent.Render("◐ Read only  ")
				default:
					badge = dim.Render("○ Off        ")
				}
				row := badge + "  " + fmt.Sprintf("%-16s", f)
				if memory.IsPersonalFile(f) && m.shareState[f] != shareOff {
					row += "  " + warnS.Render("⚠ private")
				}
				if i == m.shareCursor {
					lines = append(lines, accent.Render("▸ ")+selRow.Render(row))
				} else {
					lines = append(lines, "  "+row)
				}
			}
			lines = append(lines, "")
			lines = append(lines, accent.Render("↑/↓")+dim.Render(" move · ")+accent.Render("←/→")+dim.Render(" access · ")+
				accent.Render("a")+dim.Render(" all-read · ")+accent.Render("n")+dim.Render(" none · ")+
				accent.Render("enter")+dim.Render(" connect · esc cancel"))
		}

		// ── What the relay flow will do (set expectations) ───────────────
		if isRelay && m.formStep != formStepShare {
			lines = append(lines, "")
			lines = append(lines, dim.Render("  → Opens a reverse tunnel from this machine, then installs auxly on the"))
			lines = append(lines, dim.Render("    relay box and wires its agent to your memory. Nothing to run there."))
			lines = append(lines, dim.Render("  → Next: pick which memory files it may read or read+write, then connect."))
		}
	case sshModePrint:
		lines = append(lines, cyan.Render(fmt.Sprintf("MCP config for %q", m.selectedName()))+dim.Render("  (paste into your IDE)"))
		for _, l := range strings.Split(m.preview, "\n") {
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(ColorSuccess).Render(l))
		}
		lines = append(lines, "")
		lines = append(lines, dim.Render("Press any key to close."))
	default:
		action := func(k, label string) string { return accent.Render("["+k+"]") + dim.Render(" "+label) }
		var bar []string
		// The action bar reflects WHAT the cursor is on so both lists stay usable.
		if _, onRemote := m.cursorOnRemote(); onRemote {
			// A consumer remote (this machine → another host).
			bar = []string{
				action("c", "Connect new"),
				action("t", "Test"),
				action("p", "Print config"),
				action("d", "Remove"),
				action("i/I", "Invite a box"),
				action("J", "Join a host"),
			}
		} else if m.clientCount() > 0 {
			// A connected box (host side).
			bar = []string{
				action("c", "Connect new"),
				action("d", "Disconnect"),
				action("r", "Reconnect"),
				action("e", "Rename"),
				action("s", "Share files"),
				action("u", "Update"),
				action("x", "Remove"),
				action("i/I", "Invite a box"),
				action("J", "Join a host"),
			}
		} else {
			bar = []string{
				action("c", "Connect new"),
				action("t", "Test"),
				action("p", "Print config"),
				action("d", "Remove"),
				action("i/I", "Invite a box"),
				action("J", "Join a host"),
			}
		}
		if m.inviteToken != "" {
			bar = append(bar, action("y", "Copy invite"))
		}
		lines = append(lines, strings.Join(bar, dim.Render("   ")))
		switch {
		case m.status != "":
			lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Render(m.status))
		case m.clientCount() > 0 && len(m.remotes) > 0:
			lines = append(lines, dim.Render("Select with ")+accent.Render("↑/↓")+dim.Render(" — boxes you host and hosts you use. Restart that box's agent after changes."))
		case m.clientCount() > 0:
			lines = append(lines, dim.Render("Select a box with ")+accent.Render("↑/↓")+dim.Render(" or a click, then act. Restart that box's agent after changes."))
		default:
			lines = append(lines, dim.Render("`auxly connect` in a terminal does the same — this tab is a front-end for it."))
		}
	}

	var padded []string
	for _, line := range lines {
		padded = append(padded, padLine(clampLine(line, bodyWidth), bodyWidth))
	}
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Render(strings.Join(padded, "\n"))

	return panel + "\n\n"
}
