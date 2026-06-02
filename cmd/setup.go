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
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/skills"
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

	// 6. Warp terminal — file-based MCP config at ~/.warp/.mcp.json (same
	// {"mcpServers":{...}} stdio schema as Claude Desktop). Note: Warp shows a
	// one-time approval prompt before a file-added server first runs.
	warpBaseDir := filepath.Join(home, ".warp")
	targets = append(targets, ideTarget{filepath.Join(warpBaseDir, ".mcp.json"), "Warp", warpBaseDir, false, "warp"})

	// 7. Void editor (open-source VS Code fork) — ~/.void-editor/mcp.json, same
	// mcpServers stdio schema (confirmed from Void source). May need a Void restart.
	voidBaseDir := filepath.Join(home, ".void-editor")
	targets = append(targets, ideTarget{filepath.Join(voidBaseDir, "mcp.json"), "Void", voidBaseDir, false, "void"})

	// 8. GitHub Copilot CLI — ~/.copilot/mcp-config.json. Same {"mcpServers":{...}}
	// wrapper, but each entry also needs "type":"local" and "tools":["*"] (added in
	// runSetup for the copilot provider). COPILOT_HOME can relocate ~/.copilot.
	copilotBaseDir := filepath.Join(home, ".copilot")
	if ch := os.Getenv("COPILOT_HOME"); ch != "" {
		copilotBaseDir = ch
	}
	targets = append(targets, ideTarget{filepath.Join(copilotBaseDir, "mcp-config.json"), "GitHub Copilot CLI", copilotBaseDir, false, "copilot"})

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

	if t.IsClaudeDesktop || filepath.Base(path) == "settings.json" || filepath.Base(path) == "mcp_config.json" || filepath.Base(path) == "mcpServers.json" || filepath.Base(path) == "mcp-config.json" {
		// Claude Desktop, Cursor, VS Code, Antigravity, and GitHub Copilot CLI put
		// servers inside the "mcpServers" key
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
		// Note: Warp's file is ".mcp.json" (leading dot) and Void's is "mcp.json";
		// both want the {"mcpServers":{...}} wrapper, so match both basenames.
		base := filepath.Base(path)
		if _, ok := config["mcpServers"]; ok || base == "mcp.json" || base == ".mcp.json" {
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
		serverDef := localServerDef(binaryPath, memPath, t.ProviderID)
		// GitHub Copilot CLI requires an explicit transport type and tool allow-list
		// per server entry (other clients ignore these extra keys).
		if t.ProviderID == "copilot" {
			serverDef["type"] = "local"
			serverDef["tools"] = []interface{}{"*"}
		}
		if app := writeMCPConfigEntry(t, serverDef); app != "" {
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

	// 7c. Perplexity (macOS) is wired through its Connectors GUI (via the
	// PerplexityXPC helper), not a writable config file — so we surface the exact
	// command to paste into Settings → Connectors → Add Connector → Simple.
	if _, err := os.Stat(filepath.Join(home, "Library/Application Support/Perplexity")); err == nil {
		printAl("🤖 Perplexity detected. It's wired via the Connectors UI (no config file):")
		printAl("   1. Perplexity → Settings → Connectors → Add Connector → \"Simple\" tab")
		printAl("   2. Install the PerplexityXPC helper if prompted")
		printAlf("   3. Command:  %s --path %s mcp-server\r\n", binaryPath, memPath)
		printAl("   4. Save and wait for the connector to show \"Running\".")
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
	updateReminder := skills.UpdateReminder

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
	return skills.Map()
}
