package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/templates"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type wizardStep int

const (
	wizardStepInstallType wizardStep = iota
	wizardStepDetecting
	wizardStepAgentSelect
	wizardStepMigrating
	wizardStepSetup
	wizardStepOnboarding
	wizardStepDone
)

type agentEntry struct {
	agent    detect.Agent
	selected bool
}

type scanResult struct {
	provider string
	path     string
	folders  int
	files    int
}

type agentOnboardStatus struct {
	name    string
	isCLI   bool
	status  string // "success", "auth_needed", "manual"
	message string
}

type onboardDoneMsg struct {
	statuses []agentOnboardStatus
}

type setupDoneMsg struct {
	err error
}

type onboardAgentProgressMsg struct {
	status agentOnboardStatus
}

type wizardModel struct {
	step            wizardStep
	cursor          int
	installType     int
	agents          []agentEntry
	memoryPath      string
	migrated        int
	migrationLog    []string
	spinFrame       int
	scanResults     []scanResult
	totalFolders    int
	totalFiles      int
	setupChoice     int // 0=yes, 1=skip
	width           int
	height          int
	onboardStatuses []agentOnboardStatus
}

type tickMsg struct{}
type migrationDoneMsg struct {
	count int
	log   []string
}
type detectAndScanResult struct {
	agents       []detect.Agent
	scanResults  []scanResult
	totalFolders int
	totalFiles   int
}
type detectAndScanDoneMsg struct {
	result detectAndScanResult
}

func tickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

func newWizardModel(memPath string) wizardModel {
	return wizardModel{
		step:       wizardStepInstallType,
		memoryPath: memPath,
	}
}

func (m wizardModel) Init() tea.Cmd { return nil }

func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		m.spinFrame++
		if m.step == wizardStepDetecting || m.step == wizardStepMigrating || m.step == wizardStepOnboarding {
			return m, tickCmd()
		}
		return m, nil

	case detectAndScanDoneMsg:
		for _, a := range msg.result.agents {
			m.agents = append(m.agents, agentEntry{agent: a, selected: true})
		}
		m.scanResults = msg.result.scanResults
		m.totalFolders = msg.result.totalFolders
		m.totalFiles = msg.result.totalFiles
		m.step = wizardStepAgentSelect
		m.cursor = 0
		return m, nil

	case migrationDoneMsg:
		m.migrated = msg.count
		m.migrationLog = msg.log
		m.step = wizardStepSetup
		m.cursor = 0
		// Write .initialized marker
		markerPath := filepath.Join(m.memoryPath, ".initialized")
		os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)), 0644)
		return m, nil

	case setupDoneMsg:
		m.onboardStatuses = []agentOnboardStatus{}

		var cmds []tea.Cmd

		isSelected := func(name string) bool {
			for _, a := range m.agents {
				if strings.Contains(strings.ToLower(a.agent.Name), strings.ToLower(name)) && a.selected {
					return true
				}
			}
			return false
		}

		// Pre-populate manual statuses for Desktop/IDE agents so they show immediately
		for _, a := range m.agents {
			if !a.selected {
				continue
			}
			name := a.agent.Name
			// CLI agents we drive headlessly are handled below — skip them here.
			if name == "Claude Code / CLI" || name == "Gemini CLI" || name == "Codex CLI" ||
				name == "Antigravity CLI" || name == "Cursor CLI" || name == "GitHub Copilot CLI" {
				continue
			}
			message := fmt.Sprintf("Open %s and run '/auxly-init'", name)
			// Perplexity (macOS) is wired through its Connectors GUI, not a slash
			// command — so point the user at the right place with the exact command.
			if name == "Perplexity" {
				message = "Perplexity → Settings → Connectors → Add (Simple): auxly --path <mem> mcp-server"
			}
			m.onboardStatuses = append(m.onboardStatuses, agentOnboardStatus{
				name:    name,
				isCLI:   false,
				status:  "manual",
				message: message,
			})
		}

		// Fire parallel async commands for CLI agents
		if isSelected("Claude") {
			cmds = append(cmds, m.onboardClaudeCmd())
			m.onboardStatuses = append(m.onboardStatuses, agentOnboardStatus{
				name:    "Claude Code / CLI",
				isCLI:   true,
				status:  "pending",
				message: "✓ Connecting...",
			})
		}
		if isSelected("Gemini") {
			cmds = append(cmds, m.onboardGeminiCmd())
			m.onboardStatuses = append(m.onboardStatuses, agentOnboardStatus{
				name:    "Gemini CLI",
				isCLI:   true,
				status:  "pending",
				message: "✓ Connecting...",
			})
		}
		if isSelected("Codex CLI") {
			cmds = append(cmds, m.onboardCodexCmd())
			m.onboardStatuses = append(m.onboardStatuses, agentOnboardStatus{
				name:    "Codex CLI",
				isCLI:   true,
				status:  "pending",
				message: "✓ Connecting...",
			})
		}
		if isSelected("Antigravity CLI") {
			cmds = append(cmds, m.onboardAntigravityCmd())
			m.onboardStatuses = append(m.onboardStatuses, agentOnboardStatus{
				name:    "Antigravity CLI",
				isCLI:   true,
				status:  "pending",
				message: "✓ Connecting...",
			})
		}
		if isSelected("Cursor CLI") {
			cmds = append(cmds, m.onboardCursorCmd())
			m.onboardStatuses = append(m.onboardStatuses, agentOnboardStatus{
				name:    "Cursor CLI",
				isCLI:   true,
				status:  "pending",
				message: "✓ Connecting...",
			})
		}
		if isSelected("Copilot") {
			cmds = append(cmds, m.onboardCopilotCmd())
			m.onboardStatuses = append(m.onboardStatuses, agentOnboardStatus{
				name:    "GitHub Copilot CLI",
				isCLI:   true,
				status:  "pending",
				message: "✓ Connecting...",
			})
		}

		if len(cmds) == 0 {
			m.step = wizardStepDone
			m.cursor = 0
			return m, nil
		}

		return m, tea.Batch(cmds...)

	case onboardAgentProgressMsg:
		found := false
		for i, s := range m.onboardStatuses {
			if s.name == msg.status.name {
				m.onboardStatuses[i] = msg.status
				found = true
				break
			}
		}
		if !found {
			m.onboardStatuses = append(m.onboardStatuses, msg.status)
		}

		// Check if all selected CLI agents have finished (status is success or auth_needed)
		allDone := true
		for _, s := range m.onboardStatuses {
			if s.isCLI && s.status == "pending" {
				allDone = false
				break
			}
		}
		if allDone {
			m.step = wizardStepDone
			m.cursor = 0
		}
		return m, nil

	case tea.KeyMsg:
		switch m.step {
		case wizardStepInstallType:
			return m.updateInstallType(msg)
		case wizardStepAgentSelect:
			return m.updateAgentSelect(msg)
		case wizardStepSetup:
			return m.updateSetup(msg)
		case wizardStepDetecting, wizardStepMigrating, wizardStepOnboarding:
			if msg.String() == "q" || msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
		case wizardStepDone:
			if msg.String() == "enter" || msg.String() == "q" || msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m wizardModel) updateInstallType(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < 1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.installType = m.cursor
		if m.installType == 0 {
			home, _ := os.UserHomeDir()
			m.memoryPath = filepath.Join(home, ".auxly", "memory")
		} else {
			m.memoryPath = filepath.Clean(".auxly/memory")
		}
		m.cursor = 0
		m.step = wizardStepDetecting
		return m, tea.Batch(tickCmd(), m.detectAgentsProgressive())
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m wizardModel) detectAgentsProgressive() tea.Cmd {
	return func() tea.Msg {
		home, _ := os.UserHomeDir()
		time.Sleep(500 * time.Millisecond)
		agents := detect.InstalledAgents()
		time.Sleep(400 * time.Millisecond)

		var cursorBaseDir string
		switch runtime.GOOS {
		case "darwin":
			cursorBaseDir = filepath.Join(home, "Library/Application Support/Cursor")
		case "linux":
			cursorBaseDir = filepath.Join(home, ".config/Cursor")
		default:
			cursorBaseDir = filepath.Join(os.Getenv("APPDATA"), "Cursor")
		}
		agentPaths := map[string]string{
			"claude":      filepath.Join(home, "Library/Application Support/Claude"),
			"cursor":      cursorBaseDir,
			"claude-code": filepath.Join(home, ".claude"),
			"gemini":      filepath.Join(home, ".gemini"),
			"codex":       filepath.Join(home, ".codex"),
		}

		var results []scanResult
		totalFolders := 0
		totalFiles := 0

		for provider, path := range agentPaths {
			time.Sleep(120 * time.Millisecond)
			folders := 0
			files := 0
			entries, err := os.ReadDir(path)
			if err == nil {
				for _, e := range entries {
					if e.IsDir() {
						folders++
					} else {
						files++
					}
				}
			}
			totalFolders += folders
			totalFiles += files
			results = append(results, scanResult{provider: provider, path: path, folders: folders, files: files})
		}

		time.Sleep(300 * time.Millisecond)
		return detectAndScanDoneMsg{result: detectAndScanResult{
			agents: agents, scanResults: results,
			totalFolders: totalFolders, totalFiles: totalFiles,
		}}
	}
}

func (m wizardModel) updateAgentSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.agents) {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case " ":
		if m.cursor < len(m.agents) {
			m.agents[m.cursor].selected = !m.agents[m.cursor].selected
		} else {
			allSelected := true
			for _, a := range m.agents {
				if !a.selected {
					allSelected = false
					break
				}
			}
			for i := range m.agents {
				m.agents[i].selected = !allSelected
			}
		}
	case "a":
		for i := range m.agents {
			m.agents[i].selected = true
		}
	case "enter":
		m.step = wizardStepMigrating
		return m, tea.Batch(tickCmd(), m.runMigration())
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m wizardModel) updateSetup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < 1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.setupChoice = m.cursor
		if m.setupChoice == 0 {
			m.step = wizardStepOnboarding
			return m, tea.Batch(tickCmd(), m.runSetupCmd())
		}
		m.step = wizardStepDone
		return m, nil
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m wizardModel) runSetupCmd() tea.Cmd {
	return func() tea.Msg {
		binaryPath, err := os.Executable()
		if err != nil {
			binaryPath = "auxly"
		}
		memPath, _ := filepath.Abs(m.memoryPath)
		setupCmd := exec.Command(binaryPath, "--path", memPath, "setup")
		setupCmd.Stdin = strings.NewReader("")
		runErr := setupCmd.Run()
		return setupDoneMsg{err: runErr}
	}
}

func (m wizardModel) onboardClaudeCmd() tea.Cmd {
	return func() tea.Msg {
		memPath, _ := filepath.Abs(m.memoryPath)
		status := agentOnboardStatus{name: "Claude Code / CLI", isCLI: true}
		claudePath, err := lookPath("claude")
		if err != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
			return onboardAgentProgressMsg{status: status}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, claudePath, "-p", "/auxly-init")
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "AUXLY_MEMORY_PATH="+memPath)
		outBytes, runErr := cmd.CombinedOutput()
		output := string(outBytes)

		outputLower := strings.ToLower(output)
		isAuthError := strings.Contains(outputLower, "login") ||
			strings.Contains(outputLower, "auth") ||
			strings.Contains(outputLower, "sign in") ||
			strings.Contains(outputLower, "not authenticated") ||
			strings.Contains(outputLower, "api key") ||
			strings.Contains(outputLower, "unauthorized") ||
			strings.Contains(outputLower, "credentials")

		if isAuthError {
			status.status = "auth_needed"
			status.message = "Open Claude CLI and auth (run 'claude auth') then run '/auxly-init'"
		} else if runErr != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
		} else {
			status.status = "success"
			status.message = "✓ Synced successfully! (auto-migrated)"
		}

		return onboardAgentProgressMsg{status: status}
	}
}

func (m wizardModel) onboardGeminiCmd() tea.Cmd {
	return func() tea.Msg {
		memPath, _ := filepath.Abs(m.memoryPath)
		status := agentOnboardStatus{name: "Gemini CLI", isCLI: true}
		geminiPath, err := lookPath("gemini")
		if err != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
			return onboardAgentProgressMsg{status: status}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, geminiPath, "--skip-trust", "--approval-mode", "yolo", "-m", "gemini-2.5-flash", "-p", "/auxly-init")
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "AUXLY_MEMORY_PATH="+memPath)
		outBytes, runErr := cmd.CombinedOutput()
		output := string(outBytes)

		outputLower := strings.ToLower(output)
		isAuthError := strings.Contains(outputLower, "login") ||
			strings.Contains(outputLower, "auth") ||
			strings.Contains(outputLower, "sign in") ||
			strings.Contains(outputLower, "api key") ||
			strings.Contains(outputLower, "unauthorized") ||
			strings.Contains(outputLower, "credentials")

		if isAuthError {
			status.status = "auth_needed"
			status.message = "Open Gemini CLI and auth (run 'gemini login') then run '/auxly-init'"
		} else if runErr != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
		} else {
			status.status = "success"
			status.message = "✓ Synced successfully! (auto-migrated)"
		}

		return onboardAgentProgressMsg{status: status}
	}
}

func (m wizardModel) onboardCodexCmd() tea.Cmd {
	return func() tea.Msg {
		memPath, _ := filepath.Abs(m.memoryPath)
		status := agentOnboardStatus{name: "Codex CLI", isCLI: true}
		codexPath, err := lookPath("codex")
		if err != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
			return onboardAgentProgressMsg{status: status}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, codexPath, "exec", "--skip-git-repo-check", "/auxly-init")
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "AUXLY_MEMORY_PATH="+memPath)
		outBytes, runErr := cmd.CombinedOutput()
		output := string(outBytes)

		outputLower := strings.ToLower(output)
		isAuthError := strings.Contains(outputLower, "login") ||
			strings.Contains(outputLower, "auth") ||
			strings.Contains(outputLower, "sign in") ||
			strings.Contains(outputLower, "api key") ||
			strings.Contains(outputLower, "unauthorized") ||
			strings.Contains(outputLower, "credentials")

		if isAuthError {
			status.status = "auth_needed"
			status.message = "Open Codex CLI and auth (run 'codex login') then run '/auxly-init'"
		} else if runErr != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
		} else {
			status.status = "success"
			status.message = "✓ Synced successfully! (auto-migrated)"
		}

		return onboardAgentProgressMsg{status: status}
	}
}

func (m wizardModel) onboardCopilotCmd() tea.Cmd {
	return func() tea.Msg {
		memPath, _ := filepath.Abs(m.memoryPath)
		status := agentOnboardStatus{name: "GitHub Copilot CLI", isCLI: true}
		copilotPath, err := lookPath("copilot")
		if err != nil {
			// CLI not on PATH (e.g. wired by config dir only) — the MCP server is
			// registered; the user runs onboarding in an interactive session.
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
			return onboardAgentProgressMsg{status: status}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		// Non-interactive run. Copilot doesn't read Claude-style slash skills, so we
		// name the MCP tool explicitly; --allow-all-tools lets it call the tool and
		// write memory without an approval prompt in this headless run.
		prompt := "Use the auxly_skill_init tool now to run Auxly memory onboarding, then follow its instructions and call auxly_skill_sync to save any facts about me from this context."
		cmd := exec.CommandContext(ctx, copilotPath, "-p", prompt, "--allow-all-tools")
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "AUXLY_MEMORY_PATH="+memPath)
		outBytes, runErr := cmd.CombinedOutput()
		outputLower := strings.ToLower(string(outBytes))

		isAuthError := strings.Contains(outputLower, "login") ||
			strings.Contains(outputLower, "sign in") ||
			strings.Contains(outputLower, "not logged in") ||
			strings.Contains(outputLower, "unauthorized") ||
			strings.Contains(outputLower, "authenticate")

		if isAuthError {
			status.status = "auth_needed"
			status.message = "Open GitHub Copilot CLI and sign in ('copilot'), then run '/auxly-init'"
		} else if runErr != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
		} else {
			status.status = "success"
			status.message = "✓ Synced successfully! (auto-migrated)"
		}

		return onboardAgentProgressMsg{status: status}
	}
}

func (m wizardModel) onboardAntigravityCmd() tea.Cmd {
	return func() tea.Msg {
		memPath, _ := filepath.Abs(m.memoryPath)
		status := agentOnboardStatus{name: "Antigravity CLI", isCLI: true}
		agyPath, err := lookPath("agy")
		if err != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
			return onboardAgentProgressMsg{status: status}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, agyPath, "-p", "/auxly-init")
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "AUXLY_MEMORY_PATH="+memPath)
		outBytes, runErr := cmd.CombinedOutput()
		output := string(outBytes)

		outputLower := strings.ToLower(output)
		isAuthError := strings.Contains(outputLower, "login") ||
			strings.Contains(outputLower, "auth") ||
			strings.Contains(outputLower, "sign in") ||
			strings.Contains(outputLower, "api key") ||
			strings.Contains(outputLower, "unauthorized") ||
			strings.Contains(outputLower, "credentials")

		if isAuthError {
			status.status = "auth_needed"
			status.message = "Open Antigravity CLI and authenticate, then run '/auxly-init'"
		} else if runErr != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
		} else {
			status.status = "success"
			status.message = "✓ Synced successfully! (auto-migrated)"
		}

		return onboardAgentProgressMsg{status: status}
	}
}

// onboardCursorCmd drives the Cursor Agent CLI (`cursor-agent`) headlessly. The
// MCP server is already written to ~/.cursor/mcp.json and approved by `auxly
// setup`; --approve-mcps makes the headless run load it too, and -p runs the
// /auxly-init sync non-interactively.
func (m wizardModel) onboardCursorCmd() tea.Cmd {
	return func() tea.Msg {
		memPath, _ := filepath.Abs(m.memoryPath)
		status := agentOnboardStatus{name: "Cursor CLI", isCLI: true}
		cursorPath, err := lookPath("cursor-agent")
		if err != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
			return onboardAgentProgressMsg{status: status}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// -f trusts the workspace (headless runs otherwise stop at a "Workspace
		// Trust Required" gate and exit non-zero); --approve-mcps loads the server
		// for this run even if the persistent allowlist write hasn't landed.
		cmd := exec.CommandContext(ctx, cursorPath, "-p", "-f", "--approve-mcps", "/auxly-init")
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "AUXLY_MEMORY_PATH="+memPath)
		outBytes, runErr := cmd.CombinedOutput()
		output := strings.ToLower(string(outBytes))

		isAuthError := strings.Contains(output, "login") ||
			strings.Contains(output, "auth") ||
			strings.Contains(output, "sign in") ||
			strings.Contains(output, "api key") ||
			strings.Contains(output, "unauthorized") ||
			strings.Contains(output, "credentials")

		if isAuthError {
			status.status = "auth_needed"
			status.message = "Open Cursor CLI and auth (run 'cursor-agent login') then run '/auxly-init'"
		} else if runErr != nil {
			status.status = "success"
			status.message = "✓ MCP registered! Type '/auxly-init' in active chat to sync."
		} else {
			status.status = "success"
			status.message = "✓ Synced successfully! (auto-migrated)"
		}

		return onboardAgentProgressMsg{status: status}
	}
}

func (m wizardModel) runMigration() tea.Cmd {
	memPath := m.memoryPath
	return func() tea.Msg {
		var log []string
		count := 0

		if err := os.MkdirAll(memPath, 0755); err != nil {
			return migrationDoneMsg{count: 0, log: []string{"✗ " + err.Error()}}
		}
		os.MkdirAll(filepath.Join(memPath, ".pending"), 0755)

		entries, err := templates.FS.ReadDir(".")
		if err != nil {
			return migrationDoneMsg{count: 0, log: []string{"✗ " + err.Error()}}
		}

		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == "embed.go" {
				continue
			}
			destPath := filepath.Join(memPath, entry.Name())
			if _, err := os.Stat(destPath); err == nil {
				continue
			}
			data, err := templates.FS.ReadFile(entry.Name())
			if err != nil {
				continue
			}
			time.Sleep(50 * time.Millisecond)
			if err := os.WriteFile(destPath, data, 0644); err != nil {
				continue
			}
			count++
			log = append(log, entry.Name())
		}

		auditPath := filepath.Join(memPath, ".audit.log")
		if _, err := os.Stat(auditPath); os.IsNotExist(err) {
			os.WriteFile(auditPath, []byte{}, 0644)
			count++
			log = append(log, ".audit.log")
		}

		time.Sleep(200 * time.Millisecond)
		return migrationDoneMsg{count: count, log: log}
	}
}

func (m wizardModel) spinner() string {
	frames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	return frames[m.spinFrame%len(frames)]
}

func (m wizardModel) View() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("038"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("34"))
	bold := lipgloss.NewStyle().Bold(true)

	var b strings.Builder

	b.WriteString(renderBanner(m.width))
	b.WriteString("\n")

	// Step indicator
	steps := []string{"Location", "Scan", "Agents", "Migrate", "Setup", "Done"}
	stepIdx := int(m.step)
	b.WriteString("  ")
	if m.width > 0 && m.width < 75 {
		// Render compact step indicator to avoid line wrapping on narrow terminals
		b.WriteString(cyan.Render(fmt.Sprintf("Step %d/%d: %s", stepIdx+1, len(steps), steps[stepIdx])))
	} else {
		for i, s := range steps {
			if i == stepIdx {
				b.WriteString(cyan.Render(fmt.Sprintf("[ %s ]", s)))
			} else if i < stepIdx {
				b.WriteString(green.Render(fmt.Sprintf("✓ %s", s)))
			} else {
				b.WriteString(dim.Render(s))
			}
			if i < len(steps)-1 {
				b.WriteString(dim.Render(" ─── "))
			}
		}
	}
	b.WriteString("\n\n")

	switch m.step {
	case wizardStepInstallType:
		b.WriteString(bold.Render("  Where should Auxly store unified memory?"))
		b.WriteString("\n\n")

		options := []struct {
			label string
			desc  string
			path  string
		}{
			{"Global (recommended)", "All agents share one memory across all projects", "~/.auxly/memory/"},
			{"Project-local", "Memory scoped to current directory only", "./.auxly/memory/"},
		}

		for i, opt := range options {
			if i == m.cursor {
				b.WriteString(fmt.Sprintf("  %s %s\n", cyan.Render("▸"), bold.Render(opt.label)))
				b.WriteString(fmt.Sprintf("    %s\n", dim.Render(opt.desc)))
				b.WriteString(fmt.Sprintf("    %s\n\n", cyan.Render("→ "+opt.path)))
			} else {
				b.WriteString(fmt.Sprintf("    %s\n", dim.Render(opt.label)))
				b.WriteString(fmt.Sprintf("    %s\n\n", dim.Render(opt.desc)))
			}
		}
		b.WriteString("\n")
		b.WriteString(dim.Render("  ↑/↓ move • Enter select • q quit"))
	case wizardStepDetecting:
		b.WriteString(fmt.Sprintf("  %s %s\n\n",
			cyan.Render(m.spinner()),
			bold.Render("Scanning your system for AI agents..."),
		))

		var cursorDisp string
		if runtime.GOOS == "darwin" {
			cursorDisp = "~/Library/Application Support/Cursor/"
		} else {
			cursorDisp = "~/.config/Cursor/"
		}

		scanDirs := []string{
			"~/Library/Application Support/Claude/",
			"~/Library/Application Support/com.openai.chat/",
			cursorDisp,
			"~/.claude/",
			"~/.gemini/",
			"~/.codex/",
			"/opt/homebrew/bin/",
		}
		shown := m.spinFrame / 3
		if shown > len(scanDirs) {
			shown = len(scanDirs)
		}

		pBar := RenderProgressBar(shown, len(scanDirs), 40, ColorPrimary)
		b.WriteString("  " + pBar + "\n\n")

		for i := 0; i < shown; i++ {
			b.WriteString(fmt.Sprintf("    %s %s\n", dim.Render("↳"), dim.Render(scanDirs[i])))
		}

	case wizardStepAgentSelect:
		selected := 0
		for _, a := range m.agents {
			if a.selected {
				selected++
			}
		}
		b.WriteString(fmt.Sprintf("  %s Scanned %d directories · %d folders · %d files\n",
			green.Render("✓"), len(m.scanResults), m.totalFolders, m.totalFiles))
		b.WriteString(fmt.Sprintf("  %s Found %d agents\n\n",
			green.Render("✓"), len(m.agents)))

		b.WriteString(bold.Render("  Select agents to connect:"))
		b.WriteString(fmt.Sprintf(" %s\n\n", dim.Render(fmt.Sprintf("(%d selected)", selected))))

		home, _ := os.UserHomeDir()
		for i, a := range m.agents {
			cursor := " "
			if i == m.cursor {
				cursor = cyan.Render("▸")
			}
			check := dim.Render("○")
			if a.selected {
				check = green.Render("●")
			}

			shortPath := strings.Replace(a.agent.Path, home, "~", 1)
			b.WriteString(fmt.Sprintf("  %s %s %-18s %s\n",
				cursor, check,
				a.agent.Name,
				dim.Render(shortPath),
			))
		}

		// Select All
		cursor := " "
		if m.cursor == len(m.agents) {
			cursor = cyan.Render("▸")
		}
		allSelected := true
		for _, a := range m.agents {
			if !a.selected {
				allSelected = false
				break
			}
		}
		allCheck := dim.Render("○")
		if allSelected {
			allCheck = green.Render("●")
		}
		b.WriteString(fmt.Sprintf("\n  %s %s %s\n", cursor, allCheck, bold.Render("Select All")))

		b.WriteString("\n")
		b.WriteString(dim.Render("  ↑/↓ move • Space toggle • a all • Enter continue"))

	case wizardStepMigrating:
		b.WriteString(fmt.Sprintf("  %s %s\n\n",
			cyan.Render(m.spinner()),
			bold.Render("Creating unified memory..."),
		))

		pBar := RenderProgressBar(len(m.migrationLog), 15, 40, ColorSuccess)
		b.WriteString("  " + pBar + "\n\n")

		for _, name := range m.migrationLog {
			b.WriteString(fmt.Sprintf("    %s %s\n", green.Render("✓"), name))
		}

	case wizardStepSetup:
		b.WriteString(fmt.Sprintf("  %s Created %d memory files\n",
			green.Render("✓"), m.migrated))
		selected := 0
		for _, a := range m.agents {
			if a.selected {
				selected++
			}
		}
		b.WriteString(fmt.Sprintf("  %s Connected %d agents\n\n",
			green.Render("✓"), selected))

		b.WriteString(bold.Render("  Configure MCP connections now?"))
		b.WriteString("\n")
		b.WriteString(dim.Render("  This writes MCP config so agents can access unified memory."))
		b.WriteString("\n\n")

		setupOpts := []struct {
			label string
			desc  string
		}{
			{"Yes, configure now", "Auto-write MCP config for all detected agents"},
			{"Skip for now", "You can run  auxly setup  later"},
		}

		for i, opt := range setupOpts {
			if i == m.cursor {
				b.WriteString(fmt.Sprintf("  %s %s\n", cyan.Render("▸"), bold.Render(opt.label)))
				b.WriteString(fmt.Sprintf("    %s\n\n", dim.Render(opt.desc)))
			} else {
				b.WriteString(fmt.Sprintf("    %s\n", dim.Render(opt.label)))
				b.WriteString(fmt.Sprintf("    %s\n\n", dim.Render(opt.desc)))
			}
		}
		b.WriteString(dim.Render("  ↑/↓ move • Enter select"))

	case wizardStepOnboarding:
		b.WriteString(fmt.Sprintf("  %s %s\n\n",
			cyan.Render(m.spinner()),
			bold.Render("Onboarding selected agents headlessly..."),
		))

		descText := "Running '/auxly-init' on installed CLI agents to synchronize memory vault..."
		b.WriteString("  " + dim.Render(descText) + "\n\n")

		pBar := RenderProgressBar(m.spinFrame%20, 20, 40, ColorPrimary)
		b.WriteString("  " + pBar + "\n\n")

		if len(m.onboardStatuses) > 0 {
			b.WriteString(bold.Render("  🔄 Active Onboarding Progress:") + "\n")
			for _, status := range m.onboardStatuses {
				icon := "●"
				var nameStyle, msgStyle lipgloss.Style

				if status.status == "success" {
					nameStyle = lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
					msgStyle = lipgloss.NewStyle().Foreground(ColorSuccess)
					icon = green.Render("✓")
				} else if status.status == "auth_needed" {
					nameStyle = lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
					msgStyle = lipgloss.NewStyle().Foreground(ColorWarning)
					icon = lipgloss.NewStyle().Foreground(ColorWarning).Render("⚠️")
				} else if status.status == "pending" {
					nameStyle = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
					msgStyle = lipgloss.NewStyle().Foreground(ColorPrimary)
					icon = cyan.Render(m.spinner())
				} else {
					// Manual action
					nameStyle = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
					msgStyle = lipgloss.NewStyle().Foreground(ColorDim)
					icon = cyan.Render("👉")
				}

				b.WriteString(fmt.Sprintf("    %s %-24s %s\n",
					icon,
					nameStyle.Render(status.name),
					msgStyle.Render(status.message),
				))
			}
			b.WriteString("\n")
		}

	case wizardStepDone:
		b.WriteString(fmt.Sprintf("  %s Scanned %d directories (%d folders, %d files)\n",
			green.Render("✓"), len(m.scanResults), m.totalFolders, m.totalFiles))
		b.WriteString(fmt.Sprintf("  %s Created %d memory files\n",
			green.Render("✓"), m.migrated))

		selected := 0
		for _, a := range m.agents {
			if a.selected {
				selected++
			}
		}
		b.WriteString(fmt.Sprintf("  %s Connected %d agents (all trusted)\n",
			green.Render("✓"), selected))

		if m.setupChoice == 0 {
			b.WriteString(fmt.Sprintf("  %s MCP configured\n", green.Render("✓")))
		} else {
			b.WriteString(fmt.Sprintf("  %s MCP setup skipped — run %s anytime\n",
				dim.Render("○"), cyan.Render("auxly setup")))
		}

		b.WriteString("\n")

		// Render E2E Onboarding Results Dashboard
		if m.setupChoice == 0 && len(m.onboardStatuses) > 0 {
			b.WriteString(bold.Render("  🔄 E2E Agent Onboarding Dashboard:") + "\n")
			for _, status := range m.onboardStatuses {
				icon := "●"
				var nameStyle, msgStyle lipgloss.Style

				if status.status == "success" {
					nameStyle = lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
					msgStyle = lipgloss.NewStyle().Foreground(ColorSuccess)
					icon = green.Render("✓")
				} else if status.status == "auth_needed" {
					nameStyle = lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
					msgStyle = lipgloss.NewStyle().Foreground(ColorWarning)
					icon = lipgloss.NewStyle().Foreground(ColorWarning).Render("⚠️")
				} else {
					// Manual action
					nameStyle = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
					msgStyle = lipgloss.NewStyle().Foreground(ColorDim)
					icon = cyan.Render("👉")
				}

				b.WriteString(fmt.Sprintf("    %s %-24s %s\n",
					icon,
					nameStyle.Render(status.name),
					msgStyle.Render(status.message),
				))
			}
			b.WriteString("\n")
		} else {
			// Fallback to simple agent summary if setup skipped
			for _, a := range m.agents {
				b.WriteString(fmt.Sprintf("    %s %-18s %s\n",
					green.Render("●"),
					a.agent.Name,
					dim.Render(a.agent.Connection),
				))
			}
		}

		b.WriteString(fmt.Sprintf("  %s %s\n", dim.Render("Memory:"), cyan.Render(m.memoryPath)))
		b.WriteString("\n")

		boxWidth := 78
		if m.width > 0 && m.width < 82 {
			boxWidth = m.width - 4
		}
		promptStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Padding(1, 2).
			Width(boxWidth)

		b.WriteString(cyan.Render("  🚀 ONBOARD YOUR AGENTS INSTANTLY WITH /auxly-init:") + "\n")

		descText := "Type this command first in any new chat session with your AI assistants:"
		if m.width > 0 && m.width < 90 {
			b.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Width(m.width-4).Render(descText) + "\n\n")
		} else {
			b.WriteString(dim.Render("  "+descText) + "\n\n")
		}

		purple := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
		warnStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		warnBold := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		cyanBold := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)

		b.WriteString(promptStyle.Render(
			purple.Render("/auxly-init") + " (or type 'auxly init' in agent chat)\n\n" +
				"This universal onboarding command trains the agent on your memory preferences,\n" +
				"provides local environment rules, and commands the assistant to immediately\n" +
				"scan your active context and synchronize existing facts to the Auxly vault.\n\n" +
				warnBold.Render("👉 FOR CLAUDE DESKTOP GUI:\n") +
				warnStyle.Render("Please upload the generated skills ZIP files from ") +
				cyanBold.Render("~/Downloads/auxly-skills/") +
				warnStyle.Render(" via\nSettings -> Customize -> Skills -> Upload a skill after updates.") + "\n\n" +
				dim.Render("Supported in: Claude Desktop, Claude Code CLI, Cursor, and Codex IDE."),
		))
		b.WriteString("\n\n")

		b.WriteString(green.Render("  ✓ Auxly is ready!"))
		b.WriteString("\n\n")
		b.WriteString(dim.Render("  Press Enter or q to exit"))
	}

	return b.String()
}

// RunWizard starts the interactive setup wizard.
func RunWizard(memoryPath string) {
	wiz := newWizardModel(memoryPath)
	p := tea.NewProgram(&wiz, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if w, ok := result.(*wizardModel); ok && w.step == wizardStepDone {
		fmt.Println("\n🎉 Onboarding initialization complete!")
		fmt.Println("👉 Run 'auxly' anytime to open the secure memory TUI dashboard.")
	}
}

// IsInitialized checks if Auxly has completed onboarding.
func IsInitialized(memoryPath string) bool {
	markerPath := filepath.Join(memoryPath, ".initialized")
	_, err := os.Stat(markerPath)
	return err == nil
}

func lookPath(binary string) (string, error) {
	checkPath := filepath.Join("/opt/homebrew/bin", binary)
	if _, err := os.Stat(checkPath); err == nil {
		return checkPath, nil
	}
	checkPath = filepath.Join("/usr/local/bin", binary)
	if _, err := os.Stat(checkPath); err == nil {
		return checkPath, nil
	}
	pathDirs := strings.Split(os.Getenv("PATH"), ":")
	for _, dir := range pathDirs {
		p := filepath.Join(dir, binary)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found: %s", binary)
}
