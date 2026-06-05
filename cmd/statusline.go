package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	"github.com/spf13/cobra"
)

var (
	statuslineSegment            bool
	statuslineWrap               bool   // `statusline --wrap` render mode
	statuslineInstallWrap        bool   // `statusline install --wrap` mode (distinct flag owner)
	statuslineInstallEnableUsage bool   // `statusline install --enable-usage`: also opt into Live Usage
	statuslineRefreshUsage       bool   // hidden: refresh the usage cache, then exit (no render)
	statuslineProvider           string // `--provider`: which agent's plan usage to show (default: auto)
	statuslineAgent              string // `install/uninstall --agent`: claude|cursor|antigravity|all
)

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "Render the Auxly statusline for Claude Code, Cursor, or Antigravity (reads session JSON on stdin)",
	Long: `Render the Auxly statusline.

The agent (Claude Code, Cursor CLI, or Antigravity CLI) pipes its session JSON on stdin
and prints this command's output as the statusline. It reads only local/cached data and
never makes a network call.

  auxly statusline                       full multi-line statusline (where · session · memory · usage)
  auxly statusline --provider cursor     render for a specific agent's payload + plan usage
  auxly statusline --segment             only the Auxly memory + plan-usage lines
  auxly statusline --wrap                run the user's backed-up statusline, then append the Auxly segment

Manage the wiring for an agent (additive + reversible):
  auxly statusline install [--agent cursor] [--wrap]   point the agent at Auxly (backs up any prior command)
  auxly statusline install --agent all                 wire every detected agent at once
  auxly statusline uninstall [--agent cursor]          restore the backed-up original (or clear the slot)`,
	RunE: runStatusline,
}

var statuslineInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Wire the Auxly statusline into an agent (additive + reversible)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatuslineInstall()
	},
}

var statuslineUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the Auxly statusline and restore the backed-up original",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatuslineUninstall()
	},
}

func init() {
	statuslineCmd.Flags().BoolVar(&statuslineSegment, "segment", false, "print only the Auxly memory + usage lines")
	statuslineCmd.Flags().BoolVar(&statuslineWrap, "wrap", false, "run the backed-up original statusline, then append the Auxly segment")
	statuslineCmd.Flags().StringVar(&statuslineProvider, "provider", "", "which agent's plan usage to show: claude|cursor|antigravity (default: auto-detect)")
	// --refresh-usage is the detached child the render spawns to keep the usage cache
	// live; it does the networked refresh and exits, printing nothing. Hidden because
	// it's an internal mechanism, not a user-facing mode.
	statuslineCmd.Flags().BoolVar(&statuslineRefreshUsage, "refresh-usage", false, "")
	_ = statuslineCmd.Flags().MarkHidden("refresh-usage")
	statuslineInstallCmd.Flags().BoolVar(&statuslineInstallWrap, "wrap", false, "append Auxly to the agent's existing statusline instead of replacing it")
	statuslineInstallCmd.Flags().BoolVar(&statuslineInstallEnableUsage, "enable-usage", false, "also turn on Live Usage so the statusline's plan-usage line renders")
	statuslineInstallCmd.Flags().StringVar(&statuslineAgent, "agent", "claude", "agent to install for: claude|cursor|antigravity|all")
	statuslineUninstallCmd.Flags().StringVar(&statuslineAgent, "agent", "claude", "agent to uninstall for: claude|cursor|antigravity|all")
	statuslineCmd.AddCommand(statuslineInstallCmd)
	statuslineCmd.AddCommand(statuslineUninstallCmd)
	rootCmd.AddCommand(statuslineCmd)
}

// resolveAgents expands the --agent spec into concrete agent ids. "all" selects every
// agent actually installed on the machine; "" defaults to claude; anything else is
// passed through verbatim (validated by the caller via TargetByName).
func resolveAgents(spec string) []string {
	if strings.EqualFold(strings.TrimSpace(spec), "all") {
		var names []string
		for _, t := range statusline.Targets() {
			if t.Available() {
				names = append(names, t.Name)
			}
		}
		return names
	}
	if strings.TrimSpace(spec) == "" {
		return []string{statusline.ProviderClaude}
	}
	return []string{spec}
}

func runStatuslineInstall() error {
	mode := "full"
	if statuslineInstallWrap {
		mode = "wrap"
	}
	// Optionally turn Live Usage on so the statusline's plan-usage line actually
	// renders (it's network-free at render time but only self-refreshes when Live
	// Usage is opted in). Used by the host to carry its usage preference to a remote.
	if statuslineInstallEnableUsage {
		s := config.LoadSettings()
		if !s.LiveUsage {
			s.LiveUsage = true
			if err := config.SaveSettings(s); err != nil {
				fmt.Printf("⚠ could not enable Live Usage: %v\n", err)
			} else {
				fmt.Println("✓ Live Usage enabled — the statusline usage line will populate on the next renders.")
			}
		}
	}
	agents := resolveAgents(statuslineAgent)
	if len(agents) == 0 {
		fmt.Println("No statusline-capable agents detected on this machine.")
		return nil
	}
	for _, name := range agents {
		t, ok := statusline.TargetByName(name)
		if !ok {
			return fmt.Errorf("unknown agent %q (use claude|cursor|antigravity|all)", name)
		}
		if err := statusline.Install(name, statuslineInstallWrap); err != nil {
			return fmt.Errorf("%s: %w", t.Label, err)
		}
		fmt.Printf("✓ Auxly statusline installed for %s (%s). Reload %s to see it.\n", t.Label, mode, t.Label)
	}
	return nil
}

func runStatuslineUninstall() error {
	agents := resolveAgents(statuslineAgent)
	for _, name := range agents {
		t, ok := statusline.TargetByName(name)
		if !ok {
			return fmt.Errorf("unknown agent %q (use claude|cursor|antigravity|all)", name)
		}
		if err := statusline.Uninstall(name); err != nil {
			return fmt.Errorf("%s: %w", t.Label, err)
		}
		fmt.Printf("✓ Auxly statusline removed for %s; your previous statusline was restored.\n", t.Label)
	}
	return nil
}

func runStatusline(cmd *cobra.Command, args []string) error {
	// Detached child: refresh the usage cache out-of-band, print nothing, exit. This
	// is the only path that touches the network; the render paths below never do.
	if statuslineRefreshUsage {
		statusline.RefreshUsageCache()
		return nil
	}

	raw, _ := io.ReadAll(os.Stdin)
	in := statusline.ReadInput(raw)

	// Prefer the explicit --provider flag (what install bakes in per agent); fall back
	// to sniffing the payload only when it's absent (e.g. a hand-edited command).
	provider := statuslineProvider
	if provider == "" {
		provider = statusline.DetectProvider(in)
	}

	// Wrap mode: run the agent's saved original statusline first (feeding it the same
	// session JSON), print its output, then append the Auxly segment lines.
	//
	// SECURITY NOTE: `orig` is the user's OWN statusline command, read from their
	// local backup file — the exact string they previously had in the agent's
	// statusLine.command, which the agent itself executes via a shell. A statusline
	// command is inherently an arbitrary shell command (pipes, redirects, etc.), so
	// faithfully re-running it REQUIRES a shell. This is trusted, self-authored local
	// config — not runtime or network input — so `sh -c` is the correct mechanism.
	if statuslineWrap {
		if orig := statusline.OriginalCommand(provider); orig != "" {
			var oc *exec.Cmd
			if runtime.GOOS == "windows" {
				oc = exec.Command("cmd", "/c", orig) //nolint:gosec // user's own statusline command, see note
			} else {
				oc = exec.Command("sh", "-c", orig) //nolint:gosec // user's own statusline command, see note
			}
			oc.Stdin = bytes.NewReader(raw)
			oc.Stderr = os.Stderr
			if out, err := oc.Output(); err == nil {
				os.Stdout.Write(trimTrailingNewline(out))
				fmt.Println()
			}
		}
		fmt.Print(statusline.Render(in, false, provider))
		statusline.MaybeRefreshUsage(provider)
		return nil
	}

	fmt.Print(statusline.Render(in, !statuslineSegment, provider))
	statusline.MaybeRefreshUsage(provider)
	return nil
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
