package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var pendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List memory changes waiting for approval",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := pending.NewManager(getMemoryPath())
		if archived, err := mgr.SweepExpired(); err == nil && len(archived) > 0 {
			fmt.Printf("🧹 archived %d expired entr%s to .pending/archive/ (older than TTL)\n\n",
				len(archived), map[bool]string{true: "y", false: "ies"}[len(archived) == 1])
		}
		entries, err := mgr.List()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No pending changes.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "PENDING\tAGENT\tTARGET\tAGE\tCHANGE\n")
		for _, e := range entries {
			info, ierr := mgr.Info(e.Name)
			if ierr != nil {
				fmt.Fprintf(w, "%s\t?\t?\t?\t(unreadable: %v)\n", e.Name, ierr)
				continue
			}
			agent := info.Agent
			if agent == "" {
				agent = "-"
			}
			created := info.Created
			if created.IsZero() {
				created = e.ModTime
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t+%d/−%d\n",
				e.Name, agent, info.Target, humanAge(time.Since(created)), info.Additions, info.Deletions)
		}
		w.Flush()
		fmt.Printf("\n%d pending — `auxly approve <name>` · `auxly approve --all` · `auxly reject --agent <x>`\n", len(entries))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pendingCmd)
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
