package tui

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

// ─────────────────────────────────────────────────────────────────
//  Remote Memory over SSH
//
//  The old "SSH Bridge" stack (reverse tunnel + localhost daemon on
//  port 7357 + session token + cross-compile/scp) is gone. The model
//  is now plain SSH: the memory HOST runs `auxly mcp-server` and this
//  machine launches it over SSH via `auxly connect`. No daemon, no
//  open port, no token — SSH is the transport.
// ─────────────────────────────────────────────────────────────────

// remoteEntry is a single configured remote read from ~/.auxly/remotes.yaml.
type remoteEntry struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
}

// remotesFile is the minimal shape we read from ~/.auxly/remotes.yaml.
// The file may not exist yet — a missing file is tolerated silently.
type remotesFile struct {
	Remotes []remoteEntry `yaml:"remotes"`
}

type sshModel struct {
	remotes []remoteEntry
	width   int
	height  int

	// editingHost is retained for the app.go contract. There is no
	// interactive editing in the new informational surface, so it
	// stays false.
	editingHost bool
}

// sshRefreshMsg carries the freshly read remotes list back into Update.
type sshRefreshMsg struct {
	remotes []remoteEntry
}

// ─────────────────────────────────────────────────────────────────
//  Constructor
// ─────────────────────────────────────────────────────────────────

func newSSHModel() sshModel {
	return sshModel{
		remotes:     readRemotes(),
		editingHost: false,
	}
}

// ─────────────────────────────────────────────────────────────────
//  Refresh — best-effort re-read of ~/.auxly/remotes.yaml
// ─────────────────────────────────────────────────────────────────

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

// readRemotes loads configured remotes. A missing or unparsable file
// yields an empty slice rather than an error — the surface is purely
// informational and must tolerate a not-yet-configured machine.
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

// ─────────────────────────────────────────────────────────────────
//  Update — no interactive editing; keep minimal but valid
// ─────────────────────────────────────────────────────────────────

func (m sshModel) Update(msg tea.Msg) (sshModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case sshRefreshMsg:
		m.remotes = msg.remotes
		return m, nil
	}
	// Keys and everything else: nothing to do on an informational pane.
	return m, nil
}

// ─────────────────────────────────────────────────────────────────
//  View
// ─────────────────────────────────────────────────────────────────

func (m sshModel) View() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)

	width := m.width
	if width <= 0 {
		width = 80
	}
	// Inner content width inside a rounded border with Padding(1, 2).
	bodyWidth := width - 8
	if bodyWidth < 40 {
		bodyWidth = 40
	}

	// ── Explanation panel ─────────────────────────────────────────
	var lines []string
	lines = append(lines, cyan.Render("REMOTE MEMORY OVER SSH"))
	lines = append(lines, "")

	intro := "Your memory HOST runs `auxly mcp-server` over SSH. This machine runs the " +
		"`auxly connect` wizard plus a thin launcher that opens the session. There is " +
		"no daemon, no open port, and no session token — SSH itself is the transport."
	lines = append(lines, wrapText(intro, bodyWidth)...)
	lines = append(lines, "")

	vpn := "VPN-agnostic and no public IP required: bring your own network. Reach the host " +
		"over a LAN, a VPN, or a bastion — whatever already connects the two machines."
	lines = append(lines, wrapText(vpn, bodyWidth)...)
	lines = append(lines, "")

	// ── Reachability methods ──────────────────────────────────────
	lines = append(lines, cyan.Render("HOW TO REACH THE HOST"))
	lines = append(lines, "")
	methods := []string{
		bold.Render("1. Same network (LAN)") + dim.Render(" — direct ssh to the host on the local network"),
		bold.Render("2. Over a VPN") + dim.Render(" — e.g. Tailscale, WireGuard, or a corporate VPN"),
		bold.Render("3. Jump host / bastion") + dim.Render(" — hop through a gateway with ProxyJump"),
		bold.Render("4. Public host / custom") + dim.Render(" — a reachable IP or your own ssh config"),
	}
	for _, line := range methods {
		lines = append(lines, "  "+line)
	}
	lines = append(lines, "")

	// ── Configured remotes ────────────────────────────────────────
	lines = append(lines, cyan.Render("CONFIGURED REMOTES"))
	lines = append(lines, "")
	if len(m.remotes) == 0 {
		lines = append(lines, "  "+dim.Render("No remotes configured yet."))
	} else {
		for _, r := range m.remotes {
			name := r.Name
			if name == "" {
				name = "(unnamed)"
			}
			host := r.Host
			if host == "" {
				host = dim.Render("(no host)")
			}
			lines = append(lines, "  "+green.Render("•")+" "+bold.Render(name)+dim.Render("  →  ")+host)
		}
	}
	lines = append(lines, "")

	// ── Call to action ────────────────────────────────────────────
	cta := "Run `auxly connect` in a terminal to add or manage a remote host."
	lines = append(lines, dim.Render(strings.Repeat("─", bodyWidth)))
	lines = append(lines, cyan.Render(cta))

	// Pad every line to a uniform width for a clean border.
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
