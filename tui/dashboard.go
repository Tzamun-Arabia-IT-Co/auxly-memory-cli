package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/sharing"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/usage"
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
	composition      []categoryStat        // per-category memory breakdown (left column)
	pendingFiles     []pending.PendingFile // queued approvals (shown inline when > 0)
	remoteScope      map[string]string     // host → access scope ("read · 6 files")
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

	// Live Usage data layer (opt-in; off keeps Auxly fully local).
	usageMgr       *usage.Manager
	usageReports   map[string]usage.Report
	liveUsage      bool
	showUsagePopup bool   // all-agents Live Usage overlay ([u])
	agyAuthing     bool   // Antigravity consent flow in progress
	agyAuthStatus  string // last Antigravity auth result/status

	// Interactive Popup & Grid Selection State
	selectedAgent  string      // E.g., "claude", "cursor", etc.
	activeAgentTab int         // 0 = Info, 1 = Recent Writes, 2 = Connected, 3 = Usage
	gridCursor     int         // index into cards
	cards          []agentCard // detected agent brands shown in the grid (dynamic)
}

type dashboardRefreshMsg struct {
	stats           *audit.Stats
	pendingCnt      int
	trustCfg        *trust.Config
	activeProviders []string
	recentWrites    []audit.Entry
	composition     []categoryStat
	pendingFiles    []pending.PendingFile
	remoteScope     map[string]string
	sessions        []agentSession
	unregistered    int
	updateAvail     bool
	updateLatest    string
	at              time.Time
	mcpError        string
	cards           []agentCard
}

// dashboardUpdateDoneMsg carries the result of a one-click [u] self-update.
type dashboardUpdateDoneMsg struct {
	path string
	err  error
}

type dashboardTickMsg struct{}
type animationTickMsg struct{}

// usageReportsMsg carries the result of an async Live Usage fetch.
type usageReportsMsg struct {
	reports []usage.Report
}

// usageFetchCmd runs the blocking Reports call off the UI thread. The manager's
// 60s cache throttles real network calls, so firing this each tick is cheap.
func usageFetchCmd(mgr *usage.Manager) tea.Cmd {
	return func() tea.Msg {
		if mgr == nil {
			return usageReportsMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		return usageReportsMsg{reports: mgr.Reports(ctx)}
	}
}

// agyAuthMsg carries the outcome of the Antigravity consent flow.
type agyAuthMsg struct {
	email string
	err   error
}

// agyAuthCmd runs the Antigravity OAuth consent off the UI thread: it opens the
// browser and waits (up to 3 min) for the user to approve, then stores the token
// and busts the usage cache so the meter refetches.
func agyAuthCmd(mgr *usage.Manager) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		email, err := usage.AntigravityLogin(ctx)
		if err == nil && mgr != nil {
			mgr.Invalidate()
		}
		return agyAuthMsg{email: email, err: err}
	}
}

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

func newDashboardModel(logger *audit.Logger, mgr *pending.Manager, memoryPath string, usageMgr *usage.Manager) dashboardModel {
	return dashboardModel{
		logger:           logger,
		pendingMgr:       mgr,
		memoryPath:       memoryPath,
		blinkCycle:       0,
		animationStarted: false,
		mcpError:         "",
		reloaded:         false,
		gridCursor:       0,
		usageMgr:         usageMgr,
		usageReports:     map[string]usage.Report{},
		liveUsage:        config.LoadSettings().LiveUsage,
		cards:            agentCardOrder(initialActivity(logger)),
	}
}

// initialActivity loads the activity-provider list once at construction so a
// manually-wired agent (e.g. Android Studio) shows on the very first paint,
// before the first Refresh tick fills it in again.
func initialActivity(logger *audit.Logger) []string {
	if logger == nil {
		return nil
	}
	s, err := logger.Stats()
	if err != nil || s == nil {
		return nil
	}
	return providersWithActivity(s)
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
		var pendingFiles []pending.PendingFile
		if m.pendingMgr != nil {
			pendingFiles, _ = m.pendingMgr.List()
			pendingCnt = len(pendingFiles)
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
		composition := computeComposition(m.memoryPath)
		mcpError := getClaudeMCPError()
		sessions := gatherSessions()
		// gatherSessions already reconciles the registry against live servers;
		// count how many are connected via inference only (older builds that
		// never registered) so the dashboard can hint that they need a reconnect.
		unregistered := 0
		for _, s := range sessions {
			if s.Unregistered {
				unregistered++
			}
		}
		// Resolve each connected remote box's access scope once (read/write + how many
		// files it may see) so the connections list shows WHAT each box can reach.
		remoteScope := computeRemoteScopes(m.memoryPath, sessions)
		latest, updateAvail := update.Available()
		return dashboardRefreshMsg{
			stats:           stats,
			pendingCnt:      pendingCnt,
			trustCfg:        trustCfg,
			activeProviders: activeProviders,
			recentWrites:    recentWrites,
			composition:     composition,
			pendingFiles:    pendingFiles,
			remoteScope:     remoteScope,
			sessions:        sessions,
			unregistered:    unregistered,
			updateAvail:     updateAvail,
			updateLatest:    latest,
			at:              time.Now(),
			mcpError:        mcpError,
			cards:           agentCardOrder(providersWithActivity(stats)),
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
		m.composition = msg.composition
		m.pendingFiles = msg.pendingFiles
		m.remoteScope = msg.remoteScope
		m.sessions = msg.sessions
		m.unregistered = msg.unregistered
		if !m.updating {
			m.updateAvail = msg.updateAvail
			m.updateLatest = msg.updateLatest
		}
		m.lastRefresh = msg.at
		m.mcpError = msg.mcpError
		if len(msg.cards) > 0 {
			m.cards = msg.cards
		}
		if m.gridCursor >= len(m.cards) {
			m.gridCursor = 0
		}
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
		// Usage isn't on the cards anymore; it's fetched on demand when the [u]
		// popup opens (and on the Analytics Usage tab), to keep background load
		// off the rate-limited endpoints.
		return m, m.Refresh()
	case usageReportsMsg:
		for _, r := range msg.reports {
			m.usageReports[r.Provider] = r
		}
		return m, nil
	case agyAuthMsg:
		m.agyAuthing = false
		if msg.err != nil {
			m.agyAuthStatus = "✗ " + msg.err.Error()
			return m, nil
		}
		if msg.email != "" {
			m.agyAuthStatus = "✓ Connected as " + msg.email
		} else {
			m.agyAuthStatus = "✓ Connected"
		}
		return m, usageFetchCmd(m.usageMgr)
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
			// Match the app's banner + spacing so the content offset the card
			// hit-test relies on lines up with what's actually drawn (a compact
			// banner and/or tight spacing on a short terminal shift every row up).
			bodyC := m.bodyCompact()
			banner := renderBanner(w)
			tabRow := strings.Count(banner, "\n")
			contentOffsetY := tabRow + 4
			if bodyC {
				contentOffsetY = tabRow + 2 // tight spacing removes two blank rows
			}

			// Use dynamic view rendering to locate coordinates!
			viewStr := m.View()
			viewLines := strings.Split(viewStr, "\n")

			// If popup is open
			if m.selectedAgent != "" {
				popupStr := m.renderPopup(m.selectedAgent)
				popLines := strings.Split(popupStr, "\n")

				// Mirror the renderer's geometry exactly (see View): the popup is
				// overlaid at startX, three rows into dashboardContent, which
				// itself begins after the header row, the optional update banner,
				// and one blank line.
				pStartX := 10
				popWidth := popupVisibleWidth(popLines)
				if w > 0 && pStartX+popWidth > w {
					pStartX = w - popWidth
					if pStartX < 0 {
						pStartX = 0
					}
				}
				contentLocalStart := 2
				if m.renderUpdateBanner() != "" {
					contentLocalStart = 3
				}
				pStartY := contentOffsetY + contentLocalStart + 3
				pEndY := pStartY + len(popLines)
				pEndX := pStartX + popWidth

				if msg.X < pStartX || msg.X > pEndX || msg.Y < pStartY || msg.Y > pEndY {
					m.selectedAgent = "" // Clicked outside, close popup!
					return m, nil
				}

				// Tab row: find the rendered line carrying the tab markers and
				// partition it by the markers' actual columns, so clicking
				// anywhere on a tab selects it regardless of label width.
				for i, line := range popLines {
					clean := stripANSI(line)
					if !strings.Contains(clean, "[1 ") {
						continue
					}
					if msg.Y != pStartY+i {
						break
					}
					if tab := tabAtColumn(clean, pStartX, msg.X); tab >= 0 {
						m.activeAgentTab = tab
					}
					break
				}
				return m, nil // Swallow clicks inside the popup
			}

			// Popup closed: locate each agent card by scanning the rendered view
			// for its exact name, deriving the hit rectangle from where the name
			// actually sits (column + card width) rather than hardcoded splits —
			// so two-up cards and any future layout shift still hit-test cleanly.
			const cardWidth = 30 // card content width; border/padding add a small margin
			clickLineIdx := msg.Y - contentOffsetY
			for _, b := range m.cards {
				for idx, line := range viewLines {
					clean := stripANSI(line)
					col := visibleColumnOf(clean, b.name)
					if col < 0 {
						continue
					}
					// The name sits right of the glyph emblem (border+pad+glyph+gap ≈ 5
					// cols, +1 left margin); the card spans a row above and below it.
					// Bound the hit box to that rectangle so adjacent cards don't overlap.
					bottom := idx + 2
					x0, x1 := col-6, col-6+cardWidth+2
					if clickLineIdx >= idx-1 && clickLineIdx <= bottom && msg.X >= x0 && msg.X <= x1 {
						m.selectedAgent = b.id
						m.activeAgentTab = 0
						if m.liveUsage {
							return m, usageFetchCmd(m.usageMgr)
						}
						return m, nil
					}
					break // first occurrence of this name is its card
				}
			}
		}
	case tea.KeyMsg:
		if m.showUsagePopup {
			switch msg.String() {
			case "esc", "q", "u":
				m.showUsagePopup = false
				return m, nil
			case "r", "R":
				if m.liveUsage {
					m.usageMgr.Invalidate()
					return m, usageFetchCmd(m.usageMgr)
				}
				return m, nil
			}
			return m, nil // swallow keys while the usage popup is open
		}
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
			case "4":
				m.activeAgentTab = 3
				return m, nil
			case "left":
				if m.activeAgentTab > 0 {
					m.activeAgentTab--
				}
				return m, nil
			case "right":
				if m.activeAgentTab < 3 {
					m.activeAgentTab++
				}
				return m, nil
			case "tab":
				m.activeAgentTab = (m.activeAgentTab + 1) % 4
				return m, nil
			case "a", "A":
				// Authorize Antigravity from within the Usage tab: launches the
				// one-time Google consent flow (opens the browser) without leaving
				// the TUI.
				if m.selectedAgent == "antigravity" && m.activeAgentTab == 3 && !m.agyAuthing {
					m.agyAuthing = true
					m.agyAuthStatus = ""
					return m, agyAuthCmd(m.usageMgr)
				}
				return m, nil
			}
			return m, nil // Swallow keys when popup is active
		}

		// Navigate using the SAME column count the grid renders (agentGridLayout),
		// so arrows/vim keys land on the right card at 1–4 columns.
		cols, _ := agentGridLayout(m.width, len(m.cards), m.bodyCompact())
		if cols < 1 {
			cols = 1
		}
		switch msg.String() {
		case "left", "h":
			if m.gridCursor%cols > 0 {
				m.gridCursor--
			}
		case "right", "l":
			if m.gridCursor%cols < cols-1 && m.gridCursor+1 < len(m.cards) {
				m.gridCursor++
			}
		case "up", "k":
			if m.gridCursor >= cols {
				m.gridCursor -= cols
			}
		case "down", "j":
			if m.gridCursor+cols < len(m.cards) {
				m.gridCursor += cols
			}
		case "enter", " ":
			if m.gridCursor >= 0 && m.gridCursor < len(m.cards) {
				m.selectedAgent = m.cards[m.gridCursor].id
				m.activeAgentTab = 0
				if m.liveUsage {
					return m, usageFetchCmd(m.usageMgr)
				}
			}
		case "r", "R":
			// Force refresh: immediately re-scan the session registry and the
			// live process table (no waiting for the 1s tick), surfacing any
			// agent that's running but not yet reflected. When Live Usage is on,
			// also bust its 60s cache so the meters refetch fresh numbers now.
			m.reloaded = true
			m.reloadedAt = time.Now()
			if m.liveUsage {
				m.usageMgr.Invalidate()
				return m, tea.Batch(m.Refresh(), usageFetchCmd(m.usageMgr))
			}
			return m, m.Refresh()
		case "u":
			// Toggle the all-agents Live Usage popup.
			if m.selectedAgent == "" {
				m.showUsagePopup = !m.showUsagePopup
				if m.showUsagePopup && m.liveUsage {
					return m, usageFetchCmd(m.usageMgr)
				}
				return m, nil
			}
		case "U":
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

// View renders the dashboard, compacting ONLY when the full-look layout would not
// fit the terminal height (content-aware) — so a terminal with vertical room keeps
// the full ASCII banner and rich diagnostics even if it isn't especially tall.
func (m dashboardModel) View() string {
	return m.renderBody(m.bodyCompact())
}

// bodyCompact decides, by MEASURING, whether the dashboard body must tighten to
// fit the terminal height beneath the always-full logo. The dashboard is the one
// screen that fits rather than scrolls (a fixed overview), so it tightens the body
// — never the logo (always shown) and never the bordered cards (kept). The body
// compaction packs more grid columns and trims the diagnostics box; if even that
// can't fit a very short pane, the parent's height guard keeps the chrome on screen.
func (m dashboardModel) bodyCompact() bool {
	if m.width > 0 && m.width < 80 {
		return true
	}
	if m.height <= 0 {
		return false
	}
	const chromeFull = 6 // tabs(2) + footer(1) + blank separators in the full layout
	bFull := lipgloss.Height(renderBanner(m.width))
	if bFull+chromeFull+lipgloss.Height(m.renderBody(false)) <= m.height {
		return false
	}
	return true
}

func (m dashboardModel) renderBody(compact bool) string {
	title := StyleTitle.Render("📊 Auxly-Memory CLI Dashboard")

	clockStr := time.Now().Format("02/01/2006 15:04:05")
	clockHeader := lipgloss.NewStyle().Foreground(ColorDim).Render("🕒 Time: " + clockStr)
	headerRow := lipgloss.JoinHorizontal(lipgloss.Top, title, "         ", clockHeader)
	// The freshness signal rides on the header row (no extra height); only in full
	// mode so cramped terminals stay byte-identical to before.
	if !compact {
		if fresh := m.renderFreshness(); fresh != "" {
			headerRow = lipgloss.JoinHorizontal(lipgloss.Top, headerRow, "      ", fresh)
		}
	}
	if m.reloaded {
		refreshed := lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true).Render("   ⟳ Force refreshed")
		headerRow = lipgloss.JoinHorizontal(lipgloss.Top, headerRow, refreshed)
	}

	if m.stats == nil {
		return headerRow + "\n\nLoading dashboard..."
	}

	dim := lipgloss.NewStyle().Foreground(ColorDim)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	bold := lipgloss.NewStyle().Bold(true)

	// Left Column: System Status & Diagnostic Details
	vPad := 1
	sep := "\n\n"
	memStorePath := m.memoryPath
	if compact {
		vPad = 0
		sep = "\n"
		memStorePath = filepath.Base(m.memoryPath)
	}
	diagStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(vPad, 2).
		Width(44)

	pendingColor := ColorDim
	if m.pendingCnt > 0 {
		pendingColor = ColorWarning
	}
	pendingText := lipgloss.NewStyle().Bold(true).Foreground(pendingColor).Render(fmt.Sprintf("%d", m.pendingCnt))

	diagContent := fmt.Sprintf(
		"💻 %s"+sep+
			"Writes Today:   %s\n"+
			"Total Entries:  %s\n"+
			"Pending Queue:  %s"+sep+
			"📂 %s\n%s",
		bold.Render("System Diagnostics"),
		green.Render(fmt.Sprintf("%d", m.stats.WritesToday)),
		lipgloss.NewStyle().Foreground(ColorSecondary).Render(fmt.Sprintf("%d", m.stats.TotalEntries)),
		pendingText,
		bold.Render("Memory Store:"),
		dim.Render(memStorePath),
	)
	// Enrichment depth scales with terminal height so the rich dashboard fits more
	// terminals before falling back to compact: taller panes show more rows.
	enrichN := 9
	switch {
	case m.height > 0 && m.height < 56:
		enrichN = 4
	case m.height > 0 && m.height < 64:
		enrichN = 6
	}
	// Memory-by-category breakdown sits in the left column's spare height — full
	// mode only, so compact panes don't grow.
	if !compact {
		if comp := m.renderComposition(enrichN); comp != "" {
			diagContent += sep + comp
		}
	}
	diagContent += sep + fmt.Sprintf("🔌 %s\n%s",
		bold.Render("Active Connections:"), m.renderConnectionsSummary(compact))
	leftCol := diagStyle.Render(diagContent)

	// Right Column: Connected Agent Grid — only DETECTED brands (shared with the
	// hit-tester via m.cards), so the grid scales to whatever is installed.
	brands := m.cards

	// Decide the grid shape so cards fill the available width: more columns (and
	// thus fewer rows) on wide terminals, keeping the exact bordered-card design.
	gridCols, gridCardW := agentGridLayout(m.width, len(brands), compact)

	var brandCards []string
	for idx, b := range brands {
		brandCards = append(brandCards, m.renderAgentCard(b, idx, gridCardW))
	}

	var grid []string
	for i := 0; i < len(brandCards); i += gridCols {
		end := i + gridCols
		if end > len(brandCards) {
			end = len(brandCards)
		}
		cells := make([]string, 0, (end-i)*2-1)
		for j := i; j < end; j++ {
			if j > i {
				cells = append(cells, " ")
			}
			cells = append(cells, brandCards[j])
		}
		grid = append(grid, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	gridSection := strings.Join(grid, "\n")
	if len(brands) == 0 {
		gridSection = lipgloss.NewStyle().Foreground(ColorDim).Render("No AI agents detected on this machine.\nRun  auxly setup  to wire one up.")
	}

	rightCol := fmt.Sprintf("%s\n%s", StyleHeader.Render("📡 Connected Agent Brands"), gridSection)
	// Pending approvals (when any) and the "what just happened" feed fill the space
	// under the grid — full mode only. Pending sits first: it's actionable.
	if !compact {
		if pend := m.renderPendingInline(5); pend != "" {
			rightCol += "\n\n" + pend
		}
		if feed := m.renderRecentFeed(enrichN); feed != "" {
			rightCol += "\n\n" + feed
		}
	}

	var dashboardContent string
	if m.width > 0 && m.width < 116 {
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
	} else if m.showUsagePopup {
		dashboardContent = overlayPopup(dashboardContent, m.renderUsagePopup(), 10, 3)
	}

	var b strings.Builder
	b.WriteString(headerRow + "\n")
	if banner := m.renderUpdateBanner(); banner != "" {
		b.WriteString(banner + "\n")
	}
	// Drop the blank lines around the content on short terminals to reclaim rows.
	// In full mode keep one separator above the content and hug the footer below it,
	// so the rich body needs one less row and fits more terminals.
	if compact {
		b.WriteString(dashboardContent + "\n")
	} else {
		b.WriteString("\n")
		b.WriteString(dashboardContent + "\n")
	}

	if m.selectedAgent != "" {
		b.WriteString(dim.Render("  Popup active: [1/2/3/4] switch tabs • [Esc] or Click outside to close"))
	} else if m.showUsagePopup {
		b.WriteString(dim.Render("  Live Usage • [r] refresh • [Esc]/[u] close"))
	} else {
		hint := "  Use arrow keys or Mouse Clicks to select agent boxes • Enter: open details popup • [u] usage • q: exit TUI"
		if m.updateAvail && !m.updating {
			hint += " • [U] update"
		}
		b.WriteString(dim.Render(hint))
	}
	b.WriteString("\n")

	return b.String()
}

// categoryStat is one row of the dashboard's memory-composition breakdown.
type categoryStat struct {
	label   string
	items   int   // bullet/fact lines in the category file
	size    int64 // file size in bytes
	private bool  // personal tier (shown with a lock)
}

// computeComposition reads the vault and counts memory items per taxonomy category,
// so the dashboard can show what KIND of memory the user has (not just a total).
// Items are bullet lines ("- " / "* "); empty/heading/footer lines are ignored.
func computeComposition(memoryPath string) []categoryStat {
	if memoryPath == "" {
		return nil
	}
	store := memory.NewStore(memoryPath)
	files, err := store.List()
	if err != nil {
		return nil
	}
	sizeByFile := make(map[string]int64, len(files))
	for _, f := range files {
		sizeByFile[f.Name] = f.Size
	}
	var out []categoryStat
	for _, c := range memory.Taxonomy {
		content, err := store.View(c.File)
		if err != nil {
			continue // category file doesn't exist yet
		}
		items := 0
		for _, ln := range strings.Split(content, "\n") {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
				items++
			}
		}
		out = append(out, categoryStat{
			label:   c.Slug,
			items:   items,
			size:    sizeByFile[c.File],
			private: c.Tier == memory.TierPersonal,
		})
	}
	return out
}

// renderComposition draws the per-category breakdown with proportional bars, busiest
// category first. Only categories that hold at least one item are shown, capped at
// maxRows so the left column stays short enough to fit the terminal.
func (m dashboardModel) renderComposition(maxRows int) string {
	stats := make([]categoryStat, 0, len(m.composition))
	maxItems := 0
	for _, c := range m.composition {
		if c.items > 0 {
			stats = append(stats, c)
			if c.items > maxItems {
				maxItems = c.items
			}
		}
	}
	if len(stats) == 0 {
		return ""
	}
	sort.SliceStable(stats, func(i, j int) bool { return stats[i].items > stats[j].items })

	const barW = 10
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	track := lipgloss.NewStyle().Foreground(lipgloss.Color("237")) // a visible-but-quiet bar track
	hidden := 0
	if maxRows > 0 && len(stats) > maxRows {
		hidden = len(stats) - maxRows
		stats = stats[:maxRows]
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("🗂  Memory by category"))
	for _, c := range stats {
		filled := 0
		if maxItems > 0 {
			filled = c.items * barW / maxItems
			if filled == 0 {
				filled = 1
			}
		}
		bar := lipgloss.NewStyle().Foreground(ColorPrimary).Render(strings.Repeat("█", filled)) +
			track.Render(strings.Repeat("░", barW-filled))
		label := c.label
		if c.private {
			label += " 🔒"
		}
		// Pad by DISPLAY width (not rune count) so the 🔒 emoji's two cells don't
		// shove the personal row's bar a column to the right.
		const labelCol = 13
		if pad := labelCol - runewidth.StringWidth(label); pad > 0 {
			label += strings.Repeat(" ", pad)
		}
		b.WriteString(fmt.Sprintf("\n%s %s %s",
			label, bar, dim.Render(fmt.Sprintf("%3d", c.items))))
	}
	if hidden > 0 {
		b.WriteString(dim.Render(fmt.Sprintf("\n%-13s +%d more", "", hidden)))
	}
	return b.String()
}

// renderRecentFeed lists the most recent memory writes (newest first) so the
// dashboard answers "what just happened" without leaving the tab.
func (m dashboardModel) renderRecentFeed(maxRows int) string {
	if len(m.recentWrites) == 0 {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	var b strings.Builder
	b.WriteString(StyleHeader.Render("🕘 Recent Memory Changes"))
	n := len(m.recentWrites)
	if n > maxRows {
		n = maxRows
	}
	for i := 0; i < n; i++ {
		e := m.recentWrites[i]
		ts := dim.Render(fmt.Sprintf("%-6s", feedTimestamp(e)))
		typeLabel, typeColor := activityType(e)
		tag := lipgloss.NewStyle().Foreground(typeColor).Render(fmt.Sprintf("%-10s", typeLabel))
		// "who": provider in its brand color, width-padded (ANSI-aware).
		whoR := colorProvider(e.Provider)
		if gap := 11 - lipgloss.Width(whoR); gap > 0 {
			whoR += strings.Repeat(" ", gap)
		}
		file := e.File
		if file == "" {
			file = "—"
		}
		added, removed := diffCounts(e.Diff)
		delta := ""
		if added > 0 || removed > 0 {
			delta = "  " + lipgloss.NewStyle().Foreground(ColorSuccess).Render(fmt.Sprintf("+%d", added)) +
				"/" + lipgloss.NewStyle().Foreground(ColorDanger).Render(fmt.Sprintf("-%d", removed))
		}
		b.WriteString(fmt.Sprintf("\n%s %s %s%-14s%s",
			ts, tag, whoR, file, delta))
	}
	return b.String()
}

// feedTimestamp shows the clock time for today's writes and a short date for older
// ones, so a row from a previous day isn't mistaken for "just now".
func feedTimestamp(e audit.Entry) string {
	t := parseTS(e.Timestamp)
	now := time.Now()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2")
}

// computeRemoteScopes resolves, per connected remote host, what that box can reach:
// read vs read/write and how many files it may see. Uses the same sharing ACL the
// host enforces, so the dashboard reflects the real grant (default is read-only over
// all non-personal files when no per-client config exists).
func computeRemoteScopes(memoryPath string, sessions []agentSession) map[string]string {
	if memoryPath == "" {
		return nil
	}
	var names []string
	if files, err := memory.NewStore(memoryPath).List(); err == nil {
		for _, f := range files {
			names = append(names, f.Name)
		}
	}
	out := map[string]string{}
	for _, s := range sessions {
		if !s.Remote || s.Host == "" {
			continue
		}
		if _, done := out[s.Host]; done {
			continue
		}
		share := sharing.LoadForRemoteHost(memoryPath, s.Host)
		readable := 0
		for _, ok := range sharing.AllowedReads(share, names) {
			if ok {
				readable++
			}
		}
		access := "read"
		for _, n := range names {
			if sharing.CanWrite(share, n, names) {
				access = "read/write"
				break
			}
		}
		out[s.Host] = fmt.Sprintf("%s · %d file(s)", access, readable)
	}
	return out
}

// renderPendingInline surfaces queued approvals right on the dashboard (only when
// the queue is non-empty) so a waiting change is impossible to miss.
func (m dashboardModel) renderPendingInline(maxRows int) string {
	if len(m.pendingFiles) == 0 {
		return ""
	}
	warn := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	var b strings.Builder
	b.WriteString(warn.Render(fmt.Sprintf("⚠ %d Pending Approval(s)", len(m.pendingFiles))))
	n := len(m.pendingFiles)
	if n > maxRows {
		n = maxRows
	}
	for i := 0; i < n; i++ {
		p := m.pendingFiles[i]
		b.WriteString(fmt.Sprintf("\n  %-22s %s",
			strings.TrimSuffix(p.Name, ".md"),
			dim.Render(humanizeAgo(time.Since(p.ModTime)))))
	}
	if len(m.pendingFiles) > maxRows {
		b.WriteString(dim.Render(fmt.Sprintf("\n  …and %d more", len(m.pendingFiles)-maxRows)))
	}
	b.WriteString("\n" + dim.Render("  → review in Approvals (4)"))
	return b.String()
}

// renderFreshness is the one-line "is anything happening" signal for the header.
func (m dashboardModel) renderFreshness() string {
	if m.stats == nil || m.stats.LastWriteTime == "" {
		return ""
	}
	ago := humanizeAgo(time.Since(parseTS(m.stats.LastWriteTime)))
	s := "✎ Last write: " + ago
	if len(m.recentWrites) > 0 {
		w := m.recentWrites[0]
		if w.File != "" {
			s += " · " + w.Provider + " → " + w.File
		}
	}
	return lipgloss.NewStyle().Foreground(ColorDim).Render(s)
}

// diffCounts tallies +/- lines in an audit diff string.
func diffCounts(diff string) (added, removed int) {
	for _, ln := range strings.Split(diff, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "+"):
			added++
		case strings.HasPrefix(t, "-"):
			removed++
		}
	}
	return added, removed
}

// humanizeAgo renders a duration as a compact relative time ("just now", "4m ago").
func humanizeAgo(d time.Duration) string {
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
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
			Render(fmt.Sprintf("  ⬆ Update available: v%s (you have v%s) — press [U] to update",
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

// usageBar renders a fixed-width meter for a percent-used value (0–100). The
// filled portion uses the brand's accent color; the remainder is dim. Mirrors
// the half-block aesthetic of the brand marks with ▰/▱ cells.
func usageBar(pct float64, width int, brand string) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	accent, ok := brandAccent[brand]
	if !ok {
		accent = "#84DCFB"
	}
	barStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(accent))
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	return barStyle.Render(strings.Repeat("▰", filled)) +
		dimStyle.Render(strings.Repeat("▱", width-filled))
}

// renderUsageTab writes the "Usage" popup tab for one provider: its plan, each
// quota window as a labeled bar with reset time, and a provenance line. When the
// Live Usage opt-in is off, or the snapshot is unavailable, it explains why.
func (m dashboardModel) renderUsageTab(pb *strings.Builder, provider string) {
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	bold := lipgloss.NewStyle().Bold(true)
	teal := lipgloss.NewStyle().Foreground(lipgloss.Color("#73CBAD"))

	if !m.liveUsage {
		pb.WriteString("\n  " + dim.Render("Live Usage is off — enable it in Settings.") + "\n")
		return
	}

	report, loaded := m.usageReports[provider]
	if !loaded {
		pb.WriteString("\n  " + dim.Render("usage … (fetching from provider)") + "\n")
		return
	}

	if report.Err != "" {
		pb.WriteString("\n  " + bold.Render("—") + "  " + dim.Render(report.Err) + "\n")
		if provider == "antigravity" {
			m.writeAgyAuthHint(pb)
		}
		return
	}

	if report.Account != "" {
		pb.WriteString("  " + dim.Render(fmt.Sprintf("%-8s", "Account:")) + " " + bold.Render(report.Account) + "\n")
	}
	if report.Plan != "" {
		pb.WriteString("  " + dim.Render(fmt.Sprintf("%-8s", "Plan:")) + " " + bold.Render(report.Plan) + "\n")
	}
	if report.Org != "" {
		pb.WriteString("  " + dim.Render(fmt.Sprintf("%-8s", "Org:")) + " " + bold.Render(report.Org) + "\n")
	}
	pb.WriteString("\n")

	now := time.Now()
	for _, w := range report.Windows {
		bar := usageBar(w.Pct, 12, provider)
		reset := ""
		if r := usage.FormatReset(w.ResetAt, now); r != "" {
			reset = dim.Render("resets " + r)
		}
		pb.WriteString(fmt.Sprintf("  %s  %s  %s   %s\n",
			bold.Render(fmt.Sprintf("%-8s", w.Label)),
			bar,
			fmt.Sprintf("%3.0f%%", w.Pct),
			reset,
		))
	}

	source := report.Source
	if source == "" {
		source = "provider"
	}
	// Distinguish a fresh read from a last-good snapshot held through a transient
	// rate-limit, so the user knows whether the numbers are live or cached.
	stamp := teal.Render("↻ live")
	if !report.FetchedAt.IsZero() && time.Since(report.FetchedAt) > 90*time.Second {
		stamp = lipgloss.NewStyle().Foreground(ColorWarning).Render("⧗ as of " + report.FetchedAt.Format("15:04"))
	}
	pb.WriteString("\n  " + stamp + " " + dim.Render("· via "+source) + "\n")
}

// writeAgyAuthHint shows the Antigravity authorization affordance in its Usage
// tab: a [a] prompt, the in-progress state, or the last result. Antigravity is
// the one provider that needs its own one-time consent (it stores no reusable
// token), so we surface the flow inline rather than sending the user to a CLI.
func (m dashboardModel) writeAgyAuthHint(pb *strings.Builder) {
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	switch {
	case m.agyAuthing:
		warn := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		pb.WriteString("\n  " + warn.Render("⏳ Authorizing…") + " " + dim.Render("approve in your browser") + "\n")
	case m.agyAuthStatus != "":
		color := ColorSuccess
		if strings.HasPrefix(m.agyAuthStatus, "✗") {
			color = ColorDanger
		}
		pb.WriteString("\n  " + lipgloss.NewStyle().Foreground(color).Render(m.agyAuthStatus) + "\n")
	default:
		key := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
		pb.WriteString("\n  " + key.Render("[a]") + " " + dim.Render("Authorize Antigravity in your browser") + "\n")
	}
}

// renderConnectionsSummary renders the live MCP sessions for the left
// diagnostics column: remote boxes by host/IP/OS, then a local-agent count. When
// compact (short terminal) each remote collapses to a single host+IP line so the
// panel fits the screen height.
// renderAgentCard renders one bordered agent-brand card at the given content width.
// The card is always exactly two content lines (4 rows incl. border): the brand
// name on line 1 and a status line on line 2. The status line degrades gracefully
// (drops the ⇄N count, then the trust badge) so it never wraps onto a third line —
// width is supplied by agentGridLayout, which caps the grid at 3 columns so cards
// stay wide enough for the full status in the common case.
func (m dashboardModel) renderAgentCard(b agentCard, idx, gridCardW int) string {
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	yellow := lipgloss.NewStyle().Foreground(ColorWarning)
	bold := lipgloss.NewStyle().Bold(true)

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

	// Count live MCP sessions for this brand from the session registry (ground
	// truth). "active" = at least one connection right now. We do NOT fall back to
	// recent audit activity: background apps (e.g. the Antigravity IDE) reconnect
	// every few minutes just to enumerate tools, which would falsely light the card.
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

	// Brand-colored glyph on the name line; the status line sits below, aligned to
	// the same left edge (Top join keeps both text lines flush regardless of the
	// glyph's rendered width).
	icon := brandMark(b.id)
	// Width of the text column to the right of the icon: card content width minus the
	// icon glyph, the 2-space join gutter, and the card's 1-col side padding. Keeping
	// name and status within this budget guarantees each is exactly one row, so every
	// card is two content lines (4 rows with the border) — never a wrapped trust
	// badge on a third line. Conservative by design (errs toward dropping a segment,
	// never wrapping).
	textW := gridCardW - lipgloss.Width(icon) - 4
	if textW < 8 {
		textW = 8
	}

	// truncate is byte-based; brand names are ASCII so this is safe. Style only after
	// truncation so we never cut inside an ANSI sequence.
	nameRaw := truncate(b.name, textW)
	if isActive {
		dotPart := lipgloss.NewStyle().Foreground(lipgloss.Color("#73CBAD")).Render("●")
		var activePart string
		if hasCursor {
			activePart = renderHoverShimmerText("active", m.blinkCycle)
		} else {
			activePart = renderShimmerText("active", m.blinkCycle)
		}
		statusDot = fmt.Sprintf("%s %s", dotPart, activePart)
		cardName = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#73CBAD")).Render(nameRaw)
	} else {
		statusDot = dim.Render("○ idle")
		cardName = bold.Render(nameRaw)
	}

	// Status line degrades gracefully to stay on one row: drop the least-essential
	// segment first (the ⇄N connection count), then the trust badge, before it would
	// ever wrap. We measure with lipgloss.Width (ANSI-aware) and only ever drop whole
	// styled segments — never cut inside one.
	connInfo := dim.Render(fmt.Sprintf("⇄%d", connCount))
	statusLine := fmt.Sprintf("%s · %s · %s", statusDot, connInfo, trustBadge)
	if lipgloss.Width(statusLine) > textW {
		statusLine = fmt.Sprintf("%s · %s", statusDot, trustBadge)
	}
	if lipgloss.Width(statusLine) > textW {
		statusLine = statusDot
	}
	textBlock := cardName + "\n" + statusLine
	body := lipgloss.JoinHorizontal(lipgloss.Top, icon, "  ", textBlock)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cardBorderColor).
		Padding(0, 1).
		Width(gridCardW).
		Render(body)
}

func (m dashboardModel) renderConnectionsSummary(compact bool) string {
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

		// Collapse identical remote sessions (same host + IP + provider) into a single
		// row with a ×N count: three live MCP processes from the same box render as one
		// "host … ×3" line instead of three duplicate rows. First-seen order is kept.
		// NOTE: this grouping is display-only — the agent cards still count every live
		// PID for their ⇄N badge, so a host with 3 sessions reads ⇄3 on its card and
		// "×3" here.
		type remoteGroup struct {
			s     agentSession
			count int
		}
		var groups []remoteGroup
		groupIdx := make(map[string]int, len(remotes))
		for _, s := range remotes {
			key := strings.ToLower(s.Host + "|" + s.IP + "|" + s.Provider)
			if i, ok := groupIdx[key]; ok {
				groups[i].count++
				continue
			}
			groupIdx[key] = len(groups)
			groups = append(groups, remoteGroup{s: s, count: 1})
		}

		for _, g := range groups {
			s := g.s
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
			countSuffix := ""
			if g.count > 1 {
				countSuffix = fmt.Sprintf("  ×%d", g.count)
			}
			if compact {
				sb.WriteString(fmt.Sprintf("%s %s  %s%s\n",
					teal.Render("●"), cyan.Render(host), dim.Render(loc), dim.Render(countSuffix)))
				continue
			}
			// Scope rides on the same detail line (not a new row) to keep the left
			// column short enough that the rich dashboard still fits shorter terminals.
			detail := fmt.Sprintf("%s · %s · %s", loc, osLabel, s.Provider)
			if sc := m.remoteScope[s.Host]; sc != "" {
				detail += " · 🔑 " + sc
			}
			sb.WriteString(fmt.Sprintf("%s %s%s\n   %s\n",
				teal.Render("●"),
				cyan.Render(host),
				dim.Render(countSuffix),
				dim.Render(detail),
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
		sb.WriteString(yellow.Render(fmt.Sprintf("⚠ %d %s on an older build", m.unregistered, label)))
		sb.WriteString("\n  " + dim.Render("provider inferred — reconnect to confirm"))
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
		logs = "Configured via claude_desktop_config.json."
	case "claude-code":
		name = "Claude Code CLI"
		icon = "💻"
		logs = "Registered through the Claude Code MCP config."
	case "antigravity":
		name = "Antigravity"
		icon = "🌌"
		logs = "Antigravity / Gemini surface configured under ~/.gemini."
	case "cursor":
		name = "Cursor"
		icon = "🎯"
		logs = "Configured in Cursor's MCP settings."
	case "codex":
		name = "Codex"
		icon = "💻"
		logs = "Configured in ~/.codex/config.toml."
	case "gemini":
		name = "Gemini"
		icon = "✨"
		logs = "Gemini CLI / Code Assist configured under ~/.gemini."
	}

	// Fallback for brands without a bespoke blurb (copilot, perplexity, warp, …) so
	// the popup always shows a real name/icon instead of an empty header.
	if name == "" {
		if meta, ok := brandMeta[provider]; ok {
			name, icon = meta.name, meta.icon
		} else {
			name, icon = provider, "🔌"
		}
	}
	if logs == "" {
		logs = "Configured via its MCP server settings."
	}

	var pb strings.Builder
	header := fmt.Sprintf("%s %s AGENT CENTER", icon, strings.ToUpper(name))
	pb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(header) + "\n\n")

	tab1 := "[1 Info & Diagnostics]"
	tab2 := "[2 Recent Writes]"
	tab3 := "[3 Connected]"
	tab4 := "[4 Usage]"
	switch m.activeAgentTab {
	case 0:
		tab1 = cyan.Render(tab1)
	case 1:
		tab2 = cyan.Render(tab2)
	case 2:
		tab3 = cyan.Render(tab3)
	case 3:
		tab4 = cyan.Render(tab4)
	}
	pb.WriteString(fmt.Sprintf("  %s    %s    %s    %s\n\n", tab1, tab2, tab3, tab4))

	if m.activeAgentTab == 0 {
		matchesProvider := func(p string) bool {
			return p == provider || (provider == "antigravity" && strings.HasPrefix(p, "antigravity"))
		}

		writes := 0
		if m.stats != nil && m.stats.ByProvider != nil {
			if provider == "antigravity" {
				writes = m.stats.ByProvider["antigravity"] + m.stats.ByProvider["antigravity-ide"] + m.stats.ByProvider["antigravity-agent"] + m.stats.ByProvider["antigravity-cli"]
			} else {
				writes = m.stats.ByProvider[provider]
			}
		}

		// Real live-session facts for this brand (from the reconciled registry).
		liveCount, inferred, remote := 0, false, false
		for _, s := range m.sessions {
			if matchesProvider(s.Provider) {
				liveCount++
				inferred = inferred || s.Unregistered
				remote = remote || s.Remote
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

		statusVal := green.Render("● connected")
		if liveCount == 0 {
			statusVal = dim.Render("○ idle — no live session")
		}
		sessVal := bold.Render(fmt.Sprintf("%d", liveCount))
		if inferred {
			sessVal += "  " + yellow.Render("(provider inferred — reconnect to confirm)")
		}
		connVal := bold.Render("MCP · stdio")
		if remote {
			connVal = bold.Render("MCP · stdio (SSH-remote)")
		}

		// Pad the label as plain text BEFORE styling so columns line up — the
		// previous %-18s counted ANSI escape bytes and never aligned visibly.
		row := func(label, value string) {
			pb.WriteString("  " + dim.Render(fmt.Sprintf("%-15s", label)) + " " + value + "\n")
		}
		row("Status", statusVal)
		row("Trust level", trustBadge)
		row("Live sessions", sessVal)
		row("Total writes", bold.Render(fmt.Sprintf("%d", writes)))
		row("Connection", connVal)
		pb.WriteString("\n  " + dim.Render(logs) + "\n")
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
	} else if m.activeAgentTab == 2 {
		m.renderConnectedTab(&pb, provider)
	} else {
		m.renderUsageTab(&pb, provider)
	}

	pb.WriteString("\n")
	pb.WriteString(StyleFooter.Render("  Press [Esc] to close • [1/2/3/4] Switch tabs"))

	popupStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(82)

	return popupStyle.Render(pb.String())
}

// renderUsagePopup is the all-agents Live Usage overlay opened with [u]: every
// brand's account/tier + quota meters in one box, sharing the Analytics renderer.
func (m dashboardModel) renderUsagePopup() string {
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	var pb strings.Builder
	pb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("🔋 LIVE USAGE — ALL AGENTS") + "\n\n")
	if !m.liveUsage {
		pb.WriteString(dim.Render("Live Usage is off. Enable it in Settings (6).") + "\n")
	} else {
		pb.WriteString(renderUsageRows(m.usageReports, 16) + "\n")
	}
	pb.WriteString("\n" + StyleFooter.Render("  [r] refresh • [Esc]/[u] close"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(82).
		Render(pb.String())
}

// agentCard describes one brand tile on the dashboard grid. Used by both the
// renderer and the mouse hit-tester so they share one ordering and naming.
type agentCard struct {
	id    string
	name  string
	icon  string
	color string
}

// agentCardOrder is the single source of truth for the agent grid: its order,
// ids, display names, icons, and accent colors.
// brandMeta is the display metadata per provider brand. Adding a provider to
// detect.InstalledAgents() makes it appear here automatically; an unknown brand
// still renders via a neutral default, so nothing is ever silently hidden.
// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// agentGridLayout decides how the agent-brand grid fills the width: how many
// columns and how wide each card's content is. It fits as many minimum-width cards
// as the space allows (more columns on wide terminals → fewer rows, so the grid is
// shorter), then flexes the chosen cards to consume the leftover width. The card
// design is unchanged — only the column count and width adapt.
func agentGridLayout(termWidth, numCards int, compact bool) (cols, cardContentW int) {
	const (
		maxContent = 40 // ceiling so cards add columns instead of stretching sparse
		borderPad  = 2  // rounded border adds 1 column each side
		gap        = 1  // single space between cards in a row
		leftCol    = 46 // rendered width of the diagnostics column (44 + border)
		leftGap    = 4  // gutter between diagnostics and the grid when side by side
	)
	// In compact mode, allow slightly narrower cards so an extra column fits — fewer
	// rows means the grid is shorter and the dashboard fits the height. 22 is the
	// floor that still holds the status line ("○ idle · ⇄0 · auto") without wrapping
	// (which would add height and defeat the purpose).
	minContent := 24
	if compact {
		minContent = 22
	}
	// Before the first WindowSizeMsg, keep the original 2-up / 30-wide default so
	// the first frame doesn't flash a degraded layout.
	if termWidth <= 0 {
		if numCards == 1 {
			return 1, 30
		}
		return 2, 30
	}
	if numCards <= 0 {
		return 1, minContent
	}
	avail := termWidth
	if termWidth >= 116 { // grid sits to the right of the diagnostics column
		avail -= leftCol + leftGap
	}
	minCell := minContent + borderPad
	if avail < minCell {
		avail = minCell
	}
	cols = (avail + gap) / (minCell + gap)
	if cols < 1 {
		cols = 1
	}
	if cols > numCards {
		cols = numCards
	}
	// Hard ceiling: never more than 3 columns regardless of how wide the terminal
	// is. Fewer columns means wider cards, so the status line ("● active · ⇄N ·
	// auto") always has room on a single row instead of wrapping a trust badge onto
	// a third line. On narrow terminals the width math above still drops to 2 or 1.
	const maxCols = 3
	if cols > maxCols {
		cols = maxCols
	}
	cellW := (avail - (cols-1)*gap) / cols
	cardContentW = clampInt(cellW-borderPad, minContent, maxContent)
	return cols, cardContentW
}

var brandMeta = map[string]agentCard{
	"claude":         {"claude", "Claude Desktop", "🧠", "99"},
	"claude-code":    {"claude-code", "Claude Code CLI", "💻", "39"},
	"antigravity":    {"antigravity", "Antigravity", "🌌", "205"},
	"cursor":         {"cursor", "Cursor", "🎯", "39"},
	"codex":          {"codex", "Codex", "💻", "34"},
	"gemini":         {"gemini", "Gemini", "✨", "220"},
	"copilot":        {"copilot", "GitHub Copilot", "🐙", "240"},
	"perplexity":     {"perplexity", "Perplexity", "🔮", "37"},
	"warp":           {"warp", "Warp", "⚡", "39"},
	"void":           {"void", "Void", "🕳️", "205"},
	"android-studio": {"android-studio", "Android Studio", "🤖", "34"},
	"kimi":           {"kimi", "Kimi", "🌙", "34"},
	"trae":           {"trae", "Trae", "🔷", "39"},
}

// brandOrder is the canonical card order; detected brands not listed fall to the
// end (in detection order) so newly-added providers never need a code change here.
var brandOrder = []string{"claude", "claude-code", "antigravity", "cursor", "codex", "gemini", "copilot", "perplexity", "warp", "void", "android-studio", "kimi", "trae"}

// agentCardOrder returns one card per brand that is either DETECTED on this
// machine (config/binary present) OR has audit activity (it connected and wrote,
// even if we don't statically detect it — e.g. Android Studio, wired by hand via
// the JetBrains AI Assistant stdio dialog, which leaves no config file to detect).
// Deduped, in canonical order. This keeps the dashboard showing only relevant
// agents: with 100 supported, it still shows only the ones installed or active.
func agentCardOrder(activity []string) []agentCard {
	hidden := hiddenAgentSet()
	var providers []string
	for _, a := range detect.InstalledAgents() {
		providers = append(providers, a.Provider)
	}
	providers = append(providers, activity...)
	return buildAgentCards(filterHiddenProviders(providers, hidden))
}

// filterHiddenProviders drops any provider whose canonical brand is in the hide
// set. Pure (no config/detect access) so the dashboard hide behavior is testable.
func filterHiddenProviders(providers []string, hidden map[string]bool) []string {
	if len(hidden) == 0 {
		return providers
	}
	kept := make([]string, 0, len(providers))
	for _, p := range providers {
		if !hidden[canonicalProvider(p)] {
			kept = append(kept, p)
		}
	}
	return kept
}

// hiddenAgentSet loads the user's Settings → Agents hide list as a set of
// canonical brand ids. Read fresh each refresh so toggles apply on the next tick.
func hiddenAgentSet() map[string]bool {
	hidden := config.LoadSettings().HiddenAgents
	if len(hidden) == 0 {
		return nil
	}
	set := make(map[string]bool, len(hidden))
	for _, h := range hidden {
		if c := canonicalProvider(h); c != "" {
			set[c] = true
		}
	}
	return set
}

// providersWithActivity returns every provider that has ANY audit history (writes
// or connect/skill/disconnect events), sorted for determinism. This is the signal
// that surfaces a manually-wired agent on the dashboard the moment it has activity
// — independent of whether it can be statically detected or auto-configured.
func providersWithActivity(stats *audit.Stats) []string {
	if stats == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]int{stats.TotalLogsByProvider, stats.ByProvider} {
		for p := range m {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// providerAlias folds finer-grained audit provider tags onto their dashboard
// brand so one brand shows one card. The audit log records per-surface tags
// (e.g. antigravity-ide / -cli / -agent) and the occasional hand-typed variant;
// the dashboard groups them under the brand. Keep keys lowercase.
var providerAlias = map[string]string{
	"antigravity-ide":   "antigravity",
	"antigravity-cli":   "antigravity",
	"antigravity-agent": "antigravity",
	"as":                "android-studio",
}

// nonAgentProviders are audit "providers" that are not user-facing agents and
// must never get a dashboard card (internal bookkeeping / empty).
var nonAgentProviders = map[string]bool{"system": true, "": true}

// canonicalProvider normalizes an audit/detect provider tag to its dashboard
// brand id, returning "" for tags that should not produce a card at all.
func canonicalProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if nonAgentProviders[p] {
		return ""
	}
	if a, ok := providerAlias[p]; ok {
		return a
	}
	return p
}

// buildAgentCards is the pure mapping behind agentCardOrder (split out so it's
// testable without depending on what's installed on the test machine): known
// brands first in canonical brandOrder, then any unknown providers appended in
// order with a neutral card. Provider tags are canonicalized (per-surface tags
// folded to their brand, internal tags dropped). Deduped, deterministic.
func buildAgentCards(detected []string) []agentCard {
	canon := make([]string, 0, len(detected))
	for _, p := range detected {
		if c := canonicalProvider(p); c != "" {
			canon = append(canon, c)
		}
	}
	detected = canon

	present := map[string]bool{}
	for _, p := range detected {
		if p != "" {
			present[p] = true
		}
	}

	var cards []agentCard
	emitted := map[string]bool{}
	for _, id := range brandOrder {
		if present[id] && !emitted[id] {
			emitted[id] = true
			cards = append(cards, brandMeta[id])
		}
	}
	for _, p := range detected {
		if p == "" || emitted[p] {
			continue
		}
		emitted[p] = true
		if meta, ok := brandMeta[p]; ok {
			cards = append(cards, meta)
		} else {
			cards = append(cards, agentCard{id: p, name: titleCaseBrand(p), icon: "🔌", color: "240"})
		}
	}
	return cards
}

// titleCaseBrand turns a provider key like "my-agent" into "My Agent" for display.
func titleCaseBrand(s string) string {
	s = strings.ReplaceAll(s, "-", " ")
	parts := strings.Fields(s)
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// visibleColumnOf returns the visible (terminal cell) column at which substr
// begins in a plain, ANSI-stripped line, or -1 when substr is absent.
func visibleColumnOf(clean, substr string) int {
	idx := strings.Index(clean, substr)
	if idx < 0 {
		return -1
	}
	return runewidth.StringWidth(clean[:idx])
}

// popupVisibleWidth returns the widest visible line in a rendered popup box.
func popupVisibleWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if vw := visibleWidth(l); vw > w {
			w = vw
		}
	}
	return w
}

// tabAtColumn partitions the popup tab row by the actual columns of its "[1 ",
// "[2 ", "[3 ", "[4 " markers and returns the 0-based tab index containing
// clickX (an absolute terminal column), or -1 if the row carries no markers.
// originX is the popup box's left edge. Absent markers are skipped (so a row
// with fewer tabs still hit-tests), and the first present tab also catches
// clicks in the left margin so the row reads as contiguous zones.
func tabAtColumn(cleanRow string, originX, clickX int) int {
	markers := []string{"[1 ", "[2 ", "[3 ", "[4 "}
	type tabCol struct {
		idx int
		col int
	}
	var cols []tabCol
	for i, mk := range markers {
		if c := visibleColumnOf(cleanRow, mk); c >= 0 {
			cols = append(cols, tabCol{idx: i, col: originX + c})
		}
	}
	if len(cols) == 0 {
		return -1
	}
	for i := range cols {
		start := cols[i].col
		if i == 0 {
			start = originX
		}
		end := 1 << 30
		if i+1 < len(cols) {
			end = cols[i+1].col
		}
		if clickX >= start && clickX < end {
			return cols[i].idx
		}
	}
	return -1
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
