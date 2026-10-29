package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Back up memory changes to the configured git remote",
	Args:  cobra.NoArgs,
	RunE:  runSync,
}

var syncSetupCmd = &cobra.Command{
	Use:   "setup [<url>]",
	Short: "Set up git backup to a remote URL or a new private GitHub repository",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runSyncSetup,
}

var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show git backup remote, auto-backup state, last backup, and unpushed/dirty counts",
	Args:  cobra.NoArgs,
	RunE:  runSyncStatus,
}

var syncAutoCmd = &cobra.Command{
	Use:   "auto <on|off>",
	Short: "Enable or disable automatic background git backup",
	Args:  cobra.ExactArgs(1),
	RunE:  runSyncAuto,
}

func init() {
	syncSetupCmd.Flags().Bool("yes", false, "Confirm initial push even when pre-push checks report sensitive plaintext files or existing commits")
	syncSetupCmd.Flags().Bool("allow-public", false, "Allow backing up to a public repository")
	syncSetupCmd.Flags().String("create-github", "", "Create a private GitHub repository with this name via `gh` and use it as the remote")

	syncCmd.AddCommand(syncSetupCmd, syncStatusCmd, syncAutoCmd)
	rootCmd.AddCommand(syncCmd)
}

func resetSyncSetupFlags(cmd *cobra.Command) {
	for _, name := range []string{"yes", "allow-public"} {
		if f := cmd.Flags().Lookup(name); f != nil {
			_ = f.Value.Set("false")
			f.Changed = false
		}
	}
	if f := cmd.Flags().Lookup("create-github"); f != nil {
		_ = f.Value.Set("")
		f.Changed = false
	}
}

func oneLineGitErr(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

func runSync(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "🔄 Syncing memory to remote...")
	res, err := git.BackupNow(cmd.Context(), memPath)
	if err != nil {
		return err
	}
	if !res.Pushed {
		fmt.Fprintln(out, "✅ Memory synced successfully (already up to date).")
		return nil
	}
	fmt.Fprintln(out, "✅ Memory synced successfully.")
	return nil
}

func printPrePushReport(out io.Writer, rep git.Report) {
	if len(rep.SensitiveFiles) > 0 {
		fmt.Fprintln(out, "⚠ Pre-push check: unencrypted vault file(s) contain potential secrets:")
		for _, sf := range rep.SensitiveFiles {
			fmt.Fprintf(out, "   • %s (%d match(es))\n", sf.File, sf.Count)
		}
	}
	if rep.ExistingCommits > 0 {
		fmt.Fprintf(out, "⚠ Pre-push check: %d earlier commits will be pushed as they are; older commits are not checked for secrets\n", rep.ExistingCommits)
	}
	switch rep.Visibility {
	case git.VisibilityPublic:
		fmt.Fprintln(out, "⚠ Pre-push check: remote repository is PUBLIC.")
	case git.VisibilityUnknown:
		if rep.Warning != "" {
			fmt.Fprintf(out, "⚠ %s\n", rep.Warning)
		}
	}
}

func runSyncSetup(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	allowPublic, _ := cmd.Flags().GetBool("allow-public")
	createGH, _ := cmd.Flags().GetString("create-github")
	defer resetSyncSetupFlags(cmd)

	createGH = strings.TrimSpace(createGH)
	if createGH != "" && len(args) > 0 {
		return fmt.Errorf("pass either a remote <url> or --create-github <name>, not both")
	}
	if createGH == "" && len(args) != 1 {
		return fmt.Errorf("usage: auxly sync setup <url> [--yes] [--allow-public]  or  auxly sync setup --create-github <name> [--yes]")
	}

	memPath := getMemoryPath()
	out := cmd.OutOrStdout()
	opts := git.SetupOptions{
		Confirmed:   yes,
		AllowPublic: allowPublic,
	}

	var (
		res git.SetupResult
		err error
	)
	if createGH != "" {
		res, err = git.CreateGitHubAndSetup(cmd.Context(), memPath, createGH, opts)
	} else {
		res, err = git.Setup(cmd.Context(), memPath, args[0], opts)
	}

	printPrePushReport(out, res.Report)

	if err != nil {
		if errors.Is(err, git.ErrPrePushBlocked) {
			if res.Report.IsPublic() && !allowPublic {
				fmt.Fprintln(out, "   Stopped: public repositories require --allow-public.")
			}
			if res.Report.HasSensitiveFiles() && !yes {
				fmt.Fprintln(out, "   Stopped: encrypt sensitive files (`auxly encrypt file <name>`) or pass --yes to push anyway.")
			}
			if res.Report.ExistingCommits > 0 && !yes {
				fmt.Fprintln(out, "   Stopped: pass --yes to push earlier commits anyway.")
			}
		}
		return err
	}

	fmt.Fprintf(out, "✅ Backup configured → %s (branch %s)\n", res.RemoteURL, res.Branch)
	return nil
}

func runSyncStatus(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	out := cmd.OutOrStdout()
	st := git.BackupStatus(memPath)

	autoLabel := "OFF"
	if st.AutoBackup {
		autoLabel = "ON"
	}

	if !st.Configured {
		fmt.Fprintln(out, "📦 Backup (git): not set up")
		fmt.Fprintf(out, "   Auto-backup: %s\n", autoLabel)
		fmt.Fprintln(out, "   Set up with: auxly sync setup <url>  or  auxly sync setup --create-github auxly-vault")
		return nil
	}

	fmt.Fprintln(out, "📦 Backup (git)")
	fmt.Fprintf(out, "   Remote:           %s\n", st.RemoteURL)
	fmt.Fprintf(out, "   Branch:           %s\n", st.Branch)
	fmt.Fprintf(out, "   Auto-backup:      %s\n", autoLabel)
	fmt.Fprintf(out, "   Unpushed commits: %d\n", st.UnpushedCount)
	fmt.Fprintf(out, "   Dirty files:      %d\n", st.DirtyFileCount)
	if st.LastSuccess.IsZero() {
		fmt.Fprintln(out, "   Last backup:      never")
	} else {
		fmt.Fprintf(out, "   Last backup:      %s\n", st.LastSuccess.Local().Format(time.RFC3339))
	}
	if !st.LastAttempt.IsZero() {
		fmt.Fprintf(out, "   Last attempt:     %s\n", st.LastAttempt.Local().Format(time.RFC3339))
	}
	if st.LastError != "" {
		fmt.Fprintf(out, "   Last error:       %s\n", strings.Join(strings.Fields(st.LastError), " "))
	}
	return nil
}

func runSyncAuto(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	out := cmd.OutOrStdout()

	var enable bool
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on":
		enable = true
	case "off":
		enable = false
	default:
		return fmt.Errorf("expected 'on' or 'off', got %q", args[0])
	}

	cfg, err := git.LoadConfig(memPath)
	if err != nil {
		return err
	}
	cfg.AutoPush = enable
	if err := git.SaveConfig(memPath, cfg); err != nil {
		return err
	}

	if enable {
		fmt.Fprintln(out, "✅ Auto-backup: ON")
	} else {
		fmt.Fprintln(out, "✅ Auto-backup: OFF")
	}
	return nil
}
