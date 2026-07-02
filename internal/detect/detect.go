package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// AppSupportDir returns the per-application config base directory for the
// current OS: macOS ~/Library/Application Support, Linux ~/.config,
// Windows %APPDATA% (Roaming).
func AppSupportDir(home, app string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", app)
	case "linux":
		return filepath.Join(home, ".config", app)
	default: // windows (and any other OS falls back to %APPDATA% / home-relative)
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, app)
	}
}

type Agent struct {
	Name     string
	Provider string
	// Path is where the agent was detected: a config file/dir if one exists,
	// otherwise the resolved binary. Use it to know an agent is installed — NOT
	// to execute it (a config dir is not runnable).
	Path string
	// Command is the resolved executable on PATH (empty if none). This is the
	// only field safe to fork/exec — e.g. for on-demand memory organization.
	Command    string
	Connection string
}

func InstalledAgents() []Agent {
	home, _ := os.UserHomeDir()
	var agents []Agent

	checks := []struct {
		name       string
		provider   string
		connection string
		paths      []string
		binaries   []string
	}{
		{"Claude Desktop", "claude", "MCP",
			[]string{filepath.Join(AppSupportDir(home, "Claude"), "claude_desktop_config.json")}, nil},
		{"Claude Code / CLI", "claude-code", "MCP+Shell",
			nil, []string{"claude"}},
		// Cursor ships two distinct surfaces that each take their own MCP config:
		// the IDE (globalStorage mcpServers.json, launched by the `cursor` binary)
		// and the Agent CLI (`cursor-agent`, reads ~/.cursor/mcp.json). Detect them
		// separately so init shows both and onboards each correctly.
		{"Cursor IDE", "cursor", "MCP",
			[]string{filepath.Join(AppSupportDir(home, "Cursor"), "User", "globalStorage", "co.heron.cursor", "mcpServers.json")},
			[]string{"cursor"}},
		{"Cursor CLI", "cursor", "MCP",
			[]string{filepath.Join(home, ".cursor", "mcp.json")},
			[]string{"cursor-agent"}},
		{"Codex IDE Desktop", "codex", "MCP",
			[]string{filepath.Join(home, ".codex/config.toml")}, nil},
		{"Gemini CLI", "gemini", "MCP",
			nil, []string{"gemini"}},
		{"Codex CLI", "codex", "Shell",
			nil, []string{"codex"}},
		{"Antigravity IDE", "antigravity", "MCP",
			[]string{filepath.Join(home, ".gemini/antigravity-ide")}, nil},
		{"Antigravity CLI", "antigravity", "MCP",
			[]string{filepath.Join(home, ".gemini/antigravity-cli")}, []string{"agy"}},
		{"Antigravity Agent", "antigravity", "MCP",
			[]string{filepath.Join(home, ".gemini/antigravity-agent")}, []string{"antigravity-agent"}},
		// GitHub Copilot CLI (npm @github/copilot, binary `copilot`) — MCP servers
		// live in ~/.copilot/mcp-config.json. Detected by that config dir or the
		// binary on PATH. The older Copilot desktop app dir is kept as a fallback.
		{"GitHub Copilot CLI", "copilot", "MCP",
			[]string{filepath.Join(home, ".copilot"), AppSupportDir(home, "GitHub Copilot")},
			[]string{"copilot"}},
		// Perplexity for macOS supports MCP via its Connectors UI (requires the
		// PerplexityXPC helper). There is no writable config file, so auxly detects
		// the app and surfaces a paste-in connector command during setup.
		{"Perplexity", "perplexity", "MCP",
			[]string{AppSupportDir(home, "Perplexity")}, nil},
		{"Gemini Desktop", "gemini", "Shell",
			[]string{AppSupportDir(home, "Gemini")}, nil},
		// Warp terminal — detected by its config dir (created once Warp runs); MCP
		// config is wired at ~/.warp/.mcp.json by knownIDETargets.
		{"Warp", "warp", "MCP",
			[]string{filepath.Join(home, ".warp")}, []string{"warp"}},
		// Void editor (open-source VS Code fork) — detected by its data dir; MCP
		// config is wired at ~/.void-editor/mcp.json.
		{"Void", "void", "MCP",
			[]string{filepath.Join(home, ".void-editor")}, []string{"void"}},
		// Windsurf / Devin Desktop (Cognition rebrand Jun 2026). Detected ONLY by
		// the ~/.codeium config root — the same root setup's wiring targets reach
		// — so "detected" never means "detected but forever unwirable". (An
		// install exposing only the AppSupport data dir has no known writable MCP
		// path, so we deliberately don't detect on it.)
		{"Windsurf (Devin Desktop)", "windsurf", "MCP",
			[]string{filepath.Join(home, ".codeium", "windsurf"), filepath.Join(home, ".codeium")},
			[]string{"windsurf"}},
		// Kimi Code CLI + Trae IDE — both have wiring targets in knownIDETargets;
		// detection was missing (agents/doctor could never show them).
		{"Kimi Code CLI", "kimi", "MCP",
			[]string{filepath.Join(home, ".kimi-code"), filepath.Join(home, ".kimi")}, []string{"kimi"}},
		{"Trae IDE", "trae", "MCP",
			[]string{filepath.Join(home, ".trae")}, []string{"trae"}},
	}

	for _, c := range checks {
		configPath := ""
		for _, p := range c.paths {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}

		// Always resolve the runnable binary too — independent of config-path
		// detection — so an agent detected only by its config dir still exposes
		// an executable Command (or none, if the CLI isn't on PATH).
		command := ""
		for _, b := range c.binaries {
			if p, err := exec.LookPath(b); err == nil {
				command = p
				break
			}
		}

		if configPath == "" && command == "" {
			continue
		}
		path := configPath
		if path == "" {
			path = command
		}
		agents = append(agents, Agent{
			Name:       c.name,
			Provider:   c.provider,
			Path:       path,
			Command:    command,
			Connection: c.connection,
		})
	}

	return agents
}
