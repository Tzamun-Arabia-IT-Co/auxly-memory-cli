package cmd

import (
	"errors"
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var approveForce bool

var approveCmd = &cobra.Command{
	Use:   "approve <pending_file>",
	Short: "Approve a pending change and move it to memory",
	Args:  cobra.ExactArgs(1),
	RunE:  runApprove,
}

func init() {
	approveCmd.Flags().BoolVar(&approveForce, "force", false, "apply even if the target file changed since the pending was created (conflict)")
	rootCmd.AddCommand(approveCmd)
}

func runApprove(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	mgr := pending.NewManager(memPath)

	pendingName := args[0]

	// Show the diff before approving
	content, err := mgr.ViewDiff(pendingName)
	if err != nil {
		return err
	}
	fmt.Printf("📄 Approving: %s\n\n%s\n\n", pendingName, content)

	applyErr := mgr.Approve(pendingName)
	if approveForce && errors.Is(applyErr, pending.ErrConflict) {
		applyErr = mgr.ForceApprove(pendingName)
	}
	if applyErr != nil {
		return applyErr
	}

	// Log audit entry
	logger, err := audit.NewLogger(memPath)
	if err == nil {
		defer logger.Close()
		logger.Log("human", "user", "approve", pendingName, "", "Approved pending change", "auto")
	}

	// Auto-commit if configured
	gitCfg, _ := git.LoadConfig(memPath)
	if gitCfg != nil && gitCfg.AutoCommit {
		git.AutoCommit(memPath, pendingName, "Approved pending change")
	}

	fmt.Printf("✅ Approved and applied: %s\n", pendingName)
	return nil
}
