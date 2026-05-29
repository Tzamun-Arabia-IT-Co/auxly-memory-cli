package detect

import (
	"os"
	"os/exec"
	"path/filepath"
)

type Agent struct {
	Name       string
	Provider   string
	Path       string
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
			[]string{filepath.Join(home, "Library/Application Support/Claude/claude_desktop_config.json")}, nil},
		{"Claude Code / CLI", "claude-code", "MCP+Shell",
			nil, []string{"claude"}},
		{"Cursor CLI", "cursor", "MCP",
			nil, []string{"cursor"}},
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
		{"Copilot", "copilot", "Shell",
			[]string{filepath.Join(home, "Library/Application Support/GitHub Copilot")}, nil},
		{"Gemini Desktop", "gemini", "Shell",
			[]string{filepath.Join(home, "Library/Application Support/Gemini")}, nil},
	}

	for _, c := range checks {
		found := false
		foundPath := ""

		for _, p := range c.paths {
			if _, err := os.Stat(p); err == nil {
				found = true
				foundPath = p
				break
			}
		}

		if !found {
			for _, b := range c.binaries {
				if p, err := exec.LookPath(b); err == nil {
					found = true
					foundPath = p
					break
				}
			}
		}

		if found {
			agents = append(agents, Agent{
				Name:       c.name,
				Provider:   c.provider,
				Path:       foundPath,
				Connection: c.connection,
			})
		}
	}

	return agents
}
