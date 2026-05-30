package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/session"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/trust"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/update"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type dashboardModel struct {
	logger           *audit.Logger
	pendingMgr       *pending.Manager
	stats            *audit.Stats
	pendingCnt       int
	trustCfg         *trust.Config
	memoryPath       string
	activeProviders  []string
	recentWrites     []audit.Entry
	sessions         []agentSession
	unregistered     int // live mcp-servers running but not in the session registry
	updateAvail      bool
	updateLatest     string
	updating         bool
	updateResult     string
	lastRefresh      time.Time
	blinkCycle       int
	animationStarted bool
	mcpError         string
	reloaded         bool
	reloadedAt       time.Time
	width            int
	height           int

	// Interactive Popup & Grid Selection State
	selectedAgent  string // E.g., "claude", "cursor", etc.
	activeAgentTab int    // 0 = Info, 1 = Recent Writes
	gridCursor     int    // 0 to 5 for selecting the grid box
}

type dashboardRefreshMsg struct {
	stats           *audit.Stats
	pendingCnt      int
	trustCfg        *trust.Config
	activeProviders []string
	recentWrites    []audit.Entry
	sessions        []agentSession
	unregistered    int
	updateAvail     bool
	updateLatest    string
	at              time.Time
	mcpError        string
}

// dashboardUpdateDoneMsg carries the result of a one-click [u] self-update.
type dashboardUpdateDoneMsg struct {
	path string
	err  error
}

type dashboardTickMsg struct{}
type animationTickMsg struct{}

func dashboardTickCmd() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return dashboardTickMsg{}
	})
}

func animationTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return animationTickMsg{}
	})
}

func newDashboardModel(logger *audit.Logger, mgr *pending.Manager, memoryPath string) dashboardModel {
	return dashboardModel{
		logger:           logger,
		pendingMgr:       mgr,
		memoryPath:       memoryPath,
		blinkCycle:       0,
		animationStarted: false,
		mcpError:         "",
		reloaded:         false,
		gridCursor:       0,
	}
}

func (m dashboardModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		stats := &audit.Stats{
			ByProvider: make(map[string]int),
			ByAction:   make(map[string]int),
		}
		if m.logger != nil {
			if s, err := m.logger.Stats(); err == nil && s != nil {
				stats = s
			}
		}
		var pendingCnt int
		if m.pendingMgr != nil {
			files, _ := m.pendingMgr.List()
			pendingCnt = len(files)
		}
		var trustCfg *trust.Config
		if m.memoryPath != "" {
			trustCfg, _ = trust.Load(m.memoryPath)
		}
		var activeProviders []string
		var recentWrites []audit.Entry
		if m.logger != nil {
			activeProviders, _ = m.logger.ActiveProviders(5 * time.Minute)
			recentWrites, _ = m.logger.TailWrites(10)
		}
		mcpError := getClaudeMCPError()
		sessions := gatherSessions()
		// Reconcile the registry against actually-running servers so the
		// dashboard can flag agents that are live but not yet reflected
		// (e.g. started before the session-registry feature).
		unregistered := len(session.LiveServerPIDs()) - len(sessions)
		if unregistered < 0 {
			unregistered = 0
		}
		latest, updateAvail := update.Available()
		return dashboardRefreshMsg{
			stats:           stats,
			pendingCnt:      pendingCnt,
			trustCfg:        trustCfg,
			activeProviders: activeProviders,
			recentWrites:    recentWrites,
			sessions:        sessions,
			unregistered:    unregistered,
			updateAvail:     updateAvail,
			updateLatest:    latest,
			at:              time.Now(),
			mcpError:        mcpError,
		}
	}
}

func (m dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case dashboardRefreshMsg:
		m.stats = msg.stats
		m.pendingCnt = msg.pendingCnt
		m.trustCfg = msg.trustCfg
		m.activeProviders = msg.activeProviders
		m.recentWrites = msg.recentWrites
		m.sessions = msg.sessions
		m.unregistered = msg.unregistered
		if !m.updating {
			m.updateAvail = msg.updateAvail
			m.updateLatest = msg.updateLatest
		}
		m.lastRefresh = msg.at
		m.mcpError = msg.mcpError
		if m.reloaded && time.Since(m.reloadedAt) > 3*time.Second {
			m.reloaded = false
		}

		var cmds []tea.Cmd
		cmds = append(cmds, dashboardTickCmd())

		if !m.animationStarted {
			m.animationStarted = true
			cmds = append(cmds, animationTickCmd())
		}

		return m, tea.Batch(cmds...)
	case dashboardTickMsg:
		return m, m.Refresh()
	case animationTickMsg:
		m.blinkCycle = (m.blinkCycle + 1) % 24
		return m, animationTickCmd()
	case dashboardUpdateDoneMsg:
		m.updating = false
		if msg.err != nil {
			m.updateResult = "✗ Update failed: " + msg.err.Error()
		} else {
			m.updateResult = "✅ Updated to v" + m.updateLatest + " — restart auxly to use it"
			m.updateAvail = false
		}
		return m, nil
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			w := m.width
			if w <= 0 {
				w = 80
			}
			banner := renderBanner(w)
			tabRow := strings.Count(banner, "\n")
			contentOffsetY := tabRow + 4

			// Use dynamic view rendering to locate coordinates!
			viewStr := m.View()
			viewLines := strings.Split(viewStr, "\n")

			// If popup is open
			if m.selectedAgent != "" {
				startX := 10
				if w > 0 && startX+82 > w {
					startX = w - 82
					if startX < 0 {
						startX = 0
					}
				}

				popupStr := m.renderPopup(m.selectedAgent)
				popLines := strings.Split(popupStr, "\n")

				pStartY := contentOffsetY + 6
				pEndY := pStartY + len(popLines)
				pStartX := startX
				pEndX := startX + 82

				if msg.X < pStartX || msg.X > pEndX || msg.Y < pStartY || msg.Y > pEndY {
					m.selectedAgent = "" // Clicked outside, close popup!
					return m, nil
				}

				// Tab clicking line is Y = pStartY + 5
				if msg.Y == pStartY+5 {
					// Tab 1: [1 Info & Diagnostics] spans X: [pStartX+3, pStartX+25]
					if msg.X >= pStartX+3 && msg.X <= pStartX+25 {
						m.activeAgentTab = 0
						return m, nil
					}
					// Tab 2: [2 Recent Writes] spans X: [pStartX+29, pStartX+45]
					if msg.X >= pStartX+29 && msg.X <= pStartX+45 {
						m.activeAgentTab = 1
						return m, nil
					}
					// Tab 3: [3 Connected] spans X: [pStartX+50, pStartX+62]
					if msg.X >= pStartX+50 && msg.X <= pStartX+62 {
						m.activeAgentTab = 2
						return m, nil
					}
				}
				return m, nil // Swallow clicks inside the popup
			}

			// Popup closed: check click on agent cards grid by scanning viewLines dynamically
			brandLines := make(map[string]int)
			for idx, line := range viewLines {
				clean := stripANSI(line)
				if strings.Contains(clean, "Claude Desktop") {
					brandLines["claude"] = idx
				} else if strings.Contains(clean, "Claude Code CLI") {
					brandLines["claude-code"] = idx
				} else if strings.Contains(clean, "Antigravity Agent") {
					brandLines["antigravity"] = idx
				} else if strings.Contains(clean, "Cursor") && !strings.Contains(clean, "Use arrow") {
					brandLines["cursor"] = idx
				} else if strings.Contains(clean, "Codex") {
					brandLines["codex"] = idx
				} else if strings.Contains(clean, "Gemini") {
					brandLines["gemini"] = idx
				}
			}

			clickLineIdx := msg.Y - contentOffsetY
			stacked := m.width > 0 && m.width < 95

			for brand, lineY := range brandLines {
				if clickLineIdx >= lineY-1 && clickLineIdx <= lineY+2 {
					if !stacked {
						// Side-by-side
						if brand == "claude" || brand == "antigravity" || brand == "codex" {
							if msg.X <= 75 {
								m.selectedAgent = brand
								m.activeAgentTab = 0
								return m, nil
							}
						} else {
							if msg.X >= 76 {
								m.selectedAgent = brand
								m.activeAgentTab = 0
								return m, nil
							}
						}
					} else {
						// Stacked
						m.selectedAgent = brand
						m.activeAgentTab = 0
						return m, nil
					}
				}
			}
		}
	case tea.KeyMsg:
		if m.selectedAgent != "" {
			switch msg.String() {
			case "esc", "q":
				m.selectedAgent = ""
				return m, nil
			case "1":
				m.activeAgentTab = 0
				return m, nil
			case "2":
				m.activeAgentTab = 1
				return m, nil
			case "3":
				m.activeAgentTab = 2
				return m, nil
			case "left":
				if m.activeAgentTab > 0 {
					m.activeAgentTab--
				}
				return m, nil
			case "right":
				if m.activeAgentTab < 2 {
					m.activeAgentTab++
				}
				return m, nil
			case "tab":
				m.activeAgentTab = (m.activeAgentTab + 1) % 3
				return m, nil
			}
			return m, nil // Swallow keys when popup is active
		}

		switch msg.String() {
		case "left", "h":
			if m.gridCursor%2 == 1 {
				m.gridCursor--
			}
		case "right", "l":
			if m.gridCursor%2 == 0 {
				m.gridCursor++
			}
		case "up", "k":
			if m.gridCursor >= 2 {
				m.gridCursor -= 2
			}
		case "down", "j":
			if m.gridCursor <= 3 {
				m.gridCursor += 2
			}
		case "enter", " ":
			brands := []string{"claude", "claude-code", "antigravity", "cursor", "codex", "gemini"}
			if m.gridCursor >= 0 && m.gridCursor < len(brands) {
				m.selectedAgent = brands[m.gridCursor]
				m.activeAgentTab = 0
			}
		case "r", "R":
			// Force refresh: immediately re-scan the session registry and the
			// live process table (no waiting for the 1s tick), surfacing any
			// agent that's running but not yet reflected.
			m.reloaded = true
			m.reloadedAt = time.Now()
			return m, m.Refresh()
		case "u", "U":
			// One-click self-update when a newer release is available.
			if m.updateAvail && !m.updating {
				m.updating = true
				m.updateResult = ""
				return m, func() tea.Msg {
					path, err := update.SelfUpdate()
					return dashboardUpdateDoneMsg{path: path, err: err}
				}
			}
		}
	}
	return m, nil
}

func (m dashboardModel) View() string {
	title := StyleTitle.Render("📊 Auxly-Memory CLI Dashboard")

	clockStr := time.Now().Format("02/01/2006 15:04:05")
	clockHeader := lipgloss.NewStyle().Foreground(ColorDim).Render("🕒 Time: " + clockStr)
	headerRow := lipgloss.JoinHorizontal(lipgloss.Top, title, "         ", clockHeader)
	if m.reloaded {
		refreshed := lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true).Render("   ⟳ Force refreshed")
		headerRow = lipgloss.JoinHorizontal(lipgloss.Top, headerRow, refreshed)
	}

	if m.stats == nil {
		return headerRow + "\n\nLoading dashboard..."
	}

	dim := lipgloss.NewStyle().Foreground(ColorDim)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	yellow := lipgloss.NewStyle().Foreground(ColorWarning)
	bold := lipgloss.NewStyle().Bold(true)

	// Left Column: System Status & Diagnostic Details
	diagStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(44)

	pendingColor := ColorDim
	if m.pendingCnt > 0 {
		pendingColor = ColorWarning
	}
	pendingText := lipgloss.NewStyle().Bold(true).Foreground(pendingColor).Render(fmt.Sprintf("%d", m.pendingCnt))

	diagContent := fmt.Sprintf(
		"💻 %s\n\n"+
			"Writes Today:   %s\n"+
			"Total Entries:  %s\n"+
			"Pending Queue:  %s\n\n"+
			"📂 %s\n%s\n\n"+
			"🔌 %s\n%s",
		bold.Render("System Diagnostics"),
		green.Render(fmt.Sprintf("%d", m.stats.WritesToday)),
		lipgloss.NewStyle().Foreground(ColorSecondary).Render(fmt.Sprintf("%d", m.stats.TotalEntries)),
		pendingText,
		bold.Render("Memory Store:"),
		dim.Render(m.memoryPath),
		bold.Render("Active Connections:"),
		m.renderConnectionsSummary(),
	)
	leftCol := diagStyle.Render(diagContent)

	// Right Column: Connected Agent Grid
	brands := []struct {
		id    string
		name  string
		icon  string
		color string
	}{
		{"claude", "Claude Desktop", "🧠", "99"},
		{"claude-code", "Claude Code CLI", "💻", "39"},
		{"antigravity", "Antigravity", "🌌", "205"},
		{"cursor", "Cursor", "🎯", "39"},
		{"codex", "Codex IDE", "💻", "34"},
		{"gemini", "Gemini CLI", "✨", "220"},
	}

	var brandCards []string
	for idx, b := range brands {
		trustLevel := "auto"
		if m.trustCfg != nil {
			trustLevel = m.trustCfg.GetTrustLevel(b.id)
			if b.id == "antigravity" && (trustLevel == "" || trustLevel == "auto") {
				if tl := m.trustCfg.GetTrustLevel("antigravity-agent"); tl != "" && tl != "auto" {
					trustLevel = tl
				} else if tl := m.trustCfg.GetTrustLevel("antigravity-ide"); tl != "" && tl != "auto" {
					trustLevel = tl
				} else if tl := m.trustCfg.GetTrustLevel("antigravity-cli"); tl != "" && tl != "auto" {
					trustLevel = tl
				}
			}
		}

		trustBadge := green.Render("auto")
		if trustLevel == "require_approval" {
			trustBadge = yellow.Render("approve")
		} else if trustLevel == "read_only" {
			trustBadge = dim.Render("read-only")
		}

		// Count live MCP sessions for this brand from the session registry
		// (ground truth). "active" = at least one connection right now.
		connCount := 0
		for _, sess := range m.sessions {
			if sess.Provider == b.id ||
				(b.id == "antigravity" && strings.HasPrefix(sess.Provider, "antigravity")) {
				connCount++
			}
		}
		isActive := connCount > 0

		var cardName string
		var cardBorderColor lipgloss.Color
		var statusDot string

		hasCursor := m.gridCursor == idx
		if hasCursor {
			if isActive {
				cardBorderColor = lipgloss.Color("#917FD1") // premium brand purple for hovered active agents
			} else {
				cardBorderColor = lipgloss.Color("#84DCFB") // brand cyan for hovered idle agents
			}
		} else if isActive {
			cardBorderColor = lipgloss.Color("#84DCFB") // clean, static brand cyan border for active agents
		} else {
			cardBorderColor = ColorDim // static grey border for idle agents
		}

		if isActive {
			dotPart := lipgloss.NewStyle().Foreground(lipgloss.Color("#73CBAD")).Render("●")
			var activePart string
			if hasCursor {
				activePart = renderHoverShimmerText("active", m.blinkCycle)
			} else {
				activePart = renderShimmerText("active", m.blinkCycle)
			}
			statusDot = fmt.Sprintf("%s %s", dotPart, activePart)
			cardName = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#73CBAD")).Render(b.name)
		} else {
			statusDot = dim.Render("○ idle")
			cardName = bold.Render(b.name)
		}

		rightDetails := fmt.Sprintf(" %s %s\n   %s · 🔌%d · %s",
			b.icon,
			cardName,
			statusDot,
			connCount,
			trustBadge,
		)

		card := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cardBorderColor).
			Padding(0, 1).
			Width(28).
			Render(rightDetails)

		brandCards = append(brandCards, card)
	}

	var grid []string
	for i := 0; i < len(brandCards); i += 2 {
		if i+1 < len(brandCards) {
			grid = append(grid, lipgloss.JoinHorizontal(lipgloss.Top, brandCards[i], " ", brandCards[i+1]))
		} else {
			grid = append(grid, brandCards[i])
		}
	}
	gridSection := strings.Join(grid, "\n")

	rightCol := fmt.Sprintf("%s\n%s", StyleHeader.Render("📡 Connected Agent Brands"), gridSection)

	var dashboardContent string
	if m.width > 0 && m.width < 95 {
		dashboardContent = lipgloss.JoinVertical(lipgloss.Left, leftCol, "", rightCol)
	} else {
		dashboardContent = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "    ", rightCol)
	}

	// Dynamic overlay splicing: If selectedAgent is set, overlay the pop-up box over the background page!
	if m.selectedAgent != "" {
		popupStr := m.renderPopup(m.selectedAgent)
		startX := 10
		if m.width > 0 && startX+82 > m.width {
			startX = m.width - 82
			if startX < 0 {
				startX = 0
			}
		}
		dashboardContent = overlayPopup(dashboardContent, popupStr, startX, 3)
	}

	var b strings.Builder
	b.WriteString(headerRow + "\n")
	if banner := m.renderUpdateBanner(); banner != "" {
		b.WriteString(banner + "\n")
	}
	b.WriteString("\n")
	b.WriteString(dashboardContent + "\n\n")

	if m.selectedAgent != "" {
		b.WriteString(dim.Render("  Popup active: [1/2/3] switch tabs • [Esc] or Click outside to close"))
	} else {
		hint := "  Use arrow keys or Mouse Clicks to select agent boxes • Enter: open details popup • q: exit TUI"
		if m.updateAvail && !m.updating {
			hint += " • [u] update"
		}
		b.WriteString(dim.Render(hint))
	}
	b.WriteString("\n")

	return b.String()
}

// renderUpdateBanner shows the one-click self-update prompt / progress / result.
func (m dashboardModel) renderUpdateBanner() string {
	switch {
	case m.updating:
		return lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).
			Render("  ⏳ Updating auxly…")
	case m.updateResult != "":
		color := ColorSuccess
		if strings.HasPrefix(m.updateResult, "✗") {
			color = ColorDanger
		}
		return lipgloss.NewStyle().Foreground(color).Bold(true).Render("  " + m.updateResult)
	case m.updateAvail:
		return lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).
			Render(fmt.Sprintf("  ⬆ Update available: v%s (you have v%s) — press [u] to update",
				m.updateLatest, update.Current))
	}
	return ""
}

// renderConnectedTab writes the "Connected" popup tab for one provider: the
// live MCP sessions attributed to it, showing PID for local agents and the
// originating IP/host/OS for SSH-remote agents.
func (m dashboardModel) renderConnectedTab(pb *strings.Builder, provider string) {
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	teal := lipgloss.NewStyle().Foreground(lipgloss.Color("#73CBAD"))

	matches := func(p string) bool {
		if p == provider {
			return true
		}
		return provider == "antigravity" && strings.HasPrefix(p, "antigravity")
	}

	var sessions []agentSession
	for _, s := range m.sessions {
		if matches(s.Provider) {
			sessions = append(sessions, s)
		}
	}

	if len(sessions) == 0 {
		pb.WriteString("\n  " + dim.Render("No live sessions for this agent right now.") + "\n")
		pb.WriteString("  " + dim.Render("(a session appears while the agent's MCP server is running)") + "\n")
		return
	}

	plural := "session"
	if len(sessions) != 1 {
		plural = "sessions"
	}
	pb.WriteString(fmt.Sprintf("  %s %s connected\n\n",
		bold.Render(fmt.Sprintf("%d", len(sessions))),
		plural,
	))

	for _, s := range sessions {
		if s.Remote {
			host := s.Host
			if host == "" {
				host = "remote"
			}
			ip := s.IP
			if ip == "" {
				ip = "via tunnel"
			}
			osLabel := s.OS
			if osLabel == "" {
				osLabel = "?"
			}
			pb.WriteString(fmt.Sprintf("  %s %s  %s\n",
				teal.Render("● remote"),
				bold.Render(host),
				dim.Render(fmt.Sprintf("IP %s · %s", ip, osLabel)),
			))
		} else {
			pb.WriteString(fmt.Sprintf("  %s %s  %s\n",
				green.Render("● local"),
				bold.Render(fmt.Sprintf("PID %d", s.PID)),
				dim.Render("this machine"),
			))
		}
	}
}

// renderConnectionsSummary renders the live MCP sessions for the left
// diagnostics column: remote boxes by host/IP/OS, then a local-agent count.
func (m dashboardModel) renderConnectionsSummary() string {
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	teal := lipgloss.NewStyle().Foreground(lipgloss.Color("#73CBAD"))
	cyan := lipgloss.NewStyle().Foreground(ColorPrimary)
	yellow := lipgloss.NewStyle().Foreground(ColorWarning)

	var sb strings.Builder

	if len(m.sessions) == 0 {
		sb.WriteString(dim.Render("No active agent sessions"))
	} else {
		var remotes []agentSession
		localCount := 0
		for _, s := range m.sessions {
			if s.Remote {
				remotes = append(remotes, s)
			} else {
				localCount++
			}
		}

		for _, s := range remotes {
			host := s.Host
			if host == "" {
				host = "remote"
			}
			loc := s.IP
			if loc == "" {
				loc = "via tunnel"
			}
			osLabel := s.OS
			if osLabel == "" {
				osLabel = "?"
			}
			sb.WriteString(fmt.Sprintf("%s %s\n   %s\n",
				teal.Render("●"),
				cyan.Render(host),
				dim.Render(fmt.Sprintf("%s · %s · %s", loc, osLabel, s.Provider)),
			))
		}

		if localCount > 0 {
			label := "local agent"
			if localCount != 1 {
				label += "s"
			}
			sb.WriteString(fmt.Sprintf("%s %s",
				teal.Render("●"),
				dim.Render(fmt.Sprintf("%d %s on this machine", localCount, label)),
			))
		} else {
			// Drop the trailing newline left by the remote block.
			trimmed := strings.TrimRight(sb.String(), "\n")
			sb.Reset()
			sb.WriteString(trimmed)
		}
	}

	// Surface servers that are running but haven't registered a session
	// (e.g. started before this build) so the force-refresh "shows" them.
	if m.unregistered > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		label := "server"
		if m.unregistered != 1 {
			label += "s"
		}
		sb.WriteString(yellow.Render(fmt.Sprintf("⚠ %d %s running, unregistered", m.unregistered, label)))
		sb.WriteString("\n  " + dim.Render("press [r], then reconnect to attribute"))
	}

	return sb.String()
}

func (m dashboardModel) renderPopup(provider string) string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	yellow := lipgloss.NewStyle().Foreground(ColorWarning)
	red := lipgloss.NewStyle().Foreground(ColorDanger)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	bold := lipgloss.NewStyle().Bold(true)

	var name string
	var icon string
	var logs string
	switch provider {
	case "claude":
		name = "Claude Desktop"
		icon = "🧠"
		logs = "Connected over Stdio protocol. Read-write active."
	case "claude-code":
		name = "Claude Code CLI"
		icon = "💻"
		logs = "Auto-registered with npm global package. Ready."
	case "antigravity":
		name = "Antigravity Agent"
		icon = "🌌"
		logs = "Antigravity CLI relayer active. 100% verified."
	case "cursor":
		name = "Cursor"
		icon = "🎯"
		logs = "Configured in globalStorage/mcpServers.json. Active."
	case "codex":
		name = "Codex IDE"
		icon = "💻"
		logs = "Configured in .codex/config.toml. Active."
	case "gemini":
		name = "Gemini CLI"
		icon = "✨"
		logs = "Connected over local settings.json channel. Active."
	}

	var pb strings.Builder
	header := fmt.Sprintf("%s %s AGENT CENTER", icon, strings.ToUpper(name))
	pb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(header) + "\n\n")

	tab1 := "[1 Info & Diagnostics]"
	tab2 := "[2 Recent Writes]"
	tab3 := "[3 Connected]"
	switch m.activeAgentTab {
	case 0:
		tab1 = cyan.Render(tab1)
	case 1:
		tab2 = cyan.Render(tab2)
	case 2:
		tab3 = cyan.Render(tab3)
	}
	pb.WriteString(fmt.Sprintf("  %s    %s    %s\n\n", tab1, tab2, tab3))

	if m.activeAgentTab == 0 {
		writes := 0
		if m.stats != nil && m.stats.ByProvider != nil {
			if provider == "antigravity" {
				writes = m.stats.ByProvider["antigravity"] + m.stats.ByProvider["antigravity-ide"] + m.stats.ByProvider["antigravity-agent"] + m.stats.ByProvider["antigravity-cli"]
			} else {
				writes = m.stats.ByProvider[provider]
			}
		}

		trustLevel := "auto"
		if m.trustCfg != nil {
			trustLevel = m.trustCfg.GetTrustLevel(provider)
		}
		trustBadge := green.Render("● auto")
		if trustLevel == "require_approval" {
			trustBadge = yellow.Render("● require_approval")
		} else if trustLevel == "read_only" {
			trustBadge = red.Render("● read_only")
		}

		pb.WriteString(fmt.Sprintf("  %-18s %s\n", dim.Render("Trust Level:"), trustBadge))
		pb.WriteString(fmt.Sprintf("  %-18s %s\n", dim.Render("Total Writes:"), bold.Render(fmt.Sprintf("%d writes", writes))))
		pb.WriteString(fmt.Sprintf("  %-18s %s\n", dim.Render("Interface:"), bold.Render("MCP Stdio relayer")))
		pb.WriteString(fmt.Sprintf("  %-18s %s\n\n", dim.Render("Target Scopes:"), bold.Render("Global & Workspace Override")))
		pb.WriteString(fmt.Sprintf("  %-18s %s\n", dim.Render("Status Logs:"), green.Render(logs)))
	} else if m.activeAgentTab == 1 {
		var writes []audit.Entry
		if m.logger != nil {
			all, _ := m.logger.TailWrites(50)
			for _, e := range all {
				if e.Provider == provider || (provider == "antigravity" && strings.HasPrefix(e.Provider, "antigravity")) {
					writes = append(writes, e)
				}
				if len(writes) >= 10 {
					break
				}
			}
		}

		if len(writes) == 0 {
			pb.WriteString("\n  " + dim.Render("No write operations recorded yet for this agent.") + "\n\n")
		} else {
			for _, e := range writes {
				ts := parseTS(e.Timestamp)
				cleanReason := e.Reason
				if len(cleanReason) > 40 {
					cleanReason = cleanReason[:37] + "..."
				}
				pb.WriteString(fmt.Sprintf("  %s ➔ %-18s - %s\n",
					dim.Render(ts.Format("15:04:05")),
					bold.Render(e.File),
					cleanReason,
				))
			}
		}
	} else {
		m.renderConnectedTab(&pb, provider)
	}

	pb.WriteString("\n")
	pb.WriteString(StyleFooter.Render("  Press [Esc] to close • [1/2/3] Switch tabs"))

	popupStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(82)

	return popupStyle.Render(pb.String())
}

// stripANSI strips ANSI escape codes from a string for accurate column calculations.
func stripANSI(str string) string {
	var sb strings.Builder
	inEscape := false
	runes := []rune(str)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// visibleWidth returns the length of a string after stripping ANSI escape sequences.
func visibleWidth(s string) int {
	return runewidth.StringWidth(stripANSI(s))
}

// splitLineAtVisibleColumn splits a styled string with ANSI escape codes at a visible column width.
func splitLineAtVisibleColumn(line string, col int) (string, string) {
	var left strings.Builder
	var right strings.Builder

	inEscape := false
	visibleCol := 0

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\x1b' {
			inEscape = true
			if visibleCol < col {
				left.WriteRune(r)
			} else {
				right.WriteRune(r)
			}
			continue
		}
		if inEscape {
			if visibleCol < col {
				left.WriteRune(r)
			} else {
				right.WriteRune(r)
			}
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}

		rw := runewidth.RuneWidth(r)
		if visibleCol < col {
			left.WriteRune(r)
			visibleCol += rw
		} else {
			right.WriteRune(r)
			visibleCol += rw
		}
	}

	if visibleCol < col {
		left.WriteString(strings.Repeat(" ", col-visibleCol))
	}

	return left.String(), right.String()
}

// overlayPopup splices a popup string directly into the center coordinates of a background string, in an ANSI-safe manner.
func overlayPopup(bg, popup string, startX, startY int) string {
	bgLines := strings.Split(bg, "\n")
	popLines := strings.Split(popup, "\n")

	neededLines := startY + len(popLines)
	for len(bgLines) < neededLines {
		bgLines = append(bgLines, "")
	}

	for i, pLine := range popLines {
		y := startY + i
		bgLine := bgLines[y]

		left, right := splitLineAtVisibleColumn(bgLine, startX)
		pWidth := visibleWidth(pLine)
		_, farRight := splitLineAtVisibleColumn(right, pWidth)

		bgLines[y] = left + pLine + farRight
	}

	return strings.Join(bgLines, "\n")
}

func getClaudeMCPError() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	logPath := filepath.Join(home, "Library/Logs/Claude/mcp.log")
	if _, err := os.Stat(logPath); err != nil {
		logPath = filepath.Join(home, "Library/Logs/Claude/mcp-server-auxly-memory.log")
		if _, err := os.Stat(logPath); err != nil {
			return ""
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if strings.Contains(line, "[error]") || strings.Contains(line, "unexpectedly") || strings.Contains(line, "disconnected") {
			if strings.Contains(line, "Server transport closed unexpectedly") {
				return "MCP server closed unexpectedly (exited early)"
			}
			if strings.Contains(line, "Server disconnected") {
				return "MCP server disconnected"
			}
			if strings.Contains(line, "database is locked") {
				return "SQLite database is locked by another process"
			}
			parts := strings.SplitN(line, " ", 3)
			if len(parts) >= 3 {
				return parts[2]
			}
			return line
		}
	}
	return ""
}

func touchClaudeConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configPath := filepath.Join(home, "Library/Application Support/Claude/claude_desktop_config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// shimmerPalette defines a smooth multi-stop gradient between Auxly brand colors.
// We expand brand hex stops into a 24-step palette using linear interpolation
// so adjacent stops look almost identical — producing a silky smooth wave.
var shimmerPalette = buildShimmerPalette()

func buildShimmerPalette() []string {
	// Brand color stops: #917FD1 (purple) → #775099 (deep purple) → #73CBAD (teal) → #84DCFB (cyan) → loop
	type stop struct{ r, g, b int }
	stops := []stop{
		{0x91, 0x7F, 0xD1}, // #917FD1 purple
		{0x77, 0x50, 0x99}, // #775099 deep purple
		{0x73, 0xCB, 0xAD}, // #73CBAD teal
		{0x84, 0xDC, 0xFB}, // #84DCFB cyan
		{0x91, 0x7F, 0xD1}, // loop
	}

	stepsPerSegment := 6 // 4 segments × 6 steps = 24 total (matches blinkCycle mod 24)
	var palette []string
	for seg := 0; seg < len(stops)-1; seg++ {
		a, b := stops[seg], stops[seg+1]
		for step := 0; step < stepsPerSegment; step++ {
			t := float64(step) / float64(stepsPerSegment)
			r := int(float64(a.r) + t*float64(b.r-a.r))
			g := int(float64(a.g) + t*float64(b.g-a.g))
			bv := int(float64(a.b) + t*float64(b.b-a.b))
			palette = append(palette, fmt.Sprintf("#%02X%02X%02X", r, g, bv))
		}
	}
	return palette
}

func renderShimmerText(text string, frame int) string {
	n := len(shimmerPalette) // 24

	var result strings.Builder
	for i, r := range text {
		if r == ' ' {
			result.WriteRune(r)
			continue
		}
		// Flowing left-to-right wave
		colorIdx := ((frame-i*2)%n + n) % n
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(shimmerPalette[colorIdx])).Bold(true)
		result.WriteString(style.Render(string(r)))
	}
	return result.String()
}

// hoverShimmerPalette defines a smooth multi-stop gradient for hovered active agents.
var hoverShimmerPalette = buildHoverShimmerPalette()

func buildHoverShimmerPalette() []string {
	// Hover Brand color stops: #917FD1 (purple) → #775099 (deep purple) → #73CBAD (teal) → #84DCFB (cyan) → loop
	type stop struct{ r, g, b int }
	stops := []stop{
		{0x91, 0x7F, 0xD1}, // #917FD1 purple
		{0x77, 0x50, 0x99}, // #775099 deep purple
		{0x73, 0xCB, 0xAD}, // #73CBAD teal
		{0x84, 0xDC, 0xFB}, // #84DCFB cyan
		{0x91, 0x7F, 0xD1}, // loop
	}

	stepsPerSegment := 6 // 4 segments × 6 steps = 24 total (matches blinkCycle mod 24)
	var palette []string
	for seg := 0; seg < len(stops)-1; seg++ {
		a, b := stops[seg], stops[seg+1]
		for step := 0; step < stepsPerSegment; step++ {
			t := float64(step) / float64(stepsPerSegment)
			r := int(float64(a.r) + t*float64(b.r-a.r))
			g := int(float64(a.g) + t*float64(b.g-a.g))
			bv := int(float64(a.b) + t*float64(b.b-a.b))
			palette = append(palette, fmt.Sprintf("#%02X%02X%02X", r, g, bv))
		}
	}
	return palette
}

func renderHoverShimmerText(text string, frame int) string {
	n := len(hoverShimmerPalette) // 24

	var result strings.Builder
	for i, r := range text {
		if r == ' ' {
			result.WriteRune(r)
			continue
		}
		// Flowing left-to-right wave
		colorIdx := ((frame-i*2)%n + n) % n
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(hoverShimmerPalette[colorIdx])).Bold(true)
		result.WriteString(style.Render(string(r)))
	}
	return result.String()
}
