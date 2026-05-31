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
		settings:   newSettingsModel(memoryPath),
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
		m.viewer.filename = msg.Filename
		m.viewer.content = msg.Content
		m.viewer.lines = strings.Split(msg.Content, "\n")
		m.viewer.scrollY = 0
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
			return m, cmd
		}

		if m.screen == screenSettings && m.settings.configuringCustom {
			var cmd tea.Cmd
			m.settings, cmd = m.settings.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			if m.logger != nil {
				m.logger.Close()
			}
			return m, tea.Quit
		case "1":
			m.screen = screenDashboard
			return m, m.refreshCurrentScreen()
		case "2":
			m.screen = screenActivity
			return m, m.refreshCurrentScreen()
		case "3":
			m.screen = screenBrowser
			return m, m.refreshCurrentScreen()
		case "4":
			m.screen = screenDiff
			return m, m.refreshCurrentScreen()
		case "5":
			m.screen = screenAnalytics
			return m, m.refreshCurrentScreen()
		case "6":
			m.screen = screenSettings
			return m, m.refreshCurrentScreen()
		case "7":
			m.screen = screenSSH
			return m, m.refreshCurrentScreen()
		case "8":
			m.screen = screenSkills
			return m, m.refreshCurrentScreen()
		case "9":
			m.screen = screenAuditTrail
			return m, m.refreshCurrentScreen()
		case "]", "tab":
			if msg.String() == "tab" && ((m.screen == screenDashboard && m.dashboard.selectedAgent != "") ||
				(m.screen == screenSSH && m.ssh.editingHost) ||
				(m.screen == screenSettings && (m.settings.configuringCustom || m.settings.showingDiff || m.settings.confirmingRun))) {
				break
			}
			if m.screen == screenViewer {
				m.screen = screenBrowser
			}
			m.screen = (m.screen + 1) % 9
			return m, m.refreshCurrentScreen()
		case "[", "shift+tab", "backtab":
			if (msg.String() == "shift+tab" || msg.String() == "backtab") && ((m.screen == screenDashboard && m.dashboard.selectedAgent != "") ||
				(m.screen == screenSSH && m.ssh.editingHost) ||
				(m.screen == screenSettings && (m.settings.configuringCustom || m.settings.showingDiff || m.settings.confirmingRun))) {
				break
			}
			if m.screen == screenViewer {
				m.screen = screenBrowser
			}
			if m.screen == 0 {
				m.screen = 8
			} else {
				m.screen = (m.screen - 1) % 9
			}
			return m, m.refreshCurrentScreen()
		case "esc":
			if m.screen == screenViewer {
				m.screen = screenBrowser
				return m, m.refreshCurrentScreen()
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

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			banner := renderBanner(m.width)
			tabRow := strings.Count(banner, "\n")
			if msg.Y >= tabRow-1 && msg.Y <= tabRow+1 {
				startX := 0
				for idx, name := range screenNames {
					tabWidth := 4 + len(name) + 2
					if msg.X >= startX && msg.X < startX+tabWidth {
						m.screen = screen(idx)
						return m, m.refreshCurrentScreen()
					}
					startX += tabWidth
				}
			}
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

	return m, cmd
}

func (m model) View() string {
	// Banner
	banner := renderBanner(m.width)

	// Tab bar
	tabs := m.renderTabs()

	// Content
	var content string
	switch m.screen {
	case screenDashboard:
		content = m.dashboard.View()
	case screenActivity:
		content = m.activity.View()
	case screenBrowser:
		content = m.browser.View()
	case screenDiff:
		content = m.diff.View()
	case screenAnalytics:
		content = m.analytics.View(m.width)
	case screenSettings:
		content = m.settings.View()
	case screenSSH:
		content = m.ssh.View()
	case screenSkills:
		content = m.skills.View(m.width, m.height)
	case screenAuditTrail:
		content = m.auditTrail.View()
	case screenViewer:
		content = m.viewer.View()
	}

	// Footer
	footer := m.renderFooter()

	return fmt.Sprintf("%s\n%s\n\n%s\n\n%s", banner, tabs, content, footer)
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
		footerText = "j/k: Scroll file • PgUp/PgDn: Scroll page • Esc: Back to files • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
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

	return StyleFooter.Render(footerText)
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
