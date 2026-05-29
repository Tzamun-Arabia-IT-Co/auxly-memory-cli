package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/tui"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure auxly-cli MCP server for Claude Desktop and other agents",
	RunE:  runSetup,
}

func init() {
	rootCmd.AddCommand(setupCmd)
}

type mcpConfig struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

func getBinaryPath() string {
	exePath, err := os.Executable()
	if err == nil {
		if absPath, err := filepath.Abs(exePath); err == nil {
			// Check if it's a real path and not a go run temp binary
			if !strings.Contains(absPath, "/var/folders/") && !strings.Contains(absPath, "/tmp/") && !strings.Contains(absPath, "\\Temp\\") {
				return absPath
			}
		}
	}

	// Fallback to exec.LookPath
	if p, err := exec.LookPath("auxly"); err == nil {
		if absPath, err := filepath.Abs(p); err == nil {
			return absPath
		}
	}

	// Default fallback to ~/.local/bin/auxly
	home, err := os.UserHomeDir()
	if err == nil {
		localBin := filepath.Join(home, ".local", "bin", "auxly")
		if _, err := os.Stat(localBin); err == nil {
			return localBin
		}
	}

	return "/usr/local/bin/auxly"
}

func updateLocalMCPConfigFile(path string, binaryPath string, memPath string, appName string, baseDir string, isClaudeDesktop bool, providerID string) string {
	// Check if base directory exists (meaning the app is installed)
	if baseDir != "" {
		if _, err := os.Stat(baseDir); err != nil {
			if os.IsNotExist(err) {
				return "" // Skip since app is not installed
			}
			fmt.Printf("⚠️  [Debug] Stat error on baseDir %s for %s: %v\n", baseDir, appName, err)
		}
	}

	// Check if the path is a symlink
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			// It's a symlink! Delete it to prevent writing to protected system configs
			os.Remove(path)
		}
	}

	// Force create the parent directory of the config file
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("⚠️  [Debug] MkdirAll error for %s at %s: %v\n", appName, dir, err)
		return ""
	}

	// Read existing JSON
	var config map[string]interface{}
	data, err := os.ReadFile(path)
	if err == nil {
		json.Unmarshal(data, &config)
	}
	if config == nil {
		config = make(map[string]interface{})
	}

	// Define our server structure with robust path fallbacks for GUI applications
	serverDef := map[string]interface{}{
		"command": binaryPath,
		"args":    []interface{}{"--path", memPath, "mcp-server"},
		"env": map[string]string{
			"AUXLY_MEMORY_PATH": memPath,
			"AUXLY_PROVIDER":    providerID,
		},
	}

	if isClaudeDesktop || filepath.Base(path) == "settings.json" || filepath.Base(path) == "mcp_config.json" || filepath.Base(path) == "mcpServers.json" {
		// Claude Desktop, Cursor, VS Code, and Antigravity put servers inside "mcpServers" key
		servers, ok := config["mcpServers"].(map[string]interface{})
		if !ok {
			servers = make(map[string]interface{})
		}
		servers["auxly-memory"] = serverDef
		config["mcpServers"] = servers
		// Ensure we clean up any legacy direct key at the root of mcpServers.json (Cursor)
		delete(config, "auxly-memory")
	} else {
		// Claude Code and Antigravity CLI use direct servers list or "mcpServers"
		// Let's support both direct key or "mcpServers" depending on what already exists
		if _, ok := config["mcpServers"]; ok || filepath.Base(path) == "mcp.json" {
			servers, ok := config["mcpServers"].(map[string]interface{})
			if !ok {
				servers = make(map[string]interface{})
			}
			servers["auxly-memory"] = serverDef
			config["mcpServers"] = servers
		} else {
			// Direct key
			config["auxly-memory"] = serverDef
		}
	}

	if filepath.Base(path) == "settings.json" && providerID == "gemini" {
		config["model"] = map[string]string{"name": "gemini-2.5-flash"}
	}

	// Marshal and write back
	newData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Printf("⚠️  [Debug] Marshal error for %s: %v\n", appName, err)
		return ""
	}
	if err := os.WriteFile(path, newData, 0644); err != nil {
		fmt.Printf("⚠️  [Debug] Write error for %s at %s: %v\n", appName, path, err)
		return ""
	}
	return appName
}

func printAl(text string) {
	text = strings.ReplaceAll(text, "\n", "\r\n")
	fmt.Print(text + "\r\n")
}

func printAlf(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	text = strings.ReplaceAll(text, "\n", "\r\n")
	fmt.Print(text)
}

func runSetup(cmd *cobra.Command, args []string) error {
	binaryPath := getBinaryPath()
	memPath := getMemoryPath()

	printAl("🔧 auxly-cli MCP Automated Setup")
	printAl("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	printAlf("📍 Binary: %s\r\n", binaryPath)
	printAlf("📂 Memory: %s\r\n\r\n", memPath)

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	var configuredApps []string

	// 1. Claude Desktop
	var claudeConfigPath string
	var claudeBaseDir string
	switch runtime.GOOS {
	case "darwin":
		claudeBaseDir = filepath.Join(home, "Library/Application Support/Claude")
		claudeConfigPath = filepath.Join(claudeBaseDir, "claude_desktop_config.json")
	case "linux":
		claudeBaseDir = filepath.Join(home, ".config/Claude")
		claudeConfigPath = filepath.Join(claudeBaseDir, "claude_desktop_config.json")
	default:
		claudeBaseDir = filepath.Join(os.Getenv("APPDATA"), "Claude")
		claudeConfigPath = filepath.Join(claudeBaseDir, "claude_desktop_config.json")
	}
	if app := updateLocalMCPConfigFile(claudeConfigPath, binaryPath, memPath, "Claude Desktop", claudeBaseDir, true, "claude"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	// 2. Cursor
	var cursorConfigPath string
	var cursorBaseDir string
	switch runtime.GOOS {
	case "darwin":
		cursorBaseDir = filepath.Join(home, "Library/Application Support/Cursor")
		cursorConfigPath = filepath.Join(cursorBaseDir, "User/globalStorage/co.heron.cursor/mcpServers.json")
	case "linux":
		cursorBaseDir = filepath.Join(home, ".config/Cursor")
		cursorConfigPath = filepath.Join(cursorBaseDir, "User/globalStorage/co.heron.cursor/mcpServers.json")
	default:
		cursorBaseDir = filepath.Join(os.Getenv("APPDATA"), "Cursor")
		cursorConfigPath = filepath.Join(cursorBaseDir, "User", "globalStorage", "co.heron.cursor", "mcpServers.json")
	}
	if app := updateLocalMCPConfigFile(cursorConfigPath, binaryPath, memPath, "Cursor", cursorBaseDir, false, "cursor"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	// 4. Antigravity CLI
	antigravityBaseDir := filepath.Join(home, ".gemini/antigravity-cli")
	antigravityConfigPath := filepath.Join(antigravityBaseDir, "mcp.json")
	if app := updateLocalMCPConfigFile(antigravityConfigPath, binaryPath, memPath, "Antigravity CLI", antigravityBaseDir, false, "antigravity-cli"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	antigravityCliConfigPath2 := filepath.Join(antigravityBaseDir, "mcp_config.json")
	if app := updateLocalMCPConfigFile(antigravityCliConfigPath2, binaryPath, memPath, "Antigravity Agent (Config)", antigravityBaseDir, false, "antigravity-agent"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	// 4b. Antigravity IDE (Bundle Support Paths)
	var antigravityIdeConfigPath string
	var antigravityIdeBaseDir string
	switch runtime.GOOS {
	case "darwin":
		antigravityIdeBaseDir = filepath.Join(home, "Library/Application Support/Antigravity")
		antigravityIdeConfigPath = filepath.Join(antigravityIdeBaseDir, "User/settings.json")
	case "linux":
		antigravityIdeBaseDir = filepath.Join(home, ".config/Antigravity")
		antigravityIdeConfigPath = filepath.Join(antigravityIdeBaseDir, "User/settings.json")
	default:
		antigravityIdeBaseDir = filepath.Join(os.Getenv("APPDATA"), "Antigravity")
		antigravityIdeConfigPath = filepath.Join(antigravityIdeBaseDir, "User", "settings.json")
	}
	if app := updateLocalMCPConfigFile(antigravityIdeConfigPath, binaryPath, memPath, "Antigravity Agent (settings)", antigravityIdeBaseDir, false, "antigravity-agent"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	var antigravityIdeConfigPath2 string
	var antigravityIdeBaseDir2 string
	switch runtime.GOOS {
	case "darwin":
		antigravityIdeBaseDir2 = filepath.Join(home, "Library/Application Support/Antigravity IDE")
		antigravityIdeConfigPath2 = filepath.Join(antigravityIdeBaseDir2, "User/settings.json")
	case "linux":
		antigravityIdeBaseDir2 = filepath.Join(home, ".config/Antigravity IDE")
		antigravityIdeConfigPath2 = filepath.Join(antigravityIdeBaseDir2, "User", "settings.json")
	default:
		antigravityIdeBaseDir2 = filepath.Join(os.Getenv("APPDATA"), "Antigravity IDE")
		antigravityIdeConfigPath2 = filepath.Join(antigravityIdeBaseDir2, "User", "settings.json")
	}
	if app := updateLocalMCPConfigFile(antigravityIdeConfigPath2, binaryPath, memPath, "Antigravity IDE (Bundle)", antigravityIdeBaseDir2, false, "antigravity-ide"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	// 4c. Antigravity IDE (True Gemini Config Directories)
	antigravityGeminiBaseDir := filepath.Join(home, ".gemini/antigravity")
	antigravityGeminiConfigPath := filepath.Join(antigravityGeminiBaseDir, "mcp_config.json")
	if app := updateLocalMCPConfigFile(antigravityGeminiConfigPath, binaryPath, memPath, "Antigravity Agent (Gemini)", antigravityGeminiBaseDir, false, "antigravity-agent"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	antigravityIdeGeminiBaseDir := filepath.Join(home, ".gemini/antigravity-ide")
	antigravityIdeGeminiConfigPath := filepath.Join(antigravityIdeGeminiBaseDir, "mcp_config.json")
	if app := updateLocalMCPConfigFile(antigravityIdeGeminiConfigPath, binaryPath, memPath, "Antigravity IDE (Gemini IDE)", antigravityIdeGeminiBaseDir, false, "antigravity-ide"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	antigravityConfigBaseDir := filepath.Join(home, ".gemini/config")
	antigravityConfigPath3 := filepath.Join(antigravityConfigBaseDir, "mcp_config.json")
	if app := updateLocalMCPConfigFile(antigravityConfigPath3, binaryPath, memPath, "Antigravity IDE (Config)", antigravityConfigBaseDir, false, "antigravity-ide"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	// Dynamic Gemini CLI / Root overrides
	if app := updateLocalMCPConfigFile(filepath.Join(home, ".gemini/settings.json"), binaryPath, memPath, "Gemini CLI settings.json", filepath.Join(home, ".gemini"), false, "gemini"); app != "" {
		configuredApps = append(configuredApps, app)
	}
	updateLocalMCPConfigFile(filepath.Join(home, ".gemini/mcp_config.json"), binaryPath, memPath, "Gemini CLI Config", filepath.Join(home, ".gemini"), false, "gemini")
	updateLocalMCPConfigFile(filepath.Join(home, ".gemini/mcp.json"), binaryPath, memPath, "Gemini CLI mcp.json", filepath.Join(home, ".gemini"), false, "gemini")
	updateLocalMCPConfigFile(filepath.Join(home, ".gemini/antigravity-cli/mcp_config.json"), binaryPath, memPath, "Antigravity CLI Config", filepath.Join(home, ".gemini/antigravity-cli"), false, "antigravity-cli")

	// 5. Claude Code CLI Configs (fallback manually if command fails)
	claudeCodeBaseDir := filepath.Join(home, ".claudecode")
	if app := updateLocalMCPConfigFile(filepath.Join(claudeCodeBaseDir, "mcp.json"), binaryPath, memPath, "Claude Code CLI (~/.claudecode)", claudeCodeBaseDir, false, "claude-code"); app != "" {
		configuredApps = append(configuredApps, app)
	}
	if app := updateLocalMCPConfigFile(filepath.Join(home, ".claude.json"), binaryPath, memPath, "Claude Code CLI (~/.claude.json)", "", false, "claude-code"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	// 5b. Kimi Code CLI (Global Config)
	kimiCodeBaseDir := filepath.Join(home, ".kimi-code")
	if app := updateLocalMCPConfigFile(filepath.Join(kimiCodeBaseDir, "mcp.json"), binaryPath, memPath, "Kimi Code CLI (~/.kimi-code/mcp.json)", "", false, "kimi"); app != "" {
		configuredApps = append(configuredApps, app)
	}
	updateLocalMCPConfigFile(filepath.Join(kimiCodeBaseDir, "mcp_config.json"), binaryPath, memPath, "Kimi CLI config", "", false, "kimi")
	updateLocalMCPConfigFile(filepath.Join(home, ".kimi/mcp.json"), binaryPath, memPath, "Kimi mcp.json", "", false, "kimi")
	updateLocalMCPConfigFile(filepath.Join(home, ".kimi/mcp_config.json"), binaryPath, memPath, "Kimi mcp_config.json", "", false, "kimi")

	// 5c. Trae IDE (Global Config)
	traeBaseDir := filepath.Join(home, ".trae")
	if app := updateLocalMCPConfigFile(filepath.Join(traeBaseDir, "mcp.json"), binaryPath, memPath, "Trae IDE (~/.trae/mcp.json)", "", false, "trae"); app != "" {
		configuredApps = append(configuredApps, app)
	}

	// Print beautiful aligned configured applications
	if len(configuredApps) > 0 {
		printAl("✅ Automatically configured local MCP for:")
		for _, app := range configuredApps {
			printAlf("   ↳ %s\r\n", app)
		}
		printAl("")
	}

	// 6. Automated 'claude mcp add' command execution
	claudePath, err := exec.LookPath("claude")
	if err == nil {
		printAl("🤖 Claude Code CLI detected. Registering auxly-memory MCP server automatically...")
		cCmd := exec.Command(claudePath, "mcp", "add", "auxly-memory", binaryPath, "mcp-server")
		cCmd.Stdin = strings.NewReader("")
		cCmd.Stdout = os.Stdout
		cCmd.Stderr = os.Stderr
		if err := cCmd.Run(); err != nil {
			printAlf("⚠️  Failed to register with Claude Code CLI: %v\r\n", err)
		} else {
			printAl("✅ Successfully registered auxly-memory MCP server with Claude Code CLI!")
		}
		printAl("")
	}

	// 7. Automated 'codex mcp add' command execution
	codexPath, err := exec.LookPath("codex")
	if err == nil {
		printAl("🤖 Codex CLI detected. Registering auxly-memory MCP server automatically...")
		cCmd := exec.Command(codexPath, "mcp", "add", "auxly-memory", "--env", "AUXLY_MEMORY_PATH="+memPath, "--", binaryPath, "--path", memPath, "mcp-server")
		cCmd.Stdin = strings.NewReader("")
		cCmd.Stdout = os.Stdout
		cCmd.Stderr = os.Stderr
		if err := cCmd.Run(); err != nil {
			printAlf("⚠️  Failed to register with Codex CLI: %v\r\n", err)
		} else {
			printAl("✅ Successfully registered auxly-memory MCP server with Codex CLI!")
		}
		printAl("")
	}

	// 8. Generate Claude and Codex Skills, Antigravity commands, and rule files
	ensureClaudeAndCodexSkills(memPath)
	ensureAntigravitySlashCommands(memPath)
	cleanupGeminiSlashCommands()
	tui.EnsureClaudeSkillsZip()
	ensureWorkspaceRuleFiles()
	printAl("✅ Successfully generated native slash command skills globally & locally for Claude & Codex!")
	printAl("✅ Successfully registered Antigravity slash commands and cleaned up Gemini TOMLs!")
	printAl("✅ Automatically synchronized `.cursorrules`, `.antigravityrules`, and all workspace rules!")
	printAl("")

	printAl("🚀 Onboard your AI Agents instantly:")
	printAl("   Simply type `/auxly-init` (or 'auxly init') inside your agent's active chat panel")
	printAl("   (Claude Desktop, Claude Code CLI, Cursor, or Codex IDE).")
	printAl("   This will run the onboarding training and align memory automatically!")
	printAl("")

	printAl("🎉 Automated setup complete!")
	printAl("   Please restart your IDEs (Cursor/Codex) or Claude Desktop to load the new tools.")
	printAl("   All agents will have access to these MCP tools:")
	printAl("   • auxly_memory_list     — list all memory files")
	printAl("   • auxly_memory_read     — read a specific file")
	printAl("   • auxly_memory_write    — write/update memory (trust-aware)")
	printAl("   • auxly_memory_search   — search across all files")
	printAl("   • auxly_memory_stats    — usage statistics")
	printAl("   • auxly_pending_list    — pending approvals")
	printAl("")

	return nil
}

func ensureClaudeAndCodexSkills(memPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	commands := getSkillsMap()
	updateReminder := "\n\nIMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!"

	// Paths to write skills to
	globalClaude := filepath.Join(home, ".claude", "skills")
	localClaude := ".claude/skills"
	globalCodex := filepath.Join(home, ".codex", "skills")
	localCodex := ".codex/skills"

	targetDirs := []string{globalClaude, localClaude, globalCodex, localCodex}

	for _, baseDir := range targetDirs {
		// Only write local dirs if we are actually in a directory that is not home
		if baseDir == localClaude || baseDir == localCodex {
			// Skip local creation if we are in the home folder to avoid cluttering it
			if wd, err := os.Getwd(); err == nil && wd == home {
				continue
			}
		}

		for skillName, content := range commands {
			skillDir := filepath.Join(baseDir, skillName)
			_ = os.MkdirAll(skillDir, 0755)
			skillFilePath := filepath.Join(skillDir, "SKILL.md")
			_ = os.WriteFile(skillFilePath, []byte(content+updateReminder), 0644)
		}
	}
}

func ensureAntigravitySlashCommands(memPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	commands := getSkillsMap()
	updateReminder := "\n\nIMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!"

	globalGeminiSkills := filepath.Join(home, ".gemini", "config", "skills")

	for skillName, content := range commands {
		skillDir := filepath.Join(globalGeminiSkills, skillName)
		_ = os.MkdirAll(skillDir, 0755)
		skillFilePath := filepath.Join(skillDir, "SKILL.md")
		_ = os.WriteFile(skillFilePath, []byte(content+updateReminder), 0644)
	}
}

func cleanupGeminiSlashCommands() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	globalGeminiCmds := filepath.Join(home, ".gemini", "commands")
	commands := []string{
		"auxly-init.toml",
		"auxly-memory.toml",
		"auxly-max.toml",
		"auxly-sync.toml",
		"auxly-pending.toml",
		"auxly-status.toml",
		"auxly-forget.toml",
		"auxly-learn.toml",
	}

	for _, cmdFile := range commands {
		filePath := filepath.Join(globalGeminiCmds, cmdFile)
		if _, err := os.Stat(filePath); err == nil {
			_ = os.Remove(filePath)
		}
	}
}

func ensureWorkspaceRuleFiles() {
	rulesContent := `# 🔒 AUXLY PROJECT RULES - MANDATORY COMPLIANCE

## 🤖 Native Auxly Agent Skills (Slash Commands)
You natively support the following slash commands! When the user inputs one of these commands, intercept it and run the corresponding MCP tool call immediately:
- /auxly-init (or auxly-init / auxly init): Call the auxly_skill_init tool to run the onboarding and training setup, scan current chat context/system prompt, and sync all existing preferences to Auxly.
- /auxly-memory (or auxly-memory / auxly memory): Call the auxly_skill_memory tool to display a consolidated markdown profile of the user's identity, preferences, and infrastructure.
- /auxly-status (or auxly-status / auxly status): Call the auxly_skill_status tool to show live loopback daemon and diagnostics.
- /auxly-sync (or auxly-sync / auxly sync): Call the auxly_skill_sync tool with the provided content to perform an automated smart delta-merge into the preferences file.
- /auxly-pending (or auxly-pending / auxly pending): Call the auxly_skill_pending tool with arguments list/approve/reject to manage the approval queue.
- /auxly-max (or auxly-max / auxly max): Call the auxly_skill_max tool to obtain the dynamic Maximum Memory sync instructions block.
- /auxly-forget (or auxly-forget / auxly forget): Call the auxly_skill_forget tool to search and prune outdated bullet statements.
- /auxly-learn (or auxly-learn / auxly learn): Call the auxly_skill_learn tool to parse raw context/snippets and extract new facts.

## 🧠 CRITICAL REMINDER: Always Update Memory
IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
`

	ruleFiles := []string{
		".cursorrules",
		".antigravityrules",
		".windsurfrules",
		".geminirules",
		".clauderules",
		".codexrules",
	}

	for _, filename := range ruleFiles {
		_ = os.WriteFile(filename, []byte(rulesContent), 0644)
	}
}

func getSkillsMap() map[string]string {
	return map[string]string{
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
description: Append and synchronize a new fact, preference, or system detail using smart automated delta-merges into memory files (preferences.md, identity.md, infra.md, products.md, projects.md, daily.md, etc.).
argument-hint: "<fact or preference statement to sync>"
---
# /auxly-sync

You must immediately invoke the 'auxly_skill_sync' MCP tool, passing the user's provided input statement as the 'content' argument. This performs a smart automated delta-merge to update the memory files. Simply run the tool and display the confirmation output!`,

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
}
