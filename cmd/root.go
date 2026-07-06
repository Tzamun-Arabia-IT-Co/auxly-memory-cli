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
			go selfHealKeepAlive() // background: never delay the dashboard
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
	// Opt-in auto-update: apply the new release IN PLACE now that the interactive
	// command has finished (we're past the command run — never mid-session, and the
	// hot statusline path is excluded by updateNoticeEligible). The current process
	// already ran on the old binary; the next launch picks up the new one. Best-effort
	// — a failed self-update falls through to the manual notice.
	if config.LoadSettings().AutoUpdate {
		if _, err := update.SelfUpdate(); err == nil {
			fmt.Fprintf(os.Stderr,
				"\n\033[38;5;220m⬆ auxly auto-updated to %s\033[0m (was %s) — the next launch uses it.\n",
				latest, update.Current)
			return
		}
		// fall through to the manual notice if the self-update failed.
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
	fmt.Print(helpText())
}

// helpText renders the full custom help. Kept as a pure function so
// TestHelpListsEveryCommand can walk rootCmd.Commands() against it — a new
// command that isn't added here fails CI instead of shipping undiscoverable.
func helpText() string {
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
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"agents"+reset, "List detected agents and their wiring status"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"doctor"+reset, "One-screen health check of your Auxly install"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"statusline"+reset, "Install/manage the Auxly agent statusline"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"update"+reset, "Check for updates and automatically rebuild/install"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"version"+reset, "Print the version number of auxly-cli"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Memory Management:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"note"+reset, "Quick-capture a thought into inbox.md (alias: q)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"todo"+reset, "Shared todo list you and your agents share (tasks.md)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"list"+reset, "List all active memory files in the vault"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"view"+reset, "Print the contents of a specific memory file"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"write"+reset, "Write a change/diff to memory (respects trust levels)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"sync"+reset, "Git commit and push memory changes to remote"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"search"+reset, "Fuzzy search across all memory files"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"organize"+reset, "LLM re-file/tidy of the whole vault (review before apply)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"index"+reset, "Build/rebuild the semantic recall index"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"export"+reset, "Export the memory vault (backup/share)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"populate"+reset, "Auto-detect system profile and populate files"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"review"+reset, "Review stale facts (old + unrecalled) — keep or archive"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"encrypt"+reset, "Encrypt memory files at rest (init/file/status)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"decrypt"+reset, "Remove encryption-at-rest from a memory file"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Remote Access:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect"+reset, "Interactive wizard to link a remote memory host over SSH"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect list"+reset, "List configured remote hosts"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect remove"+reset, "Remove a configured remote host"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect test"+reset, "Reachability + host-auxly dependency doctor"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect print"+reset, "Print the MCP JSON block (manual fallback)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"connect use"+reset, "Use a host's memory from this machine; disconnect leaves no trace"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"host"+reset, "Serve this machine's memory to a NAT'd remote via a public relay"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"host invite"+reset, "Mint a one-time token so a box can `auxly join` over direct SSH"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"join"+reset, "Pair with a host's memory using a token from `auxly host invite`"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Audit & Pending Queue:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"stats"+reset, "Show agent usage metrics from audit.db"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"usage"+reset, "Live provider usage/quota meters"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"tail"+reset, "Live stream the .audit.log"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"capture"+reset, "Extract session facts into the pending queue (LLM, never direct writes)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"hooks"+reset, "Install/remove the Claude Code auto-capture Stop hook (opt-in)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"pending"+reset, "List memory changes waiting for approval (agent, target, age)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"approve"+reset, "Approve pending changes (--force, or bulk --all/--agent/--file)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"reject"+reset, "Reject pending changes (one, or bulk --all/--agent/--file)"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", cyan+"trust"+reset, "View/set per-agent trust levels (auto/approval/read-only); `trust suggest` recommends changes"))
	sb.WriteString("\r\n")

	sb.WriteString(bold + "Flags:" + reset + "\r\n")
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", green+"-h, --help"+reset, "help for auxly"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", green+"-v, --version"+reset, "version for auxly"))
	sb.WriteString(fmt.Sprintf("  %-12s %s\r\n", green+"--path string"+reset, "Override memory folder path (default: ~/.auxly/memory/)"))
	sb.WriteString("\r\n")

	sb.WriteString("Use " + bold + "auxly [command] --help" + reset + " for more information about a command.\r\n")

	return sb.String()
}

// requireInit gates read commands on an initialized vault, replacing raw file
// errors with a friendly pointer. Shared by view/search/stats/export/organize.
// An explicit --path override bypasses the gate: pointing at a foreign vault
// (restored backup, shared dir) that never ran `auxly init` is a legitimate
// read — the marker only guards the DEFAULT path's first-run confusion.
func requireInit() error {
	if cfgPath != "" {
		return nil
	}
	memPath := getMemoryPath()
	if !tui.IsInitialized(memPath) {
		return fmt.Errorf("memory not initialized — run `auxly init` first (it creates %s, detects your agents, and wires their MCP configs)", memPath)
	}
	return nil
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
