package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/tui"
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
	err := rootCmd.Execute()
	notifyUpdateAvailable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// notifyUpdateAvailable prints a one-line "update available" notice to stderr
// after an interactive command. It is suppressed for machine/stream commands
// (mcp-server, connect-mcp) whose output must stay clean, for `update`/`version`
// themselves, and when stderr is not a terminal.
func notifyUpdateAvailable() {
	if !updateNoticeEligible() {
		return
	}
	latest, ok := update.Available()
	if !ok {
		return
	}
	fmt.Fprintf(os.Stderr,
		"\n\033[38;5;220m⬆ auxly %s is available\033[0m (you have %s) — run \033[1mauxly update\033[0m\n",
		latest, update.Current)
}

func updateNoticeEligible() bool {
	for _, a := range os.Args[1:] {
		switch a {
		case "mcp-server", "connect-mcp", "update", "version", "completion",
			"--version", "-v", "host", "connect":
			return false
		}
	}
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func init() {
	// Don't dump the full help banner on a command error, and let Execute() print
	// the error once — keeps the TUI's captured result pane clean.
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	rootCmd.PersistentFlags().StringVar(&cfgPath, "path", "", "Override memory folder path (default: ~/.auxly/memory/)")
	rootCmd.Version = update.Current
	rootCmd.SetVersionTemplate("\r\n🧠 Auxly-Memory CLI Version: {{.Version}}\r\n   ↳ Platform: stdio-native\r\n   ↳ Revision: release-v{{.Version}}\r\n\r\n")
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
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect use"+reset, "Use a host's memory from this machine; disconnect leaves no trace"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"host"+reset, "Serve this machine's memory to a NAT'd remote via a public relay"))
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
