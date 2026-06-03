package cmd

import (
	"fmt"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/spf13/cobra"
)

var tailLines int

var tailCmd = &cobra.Command{
	Use:   "tail",
	Short: "Live stream of .audit.log",
	RunE:  runTail,
}

func init() {
	tailCmd.Flags().IntVarP(&tailLines, "lines", "n", 20, "Number of recent entries to show")
	rootCmd.AddCommand(tailCmd)
}

func runTail(cmd *cobra.Command, args []string) error {
	logger, err := audit.NewLogger(getMemoryPath())
	if err != nil {
		return err
	}
	defer logger.Close()

	entries, err := logger.Tail(tailLines)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Println("No audit entries yet.")
		return nil
	}

	for _, e := range entries {
		ts, _ := time.Parse(time.RFC3339, e.Timestamp)
		fmt.Printf("[%s] %s/%s %s %s — %s\n",
			ts.Format("15:04:05"),
			e.Provider,
			e.AgentID,
			e.Action,
			e.File,
			e.Reason,
		)
	}
	return nil
}
