package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

type trustConfig struct {
	Default   string                       `yaml:"default"`
	Providers map[string]map[string]string `yaml:"providers"`
}

type settingsModel struct {
	memoryPath            string
	cursor                int
	agents                []detect.Agent
	trust                 trustConfig
	editing               bool
	editField             int
	trustLevels           []string
	organizing            bool
	organizeResult        string
	store                 *memory.Store
	selectedOrganizeAgent int // -2 for Custom, -1 for Direct LLM, 0+ for specific index in unique agents
	width                 int
	height                int

	// Custom Local AI Popup State
	configuringCustom   bool
	customURL           string
	customModels        []string
	customModelIdx      int
	fetchingModels      bool
	customError         string
	selectedCustomModel string
	selectedCustomURL   string

	// Organize Diff Popup State
	organizeDiff string
	showingDiff  bool
	diffScrollY  int

	// Persistent Stats State
	lastRun        string
	lastTokensUsed int
	confirmingRun  bool

	// Live Usage opt-in (calls each agent's provider with its stored login —
	// off keeps Auxly fully local). Persisted via config.SaveSettings.
	liveUsage bool
}

type organizeStats struct {
	LastRun    string `json:"last_run"`
	TokensUsed int    `json:"tokens_used"`
}

type settingsRefreshMsg struct {
	agents []detect.Agent
	trust  trustConfig
	stats  organizeStats
}

type settingsOrganizeResultMsg struct {
	success    bool
	msg        string
	diff       string
	tokensUsed int
}

type customModelsFetchedMsg struct {
	success bool
	models  []string
	err     string
}

func (m settingsModel) fetchCustomModels(url string) tea.Cmd {
	return func() tea.Msg {
		endpoint := strings.TrimRight(url, "/") + "/v1/models"
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(endpoint)
		if err != nil {
			endpoint = strings.TrimRight(url, "/") + "/api/tags"
			resp, err = client.Get(endpoint)
			if err != nil {
				return customModelsFetchedMsg{success: false, err: "Endpoint is unreachable: " + err.Error()}
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return customModelsFetchedMsg{success: false, err: fmt.Sprintf("HTTP status %d", resp.StatusCode)}
		}

		bodyBytes, _ := io.ReadAll(resp.Body)

		type modelItem struct {
			ID string `json:"id"`
		}
		type modelsResp struct {
			Data []modelItem `json:"data"`
		}

		type ollamaModelItem struct {
			Name string `json:"name"`
		}
		type ollamaModelsResp struct {
			Models []ollamaModelItem `json:"models"`
		}

		var mResp modelsResp
		if err := json.Unmarshal(bodyBytes, &mResp); err == nil && len(mResp.Data) > 0 {
			var list []string
			for _, item := range mResp.Data {
				list = append(list, item.ID)
			}
			return customModelsFetchedMsg{success: true, models: list}
		}

		var oResp ollamaModelsResp
		if err := json.Unmarshal(bodyBytes, &oResp); err == nil && len(oResp.Models) > 0 {
			var list []string
			for _, item := range oResp.Models {
				list = append(list, item.Name)
			}
			return customModelsFetchedMsg{success: true, models: list}
		}

		return customModelsFetchedMsg{success: false, err: "Failed to parse model list from endpoint response"}
	}
}

func newSettingsModel(memPath string) settingsModel {
	store := memory.NewStore(memPath)
	return settingsModel{
		memoryPath:            memPath,
		trustLevels:           []string{"auto", "require_approval", "read_only"},
		store:                 store,
		selectedOrganizeAgent: -2,
		customURL:             "http://localhost:11434",
		liveUsage:             config.LoadSettings().LiveUsage,
	}
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

func (m settingsModel) getCLIAgents() []detect.Agent {
	var list []detect.Agent
	seen := make(map[string]bool)
	for _, a := range m.agents {
		isCLI := strings.Contains(a.Name, "CLI") || strings.Contains(a.Name, "Code") || a.Connection == "MCP+Shell" || a.Connection == "Shell"
		// Only offer agents with a real runnable executable — organization
		// fork/execs this, so a config dir/file (e.g. ~/.gemini/antigravity-cli)
		// must never be selectable.
		if isCLI && a.Command != "" {
			if seen[a.Provider] {
				continue
			}
			seen[a.Provider] = true
			list = append(list, a)
		}
	}
	return list
}

func (m settingsModel) Refresh() tea.Cmd {
	memPath := m.memoryPath
	return func() tea.Msg {
		agents := detect.InstalledAgents()

		var trust trustConfig
		trustPath := filepath.Join(memPath, "trust.yaml")
		data, err := os.ReadFile(trustPath)
		if err == nil {
			yaml.Unmarshal(data, &trust)
		}

		var stats organizeStats
		statsPath := filepath.Join(memPath, ".last_organize.json")
		sData, err := os.ReadFile(statsPath)
		if err == nil {
			json.Unmarshal(sData, &stats)
		}

		return settingsRefreshMsg{agents: agents, trust: trust, stats: stats}
	}
}

func (m settingsModel) runOrganize() tea.Cmd {
	return func() tea.Msg {
		cliAgents := m.getCLIAgents()
		var res memory.OrganizeResult
		if m.selectedOrganizeAgent == -2 {
			endpoint := m.selectedCustomURL
			if endpoint == "" {
				endpoint = "http://localhost:11434"
			}
			model := m.selectedCustomModel
			if model == "" {
				model = "qwen2.5-coder:7b"
			}
			res = m.store.OrganizeVaultWithCustom(endpoint, model)
		} else if m.selectedOrganizeAgent >= 0 && m.selectedOrganizeAgent < len(cliAgents) {
			target := cliAgents[m.selectedOrganizeAgent]
			res = m.store.OrganizeVaultWithAgent(target.Name, target.Command)
		} else {
			res = m.store.OrganizeVaultWithAgent("Direct LLM", "")
		}

		if res.Success {
			stats := organizeStats{
				LastRun:    time.Now().Format("2006-01-02 15:04:05"),
				TokensUsed: res.TokensUsed,
			}
			statsPath := filepath.Join(m.memoryPath, ".last_organize.json")
			if sData, err := json.Marshal(stats); err == nil {
				_ = os.WriteFile(statsPath, sData, 0644)
			}
		}

		return settingsOrganizeResultMsg{
			success:    res.Success,
			msg:        res.Message,
			diff:       res.Diff,
			tokensUsed: res.TokensUsed,
		}
	}
}

func (m settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case customModelsFetchedMsg:
		m.fetchingModels = false
		if msg.success {
			m.customModels = msg.models
			m.customModelIdx = 0
			m.customError = ""
		} else {
			m.customError = msg.err
			m.customModels = nil
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case settingsRefreshMsg:
		m.agents = msg.agents
		m.trust = msg.trust
		m.lastRun = msg.stats.LastRun
		m.lastTokensUsed = msg.stats.TokensUsed
	case settingsOrganizeResultMsg:
		m.organizing = false
		m.organizeResult = msg.msg
		m.organizeDiff = msg.diff
		m.diffScrollY = 0
		if msg.success {
			m.lastTokensUsed = msg.tokensUsed
			m.lastRun = time.Now().Format("2006-01-02 15:04:05")
			if msg.diff != "" {
				m.showingDiff = true
			}
		}
		return m, m.Refresh()
	case tea.MouseMsg:
		if m.showingDiff {
			if msg.Type == tea.MouseWheelUp {
				if m.diffScrollY > 0 {
					m.diffScrollY--
				}
				return m, nil
			}
			if msg.Type == tea.MouseWheelDown {
				lines := strings.Split(m.organizeDiff, "\n")
				if m.diffScrollY < len(lines)-12 {
					m.diffScrollY++
				}
				return m, nil
			}
			if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
				// Click outside the 60x20 popup starting at X=8, Y=4 closes it
				popupWidth := 60
				popupHeight := 20
				if msg.X < 8 || msg.X > 8+popupWidth || msg.Y < 4 || msg.Y > 4+popupHeight {
					m.showingDiff = false
					return m, nil
				}
			}
			return m, nil
		}
		if m.confirmingRun {
			if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
				m.confirmingRun = false
				return m, nil
			}
			return m, nil
		}
		if m.configuringCustom {
			return m, nil
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			w := m.width
			if w <= 0 {
				w = 80
			}
			banner := renderBanner(w)
			tabRow := strings.Count(banner, "\n")
			contentOffsetY := tabRow + 4

			viewStr := m.View()
			viewLines := strings.Split(viewStr, "\n")

			defaultTrustLineY := -1
			agentToUseLineY := -1
			runButtonLineY := -1
			liveUsageLineY := -1
			providerLineYs := make(map[string]int)

			uniqueAgents := m.getUniqueAgents()
			for idx, line := range viewLines {
				clean := stripANSI(line)
				if strings.Contains(clean, "Default Trust") {
					defaultTrustLineY = idx
				}
				if strings.Contains(clean, "Agent to Use:") {
					agentToUseLineY = idx
				}
				if strings.Contains(clean, "RUN ON-DEMAND ORGANIZATION") {
					runButtonLineY = idx
				}
				// The toggle row carries the [ON]/[OFF] badge; the heading line
				// ("Live Usage") and the description lines are excluded so only the
				// interactive row hit-tests.
				if strings.Contains(clean, "Live Usage") &&
					(strings.Contains(clean, "[ON]") || strings.Contains(clean, "[OFF]")) {
					liveUsageLineY = idx
				}
				for _, a := range uniqueAgents {
					if strings.Contains(clean, a.Name) && !strings.Contains(clean, "Overrides") && !strings.Contains(clean, "Default Trust") {
						providerLineYs[a.Provider] = idx
					}
				}
			}

			clickedViewLineY := msg.Y - contentOffsetY

			if clickedViewLineY == defaultTrustLineY && defaultTrustLineY != -1 {
				m.cursor = 0
				m.trust.Default = m.cycleTrust(m.trust.Default)
				return m, m.saveTrust()
			}

			for provider, lineY := range providerLineYs {
				if clickedViewLineY == lineY {
					providerIdx := -1
					for i, a := range uniqueAgents {
						if a.Provider == provider {
							providerIdx = i
							break
						}
					}
					if providerIdx != -1 {
						m.cursor = providerIdx + 1
					}

					if m.trust.Providers == nil {
						m.trust.Providers = make(map[string]map[string]string)
					}
					current := ""
					if p, ok := m.trust.Providers[provider]; ok {
						current = p["trust_level"]
					}
					next := m.cycleTrust(current)
					if m.trust.Providers[provider] == nil {
						m.trust.Providers[provider] = make(map[string]string)
					}
					m.trust.Providers[provider]["trust_level"] = next
					return m, m.saveTrust()
				}
			}

			if (clickedViewLineY == agentToUseLineY || clickedViewLineY == agentToUseLineY+1) && agentToUseLineY != -1 {
				m.cursor = len(uniqueAgents) + 2
				dir := 1
				if msg.X < 55 {
					dir = -1
				}
				m.cycleOrganizeAgent(dir)
				return m, nil
			}

			if clickedViewLineY == runButtonLineY && runButtonLineY != -1 {
				m.cursor = len(uniqueAgents) + 3
				if m.selectedOrganizeAgent == -2 && m.selectedCustomURL == "" {
					m.configuringCustom = true
					m.customModels = nil
					m.customError = ""
					m.fetchingModels = false
					return m, nil
				}
				if !m.organizing {
					m.confirmingRun = true
					m.organizeResult = ""
					return m, nil
				}
			}

			if clickedViewLineY == liveUsageLineY && liveUsageLineY != -1 {
				m.cursor = len(uniqueAgents) + 1
				return m, m.toggleLiveUsage()
			}
		}
	case tea.KeyMsg:
		uniqueAgents := m.getUniqueAgents()

		if m.showingDiff {
			switch msg.String() {
			case "up", "k":
				if m.diffScrollY > 0 {
					m.diffScrollY--
				}
				return m, nil
			case "down", "j":
				lines := strings.Split(m.organizeDiff, "\n")
				if m.diffScrollY < len(lines)-12 {
					m.diffScrollY++
				}
				return m, nil
			case "esc", "enter", "q", " ":
				m.showingDiff = false
				return m, nil
			default:
				return m, nil
			}
		}

		if m.confirmingRun {
			switch msg.String() {
			case "y", "Y", "enter":
				m.confirmingRun = false
				m.organizing = true
				m.organizeResult = ""
				return m, m.runOrganize()
			case "n", "N", "esc":
				m.confirmingRun = false
				return m, nil
			default:
				return m, nil
			}
		}

		if m.configuringCustom {
			switch msg.String() {
			case "esc":
				m.configuringCustom = false
				return m, nil
			case "up", "k":
				if len(m.customModels) > 0 {
					if m.customModelIdx > 0 {
						m.customModelIdx--
					}
				}
				return m, nil
			case "down", "j":
				if len(m.customModels) > 0 {
					if m.customModelIdx < len(m.customModels)-1 {
						m.customModelIdx++
					}
				}
				return m, nil
			case "enter":
				if len(m.customModels) > 0 {
					m.selectedCustomURL = m.customURL
					m.selectedCustomModel = m.customModels[m.customModelIdx]
					m.configuringCustom = false
					m.organizeResult = fmt.Sprintf("✓ Endpoint %s and model %s configured!", m.selectedCustomURL, m.selectedCustomModel)
					return m, nil
				} else if !m.fetchingModels {
					m.fetchingModels = true
					m.customError = ""
					return m, m.fetchCustomModels(m.customURL)
				}
				return m, nil
			case "backspace":
				if len(m.customModels) == 0 && len(m.customURL) > 0 {
					m.customURL = m.customURL[:len(m.customURL)-1]
				}
				return m, nil
			default:
				if len(m.customModels) == 0 && len(msg.String()) == 1 {
					m.customURL += msg.String()
				}
				return m, nil
			}
		}

		switch msg.String() {
		case "j", "down":
			max := len(uniqueAgents) + 3
			if m.cursor < max {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "h", "left":
			if m.cursor == len(uniqueAgents)+2 {
				m.cycleOrganizeAgent(-1)
			}
		case "l", "right":
			if m.cursor == len(uniqueAgents)+2 {
				m.cycleOrganizeAgent(1)
			}
		case "enter", " ":
			if m.cursor == 0 {
				m.trust.Default = m.cycleTrust(m.trust.Default)
				return m, m.saveTrust()
			} else if m.cursor-1 < len(uniqueAgents) {
				provider := uniqueAgents[m.cursor-1].Provider
				if m.trust.Providers == nil {
					m.trust.Providers = make(map[string]map[string]string)
				}
				current := ""
				if p, ok := m.trust.Providers[provider]; ok {
					current = p["trust_level"]
				}
				next := m.cycleTrust(current)
				if m.trust.Providers[provider] == nil {
					m.trust.Providers[provider] = make(map[string]string)
				}
				m.trust.Providers[provider]["trust_level"] = next
				return m, m.saveTrust()
			} else if m.cursor == len(uniqueAgents)+2 {
				if m.selectedOrganizeAgent == -2 {
					m.configuringCustom = true
					m.customURL = "http://localhost:11434"
					if m.selectedCustomURL != "" {
						m.customURL = m.selectedCustomURL
					}
					m.customModels = nil
					m.customError = ""
					m.fetchingModels = false
					return m, nil
				}
				m.cycleOrganizeAgent(1)
				return m, nil
			} else if m.cursor == len(uniqueAgents)+3 {
				if m.selectedOrganizeAgent == -2 && m.selectedCustomURL == "" {
					m.configuringCustom = true
					m.customModels = nil
					m.customError = ""
					m.fetchingModels = false
					return m, nil
				}
				if !m.organizing {
					m.confirmingRun = true
					m.organizeResult = ""
					return m, nil
				}
			} else if m.cursor == len(uniqueAgents)+1 {
				return m, m.toggleLiveUsage()
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

func (m *settingsModel) cycleOrganizeAgent(dir int) {
	cliAgents := m.getCLIAgents()

	if len(cliAgents) == 0 {
		m.selectedOrganizeAgent = -2
		return
	}

	m.selectedOrganizeAgent += dir

	if m.selectedOrganizeAgent == -1 {
		if dir > 0 {
			m.selectedOrganizeAgent = 0
		} else {
			m.selectedOrganizeAgent = -2
		}
	}

	if m.selectedOrganizeAgent < -2 {
		m.selectedOrganizeAgent = len(cliAgents) - 1
	} else if m.selectedOrganizeAgent >= len(cliAgents) {
		m.selectedOrganizeAgent = -2
	}
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
	return func() tea.Msg {
		trustPath := filepath.Join(memPath, "trust.yaml")
		data, err := yaml.Marshal(&trust)
		if err == nil {
			os.WriteFile(trustPath, data, 0644)
		}
		return settingsRefreshMsg{agents: detect.InstalledAgents(), trust: trust}
	}
}

func (m settingsModel) View() string {
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

	// Width of right panel card
	var rightInnerWidth int
	if stacked {
		rightInnerWidth = w - 10
		if rightInnerWidth < 30 {
			rightInnerWidth = 40
		}
	} else {
		rightInnerWidth = w - 60
		if rightInnerWidth < 30 {
			rightInnerWidth = 30
		}
	}

	uniqueAgents := m.getUniqueAgents()

	// 1. LEFT PANEL: Trust & Access Controls
	var leftLines []string
	leftLines = append(leftLines, bold.Render("Trust & Access Controls"))
	leftLines = append(leftLines, dim.Render("Manage active security levels"))
	leftLines = append(leftLines, "")

	// Default Trust row
	cursorDefault := "  "
	if m.cursor == 0 {
		cursorDefault = cyan.Render("▸ ")
	}
	defaultTrust := m.trust.Default
	if defaultTrust == "" {
		defaultTrust = "require_approval"
	}

	// Pad fields correctly accounting for ANSI escape sequences
	defaultTrustRow := fmt.Sprintf("%s%-18s %s",
		cursorDefault,
		"Default Trust",
		m.renderTrust(defaultTrust, green, yellow, red),
	)
	if m.cursor == 0 {
		defaultTrustRow = fmt.Sprintf("%s%s %s",
			cursorDefault,
			bold.Render("Default Trust"),
			m.renderTrust(defaultTrust, green, yellow, red),
		)
	}
	leftLines = append(leftLines, defaultTrustRow)
	leftLines = append(leftLines, "")
	leftLines = append(leftLines, purple.Render("   Agent Security Overrides"))
	leftLines = append(leftLines, "")

	for i, a := range uniqueAgents {
		cursorAgent := "  "
		if m.cursor == i+1 {
			cursorAgent = cyan.Render("▸ ")
		}

		trust := ""
		if p, ok := m.trust.Providers[a.Provider]; ok {
			trust = p["trust_level"]
		}
		if trust == "" {
			trust = defaultTrust
		}

		agentText := fmt.Sprintf("%s%-18s %s",
			cursorAgent,
			a.Name,
			m.renderTrust(trust, green, yellow, red),
		)
		leftLines = append(leftLines, agentText)
	}

	// Live Usage toggle (opt-in network panel). Cursor sits last.
	leftLines = append(leftLines, "")
	leftLines = append(leftLines, purple.Render("   Live Usage"))
	leftLines = append(leftLines, dim.Render("   Calls each agent's provider with its stored"))
	leftLines = append(leftLines, dim.Render("   login — off keeps Auxly fully local."))
	cursorLive := "  "
	if m.cursor == len(uniqueAgents)+1 {
		cursorLive = cyan.Render("▸ ")
	}
	liveState := red.Render("[OFF]")
	if m.liveUsage {
		liveState = green.Render("[ON]")
	}
	liveLabel := "Live Usage"
	if m.cursor == len(uniqueAgents)+1 {
		liveLabel = bold.Render("Live Usage") + strings.Repeat(" ", 8)
	} else {
		liveLabel = fmt.Sprintf("%-18s", liveLabel)
	}
	leftLines = append(leftLines, fmt.Sprintf("%s%s %s", cursorLive, liveLabel, liveState))

	// 2. RIGHT PANEL: Memory Organization
	var rightLines []string
	rightLines = append(rightLines, bold.Render("On-Demand Memory Organization"))
	rightLines = append(rightLines, dim.Render("De-duplicate and summarize facts"))
	rightLines = append(rightLines, "")

	cliAgents := m.getCLIAgents()

	// Determine active agent string selection
	agentSelectStr := "[ Custom LLM Endpoint ]"
	if m.selectedOrganizeAgent == -2 {
		if m.selectedCustomModel != "" {
			agentSelectStr = fmt.Sprintf("[ Custom: %s ]", m.selectedCustomModel)
		}
	} else if m.selectedOrganizeAgent >= 0 && m.selectedOrganizeAgent < len(cliAgents) {
		agentSelectStr = fmt.Sprintf("[ %s ]", cliAgents[m.selectedOrganizeAgent].Name)
	}

	cursorAgentSelect := "  "
	if m.cursor == len(uniqueAgents)+2 {
		cursorAgentSelect = cyan.Render("▸ ")
	}

	cursorRun := "  "
	if m.cursor == len(uniqueAgents)+3 {
		cursorRun = cyan.Render("▸ ")
	}

	rightLines = append(rightLines, fmt.Sprintf("  %sAgent to Use:", cursorAgentSelect))
	rightLines = append(rightLines, fmt.Sprintf("  %s", cyan.Render("◀ "+agentSelectStr+" ▶")))
	rightLines = append(rightLines, "")

	if m.organizing {
		rightLines = append(rightLines, fmt.Sprintf("  %s", yellow.Render("Organizing memory vault... Please wait...")))
	} else {
		// A beautiful clickable button styling
		btnStyle := lipgloss.NewStyle().
			Background(ColorPrimary).
			Foreground(lipgloss.Color("255")).
			Bold(true).
			Padding(0, 3)
		if m.cursor == len(uniqueAgents)+3 {
			btnStyle = btnStyle.Background(ColorSecondary)
		}
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", cursorRun, btnStyle.Render(" RUN ON-DEMAND ORGANIZATION ")))
	}
	rightLines = append(rightLines, "")

	if m.organizeResult != "" {
		resStyle := green
		if !strings.HasPrefix(m.organizeResult, "✓") {
			resStyle = red
		}
		rightLines = append(rightLines, fmt.Sprintf("  %s", resStyle.Render(m.organizeResult)))
		rightLines = append(rightLines, "")
	}

	rightLines = append(rightLines, dim.Render("How it works:"))
	rightLines = append(rightLines, dim.Render("Combines chronological facts, resolves duplicates, and refines"))
	rightLines = append(rightLines, dim.Render("sections into clean, structured summaries using a local LLM."))
	rightLines = append(rightLines, dim.Render("Does NOT overwrite your core identity, projects, or active scopes."))
	rightLines = append(rightLines, "")

	estTokens := m.store.GetEstimatedTokens()
	if m.lastRun != "" {
		rightLines = append(rightLines, fmt.Sprintf("%-23s %s", dim.Render("Last Run:"), bold.Render(m.lastRun)))
		tokenStr := fmt.Sprintf("%d tokens", m.lastTokensUsed)
		if m.lastTokensUsed == 0 {
			tokenStr = "Unknown"
		}
		rightLines = append(rightLines, fmt.Sprintf("%-23s %s", dim.Render("Actual Tokens Used:"), bold.Render(tokenStr)))
	}
	rightLines = append(rightLines, fmt.Sprintf("%-23s %s", dim.Render("Estimated Token Cost:"), bold.Render(fmt.Sprintf("~%d tokens", estTokens))))
	rightLines = append(rightLines, fmt.Sprintf("%-23s %s", dim.Render("Recommended Frequency:"), bold.Render("Optional; once a week or after ~30 writes.")))

	// Wrap long lines in rightLines that exceed rightInnerWidth
	var wrappedRightLines []string
	for _, line := range rightLines {
		if visibleWidth(line) > rightInnerWidth {
			wrapped := wrapText(line, rightInnerWidth)
			wrappedRightLines = append(wrappedRightLines, wrapped...)
		} else {
			wrappedRightLines = append(wrappedRightLines, line)
		}
	}
	rightLines = wrappedRightLines

	// Mathematical Height Alignment
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

	var leftPadW int
	if stacked {
		leftPadW = w - 10
		if leftPadW < 40 {
			leftPadW = 40
		}
	} else {
		leftPadW = 40
	}

	var paddedLeftLines []string
	for _, line := range leftLines {
		paddedLeftLines = append(paddedLeftLines, padLine(line, leftPadW))
	}

	var paddedRightLines []string
	for _, line := range rightLines {
		paddedRightLines = append(paddedRightLines, padLine(line, rightInnerWidth))
	}

	leftColStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2)

	leftPanel := leftColStyle.Render(strings.Join(paddedLeftLines, "\n"))

	rightColStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorDim).
		Padding(1, 2)

	rightPanel := rightColStyle.Render(strings.Join(paddedRightLines, "\n"))

	var content string
	if !stacked {
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, "  ", rightPanel)
	} else {
		content = lipgloss.JoinVertical(lipgloss.Left, leftPanel, "", rightPanel)
	}

	var sb strings.Builder
	sb.WriteString(StyleTitle.Render("Settings & Access Configuration"))
	sb.WriteString("\n\n")
	sb.WriteString(content)

	fullView := sb.String()

	if m.configuringCustom {
		var pb strings.Builder
		pb.WriteString(bold.Render("⚙️  Configure Custom LLM Endpoint") + "\n")
		pb.WriteString(dim.Render("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━") + "\n\n")

		pb.WriteString("Endpoint URL (Press [Enter] to fetch models):\n")
		urlColor := cyan
		if m.fetchingModels {
			urlColor = yellow
		}
		pb.WriteString(urlColor.Render("  "+m.customURL) + "\n\n")

		if m.fetchingModels {
			pb.WriteString(yellow.Render(" ⏳ Querying available models from endpoint...") + "\n\n")
		} else if m.customError != "" {
			pb.WriteString(red.Render(" ⚠️ Error: "+m.customError) + "\n\n")
		} else if len(m.customModels) > 0 {
			pb.WriteString(green.Render(" ✓ Successfully loaded ") + bold.Render(fmt.Sprintf("%d models", len(m.customModels))) + ":\n")
			pb.WriteString(dim.Render(" (Use Up/Down arrows to select model & Enter to save)") + "\n\n")

			start := m.customModelIdx - 1
			if start < 0 {
				start = 0
			}
			end := start + 4
			if end > len(m.customModels) {
				end = len(m.customModels)
				start = end - 4
				if start < 0 {
					start = 0
				}
			}

			for i := start; i < end; i++ {
				cursor := "  "
				if i == m.customModelIdx {
					cursor = cyan.Render("▸ ")
				}
				modelName := m.customModels[i]
				if i == m.customModelIdx {
					pb.WriteString(fmt.Sprintf("%s%s\n", cursor, bold.Render(modelName)))
				} else {
					pb.WriteString(fmt.Sprintf("%s%s\n", cursor, dim.Render(modelName)))
				}
			}
			pb.WriteString("\n")
		} else {
			pb.WriteString(dim.Render(" Type local AI endpoint URL (e.g. http://localhost:11434)") + "\n\n")
		}

		pb.WriteString(dim.Render(" [Enter]: Confirm/Fetch • [Esc]: Cancel & Return"))

		popupStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Background(lipgloss.Color("0")).
			Padding(1, 2).
			Width(52)

		popupStr := popupStyle.Render(pb.String())
		fullView = overlayPopup(fullView, popupStr, 12, 5)
	}

	if m.showingDiff {
		var pb strings.Builder
		pb.WriteString(bold.Render("🎉 Memory Vault Organization Results") + "\n")
		pb.WriteString(dim.Render("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━") + "\n\n")
		pb.WriteString(green.Render(" ✓ Deduplication & Consolidation Summary:") + "\n\n")

		lines := strings.Split(m.organizeDiff, "\n")
		viewportHeight := 12

		if m.diffScrollY < 0 {
			m.diffScrollY = 0
		}
		if m.diffScrollY > len(lines)-viewportHeight {
			m.diffScrollY = len(lines) - viewportHeight
		}
		if m.diffScrollY < 0 {
			m.diffScrollY = 0
		}

		endIdx := m.diffScrollY + viewportHeight
		if endIdx > len(lines) {
			endIdx = len(lines)
		}

		for i := m.diffScrollY; i < endIdx; i++ {
			line := lines[i]
			if strings.HasPrefix(line, "- ") {
				pb.WriteString(red.Render(line) + "\n")
			} else if strings.HasPrefix(line, "+ ") {
				pb.WriteString(green.Render(line) + "\n")
			} else {
				pb.WriteString(dim.Render(line) + "\n")
			}
		}

		if len(lines) > viewportHeight {
			pb.WriteString(dim.Render(fmt.Sprintf(" (Line %d-%d of %d) • Scroll via Up/Down/j/k or Mouse Wheel", m.diffScrollY+1, endIdx, len(lines))) + "\n")
		} else {
			pb.WriteString("\n")
		}
		pb.WriteString(dim.Render(" Press [Esc], [Enter], or click outside to close"))

		popupStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorSuccess).
			Background(lipgloss.Color("0")).
			Padding(1, 2).
			Width(60)

		popupStr := popupStyle.Render(pb.String())
		fullView = overlayPopup(fullView, popupStr, 8, 4)
	}

	if m.confirmingRun {
		var pb strings.Builder
		pb.WriteString(bold.Render("🤔 Confirm Memory Vault Organization") + "\n")
		pb.WriteString(dim.Render("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━") + "\n\n")

		pb.WriteString("Are you sure you want to run memory optimization?\n\n")

		agentStr := "[ Local LLM / Direct API ]"
		cliAgents := m.getCLIAgents()
		if m.selectedOrganizeAgent == -2 {
			if m.selectedCustomModel != "" {
				agentStr = fmt.Sprintf("Custom Model: %s", m.selectedCustomModel)
			} else {
				agentStr = "Custom LLM Endpoint"
			}
		} else if m.selectedOrganizeAgent >= 0 && m.selectedOrganizeAgent < len(cliAgents) {
			agentStr = cliAgents[m.selectedOrganizeAgent].Name
		}

		pb.WriteString(fmt.Sprintf("%-18s %s\n", dim.Render("Target Agent:"), bold.Render(agentStr)))

		estCost := m.store.GetEstimatedTokens()
		pb.WriteString(fmt.Sprintf("%-18s %s\n\n", dim.Render("Est. Token Cost:"), bold.Render(fmt.Sprintf("~%d tokens", estCost))))

		pb.WriteString(dim.Render("This will scan all files, de-duplicate chronological") + "\n")
		pb.WriteString(dim.Render("facts, and consolidate them into structured summaries.") + "\n\n")

		pb.WriteString(bold.Render("  [Y] Yes, Run") + "   " + dim.Render("[N] Cancel") + "\n\n")
		pb.WriteString(dim.Render(" Press [Y] or [Enter] to run, [N] or [Esc] to cancel"))

		popupStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorWarning).
			Background(lipgloss.Color("0")).
			Padding(1, 2).
			Width(48)

		popupStr := popupStyle.Render(pb.String())
		fullView = overlayPopup(fullView, popupStr, 14, 5)
	}

	return fullView
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
