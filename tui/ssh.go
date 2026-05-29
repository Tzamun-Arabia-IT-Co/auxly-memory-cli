package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
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
	User   string `yaml:"user"`
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
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
)

// form steps.
const (
	formStepMethod = "method"
	formStepHost   = "host"
	formStepJump   = "jump"
	formStepName   = "name"
)

type sshModel struct {
	remotes []remoteEntry
	cursor  int
	mode    string
	preview string // MCP JSON shown in print mode
	status  string // transient feedback after an action
	width   int
	height  int

	// add-host form state (mode == sshModeForm)
	formStep   string
	formMethod string
	formHost   string
	formJump   string
	formName   string

	// captured-run state (sshModeProgress / sshModeResult)
	progressTitle  string
	progressOut    []string
	progressOK     bool
	progressNeeded bool     // first-time SSH key setup required
	pendingKeyArgs []string // non-batch `connect add …` args for the key-setup fallback
	password       string   // transient masked SSH password (sshModePassword)

	// editingHost drives app.go's key-routing contract: when true, ALL keys are
	// delivered to this model (so the in-TUI add-host form can capture text). It
	// is set only while the form is open.
	editingHost bool
}

// sshRefreshMsg carries the freshly read remotes list back into Update.
type sshRefreshMsg struct {
	remotes []remoteEntry
}

// sshExecDoneMsg is returned after a SUSPENDED (ExecProcess) `auxly connect …`
// finishes — used only for the key-setup step that needs a terminal password.
type sshExecDoneMsg struct {
	status string
}

// sshCapturedMsg carries the output of a `auxly connect …` run captured in-pane
// (no TUI suspend), so the doctor/setup progress stays inside the TUI.
type sshCapturedMsg struct {
	output   string
	err      error
	needsKey bool
}

// ─────────────────────────────────────────────────────────────────
//  Constructor / data
// ─────────────────────────────────────────────────────────────────

func newSSHModel() sshModel {
	return sshModel{remotes: readRemotes()}
}

func (m sshModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		return sshRefreshMsg{remotes: readRemotes()}
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

func (m sshModel) selectedName() string {
	if m.cursor >= 0 && m.cursor < len(m.remotes) {
		return m.remotes[m.cursor].Name
	}
	return ""
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
	c := exec.Command(exePath(), append([]string{"connect"}, args...)...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sshExecDoneMsg{status: "⚠ connect " + strings.Join(args, " ") + " exited: " + err.Error()}
		}
		return sshExecDoneMsg{status: ""}
	})
}

// runConnectCaptured runs `auxly connect …` WITHOUT suspending the TUI, capturing
// its output so the doctor/setup progress renders inside the pane. Safe only for
// non-interactive runs (no password prompt) — add uses --batch to guarantee that.
func runConnectCaptured(args ...string) tea.Cmd {
	bin := exePath()
	return func() tea.Msg {
		c := exec.Command(bin, append([]string{"connect"}, args...)...)
		out, err := c.CombinedOutput()
		return sshCapturedMsg{
			output:   string(out),
			err:      err,
			needsKey: strings.Contains(string(out), "AUXLY_KEY_REQUIRED"),
		}
	}
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
		if m.cursor >= len(m.remotes) {
			m.cursor = len(m.remotes) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.mode = sshModeList
		return m, nil

	case sshExecDoneMsg:
		m.status = msg.status
		m.mode = sshModeList
		return m, m.Refresh()

	case sshCapturedMsg:
		var out []string
		for _, l := range strings.Split(strings.TrimRight(msg.output, "\n"), "\n") {
			if strings.Contains(l, "AUXLY_KEY_REQUIRED") {
				continue // internal token, not for display
			}
			out = append(out, l)
		}
		m.progressOut = out
		m.progressOK = msg.err == nil
		m.progressNeeded = msg.needsKey
		m.mode = sshModeResult
		m.remotes = readRemotes() // reload list behind the result panel
		if m.cursor >= len(m.remotes) {
			m.cursor = len(m.remotes) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
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
			if name := m.selectedName(); name != "" {
				m.mode = sshModeProgress
				m.progressTitle = "Removing " + name
				return m, runConnectCaptured("remove", name)
			}
			m.mode = sshModeList
		case "n", "N", "esc":
			m.mode = sshModeList
		}
		return m, nil

	case sshModeForm:
		return m.handleFormKey(msg)

	case sshModeProgress:
		return m, nil // ignore input while a captured run is in flight

	case sshModeResult:
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
				m.mode = sshModeList
				m.progressNeeded = false
				return m, runConnect(args...)
			}
		}
		m.mode = sshModeList // any other key closes the result panel
		return m, nil

	case sshModePassword:
		return m.handlePasswordKey(msg)

	default: // list mode
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.remotes)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "c":
			m.mode = sshModeForm
			m.formStep = formStepMethod
			m.formMethod, m.formHost, m.formJump, m.formName = "", "", "", ""
			m.status = ""
			m.editingHost = true // capture all keys for the in-TUI form
			return m, nil
		case "t":
			if name := m.selectedName(); name != "" {
				m.status = ""
				m.mode = sshModeProgress
				m.progressTitle = "Testing " + name
				return m, runConnectCaptured("test", name)
			}
		case "p", "enter":
			if name := m.selectedName(); name != "" {
				m.preview = mcpConfigJSON(name)
				m.mode = sshModePrint
			}
		case "d", "x":
			if m.selectedName() != "" {
				m.mode = sshModeConfirm
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
			switch s {
			case "1":
				m.formMethod, m.formStep = "lan", formStepHost
			case "2":
				m.formMethod, m.formStep = "vpn", formStepHost
			case "3":
				m.formMethod, m.formStep = "bastion", formStepHost
			case "4":
				m.formMethod, m.formStep = "public", formStepHost
			}
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
			return m, nil // host is required
		}
		if m.formMethod == "bastion" {
			m.formStep = formStepJump
		} else {
			m.formStep = formStepName
		}
	case formStepJump:
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
	base := []string{"add", "--method", m.formMethod, "--host", host}
	if name := strings.TrimSpace(m.formName); name != "" {
		base = append(base, "--name", name)
	}
	if jump := strings.TrimSpace(m.formJump); jump != "" {
		base = append(base, "--jump", jump)
	}
	// Stash the non-batch args for the key-setup fallback ([K] on the result panel),
	// then run the doctor/setup captured in-pane via --batch (never prompts here).
	m.pendingKeyArgs = append([]string{}, base...)
	m.editingHost = false
	m.mode = sshModeProgress
	m.progressTitle = "Connecting to " + host
	return m, runConnectCaptured(append(base, "--batch")...)
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
		m.password = "" // drop it from the model immediately
		m.editingHost = false
		m.progressNeeded = false
		m.mode = sshModeProgress
		m.progressTitle = "Installing key & connecting"
		return m, runConnectPTY(pw, args...)
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
	intro := "SSH is the transport — the HOST runs `auxly mcp-server` and this machine " +
		"launches it on demand. No daemon, no open port, no token. VPN-agnostic: reach the " +
		"host over a LAN, a VPN (Tailscale/WireGuard), or a jump host."
	lines = append(lines, wrapText(intro, bodyWidth)...)
	lines = append(lines, "")

	// ── Configured remotes (selectable) ────────────────────────────
	lines = append(lines, cyan.Render("CONFIGURED REMOTES"))
	lines = append(lines, "")
	if len(m.remotes) == 0 {
		lines = append(lines, "  "+dim.Render("No remotes configured yet — press ")+accent.Render("c")+dim.Render(" to add your first host."))
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
			row := fmt.Sprintf("%-18s %-26s %s", truncate(name, 18), truncate(target, 26), dim.Render("["+method+"]"))
			if i == m.cursor {
				marker := accent.Render("▸ ")
				lines = append(lines, marker+lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render(row))
			} else {
				lines = append(lines, "  "+row)
			}
		}
	}
	lines = append(lines, "")

	// ── Modal area: confirm / print / action bar ───────────────────
	lines = append(lines, dim.Render(strings.Repeat("─", bodyWidth)))
	switch m.mode {
	case sshModeConfirm:
		warn := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
		lines = append(lines, warn.Render(fmt.Sprintf("Remove remote %q?  ", m.selectedName()))+
			accent.Render("[y]")+dim.Render(" yes   ")+accent.Render("[n]")+dim.Render(" cancel"))
	case sshModeProgress:
		lines = append(lines, cyan.Render("⏳ "+m.progressTitle)+dim.Render("  — running doctor & setup…"))
		lines = append(lines, "")
		lines = append(lines, dim.Render("Running inside the TUI; this can take a moment on first install."))
	case sshModeResult:
		head := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("✓ Done")
		if !m.progressOK {
			head = lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render("Finished with issues")
		}
		lines = append(lines, head+dim.Render("  ("+m.progressTitle+")"))
		out := m.progressOut
		if len(out) > 16 {
			lines = append(lines, dim.Render(fmt.Sprintf("  … %d earlier lines", len(out)-16)))
			out = out[len(out)-16:]
		}
		for _, l := range out {
			lines = append(lines, "  "+l)
		}
		lines = append(lines, "")
		if m.progressNeeded {
			lines = append(lines, accent.Render("[p]")+dim.Render(" enter SSH password here   ")+accent.Render("[K]")+dim.Render(" use a terminal instead   ·   any other key: close"))
		} else {
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
		hl := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
		label := func(step, text string) string {
			if m.formStep == step {
				return hl.Render(text)
			}
			return dim.Render(text)
		}
		caret := func(step string) string {
			if m.formStep == step {
				return accent.Render("▌")
			}
			return ""
		}
		lines = append(lines, cyan.Render("ADD A REMOTE HOST")+dim.Render("    (esc cancels · Enter = next/save)"))
		lines = append(lines, "")

		radios := ""
		for _, opt := range []struct{ k, v string }{{"1", "lan"}, {"2", "vpn"}, {"3", "bastion"}, {"4", "public"}} {
			dot := "○"
			if m.formMethod == opt.v {
				dot = accent.Render("●")
			}
			radios += fmt.Sprintf("%s %s   ", dot, opt.v)
		}
		methodHint := ""
		if m.formStep == formStepMethod {
			methodHint = dim.Render("  ‹press 1–4›")
		}
		lines = append(lines, "  "+label(formStepMethod, "Method")+"   "+radios+methodHint)

		hostHint := ""
		if m.formStep == formStepHost {
			hostHint = dim.Render("  ‹user@host[:port]›")
		}
		lines = append(lines, "  "+label(formStepHost, "Host  ")+"   "+m.formHost+caret(formStepHost)+hostHint)

		if m.formMethod == "bastion" {
			lines = append(lines, "  "+label(formStepJump, "Jump  ")+"   "+m.formJump+caret(formStepJump))
		}

		nameShown := m.formName + caret(formStepName)
		if m.formName == "" && m.formStep != formStepName {
			nameShown = dim.Render("(defaults to host)")
		}
		lines = append(lines, "  "+label(formStepName, "Name  ")+"   "+nameShown)
	case sshModePrint:
		lines = append(lines, cyan.Render(fmt.Sprintf("MCP config for %q", m.selectedName()))+dim.Render("  (paste into your IDE)"))
		for _, l := range strings.Split(m.preview, "\n") {
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(ColorSuccess).Render(l))
		}
		lines = append(lines, "")
		lines = append(lines, dim.Render("Press any key to close."))
	default:
		action := func(k, label string) string { return accent.Render("["+k+"]") + dim.Render(" "+label) }
		lines = append(lines, strings.Join([]string{
			action("c", "Connect new"),
			action("t", "Test"),
			action("p", "Print config"),
			action("d", "Remove"),
		}, dim.Render("   ")))
		if m.status != "" {
			lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Render(m.status))
		} else {
			lines = append(lines, dim.Render("`auxly connect` in a terminal does the same — this tab is a front-end for it."))
		}
	}

	var padded []string
	for _, line := range lines {
		padded = append(padded, padLine(line, bodyWidth))
	}
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Render(strings.Join(padded, "\n"))

	return panel + "\n\n"
}
