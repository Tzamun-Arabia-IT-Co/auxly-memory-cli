package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/tui"
	"github.com/spf13/cobra"
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

// ideTarget describes a single IDE/agent MCP config target.
type ideTarget struct {
	Path            string
	AppName         string
	BaseDir         string
	IsClaudeDesktop bool
	ProviderID      string
}

// knownIDETargets returns every IDE/agent config target that auxly setup writes,
// with OS-specific path construction resolved for the given home directory.
func knownIDETargets(home string) []ideTarget {
	var targets []ideTarget

	// 1. Claude Desktop
	var claudeConfigPath, claudeBaseDir string
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
	targets = append(targets, ideTarget{claudeConfigPath, "Claude Desktop", claudeBaseDir, true, "claude"})

	// 2. Cursor
	var cursorConfigPath, cursorBaseDir string
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
	targets = append(targets, ideTarget{cursorConfigPath, "Cursor IDE", cursorBaseDir, false, "cursor"})

	// 2b. Cursor CLI (cursor-agent) reads ~/.cursor/mcp.json (separate from the
	// IDE's globalStorage config). Without this, `cursor-agent` never sees the server.
	cursorCLIBase := filepath.Join(home, ".cursor")
	cursorCLIConfig := filepath.Join(cursorCLIBase, "mcp.json")
	targets = append(targets, ideTarget{cursorCLIConfig, "Cursor CLI", cursorCLIBase, false, "cursor"})

	// 4. Antigravity CLI
	antigravityBaseDir := filepath.Join(home, ".gemini/antigravity-cli")
	targets = append(targets, ideTarget{filepath.Join(antigravityBaseDir, "mcp.json"), "Antigravity CLI", antigravityBaseDir, false, "antigravity-cli"})
	targets = append(targets, ideTarget{filepath.Join(antigravityBaseDir, "mcp_config.json"), "Antigravity Agent (Config)", antigravityBaseDir, false, "antigravity-agent"})

	// 4b. Antigravity IDE (Bundle Support Paths)
	var antigravityIdeConfigPath, antigravityIdeBaseDir string
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
	targets = append(targets, ideTarget{antigravityIdeConfigPath, "Antigravity Agent (settings)", antigravityIdeBaseDir, false, "antigravity-agent"})

	var antigravityIdeConfigPath2, antigravityIdeBaseDir2 string
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
	targets = append(targets, ideTarget{antigravityIdeConfigPath2, "Antigravity IDE (Bundle)", antigravityIdeBaseDir2, false, "antigravity-ide"})

	// 4c. Antigravity IDE (True Gemini Config Directories)
	antigravityGeminiBaseDir := filepath.Join(home, ".gemini/antigravity")
	targets = append(targets, ideTarget{filepath.Join(antigravityGeminiBaseDir, "mcp_config.json"), "Antigravity Agent (Gemini)", antigravityGeminiBaseDir, false, "antigravity-agent"})

	antigravityIdeGeminiBaseDir := filepath.Join(home, ".gemini/antigravity-ide")
	targets = append(targets, ideTarget{filepath.Join(antigravityIdeGeminiBaseDir, "mcp_config.json"), "Antigravity IDE (Gemini IDE)", antigravityIdeGeminiBaseDir, false, "antigravity-ide"})

	antigravityConfigBaseDir := filepath.Join(home, ".gemini/config")
	targets = append(targets, ideTarget{filepath.Join(antigravityConfigBaseDir, "mcp_config.json"), "Antigravity IDE (Config)", antigravityConfigBaseDir, false, "antigravity-ide"})

	// Dynamic Gemini CLI / Root overrides
	geminiBaseDir := filepath.Join(home, ".gemini")
	targets = append(targets, ideTarget{filepath.Join(home, ".gemini/settings.json"), "Gemini CLI settings.json", geminiBaseDir, false, "gemini"})
	targets = append(targets, ideTarget{filepath.Join(home, ".gemini/mcp_config.json"), "Gemini CLI Config", geminiBaseDir, false, "gemini"})
	targets = append(targets, ideTarget{filepath.Join(home, ".gemini/mcp.json"), "Gemini CLI mcp.json", geminiBaseDir, false, "gemini"})
	targets = append(targets, ideTarget{filepath.Join(home, ".gemini/antigravity-cli/mcp_config.json"), "Antigravity CLI Config", antigravityBaseDir, false, "antigravity-cli"})

	// 5. Claude Code CLI Configs (fallback manually if command fails)
	claudeCodeBaseDir := filepath.Join(home, ".claudecode")
	targets = append(targets, ideTarget{filepath.Join(claudeCodeBaseDir, "mcp.json"), "Claude Code CLI (~/.claudecode)", claudeCodeBaseDir, false, "claude-code"})
	targets = append(targets, ideTarget{filepath.Join(home, ".claude.json"), "Claude Code CLI (~/.claude.json)", "", false, "claude-code"})

	// 5b. Kimi Code CLI (Global Config)
	kimiCodeBaseDir := filepath.Join(home, ".kimi-code")
	targets = append(targets, ideTarget{filepath.Join(kimiCodeBaseDir, "mcp.json"), "Kimi Code CLI (~/.kimi-code/mcp.json)", "", false, "kimi"})
	targets = append(targets, ideTarget{filepath.Join(kimiCodeBaseDir, "mcp_config.json"), "Kimi CLI config", "", false, "kimi"})
	targets = append(targets, ideTarget{filepath.Join(home, ".kimi/mcp.json"), "Kimi mcp.json", "", false, "kimi"})
	targets = append(targets, ideTarget{filepath.Join(home, ".kimi/mcp_config.json"), "Kimi mcp_config.json", "", false, "kimi"})

	// 5c. Trae IDE (Global Config)
	traeBaseDir := filepath.Join(home, ".trae")
	targets = append(targets, ideTarget{filepath.Join(traeBaseDir, "mcp.json"), "Trae IDE (~/.trae/mcp.json)", "", false, "trae"})

	return targets
}

// localServerDef builds the MCP server definition for a locally-installed auxly binary.
func localServerDef(binaryPath, memPath, providerID string) map[string]interface{} {
	return map[string]interface{}{
		"command": binaryPath,
		"args":    []interface{}{"--path", memPath, "mcp-server"},
		"env": map[string]string{
			"AUXLY_MEMORY_PATH": memPath,
			"AUXLY_PROVIDER":    providerID,
		},
	}
}

// writeMCPConfigEntry writes serverDef into the target's config file, honoring
// the per-file placement rules. Returns the app name on success, "" on skip/error.
func writeMCPConfigEntry(t ideTarget, serverDef map[string]interface{}) string {
	path := t.Path
	appName := t.AppName

	// Check if base directory exists (meaning the app is installed)
	if t.BaseDir != "" {
		if _, err := os.Stat(t.BaseDir); err != nil {
			if os.IsNotExist(err) {
				return "" // Skip since app is not installed
			}
			fmt.Printf("⚠️  [Debug] Stat error on baseDir %s for %s: %v\n", t.BaseDir, appName, err)
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

	if t.IsClaudeDesktop || filepath.Base(path) == "settings.json" || filepath.Base(path) == "mcp_config.json" || filepath.Base(path) == "mcpServers.json" {
		// Claude Desktop, Cursor, VS Code, and Antigravity put servers inside "mcpServers" key
		servers, ok := config["mcpServers"].(map[string]interface{})
		if !ok {
			servers = make(map[string]interface{})
		}
		servers["auxly-memory"] = serverDef
		config["mcpServers"] = servers
		// Ensure we clean up any legacy direct key at the root of mcpServers.json (Cursor)
		delete(config, "auxly-memory")
	} else if filepath.Base(path) == ".claude.json" {
		// Claude Code (~/.claude.json) keeps BOTH a global "mcpServers" AND
		// per-project "projects[<dir>].mcpServers". When launched inside a project
		// the project-scoped entry WINS — so writing only the global one leaves a
		// stale/local project entry overriding it (the remote-wiring bug). Write
		// the global entry and repoint every existing project-scoped auxly-memory.
		servers, ok := config["mcpServers"].(map[string]interface{})
		if !ok {
			servers = make(map[string]interface{})
		}
		servers["auxly-memory"] = serverDef
		config["mcpServers"] = servers
		if projects, ok := config["projects"].(map[string]interface{}); ok {
			for _, pv := range projects {
				proj, ok := pv.(map[string]interface{})
				if !ok {
					continue
				}
				psrv, ok := proj["mcpServers"].(map[string]interface{})
				if !ok {
					continue
				}
				if _, exists := psrv["auxly-memory"]; exists {
					psrv["auxly-memory"] = serverDef
				}
			}
		}
		delete(config, "auxly-memory") // drop any stray root-level key
	} else {
		// Antigravity CLI and others: direct key or "mcpServers" by what exists.
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

	if filepath.Base(path) == "settings.json" && t.ProviderID == "gemini" {
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

// findCursorAgent returns the Cursor Agent CLI path, preferring the canonical
// `cursor-agent` name and only accepting a bare `agent` when it actually
// responds as Cursor's MCP-capable CLI — so we never poke ssh-agent or similar.
func findCursorAgent() string {
	if p, err := exec.LookPath("cursor-agent"); err == nil {
		return p
	}
	if p, err := exec.LookPath("agent"); err == nil {
		out, _ := exec.Command(p, "mcp", "--help").CombinedOutput()
		if strings.Contains(strings.ToLower(string(out)), "mcp server") {
			return p
		}
	}
	return ""
}

// approveCursorAllowlist adds a server-level allow token to cursor-agent's
// ~/.cursor/cli-config.json so the INTERACTIVE `agent` UI loads auxly-memory's
// tools. cursor-agent gates local stdio MCP servers behind a per-machine
// allowlist (permissions.allow) using "Mcp(<server>)" / "Mcp(<server>:<tool>)"
// tokens. `mcp enable` only flips the server's enabled flag — it never writes
// this allowlist — which is why a live `agent` session showed "needs approval /
// 0 tools" despite the server being enabled. The whole config is round-tripped
// through a map so every existing setting (auth, model, other allows) is kept.
func approveCursorAllowlist() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(home, ".cursor", "cli-config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		// No cli-config.json → cursor-agent isn't set up / logged in yet. The
		// allowlist will be created on first interactive use; nothing to do.
		return err
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	perms, _ := cfg["permissions"].(map[string]interface{})
	if perms == nil {
		perms = map[string]interface{}{}
		cfg["permissions"] = perms
	}
	allow, _ := perms["allow"].([]interface{})

	const token = "Mcp(auxly-memory)"
	for _, v := range allow {
		if s, ok := v.(string); ok && s == token {
			return nil // already allowed — leave the file untouched
		}
	}
	perms["allow"] = append(allow, token)

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// cli-config.json holds auth tokens (authInfo, authCacheKey) — keep it
	// owner-only readable rather than the world-readable 0644 a naive write picks.
	return os.WriteFile(cfgPath, out, 0600)
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

	// Ensure the vault has all default files (create-if-missing). Back-fills new
	// default files like personal.md for existing users; harmless for new ones.
	if created, _ := memory.SeedDefaultFiles(memPath); len(created) > 0 {
		printAlf("📂 Seeded memory files: %s\r\n\r\n", strings.Join(created, ", "))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	var configuredApps []string

	// Inject the local MCP server definition into every known IDE/agent target.
	for _, t := range knownIDETargets(home) {
		if app := writeMCPConfigEntry(t, localServerDef(binaryPath, memPath, t.ProviderID)); app != "" {
			configuredApps = append(configuredApps, app)
		}
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
		cCmd := exec.Command(codexPath, "mcp", "add", "auxly-memory", "--env", "AUXLY_MEMORY_PATH="+memPath, "--env", "AUXLY_PROVIDER=codex", "--", binaryPath, "--path", memPath, "mcp-server")
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

	// 7b. Cursor Agent CLI: approve the server so its tools load without the
	// manual "needs approval" prompt. We just wrote ~/.cursor/mcp.json above; the
	// `mcp enable` step marks the server enabled, and the cli-config.json allowlist
	// write makes the INTERACTIVE `agent` UI actually load its tools (enable alone
	// leaves the live session showing "needs approval / 0 tools").
	if agentBin := findCursorAgent(); agentBin != "" {
		printAl("🤖 Cursor Agent CLI detected. Approving auxly-memory MCP server...")
		eCmd := exec.Command(agentBin, "mcp", "enable", "auxly-memory")
		eCmd.Stdin = strings.NewReader("")
		if err := eCmd.Run(); err != nil {
			printAlf("⚠️  Could not auto-approve in Cursor Agent: %v (run `cursor-agent mcp enable auxly-memory`)\r\n", err)
		} else {
			printAl("✅ Approved auxly-memory in Cursor Agent CLI (tools will load).")
		}
		if err := approveCursorAllowlist(); err != nil {
			printAlf("⚠️  Could not update Cursor allowlist: %v (open `agent` and press Enable on auxly-memory)\r\n", err)
		} else {
			printAl("✅ Allowed auxly-memory tools in the interactive Cursor agent.")
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

// installAuxlySkills writes every getSkillsMap() skill's SKILL.md into the
// Claude (global + local), Codex (global + local), and Gemini target dirs.
// extraBanner is appended after the standard update reminder (empty for local
// setup; a remote banner for `auxly connect`).
func installAuxlySkills(extraBanner string) {
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
	globalGeminiSkills := filepath.Join(home, ".gemini", "config", "skills")

	targetDirs := []string{globalClaude, localClaude, globalCodex, localCodex, globalGeminiSkills}

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
			_ = os.WriteFile(skillFilePath, []byte(content+updateReminder+extraBanner), 0644)
		}
	}
}

func ensureClaudeAndCodexSkills(memPath string) {
	installAuxlySkills("")
}

func ensureAntigravitySlashCommands(memPath string) {
	// Skill installation (Claude/Codex/Gemini) is handled centrally by
	// ensureClaudeAndCodexSkills -> installAuxlySkills. Kept as a no-op for
	// call-site compatibility.
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
		"auxly-bootstrap.toml",
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
- /auxly-status (or auxly-status / auxly status): Call the auxly_skill_status tool to show system diagnostics, active connections, and remote/SSH attribution.
- /auxly-sync (or auxly-sync / auxly sync): Call the auxly_skill_sync tool with the provided content AND the best-fit category (identity, personal, preferences, infra, products, projects, daily, business, agents) to perform an automated smart delta-merge into the correct memory file. Use 'personal' for the user's OWN private life — family, health, and their personal legal/financial matters (their own lawsuit, divorce, custody, personal loan, salary); a company/business legal or money matter is NOT personal. When a fact is about the user's private life, 'personal' wins over any topical category.
- /auxly-pending (or auxly-pending / auxly pending): Call the auxly_skill_pending tool with arguments list/approve/reject to manage the approval queue.
- /auxly-max (or auxly-max / auxly max): Call the auxly_skill_max tool, then exhaustively self-harvest your entire session — write every fact up via auxly_skill_sync, one category slice at a time (personal facts into personal.md). This pushes memory up; it does NOT pull.
- /auxly-forget (or auxly-forget / auxly forget): Call the auxly_skill_forget tool to search and prune outdated bullet statements.
- /auxly-learn (or auxly-learn / auxly learn): Call the auxly_skill_learn tool to read the memory vault (optionally a single [folder], optionally focused on a [topic]) and ground yourself in it. No args = learn everything.
- /auxly-bootstrap (or auxly-bootstrap / auxly bootstrap): Call the auxly_skill_bootstrap tool to get a copyable onboarding block to paste into a tool that does NOT have Auxly installed (e.g. ChatGPT), then present that block to the user.

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

	// Create-if-missing only: never overwrite an existing workspace rules file.
	// This stops setup/init from clobbering a project's own (possibly customized)
	// rules on every run — and keeps the Auxly source repo clean.
	for _, filename := range ruleFiles {
		if _, err := os.Stat(filename); err == nil {
			continue // already present — leave it untouched
		}
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
description: Exhaustive self-harvest — scan your whole session and write every fact up into the memory vault, slice by category.
---
# /auxly-max

You must immediately invoke the 'auxly_skill_max' MCP tool to load the harvest directive. Then perform an EXHAUSTIVE SELF-HARVEST of your entire session: scan everything you have learned and write it ALL up via the 'auxly_skill_sync' tool, working ONE category at a time (collect all infra facts, then all project facts, etc.), reconciling each slice against what is already saved so you never duplicate. Route the user's OWN private-life facts — family, relationships, health, and their personal legal/financial matters (their own lawsuit/court case, divorce, custody, personal loan, salary) — into personal.md via category 'personal'; a company/business legal or money matter is NOT personal. Judge by context: a personal legal case is never a 'project' or 'business' entry. This pushes memory UP only — do NOT pull or read the vault. Finally, present a beautiful success message confirming the full session has been harvested into unified memory!`,

		"auxly-sync": `---
name: auxly-sync
description: Append and synchronize a new fact, preference, or system detail using smart automated delta-merges into memory files (preferences.md, identity.md, infra.md, products.md, projects.md, daily.md, etc.).
argument-hint: "<fact or preference statement to sync>"
---
# /auxly-sync

You must immediately invoke the 'auxly_skill_sync' MCP tool. Pass the user's statement as the 'content' argument AND set the 'category' argument to the best-fit category from the taxonomy shown in the tool's footer (identity, personal, preferences, infra, products, projects, daily, business, agents) — you understand the fact, so you pick the file; only omit 'category' if you are genuinely unsure, in which case the router will guess. Route the user's OWN private-life facts — their family, health, relationships, and their PERSONAL legal/financial matters (their own lawsuit, court case, divorce, custody, personal loan, salary) — to category 'personal'; a company/business legal or money matter is NOT personal (use 'business'). Judge by context, not the topic word, and when a fact is about the user's private life, 'personal' wins over any topical category (a personal legal case is never a 'project'). This performs a smart automated delta-merge into the chosen memory file. Run the tool and display the confirmation output!`,

		"auxly-pending": `---
name: auxly-pending
description: Manage pending memory changes awaiting human approval directly inside the active chat panel.
argument-hint: "[list | approve <id> | reject <id>]"
---
# /auxly-pending

You must immediately invoke the 'auxly_skill_pending' MCP tool, passing the provided arguments (such as action: list/approve/reject, and target ID) to manage the secure memory write queue. Simply run the tool and display the results!`,

		"auxly-status": `---
name: auxly-status
description: Show whether this agent is connected to Auxly memory and the MCP link is live.
---
# /auxly-status

Call the 'auxly_skill_status' MCP tool exactly ONCE and show its raw output to the user. That output IS the complete status: it confirms the MCP link is live, reports the memory connection (local or ssh-remote), and shows database stats.

HARD RULES — the single tool call is the entire task:
- Do NOT read any source code or files.
- Do NOT run shell/bash commands or other auxly CLI commands (no 'auxly stats', 'auxly connect test', 'auxly list', etc.).
- Do NOT investigate, diagnose, or "test" anything further — the tool reply already proves the MCP channel works.
- If the tool replies, the status is healthy. Just present it and stop.`,

		"auxly-forget": `---
name: auxly-forget
description: Search memory vault and prune obsolete or outdated bullet statements cleanly from memory files.
argument-hint: "<query string to search and delete>"
---
# /auxly-forget

You must immediately invoke the 'auxly_skill_forget' MCP tool, passing the user's provided input as the 'query' argument, to search across all memory files and delete matching obsolete lines cleanly. Simply run the tool and display the deletion diff!`,

		"auxly-learn": `---
name: auxly-learn
description: Read the memory vault (optionally a single folder, optionally focused on a topic) and ground yourself in it for the rest of the session.
argument-hint: "[folder] [topic]"
---
# /auxly-learn

You must immediately invoke the 'auxly_skill_learn' MCP tool to read the unified memory vault and internalize it — learn everything already known about the user and operate from it for the rest of the session. Pass the optional first argument as 'folder' to read only that category/file (e.g. 'infra', 'projects'), and the optional second argument as 'topic' to focus within it (e.g. 'infra nginx'). Empty args = learn everything. Absorb the returned content and behave accordingly.`,

		"auxly-bootstrap": `---
name: auxly-bootstrap
description: Get a copyable onboarding block to paste into a tool without Auxly installed.
---
# /auxly-bootstrap

You must immediately invoke the 'auxly_skill_bootstrap' MCP tool to generate a copyable onboarding block, then present that block to the user verbatim so they can paste it into a tool that does NOT have Auxly installed (e.g. ChatGPT). Running this only SHOWS the block — it does NOT sync anything itself; the foreign agent does the actual reading/writing by following the block's instructions. Simply run the tool and display the returned block!`,

		"auxly-remote-connect": `---
name: auxly-remote-connect
description: Detect and connect this machine to a remote Auxly memory host (or report the active link).
---
# /auxly-remote-connect

Immediately invoke the 'auxly_skill_remote_connect' MCP tool. Then act on what it returns:

1. If it reports an ACTIVE remote connection (host, client IP from SSH_CONNECTION, remote OS), just relay that — reads/writes are central and audited on the shared host. Nothing else to do.

2. If it reports a LOCAL vault but an offered host is available on this machine, it will include an ACTION block with an ` + "`auxly connect auto`" + ` command. RUN that exact command in a shell on this machine (it wires the MCP launcher + skills, no flags, no prompts). If the command reports the box's SSH key isn't authorized on the host yet, show the user the printed public key and the one-time step. On success, tell the user to RESTART this agent so it loads the remote memory — after restart, /auxly-remote-connect will show the live link.

3. If it reports a LOCAL vault with no offer, tell the user to run ` + "`auxly host setup`" + ` on the memory host first (that publishes the offer here).

You MAY run the ` + "`auxly connect auto`" + ` command yourself (it is non-interactive and safe). You must NOT hand-edit SSH keys or config files — connect auto handles that.`,
	}
}
