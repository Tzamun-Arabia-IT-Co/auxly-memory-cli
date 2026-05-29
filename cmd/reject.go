package cmd

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/pending"
	"github.com/spf13/cobra"
)

var rejectCmd = &cobra.Command{
	Use:   "reject <pending_file>",
	Short: "Reject and delete a pending change",
	Args:  cobra.ExactArgs(1),
	RunE:  runReject,
}

func init() {
	rootCmd.AddCommand(rejectCmd)
}

func runReject(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	mgr := pending.NewManager(memPath)

	pendingName := args[0]

	if err := mgr.Reject(pendingName); err != nil {
		return err
	}

	// Log audit entry
	logger, err := audit.NewLogger(memPath)
	if err == nil {
		defer logger.Close()
		logger.Log("human", "user", "reject", pendingName, "", "Rejected pending change", "auto")
	}

	fmt.Printf("❌ Rejected: %s\n", pendingName)
	return nil
}
