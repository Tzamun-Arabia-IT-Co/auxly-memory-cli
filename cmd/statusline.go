package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	"github.com/spf13/cobra"
)

var (
	statuslineSegment      bool
	statuslineWrap         bool // `statusline --wrap` render mode
	statuslineInstallWrap  bool // `statusline install --wrap` mode (distinct flag owner)
	statuslineRefreshUsage bool // hidden: refresh the usage cache, then exit (no render)
)

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "Render the Auxly statusline for Claude Code (reads session JSON on stdin)",
	Long: `Render the Auxly statusline.

Claude Code pipes its session JSON on stdin and prints this command's output as the
statusline. It reads only local/cached data and never makes a network call.

  auxly statusline            full multi-line statusline (where · session · memory · usage)
  auxly statusline --segment  only the Auxly memory + plan-usage lines
  auxly statusline --wrap     run the user's backed-up statusline, then append the Auxly segment

Manage the Claude Code wiring (additive + reversible):
  auxly statusline install [--wrap]   point Claude Code at Auxly (backs up any prior command)
  auxly statusline uninstall          restore the backed-up original (or clear the slot)`,
	RunE: runStatusline,
}

var statuslineInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Wire the Auxly statusline into Claude Code (additive + reversible)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := statusline.Install(statuslineInstallWrap); err != nil {
			return err
		}
		mode := "full"
		if statuslineInstallWrap {
			mode = "wrap"
		}
		fmt.Printf("✓ Auxly statusline installed (%s). Reload Claude Code to see it.\n", mode)
		return nil
	},
}

var statuslineUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the Auxly statusline and restore the backed-up original",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := statusline.Uninstall(); err != nil {
			return err
		}
		fmt.Println("✓ Auxly statusline removed; your previous statusline was restored.")
		return nil
	},
}

func init() {
	statuslineCmd.Flags().BoolVar(&statuslineSegment, "segment", false, "print only the Auxly memory + usage lines")
	statuslineCmd.Flags().BoolVar(&statuslineWrap, "wrap", false, "run the backed-up original statusline, then append the Auxly segment")
	// --refresh-usage is the detached child the render spawns to keep the usage cache
	// live; it does the networked refresh and exits, printing nothing. Hidden because
	// it's an internal mechanism, not a user-facing mode.
	statuslineCmd.Flags().BoolVar(&statuslineRefreshUsage, "refresh-usage", false, "")
	_ = statuslineCmd.Flags().MarkHidden("refresh-usage")
	statuslineInstallCmd.Flags().BoolVar(&statuslineInstallWrap, "wrap", false, "append Auxly to the user's existing statusline instead of replacing it")
	statuslineCmd.AddCommand(statuslineInstallCmd)
	statuslineCmd.AddCommand(statuslineUninstallCmd)
	rootCmd.AddCommand(statuslineCmd)
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

	// Wrap mode: run the user's saved original statusline first (feeding it the same
	// session JSON), print its output, then append the Auxly segment lines.
	//
	// SECURITY NOTE: `orig` is the user's OWN statusline command, read from their
	// local backup file (~/.auxly/cc-statusline-original.txt) — the exact string they
	// previously had in ~/.claude/settings.json's statusLine.command, which Claude
	// Code itself executes via a shell. A statusline command is inherently an
	// arbitrary shell command (pipes, redirects, etc.), so faithfully re-running it
	// REQUIRES a shell. This is trusted, self-authored local config — not runtime or
	// network input — so `sh -c` is the correct and intended mechanism here.
	if statuslineWrap {
		if orig := statusline.OriginalCommand(); orig != "" {
			oc := exec.Command("sh", "-c", orig) //nolint:gosec // user's own statusline command, see note
			oc.Stdin = bytes.NewReader(raw)
			oc.Stderr = os.Stderr
			if out, err := oc.Output(); err == nil {
				os.Stdout.Write(trimTrailingNewline(out))
				fmt.Println()
			}
		}
		fmt.Print(statusline.Render(in, false))
		statusline.MaybeRefreshUsage()
		return nil
	}

	fmt.Print(statusline.Render(in, !statuslineSegment))
	statusline.MaybeRefreshUsage()
	return nil
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
