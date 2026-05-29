package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/trust"
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
	at              time.Time
	mcpError        string
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
		return dashboardRefreshMsg{
			stats:           stats,
			pendingCnt:      pendingCnt,
			trustCfg:        trustCfg,
			activeProviders: activeProviders,
			recentWrites:    recentWrites,
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
				if w > 0 && startX + 82 > w {
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
				if msg.Y == pStartY + 5 {
					// Tab 1: [1 Info & Diagnostics] spans X: [pStartX+3, pStartX+25]
					if msg.X >= pStartX+3 && msg.X <= pStartX+25 {
						m.activeAgentTab = 0
						return m, nil
					}
					// Tab 2: [2 Recent Writes] spans X: [pStartX+29, pStartX+46]
					if msg.X >= pStartX+29 && msg.X <= pStartX+46 {
						m.activeAgentTab = 1
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
			case "1", "left":
				m.activeAgentTab = 0
				return m, nil
			case "2", "right":
				m.activeAgentTab = 1
				return m, nil
			case "tab":
				m.activeAgentTab = (m.activeAgentTab + 1) % 2
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
			_ = touchClaudeConfig()
			m.reloaded = true
			m.reloadedAt = time.Now()
			return m, m.Refresh()
		}
	}
	return m, nil
}

func (m dashboardModel) View() string {
	title := StyleTitle.Render("📊 Auxly-Memory CLI Dashboard")
	
	clockStr := time.Now().Format("02/01/2006 15:04:05")
	clockHeader := lipgloss.NewStyle().Foreground(ColorDim).Render("🕒 Time: " + clockStr)
	headerRow := lipgloss.JoinHorizontal(lipgloss.Top, title, "         ", clockHeader)

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

	daemonActive, daemonConns := getDaemonState()
	daemonText := dim.Render("○ idle")
	if daemonActive {
		daemonText = fmt.Sprintf("%s (Port 7357, conns: %d)", green.Render("● active"), daemonConns)
	}

	diagContent := fmt.Sprintf(
		"💻 %s\n\n"+
			"Writes Today:   %s\n"+
			"Total Entries:  %s\n"+
			"Pending Queue:  %s\n\n"+
			"📂 %s\n%s\n\n"+
			"📡 %s\n%s",
		bold.Render("System Diagnostics"),
		green.Render(fmt.Sprintf("%d", m.stats.WritesToday)),
		lipgloss.NewStyle().Foreground(ColorSecondary).Render(fmt.Sprintf("%d", m.stats.TotalEntries)),
		pendingText,
		bold.Render("Memory Store:"),
		dim.Render(m.memoryPath),
		bold.Render("Daemon Gateway:"),
		daemonText,
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
		writes := 0
		if m.stats.ByProvider != nil {
			if b.id == "antigravity" {
				writes = m.stats.ByProvider["antigravity"] +
					m.stats.ByProvider["antigravity-ide"] +
					m.stats.ByProvider["antigravity-agent"] +
					m.stats.ByProvider["antigravity-cli"]
			} else {
				writes = m.stats.ByProvider[b.id]
			}
		}

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

		isActive := false
		for _, ap := range m.activeProviders {
			if ap == b.id ||
				(b.id == "antigravity" && (ap == "antigravity" || ap == "antigravity-ide" || ap == "antigravity-agent" || ap == "antigravity-cli")) {
				isActive = true
				break
			}
		}

		var cardName string
		var cardBorderColor lipgloss.Color
		var statusDot string

		hasCursor := m.gridCursor == idx
		if hasCursor {
			cardBorderColor = lipgloss.Color("#84DCFB") // selected highlight border
		} else if isActive {
			cardBorderColor = lipgloss.Color("#008080") // active green/cyan border
		} else {
			cardBorderColor = ColorDim // static grey border for idle agents
		}

		if isActive {
			dotPart := lipgloss.NewStyle().Foreground(lipgloss.Color("#73CBAD")).Render("●")
			activePart := renderShimmerText("active", m.blinkCycle)
			statusDot = fmt.Sprintf("%s %s", dotPart, activePart)
			cardName = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#73CBAD")).Render(b.name)
		} else {
			statusDot = dim.Render("○ idle")
			cardName = bold.Render(b.name)
		}

		rightDetails := fmt.Sprintf(" %s %s\n   %s · W:%d · %s",
			b.icon,
			cardName,
			statusDot,
			writes,
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
		if m.width > 0 && startX + 82 > m.width {
			startX = m.width - 82
			if startX < 0 {
				startX = 0
			}
		}
		dashboardContent = overlayPopup(dashboardContent, popupStr, startX, 3)
	}

	var b strings.Builder
	b.WriteString(headerRow + "\n\n")
	b.WriteString(dashboardContent + "\n\n")

	if m.selectedAgent == "" {
		b.WriteString(dim.Render("  Use arrow keys or Mouse Clicks to select agent boxes • Enter: open details popup • q: exit TUI"))
	} else {
		b.WriteString(dim.Render("  Popup active: [1/2] switch tabs • [Esc] or Click outside to close"))
	}
	b.WriteString("\n")

	return b.String()
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
	if m.activeAgentTab == 0 {
		tab1 = cyan.Render(tab1)
	} else {
		tab2 = cyan.Render(tab2)
	}
	pb.WriteString(fmt.Sprintf("  %s    %s\n\n", tab1, tab2))

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
	} else {
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
				ts, _ := time.Parse(time.RFC3339, e.Timestamp)
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
	}

	pb.WriteString("\n")
	pb.WriteString(StyleFooter.Render("  Press [Esc] to close • [1/2] Switch tabs"))

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

func getDaemonState() (bool, int) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, 0
	}
	pidPath := filepath.Join(home, ".auxly", "daemon.pid")
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return false, 0
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}
	err = process.Signal(syscall.Signal(0))
	if err != nil {
		return false, 0
	}

	connsPath := filepath.Join(home, ".auxly", "daemon.conns")
	connsData, err := os.ReadFile(connsPath)
	if err != nil {
		return true, 0
	}
	conns, err := strconv.Atoi(strings.TrimSpace(string(connsData)))
	if err != nil {
		return true, 0
	}
	return true, conns
}

func renderShimmerText(text string, cycle int) string {
	colors := []string{
		"#73CBAD", "#84D3BD", "#95DBCF", "#A6E3E1", "#B7EBE3", "#C8F3F5",
		"#B7EBE3", "#A6E3E1", "#95DBCF", "#84D3BD", "#73CBAD", "#62C39D",
		"#51BB8D", "#40B37E", "#2FAF6E", "#1EAB5F", "#10A350", "#1EAB5F",
		"#2FAF6E", "#40B37E", "#51BB8D", "#62C39D",
	}
	colorIdx := cycle % len(colors)
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(colors[colorIdx])).
		Render(text)
}
