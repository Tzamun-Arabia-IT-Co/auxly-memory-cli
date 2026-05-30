package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	runewidth "github.com/mattn/go-runewidth"
)

// ==========================================
// 1. New Activity Model (Strictly Writes)
// ==========================================

// diffMaxScroll returns the maximum scroll offset for a diff-detail popup,
// computed from the SAME wrapped + formatted lines the View renders (width 68,
// 10-line window) so the scroll handler and the View never disagree. The old
// code measured the unwrapped diff (fewer lines) minus 6, which clamped scroll
// to 0 for long single-line entries — making arrows and the wheel do nothing.
func diffMaxScroll(diff string) int {
	diffLines := strings.Split(formatDiff(wrapRawDiffText(diff, 68)), "\n")
	if max := len(diffLines) - 10; max > 0 {
		return max
	}
	return 0
}

type activityModel struct {
	logger        *audit.Logger
	entries       []audit.Entry
	cursor        int
	viewingDetail bool
	detailScrollY int
	width         int
	height        int
}

type activityRefreshMsg struct {
	entries []audit.Entry
}

type activityTickMsg struct{}

func newActivityModel(logger *audit.Logger) activityModel {
	return activityModel{logger: logger}
}

func activityTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return activityTickMsg{}
	})
}

func (m activityModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		var entries []audit.Entry
		if m.logger != nil {
			entries, _ = m.logger.TailWrites(50)
		}
		return activityRefreshMsg{entries: entries}
	}
}

func (m activityModel) Update(msg tea.Msg) (activityModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case activityRefreshMsg:
		m.entries = msg.entries
		return m, activityTickCmd()
	case activityTickMsg:
		return m, m.Refresh()
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			w := m.width
			if w <= 0 {
				w = 80
			}
			banner := renderBanner(w)
			tabRow := strings.Count(banner, "\n")
			contentOffsetY := tabRow + 4

			if m.viewingDetail {
				// Popup Y: [contentOffsetY + 4, contentOffsetY + 22], X: [5, 76]
				if msg.X < 5 || msg.X > 76 || msg.Y < contentOffsetY+4 || msg.Y > contentOffsetY+22 {
					m.viewingDetail = false
					m.detailScrollY = 0
				}
				return m, nil
			}

			listStartY := contentOffsetY + 6

			visibleCount := 10
			start := m.cursor - visibleCount/2
			if start < 0 {
				start = 0
			}
			end := start + visibleCount
			if end > len(m.entries) {
				end = len(m.entries)
			}
			if end-start < visibleCount {
				start = end - visibleCount
				if start < 0 {
					start = 0
				}
			}

			listHeight := end - start
			if msg.Y >= listStartY && msg.Y < listStartY+listHeight {
				clickedIdx := start + (msg.Y - listStartY)
				if clickedIdx >= 0 && clickedIdx < len(m.entries) {
					if m.cursor == clickedIdx {
						m.viewingDetail = true
						m.detailScrollY = 0
					} else {
						m.cursor = clickedIdx
					}
				}
			}
		} else if msg.Type == tea.MouseWheelUp {
			if m.viewingDetail {
				if m.detailScrollY > 0 {
					m.detailScrollY--
				}
			} else {
				if m.cursor > 0 {
					m.cursor--
				}
			}
		} else if msg.Type == tea.MouseWheelDown {
			if m.viewingDetail {
				maxScroll := diffMaxScroll(m.entries[m.cursor].Diff)
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.detailScrollY < maxScroll {
					m.detailScrollY++
				}
			} else {
				if m.cursor < len(m.entries)-1 {
					m.cursor++
				}
			}
		}
	case tea.KeyMsg:
		if m.viewingDetail {
			switch msg.String() {
			case "esc", "backspace", "enter":
				m.viewingDetail = false
				m.detailScrollY = 0
			case "j", "down":
				maxScroll := diffMaxScroll(m.entries[m.cursor].Diff)
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.detailScrollY < maxScroll {
					m.detailScrollY++
				}
			case "k", "up":
				if m.detailScrollY > 0 {
					m.detailScrollY--
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if len(m.entries) > 0 && m.cursor < len(m.entries) {
				m.viewingDetail = true
				m.detailScrollY = 0
			}
		}
	}
	return m, nil
}

func (m activityModel) View() string {
	title := StyleTitle.Render("📡 Agent Writing Activity Feed")

	var listContent string
	if len(m.entries) == 0 {
		listContent = "\n\nNo write activities recorded yet. Active memory writes will appear here."
	} else {
		visibleCount := 10
		start := m.cursor - visibleCount/2
		if start < 0 {
			start = 0
		}
		end := start + visibleCount
		if end > len(m.entries) {
			end = len(m.entries)
		}
		if end-start < visibleCount {
			start = end - visibleCount
			if start < 0 {
				start = 0
			}
		}

		w := m.width
		if w <= 0 {
			w = 80
		}
		listContent = renderTable(m.entries, m.cursor, start, end, w)
	}

	fullView := title + "\n\n" + listContent

	// Centered Overlay Popup for Details!
	if m.viewingDetail && m.cursor < len(m.entries) {
		popupStr := m.renderDetailPopup(m.entries[m.cursor])
		fullView = overlayPopup(fullView, popupStr, 5, 4)
	}

	return fullView
}

func (m activityModel) renderDetailPopup(e audit.Entry) string {
	ts := parseTS(e.Timestamp)
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	styledProvider := colorProvider(e.Provider)
	icon := brandIcon(e.Provider)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	bold := lipgloss.NewStyle().Bold(true)

	var lines []string
	lines = append(lines, cyan.Render("🔍 TRANSACTION DETAILS"))
	lines = append(lines, "")

	lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("File:"), bold.Render(e.File)))
	lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Timestamp:"), ts.Format("02/01/2006 15:04:05")))
	lines = append(lines, fmt.Sprintf("  %-12s %s %s (%s)", dim.Render("Agent:"), icon, styledProvider, dim.Render(e.AgentID)))
	lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Trust Level:"), green.Render(e.TrustLevel)))

	// Source line: Local, or Remote with host / IP / OS from the SSH attribution.
	if e.Source == "ssh-remote" {
		src := "Remote (SSH)"
		if meta := remoteSourceMeta(e); len(meta) > 0 {
			src += "  " + dim.Render(strings.Join(meta, " · "))
		}
		lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Source:"), lipgloss.NewStyle().Foreground(ColorAccent).Render(src)))
	} else {
		lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Source:"), dim.Render("Local")))
	}

	cleanReasonLines := wrapText(e.Reason, 50)
	for i, rl := range cleanReasonLines {
		label := ""
		if i == 0 {
			label = "Reason:"
		}
		lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render(label), lipgloss.NewStyle().Italic(true).Render(rl)))
	}
	lines = append(lines, "")

	lines = append(lines, dim.Render("  ── Diff Content ──"))

	wrappedRawDiff := wrapRawDiffText(e.Diff, 68)
	fullDiffFormatted := formatDiff(wrappedRawDiff)
	diffLines := strings.Split(fullDiffFormatted, "\n")

	maxLines := 10
	startLine := m.detailScrollY
	endLine := startLine + maxLines
	if endLine > len(diffLines) {
		endLine = len(diffLines)
	}

	if startLine > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Render("  ▲ MORE LINES ABOVE"))
	}

	for i := startLine; i < endLine; i++ {
		line := diffLines[i]
		lines = append(lines, "  "+line)
	}

	if endLine < len(diffLines) {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Render("  ▼ MORE LINES BELOW"))
	}

	lines = append(lines, "")
	lines = append(lines, StyleFooter.Render("  [Esc] or [Enter] to Close • ↑/↓ or scroll to scroll diff"))

	var paddedLines []string
	for _, line := range lines {
		paddedLines = append(paddedLines, padLine(line, 76))
	}

	popupStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2)

	return popupStyle.Render(strings.Join(paddedLines, "\n"))
}

// ==========================================
// 2. Audit Trail Model (Renamed old Activity)
// ==========================================

type auditTrailModel struct {
	logger        *audit.Logger
	entries       []audit.Entry
	cursor        int
	viewingDetail bool
	detailScrollY int
	width         int
	height        int
}

type auditTrailRefreshMsg struct {
	entries []audit.Entry
}

type auditTrailTickMsg struct{}

func newAuditTrailModel(logger *audit.Logger) auditTrailModel {
	return auditTrailModel{logger: logger}
}

func auditTrailTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return auditTrailTickMsg{}
	})
}

func (m auditTrailModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		var entries []audit.Entry
		if m.logger != nil {
			entries, _ = m.logger.Tail(50)
		}
		return auditTrailRefreshMsg{entries: entries}
	}
}

func (m auditTrailModel) Update(msg tea.Msg) (auditTrailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case auditTrailRefreshMsg:
		m.entries = msg.entries
		return m, auditTrailTickCmd()
	case auditTrailTickMsg:
		return m, m.Refresh()
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			w := m.width
			if w <= 0 {
				w = 80
			}
			banner := renderBanner(w)
			tabRow := strings.Count(banner, "\n")
			contentOffsetY := tabRow + 4

			if m.viewingDetail {
				// Popup Y: [contentOffsetY + 4, contentOffsetY + 22], X: [5, 76]
				if msg.X < 5 || msg.X > 76 || msg.Y < contentOffsetY+4 || msg.Y > contentOffsetY+22 {
					m.viewingDetail = false
					m.detailScrollY = 0
				}
				return m, nil
			}

			listStartY := contentOffsetY + 6

			visibleCount := 10
			start := m.cursor - visibleCount/2
			if start < 0 {
				start = 0
			}
			end := start + visibleCount
			if end > len(m.entries) {
				end = len(m.entries)
			}
			if end-start < visibleCount {
				start = end - visibleCount
				if start < 0 {
					start = 0
				}
			}

			listHeight := end - start
			if msg.Y >= listStartY && msg.Y < listStartY+listHeight {
				clickedIdx := start + (msg.Y - listStartY)
				if clickedIdx >= 0 && clickedIdx < len(m.entries) {
					if m.cursor == clickedIdx {
						m.viewingDetail = true
						m.detailScrollY = 0
					} else {
						m.cursor = clickedIdx
					}
				}
			}
		} else if msg.Type == tea.MouseWheelUp {
			if m.viewingDetail {
				if m.detailScrollY > 0 {
					m.detailScrollY--
				}
			} else {
				if m.cursor > 0 {
					m.cursor--
				}
			}
		} else if msg.Type == tea.MouseWheelDown {
			if m.viewingDetail {
				maxScroll := diffMaxScroll(m.entries[m.cursor].Diff)
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.detailScrollY < maxScroll {
					m.detailScrollY++
				}
			} else {
				if m.cursor < len(m.entries)-1 {
					m.cursor++
				}
			}
		}
	case tea.KeyMsg:
		if m.viewingDetail {
			switch msg.String() {
			case "esc", "backspace", "enter":
				m.viewingDetail = false
				m.detailScrollY = 0
			case "j", "down":
				maxScroll := diffMaxScroll(m.entries[m.cursor].Diff)
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.detailScrollY < maxScroll {
					m.detailScrollY++
				}
			case "k", "up":
				if m.detailScrollY > 0 {
					m.detailScrollY--
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if len(m.entries) > 0 && m.cursor < len(m.entries) {
				m.viewingDetail = true
				m.detailScrollY = 0
			}
		}
	}
	return m, nil
}

func (m auditTrailModel) View() string {
	title := StyleTitle.Render("📋 System Audit Trail & Logs")

	var listContent string
	if len(m.entries) == 0 {
		listContent = "\n\nNo logs recorded yet."
	} else {
		visibleCount := 10
		start := m.cursor - visibleCount/2
		if start < 0 {
			start = 0
		}
		end := start + visibleCount
		if end > len(m.entries) {
			end = len(m.entries)
		}
		if end-start < visibleCount {
			start = end - visibleCount
			if start < 0 {
				start = 0
			}
		}

		w := m.width
		if w <= 0 {
			w = 80
		}
		listContent = renderTable(m.entries, m.cursor, start, end, w)
	}

	fullView := title + "\n\n" + listContent

	// Centered Overlay Popup for Details!
	if m.viewingDetail && m.cursor < len(m.entries) {
		popupStr := m.renderDetailPopup(m.entries[m.cursor])
		fullView = overlayPopup(fullView, popupStr, 5, 4)
	}

	return fullView
}

func (m auditTrailModel) renderDetailPopup(e audit.Entry) string {
	ts := parseTS(e.Timestamp)
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	styledProvider := colorProvider(e.Provider)
	icon := brandIcon(e.Provider)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	bold := lipgloss.NewStyle().Bold(true)

	var lines []string
	lines = append(lines, cyan.Render("🔍 LOG TRANSACTION DETAILS"))
	lines = append(lines, "")

	lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("File:"), bold.Render(e.File)))
	lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Timestamp:"), ts.Format("02/01/2006 15:04:05")))
	lines = append(lines, fmt.Sprintf("  %-12s %s %s (%s)", dim.Render("Agent:"), icon, styledProvider, dim.Render(e.AgentID)))
	lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Action:"), green.Render(e.Action)))

	// Source line: Local, or Remote enriched with friendly box name · host · IP · OS.
	if e.Source == "ssh-remote" {
		src := "Remote (SSH)"
		if meta := remoteSourceMeta(e); len(meta) > 0 {
			src += "  " + dim.Render(strings.Join(meta, " · "))
		}
		lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Source:"), lipgloss.NewStyle().Foreground(ColorAccent).Render(src)))
	} else {
		lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render("Source:"), dim.Render("Local")))
	}

	cleanReasonLines := wrapText(e.Reason, 50)
	for i, rl := range cleanReasonLines {
		label := ""
		if i == 0 {
			label = "Reason:"
		}
		lines = append(lines, fmt.Sprintf("  %-12s %s", dim.Render(label), lipgloss.NewStyle().Italic(true).Render(rl)))
	}
	lines = append(lines, "")

	lines = append(lines, dim.Render("  ── Diff Content ──"))

	wrappedRawDiff := wrapRawDiffText(e.Diff, 68)
	fullDiffFormatted := formatDiff(wrappedRawDiff)
	diffLines := strings.Split(fullDiffFormatted, "\n")

	maxLines := 10
	startLine := m.detailScrollY
	endLine := startLine + maxLines
	if endLine > len(diffLines) {
		endLine = len(diffLines)
	}

	if startLine > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Render("  ▲ MORE LINES ABOVE"))
	}

	for i := startLine; i < endLine; i++ {
		line := diffLines[i]
		lines = append(lines, "  "+line)
	}

	if endLine < len(diffLines) {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Render("  ▼ MORE LINES BELOW"))
	}

	lines = append(lines, "")
	lines = append(lines, StyleFooter.Render("  [Esc] or [Enter] to Close • ↑/↓ or scroll to scroll diff"))

	var paddedLines []string
	for _, line := range lines {
		paddedLines = append(paddedLines, padLine(line, 76))
	}

	popupStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2)

	return popupStyle.Render(strings.Join(paddedLines, "\n"))
}

// remoteSourceMeta builds the human-facing remote attribution for an audit
// entry: the friendly box name (from clients.yaml), the reporting hostname, the
// real box IP, and the OS — whatever is available, deduped. The raw SSH metadata
// only carries the hostname/IP the agent reported; this enriches it with the
// box's configured name and resolves the real IP when the entry lacks one.
func remoteSourceMeta(e audit.Entry) []string {
	clients := readClients()
	name := friendlyClientName(e.RemoteHost, clients)
	// The stored RemoteIP is the tunnel's localhost (::1 / 127.0.0.1) for relayed
	// boxes — meaningless. Prefer the real box IP from clients.yaml whenever the
	// stored value is empty or loopback.
	ip := e.RemoteIP
	if ip == "" || isLoopbackIP(ip) {
		if real := remoteIPForHost(e.RemoteHost, clients); real != "" {
			ip = real
		}
	}

	var meta []string
	if name != "" {
		meta = append(meta, name)
	}
	if e.RemoteHost != "" && !strings.EqualFold(e.RemoteHost, name) {
		meta = append(meta, e.RemoteHost)
	}
	if ip != "" {
		meta = append(meta, ip)
	}
	if e.RemoteOS != "" {
		meta = append(meta, e.RemoteOS)
	}
	return meta
}

// isLoopbackIP reports whether an IP string is a loopback address — the value a
// relayed/tunnelled box reports for its connection (the host side sees the
// tunnel's localhost), which is not a useful identifier.
func isLoopbackIP(ip string) bool {
	switch strings.ToLower(strings.TrimSpace(ip)) {
	case "::1", "127.0.0.1", "localhost", "0.0.0.0":
		return true
	}
	return strings.HasPrefix(ip, "127.")
}

// friendlyClientName resolves a session's reporting hostname to a configured
// box's friendly name, matching by name, captured hostname, or target host.
func friendlyClientName(host string, clients []clientRow) string {
	if host == "" {
		return ""
	}
	for _, c := range clients {
		if strings.EqualFold(c.Name, host) ||
			strings.EqualFold(c.Hostname, host) ||
			strings.EqualFold(targetHost(c.Target), host) {
			return c.Name
		}
	}
	return ""
}

func padSpace(w int) string {
	if w <= 0 {
		return ""
	}
	return strings.Repeat(" ", w)
}

func repeatChar(char string, count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat(char, count)
}

func renderTable(entries []audit.Entry, cursor int, start, end int, mWidth int) string {
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	bold := lipgloss.NewStyle().Bold(true)

	// Subtract 6 from mWidth to guarantee a safe right margin in all terminals
	mWidth = mWidth - 6
	if mWidth < 58 {
		mWidth = 58
	}

	colTimeW := 21
	colAgentW := 16
	colSourceW := 13
	colActivityW := 30

	totalFixed := colTimeW + colAgentW + colSourceW + 5 // 55
	if mWidth > totalFixed {
		colActivityW = mWidth - totalFixed
		if colActivityW < 20 {
			colActivityW = 20
		}
		if colActivityW > 90 {
			colActivityW = 90
		}
	}

	var sb strings.Builder

	sb.WriteString(border.Render("┌"+repeatChar("─", colTimeW)+"┬"+repeatChar("─", colAgentW)+"┬"+repeatChar("─", colActivityW)+"┬"+repeatChar("─", colSourceW)+"┐") + "\n")

	// Pad header strings manually to ensure mathematically perfect widths that connect seamlessly with the borders
	timeH := fmt.Sprintf(" %-*s", colTimeW-1, "Timestamp")

	var agentH string
	if colAgentW >= 18 {
		agentH = fmt.Sprintf(" %-*s", colAgentW-1, "Agent / Provider")
	} else {
		agentH = fmt.Sprintf(" %-*s", colAgentW-1, "Agent")
	}

	var actH string
	if colActivityW >= 18 {
		actH = fmt.Sprintf(" %-*s", colActivityW-1, "Activity Details")
	} else {
		actH = fmt.Sprintf(" %-*s", colActivityW-1, "Activity")
	}

	srcH := fmt.Sprintf(" %-*s", colSourceW-1, "Source")

	sb.WriteString(border.Render("│") + bold.Render(timeH) + border.Render("│") + bold.Render(agentH) + border.Render("│") + bold.Render(actH) + border.Render("│") + bold.Render(srcH) + border.Render("│") + "\n")

	sb.WriteString(border.Render("├"+repeatChar("─", colTimeW)+"┼"+repeatChar("─", colAgentW)+"┼"+repeatChar("─", colActivityW)+"┼"+repeatChar("─", colSourceW)+"┤") + "\n")

	for i := start; i < end; i++ {
		e := entries[i]
		ts := parseTS(e.Timestamp)
		timeStr := ts.Format("02/01/2006 15:04:05")

		styledAgent := colorProvider(e.Provider)
		agentStr := fmt.Sprintf("%s (%s)", styledAgent, e.AgentID)
		agentStr = stripEmojis(agentStr)
		agentCleanWidth := visibleWidth(stripANSI(agentStr))
		if agentCleanWidth > colAgentW-2 {
			agentStr = colorProvider(e.Provider)
			agentStr = stripEmojis(agentStr)
			agentCleanWidth = visibleWidth(stripANSI(agentStr))
			if agentCleanWidth > colAgentW-2 {
				agentStr = stripANSI(agentStr)
				agentStr = safeTruncate(agentStr, colAgentW-5) + "..."
				agentStr = colorProvider(agentStr)
				agentStr = stripEmojis(agentStr)
				agentCleanWidth = visibleWidth(stripANSI(agentStr))
			}
		}

		summary := e.Reason
		if e.Action == "write" && e.Diff != "" {
			cleanDiff := strings.TrimSpace(e.Diff)
			for {
				orig := cleanDiff
				cleanDiff = strings.TrimSpace(cleanDiff)
				cleanDiff = strings.TrimPrefix(cleanDiff, "+")
				cleanDiff = strings.TrimPrefix(cleanDiff, "-")
				cleanDiff = strings.TrimPrefix(cleanDiff, "*")
				cleanDiff = strings.TrimSpace(cleanDiff)
				if cleanDiff == orig {
					break
				}
			}
			if strings.HasPrefix(cleanDiff, "[") {
				if closeBracketIdx := strings.Index(cleanDiff, "]"); closeBracketIdx != -1 {
					cleanDiff = strings.TrimSpace(cleanDiff[closeBracketIdx+1:])
				}
				cleanDiff = strings.TrimPrefix(cleanDiff, "Smart Sync:")
				cleanDiff = strings.TrimSpace(cleanDiff)
			}
			if cleanDiff != "" {
				summary = fmt.Sprintf("write: %s", cleanDiff)
			} else {
				summary = fmt.Sprintf("write: %s", e.Reason)
			}
		} else {
			summary = fmt.Sprintf("%s: %s", e.Action, e.Reason)
		}

		// Strictly sanitize summary to be a single-line string to prevent table layout breakage
		summary = strings.ReplaceAll(summary, "\r\n", " ")
		summary = strings.ReplaceAll(summary, "\n", " ")
		summary = strings.ReplaceAll(summary, "\r", " ")
		summary = stripEmojis(summary)

		actCleanWidth := visibleWidth(stripANSI(summary))
		if actCleanWidth > colActivityW-2 {
			summary = safeTruncate(summary, colActivityW-5) + "..."
			actCleanWidth = visibleWidth(stripANSI(summary))
		}

		// Compact column (13 wide): "Local" or "Remote". The detail popup shows the
		// full remote attribution (friendly name · host · IP · OS).
		sourceVal := "Local"
		if e.Source == "ssh-remote" {
			sourceVal = "Remote"
		}

		var rowLine string
		if i == cursor {
			cleanAgent := stripANSI(agentStr)
			tCell := " " + timeStr + " "
			agCell := " " + cleanAgent + padSpace(colAgentW-2-visibleWidth(stripANSI(cleanAgent))) + " "
			aCell := " " + summary + padSpace(colActivityW-2-visibleWidth(stripANSI(summary))) + " "
			sCell := fmt.Sprintf(" %-*s ", colSourceW-2, sourceVal)

			styleSelectedSep := lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("250"))
			innerSep := styleSelectedSep.Render("│")

			rowLine = border.Render("│") + StyleSelectedRow.Render(tCell) + innerSep + StyleSelectedRow.Render(agCell) + innerSep + StyleSelectedRow.Render(aCell) + innerSep + StyleSelectedRow.Render(sCell) + border.Render("│")
		} else {
			tCell := " " + timeStr + " "
			agCell := " " + agentStr + padSpace(colAgentW-2-visibleWidth(stripANSI(agentStr))) + " "
			aCell := " " + summary + padSpace(colActivityW-2-visibleWidth(stripANSI(summary))) + " "
			sCell := fmt.Sprintf(" %-*s ", colSourceW-2, sourceVal)

			rowLine = border.Render("│") + tCell + border.Render("│") + agCell + border.Render("│") + aCell + border.Render("│") + sCell + border.Render("│")
		}
		sb.WriteString(rowLine + "\n")
	}

	sb.WriteString(border.Render("└"+repeatChar("─", colTimeW)+"┴"+repeatChar("─", colAgentW)+"┴"+repeatChar("─", colActivityW)+"┴"+repeatChar("─", colSourceW)+"┘") + "\n")

	return sb.String()
}

func stripEmojis(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 0x1F000 && r <= 0x1FBFF) || // Emojis, pictographs, symbols
			(r >= 0x2600 && r <= 0x27BF) || // Dingbats, Miscellaneous Symbols
			(r >= 0x2300 && r <= 0x23FF) || // Miscellaneous Technical
			(r >= 0x2B00 && r <= 0x2BFF) || // Miscellaneous Symbols and Arrows
			(r >= 0xFE00 && r <= 0xFE0F) || // Variation Selectors
			(r == 0x200D) { // Zero Width Joiner
			continue
		}
		sb.WriteRune(r)
	}
	res := sb.String()
	for strings.Contains(res, "  ") {
		res = strings.ReplaceAll(res, "  ", " ")
	}
	return strings.TrimSpace(res)
}

func safeTruncate(s string, maxLen int) string {
	if visibleWidth(s) <= maxLen {
		return s
	}
	var sb strings.Builder
	currentWidth := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if currentWidth+rw > maxLen {
			break
		}
		sb.WriteRune(r)
		currentWidth += rw
	}
	return sb.String()
}

// ==========================================
// 3. Shared Presentation Helpers
// ==========================================

func brandIcon(provider string) string {
	switch provider {
	case "claude":
		return "🧠"
	case "claude-code":
		return "💻"
	case "chatgpt":
		return "💬"
	case "cursor":
		return "🎯"
	case "gemini":
		return "✨"
	case "antigravity-ide":
		return "🌌"
	case "antigravity-cli":
		return "🚀"
	case "antigravity-agent":
		return "🤖"
	case "codex":
		return "💻"
	case "copilot":
		return "🤝"
	case "kimi":
		return "📊"
	case "trae":
		return "🎯"
	}
	return "●"
}

func colorProvider(provider string) string {
	colors := map[string]string{
		"claude":            "99",
		"claude-code":       "39",
		"chatgpt":           "78",
		"cursor":            "39",
		"gemini":            "220",
		"antigravity-ide":   "205",
		"antigravity-cli":   "141",
		"antigravity-agent": "172",
		"codex":             "34",
		"copilot":           "45",
		"kimi":              "45",
		"trae":              "34",
	}
	color, ok := colors[provider]
	if !ok {
		color = "252"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(provider)
}

func formatDiff(diffText string) string {
	if diffText == "" {
		return ""
	}

	// Pre-process: split inline [+] and [-] changes to separate lines for clean normal diff UX
	diffText = strings.ReplaceAll(diffText, " [+] ", "\n[+] ")
	diffText = strings.ReplaceAll(diffText, " [-] ", "\n[-] ")
	diffText = strings.ReplaceAll(diffText, " + [+] ", "\n[+] ")
	diffText = strings.ReplaceAll(diffText, " + [-] ", "\n[-] ")

	lines := strings.Split(diffText, "\n")
	var formatted []string

	// White text on user-specified hex dark green (#21381E) & dark red (#3C1211) backgrounds
	styleAdd := lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(lipgloss.Color("#21381E")) // dark forest green
	styleDel := lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(lipgloss.Color("#3C1211")) // dark wine red
	styleHeader := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)                          // bright cyan
	styleDim := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))                                       // dim grey

	for _, line := range lines {
		cleanLine := strings.TrimSpace(line)
		if cleanLine == "" {
			continue
		}

		// If line has '[-]' or starts with '-' (excluding diff headers)
		if strings.Contains(cleanLine, "[-]") || (strings.HasPrefix(cleanLine, "-") && !strings.HasPrefix(cleanLine, "---")) {
			formatted = append(formatted, styleDel.Render(line))
		} else if strings.Contains(cleanLine, "[+]") || (strings.HasPrefix(cleanLine, "+") && !strings.HasPrefix(cleanLine, "+++")) {
			formatted = append(formatted, styleAdd.Render(line))
		} else if strings.HasPrefix(cleanLine, "@@") || strings.HasPrefix(cleanLine, "diff ") || strings.HasPrefix(cleanLine, "---") || strings.HasPrefix(cleanLine, "+++") {
			formatted = append(formatted, styleHeader.Render(line))
		} else {
			formatted = append(formatted, styleDim.Render(line))
		}
	}
	return strings.Join(formatted, "\n")
}

func wrapRawDiffText(diffText string, width int) string {
	diffText = strings.ReplaceAll(diffText, " [+] ", "\n[+] ")
	diffText = strings.ReplaceAll(diffText, " [-] ", "\n[-] ")
	diffText = strings.ReplaceAll(diffText, " + [+] ", "\n[+] ")
	diffText = strings.ReplaceAll(diffText, " + [-] ", "\n[-] ")

	rawLines := strings.Split(diffText, "\n")
	var wrappedLines []string
	for _, rl := range rawLines {
		wrappedLines = append(wrappedLines, wrapDiffLine(rl, width)...)
	}
	return strings.Join(wrappedLines, "\n")
}

func wrapDiffLine(line string, width int) []string {
	if len(line) <= width {
		return []string{line}
	}
	var chunks []string
	prefix := ""
	cleanLine := line
	if strings.HasPrefix(line, "+") {
		prefix = "+"
		cleanLine = strings.TrimPrefix(line, "+")
	} else if strings.HasPrefix(line, "-") {
		prefix = "-"
		cleanLine = strings.TrimPrefix(line, "-")
	} else if strings.HasPrefix(line, " ") {
		prefix = " "
		cleanLine = strings.TrimPrefix(line, " ")
	}

	runes := []rune(cleanLine)
	for len(runes) > 0 {
		limit := width - 4
		if limit > len(runes) {
			limit = len(runes)
		}
		chunk := string(runes[:limit])
		if len(chunks) == 0 {
			chunks = append(chunks, prefix+chunk)
		} else {
			chunks = append(chunks, "   "+chunk)
		}
		runes = runes[limit:]
	}
	return chunks
}
