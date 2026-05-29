package tui

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─────────────────────────────────────────────────────────────────
//  State structs
// ─────────────────────────────────────────────────────────────────

type sshState struct {
	daemonActive   bool
	daemonConns    int
	launchAgent    bool // PLIST installed?
	compress       bool // saved in ~/.auxly/config.yaml
	sessionToken   string
	port           int
}

type sshModel struct {
	state         sshState
	cursor        int // action menu cursor
	statusMsg     string
	statusOK      bool
	statusAt      time.Time
	tickCycle     int
	sshHost       string // e.g. root@192.168.1.141
	editingHost   bool   // active typing mode
	remoteOS      string // "linux", "macos", or "windows"
	showManual    bool   // toggle manual instructions
	width         int
	height        int
	selectedGuide int // 0 = One-Click, 1 = Local Client CLI Setup, 2 = Manual Setup, 3 = Multi-Server Support
}

// ── Messages ──────────────────────────────────────────────────────

type sshRefreshMsg struct{ state sshState }
type sshActionDoneMsg struct {
	ok  bool
	msg string
}
type sshTickMsg struct{}

// ─────────────────────────────────────────────────────────────────
//  Constructor
// ─────────────────────────────────────────────────────────────────

func newSSHModel() sshModel {
	return sshModel{
		sshHost:       "root@<your-remote-server-ip>",
		remoteOS:      "linux",
		showManual:    false,
		selectedGuide: 0,
	}
}

// ─────────────────────────────────────────────────────────────────
//  Refresh (reads files, no goroutine needed — fast I/O only)
// ─────────────────────────────────────────────────────────────────

func (m sshModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		return sshRefreshMsg{state: readSSHState()}
	}
}

func sshTickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return sshTickMsg{} })
}

func readSSHState() sshState {
	home, err := os.UserHomeDir()
	if err != nil {
		return sshState{port: 7357}
	}
	auxlyDir := filepath.Join(home, ".auxly")

	s := sshState{port: 7357}

	// 1. Daemon alive?
	pidData, err := os.ReadFile(filepath.Join(auxlyDir, "daemon.pid"))
	if err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					s.daemonActive = true
				}
			}
		}
	}

	// 2. Active port
	if s.daemonActive {
		if portData, err := os.ReadFile(filepath.Join(auxlyDir, "daemon.port")); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(portData))); err == nil {
				s.port = p
			}
		}
	} else {
		// Find first available target port starting from 7357
		for p := 7357; p < 7367; p++ {
			addr := fmt.Sprintf("127.0.0.1:%d", p)
			ln, err := net.Listen("tcp", addr)
			if err == nil {
				ln.Close()
				s.port = p
				break
			}
		}
	}

	// 3. Active connections
	if connsData, err := os.ReadFile(filepath.Join(auxlyDir, "daemon.conns")); err == nil {
		s.daemonConns, _ = strconv.Atoi(strings.TrimSpace(string(connsData)))
	}

	// 4. Session token (first 16 chars shown)
	if tokData, err := os.ReadFile(filepath.Join(auxlyDir, ".session_token")); err == nil {
		full := strings.TrimSpace(string(tokData))
		if len(full) > 16 {
			s.sessionToken = full[:16] + "…"
		} else {
			s.sessionToken = full
		}
	}

	// 5. LaunchAgent installed?
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.tzamun.auxly.server.plist")
	if _, err := os.Stat(plistPath); err == nil {
		s.launchAgent = true
	}

	// 6. Compression config
	s.compress = readCompressConfig()

	return s
}

// ─────────────────────────────────────────────────────────────────
//  Compression config persistence (~/.auxly/config.yaml minimal)
// ─────────────────────────────────────────────────────────────────

func auxlyConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auxly", "config.yaml")
}

func readCompressConfig() bool {
	data, err := os.ReadFile(auxlyConfigPath())
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "compress: true" {
			return true
		}
	}
	return false
}

func writeCompressConfig(enabled bool) error {
	path := auxlyConfigPath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)

	// Read existing lines, update or add the compress key
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	}

	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "compress:") {
			if enabled {
				lines[i] = "compress: true"
			} else {
				lines[i] = "compress: false"
			}
			found = true
		}
	}
	if !found {
		val := "false"
		if enabled {
			val = "true"
		}
		lines = append(lines, "compress: "+val)
	}

	// Remove trailing empty lines, add one newline
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// ReadCompressFromConfig is exported so cmd/server.go and cmd/bridge.go can call it.
func ReadCompressFromConfig() bool {
	return readCompressConfig()
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
		m.state = msg.state
		return m, sshTickCmd()

	case sshTickMsg:
		return m, m.Refresh()

	case sshActionDoneMsg:
		m.statusMsg = msg.msg
		m.statusOK = msg.ok
		m.statusAt = time.Now()
		return m, m.Refresh()

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			w := m.width
			if w <= 0 {
				w = 80
			}
			banner := renderBanner(w)
			tabRow := strings.Count(banner, "\n")
			contentOffsetY := tabRow + 3

			stacked := m.width > 0 && m.width < 95
			if !stacked {
				// Side-by-side Layout: Left Panel is X: [0, 44]
				if msg.X >= 0 && msg.X <= 44 {
					// Gateway toggle: Y = contentOffsetY + 5
					if msg.Y == contentOffsetY+5 {
						if m.state.daemonActive {
							return m, func() tea.Msg { return doStopDaemon() }
						} else {
							return m, func() tea.Msg { return doStartDaemon(m.state) }
						}
					}
					// Auto-start: Y = contentOffsetY + 6
					if msg.Y == contentOffsetY+6 {
						if m.state.launchAgent {
							return m, func() tea.Msg { return doUninstallLaunchAgent() }
						} else {
							return m, func() tea.Msg { return doInstallLaunchAgent() }
						}
					}
					// Compression: Y = contentOffsetY + 7
					if msg.Y == contentOffsetY+7 {
						return m, func() tea.Msg { return doToggleCompress(m.state) }
					}
					// Session Token Copy: Y = contentOffsetY + 9
					if msg.Y == contentOffsetY+9 {
						if m.state.daemonActive {
							return m, func() tea.Msg { return doCopyToken() }
						}
					}
					// Toggle Remote OS: Y = contentOffsetY + 12
					if msg.Y == contentOffsetY+12 {
						switch m.remoteOS {
						case "linux":
							m.remoteOS = "macos"
						case "macos":
							m.remoteOS = "windows"
						default:
							m.remoteOS = "linux"
						}
						return m, m.Refresh()
					}
					// Edit Remote Address: Y = contentOffsetY + 13
					if msg.Y == contentOffsetY+13 {
						m.editingHost = true
						return m, nil
					}
				}

				// Right Panel Guide Selector: starts at X = 48
				// Bullets are at Y = contentOffsetY + 4, 5, 6, 7
				if msg.X >= 48 {
					if msg.Y >= contentOffsetY+4 && msg.Y <= contentOffsetY+7 {
						m.selectedGuide = msg.Y - (contentOffsetY + 4)
						return m, nil
					}
					// Also let clicking inside the active guide box bottom trigger Copy Command (if 1-click is shown)
					if m.selectedGuide == 0 && msg.Y >= contentOffsetY+15 && msg.Y <= contentOffsetY+18 {
						return m, func() tea.Msg { return doCopyTunnelCmd(m.state, m.sshHost, m.remoteOS) }
					}
				}
			} else {
				// Stacked Layout click check:
				// Left Panel starts at relative line 3 (Y = contentOffsetY + 3) to line 15 (Y = contentOffsetY + 15)
				if msg.Y >= contentOffsetY+3 && msg.Y <= contentOffsetY+15 {
					relY := msg.Y - (contentOffsetY + 3)
					if relY == 2 { // Gateway toggle (Y = contentOffsetY + 5)
						if m.state.daemonActive {
							return m, func() tea.Msg { return doStopDaemon() }
						} else {
							return m, func() tea.Msg { return doStartDaemon(m.state) }
						}
					}
					if relY == 3 { // Auto-start (Y = contentOffsetY + 6)
						if m.state.launchAgent {
							return m, func() tea.Msg { return doUninstallLaunchAgent() }
						} else {
							return m, func() tea.Msg { return doInstallLaunchAgent() }
						}
					}
					if relY == 4 { // Compression (Y = contentOffsetY + 7)
						return m, func() tea.Msg { return doToggleCompress(m.state) }
					}
					if relY == 6 { // Token copy (Y = contentOffsetY + 9)
						if m.state.daemonActive {
							return m, func() tea.Msg { return doCopyToken() }
						}
					}
					if relY == 9 { // Toggle Remote OS (Y = contentOffsetY + 12)
						switch m.remoteOS {
						case "linux":
							m.remoteOS = "macos"
						case "macos":
							m.remoteOS = "windows"
						default:
							m.remoteOS = "linux"
						}
						return m, m.Refresh()
					}
					if relY == 10 { // Edit Remote Address (Y = contentOffsetY + 13)
						m.editingHost = true
						return m, nil
					}
				}

				// Right Panel Guide Selector (stacked under Left Panel)
				// Bullets are at Y = contentOffsetY + 18, 19, 20, 21
				if msg.Y >= contentOffsetY+18 && msg.Y <= contentOffsetY+21 {
					m.selectedGuide = msg.Y - (contentOffsetY + 18)
					return m, nil
				}
			}
		}

	case tea.KeyMsg:
		// Clear old status after 5 s
		if m.statusMsg != "" && time.Since(m.statusAt) > 5*time.Second {
			m.statusMsg = ""
		}

		if m.editingHost {
			switch msg.String() {
			case "enter", "esc":
				m.editingHost = false
				if m.sshHost == "" {
					m.sshHost = "root@<your-remote-server-ip>"
				}
				return m, m.Refresh()
			case "backspace":
				if len(m.sshHost) > 0 {
					m.sshHost = m.sshHost[:len(m.sshHost)-1]
				}
				return m, nil
			default:
				// Append standard printable characters
				if len(msg.String()) == 1 {
					if m.sshHost == "root@<your-remote-server-ip>" {
						m.sshHost = "" // clear placeholder on type
					}
					m.sshHost += msg.String()
				}
				return m, nil
			}
		}

		switch msg.String() {
		case "h":
			m.editingHost = true
			return m, nil
		case "o":
			switch m.remoteOS {
			case "linux":
				m.remoteOS = "macos"
			case "macos":
				m.remoteOS = "windows"
			default:
				m.remoteOS = "linux"
			}
			return m, m.Refresh()
		case "m":
			m.showManual = !m.showManual
			return m, m.Refresh()
		case "enter", "s":
			if m.state.daemonActive {
				return m, func() tea.Msg { return doStopDaemon() }
			} else {
				return m, func() tea.Msg { return doStartDaemon(m.state) }
			}
		case "a":
			if m.state.launchAgent {
				return m, func() tea.Msg { return doUninstallLaunchAgent() }
			} else {
				return m, func() tea.Msg { return doInstallLaunchAgent() }
			}
		case "z":
			return m, func() tea.Msg { return doToggleCompress(m.state) }
		case "c":
			if m.state.daemonActive {
				return m, func() tea.Msg { return doCopyToken() }
			}
		case "t":
			return m, func() tea.Msg { return doCopyTunnelCmd(m.state, m.sshHost, m.remoteOS) }
		case "left":
			m.selectedGuide--
			if m.selectedGuide < 0 {
				m.selectedGuide = 3
			}
			return m, nil
		case "right":
			m.selectedGuide++
			if m.selectedGuide > 3 {
				m.selectedGuide = 0
			}
			return m, nil
		case "1", "2", "3", "4":
			idx := int(msg.String()[0] - '1')
			m.selectedGuide = idx
			return m, nil
		}
	}
	return m, nil
}

// ─────────────────────────────────────────────────────────────────
//  Action handlers
// ─────────────────────────────────────────────────────────────────

func doStartDaemon(s sshState) tea.Msg {
	// 1. Check if the port is already in use
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return sshActionDoneMsg{
			ok:  false,
			msg: fmt.Sprintf("❌ Port %d is already in use! Please stop any running gateway first.", s.port),
		}
	}
	ln.Close()

	// Self binary path
	exe, err := os.Executable()
	if err != nil {
		return sshActionDoneMsg{ok: false, msg: "Cannot find auxly binary: " + err.Error()}
	}
	resolved, _ := filepath.EvalSymlinks(exe)
	if resolved != "" {
		exe = resolved
	}
	portArg := strconv.Itoa(s.port)
	args := []string{exe, "server", "--port", portArg}
	if s.compress {
		args = append(args, "--compress")
	}
	// nohup-style: start detached process
	cmd := exec.Command("nohup", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logPath := filepath.Join(filepath.Dir(auxlyConfigPath()), "server-tui.log")
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	if err := cmd.Start(); err != nil {
		return sshActionDoneMsg{ok: false, msg: "Start failed: " + err.Error()}
	}
	time.Sleep(400 * time.Millisecond) // small wait for PID file to be written
	return sshActionDoneMsg{ok: true, msg: "✅ Daemon started (nohup, port " + portArg + ")"}
}

func doStopDaemon() tea.Msg {
	home, _ := os.UserHomeDir()
	pidData, err := os.ReadFile(filepath.Join(home, ".auxly", "daemon.pid"))
	if err != nil {
		return sshActionDoneMsg{ok: false, msg: "Daemon PID not found — already stopped?"}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return sshActionDoneMsg{ok: false, msg: "Invalid PID file"}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return sshActionDoneMsg{ok: false, msg: "Process not found"}
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return sshActionDoneMsg{ok: false, msg: "Kill failed: " + err.Error()}
	}
	return sshActionDoneMsg{ok: true, msg: "✅ Daemon stopped (SIGTERM)"}
}

func doInstallLaunchAgent() tea.Msg {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cmd := exec.Command(exe, "server", "install")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return sshActionDoneMsg{ok: false, msg: "Install failed: " + strings.TrimSpace(string(out))}
	}
	return sshActionDoneMsg{ok: true, msg: "✅ LaunchAgent installed — daemon will auto-start on login"}
}

func doUninstallLaunchAgent() tea.Msg {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cmd := exec.Command(exe, "server", "uninstall")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return sshActionDoneMsg{ok: false, msg: "Uninstall failed: " + strings.TrimSpace(string(out))}
	}
	return sshActionDoneMsg{ok: true, msg: "✅ LaunchAgent removed"}
}

func doToggleCompress(s sshState) tea.Msg {
	next := !s.compress
	if err := writeCompressConfig(next); err != nil {
		return sshActionDoneMsg{ok: false, msg: "Failed to save config: " + err.Error()}
	}
	label := "off"
	if next {
		label = "on"
	}
	return sshActionDoneMsg{ok: true, msg: fmt.Sprintf("✅ Compression set to %s — restart daemon to apply", label)}
}

// copyToClipboard is a cross-platform helper to copy text to the system clipboard.
func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("clip")
	case "darwin":
		cmd = exec.Command("pbcopy")
	default: // linux, etc.
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return fmt.Errorf("no clipboard utility found (please install xclip or xsel)")
		}
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func doCopyToken() tea.Msg {
	home, _ := os.UserHomeDir()
	tokData, err := os.ReadFile(filepath.Join(home, ".auxly", ".session_token"))
	if err != nil {
		return sshActionDoneMsg{ok: false, msg: "Token not found — is the daemon running?"}
	}
	token := strings.TrimSpace(string(tokData))
	if err := copyToClipboard(token); err != nil {
		return sshActionDoneMsg{ok: false, msg: "Clipboard copy failed: " + err.Error()}
	}
	return sshActionDoneMsg{ok: true, msg: "✅ Session token copied to clipboard"}
}

func doCopyTunnelCmd(s sshState, host string, remoteOS string) tea.Msg {
	home, _ := os.UserHomeDir()
	tokData, err := os.ReadFile(filepath.Join(home, ".auxly", ".session_token"))
	token := "YOUR_TOKEN"
	if err == nil {
		token = strings.TrimSpace(string(tokData))
	}

	exePath, err := os.Executable()
	if err != nil {
		exePath = "auxly"
	} else if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	var tunnelCmd string
	if runtime.GOOS == "windows" {
		// Format command for Windows PowerShell locally
		if remoteOS == "linux" {
			tunnelCmd = fmt.Sprintf(
				`$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o $env:TEMP\auxly-linux && scp $env:TEMP\auxly-linux %s:/usr/local/bin/auxly && Remove-Item $env:TEMP\auxly-linux && ssh %s "auxly bridge setup --token %s --port %d" && ssh -R %d:127.0.0.1:%d %s`,
				host, host, token, s.port, s.port, s.port, host,
			)
		} else if remoteOS == "windows" {
			tunnelCmd = fmt.Sprintf(
				`$env:GOOS="windows"; $env:GOARCH="amd64"; go build -o $env:TEMP\auxly.exe && scp $env:TEMP\auxly.exe %s:C:/Users/Public/auxly.exe && Remove-Item $env:TEMP\auxly.exe && ssh %s "C:/Users/Public/auxly.exe bridge setup --token %s --port %d" && ssh -R %d:127.0.0.1:%d %s`,
				host, host, token, s.port, s.port, s.port, host,
			)
		} else { // macOS remote
			tunnelCmd = fmt.Sprintf(
				`scp "%s" %s:/usr/local/bin/auxly && ssh %s "auxly bridge setup --token %s --port %d" && ssh -R %d:127.0.0.1:%d %s`,
				exePath, host, host, token, s.port, s.port, s.port, host,
			)
		}
	} else {
		// Format command for Unix bash locally (macOS/Linux)
		if remoteOS == "linux" {
			tunnelCmd = fmt.Sprintf(
				"GOOS=linux GOARCH=amd64 go build -o /tmp/auxly-linux && scp /tmp/auxly-linux %s:/usr/local/bin/auxly && rm /tmp/auxly-linux && ssh %s \"auxly bridge setup --token %s --port %d\" && ssh -R %d:127.0.0.1:%d %s",
				host, host, token, s.port, s.port, s.port, host,
			)
		} else if remoteOS == "windows" {
			tunnelCmd = fmt.Sprintf(
				"GOOS=windows GOARCH=amd64 go build -o /tmp/auxly.exe && scp /tmp/auxly.exe %s:C:/Users/Public/auxly.exe && rm /tmp/auxly.exe && ssh %s \"C:/Users/Public/auxly.exe bridge setup --token %s --port %d\" && ssh -R %d:127.0.0.1:%d %s",
				host, host, token, s.port, s.port, s.port, host,
			)
		} else { // macOS remote
			tunnelCmd = fmt.Sprintf(
				"scp %s %s:/usr/local/bin/auxly && ssh %s \"auxly bridge setup --token %s --port %d\" && ssh -R %d:127.0.0.1:%d %s",
				exePath, host, host, token, s.port, s.port, s.port, host,
			)
		}
	}

	if err := copyToClipboard(tunnelCmd); err != nil {
		return sshActionDoneMsg{ok: false, msg: "Clipboard copy failed: " + err.Error()}
	}
	return sshActionDoneMsg{ok: true, msg: "✅ 1-Click Setup & Connect command copied!"}
}

// ─────────────────────────────────────────────────────────────────
//  View
// ─────────────────────────────────────────────────────────────────

func (m sshModel) View() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	yellow := lipgloss.NewStyle().Foreground(ColorWarning)
	bold := lipgloss.NewStyle().Bold(true)

	// Fetch daemon details
	s := m.state
	
	// Gateway status details
	var gatewayStatusLabel string
	var actBtn string
	if s.daemonActive {
		gatewayStatusLabel = fmt.Sprintf("%s  (Port %d)", green.Render("Running"), s.port)
		actBtn = "[ Stop Gateway ]"
	} else {
		gatewayStatusLabel = dim.Render("Stopped")
		actBtn = "[ Start Gateway ]"
	}

	var laLabel string
	if s.launchAgent {
		laLabel = fmt.Sprintf("%s  %s", green.Render("Enabled"), dim.Render("([a] Disable)"))
	} else {
		laLabel = fmt.Sprintf("%s %s", "Disabled", dim.Render("([a] Enable)"))
	}

	var compLabel string
	if s.compress {
		compLabel = fmt.Sprintf("%s %s", green.Render("On (Gzip)"), dim.Render("([z] Disable)"))
	} else {
		compLabel = fmt.Sprintf("%s     %s", "Off", dim.Render("([z] Enable)"))
	}

	var tokenHeader, tokenVal string
	if s.daemonActive && s.sessionToken != "" {
		tokenHeader = fmt.Sprintf("  Token:       %s", dim.Render("([c] Copy)"))
		tokenVal = fmt.Sprintf("  %s", yellow.Render(s.sessionToken))
	} else {
		tokenHeader = fmt.Sprintf("  Token:       %s", yellow.Render("Start Gateway first"))
		tokenVal = ""
	}

	// OS info
	var localOSLabel string
	switch runtime.GOOS {
	case "darwin":
		localOSLabel = "macOS"
	case "windows":
		localOSLabel = "Windows"
	case "linux":
		localOSLabel = "Linux"
	default:
		localOSLabel = runtime.GOOS
	}

	var osToggleLabel string
	switch m.remoteOS {
	case "linux":
		osToggleLabel = green.Render("Linux") + dim.Render(" ([o] macOS)")
	case "macos":
		osToggleLabel = green.Render("macOS") + dim.Render(" ([o] Windows)")
	default:
		osToggleLabel = green.Render("Windows") + dim.Render(" ([o] Linux)")
	}

	var hostHeader, hostVal string
	if m.editingHost {
		hostHeader = fmt.Sprintf("  Remote Addr: %s", dim.Render("([h] Save)"))
		hostVal = fmt.Sprintf("  %s", bold.Render(m.sshHost)+"█")
	} else {
		hostHeader = fmt.Sprintf("  Remote Addr: %s", dim.Render("([h] Edit)"))
		hostVal = fmt.Sprintf("  %s", bold.Render(m.sshHost))
	}

	// Resolve session token for command generation
	home, _ := os.UserHomeDir()
	tokData, _ := os.ReadFile(filepath.Join(home, ".auxly", ".session_token"))
	tokenVal = "YOUR_TOKEN"
	if len(tokData) > 0 {
		tokenVal = strings.TrimSpace(string(tokData))
	}

	// 1. LEFT COLUMN/PANEL: System & Remote Config
	var leftLines []string
	leftLines = append(leftLines, cyan.Render("SYSTEM & CONFIG"))
	leftLines = append(leftLines, "")
	leftLines = append(leftLines, fmt.Sprintf("  Gateway:     %s", gatewayStatusLabel))
	if s.daemonActive {
		leftLines = append(leftLines, fmt.Sprintf("  Connections: %s", green.Render(fmt.Sprintf("%d connection(s)", s.daemonConns))))
	}
	leftLines = append(leftLines, fmt.Sprintf("  Action:      %s", cyan.Render(actBtn)))
	leftLines = append(leftLines, "")
	leftLines = append(leftLines, fmt.Sprintf("  Auto-start:  %s", laLabel))
	leftLines = append(leftLines, fmt.Sprintf("  Compression: %s", compLabel))
	leftLines = append(leftLines, "")
	leftLines = append(leftLines, tokenHeader)
	if tokenVal != "" {
		leftLines = append(leftLines, tokenVal)
	}
	leftLines = append(leftLines, "")
	leftLines = append(leftLines, fmt.Sprintf("  Local OS:    %s", bold.Render(localOSLabel)))
	leftLines = append(leftLines, fmt.Sprintf("  Remote OS:   %s", osToggleLabel))
	leftLines = append(leftLines, hostHeader)
	leftLines = append(leftLines, hostVal)

	// 2. RIGHT COLUMN: interactive selector menu + guide text box
	guides := []string{
		"Option 1: One-Click Connection (Recommended)",
		"Option 2: Local Client CLI Setup (Mac/PC)",
		"Option 3: Manual Step-by-Step Alternative",
		"Option 4: Multi-Server Support & Tunneling",
	}

	stacked := m.width > 0 && m.width < 95

	// Width of right panel card
	var rWidth int
	if stacked {
		rWidth = m.width - 10
		if rWidth < 30 {
			rWidth = 40
		}
	} else {
		rWidth = m.width - 66
		if rWidth < 30 {
			rWidth = 30
		}
	}

	// Render the guide content
	var guideBody string
	switch m.selectedGuide {
	case 0:
		guideBody = renderOneClickGuide(m.sshHost, m.remoteOS, tokenVal, s.port, rWidth)
	case 1:
		guideBody = renderClientCLIGuide()
	case 2:
		guideBody = renderManualStepsGuide(tokenVal, s.port, m.sshHost, m.remoteOS)
	case 3:
		guideBody = renderMultiServerGuide()
	}

	// Combine selector and guide content into rightLines
	var rightLines []string
	rightLines = append(rightLines, cyan.Render("CHOOSE SETUP GUIDE"))
	rightLines = append(rightLines, "")
	for i, gName := range guides {
		bullet := "  "
		style := lipgloss.NewStyle().Foreground(ColorDim)
		if m.selectedGuide == i {
			bullet = "▸ "
			style = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
		}
		rightLines = append(rightLines, fmt.Sprintf("%s%s", bullet, style.Render(gName)))
	}
	rightLines = append(rightLines, "")
	rightLines = append(rightLines, dim.Render(strings.Repeat("─", rWidth)))
	rightLines = append(rightLines, "")

	// Split guideBody by newline and append each line to rightLines
	bodyLines := strings.Split(guideBody, "\n")
	rightLines = append(rightLines, bodyLines...)

	// Wrap long lines in rightLines that exceed rWidth
	var wrappedRightLines []string
	for _, line := range rightLines {
		if visibleWidth(line) > rWidth {
			wrapped := wrapText(line, rWidth)
			wrappedRightLines = append(wrappedRightLines, wrapped...)
		} else {
			wrappedRightLines = append(wrappedRightLines, line)
		}
	}
	rightLines = wrappedRightLines

	// Mathematical Height Alignment for horizontal layout
	if !stacked {
		if len(leftLines) < len(rightLines) {
			diff := len(rightLines) - len(leftLines)
			for i := 0; i < diff; i++ {
				leftLines = append(leftLines, "")
			}
		} else if len(rightLines) < len(leftLines) {
			diff := len(leftLines) - len(rightLines)
			for i := 0; i < diff; i++ {
				rightLines = append(rightLines, "")
			}
		}
	}

	var paddedLeftLines []string
	for _, line := range leftLines {
		paddedLeftLines = append(paddedLeftLines, padLine(line, 44))
	}

	var paddedRightLines []string
	for _, line := range rightLines {
		paddedRightLines = append(paddedRightLines, padLine(line, rWidth))
	}

	leftCol := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Render(strings.Join(paddedLeftLines, "\n"))

	rightCol := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorSecondary).
		Padding(1, 2).
		Render(strings.Join(paddedRightLines, "\n"))

	// Combine Left and Right Views responsively
	var mainLayout string
	if !stacked {
		mainLayout = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "    ", rightCol)
	} else {
		mainLayout = lipgloss.JoinVertical(lipgloss.Left, leftCol, "", rightCol)
	}

	var b strings.Builder
	b.WriteString(mainLayout + "\n\n")

	// Status line if any
	if m.statusMsg != "" && time.Since(m.statusAt) < 5*time.Second {
		msgStyle := lipgloss.NewStyle().
			Foreground(ColorSuccess).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorSuccess).
			Padding(0, 2)
		if !m.statusOK {
			msgStyle = msgStyle.Foreground(ColorWarning).BorderForeground(ColorWarning)
		}
		b.WriteString(msgStyle.Render(m.statusMsg) + "\n\n")
	}

	return b.String()
}

func renderOneClickGuide(sshHost, remoteOS, tokenVal string, port int, rWidth int) string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dim := lipgloss.NewStyle().Foreground(ColorDim)

	exePath, err := os.Executable()
	if err != nil {
		exePath = "auxly"
	} else if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	var tunnelCmd string
	if runtime.GOOS == "windows" {
		if remoteOS == "linux" {
			tunnelCmd = fmt.Sprintf(
				`$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o $env:TEMP\auxly-linux && scp $env:TEMP\auxly-linux %s:/usr/local/bin/auxly && Remove-Item $env:TEMP\auxly-linux && ssh %s "auxly bridge setup --token %s --port %d" && ssh -R %d:127.0.0.1:%d %s`,
				sshHost, sshHost, tokenVal, port, port, port, sshHost,
			)
		} else if remoteOS == "windows" {
			tunnelCmd = fmt.Sprintf(
				`$env:GOOS="windows"; $env:GOARCH="amd64"; go build -o $env:TEMP\auxly.exe && scp $env:TEMP\auxly.exe %s:C:/Users/Public/auxly.exe && Remove-Item $env:TEMP\auxly.exe && ssh %s "C:/Users/Public/auxly.exe bridge setup --token %s --port %d" && ssh -R %d:127.0.0.1:%d %s`,
				sshHost, sshHost, tokenVal, port, port, port, sshHost,
			)
		} else { // macOS remote
			tunnelCmd = fmt.Sprintf(
				`scp "%s" %s:/usr/local/bin/auxly && ssh %s "auxly bridge setup --token %s --port %d" && ssh -R %d:127.0.0.1:%d %s`,
				exePath, sshHost, sshHost, tokenVal, port, port, port, sshHost,
			)
		}
	} else {
		if remoteOS == "linux" {
			tunnelCmd = fmt.Sprintf(
				"GOOS=linux GOARCH=amd64 go build -o /tmp/auxly-linux && scp /tmp/auxly-linux %s:/usr/local/bin/auxly && rm /tmp/auxly-linux && ssh %s \"auxly bridge setup --token %s --port %d\" && ssh -R %d:127.0.0.1:%d %s",
				sshHost, sshHost, tokenVal, port, port, port, sshHost,
			)
		} else if remoteOS == "windows" {
			tunnelCmd = fmt.Sprintf(
				"GOOS=windows GOARCH=amd64 go build -o /tmp/auxly.exe && scp /tmp/auxly.exe %s:C:/Users/Public/auxly.exe && rm /tmp/auxly.exe && ssh %s \"C:/Users/Public/auxly.exe bridge setup --token %s --port %d\" && ssh -R %d:127.0.0.1:%d %s",
				sshHost, sshHost, tokenVal, port, port, port, sshHost,
			)
		} else { // macOS remote
			tunnelCmd = fmt.Sprintf(
				"scp %s %s:/usr/local/bin/auxly && ssh %s \"auxly bridge setup --token %s --port %d\" && ssh -R %d:127.0.0.1:%d %s",
				exePath, sshHost, sshHost, tokenVal, port, port, port, sshHost,
			)
		}
	}

	var b strings.Builder
	b.WriteString(cyan.Render("ONE-CLICK AUTOMATED TUNNEL CONNECTION") + "\n\n")

	warningWidth := rWidth - 6
	if warningWidth < 30 {
		warningWidth = 30
	}
	warningStyle := lipgloss.NewStyle().
		Foreground(ColorWarning).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(0, 1).
		Width(warningWidth).
		Bold(true)

	warningMsg := "IMPORTANT: THIS COMMAND MUST BE RUN ON YOUR LOCAL MACHINE\n" +
		"   (Do NOT execute this command on your remote SSH server!)"
	b.WriteString(warningStyle.Render(warningMsg) + "\n\n")

	b.WriteString("  Automatically builds, copies, runs setup, and binds secure reverse port tunnel:\n\n")
	
	cmdWidth := rWidth - 6
	if cmdWidth < 30 {
		cmdWidth = 30
	}
	b.WriteString(lipgloss.NewStyle().Foreground(ColorSecondary).Width(cmdWidth).Render(tunnelCmd) + "\n\n")
	b.WriteString(dim.Render("  [ Press [ t ] or click below selector to copy automated command ]"))

	return b.String()
}

func renderClientCLIGuide() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	bold := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(ColorDim)

	var b strings.Builder
	b.WriteString(cyan.Render("LOCAL CLIENT INTEGRATION (MAC/PC)") + "\n\n")
	b.WriteString("  To use Auxly Memory locally with standard AI IDEs or CLIs:\n\n")
	
	b.WriteString("  • " + bold.Render("Claude Code CLI:") + "\n")
	b.WriteString("    Run: " + bold.Render("claude mcp add auxly-memory /Users/lab/.local/bin/auxly mcp-server") + "\n\n")
	
	b.WriteString("  • " + bold.Render("Cursor IDE:") + "\n")
	b.WriteString("    1. Open Cursor Settings -> Features -> MCP.\n")
	b.WriteString("    2. Add new MCP server: name=\"auxly-memory\", type=\"stdio\", command=\"/Users/lab/.local/bin/auxly\", args=\"mcp-server\".\n\n")
	
	b.WriteString("  • " + bold.Render("Codex IDE:") + "\n")
	b.WriteString("    Add the stdio server config to your " + bold.Render(".codex/config.toml") + ".\n\n")
	
	b.WriteString(dim.Render("  Local configurations automatically sync to global/local memory targets."))
	return b.String()
}

func renderManualStepsGuide(tokenVal string, port int, sshHost, remoteOS string) string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	bold := lipgloss.NewStyle().Bold(true)

	var b strings.Builder
	b.WriteString(cyan.Render("MANUAL STEP-BY-STEP ALTERNATIVE") + "\n\n")
	b.WriteString("  Use these steps if you don't have a Go compiler installed locally:\n\n")
	
	b.WriteString("  1. " + bold.Render("Install Auxly on the REMOTE server:") + "\n")
	if remoteOS == "windows" {
		b.WriteString("     PowerShell:  " + bold.Render("iwr -useb https://get.auxly.io/cli.ps1 | iex") + "\n")
	} else {
		b.WriteString("     Shell:       " + bold.Render("curl -sSL https://get.auxly.io/cli | bash") + "\n")
	}
	b.WriteString("\n")
	
	b.WriteString("  2. " + bold.Render("Configure MCP bridge token on the REMOTE server:") + "\n")
	b.WriteString(fmt.Sprintf("     Command:     %s\n\n", bold.Render(fmt.Sprintf("auxly bridge setup --token %s --port %d", tokenVal, port))))
	
	b.WriteString("  3. " + bold.Render("Establish SSH Reverse Tunnel from your LOCAL machine's terminal:") + "\n")
	b.WriteString(fmt.Sprintf("     Command:     %s\n", bold.Render(fmt.Sprintf("ssh -R %d:127.0.0.1:%d %s", port, port, sshHost))))
	
	return b.String()
}

func renderMultiServerGuide() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	bold := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(ColorDim)

	var b strings.Builder
	b.WriteString(cyan.Render("MULTI-SERVER SUPPORT & TUNNELING") + "\n\n")
	b.WriteString("  Yes! You can connect multiple SSH tunnels to different servers simultaneously.\n\n")
	b.WriteString("  • " + bold.Render("Simultaneous Connections:") + "\n")
	b.WriteString("    Just run the SSH reverse tunnel command on your local machine for each remote server.\n")
	b.WriteString("    All of them will safely share this single local memory gateway in real-time!\n\n")
	b.WriteString("  • " + bold.Render("Isolation:") + "\n")
	b.WriteString("    Each remote workspace maintains its own project-specific override files while\n")
	b.WriteString("    seamlessly pulling from and pushing to the global core memories.\n\n")
	b.WriteString(dim.Render("  Perfect for managing dev, staging, and production environments together."))
	return b.String()
}
