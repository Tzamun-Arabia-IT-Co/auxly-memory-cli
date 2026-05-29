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
	sshModeList    = ""
	sshModeConfirm = "confirm"
	sshModePrint   = "print"
	sshModeForm    = "form"
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

	// editingHost drives app.go's key-routing contract: when true, ALL keys are
	// delivered to this model (so the in-TUI add-host form can capture text). It
	// is set only while the form is open.
	editingHost bool
}

// sshRefreshMsg carries the freshly read remotes list back into Update.
type sshRefreshMsg struct {
	remotes []remoteEntry
}

// sshExecDoneMsg is returned after a shelled-out `auxly connect …` finishes.
type sshExecDoneMsg struct {
	status string
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

// runConnect shells out to this same binary's `connect` subcommand. tea.ExecProcess
// releases the TUI to the real terminal so the wizard can prompt for SSH passwords
// / run ssh-keygen, then restores the TUI and triggers a refresh.
func runConnect(args ...string) tea.Cmd {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "auxly"
	}
	c := exec.Command(bin, append([]string{"connect"}, args...)...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sshExecDoneMsg{status: "⚠ connect " + strings.Join(args, " ") + " exited: " + err.Error()}
		}
		return sshExecDoneMsg{status: ""}
	})
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
			name := m.selectedName()
			m.mode = sshModeList
			if name != "" {
				return m, runConnect("remove", name)
			}
		case "n", "N", "esc":
			m.mode = sshModeList
		}
		return m, nil

	case sshModeForm:
		return m.handleFormKey(msg)

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
				return m, runConnect("test", name)
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
	args := []string{"add", "--method", m.formMethod, "--host", host}
	if name := strings.TrimSpace(m.formName); name != "" {
		args = append(args, "--name", name)
	}
	if jump := strings.TrimSpace(m.formJump); jump != "" {
		args = append(args, "--jump", jump)
	}
	m.mode = sshModeList
	m.editingHost = false
	return m, runConnect(args...)
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
