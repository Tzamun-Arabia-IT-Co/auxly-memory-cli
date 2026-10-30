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
	"github.com/charmbracelet/x/ansi"
)

type screen int

const (
	screenDashboard screen = iota
	screenActivity
	screenBrowser
	screenDiff
	screenMemoryOrg
	screenAnalytics
	screenSettings
	screenSSH
	screenSkills
	screenAuditTrail
	// screenReview is appended at the END of the tab list (not spliced in after
	// Approvals) so the existing single-digit hotkeys 1-9/0 for the ten tabs
	// above stay untouched — all ten digits are already spoken for, and
	// reordering would renumber every tab after Approvals. Reachable via
	// Tab/Shift+Tab cycling, mouse click, and its own "-" key.
	// ponytail: append-only tab registration, smallest diff that doesn't
	// reshuffle existing shortcuts; revisit if Review ever needs a prime digit.
	screenReview
	// screenMemory is appended after Review for the same append-only reason —
	// its own "=" key, reachable via Tab/Shift+Tab cycling and mouse click.
	screenMemory
	screenViewer // must stay last
)

var screenNames = []string{
	"Dashboard",
	"Activity",
	"Files",
	"Approvals",
	"Memory Org",
	"Analytics",
	"Settings",
	"Remote",
	"Skills",
	"Audit Trail",
	"Review",
	"Memory",
}

type tabRect struct {
	idx       int
	row       int
	x0        int
	x1        int
	totalRows int
}

type model struct {
	memoryPath string
	screen     screen
	width      int
	height     int

	// Sub-models
	memoryOrg  organizeModel
	dashboard  dashboardModel
	activity   activityModel
	auditTrail auditTrailModel
	browser    browserModel
	diff       diffModel
	analytics  analyticsModel
	settings   settingsModel
	ssh        sshModel
	skills     skillsModel
	review     reviewModel
	memBrowser memBrowserModel
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

	// Layout cache populated by relayout()
	layoutValid       bool
	cachedScreen      screen
	cachedBanner      string
	cachedTabs        string
	cachedFooter      string
	cachedContent     string
	cachedTight       bool
	cachedUsesVP      bool
	cachedTooSmall    bool
	cachedChromeH     int
	cachedNeedsScroll bool
	cachedReviewCount int
	cachedOrgMode     orgMode
	cachedOrgApproved int
	cachedOrgRejected int
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
		memoryOrg:  newOrganizeModel(store, memoryPath, logger),
		dashboard:  newDashboardModel(logger, pendingMgr, memoryPath, usageMgr),
		activity:   newActivityModel(logger),
		auditTrail: newAuditTrailModel(logger),
		browser:    newBrowserModel(store),
		diff:       newDiffModel(pendingMgr, logger),
		analytics:  newAnalyticsModel(logger, usageMgr, store),
		settings:   newSettingsModel(memoryPath, logger),
		ssh:        newSSHModel(),
		skills:     newSkillsModel(),
		review:     newReviewModel(store, logger),
		memBrowser: newMemBrowserModel(store, logger, pendingMgr),
		viewer:     newViewerModel(store),
	}
}

func (m model) Init() tea.Cmd {
	return m.refreshCurrentScreen()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.MouseMsg); ok {
		m.relayout()
	}
	switch msg := msg.(type) {
	case OpenFileMsg:
		m.viewer = m.viewer.load(msg.Filename, msg.Content, msg.Editable)
		m.screen = screenViewer
		m.relayout()
		return m, nil
	case vaultActionMsg, trustSuggestAppliedMsg, opsRefreshMsg, opsActionMsg, opsSyncMsg, opsBackupSetupMsg, opsAutoBackupMsg, opsSpinTickMsg:
		// A vault op (init/encrypt/decrypt/rebuild), a trust-suggestion apply, or
		// an Ops action (hooks install/uninstall, sync, doctor) completes
		// asynchronously. Route its result to settingsModel regardless of the
		// current screen — otherwise leaving Settings mid-op drops the message,
		// busy never clears, and the panel comes back frozen.
		var cmd tea.Cmd
		m.settings, cmd = m.settings.Update(msg)
		// This is an early return like the capturesInput guards below, so the
		// content viewport (what's actually on screen for screenSettings) needs
		// the same refresh they give it — otherwise the model's busy/status
		// fields update correctly but the rendered frame doesn't, and a
		// mid-flight spinner tick or the final result never repaints: the Ops
		// panel looks frozen even though the underlying action completed fine.
		m.relayout()
		return m, cmd
	case tea.KeyMsg:
		if m.screen == screenDashboard && m.dashboard.selectedAgent != "" {
			// While the agent popup is open, its tab keys (1/2/3/4) must reach the
			// dashboard rather than the parent's screen switcher. "3" was missing,
			// so it fell through and jumped to the Files screen.
			switch msg.String() {
			case "1", "2", "3", "4":
				var cmd tea.Cmd
				m.dashboard, cmd = m.dashboard.Update(msg)
				m.relayout()
				return m, cmd
			}
		}

		if m.screen == screenSSH && m.ssh.editingHost {
			var cmd tea.Cmd
			m.ssh, cmd = m.ssh.Update(msg)
			// These sub-modes early-return, so refresh the viewport here too —
			// otherwise the page (in the content viewport) keeps showing the stale
			// frame and the wizard looks frozen even though its state advanced.
			// The wizard is rendered at the bottom of the SSH view, so also scroll
			// there — otherwise the viewport stays at the top showing the static
			// list header while the form (below the fold) changes invisibly.
			m.relayout()
			if m.vpReady {
				m.contentVP.GotoBottom()
			}
			return m, cmd
		}

		if m.screen == screenMemoryOrg && m.memoryOrg.capturesInput() {
			var cmd tea.Cmd
			m.memoryOrg, cmd = m.memoryOrg.Update(msg)
			m.relayout()
			return m, cmd
		}

		// The Memory tab intercepts plain "tab" locally (pane focus toggle, per
		// its own footer hint) instead of the app-wide screen-cycle, and while a
		// search/edit/new-fact input or a delete confirm owns the keyboard every
		// keystroke must reach it — never the global switch below.
		if m.screen == screenMemory && (m.memBrowser.capturesInput() || msg.String() == "tab") {
			var cmd tea.Cmd
			m.memBrowser, cmd = m.memBrowser.Update(msg)
			m.relayout()
			return m, cmd
		}

		if m.screen == screenReview && m.review.capturesInput() {
			var cmd tea.Cmd
			m.review, cmd = m.review.Update(msg)
			m.relayout()
			return m, cmd
		}

		// The Customizations confirm dialog / sync sub-panel own the keyboard (y/n/esc,
		// ↑/↓, space) so digits and tab don't switch screens out from under them. This
		// path early-returns, so refresh the viewport too — otherwise the page (rendered
		// inside the content viewport) keeps showing a STALE frame and panel navigation
		// (e.g. the sync box list) looks frozen even though the cursor advanced.
		if m.screen == screenSettings && m.settings.cust.capturesInput() {
			var cmd tea.Cmd
			m.settings, cmd = m.settings.Update(msg)
			m.relayout()
			return m, cmd
		}

		// The Vault sub-tab (passphrase entry, the keypair-vs-passphrase init
		// picker, or an in-flight keychain/disk op) and the trust-suggestions
		// panel own the keyboard the same way the Customizations dialog above
		// does — without this guard a digit typed into a passphrase switches
		// tabs mid-entry, and q/ctrl+c quits instead of reaching the buffer or
		// closing the panel. This path early-returns, so refresh the viewport
		// too, same reasoning as above.
		if m.screen == screenSettings && m.settings.capturesInput() {
			var cmd tea.Cmd
			m.settings, cmd = m.settings.Update(msg)
			m.relayout()
			return m, cmd
		}

		// While editing a memory file, every keystroke (incl. q, digits, esc,
		// ctrl+s) must reach the editor — never the global tab/quit switch below.
		if m.screen == screenViewer && m.viewer.editing {
			var cmd tea.Cmd
			m.viewer, cmd = m.viewer.Update(msg)
			m.relayout()
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
			return m, m.gotoScreen(screenMemoryOrg)
		case "6":
			return m, m.gotoScreen(screenAnalytics)
		case "7":
			return m, m.gotoScreen(screenSettings)
		case "8":
			return m, m.gotoScreen(screenSSH)
		case "9":
			return m, m.gotoScreen(screenSkills)
		case "0":
			return m, m.gotoScreen(screenAuditTrail)
		case "-":
			return m, m.gotoScreen(screenReview)
		case "=":
			return m, m.gotoScreen(screenMemory)
		case "]", "tab":
			if msg.String() == "tab" && ((m.screen == screenDashboard && m.dashboard.selectedAgent != "") ||
				(m.screen == screenSSH && m.ssh.editingHost)) {
				break
			}
			next := m.screen
			if next == screenViewer {
				next = screenBrowser
			}
			return m, m.gotoScreen((next + 1) % 12)
		case "[", "shift+tab", "backtab":
			if (msg.String() == "shift+tab" || msg.String() == "backtab") && ((m.screen == screenDashboard && m.dashboard.selectedAgent != "") ||
				(m.screen == screenSSH && m.ssh.editingHost)) {
				break
			}
			prev := m.screen
			if prev == screenViewer {
				prev = screenBrowser
			}
			if prev == 0 {
				return m, m.gotoScreen(11)
			}
			return m, m.gotoScreen((prev - 1) % 12)
		case "pgup", "pgdown", "ctrl+u", "ctrl+d", "home", "end":
			// Scroll the content viewport on long pages, unless a modal/editor owns
			// these keys.
			if m.usesViewport() && m.vpReady && !m.inModalSubmode() {
				var vcmd tea.Cmd
				m.contentVP, vcmd = m.contentVP.Update(msg)
				m.relayout()
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
		m.memoryOrg, _ = m.memoryOrg.Update(msg)
		m.dashboard, _ = m.dashboard.Update(msg)
		m.activity, _ = m.activity.Update(msg)
		m.auditTrail, _ = m.auditTrail.Update(msg)
		m.browser, _ = m.browser.Update(msg)
		m.diff, _ = m.diff.Update(msg)
		m.analytics, _ = m.analytics.Update(msg)
		m.settings, _ = m.settings.Update(msg)
		m.ssh, _ = m.ssh.Update(msg)
		m.skills, _ = m.skills.Update(msg)
		m.review, _ = m.review.Update(msg)
		m.memBrowser, _ = m.memBrowser.Update(msg)
		m.viewer, _ = m.viewer.Update(msg)
		m.relayout()
		return m, nil

	case tea.MouseMsg:
		m.relayout()
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			tabStartY := lipgloss.Height(m.cachedBanner)
			rects := m.tabLayout()
			if len(rects) > 0 {
				totalRows := rects[0].totalRows
				clickRow := msg.Y - tabStartY
				if clickRow == 1 && totalRows == 1 {
					clickRow = 0
				}
				if clickRow >= 0 && clickRow < totalRows {
					for _, r := range rects {
						if r.row == clickRow && msg.X >= r.x0 && msg.X < r.x1 {
							return m, m.gotoScreen(screen(r.idx))
						}
					}
				}
			}
		}
		// Mouse wheel scrolls the content viewport on long pages.
		if m.usesViewport() && m.vpReady &&
			(msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown) {
			var vcmd tea.Cmd
			m.contentVP, vcmd = m.contentVP.Update(msg)
			m.relayout()
			return m, vcmd
		}
	}

	// Delegate to sub-model
	var cmd tea.Cmd
	switch m.screen {
	case screenMemoryOrg:
		m.memoryOrg, cmd = m.memoryOrg.Update(msg)
	case screenDashboard:
		if m.usesViewport() {
			m.dashboard.vpYOffset = m.contentVP.YOffset
		} else {
			m.dashboard.vpYOffset = 0
		}
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
	case screenReview:
		m.review, cmd = m.review.Update(msg)
	case screenMemory:
		m.memBrowser, cmd = m.memBrowser.Update(msg)
	case screenViewer:
		m.viewer, cmd = m.viewer.Update(msg)
	}

	// Keep the content viewport sized and filled for the active screen after the
	// sub-model updated (selection moved, data refreshed).
	m.relayout()
	if m.screen == screenSSH && m.ssh.editingHost && m.vpReady {
		m.contentVP.GotoBottom()
	}

	return m, cmd
}

// baseFooter returns the footer text wrapped to width without any scroll affordance hint.
func (m model) baseFooter() string {
	footerText := m.rawFooter()
	w := m.width
	if w <= 0 {
		w = 80
	}
	return StyleFooter.Render(wrapFooter(footerText, w))
}

func (m model) screenNeedsScroll() bool {
	if m.layoutValid && m.cachedScreen == m.screen {
		return m.cachedNeedsScroll
	}
	if m.height <= 0 {
		return false
	}
	blank := 2
	if m.screen == screenDashboard && m.dashboard.bodyCompact() {
		blank = 0
	}
	chrome := lipgloss.Height(renderBanner(m.width)) +
		lipgloss.Height(m.renderTabs()) +
		lipgloss.Height(m.baseFooter()) + blank
	avail := m.height - chrome
	return lipgloss.Height(m.screenContent()) > avail
}

// usesViewport reports whether the active screen scrolls its content inside the
// shared viewport.
func (m model) usesViewport() bool {
	if m.layoutValid && m.cachedScreen == m.screen {
		return m.cachedUsesVP
	}
	switch m.screen {
	case screenDashboard, screenMemoryOrg, screenViewer:
		return m.screenNeedsScroll()
	}
	return true
}

// inModalSubmode reports whether the active screen has a focused modal/editor that
// owns its own keys (so the parent must not steal scroll keys for the viewport).
func (m model) inModalSubmode() bool {
	switch m.screen {
	case screenSettings:
		return m.settings.capturesInput()
	case screenSSH:
		return m.ssh.editingHost
	case screenActivity:
		return m.activity.viewingDetail
	case screenAuditTrail:
		return m.auditTrail.viewingDetail
	case screenDashboard:
		return m.dashboard.selectedAgent != ""
	case screenMemoryOrg:
		return m.memoryOrg.capturesInput()
	case screenViewer:
		return m.viewer.editing
	}
	return false
}

// screenContent renders only the active screen's body (no banner/tabs/footer).
func (m model) screenContent() string {
	switch m.screen {
	case screenMemoryOrg:
		return m.memoryOrg.View()
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
	case screenReview:
		return m.review.View()
	case screenMemory:
		return m.memBrowser.View(m.width)
	case screenViewer:
		return m.viewer.View()
	}
	return ""
}

// chromeHeight is the number of rows consumed by the fixed header (full logo +
// tabs), the blank separators, and the footer — i.e. everything that is NOT
// the scrollable content region.
func (m model) chromeHeight() int {
	if m.layoutValid && m.cachedScreen == m.screen {
		return m.cachedChromeH
	}
	blank := 2
	if m.screen == screenDashboard && m.dashboard.bodyCompact() {
		blank = 0
	}
	return lipgloss.Height(renderBanner(m.width)) +
		lipgloss.Height(m.renderTabs()) +
		lipgloss.Height(m.renderFooter()) + blank
}

// syncViewport (re)sizes the content viewport to the rows left under the fixed
// chrome and refreshes its content for the active screen. Scroll offset is
// preserved across data ticks; callers reset it on a screen switch.
func (m *model) syncViewport() {
	m.relayout()
}

// gotoScreen switches the active tab, resets the content scroll to the top, and
// refreshes the new screen's data.
func (m *model) gotoScreen(s screen) tea.Cmd {
	if m.screen == screenSSH && s != screenSSH && m.ssh.inviteToken != "" {
		// Don't hold a copy-able secret around once the user has left the tab.
		m.ssh.inviteToken = ""
	}
	if m.screen == screenSSH && s != screenSSH && m.ssh.joinToken != "" {
		m.ssh.joinToken = ""
	}
	if m.screen == screenSettings && s != screenSettings {
		// Don't hold a one-time backup key or a typed passphrase around once the
		// user has left Settings — same reasoning as the SSH tokens above.
		m.settings.vault.backupKey = ""
		m.settings.vault.passBuf1 = ""
		m.settings.vault.passBuf2 = ""
	}
	m.screen = s
	if m.vpReady {
		m.contentVP.GotoTop()
	}
	m.dashboard.vpYOffset = 0
	cmd := m.refreshCurrentScreen()
	m.relayout()
	return cmd
}

func (m *model) propagateChrome() {
	if m.width <= 0 {
		return
	}
	if !m.layoutValid || m.cachedScreen != m.screen {
		m.relayout()
		return
	}
	bannerH := lipgloss.Height(m.cachedBanner)
	tabsH := lipgloss.Height(m.cachedTabs)
	footerH := lipgloss.Height(m.cachedFooter)

	// Standard non-tight layout:
	// banner (bannerH) + empty line (1) + tabs (tabsH) + empty line (1)
	chromeTop := bannerH + tabsH + 1
	// Tight layout:
	// banner + tabs (no blank rows)
	chromeTopTight := bannerH + tabsH

	// Total chrome rows:
	// Full: banner + tabs + footer + 2 blank separators
	chromeFull := bannerH + tabsH + footerH + 2
	// Tight: banner + tabs + footer
	chromeTight := bannerH + tabsH + footerH

	m.dashboard.chromeTop = chromeTop
	m.dashboard.chromeTopTight = chromeTopTight
	m.dashboard.chromeFull = chromeFull
	m.dashboard.chromeTight = chromeTight
	if m.cachedUsesVP && m.screen == screenDashboard {
		m.dashboard.vpYOffset = m.contentVP.YOffset
	} else {
		m.dashboard.vpYOffset = 0
	}

	m.memoryOrg.contentTop = chromeTop
	m.memoryOrg.appChrome = chromeFull
	m.memoryOrg.resizeReviewPanes()

	m.skills.chromeTop = chromeTop
	m.activity.chromeTop = chromeTop
	m.auditTrail.chromeTop = chromeTop
	m.settings.chromeTop = chromeTop
	m.ssh.chromeTop = chromeTop
}

func (m model) tooSmall() bool {
	if m.layoutValid && m.cachedScreen == m.screen {
		return m.cachedTooSmall
	}
	if m.width <= 0 || m.height <= 0 {
		return false
	}
	if m.width < 80 {
		return true
	}
	bannerH := lipgloss.Height(renderBanner(m.width))
	tabsH := lipgloss.Height(m.renderTabs())
	footerH := lipgloss.Height(m.baseFooter())
	blank := 2
	if m.screen == screenDashboard && m.dashboard.bodyCompact() {
		blank = 0
	}
	chrome := bannerH + tabsH + footerH + blank
	return m.height-chrome < 3
}

// relayout performs a single layout pass for the active screen: renders banner,
// tabs, base footer, and screen content once; decides whether viewport scrolling
// is required; sizes and fills the viewport; computes the scroll affordance;
// updates sub-screen chrome metrics; and caches results on m.
func (m *model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}

	m.layoutValid = false

	banner := renderBanner(m.width)
	bannerH := lipgloss.Height(banner)

	tabs := m.renderTabs()
	tabsH := lipgloss.Height(tabs)

	baseFooter := m.baseFooter()
	baseFooterH := lipgloss.Height(baseFooter)

	// Pre-propagate chrome metrics so bodyCompact() and sub-models have
	// accurate measurements before screen content is rendered.
	chromeTop := bannerH + tabsH + 1
	chromeTopTight := bannerH + tabsH
	chromeFull := bannerH + tabsH + baseFooterH + 2
	chromeTight := bannerH + tabsH + baseFooterH

	m.dashboard.chromeTop = chromeTop
	m.dashboard.chromeTopTight = chromeTopTight
	m.dashboard.chromeFull = chromeFull
	m.dashboard.chromeTight = chromeTight

	m.memoryOrg.contentTop = chromeTop
	m.memoryOrg.appChrome = chromeFull
	m.memoryOrg.resizeReviewPanes()

	m.skills.chromeTop = chromeTop
	m.activity.chromeTop = chromeTop
	m.auditTrail.chromeTop = chromeTop
	m.settings.chromeTop = chromeTop
	m.ssh.chromeTop = chromeTop

	tight := m.screen == screenDashboard && m.dashboard.bodyCompact()
	blank := 2
	if tight {
		blank = 0
	}

	content := m.screenContent()
	contentH := lipgloss.Height(content)

	avail := m.height - (bannerH + tabsH + baseFooterH + blank)
	needsScroll := contentH > avail

	usesVP := true
	switch m.screen {
	case screenDashboard, screenMemoryOrg, screenViewer:
		usesVP = needsScroll
	}

	vpHeight := avail
	if vpHeight < 1 {
		vpHeight = 1
	}
	if !m.vpReady {
		m.contentVP = viewport.New(m.width, vpHeight)
		m.vpReady = true
	} else {
		m.contentVP.Width = m.width
		m.contentVP.Height = vpHeight
	}

	footer := baseFooter
	if usesVP {
		m.contentVP.SetContent(content)
		if !m.inModalSubmode() && m.contentVP.TotalLineCount() > m.contentVP.Height {
			pct := int(m.contentVP.ScrollPercent() * 100)
			hintText := fmt.Sprintf("  ⇕ %d%% · PgUp/PgDn or wheel to scroll", pct)
			if m.width > 0 && lipgloss.Width(hintText) > m.width {
				hintText = ansi.Truncate(hintText, m.width, "")
			}
			hint := lipgloss.NewStyle().Foreground(ColorPrimary).Render(hintText)
			footer = lipgloss.JoinVertical(lipgloss.Left, hint, footer)

			newH := m.height - (bannerH + tabsH + lipgloss.Height(footer) + blank)
			if newH < 1 {
				newH = 1
			}
			if newH != m.contentVP.Height {
				m.contentVP.Height = newH
			}
		}
	}

	footerH := lipgloss.Height(footer)
	if footerH != baseFooterH {
		chromeFull = bannerH + tabsH + footerH + 2
		chromeTight = bannerH + tabsH + footerH
		m.dashboard.chromeFull = chromeFull
		m.dashboard.chromeTight = chromeTight
		m.memoryOrg.appChrome = chromeFull
	}

	if usesVP && m.screen == screenDashboard {
		m.dashboard.vpYOffset = m.contentVP.YOffset
	} else {
		m.dashboard.vpYOffset = 0
	}

	tooSmall := false
	if m.width < 80 {
		tooSmall = true
	} else {
		chrome := bannerH + tabsH + baseFooterH + blank
		tooSmall = m.height-chrome < 3
	}

	m.cachedBanner = banner
	m.cachedTabs = tabs
	m.cachedFooter = footer
	m.cachedContent = content
	m.cachedTight = tight
	m.cachedUsesVP = usesVP
	m.cachedTooSmall = tooSmall
	m.cachedChromeH = bannerH + tabsH + footerH + blank
	m.cachedNeedsScroll = needsScroll
	m.cachedScreen = m.screen
	m.cachedReviewCount = len(m.review.facts)
	m.cachedOrgMode = m.memoryOrg.mode
	appr, rej, _ := m.memoryOrg.decisionCounts()
	m.cachedOrgApproved = appr
	m.cachedOrgRejected = rej
	m.layoutValid = true
}

func (m model) composeView() string {
	appr, rej, _ := m.memoryOrg.decisionCounts()
	if !m.layoutValid || m.cachedScreen != m.screen || m.cachedReviewCount != len(m.review.facts) ||
		(m.screen == screenMemoryOrg && (m.cachedOrgMode != m.memoryOrg.mode || m.cachedOrgApproved != appr || m.cachedOrgRejected != rej)) {
		m.relayout()
	}

	if m.cachedTooSmall {
		msg := fmt.Sprintf("Terminal too small — enlarge to at least 80 columns (now %dx%d)", m.width, m.height)
		if m.width > 0 {
			msg = lipgloss.PlaceHorizontal(m.width, lipgloss.Center, msg)
		}
		return msg
	}

	banner := m.cachedBanner
	tabs := m.cachedTabs
	footer := m.cachedFooter

	var out string
	if m.cachedUsesVP && m.vpReady {
		// Content pages: fixed header + scrolling viewport + fixed footer. The
		// viewport makes the body exactly its height, so the chrome can never be
		// pushed off the top, and content scrolls instead of being truncated.
		if m.cachedTight {
			out = fmt.Sprintf("%s\n%s\n%s\n%s", banner, tabs, m.contentVP.View(), footer)
		} else {
			out = fmt.Sprintf("%s\n%s\n\n%s\n\n%s", banner, tabs, m.contentVP.View(), footer)
		}
	} else {
		// Dashboard / Memory Org / viewer manage their own height when they fit.
		content := m.cachedContent
		tight := m.cachedTight
		if m.height > 0 {
			used := m.cachedChromeH
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

func (m model) View() string {
	out := m.composeView()

	// Line-width safety net: truncate any line still wider than m.width
	if m.width > 0 {
		lines := strings.Split(out, "\n")
		for i, line := range lines {
			if lipgloss.Width(line) > m.width {
				lines[i] = ansi.Truncate(line, m.width, "")
			}
		}
		out = strings.Join(lines, "\n")
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

// labelFor returns the display label for tab slot i: the static screenNames
// entry, except screenReview which gets a "(N)" badge while the queue is
// non-empty. Shared by renderTabs (what's drawn) and the mouse click math
// (what hit-zone corresponds to each tab) so the two can never desync.
func (m model) labelFor(i int) string {
	name := screenNames[i]
	if screen(i) == screenReview && len(m.review.facts) > 0 {
		// Approvals' own tab label is a static string (its count is shown
		// inline in the body instead), so badge the count here per spec.
		name = fmt.Sprintf("Review (%d)", len(m.review.facts))
	}
	return name
}

func (m model) tabKey(i int) string {
	if i == 9 {
		return "0"
	}
	if screen(i) == screenMemory {
		return "="
	}
	if screen(i) == screenReview {
		return "-"
	}
	return fmt.Sprintf("%d", i+1)
}

func (m model) fullTabsFit() bool {
	if m.width <= 0 {
		return true
	}
	totalW := 0
	for i := range screenNames {
		totalW += 6 + lipgloss.Width(m.labelFor(i))
	}
	return totalW <= m.width
}

func (m model) renderTabChip(i int, full bool) string {
	key := m.tabKey(i)
	name := m.labelFor(i)
	isActive := screen(i) == m.screen

	if full {
		if isActive {
			return lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorPrimary).
				Padding(0, 1).
				Render(fmt.Sprintf("[%s] %s", key, name))
		}
		return lipgloss.NewStyle().
			Foreground(ColorDim).
			Padding(0, 1).
			Render(fmt.Sprintf(" %s  %s", key, name))
	}

	// Compact style
	if isActive {
		return lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Render(fmt.Sprintf("[%s] %s", key, name))
	}
	return lipgloss.NewStyle().
		Foreground(ColorDim).
		Render(fmt.Sprintf("%s %s", key, name))
}

func (m model) tabLayout() []tabRect {
	rects := make([]tabRect, len(screenNames))
	w := m.width
	if w <= 0 {
		w = 80
	}

	// Strategy 1: full labels on one row if they fit m.width
	if m.fullTabsFit() {
		curX := 0
		for i := range screenNames {
			tabW := 6 + lipgloss.Width(m.labelFor(i))
			rects[i] = tabRect{
				idx:       i,
				row:       0,
				x0:        curX,
				x1:        curX + tabW,
				totalRows: 1,
			}
			curX += tabW
		}
		return rects
	}

	// Strategy 2: compact style on one row if it fits
	compactWidths := make([]int, len(screenNames))
	compactTotal := 0
	for i := range screenNames {
		txt := m.renderTabChip(i, false)
		compactWidths[i] = lipgloss.Width(txt)
		compactTotal += compactWidths[i]
		if i > 0 {
			compactTotal += 2 // two spaces between compact chips
		}
	}

	canFitOneRow := compactTotal <= w
	savedOne := false
	if !canFitOneRow && compactTotal-1 <= w {
		// If 2 spaces overshoots by exactly 1 character (e.g. 141 cols at width 140),
		// tighten the single gap before the last punctuation tab ("= Memory") to 1 space
		// so all tabs fit on 1 row instead of wasting an entire row on wrapping.
		canFitOneRow = true
		savedOne = true
	}

	if canFitOneRow {
		curX := 0
		for i := range screenNames {
			rects[i] = tabRect{
				idx:       i,
				row:       0,
				x0:        curX,
				x1:        curX + compactWidths[i],
				totalRows: 1,
			}
			gap := 2
			if savedOne && i == len(screenNames)-2 {
				gap = 1
			}
			curX = rects[i].x1 + gap
		}
		return rects
	}

	// Strategy 3: wrap tabs onto minimum rows, balanced across rows
	greedyRows := greedyWrapTabs(compactWidths, w)
	minRows := len(greedyRows)
	if minRows < 2 {
		minRows = 2
	}

	R := minRows
	numTabs := len(screenNames)
	base := numTabs / R
	rem := numTabs % R

	balancedRows := make([][]int, R)
	tabIdx := 0
	balancedFits := true
	for r := 0; r < R; r++ {
		count := base
		if r < rem {
			count++
		}
		rowW := 0
		for c := 0; c < count; c++ {
			tw := compactWidths[tabIdx]
			if c > 0 {
				rowW += 2
			}
			rowW += tw
			balancedRows[r] = append(balancedRows[r], tabIdx)
			tabIdx++
		}
		if rowW > w {
			balancedFits = false
			break
		}
	}

	chosenRows := balancedRows
	if !balancedFits {
		chosenRows = greedyRows
	}

	totalRows := len(chosenRows)
	for r, rowTabs := range chosenRows {
		curX := 0
		for _, idx := range rowTabs {
			tw := compactWidths[idx]
			rects[idx] = tabRect{
				idx:       idx,
				row:       r,
				x0:        curX,
				x1:        curX + tw,
				totalRows: totalRows,
			}
			curX = rects[idx].x1 + 2
		}
	}
	return rects
}

func greedyWrapTabs(compactWidths []int, w int) [][]int {
	var rows [][]int
	var curRow []int
	curW := 0
	for i, tw := range compactWidths {
		if len(curRow) == 0 {
			curRow = append(curRow, i)
			curW = tw
		} else if curW+2+tw <= w {
			curRow = append(curRow, i)
			curW += 2 + tw
		} else {
			rows = append(rows, curRow)
			curRow = []int{i}
			curW = tw
		}
	}
	if len(curRow) > 0 {
		rows = append(rows, curRow)
	}
	return rows
}

func (m model) renderTabs() string {
	if m.layoutValid && m.cachedScreen == m.screen && m.cachedTabs != "" && m.cachedReviewCount == len(m.review.facts) {
		return m.cachedTabs
	}
	rects := m.tabLayout()
	if len(rects) == 0 {
		return ""
	}
	totalRows := rects[0].totalRows
	lines := make([]string, totalRows)
	isFull := totalRows == 1 && m.fullTabsFit()

	for _, r := range rects {
		txt := m.renderTabChip(r.idx, isFull)
		if lines[r.row] == "" {
			if r.x0 > 0 {
				lines[r.row] = strings.Repeat(" ", r.x0) + txt
			} else {
				lines[r.row] = txt
			}
		} else {
			gap := r.x0 - lipgloss.Width(lines[r.row])
			if gap > 0 {
				lines[r.row] += strings.Repeat(" ", gap) + txt
			} else {
				lines[r.row] += txt
			}
		}
	}

	borderW := m.width
	if borderW <= 0 {
		borderW = 80
	}
	borderLine := lipgloss.NewStyle().Foreground(ColorDim).Render(strings.Repeat("─", borderW))
	return strings.Join(lines, "\n") + "\n" + borderLine
}

func (m model) rawFooter() string {
	var footerText string
	switch m.screen {
	case screenMemoryOrg:
		footerText = "up/down: Choose • Enter: Run/Submit • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
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
		footerText = "j/k: Navigate files • Enter: View file • e: Export all • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	case screenViewer:
		if m.viewer.editing {
			footerText = "Ctrl+S: Save • Esc: Cancel edit (discard changes)"
		} else if m.viewer.editable {
			footerText = "j/k: Scroll • PgUp/PgDn: Page • g/G: Top/Bottom • e: Edit • d: Download • Esc: Back • [ / ]: Tabs • q: Quit"
		} else {
			footerText = "j/k: Scroll • PgUp/PgDn: Page • g/G: Top/Bottom • d: Download • Esc: Back (read-only) • [ / ]: Tabs • q: Quit"
		}
	case screenDiff:
		switch {
		case m.diff.batchKind != "":
			footerText = "y: Confirm • n/Esc: Cancel"
		case m.diff.viewing != "":
			footerText = "a: Approve • r: Reject • Esc: Back to queue • q: Quit"
		default:
			footerText = "j/k: Navigate queue • Enter: View diff • a: Approve • r: Reject • A: Approve by agent • F: Approve by file • Tab/[ ]: tabs • q: Quit"
		}
	case screenAnalytics:
		footerText = "Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	case screenSettings:
		footerText = "↑/↓ option • ←/→ section • Enter toggle • t trust suggestions • Tab/[ ]: tabs • q quit"
	case screenSSH:
		footerText = "j/k: Select • c: Connect new • t: Test • p: Print config • d: Remove • i/I: Invite • J: Join a host • Tab/[ / ]: Switch tabs • q: Quit"
	case screenSkills:
		footerText = "j/k: Navigate commands • d: Export Claude skills ZIP • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	case screenAuditTrail:
		if m.auditTrail.viewingDetail {
			footerText = "j/k: Scroll details • Esc: Close details • q: Quit"
		} else {
			footerText = "j/k: Navigate logs • Enter: View details • Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
		}
	case screenReview:
		switch {
		case m.review.capturesInput():
			footerText = "y: Confirm • any other key: Cancel"
		default:
			footerText = "j/k/↑/↓: Navigate • K keep (re-stamp) · a archive · S keep all · A archive all · r rescan · e edit hint • Tab/[ ]: tabs • q: Quit"
		}
	case screenMemory:
		switch {
		case m.memBrowser.playground:
			footerText = "recall: " + m.memBrowser.playgroundInput.Value() + "▌ · enter run · tab lens: " +
				memLensLabel(m.memBrowser.playgroundClients, m.memBrowser.playgroundLens) + " · esc exit"
		case m.memBrowser.searching:
			footerText = "/ " + m.memBrowser.searchInput.Value() + "▌   ↑/↓ select · Enter jump · Esc cancel"
		case m.memBrowser.editing:
			footerText = "edit: " + m.memBrowser.editInput.Value() + "▌   Enter save (queues for approval) · Esc cancel"
		case m.memBrowser.creating:
			footerText = "new fact: " + m.memBrowser.createInput.Value() + "▌   Enter save (queues for approval) · Esc cancel"
		case m.memBrowser.confirmDelete:
			footerText = "delete this fact? y/n"
		default:
			footerText = "e edit · d delete · n new · / search · ? recall playground · r rescan · tab/h/l: switch pane · [ / ]: Switch tabs • q: Quit"
		}
	default:
		footerText = "Tab/Shift+Tab or [ / ]: Switch tabs • q: Quit"
	}
	return footerText
}

func (m model) renderFooter() string {
	if m.layoutValid && m.cachedScreen == m.screen && m.cachedFooter != "" {
		return m.cachedFooter
	}
	footerText := m.rawFooter()

	w := m.width
	if w <= 0 {
		w = 80
	}
	footer := StyleFooter.Render(wrapFooter(footerText, w))

	// On a content page whose body overflows, surface a scroll affordance so the
	// extra rows are discoverable (progressive disclosure) rather than silently
	// off-screen. Kept on its own line so it never widens the footer past the term.
	if m.usesViewport() && m.vpReady && !m.inModalSubmode() &&
		m.contentVP.TotalLineCount() > m.contentVP.Height {
		pct := int(m.contentVP.ScrollPercent() * 100)
		hintText := fmt.Sprintf("  ⇕ %d%% · PgUp/PgDn or wheel to scroll", pct)
		if w > 0 && lipgloss.Width(hintText) > w {
			hintText = ansi.Truncate(hintText, w, "")
		}
		hint := lipgloss.NewStyle().Foreground(ColorPrimary).Render(hintText)
		footer = lipgloss.JoinVertical(lipgloss.Left, hint, footer)
	}

	return footer
}

type footerToken struct {
	text string
	sep  string
}

func splitFooterHints(text string) []footerToken {
	var tokens []footerToken
	remaining := text
	for len(remaining) > 0 {
		idxDot := strings.Index(remaining, " • ")
		idxMid := strings.Index(remaining, " · ")
		idx := -1
		sep := ""
		if idxDot != -1 && (idxMid == -1 || idxDot < idxMid) {
			idx = idxDot
			sep = " • "
		} else if idxMid != -1 {
			idx = idxMid
			sep = " · "
		}
		if idx == -1 {
			tokens = append(tokens, footerToken{text: remaining, sep: ""})
			break
		}
		tokens = append(tokens, footerToken{text: remaining[:idx], sep: sep})
		remaining = remaining[idx+len(sep):]
	}
	return tokens
}

func wrapAtSpaces(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}
	var lines []string
	cur := ""
	for _, w := range words {
		if cur == "" {
			cur = w
		} else if lipgloss.Width(cur+" "+w) <= width {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func wrapFooter(text string, width int) string {
	if width <= 0 {
		width = 80
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	tokens := splitFooterHints(text)
	if len(tokens) == 0 {
		return text
	}

	var lines []string
	curLine := ""

	for i, tok := range tokens {
		hint := tok.text
		if lipgloss.Width(hint) > width {
			if curLine != "" {
				lines = append(lines, curLine)
				curLine = ""
			}
			hintLines := wrapAtSpaces(hint, width)
			for j, hl := range hintLines {
				if j < len(hintLines)-1 {
					lines = append(lines, hl)
				} else {
					curLine = hl
				}
			}
			continue
		}

		if curLine == "" {
			curLine = hint
		} else {
			prevSep := tokens[i-1].sep
			if prevSep == "" {
				prevSep = " "
			}
			candidate := curLine + prevSep + hint
			if lipgloss.Width(candidate) <= width {
				curLine = candidate
			} else {
				lines = append(lines, curLine)
				curLine = hint
			}
		}
	}
	if curLine != "" {
		lines = append(lines, curLine)
	}
	return strings.Join(lines, "\n")
}

func (m *model) refreshCurrentScreen() tea.Cmd {
	switch m.screen {
	case screenMemoryOrg:
		return m.memoryOrg.Refresh()
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
		// Kick off the periodic data tick alongside the one-shot refresh so inbound
		// remote connections appear automatically while the Remote screen is open.
		return tea.Batch(m.ssh.Refresh(), sshDataTickCmd())
	case screenSkills:
		return m.skills.Refresh(m.memoryPath, m.logger)
	case screenAuditTrail:
		return m.auditTrail.Refresh()
	case screenReview:
		// StaleFacts scans the whole vault + one audit-DB query per file — only
		// ever fire it on tab-enter (here), never from a background tick.
		m.review.scanning = true
		return m.review.Refresh()
	case screenMemory:
		// Mirrors Review: a vault-wide scan (Store.List + one View per file, plus
		// RecallStatsByFile) — only ever fire it on tab-enter, never a tick.
		m.memBrowser.scanning = true
		return m.memBrowser.Refresh()
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
