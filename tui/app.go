package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/usage"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type screen int

const (
	screenDashboard screen = iota
	screenActivity
	screenBrowser
	screenDiff
	screenAnalytics
	screenSettings
	screenSSH
	screenSkills
	screenAuditTrail
	screenViewer // must stay last
)

var screenNames = []string{
	"Dashboard",
	"Activity",
	"Files",
	"Approvals",
	"Analytics",
	"Settings",
	"Remote",
	"Skills",
	"Audit Trail",
}

type model struct {
	memoryPath string
	screen     screen
	width      int
	height     int

	// Sub-models
	dashboard  dashboardModel
	activity   activityModel
	auditTrail auditTrailModel
	browser    browserModel
	diff       diffModel
	analytics  analyticsModel
	search     searchModel
	settings   settingsModel
	ssh        sshModel
	skills     skillsModel
	viewer     viewerModel // must stay last (no tab number)

	// Shared state
	store      *memory.Store
	logger     *audit.Logger
	pendingMgr *pending.Manager
	usageMgr   *usage.Manager // one Live Usage manager shared by dashboard + analytics

	// contentVP scrolls long content pages within a fixed header (full logo + tabs)
	// and footer — the standard Bubble Tea header/viewport/footer pattern. The
	// dashboard and the file viewer manage their own height and don't use it.
	contentVP viewport.Model
	vpReady   bool
}

func NewApp(memoryPath string) *model {
	store := memory.NewStore(memoryPath)
	logger, _ := audit.NewLogger(memoryPath)
	pendingMgr := pending.NewManager(memoryPath)
	usageMgr := usage.New()

	return &model{
		memoryPath: memoryPath,
		screen:     screenDashboard,
		store:      store,
		logger:     logger,
		pendingMgr: pendingMgr,
		usageMgr:   usageMgr,
		dashboard:  newDashboardModel(logger, pendingMgr, memoryPath, usageMgr),
		activity:   newActivityModel(logger),
		auditTrail: newAuditTrailModel(logger),
		browser:    newBrowserModel(store),
		diff:       newDiffModel(pendingMgr),
		analytics:  newAnalyticsModel(logger, usageMgr),
		search:     newSearchModel(store),
		settings:   newSettingsModel(memoryPath, logger),
		ssh:        newSSHModel(),
		skills:     newSkillsModel(),
		viewer:     newViewerModel(store),
	}
}

func (m model) Init() tea.Cmd {
	return m.refreshCurrentScreen()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case OpenFileMsg:
		m.viewer = m.viewer.load(msg.Filename, msg.Content, msg.Editable)
		m.screen = screenViewer
		return m, nil
	case tea.KeyMsg:
		if m.screen == screenDashboard && m.dashboard.selectedAgent != "" {
			// While the agent popup is open, its tab keys (1/2/3/4) must reach the
			// dashboard rather than the parent's screen switcher. "3" was missing,
			// so it fell through and jumped to the Files screen.
			switch msg.String() {
			case "1", "2", "3", "4":
				var cmd tea.Cmd
				m.dashboard, cmd = m.dashboard.Update(msg)
				return m, cmd
			}
		}

		if m.screen == screenSSH && m.ssh.editingHost {
			var cmd tea.Cmd
			m.ssh, cmd = m.ssh.Update(msg)
			// These sub-modes early-return, so refresh the viewport here too —
			// otherwise the page (in the content viewport) keeps showing the stale
			// frame and the wizard looks frozen even though its state advanced.
			if m.vpReady {
				m.syncViewport()
			}
			return m, cmd
		}

		if m.screen == screenSettings && m.settings.configuringCustom {
			var cmd tea.Cmd
			m.settings, cmd = m.settings.Update(msg)
			if m.vpReady {
				m.syncViewport()
			}
			return m, cmd
		}

		// While editing a memory file, every keystroke (incl. q, digits, esc,
		// ctrl+s) must reach the editor — never the global tab/quit switch below.
		if m.screen == screenViewer && m.viewer.editing {
			var cmd tea.Cmd
			m.viewer, cmd = m.viewer.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			if m.logger != nil {
				m.logger.Close()
			}
			return m, tea.Quit
		case "1":
			return m, m.gotoScreen(screenDashboard)
		case "2":
			return m, m.gotoScreen(screenActivity)
		case "3":
			return m, m.gotoScreen(screenBrowser)
		case "4":
			return m, m.gotoScreen(screenDiff)
		case "5":
			return m, m.gotoScreen(screenAnalytics)
		case "6":
			return m, m.gotoScreen(screenSettings)
		case "7":
			return m, m.gotoScreen(screenSSH)
		case "8":
			return m, m.gotoScreen(screenSkills)
		case "9":
			return m, m.gotoScreen(screenAuditTrail)
		case "]", "tab":
			if msg.String() == "tab" && ((m.screen == screenDashboard && m.dashboard.selectedAgent != "") ||
				(m.screen == screenSSH && m.ssh.editingHost) ||
				(m.screen == screenSettings && (m.settings.configuringCustom || m.settings.showingDiff || m.settings.confirmingRun))) {
				break
			}
			next := m.screen
			if next == screenViewer {
				next = screenBrowser
			}
			return m, m.gotoScreen((next + 1) % 9)
		case "[", "shift+tab", "backtab":
			if (msg.String() == "shift+tab" || msg.String() == "backtab") && ((m.screen == screenDashboard && m.dashboard.selectedAgent != "") ||
				(m.screen == screenSSH && m.ssh.editingHost) ||
				(m.screen == screenSettings && (m.settings.configuringCustom || m.settings.showingDiff || m.settings.confirmingRun))) {
				break
			}
			prev := m.screen
			if prev == screenViewer {
				prev = screenBrowser
			}
			if prev == 0 {
				return m, m.gotoScreen(8)
			}
			return m, m.gotoScreen((prev - 1) % 9)
		case "pgup", "pgdown", "ctrl+u", "ctrl+d", "home", "end":
			// Scroll the content viewport on long pages, unless a modal/editor owns
			// these keys.
			if m.usesViewport() && m.vpReady && !m.inModalSubmode() {
				var vcmd tea.Cmd
				m.contentVP, vcmd = m.contentVP.Update(msg)
				return m, vcmd
			}
		case "esc":
			if m.screen == screenViewer {
				return m, m.gotoScreen(screenBrowser)
			}
		}

	case animationTickMsg:
		m.dashboard.blinkCycle = (m.dashboard.blinkCycle + 1) % 24
		return m, animationTickCmd()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Propagate window size to all submodels immediately so they adapt responsively
		m.dashboard, _ = m.dashboard.Update(msg)
		m.activity, _ = m.activity.Update(msg)
		m.auditTrail, _ = m.auditTrail.Update(msg)
		m.browser, _ = m.browser.Update(msg)
		m.diff, _ = m.diff.Update(msg)
		m.analytics, _ = m.analytics.Update(msg)
		m.search, _ = m.search.Update(msg)
		m.settings, _ = m.settings.Update(msg)
		m.ssh, _ = m.ssh.Update(msg)
		m.skills, _ = m.skills.Update(msg)
		m.viewer, _ = m.viewer.Update(msg)
		m.syncViewport()

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			banner := renderBanner(m.width)
			tabRow := strings.Count(banner, "\n")
			if msg.Y >= tabRow-1 && msg.Y <= tabRow+1 {
				startX := 0
				for idx, name := range screenNames {
					tabWidth := 4 + len(name) + 2
					if msg.X >= startX && msg.X < startX+tabWidth {
						return m, m.gotoScreen(screen(idx))
					}
					startX += tabWidth
				}
			}
		}
		// Mouse wheel scrolls the content viewport on long pages.
		if m.usesViewport() && m.vpReady &&
			(msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown) {
			var vcmd tea.Cmd
			m.contentVP, vcmd = m.contentVP.Update(msg)
			return m, vcmd
		}
	}

	// Delegate to sub-model
	var cmd tea.Cmd
	switch m.screen {
	case screenDashboard:
		m.dashboard, cmd = m.dashboard.Update(msg)
	case screenActivity:
		m.activity, cmd = m.activity.Update(msg)
	case screenBrowser:
		m.browser, cmd = m.browser.Update(msg)
	case screenDiff:
		m.diff, cmd = m.diff.Update(msg)
	case screenAnalytics:
		m.analytics, cmd = m.analytics.Update(msg)
	case screenSettings:
		m.settings, cmd = m.settings.Update(msg)
	case screenSSH:
		m.ssh, cmd = m.ssh.Update(msg)
	case screenSkills:
		m.skills, cmd = m.skills.Update(msg)
	case screenAuditTrail:
		m.auditTrail, cmd = m.auditTrail.Update(msg)
	case screenViewer:
		m.viewer, cmd = m.viewer.Update(msg)
	}

	// Keep the content viewport sized and filled for the active screen after the
	// sub-model updated (selection moved, data refreshed). Re-sizing here (not just
	// SetContent) keeps the viewport height consistent with a footer that may have
	// gained/lost the scroll-hint line. Scroll offset is preserved.
	if m.usesViewport() && m.vpReady {
		m.syncViewport()
	}

	return m, cmd
}

// usesViewport reports whether the active screen scrolls its content inside the
// shared viewport. The dashboard fits itself (fixed overview) and the file viewer
// scrolls its own content, so they render directly.
func (m model) usesViewport() bool {
	switch m.screen {
	case screenDashboard, screenViewer:
		return false
	}
	return true
}

// inModalSubmode reports whether the active screen has a focused modal/editor that
// owns its own keys (so the parent must not steal scroll keys for the viewport).
func (m model) inModalSubmode() bool {
	switch m.screen {
	case screenSettings:
		return m.settings.configuringCustom || m.settings.showingDiff || m.settings.confirmingRun
	case screenSSH:
		return m.ssh.editingHost
	case screenActivity:
		return m.activity.viewingDetail
	case screenAuditTrail:
		return m.auditTrail.viewingDetail
	}
	return false
}

// screenContent renders only the active screen's body (no banner/tabs/footer).
func (m model) screenContent() string {
	switch m.screen {
	case screenDashboard:
		return m.dashboard.View()
	case screenActivity:
		return m.activity.View()
	case screenBrowser:
		return m.browser.View()
	case screenDiff:
		return m.diff.View()
	case screenAnalytics:
		return m.analytics.View(m.width)
	case screenSettings:
		return m.settings.View()
	case screenSSH:
		return m.ssh.View()
	case screenSkills:
		return m.skills.View(m.width, m.height)
	case screenAuditTrail:
		return m.auditTrail.View()
	case screenViewer:
		return m.viewer.View()
	}
	return ""
}

// chromeHeight is the number of rows consumed by the fixed header (full logo +
// tabs), the two blank separators, and the footer — i.e. everything that is NOT
// the scrollable content region.
func (m model) chromeHeight() int {
	return lipgloss.Height(renderBanner(m.width)) +
		lipgloss.Height(m.renderTabs()) +
		lipgloss.Height(m.renderFooter()) + 2 // two blank separator rows
}

// syncViewport (re)sizes the content viewport to the rows left under the fixed
// chrome and refreshes its content for the active screen. Scroll offset is
// preserved across data ticks; callers reset it on a screen switch.
func (m *model) syncViewport() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	h := m.height - m.chromeHeight()
	if h < 1 {
		h = 1
	}
	if !m.vpReady {
		m.contentVP = viewport.New(m.width, h)
		m.vpReady = true
	} else {
		m.contentVP.Width = m.width
		m.contentVP.Height = h
	}
	if m.usesViewport() {
		m.contentVP.SetContent(m.screenContent())
	}
}

// gotoScreen switches the active tab, resets the content scroll to the top, and
// refreshes the new screen's data.
func (m *model) gotoScreen(s screen) tea.Cmd {
	m.screen = s
	if m.vpReady {
		m.contentVP.GotoTop()
	}
	cmd := m.refreshCurrentScreen()
	m.syncViewport()
	return cmd
}

func (m model) View() string {
	banner := renderBanner(m.width)
	tabs := m.renderTabs()
	footer := m.renderFooter()

	var out string
	if m.usesViewport() && m.vpReady {
		// Content pages: fixed header + scrolling viewport + fixed footer. The
		// viewport makes the body exactly its height, so the chrome can never be
		// pushed off the top, and content scrolls instead of being truncated.
		out = fmt.Sprintf("%s\n%s\n\n%s\n\n%s", banner, tabs, m.contentVP.View(), footer)
	} else {
		// Dashboard / viewer manage their own height. The dashboard tightens its body
		// to fit (full logo always); the clamp keeps the chrome on screen if a pane is
		// too short for even the compact body.
		content := m.screenContent()
		tight := m.screen == screenDashboard && m.dashboard.bodyCompact()
		blank := 2
		if tight {
			blank = 0
		}
		if m.height > 0 {
			used := lipgloss.Height(banner) + lipgloss.Height(tabs) + lipgloss.Height(footer) + blank
			content = clampContentHeight(content, m.height-used)
		}
		if tight {
			out = fmt.Sprintf("%s\n%s\n%s\n%s", banner, tabs, content, footer)
		} else {
			out = fmt.Sprintf("%s\n%s\n\n%s\n\n%s", banner, tabs, content, footer)
		}
	}

	// Final safety net: never emit more rows than the terminal has, so the fixed
	// header (logo + tab menu) can never scroll off the top in alt-screen mode —
	// even if a footer-height change races a content update by a frame.
	if m.height > 0 && lipgloss.Height(out) > m.height {
		lines := strings.Split(out, "\n")
		out = strings.Join(lines[:m.height], "\n")
	}
	return out
}

// clampContentHeight bounds the dashboard/viewer body so the fixed chrome is never
// scrolled off the top of an alt-screen terminal. Content pages use the viewport
// instead and never reach this. When the body is taller it keeps the top rows and
// notes how many are hidden (only the dashboard on a genuinely tiny pane).
func clampContentHeight(content string, maxLines int) string {
	if maxLines < 1 {
		maxLines = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	hidden := len(lines) - (maxLines - 1)
	kept := append([]string{}, lines[:maxLines-1]...)
	note := "\x1b[0m" + StyleFooter.Render(
		fmt.Sprintf("  ▾ %d more — enlarge window", hidden))
	return strings.Join(append(kept, note), "\n")
}

func (m model) renderTabs() string {
	var tabs string
	for i, name := range screenNames {
		if screen(i) == m.screen {
			tabs += lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorPrimary).
				Padding(0, 1).
				Render(fmt.Sprintf("[%d] %s", i+1, name))
		} else {
			tabs += lipgloss.NewStyle().
				Foreground(ColorDim).
				Padding(0, 1).
				Render(fmt.Sprintf(" %d  %s", i+1, name))
		}
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(ColorDim).
		Render(tabs)
}

func (m model) renderFooter() string {
	var footerText string
	switch m.screen {
	case screenDashboard:
		if m.dashboard.selectedAgent != "" {
			footerText = "Tab: Switch popup tabs • Esc: Close popup • q: Quit"
		} else {
			footerText = "j/k/h/l: Navigate cards • Enter: Configure • r: Force refresh (rescan agents) • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
		}
	case screenActivity:
		if m.activity.viewingDetail {
			footerText = "j/k: Scroll diff • Esc: Close details • q: Quit"
		} else {
			footerText = "j/k: Navigate feed • Enter: View details • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
		}
	case screenBrowser:
		footerText = "j/k: Navigate files • Enter: View file • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	case screenViewer:
		if m.viewer.editing {
			footerText = "Ctrl+S: Save • Esc: Cancel edit (discard changes)"
		} else if m.viewer.editable {
			footerText = "j/k: Scroll • PgUp/PgDn: Page • g/G: Top/Bottom • e: Edit • d: Download • Esc: Back • [ / ]: Tabs • q: Quit"
		} else {
			footerText = "j/k: Scroll • PgUp/PgDn: Page • g/G: Top/Bottom • d: Download • Esc: Back (read-only) • [ / ]: Tabs • q: Quit"
		}
	case screenDiff:
		if m.diff.viewing != "" {
			footerText = "a: Approve • r: Reject • Esc: Back to queue • q: Quit"
		} else {
			footerText = "j/k: Navigate queue • Enter: View diff • a: Approve • r: Reject • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
		}
	case screenAnalytics:
		footerText = "Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	case screenSettings:
		if m.settings.showingDiff {
			footerText = "j/k: Scroll diff • Esc/Enter: Close diff • q: Quit"
		} else if m.settings.confirmingRun {
			footerText = "y/n or Enter/Esc: Confirm/Cancel run • q: Quit"
		} else if m.settings.configuringCustom {
			footerText = "Enter: Fetch models/configure • Esc: Cancel • q: Quit"
		} else {
			footerText = "↑/↓: Select option • ←/→: Cycle agent overrides • Enter: Toggle option/Run • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
		}
	case screenSSH:
		footerText = "j/k: Select • c: Connect new • t: Test • p: Print config • d: Remove • Tab/[ / ]: Switch tabs • q: Quit"
	case screenSkills:
		footerText = "j/k: Navigate commands • d: Export Claude skills ZIP • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	case screenAuditTrail:
		if m.auditTrail.viewingDetail {
			footerText = "j/k: Scroll details • Esc: Close details • q: Quit"
		} else {
			footerText = "j/k: Navigate logs • Enter: View details • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
		}
	default:
		footerText = "Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	}

	footer := StyleFooter.Render(footerText)

	// On a content page whose body overflows, surface a scroll affordance so the
	// extra rows are discoverable (progressive disclosure) rather than silently
	// off-screen. Kept on its own line so it never widens the footer past the term.
	if m.usesViewport() && m.vpReady && !m.inModalSubmode() &&
		m.contentVP.TotalLineCount() > m.contentVP.Height {
		pct := int(m.contentVP.ScrollPercent() * 100)
		hint := lipgloss.NewStyle().Foreground(ColorPrimary).
			Render(fmt.Sprintf("  ⇕ %d%% · PgUp/PgDn or wheel to scroll", pct))
		footer = lipgloss.JoinVertical(lipgloss.Left, hint, footer)
	}

	return footer
}

func (m *model) refreshCurrentScreen() tea.Cmd {
	switch m.screen {
	case screenDashboard:
		// Re-read the Live Usage opt-in each time the dashboard is (re)entered so
		// the Settings toggle takes effect without restarting the TUI.
		m.dashboard.liveUsage = config.LoadSettings().LiveUsage
		return m.dashboard.Refresh()
	case screenActivity:
		return m.activity.Refresh()
	case screenBrowser:
		return m.browser.Refresh()
	case screenDiff:
		return m.diff.Refresh()
	case screenAnalytics:
		m.analytics.liveUsage = config.LoadSettings().LiveUsage
		return m.analytics.Refresh()
	case screenSettings:
		return m.settings.Refresh()
	case screenSSH:
		return m.ssh.Refresh()
	case screenSkills:
		return m.skills.Refresh(m.memoryPath, m.logger)
	case screenAuditTrail:
		return m.auditTrail.Refresh()
	}
	return nil
}

// Run starts the TUI application.
func Run(memoryPath string) {
	app := NewApp(memoryPath)
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}
