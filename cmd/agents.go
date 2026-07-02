package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/spf13/cobra"
)

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "List detected AI agents and their Auxly wiring status",
	RunE:  runAgents,
}

func init() {
	rootCmd.AddCommand(agentsCmd)
}

// mcpWiredProviders returns the ProviderIDs whose MCP target config exists on
// disk AND already carries an auxly-memory entry — the ground truth for "this
// agent is wired", independent of what setup claimed.
func mcpWiredProviders(home string) map[string]bool {
	wired := make(map[string]bool)
	for _, t := range knownIDETargets(home) {
		data, err := os.ReadFile(t.Path)
		if err == nil && strings.Contains(string(data), "auxly-memory") {
			wired[t.ProviderID] = true
		}
	}
	// Codex IDE has no knownIDETargets entry — it's wired by `codex mcp add`
	// into ~/.codex/config.toml (TOML, not the JSON writer's format). Check the
	// real file so agents/doctor tell the truth about it.
	if data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); err == nil &&
		strings.Contains(string(data), "auxly-memory") {
		wired["codex"] = true
	}
	return wired
}

// providerWired maps a detected agent's Provider onto the wired target ids.
// Exact match covers everything except antigravity, whose targets use
// suffixed ids (antigravity-cli/-agent/-ide).
func providerWired(wired map[string]bool, provider string) bool {
	if wired[provider] {
		return true
	}
	if provider == "antigravity" {
		for id := range wired {
			if strings.HasPrefix(id, "antigravity") {
				return true
			}
		}
	}
	return false
}

func runAgents(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	agents := detect.InstalledAgents()
	if len(agents) == 0 {
		fmt.Println("No AI agents detected on this machine.")
		fmt.Println("Install one (Claude Code, Cursor, Codex, Gemini CLI, …) and run `auxly setup`.")
		return nil
	}

	wired := mcpWiredProviders(home)

	fmt.Println("🤖 Detected AI agents")
	fmt.Printf("   %-28s %-14s %-11s %-9s %s\n", "AGENT", "PROVIDER", "CONNECTION", "MCP", "DETECTED AT")
	wiredCount := 0
	for _, a := range agents {
		mcp := "✗"
		switch {
		// Shell-only surfaces first: they have no MCP config of their own, so a
		// sibling agent sharing the provider (e.g. Gemini Desktop vs Gemini CLI)
		// must never make them show as "wired".
		case a.Connection == "Shell":
			mcp = "n/a"
		case a.Provider == "perplexity":
			mcp = "manual" // Connectors UI only — no writable config file
		case providerWired(wired, a.Provider):
			mcp = "✓"
			wiredCount++
		}
		fmt.Printf("   %-28s %-14s %-11s %-9s %s\n", a.Name, a.Provider, a.Connection, mcp, a.Path)
	}
	fmt.Printf("\n   %d agent(s) detected · %d wired to Auxly MCP", len(agents), wiredCount)
	if wiredCount < len(agents) {
		fmt.Printf("  —  run `auxly setup` to wire the rest")
	}
	fmt.Println()
	return nil
}
