package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	// Aliased: this file already has a local `trust` variable/field.
	trustcfg "github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type settingsModel struct {
	memoryPath  string
	cursor      int
	agents      []detect.Agent
	trust       trustcfg.Config
	trustLevels []string
	width       int
	height      int

	// Live Usage opt-in (calls each agent's provider with its stored login —
	// off keeps Auxly fully local). Persisted via config.SaveSettings.
	liveUsage bool

	// Auto-Update opt-in: self-update to the latest release in place after an
	// interactive session (never mid-run). Persisted via config.SaveSettings.
	autoUpdate bool

	// Sub-tab state: 0 = General (trust), 1 = Agents (dashboard show/hide),
	// 2 = Customizations (Claude Code statusline).
	subTab int
	cust   customizationsModel

	// Agents sub-tab: the toggleable brand list (mirrors the dashboard's
	// candidate set — detected + active + currently-hidden) and its cursor.
	logger       *audit.Logger
	agentBrands  []agentCard
	agentCursor  int
	hiddenAgents map[string]bool

	// Trust auto-tuning hint (Sprint 16): count of pending `auxly trust suggest`
	// recommendations, computed once per screen-enter (see Refresh) — never on
	// a tick, since it costs an audit.db query.
	tuningSuggestions int
}

type settingsRefreshMsg struct {
	agents            []detect.Agent
	trust             trustcfg.Config
	agentBrands       []agentCard
	tuningSuggestions int
}

func newSettingsModel(memPath string, logger *audit.Logger) settingsModel {
	return settingsModel{
		memoryPath:   memPath,
		trustLevels:  []string{"auto", "require_approval", "read_only"},
		liveUsage:    config.LoadSettings().LiveUsage,
		autoUpdate:   config.LoadSettings().AutoUpdate,
		logger:       logger,
		hiddenAgents: loadHiddenAgentSet(),
	}
}

// loadHiddenAgentSet reads the persisted hide list into a set of canonical ids.
func loadHiddenAgentSet() map[string]bool {
	set := map[string]bool{}
	for _, h := range config.LoadSettings().HiddenAgents {
		if c := canonicalProvider(h); c != "" {
			set[c] = true
		}
	}
	return set
}

func (m settingsModel) getUniqueAgents() []detect.Agent {
	var unique []detect.Agent
	seen := make(map[string]bool)
	for _, a := range m.agents {
		if seen[a.Provider] {
			continue
		}
		seen[a.Provider] = true
		unique = append(unique, a)
	}
	return unique
}

func (m settingsModel) Refresh() tea.Cmd {
	memPath := m.memoryPath
	logger := m.logger
	return func() tea.Msg {
		agents := detect.InstalledAgents()

		// trust.Load is the real API (also used by `auxly trust ...` and
		// countTuningSuggestions below) — no more shadow struct that silently
		// drops fields (like `tuning: off`) the TUI doesn't know about.
		trust, err := trustcfg.Load(memPath)
		if err != nil || trust == nil {
			trust = &trustcfg.Config{Default: trustcfg.LevelRequireApproval, Providers: map[string]trustcfg.ProviderConfig{}}
		}

		return settingsRefreshMsg{
			agents:            agents,
			trust:             *trust,
			agentBrands:       buildAgentSettingsBrands(agents, logger),
			tuningSuggestions: countTuningSuggestions(memPath, logger),
		}
	}
}

// countTuningSuggestions is the Settings screen's read for the trust
// auto-tuning hint — reuses the same pure trust.SuggestChanges the CLI's
// `auxly trust suggest` uses, so the hint and the command never disagree.
// nil-safe: a nil logger (not yet connected) just means no hint.
func countTuningSuggestions(memPath string, logger *audit.Logger) int {
	if logger == nil {
		return 0
	}
	cfg, err := trustcfg.Load(memPath)
	if err != nil {
		return 0
	}
	stats, err := logger.ApprovalStats(90)
	if err != nil {
		return 0
	}
	return len(trustcfg.SuggestChanges(cfg, stats))
}

// buildAgentSettingsBrands returns every brand the dashboard could show — the
// union of detected agents, providers with audit activity, and the currently
// hidden ones (so a hidden agent stays in the list to toggle back on). Reuses
// buildAgentCards (without the hidden filter) for canonical order + metadata.
func buildAgentSettingsBrands(agents []detect.Agent, logger *audit.Logger) []agentCard {
	var provs []string
	for _, a := range agents {
		provs = append(provs, a.Provider)
	}
	if logger != nil {
		if s, err := logger.Stats(); err == nil && s != nil {
			provs = append(provs, providersWithActivity(s)...)
		}
	}
	provs = append(provs, config.LoadSettings().HiddenAgents...)
	return buildAgentCards(provs)
}

func (m settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case settingsRefreshMsg:
		m.agents = msg.agents
		m.trust = msg.trust
		m.agentBrands = msg.agentBrands
		m.tuningSuggestions = msg.tuningSuggestions
		if m.agentCursor >= len(m.agentBrands) {
			m.agentCursor = 0
		}
	case customizationsPreviewTickMsg:
		// A scheduled re-render so the statusline preview reflects the background
		// usage refresh that landed since it was shown (⧗ as of … → ↻ live).
		return m, nil
	case statuslineAppliedMsg:
		// Result of an in-process statusline apply (Customizations sub-tab). On a
		// successful apply the focus advances to the next agent — refresh its preview,
		// and (if the user opted into auto-sync) push the change to the selected boxes.
		m.cust = m.cust.handleApplied(msg)
		cmds := []tea.Cmd{m.cust.previewRefreshCmd()}
		if msg.ok {
			if sync := autoSyncStatuslineCmd(); sync != nil {
				cmds = append(cmds, sync)
			}
		}
		return m, tea.Batch(cmds...)
	case remoteSyncDoneMsg:
		// Result of a "sync now" (or auto-sync) push to the boxes.
		m.cust = m.cust.handleSyncDone(msg)
		return m, nil
	case syncSpinTickMsg:
		// Animate the sync spinner; stop re-arming once the push has returned.
		if m.cust.syncing {
			m.cust.syncSpin++
			return m, syncSpinTick()
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
			viewLines := strings.Split(m.View(), "\n")
			clickedY := msg.Y - contentOffsetY

			for idx, line := range viewLines {
				clean := stripANSI(line)
				if strings.Contains(clean, subTabGeneralLabel) && strings.Contains(clean, subTabAgentsLabel) {
					if clickedY == idx {
						if msg.X >= strings.Index(clean, subTabAgentsLabel) {
							m.subTab = 1
						} else {
							m.subTab = 0
						}
						return m, nil
					}
					break
				}
			}

			if m.subTab == 1 {
				for idx, line := range viewLines {
					clean := stripANSI(line)
					if clickedY != idx {
						continue
					}
					for ai, b := range m.agentBrands {
						if b.name != "" && strings.Contains(clean, b.name) &&
							(strings.Contains(clean, "Shown") || strings.Contains(clean, "Hidden")) {
							m.agentCursor = ai
							m.toggleAgentHidden(b.id)
							return m, nil
						}
					}
				}
				return m, nil
			}

			defaultTrustLineY := -1
			liveUsageLineY := -1
			autoUpdateLineY := -1
			providerLineYs := make(map[string]int)
			uniqueAgents := m.getUniqueAgents()
			for idx, line := range viewLines {
				clean := stripANSI(line)
				hasBadge := strings.Contains(clean, "[ON]") || strings.Contains(clean, "[OFF]")
				if strings.Contains(clean, "Default Trust") {
					defaultTrustLineY = idx
				}
				// The Live Usage / Auto-Update toggles share one line in compact mode
				// (matched as Live Usage) but split onto two otherwise; map each row to
				// its toggle. The else-if keeps the compact combined line off the auto row.
				if strings.Contains(clean, "Live Usage") && hasBadge {
					liveUsageLineY = idx
				} else if strings.Contains(clean, "Auto-Update") && hasBadge {
					autoUpdateLineY = idx
				}
				for _, a := range uniqueAgents {
					if strings.Contains(clean, a.Name) && !strings.Contains(clean, "Overrides") && !strings.Contains(clean, "Default Trust") {
						providerLineYs[a.Provider] = idx
					}
				}
			}

			if clickedY == defaultTrustLineY && defaultTrustLineY != -1 {
				m.cursor = 0
				m.trust.Default = m.cycleTrust(m.trust.Default)
				return m, m.saveTrust()
			}
			for provider, lineY := range providerLineYs {
				if clickedY == lineY {
					for i, a := range uniqueAgents {
						if a.Provider == provider {
							m.cursor = i + 1
							break
						}
					}
					if m.trust.Providers == nil {
						m.trust.Providers = make(map[string]trustcfg.ProviderConfig)
					}
					next := m.cycleTrust(m.trust.Providers[provider].TrustLevel)
					m.trust.Providers[provider] = trustcfg.ProviderConfig{TrustLevel: next}
					return m, m.saveTrust()
				}
			}
			if clickedY == liveUsageLineY && liveUsageLineY != -1 {
				m.cursor = len(uniqueAgents) + 1
				return m, m.toggleLiveUsage()
			}
			if clickedY == autoUpdateLineY && autoUpdateLineY != -1 {
				m.cursor = len(uniqueAgents) + 2
				return m, m.toggleAutoUpdate()
			}
		}
	case tea.KeyMsg:
		uniqueAgents := m.getUniqueAgents()

		if m.subTab == 2 {
			// The confirm dialog / in-progress apply own the keyboard; otherwise
			// ←/→ switch sections.
			if !m.cust.capturesInput() {
				switch msg.String() {
				case "h", "left":
					m.subTab = 1
					return m, nil
				case "l", "right":
					m.subTab = 0
					return m, nil
				}
			}
			var cmd tea.Cmd
			m.cust, cmd = m.cust.handleKey(msg)
			return m, cmd
		}

		if m.subTab == 1 {
			switch msg.String() {
			case "j", "down":
				if m.agentCursor < len(m.agentBrands)-1 {
					m.agentCursor++
				}
			case "k", "up":
				if m.agentCursor > 0 {
					m.agentCursor--
				}
			case "h", "left":
				m.subTab = 0
			case "l", "right":
				m.subTab = 2
				m.cust.refresh()
				return m, m.cust.previewRefreshCmd()
			case "enter", " ":
				if m.agentCursor < len(m.agentBrands) {
					m.toggleAgentHidden(m.agentBrands[m.agentCursor].id)
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "j", "down":
			max := len(uniqueAgents) + 2 // +1 Live Usage, +2 Auto-Update
			if m.cursor < max {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "h", "left":
			m.subTab = 2
			m.cust.refresh()
			return m, m.cust.previewRefreshCmd()
		case "l", "right":
			m.subTab = 1
		case "enter", " ":
			if m.cursor == 0 {
				m.trust.Default = m.cycleTrust(m.trust.Default)
				return m, m.saveTrust()
			} else if m.cursor-1 < len(uniqueAgents) {
				provider := uniqueAgents[m.cursor-1].Provider
				if m.trust.Providers == nil {
					m.trust.Providers = make(map[string]trustcfg.ProviderConfig)
				}
				next := m.cycleTrust(m.trust.Providers[provider].TrustLevel)
				m.trust.Providers[provider] = trustcfg.ProviderConfig{TrustLevel: next}
				return m, m.saveTrust()
			} else if m.cursor == len(uniqueAgents)+1 {
				return m, m.toggleLiveUsage()
			} else if m.cursor == len(uniqueAgents)+2 {
				return m, m.toggleAutoUpdate()
			}
		}
	}
	return m, nil
}

// toggleLiveUsage flips the Live Usage opt-in, persisting it to settings.json.
func (m *settingsModel) toggleLiveUsage() tea.Cmd {
	m.liveUsage = !m.liveUsage
	next := config.LoadSettings()
	next.LiveUsage = m.liveUsage
	_ = config.SaveSettings(next)
	return nil
}

// toggleAutoUpdate flips the Auto-Update opt-in, persisting it to settings.json.
func (m *settingsModel) toggleAutoUpdate() tea.Cmd {
	m.autoUpdate = !m.autoUpdate
	next := config.LoadSettings()
	next.AutoUpdate = m.autoUpdate
	_ = config.SaveSettings(next)
	return nil
}

// toggleAgentHidden flips a brand's dashboard visibility and persists the hide
// list to settings.json. The dashboard re-reads it on its next refresh tick.
func (m *settingsModel) toggleAgentHidden(id string) {
	if id == "" {
		return
	}
	if m.hiddenAgents == nil {
		m.hiddenAgents = map[string]bool{}
	}
	if m.hiddenAgents[id] {
		delete(m.hiddenAgents, id)
	} else {
		m.hiddenAgents[id] = true
	}
	list := make([]string, 0, len(m.hiddenAgents))
	for k := range m.hiddenAgents {
		list = append(list, k)
	}
	sort.Strings(list)
	next := config.LoadSettings()
	next.HiddenAgents = list
	_ = config.SaveSettings(next)
}

func (m settingsModel) cycleTrust(current string) string {
	switch current {
	case "auto":
		return "require_approval"
	case "require_approval":
		return "read_only"
	case "read_only":
		return "auto"
	default:
		return "auto"
	}
}

func (m settingsModel) saveTrust() tea.Cmd {
	trust := m.trust
	memPath := m.memoryPath
	logger := m.logger
	return func() tea.Msg {
		// trust.Config.Save round-trips every field it read (including
		// `tuning: off`), since m.trust was loaded via trust.Load — no more
		// full-overwrite from a struct that only knew Default/Providers.
		_ = trust.Save(memPath)
		agents := detect.InstalledAgents()
		return settingsRefreshMsg{agents: agents, trust: trust, agentBrands: buildAgentSettingsBrands(agents, logger)}
	}
}

// settingsModel sub-tab labels. Kept as literals so the mouse hit-test can
// locate the sub-tab bar by scanning the rendered (ANSI-stripped) lines.
const (
	subTabGeneralLabel = "General"
	subTabAgentsLabel  = "Agents"
	subTabCustomLabel  = "Customizations"
)

// renderSubTabBar draws the "General | Agents" section switcher, highlighting
// the active sub-tab. The active label is underlined so the row reads clearly
// even on terminals without bold support.
func (m settingsModel) renderSubTabBar() string {
	active := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Underline(true)
	inactive := StyleSubtitle
	gen := inactive.Render(subTabGeneralLabel)
	ag := inactive.Render(subTabAgentsLabel)
	cu := inactive.Render(subTabCustomLabel)
	switch m.subTab {
	case 1:
		ag = active.Render(subTabAgentsLabel)
	case 2:
		cu = active.Render(subTabCustomLabel)
	default:
		gen = active.Render(subTabGeneralLabel)
	}
	sep := StyleSubtitle.Render("    ")
	hint := StyleSubtitle.Render("   ←/→ switch section")
	return "  " + gen + sep + ag + sep + cu + hint
}

// agentsView renders the Agents sub-tab: a toggleable list controlling which
// agents appear on the dashboard grid.
func (m settingsModel) agentsView() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dim := StyleSubtitle
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	red := lipgloss.NewStyle().Foreground(ColorDanger)
	bold := lipgloss.NewStyle().Bold(true)

	w := m.width
	if w <= 0 {
		w = 80
	}
	innerW := w - 10
	if innerW < 44 {
		innerW = 44
	}
	if innerW > 70 {
		innerW = 70
	}

	var lines []string
	lines = append(lines, bold.Render("Dashboard Agents"))
	lines = append(lines, dim.Render("Choose which agents appear on the dashboard grid."))
	lines = append(lines, dim.Render("Hiding only affects display — it never blocks an agent"))
	lines = append(lines, dim.Render("from connecting, writing, or being audited."))
	lines = append(lines, "")

	if len(m.agentBrands) == 0 {
		lines = append(lines, dim.Render("No agents detected or active yet."))
		lines = append(lines, dim.Render("Run  auxly setup  to wire one up."))
	}

	shownCount := 0
	for i, b := range m.agentBrands {
		hidden := m.hiddenAgents[b.id]
		if !hidden {
			shownCount++
		}
		cursor := "  "
		if i == m.agentCursor {
			cursor = cyan.Render("▸ ")
		}
		state := green.Render("[Shown] ")
		if hidden {
			state = red.Render("[Hidden]")
		}
		label := fmt.Sprintf("%s %-20s", b.icon, b.name)
		switch {
		case hidden:
			label = dim.Render(label)
		case i == m.agentCursor:
			label = bold.Render(label)
		}
		lines = append(lines, fmt.Sprintf("%s%s  %s", cursor, label, state))
	}

	if len(m.agentBrands) > 0 {
		lines = append(lines, "")
		lines = append(lines, dim.Render(fmt.Sprintf("%d of %d shown on dashboard", shownCount, len(m.agentBrands))))
	}
	lines = append(lines, "")
	lines = append(lines, dim.Render("↑/↓ move • Enter/Space toggle • ←/→ back to General"))

	var padded []string
	for _, line := range lines {
		padded = append(padded, padLine(line, innerW))
	}
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Render(strings.Join(padded, "\n"))

	var sb strings.Builder
	sb.WriteString(StyleTitle.Render("Settings & Access Configuration"))
	sb.WriteString("\n\n")
	sb.WriteString(m.renderSubTabBar())
	sb.WriteString("\n\n")
	sb.WriteString(panel)
	return sb.String()
}

func (m settingsModel) View() string {
	if m.subTab == 1 {
		return m.agentsView()
	}
	if m.subTab == 2 {
		var sb strings.Builder
		sb.WriteString(StyleTitle.Render("Settings & Access Configuration"))
		sb.WriteString("\n\n")
		sb.WriteString(m.renderSubTabBar())
		sb.WriteString("\n\n")
		sb.WriteString(m.cust.panel())
		return sb.String()
	}

	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	purple := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	dim := StyleSubtitle
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	yellow := lipgloss.NewStyle().Foreground(ColorWarning)
	red := lipgloss.NewStyle().Foreground(ColorDanger)
	bold := lipgloss.NewStyle().Bold(true)

	w := m.width
	if w <= 0 {
		w = 80
	}
	stacked := w > 0 && w < 95
	uniqueAgents := m.getUniqueAgents()
	compact := m.height > 0 && m.height < 48

	var lines []string
	lines = append(lines, bold.Render("Trust & Access Controls"))
	if !compact {
		lines = append(lines, dim.Render("Manage active security levels"))
	}
	lines = append(lines, "")

	cursorDefault := "  "
	if m.cursor == 0 {
		cursorDefault = cyan.Render("▸ ")
	}
	defaultTrust := m.trust.Default
	if defaultTrust == "" {
		defaultTrust = "require_approval"
	}
	defaultTrustRow := fmt.Sprintf("%s%-18s %s", cursorDefault, "Default Trust", m.renderTrust(defaultTrust, green, yellow, red))
	if m.cursor == 0 {
		defaultTrustRow = fmt.Sprintf("%s%s %s", cursorDefault, bold.Render("Default Trust"), m.renderTrust(defaultTrust, green, yellow, red))
	}
	lines = append(lines, defaultTrustRow)
	if !compact {
		lines = append(lines, "")
	}
	lines = append(lines, purple.Render("   Agent Security Overrides"))
	if !compact {
		lines = append(lines, "")
	}

	twoCol := compact && !stacked && len(uniqueAgents) > 6
	agentCell := func(i int) string {
		a := uniqueAgents[i]
		cur := "  "
		if m.cursor == i+1 {
			cur = cyan.Render("▸ ")
		}
		trust := m.trust.Providers[a.Provider].TrustLevel
		if trust == "" {
			trust = defaultTrust
		}
		nameW := 18
		if twoCol {
			nameW = 13
		}
		name := a.Name
		if len(name) > nameW {
			name = name[:nameW-1] + "…"
		}
		if twoCol {
			return fmt.Sprintf("%s%-*s %s", cur, nameW, name, m.renderTrustShort(trust, green, yellow, red))
		}
		return fmt.Sprintf("%s%-*s %s", cur, nameW, name, m.renderTrust(trust, green, yellow, red))
	}

	if twoCol {
		rows := (len(uniqueAgents) + 1) / 2
		const colW = 27
		for r := 0; r < rows; r++ {
			line := padLine(agentCell(r), colW)
			if r+rows < len(uniqueAgents) {
				line += agentCell(r + rows)
			}
			lines = append(lines, line)
		}
	} else {
		for i := range uniqueAgents {
			lines = append(lines, agentCell(i))
		}
	}

	// Live Usage and Auto-Update share one section + one toggle row so adding
	// Auto-Update costs zero extra rows (keeps Settings within the fit guarantee).
	lines = append(lines, "")
	lines = append(lines, purple.Render("   Live Usage  ·  Auto-Update"))
	if !compact {
		lines = append(lines, dim.Render("   Live Usage calls providers for quota; Auto-Update"))
		lines = append(lines, dim.Render("   self-updates after a session. Both off by default."))
	}
	liveCursor, autoCursor := "  ", "  "
	if m.cursor == len(uniqueAgents)+1 {
		liveCursor = cyan.Render("▸ ")
	}
	if m.cursor == len(uniqueAgents)+2 {
		autoCursor = cyan.Render("▸ ")
	}
	liveState := red.Render("[OFF]")
	if m.liveUsage {
		liveState = green.Render("[ON]")
	}
	autoState := red.Render("[OFF]")
	if m.autoUpdate {
		autoState = green.Render("[ON]")
	}
	// Pad the plain labels to a fixed width BEFORE styling so the [ON]/[OFF] badges
	// line up and the bold-on-highlight doesn't shift the column (ANSI in the string
	// would break %-width formatting).
	liveLabel := fmt.Sprintf("%-12s", "Live Usage")
	autoLabel := fmt.Sprintf("%-12s", "Auto-Update")
	if m.cursor == len(uniqueAgents)+1 {
		liveLabel = bold.Render(liveLabel)
	}
	if m.cursor == len(uniqueAgents)+2 {
		autoLabel = bold.Render(autoLabel)
	}
	if compact {
		// Short terminals: keep both on one line to honor the no-scroll fit guarantee.
		lines = append(lines, fmt.Sprintf("%s%s %s     %s%s %s", liveCursor, liveLabel, liveState, autoCursor, autoLabel, autoState))
	} else {
		// Separate rows so ↑/↓ moves the cursor one visible line at a time instead of
		// hopping sideways between two toggles sharing a line.
		lines = append(lines, fmt.Sprintf("%s%s %s", liveCursor, liveLabel, liveState))
		lines = append(lines, fmt.Sprintf("%s%s %s", autoCursor, autoLabel, autoState))
	}

	if m.tuningSuggestions > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("💡 trust: %d suggestion(s) — run `auxly trust suggest`", m.tuningSuggestions)))
	}

	padW := w - 10
	if padW < 40 {
		padW = 40
	}
	if !stacked && !twoCol && padW > 54 {
		padW = 54
	}
	if twoCol {
		padW = 54
	}
	var padded []string
	for _, line := range lines {
		padded = append(padded, padLine(line, padW))
	}
	vPad := 1
	if compact {
		vPad = 0
	}
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(vPad, 2).
		Render(strings.Join(padded, "\n"))

	titleSep := "\n\n"
	if compact {
		titleSep = "\n"
	}
	var sb strings.Builder
	sb.WriteString(StyleTitle.Render("Settings & Access Configuration"))
	sb.WriteString(titleSep)
	sb.WriteString(m.renderSubTabBar())
	sb.WriteString(titleSep)
	sb.WriteString(panel)
	return sb.String()
}

func (m settingsModel) renderTrust(trust string, green, yellow, red lipgloss.Style) string {
	switch trust {
	case "auto":
		return green.Render("[auto]")
	case "require_approval":
		return yellow.Render("[require_approval]")
	case "read_only":
		return red.Render("[read_only]")
	default:
		return trust
	}
}

// renderTrustShort is the compact badge used in the two-column override layout,
// where the full "[require_approval]" label is too wide.
func (m settingsModel) renderTrustShort(trust string, green, yellow, red lipgloss.Style) string {
	switch trust {
	case "auto":
		return green.Render("[auto]")
	case "require_approval":
		return yellow.Render("[approve]")
	case "read_only":
		return red.Render("[read-only]")
	default:
		return trust
	}
}
