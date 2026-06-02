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

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

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
)

// form steps.
const (
	formStepOS     = "os"
	formStepMethod = "method"
	formStepHost   = "host"
	formStepJump   = "jump"
	formStepName   = "name"
)

type sshModel struct {
	remotes []remoteEntry
	clients []clientRow // remote boxes wired to use THIS Mac (host side) — managed list
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

	// per-remote file-sharing modal state (mode == sshModeShare, §10)
	shareOpen   bool           // modal visible
	shareClient clientRow      // the inbound client being edited
	shareFiles  []string       // rows, in taxonomy order
	shareState  map[string]int // file → shareOff | shareRead | shareReadWrite
	shareCursor int            // highlighted row within the modal
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
	return sshModel{remotes: readRemotes(), clients: readClients(), host: h, hostOK: ok}
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
}

// clientRow mirrors an entry in ~/.auxly/clients.yaml — a remote box this Mac
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
		return h, true
	}
	// Legacy single-relay form.
	var h hostInfo
	if yaml.Unmarshal(data, &h) != nil || strings.TrimSpace(h.Rendezvous) == "" {
		return hostInfo{}, false
	}
	h.RelayCount = 1
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
	if m.hostOK {
		return len(m.clients)
	}
	return 0
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
// when the host can't dial back to a NAT'd Mac.
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

func (m sshModel) beginPTY(title, password, sub string, args ...string) (sshModel, tea.Cmd) {
	if sub == "" {
		sub = "connect"
	}
	ch := make(chan progressEvent, 128)
	startPTYRun(ch, password, sub, args...)
	return m.beginRun(title, ch)
}

// milestonePct maps a streamed line to a coarse completion percentage so the bar
// advances through the recognisable doctor/setup stages.
func milestonePct(line string) int {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "configured") || strings.Contains(l, "injected") || strings.Contains(l, "restart your") || strings.Contains(l, "onboard"):
		return 95
	case strings.Contains(l, "saved remote profile"):
		return 85
	case strings.Contains(l, "auxly present on host") || strings.Contains(l, "auxly installed"):
		return 75
	case strings.Contains(l, "installing"):
		return 55
	case strings.Contains(l, "host reachable"):
		return 45
	case strings.Contains(l, "local ssh client"):
		return 25
	case strings.Contains(l, "doctor"):
		return 10
	}
	return 0
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
	filled := pct * width / 100
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
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
			m.progressPct = 100
			m.progressCh = nil
			m.mode = sshModeResult
			m.remotes = readRemotes() // reload lists behind the result panel
			m.clients = readClients()
			m.host, m.hostOK = readHostInfo()
			m.cursor = clampCursor(m.cursor, m.listLen())
			return m, nil
		}
		if line := strings.TrimRight(msg.line, "\r"); strings.TrimSpace(line) != "" {
			m.progressLast = line
			m.progressOut = append(m.progressOut, line)
			if p := milestonePct(line); p > m.progressPct {
				m.progressPct = p
			}
		}
		if m.progressCh != nil {
			return m, waitProgress(m.progressCh)
		}
		return m, nil

	case sshSpinTickMsg:
		if m.mode == sshModeProgress {
			m.spin++
			return m, spinTick()
		}
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
			m.cursor = clampCursor(m.cursor, m.listLen())
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
				// Relay tunnel — the real fix for a NAT'd Mac. Suspends the TUI to
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
		m.mode = sshModeList // any other key closes the result panel
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
	case tea.KeySpace:
		m.formAppend(" ")
		return m, nil
	case tea.KeyRunes:
		s := string(msg.Runes)
		if m.formStep == formStepMethod {
			// Method is the first, most important choice. 5 = relay (this Mac is the
			// host, served to a NAT'd/shared box); the rest reach an external host.
			switch s {
			case "1":
				m.formMethod = "lan"
			case "2":
				m.formMethod = "vpn"
			case "3":
				m.formMethod = "bastion"
			case "4":
				m.formMethod = "public"
			case "5":
				m.formMethod = "relay"
			default:
				return m, nil
			}
			m.formStep = formStepHost
			return m, nil
		}
		if m.formStep == formStepOS {
			switch s {
			case "1":
				m.formOS = "linux"
			case "2":
				m.formOS = "darwin"
			case "3":
				m.formOS = "windows"
			default:
				return m, nil
			}
			m.formStep = formStepName
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
			m.formMethod = "public"
		}
		m.formStep = formStepHost
	case formStepHost:
		if strings.TrimSpace(m.formHost) == "" {
			return m, nil // host/relay is required
		}
		if m.formMethod == "relay" {
			m.formStep = formStepName // name the connection, then submit
		} else if m.formMethod == "bastion" {
			m.formStep = formStepJump
		} else {
			m.formStep = formStepOS
		}
	case formStepJump:
		m.formStep = formStepOS
	case formStepOS:
		if m.formOS == "" {
			m.formOS = "linux"
		}
		m.formStep = formStepName
	case formStepName:
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
	// Relay method: configure THIS Mac as a memory host reachable through a public
	// rendezvous. Runs `auxly host setup` captured in-pane; the result pane shows
	// the `auxly connect use --jump …` command to paste on the remote box.
	if m.formMethod == "relay" {
		m.editingHost = false
		// --provision drives the FULL remote setup from here: install auxly on the
		// box, authorize its key on this Mac, and wire its agent — nothing to run
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
		return m.beginCapturedSub("Setting up relay via "+host, "host", args...)
	}
	osTarget := m.formOS
	if osTarget == "" {
		osTarget = "linux"
	}
	base := []string{"add", "--os", osTarget, "--method", m.formMethod, "--host", host}
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
		_ = os.WriteFile(path, out, 0644)
	}
}

// ─────────────────────────────────────────────────────────────────
//  Per-remote file sharing (§10)
// ─────────────────────────────────────────────────────────────────

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
	shared := make([]string, 0, len(m.shareFiles))
	writes := make([]string, 0, len(m.shareFiles))
	for _, f := range m.shareFiles {
		switch m.shareState[f] {
		case shareReadWrite:
			shared = append(shared, f)
			writes = append(writes, f)
		case shareRead:
			shared = append(shared, f)
		}
	}
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
	_ = os.WriteFile(path, out, 0644)
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
		relay := m.host.Rendezvous
		if m.host.RendezvousPort != 0 && m.host.RendezvousPort != 22 {
			relay = fmt.Sprintf("%s:%d", relay, m.host.RendezvousPort)
		}
		lines = append(lines, cyan.Render("THIS MACHINE IS A MEMORY HOST")+"  "+ok.Render("● serving"))
		if m.host.RelayCount > 1 {
			lines = append(lines, "  "+dim.Render(fmt.Sprintf("Relays  %d boxes", m.host.RelayCount))+dim.Render("   each tunnels back to this machine (independent)"))
		} else {
			lines = append(lines, "  "+dim.Render("Relay   ")+relay+dim.Render(fmt.Sprintf("   reverse port %d → local :22", m.host.ReversePort)))
		}
		lines = append(lines, "  "+dim.Render("Boxes below use your memory through it.  Down it with ")+accent.Render("auxly host down"))
		lines = append(lines, "")
		lines = append(lines, cyan.Render("CONNECTED BOXES")+dim.Render("   (using your memory)"))
		lines = append(lines, "")
		if len(m.clients) == 0 {
			lines = append(lines, "  "+dim.Render("None yet — press ")+accent.Render("c")+dim.Render(" and pick 'relay' to connect a box."))
		} else {
			// A box is "live" when one of its agents currently holds an SSH-remote
			// session through this host (ground truth from the session registry).
			green := lipgloss.NewStyle().Foreground(ColorSuccess)
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
				row := fmt.Sprintf("%s %-18s %-22s %s %s", dot, truncate(name, 18), truncate(c.Target, 22), status, dim.Render("["+c.Method+"]"))
				if i == m.cursor {
					lines = append(lines, accent.Render("▸ ")+selRow.Render(row))
				} else {
					lines = append(lines, "  "+row)
				}
			}
			// Live SSH-remote sessions whose self-reported hostname doesn't match a
			// configured box name (e.g. box "OC" connecting as "open.claw").
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
		bar := lipgloss.NewStyle().Foreground(ColorPrimary).Render(progressBar(m.progressPct, 30))
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
		head := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("✓ Done")
		switch {
		case !m.progressOK:
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render("Finished with issues")
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
			lines = append(lines, accent.Render("[h]")+dim.Render(" set up the relay tunnel on this Mac (recommended for a NAT'd host)"))
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
			{"1", "lan", "Same network — same Wi-Fi / router / subnet"},
			{"2", "vpn", "Over a VPN you run (Tailscale, WireGuard…)"},
			{"3", "bastion", "Through a jump host / bastion gateway"},
			{"4", "public", "Public IP / custom host + ssh options"},
			{"5", "relay", "Serve THIS Mac to a NAT'd / shared box"},
		}
		if m.formStep == formStepMethod {
			lines = append(lines, "  "+boldAccent.Render("How does this machine reach the memory?")+dim.Render("   press 1–5"))
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
			hostTitle, hostHintTxt = "Relay server", "[user@]host[:port] — a public box this Mac dials out to"
		}
		switch {
		case m.formStep == formStepHost:
			lines = append(lines, "  "+boldAccent.Render(hostTitle)+dim.Render("   "+hostHintTxt))
			lines = append(lines, "   "+m.formHost+caret)
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

		// ── Step 3 · Target OS (not needed for relay) ────────────────────
		if !isRelay {
			osOpts := []struct{ val, lbl string }{{"linux", "Linux"}, {"darwin", "macOS"}, {"windows", "Windows"}}
			switch {
			case m.formStep == formStepOS:
				row := "  " + boldAccent.Render("Host OS") + dim.Render("   press 1–3    ")
				for i, o := range osOpts {
					if m.formOS == o.val {
						row += accent.Render("●") + " " + boldAccent.Render(o.lbl) + "   "
					} else {
						row += dim.Render(fmt.Sprintf("%d ○ %s   ", i+1, o.lbl))
					}
				}
				lines = append(lines, row)
			case m.formOS != "":
				lbl := m.formOS
				for _, o := range osOpts {
					if o.val == m.formOS {
						lbl = o.lbl
					}
				}
				lines = append(lines, "  "+check+" "+dim.Render("Host OS   ")+lbl)
			default:
				lines = append(lines, "  "+dim.Render("· Host OS"))
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
		case m.formName != "":
			lines = append(lines, "  "+check+" "+dim.Render("Name   ")+m.formName)
		}

		// ── What the relay flow will do (set expectations) ───────────────
		if isRelay {
			lines = append(lines, "")
			lines = append(lines, dim.Render("  → Opens a reverse tunnel from this Mac, then installs auxly on the"))
			lines = append(lines, dim.Render("    relay box and wires its agent to your memory. Nothing to run there."))
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
			}
		} else if m.clientCount() > 0 {
			// A connected box (host side).
			bar = []string{
				action("c", "Connect new"),
				action("d", "Disconnect"),
				action("r", "Reconnect"),
				action("e", "Rename"),
				action("s", "Share files"),
				action("x", "Remove"),
			}
		} else {
			bar = []string{
				action("c", "Connect new"),
				action("t", "Test"),
				action("p", "Print config"),
				action("d", "Remove"),
			}
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
