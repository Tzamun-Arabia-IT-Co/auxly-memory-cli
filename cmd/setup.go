package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/skills"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
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

	// Default fallback: check OS-specific install locations.
	home, err := os.UserHomeDir()
	if err == nil {
		var candidates []string
		if runtime.GOOS == "windows" {
			localAppData := os.Getenv("LOCALAPPDATA")
			if localAppData == "" {
				localAppData = filepath.Join(home, "AppData", "Local")
			}
			candidates = []string{
				filepath.Join(localAppData, "Programs", "auxly", "auxly.exe"),
				filepath.Join(home, ".local", "bin", "auxly.exe"),
			}
		} else {
			candidates = []string{filepath.Join(home, ".local", "bin", "auxly")}
		}
		for _, c := range candidates {
			if c == "" {
				continue
			}
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}

	if runtime.GOOS == "windows" {
		// Never hand Claude Desktop a bare "auxly.exe": it launches MCP servers
		// without the interactive shell PATH, and the installer drops the binary
		// in %LOCALAPPDATA%\Programs\auxly (not on the global PATH), so a bare
		// name would fail to start. Return the canonical absolute install path.
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			if home, err := os.UserHomeDir(); err == nil {
				localAppData = filepath.Join(home, "AppData", "Local")
			}
		}
		if localAppData != "" {
			return filepath.Join(localAppData, "Programs", "auxly", "auxly.exe")
		}
		return "auxly.exe"
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

	// 1. Claude Desktop. detect.AppSupportDir resolves the SAME darwin/linux
	// paths and adds the Windows APPDATA-empty fallback (home\AppData\Roaming).
	// Building Windows paths from a raw os.Getenv("APPDATA") that is empty —
	// the case in non-interactive / SSH-spawned PowerShell sessions, which is
	// auxly's main Windows wiring path — collapses to a bare relative "Claude"
	// that writeMCPConfigEntry then os.Stat-skips, so the MCP entry is never
	// written. Routing through AppSupportDir (which detect.InstalledAgents
	// already uses) makes the WRITTEN path equal the DETECTED path.
	claudeBaseDir := detect.AppSupportDir(home, "Claude")
	claudeConfigPath := filepath.Join(claudeBaseDir, "claude_desktop_config.json")
	targets = append(targets, ideTarget{claudeConfigPath, "Claude Desktop", claudeBaseDir, true, "claude"})

	// 2. Cursor (same APPDATA-empty fallback as Claude Desktop above).
	cursorBaseDir := detect.AppSupportDir(home, "Cursor")
	cursorConfigPath := filepath.Join(cursorBaseDir, "User", "globalStorage", "co.heron.cursor", "mcpServers.json")
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

	// 4b. Antigravity IDE (Bundle Support Paths) — same APPDATA-empty fallback.
	antigravityIdeBaseDir := detect.AppSupportDir(home, "Antigravity")
	antigravityIdeConfigPath := filepath.Join(antigravityIdeBaseDir, "User", "settings.json")
	targets = append(targets, ideTarget{antigravityIdeConfigPath, "Antigravity Agent (settings)", antigravityIdeBaseDir, false, "antigravity-agent"})

	antigravityIdeBaseDir2 := detect.AppSupportDir(home, "Antigravity IDE")
	antigravityIdeConfigPath2 := filepath.Join(antigravityIdeBaseDir2, "User", "settings.json")
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

	// 7b. Windsurf / Devin Desktop (Cognition rebrand, Jun 2026) — Cascade MCP
	// config. Post-rebrand docs contradict themselves on the path (the FAQ says
	// ~/.codeium/mcp_config.json, the Cascade MCP doc says
	// ~/.codeium/windsurf/mcp_config.json, verified 2026-07). Each target gates
	// on its OWN layout dir existing, so setup only writes the layout the
	// installed app actually uses and never creates a phantom sibling layout.
	// Same {"mcpServers":{...}} schema as Claude Desktop.
	codeiumBaseDir := filepath.Join(home, ".codeium")
	windsurfLayoutDir := filepath.Join(codeiumBaseDir, "windsurf")
	targets = append(targets, ideTarget{filepath.Join(windsurfLayoutDir, "mcp_config.json"), "Windsurf (Devin Desktop)", windsurfLayoutDir, false, "windsurf"})
	targets = append(targets, ideTarget{filepath.Join(codeiumBaseDir, "mcp_config.json"), "Windsurf (Devin Desktop, flat layout)", codeiumBaseDir, false, "windsurf"})

	// 8. GitHub Copilot CLI — ~/.copilot/mcp-config.json. Same {"mcpServers":{...}}
	// wrapper, but each entry also needs "type":"local" and "tools":["*"] (added in
	// runSetup for the copilot provider). COPILOT_HOME can relocate ~/.copilot.
	copilotBaseDir := filepath.Join(home, ".copilot")
	if ch := os.Getenv("COPILOT_HOME"); ch != "" {
		copilotBaseDir = ch
	}
	targets = append(targets, ideTarget{filepath.Join(copilotBaseDir, "mcp-config.json"), "GitHub Copilot CLI", copilotBaseDir, false, "copilot"})

	// 9. VS Code native MCP (1.102+) — <AppSupport>/Code/User/mcp.json. UNLIKE
	// every other target this uses a top-level "servers" key, NOT "mcpServers"
	// (VS Code silently ignores mcpServers here — the entry shows nothing). The
	// schema is pinned per-target in writeMCPConfigEntry by ProviderID=="vscode",
	// because Void (a real VS Code fork) also uses mcp.json but wants mcpServers,
	// so the shared basename cannot decide the key.
	vscodeBaseDir := detect.AppSupportDir(home, "Code")
	targets = append(targets, ideTarget{filepath.Join(vscodeBaseDir, "User", "mcp.json"), "VS Code", vscodeBaseDir, false, "vscode"})

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
// the per-file placement rules. Returns (appName, nil) on success, ("", nil)
// when the app isn't installed (benign skip), and ("", err) on a real write
// failure — callers must report failures, never silently claim success.
func writeMCPConfigEntry(t ideTarget, serverDef map[string]interface{}) (string, error) {
	path := t.Path
	appName := t.AppName

	// Check if base directory exists (meaning the app is installed). A stat
	// error other than NotExist (AV lock, OneDrive reparse point on Windows)
	// counts as "can't reach this app" and skips — hard failures are reserved
	// for real write errors so one flaky probe never fails the whole setup.
	if t.BaseDir != "" {
		if _, err := os.Stat(t.BaseDir); err != nil {
			return "", nil
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
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
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

	if t.ProviderID == "vscode" {
		// VS Code's native MCP file keys servers under "servers", not
		// "mcpServers" (which it ignores). Pinned by ProviderID because the
		// mcp.json basename is shared with Void, which wants mcpServers.
		servers, ok := config["servers"].(map[string]interface{})
		if !ok {
			servers = make(map[string]interface{})
		}
		servers["auxly-memory"] = serverDef
		config["servers"] = servers
		// Drop a stale auxly-memory a wrong-keyed prior version may have written
		// under "mcpServers" (VS Code ignores it), without touching anything else
		// the user has there.
		if legacy, ok := config["mcpServers"].(map[string]interface{}); ok {
			delete(legacy, "auxly-memory")
			if len(legacy) == 0 {
				delete(config, "mcpServers")
			}
		}
	} else if t.IsClaudeDesktop || filepath.Base(path) == "settings.json" || filepath.Base(path) == "mcp_config.json" || filepath.Base(path) == "mcpServers.json" || filepath.Base(path) == "mcp-config.json" {
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
		return "", fmt.Errorf("cannot marshal config: %w", err)
	}
	if err := os.WriteFile(path, newData, 0644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", path, err)
	}
	return appName, nil
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
	var failedApps []string

	// Inject the local MCP server definition into every known IDE/agent target.
	for _, t := range knownIDETargets(home) {
		serverDef := localServerDef(binaryPath, memPath, t.ProviderID)
		// GitHub Copilot CLI requires an explicit transport type and tool allow-list
		// per server entry (other clients ignore these extra keys).
		if t.ProviderID == "copilot" {
			serverDef["type"] = "local"
			serverDef["tools"] = []interface{}{"*"}
		}
		app, werr := writeMCPConfigEntry(t, serverDef)
		switch {
		case werr != nil:
			failedApps = append(failedApps, fmt.Sprintf("%s — %v", t.AppName, werr))
		case app != "":
			configuredApps = append(configuredApps, app)
		}
		// app == "" && werr == nil → agent not installed, benign skip
	}

	// Print beautiful aligned configured applications
	if len(configuredApps) > 0 {
		printAl("✅ Automatically configured local MCP for:")
		for _, app := range configuredApps {
			printAlf("   ↳ %s\r\n", app)
		}
		printAl("")
	}

	// Wire the capture hook for whichever agents are actually present: claude/
	// codex get their native settings-file hook, gemini/kimi/antigravity get
	// the ~/.zshrc shell-wrapper hook (see autoWireCleanHooks).
	if wired := autoWireCleanHooks(home); len(wired) > 0 {
		printAlf("✅ Auto-wired capture hooks: %s\r\n\r\n", strings.Join(wired, ", "))
	}

	// A failed write must never masquerade as success — name each failure and
	// exit non-zero at the end so scripts see it too.
	if len(failedApps) > 0 {
		printAl("❌ FAILED to configure (fix and re-run `auxly setup`):")
		for _, f := range failedApps {
			printAlf("   ↳ %s\r\n", f)
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
		// Personal ~/.claude/skills files do not reliably surface in Claude's
		// skill picker on all builds — plugin skills always do. Install the
		// auxly plugin so the /auxly-* skills load in every session.
		ensureAuxlyClaudePlugin(claudePath)
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

	// Wire the Auxly statusline for any detected agent that has none yet.
	// Idempotent + non-destructive: a user's own statusline (or an already-Auxly
	// one) is left untouched. Without this, a fresh LOCAL `auxly setup` — the
	// canonical Windows onboarding path — never configured a statusline; only
	// `auxly connect` did. Now both do.
	if wired := statusline.AutoInstallMissing(); len(wired) > 0 {
		printAlf("✅ Installed the Auxly statusline for: %s\r\n\r\n", strings.Join(wired, ", "))
	}

	printAl("🚀 Onboard your AI Agents instantly:")
	printAl("   Simply type `/auxly-init` (or 'auxly init') inside your agent's active chat panel")
	printAl("   (Claude Desktop, Claude Code CLI, Cursor, or Codex IDE).")
	printAl("   This will run the onboarding training and align memory automatically!")
	printAl("")

	if len(failedApps) > 0 {
		printAlf("⚠️  Setup finished with %d failure(s) — see the FAILED list above.\r\n", len(failedApps))
		return fmt.Errorf("%d agent config write(s) failed", len(failedApps))
	}

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
// Claude (global + local) and Kimi skill dirs — the only agents that actually
// read a SKILL.md file. Codex, Gemini, Antigravity, and Cursor are
// instruction-based, not skills-native: they get the Auxly memory context
// block injected into their global context file instead, via
// installAuxlyContextBlocks below. extraBanner is appended after the standard
// update reminder (empty for local setup; a remote banner for `auxly connect`).
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

	targetDirs := []string{globalClaude, localClaude}

	for _, baseDir := range targetDirs {
		// Only write local dirs if we are actually in a directory that is not home
		if baseDir == localClaude {
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

	// Kimi Code CLI uses a plugin/skill system: a skill is a <name>/SKILL.md
	// directory, and Kimi only loads skills from registered locations (its plugins
	// plus the extra_skill_dirs list in config.toml). Unlike Claude/Codex/Gemini,
	// dropping SKILL.md files into a conventional folder is NOT enough — the
	// containing dir must be added to extra_skill_dirs. So we write the Auxly
	// skills under <kimiHome>/auxly-skills and register that path. Both the current
	// (~/.kimi-code) and legacy (~/.kimi) homes are handled when present.
	for _, kimiHome := range []string{
		filepath.Join(home, ".kimi-code"),
		filepath.Join(home, ".kimi"),
	} {
		if fi, err := os.Stat(kimiHome); err != nil || !fi.IsDir() {
			continue // Kimi not installed at this location
		}
		skillsRoot := filepath.Join(kimiHome, "auxly-skills")
		for skillName, content := range commands {
			skillDir := filepath.Join(skillsRoot, skillName)
			_ = os.MkdirAll(skillDir, 0755)
			skillFilePath := filepath.Join(skillDir, "SKILL.md")
			_ = os.WriteFile(skillFilePath, []byte(content+updateReminder+extraBanner), 0644)
		}
		registerKimiSkillDir(filepath.Join(kimiHome, "config.toml"), skillsRoot)
	}

	// GitHub Copilot CLI loads slash commands from ~/.copilot/skills/<name>/SKILL.md
	// (same convention as Claude/Kimi, auto-discovered — no config registration
	// needed, unlike Kimi). So the auxly skills show as /auxly-* in Copilot's slash
	// menu. Only write when Copilot is installed; COPILOT_HOME can relocate it.
	copilotHome := filepath.Join(home, ".copilot")
	if ch := os.Getenv("COPILOT_HOME"); ch != "" {
		copilotHome = ch
	}
	if fi, err := os.Stat(copilotHome); err == nil && fi.IsDir() {
		copilotSkills := filepath.Join(copilotHome, "skills")
		for skillName, content := range commands {
			skillDir := filepath.Join(copilotSkills, skillName)
			_ = os.MkdirAll(skillDir, 0755)
			_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content+updateReminder+extraBanner), 0644)
		}
	}

	installAuxlyContextBlocks(home)
}

// auxlyContextBlockStart and auxlyContextBlockEnd delimit the Auxly memory
// block injected into instruction-based agents' global context files. The
// markers let installAuxlyContextBlocks / removeAuxlyContextBlock find and
// replace or strip the block without touching anything else in the file.
const (
	auxlyContextBlockStart = "<!-- >>> auxly memory (managed by auxly setup) >>>"
	auxlyContextBlockEnd   = "<!-- <<< auxly memory <<< -->"
)

// auxlyContextBlockBody is the block injected between the markers. It tells
// an instruction-based agent (Codex, Gemini, Antigravity, Cursor) that it has
// the auxly-memory MCP server and how to use it — these tools don't read
// SKILL.md, so this is their only onboarding path to the memory tools.
func auxlyContextBlockBody() string {
	return "## Auxly Memory\n" +
		"You have the `auxly-memory` MCP server — the user's local-first, unified\n" +
		"memory vault shared across their AI agents. Use it proactively:\n" +
		"- `auxly_skill_sync`: save a durable fact, preference, config, or decision.\n" +
		"  Pick the best category: identity, personal, preferences, infra, products,\n" +
		"  projects, daily, business, or agents. Route the user's OWN private-life\n" +
		"  matters (family, health, personal legal/financial) to `personal` — a\n" +
		"  company/business matter is NOT personal.\n" +
		"- `auxly_skill_status` / `auxly_skill_memory`: show connection health and a\n" +
		"  consolidated profile of what's already known.\n" +
		"- `auxly_skill_max`: exhaustively self-harvest this entire session into memory.\n" +
		"- `auxly_memory_recall`: retrieve memories relevant to the current task.\n" +
		"Whenever you learn a durable developer preference, system config, product\n" +
		"scope, or decision during this conversation, immediately call\n" +
		"`auxly_skill_sync` to save it — don't wait to be asked.\n"
}

// auxlyBlockBounds locates the marker-delimited Auxly block in text, including
// a single trailing newline right after the end marker (so a replace or strip
// doesn't accumulate blank lines across repeated runs). ok is false if the
// start marker is absent, or present with no matching end marker.
func auxlyBlockBounds(text string) (start, end int, ok bool) {
	start = strings.Index(text, auxlyContextBlockStart)
	if start < 0 {
		return 0, 0, false
	}
	rel := strings.Index(text[start:], auxlyContextBlockEnd)
	if rel < 0 {
		return 0, 0, false
	}
	end = start + rel + len(auxlyContextBlockEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return start, end, true
}

// injectAuxlyContextBlock writes (or refreshes) the Auxly memory block into an
// instruction-based agent's global context file — the mechanism Codex,
// Gemini, Antigravity, and Cursor actually read (unlike Claude/Kimi, they
// don't load SKILL.md). Idempotent: re-running replaces the existing block in
// place instead of appending a duplicate. Only creates the file if its parent
// dir already exists — never fabricates a dir for an agent that isn't there.
func injectAuxlyContextBlock(contextFile string) {
	if fi, err := os.Stat(filepath.Dir(contextFile)); err != nil || !fi.IsDir() {
		return
	}

	block := auxlyContextBlockStart + "\n" + auxlyContextBlockBody() + auxlyContextBlockEnd + "\n"

	existing, err := os.ReadFile(contextFile)
	if err != nil {
		_ = os.WriteFile(contextFile, []byte(block), 0644)
		return
	}
	text := string(existing)

	if start, end, ok := auxlyBlockBounds(text); ok {
		_ = os.WriteFile(contextFile, []byte(text[:start]+block+text[end:]), 0644)
		return
	}

	// No existing block: append, with a leading blank line only if the file
	// already has content.
	out := text
	if strings.TrimSpace(out) != "" {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "\n"
	}
	out += block
	_ = os.WriteFile(contextFile, []byte(out), 0644)
}

// removeAuxlyContextBlock strips the Auxly memory block (markers inclusive)
// from an instruction-based agent's context file, leaving the rest of the
// file byte-intact. Reports whether a block was found and removed.
func removeAuxlyContextBlock(contextFile string) bool {
	data, err := os.ReadFile(contextFile)
	if err != nil {
		return false
	}
	text := string(data)
	start, end, ok := auxlyBlockBounds(text)
	if !ok {
		return false
	}
	return os.WriteFile(contextFile, []byte(text[:start]+text[end:]), 0644) == nil
}

// installAuxlyContextBlocks injects the Auxly memory context block into every
// instruction-based agent's global context file — but only for an agent
// actually installed on this machine (its detect dir exists). It never
// creates ~/.cursor or similar for a tool that isn't present.
func installAuxlyContextBlocks(home string) {
	targets := map[string]string{
		filepath.Join(home, ".codex"):       filepath.Join(home, ".codex", "AGENTS.md"),
		filepath.Join(home, ".gemini"):      filepath.Join(home, ".gemini", "GEMINI.md"),
		filepath.Join(home, ".antigravity"): filepath.Join(home, ".antigravity", "AGENTS.md"),
		filepath.Join(home, ".cursor"):      filepath.Join(home, ".cursor", "AGENTS.md"),
	}
	for detectDir, contextFile := range targets {
		if fi, err := os.Stat(detectDir); err == nil && fi.IsDir() {
			injectAuxlyContextBlock(contextFile)
		}
	}
}

// registerKimiSkillDir adds skillsRoot to the extra_skill_dirs array in Kimi's
// config.toml so the CLI discovers the Auxly skills (merge_all_available_skills
// defaults to true). It edits the single extra_skill_dirs line in place — no TOML
// dependency — and is idempotent: a path already present is left untouched. If the
// key is missing it is appended. If the config file doesn't exist yet (Kimi writes
// it on first run) it is left alone; a later `auxly setup` will register it.
func registerKimiSkillDir(configPath, skillsRoot string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	text := string(data)
	quoted := strconv.Quote(skillsRoot) // TOML basic string; backslashes escaped on Windows
	if strings.Contains(text, quoted) {
		return // already registered
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "extra_skill_dirs") {
			continue
		}
		open := strings.Index(line, "[")
		closeIdx := strings.LastIndex(line, "]")
		if open < 0 || closeIdx < 0 || closeIdx < open {
			return // not a single-line array we can safely edit; leave as-is
		}
		inner := strings.TrimSpace(line[open+1 : closeIdx])
		if inner == "" {
			inner = quoted
		} else {
			inner = inner + ", " + quoted
		}
		lines[i] = line[:open+1] + inner + line[closeIdx:]
		_ = os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
		return
	}

	// Key absent — append it.
	lines = append(lines, "extra_skill_dirs = ["+quoted+"]")
	_ = os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
}

// ensureAuxlyClaudePlugin adds the auxly marketplace and installs the auxly
// plugin so Claude Code loads the /auxly-* skills in every session. Personal
// ~/.claude/skills files do not reliably surface in Claude's skill picker on
// all builds, but plugin skills always do — so the plugin is the reliable
// delivery. Best-effort: idempotent (both commands no-op if already
// added/installed), non-fatal, and time-bounded so a network hang can never
// block setup.
func ensureAuxlyClaudePlugin(claudePath string) {
	printAl("🔌 Installing the auxly skills plugin for Claude Code (may take a moment)...")
	run := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, claudePath, args...)
		c.Stdin = strings.NewReader("")
		c.Stderr = os.Stderr // surface the failure reason; stdout stays quiet
		return c.Run()
	}
	// marketplace add is idempotent; ignore "already exists" errors.
	_ = run("plugin", "marketplace", "add", "Tzamun-Arabia-IT-Co/auxly-skills")
	if err := run("plugin", "install", "auxly@auxly"); err != nil {
		printAlf("💡 Couldn't auto-install the plugin (%v). Run manually: claude plugin install auxly@auxly\r\n", err)
		return
	}
	printAl("✅ Installed the auxly skills plugin for Claude Code (restart Claude to load the /auxly-* skills).")
}

func ensureClaudeAndCodexSkills(memPath string) {
	installAuxlySkills("")
	printAl("💡 Already-open Claude Code sessions won't see the new skills until you run `/reload-skills` (v2.1.152+) or restart.")
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
