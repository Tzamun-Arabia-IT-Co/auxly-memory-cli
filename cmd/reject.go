package cmd

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var (
	rejectAll   bool
	rejectAgent string
	rejectFile  string
)

var rejectCmd = &cobra.Command{
	Use:   "reject [pending_file]",
	Short: "Reject pending changes (one by name, or in bulk with --all/--agent/--file)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runReject,
}

func init() {
	rejectCmd.Flags().BoolVar(&rejectAll, "all", false, "reject every pending change")
	rejectCmd.Flags().StringVar(&rejectAgent, "agent", "", "reject all pending changes queued by this agent/provider")
	rejectCmd.Flags().StringVar(&rejectFile, "file", "", "reject all pending changes targeting this memory file")
	rootCmd.AddCommand(rejectCmd)
}

func runReject(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	mgr := pending.NewManager(memPath)

	bulk := rejectAll || rejectAgent != "" || rejectFile != ""
	if bulk == (len(args) == 1) {
		return fmt.Errorf("pass exactly one of: a pending name, or --all/--agent/--file")
	}

	names := args
	if bulk {
		var err error
		if names, err = selectPending(mgr, rejectAll, rejectAgent, rejectFile); err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println("Nothing pending matches.")
			return nil
		}
	}

	logger, lerr := audit.NewLogger(memPath)
	if lerr == nil {
		defer logger.Close()
	}

	var firstErr error
	rejected := 0
	for _, name := range names {
		info, infoErr := mgr.Info(name)
		if err := mgr.Reject(name); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			fmt.Printf("❌ %s: %v\n", name, err)
			continue
		}
		rejected++
		if lerr == nil {
			logger.Log("human", "user", "reject", name, "", "Rejected pending change", "auto")
			if infoErr == nil {
				// Log the raw agent id (capture:/organize- prefix intact, no
				// strip-normalization here) — approval_stats.go is the layer
				// that decides which providers' approvals count toward trust
				// evidence, not this call site.
				agent := info.Agent
				if agent == "" {
					agent = "unknown"
				}
				// "pending" records that a queue decision was made, not a trust
				// level — capture/organize pendings queue unconditionally
				// regardless of trust, so "require_approval" here was false.
				logger.Log(agent, agent, "pending_reject", info.Target, "", "human rejected", "pending")
			}
		}
		fmt.Printf("❌ Rejected: %s\n", name)
	}
	if bulk {
		fmt.Printf("\n%d rejected\n", rejected)
	}
	return firstErr
}
