package tui

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type skillDetail struct {
	cmd     string
	name    string
	desc    string
	params  string
	useCase string
	example string
}

type skillsModel struct {
	cursor          int
	skills          []skillDetail
	stats           *audit.Stats
	activeProviders []string
	lastRefresh     time.Time
	exportSuccess   bool
	exportTime      time.Time
	width           int
	height          int
}

type skillsRefreshMsg struct {
	stats           *audit.Stats
	activeProviders []string
}

func newSkillsModel() skillsModel {
	skills := []skillDetail{
		{
			cmd:     "/auxly-init",
			name:    "Onboard & Sync",
			desc:    "Trains new agents and imports session facts",
			params:  "None",
			useCase: "The first command a user should run in any new chat session. It explains memory sync rules, trains the agent on how and when to use Auxly, and commands the agent to immediately scan current chat/prompt history to sync existing context to the vault.",
			example: "User: `/auxly-init` \nAgent: \"🚀 AUXLY UNIFIED AGENT MEMORY ONBOARDING & TRAINING... understanding aligned, scanning context to sync...\"",
		},
		{
			cmd:     "/auxly-memory",
			name:    "Retrieve Profile",
			desc:    "Consolidates identity, habits, and preferences",
			params:  "None",
			useCase: "Quickly loads and displays a beautifully structured, consolidated Markdown digest of all your saved core habits, workspace profiles, and tool decisions, allowing the model to gain deep memory alignment in a single operation.",
			example: "User: `/auxly-memory` \nAgent: \"👤 AUXLY UNIFIED AGENT MEMORY PROFILE...\"",
		},
		{
			cmd:     "/auxly-max",
			name:    "Bootstrap Sync",
			desc:    "Generates prompt sync configurations dynamically",
			params:  "None",
			useCase: "Creates a copyable Maximum Memory alignment instructions block, pre-configured with active local gateway ports, enabling instant multi-agent sync (e.g. copying prompt to other local agents or Claude sessions).",
			example: "User: `/auxly-max` \nAgent: \"Onboarding Prompt for AI Assistants: Extract and synthesize ALL possible context...\"",
		},
		{
			cmd:     "/auxly-sync [content]",
			name:    "Synchronize Fact",
			desc:    "Appends a dynamic bullet fact into memory vault",
			params:  "content (Text statement), category (preferences/identity/infra/products/projects/daily/agents/business)",
			useCase: "Performs a smart automated delta-merge to record a new developer habit, coding preference, or environment configuration details into your markdown vaults cleanly. Employs mutex safety layers and keeps logs in System metrics.",
			example: "User: `/auxly-sync I strictly prefer dark mode and standard spaces for indentation` \nAgent: \"✓ Successfully synchronized fact to preferences.md!\"",
		},
		{
			cmd:     "/auxly-pending",
			name:    "Manage Approvals",
			desc:    "Audits and resolves pending writes in-chat",
			params:  "action (list/approve/reject), target_id (File ID)",
			useCase: "Provides full control over your secure write buffer. If an agent tries to modify your memory while set to 'require_approval', it is safely queued. Resolve it instantly in chat without opening TUI screens.",
			example: "User: `/auxly-pending list` (lists items) ➔ `/auxly-pending approve preferences.md.pending` \nAgent: \"✓ Approved and committed!\"",
		},
		{
			cmd:     "/auxly-status",
			name:    "Diagnostics Report",
			desc:    "Displays real-time daemon metrics",
			params:  "None",
			useCase: "Renders a compact, agent-friendly diagnostics overview showing local loopback daemon connection status, port configurations, database metrics, and active agent handshakes.",
			example: "User: `/auxly-status` \nAgent: \"📡 AUXLY GATEWAY SYSTEM STATUS... Writes Today: 12\"",
		},
		{
			cmd:     "/auxly-forget [query]",
			name:    "Prune Memories",
			desc:    "Safely deletes obsolete statement lines",
			params:  "query (Keyword or search pattern)",
			useCase: "Searches across all markdown memory vaults for any lines or bullet statements matching the query and deletes them cleanly. Displays a complete Markdown strikethrough deletion diff for full safety.",
			example: "User: `/auxly-forget dark mode` \nAgent: \"- 🗑️ ~~Smart Sync: I strictly prefer dark mode~~ \n✓ Pruned 1 statement.\"",
		},
		{
			cmd:     "/auxly-learn [context]",
			name:    "Extract Facts",
			desc:    "Extracts structured bullet ideas for review",
			params:  "context (Raw discussion text, code, or git diffs)",
			useCase: "Scans recent development context, chat history, or local code snippets to extract structured habits or environment adjustments automatically. Outputs them for manual confirmation before saving.",
			example: "User: `/auxly-learn I migrated to Go 1.22 and TailwindCSS today.` \nAgent: \"💡 Propose: Go 1.22 environment configured, tailwindcss preference loaded...\"",
		},
	}

	return skillsModel{
		cursor: 0,
		skills: skills,
	}
}

func (m skillsModel) Refresh(memPath string, logger *audit.Logger) tea.Cmd {
	return func() tea.Msg {
		stats := &audit.Stats{
			ByProvider:          make(map[string]int),
			ByAction:            make(map[string]int),
			TotalLogsByProvider: make(map[string]int),
		}
		if logger != nil {
			if s, err := logger.Stats(); err == nil && s != nil {
				stats = s
			}
		}

		var activeProviders []string
		if logger != nil {
			activeProviders, _ = logger.ActiveProviders(5 * time.Minute)
		}

		return skillsRefreshMsg{
			stats:           stats,
			activeProviders: activeProviders,
		}
	}
}

func (m skillsModel) Update(msg tea.Msg) (skillsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case skillsRefreshMsg:
		m.stats = msg.stats
		m.activeProviders = msg.activeProviders
		m.lastRefresh = time.Now()
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
			viewStr := m.View(w, m.height)
			viewLines := strings.Split(viewStr, "\n")

			// Left column skills list: click in X: [0, 38]
			if msg.X >= 0 && msg.X <= 38 {
				clickLineIdx := msg.Y - contentOffsetY
				if clickLineIdx >= 5 {
					clickedIdx := (clickLineIdx - 5) / 3
					if clickedIdx >= 0 && clickedIdx < len(m.skills) {
						if (clickLineIdx-5)%3 != 2 {
							m.cursor = clickedIdx
							m.exportSuccess = false
							return m, nil
						}
					}
				}
			}

			// Export Button click: search dynamically for the line containing the export button
			targetLineY := -1
			for i, line := range viewLines {
				if strings.Contains(line, "Export Skills") || strings.Contains(line, "Exported Claude") {
					targetLineY = i
					break
				}
			}
			if targetLineY != -1 && msg.Y == contentOffsetY+targetLineY && msg.X >= 39 {
				EnsureClaudeSkillsZip()
				m.exportSuccess = true
				m.exportTime = time.Now()
				return m, nil
			}
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.skills)-1 {
				m.cursor++
			}
			m.exportSuccess = false
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			m.exportSuccess = false
		case "d", "D":
			EnsureClaudeSkillsZip()
			m.exportSuccess = true
			m.exportTime = time.Now()
		}
	}
	return m, nil
}

func (m skillsModel) View(width int, height int) string {
	if m.stats == nil {
		return "Loading skills module..."
	}

	dim := lipgloss.NewStyle().Foreground(ColorDim)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	bold := lipgloss.NewStyle().Bold(true)
	purple := lipgloss.NewStyle().Foreground(ColorSecondary)
	cyan := lipgloss.NewStyle().Foreground(ColorPrimary)

	// Determine column widths (Responsive, Mathematically Balanced)
	leftInnerWidth := 28
	rightInnerWidth := width - 44
	if rightInnerWidth < 30 {
		rightInnerWidth = 30
	}

	// 1. LEFT COLUMN: Skills Selector
	var leftLines []string
	leftLines = append(leftLines, bold.Render("🤖 Agent Skill Commands"))
	leftLines = append(leftLines, dim.Render("Navigate [j/k] to explore"))
	leftLines = append(leftLines, "")

	for i, s := range m.skills {
		isSel := i == m.cursor
		var prefix string
		var styledCmd string

		if isSel {
			prefix = purple.Render("➔ ")
			styledCmd = purple.Bold(true).Render(s.cmd)
		} else {
			prefix = dim.Render("• ")
			styledCmd = cyan.Render(s.cmd)
		}

		leftLines = append(leftLines, fmt.Sprintf("%s%s", prefix, styledCmd))
		leftLines = append(leftLines, fmt.Sprintf("  %s", dim.Render(s.name)))
		leftLines = append(leftLines, "")
	}

	// Wrap Left lines
	var wrappedLeftLines []string
	for _, line := range leftLines {
		if visibleWidth(line) > leftInnerWidth {
			wrapped := wrapText(line, leftInnerWidth)
			wrappedLeftLines = append(wrappedLeftLines, wrapped...)
		} else {
			wrappedLeftLines = append(wrappedLeftLines, line)
		}
	}

	var paddedLeftLines []string
	for _, line := range wrappedLeftLines {
		paddedLeftLines = append(paddedLeftLines, padLine(line, leftInnerWidth))
	}

	leftColStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2)

	leftPanel := leftColStyle.Render(strings.Join(paddedLeftLines, "\n"))

	// 2. RIGHT COLUMN: Detail & Matrix Stack
	sel := m.skills[m.cursor]

	// Dynamic config checks
	home, _ := os.UserHomeDir()
	claudeCfgExists := checkFileExists(filepath.Join(home, "Library/Application Support/Claude/claude_desktop_config.json"))
	cursorCfgExists := checkFileExists(filepath.Join(home, "Library/Application Support/Cursor/User/globalStorage/co.heron.cursor/mcpServers.json"))
	codexCfgExists := checkFileExists(filepath.Join(home, ".codex/config.toml"))
	geminiCfgExists := checkFileExists(filepath.Join(home, ".gemini/settings.json"))
	antigravityCfgExists := checkFileExists(filepath.Join(home, ".gemini/antigravity/mcp_config.json"))

	agents := []struct {
		id        string
		name      string
		cfgExists bool
	}{
		{"claude", "Claude Desktop", claudeCfgExists},
		{"claude-code", "Claude Code CLI", true},
		{"cursor", "Cursor IDE", cursorCfgExists},
		{"codex", "Codex IDE", codexCfgExists},
		{"gemini", "Gemini CLI", geminiCfgExists},
		{"antigravity", "Antigravity", antigravityCfgExists},
	}

	var matrixLines []string
	matrixLines = append(matrixLines, bold.Render("🔌 Agent Integration Matrix"))
	matrixLines = append(matrixLines, dim.Render("Live skills status across agents"))
	matrixLines = append(matrixLines, "")

	for _, a := range agents {
		// Connection status based on activity logs
		hasActivity := false
		if m.stats != nil && m.stats.TotalLogsByProvider != nil {
			if a.id == "antigravity" {
				hasActivity = m.stats.TotalLogsByProvider["antigravity"] > 0 ||
					m.stats.TotalLogsByProvider["antigravity-ide"] > 0 ||
					m.stats.TotalLogsByProvider["antigravity-agent"] > 0 ||
					m.stats.TotalLogsByProvider["antigravity-cli"] > 0
			} else {
				hasActivity = m.stats.TotalLogsByProvider[a.id] > 0
			}
		}

		// Config indicator (Pad before styling to avoid ANSI width format bug!)
		rawCfg := "✗ Unlinked"
		if a.cfgExists {
			rawCfg = "✓ Configured"
		}
		rawCfg = fmt.Sprintf("%-13s", rawCfg)

		var cfgText string
		if a.cfgExists {
			cfgText = green.Render(rawCfg)
		} else {
			cfgText = dim.Render(rawCfg)
		}

		// Connection indicator
		isActive := false
		for _, ap := range m.activeProviders {
			if ap == a.id || (a.id == "antigravity" && (ap == "antigravity" || ap == "antigravity-ide" || ap == "antigravity-agent" || ap == "antigravity-cli")) {
				isActive = true
				break
			}
		}

		rawConn := "○ Idle"
		if isActive {
			rawConn = "● Active"
		} else if hasActivity {
			rawConn = "○ Idle"
		}

		var connText string
		if isActive {
			connText = green.Render(rawConn)
		} else {
			connText = dim.Render(rawConn)
		}

		nameStr := fmt.Sprintf("%-18s", a.name)
		styledName := cyan.Render(nameStr)

		matrixLines = append(matrixLines, fmt.Sprintf("  %s · %s · %s",
			styledName,
			cfgText,
			connText,
		))
	}

	wDiv := rightInnerWidth
	sepLine := strings.Repeat("─", wDiv)
	doubleSepLine := strings.Repeat("━", wDiv)

	// Wrap Matrix lines
	var wrappedMatrix []string
	for _, line := range matrixLines {
		if visibleWidth(line) > rightInnerWidth {
			wrapped := wrapText(line, rightInnerWidth)
			wrappedMatrix = append(wrappedMatrix, wrapped...)
		} else {
			wrappedMatrix = append(wrappedMatrix, line)
		}
	}
	var paddedMatrix []string
	for _, line := range wrappedMatrix {
		paddedMatrix = append(paddedMatrix, padLine(line, rightInnerWidth))
	}

	matrixPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorDim).
		Padding(1, 2).
		Render(strings.Join(paddedMatrix, "\n"))

	var detailLines []string
	detailLines = append(detailLines, bold.Render("🎯 Skill Details: "+purple.Bold(true).Render(sel.cmd)))
	detailLines = append(detailLines, purple.Render(sepLine))
	detailLines = append(detailLines, fmt.Sprintf("Command Name: %s", cyan.Render(sel.name)))
	detailLines = append(detailLines, fmt.Sprintf("Description:  %s", dim.Render(sel.desc)))
	detailLines = append(detailLines, fmt.Sprintf("Parameters:   %s", green.Render(sel.params)))
	detailLines = append(detailLines, "")
	detailLines = append(detailLines, bold.Render("How it works:"))
	detailLines = append(detailLines, sel.useCase)
	detailLines = append(detailLines, "")
	detailLines = append(detailLines, bold.Render("Example Usage inside Agent Chat:"))
	detailLines = append(detailLines, purple.Render(sel.example))

	// Wrap Detail lines
	var wrappedDetails []string
	for _, line := range detailLines {
		if visibleWidth(line) > rightInnerWidth {
			wrapped := wrapText(line, rightInnerWidth)
			wrappedDetails = append(wrappedDetails, wrapped...)
		} else {
			wrappedDetails = append(wrappedDetails, line)
		}
	}
	var paddedDetails []string
	for _, line := range wrappedDetails {
		paddedDetails = append(paddedDetails, padLine(line, rightInnerWidth))
	}

	detailPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Render(strings.Join(paddedDetails, "\n"))

	var orangeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#C47E5D")).Bold(true)
	var orangeDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#C47E5D"))

	var guidePanel string
	if m.exportSuccess && time.Since(m.exportTime) < 15*time.Second {
		var guideLines []string
		guideLines = append(guideLines, orangeStyle.Render("🎁 CLAUDE DESKTOP GUI IMPORT GUIDE"))
		guideLines = append(guideLines, orangeStyle.Render(doubleSepLine))
		guideLines = append(guideLines, orangeDimStyle.Render("1. Open the Claude Desktop GUI Settings panel."))
		guideLines = append(guideLines, orangeDimStyle.Render("2. Select 'Customize' -> 'Skills' in the sidebar."))
		guideLines = append(guideLines, orangeDimStyle.Render("3. Click the 'Upload a skill' button."))
		guideLines = append(guideLines, orangeDimStyle.Render("4. Choose the .zip files from your Downloads folder:"))
		guideLines = append(guideLines, orangeStyle.Render("   ↳ "+filepath.Join(home, "Downloads", "auxly-skills", "auxly-memory.zip")))
		guideLines = append(guideLines, orangeDimStyle.Render("5. Press any arrow key to dismiss this guide."))

		var wrappedGuide []string
		for _, line := range guideLines {
			if visibleWidth(line) > rightInnerWidth {
				wrapped := wrapText(line, rightInnerWidth)
				wrappedGuide = append(wrappedGuide, wrapped...)
			} else {
				wrappedGuide = append(wrappedGuide, line)
			}
		}
		var paddedGuide []string
		for _, line := range wrappedGuide {
			paddedGuide = append(paddedGuide, padLine(line, rightInnerWidth))
		}

		guidePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#C47E5D")).
			Padding(1, 2).
			Render(strings.Join(paddedGuide, "\n"))
	} else {
		guidePanel = matrixPanel
	}

	var statusBanner string
	if m.exportSuccess && time.Since(m.exportTime) < 15*time.Second {
		statusBanner = green.Bold(true).Render("  ✓ Exported Claude Skills ZIPs to ~/Downloads/auxly-skills/! 🎉")
	} else {
		btnStyle := lipgloss.NewStyle().
			Background(ColorPrimary).
			Foreground(lipgloss.Color("255")).
			Bold(true).
			Padding(0, 1)
		statusBanner = fmt.Sprintf("  👉 Press [d] or click %s to export Claude Desktop GUI Skills (.zip)", btnStyle.Render(" [d] Export Skills "))
	}

	// Wrap Status Banner to fit rightInnerWidth
	var wrappedStatus []string
	if visibleWidth(statusBanner) > rightInnerWidth {
		wrappedStatus = wrapText(statusBanner, rightInnerWidth)
	} else {
		wrappedStatus = []string{statusBanner}
	}
	var paddedStatus []string
	for _, line := range wrappedStatus {
		paddedStatus = append(paddedStatus, padLine(line, rightInnerWidth))
	}
	statusBannerView := strings.Join(paddedStatus, "\n")

	rightPanel := lipgloss.JoinVertical(lipgloss.Left, detailPanel, " ", guidePanel, " ", statusBannerView)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, "  ", rightPanel)
}

func checkFileExists(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func EnsureClaudeSkillsZip() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	auxlySkillsDir := filepath.Join(home, ".auxly", "skills")
	_ = os.MkdirAll(auxlySkillsDir, 0755)

	commands := map[string]string{
		"auxly-init": `---
name: auxly-init
description: Run the onboarding training, scan current context, and synchronize existing chat context/preferences to Auxly.
---
# /auxly-init

You must immediately invoke the 'auxly_skill_init' MCP tool to align your session instructions, scan current context and system prompts, and synchronize existing facts/preferences to the Auxly vault. Show the beautiful onboarding guide and confirmation card!`,

		"auxly-memory": `---
name: auxly-memory
description: Retrieve and display a consolidated markdown profile of the user's identity, preferences, and system infrastructure.
---
# /auxly-memory

You must immediately invoke the 'auxly_skill_memory' MCP tool to retrieve and display the consolidated profile of the user's identity, preferences, and infrastructure. Do not ask for further clarification, simply run the tool and show the output!`,

		"auxly-max": `---
name: auxly-max
description: Obtain the dynamic Maximum Memory sync instructions block to sync other local agents (e.g. Cursor, Codex) E2E.
---
# /auxly-max

You must immediately invoke the 'auxly_skill_max' MCP tool to align your session, and then immediately call 'auxly_skill_memory' to pull down and load the complete memory vault. Finally, present a beautiful success message confirming that unified memory alignment is fully complete!`,

		"auxly-sync": `---
name: auxly-sync
description: Append and synchronize a new fact, preference, or system detail using smart automated delta-merges into preferences.md.
argument-hint: "<fact or preference statement to sync>"
---
# /auxly-sync

You must immediately invoke the 'auxly_skill_sync' MCP tool, passing the user's provided input statement as the 'content' argument. This performs a smart automated delta-merge to update the preferences.md file. Simply run the tool and display the confirmation output!`,

		"auxly-pending": `---
name: auxly-pending
description: Manage pending memory changes awaiting human approval directly inside the active chat panel.
argument-hint: "[list | approve <id> | reject <id>]"
---
# /auxly-pending

You must immediately invoke the 'auxly_skill_pending' MCP tool, passing the provided arguments (such as action: list/approve/reject, and target ID) to manage the secure memory write queue. Simply run the tool and display the results!`,

		"auxly-status": `---
name: auxly-status
description: Show real-time system diagnostics, active client connections, database sizes, and local daemon status.
---
# /auxly-status

You must immediately invoke the 'auxly_skill_status' MCP tool to retrieve and display the real-time system diagnostics, active connections, and database metrics. Do not perform other actions. Simply run the tool and show the diagnostics screen!`,

		"auxly-forget": `---
name: auxly-forget
description: Search memory vault and prune obsolete or outdated bullet statements cleanly from memory files.
argument-hint: "<query string to search and delete>"
---
# /auxly-forget

You must immediately invoke the 'auxly_skill_forget' MCP tool, passing the user's provided input as the 'query' argument, to search across all memory files and delete matching obsolete lines cleanly. Simply run the tool and display the deletion diff!`,

		"auxly-learn": `---
name: auxly-learn
description: Intercept recent edits or context to extract and propose structured new facts to save into memory files.
argument-hint: "[raw context text or snippet]"
---
# /auxly-learn

You must immediately invoke the 'auxly_skill_learn' MCP tool, passing the provided raw context text or snippet as the 'context' argument, to parse and extract structured new facts. Simply run the tool and display the proposed facts!`,
	}

	updateReminder := "\n\nIMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!"

	for skillName, content := range commands {
		// 1. Write to ~/.auxly/skills/
		zipPath1 := filepath.Join(auxlySkillsDir, skillName+".zip")
		writeZipFile(zipPath1, skillName, content+updateReminder)

		// 2. Write to ~/Downloads/auxly-skills/
		downloadsDir := filepath.Join(home, "Downloads", "auxly-skills")
		_ = os.MkdirAll(downloadsDir, 0755)
		zipPath2 := filepath.Join(downloadsDir, skillName+".zip")
		writeZipFile(zipPath2, skillName, content+updateReminder)
	}
}

func writeZipFile(zipPath, skillName, content string) {
	outFile, err := os.Create(zipPath)
	if err != nil {
		return
	}
	defer outFile.Close()

	zipWriter := zip.NewWriter(outFile)
	defer zipWriter.Close()

	headerPath := skillName + "/SKILL.md"
	w, err := zipWriter.Create(headerPath)
	if err == nil {
		_, _ = w.Write([]byte(content))
	}
}
