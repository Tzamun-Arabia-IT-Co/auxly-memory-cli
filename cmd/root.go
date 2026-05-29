package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/tui"
	"github.com/spf13/cobra"
)

var cfgPath string

var rootCmd = &cobra.Command{
	Use:   "auxly",
	Short: "auxly-cli — Local-first unified memory system for AI agents",
	Long: `auxly-cli is a local-first, file-based unified memory system that all AI providers
can read and write to in a controlled, auditable, and human-reviewable way.

Supports Claude, Claude Code, Codex, Gemini, Copilot, Antigravity, and any CLI-based agent.`,
	Run: func(cmd *cobra.Command, args []string) {
		memPath := getMemoryPath()
		if tui.IsInitialized(memPath) {
			tui.Run(memPath)
		} else {
			// Automatically check and install missing dependencies (Node.js)
			checkAndInstallDependencies()

			tui.RunWizard(memPath)
			// Ensure memory files are populated after wizard
			// (TUI closure may not reliably write files)
			ensurePopulated(memPath)
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// Don't dump the full help banner on a command error, and let Execute() print
	// the error once — keeps the TUI's captured result pane clean.
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	rootCmd.PersistentFlags().StringVar(&cfgPath, "path", "", "Override memory folder path (default: ~/.auxly/memory/)")
	rootCmd.Version = "1.0.0"
	rootCmd.SetVersionTemplate("\r\n🧠 Auxly-Memory CLI Version: 1.0.0\r\n   ↳ Platform: stdio-native\r\n   ↳ Revision: release-v1.0.0\r\n\r\n")
	rootCmd.SetHelpFunc(customHelpFunc)
	rootCmd.SetUsageFunc(func(cmd *cobra.Command) error {
		customHelpFunc(cmd, nil)
		return nil
	})
}

func customHelpFunc(cmd *cobra.Command, args []string) {
	cyan := "\033[38;5;38m"
	purple := "\033[38;5;134m"
	green := "\033[38;5;34m"
	bold := "\033[1m"
	dim := "\033[38;5;240m"
	reset := "\033[0m"

	var sb strings.Builder

	sb.WriteString("\r\n")
	sb.WriteString(bold + purple + "🧠 AUXLY UNIFIED AGENT MEMORY CLI" + reset + "\r\n")
	sb.WriteString(dim + "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" + reset + "\r\n")
	sb.WriteString("auxly-cli is a local-first, file-based unified memory system that all AI\r\n")
	sb.WriteString("providers can read and write to in a controlled, auditable way.\r\n\r\n")

	sb.WriteString(bold + "Usage:" + reset + "\r\n")
	sb.WriteString("  auxly [flags]\r\n")
	sb.WriteString("  auxly [command]\r\n\r\n")

	sb.WriteString(bold + "Core Commands:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"ui"+reset, "Launch the interactive TUI dashboard (Tab 1-8)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"init"+reset, "Run the onboarding wizard and configure local settings"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"setup"+reset, "Configure auxly-cli MCP server for detected agents"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"update"+reset, "Check for updates and automatically rebuild/install"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"version"+reset, "Print the version number of auxly-cli"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Memory Management:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"list"+reset, "List all active memory files in the vault"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"view"+reset, "Print the contents of a specific memory file"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"write"+reset, "Write a change/diff to memory (respects trust levels)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"sync"+reset, "Git commit and push memory changes to remote"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"search"+reset, "Fuzzy search across all memory files"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"populate"+reset, "Auto-detect system profile and populate files"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Remote Access:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect"+reset, "Interactive wizard to link a remote memory host over SSH"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect list"+reset, "List configured remote hosts"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect remove"+reset, "Remove a configured remote host"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect test"+reset, "Reachability + host-auxly dependency doctor"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect print"+reset, "Print the MCP JSON block (manual fallback)"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Audit & Pending Queue:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"stats"+reset, "Show agent usage metrics from audit.db"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"tail"+reset, "Live stream the .audit.log"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"approve"+reset, "Approve a pending memory change"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"reject"+reset, "Reject and delete a pending memory change"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Flags:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", green+"-h, --help"+reset, "help for auxly"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", green+"-v, --version"+reset, "version for auxly"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", green+"--path string"+reset, "Override memory folder path (default: ~/.auxly/memory/)"))
	sb.WriteString("\r\n")

	sb.WriteString("Use " + bold + "auxly [command] --help" + reset + " for more information about a command.\r\n")

	fmt.Print(sb.String())
}

func getMemoryPath() string {
	if cfgPath != "" {
		return cfgPath
	}
	return config.ResolveMemoryPath()
}

func ensurePopulated(memPath string) {
	// If identity.md is missing or still a template, run populate
	identityPath := memPath + "/identity.md"
	data, err := os.ReadFile(identityPath)
	if err != nil || len(data) < 200 || !strings.Contains(string(data), "auto-detected") {
		runPopulate(nil, nil)
	}
}
