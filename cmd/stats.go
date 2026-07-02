package cmd

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show agent usage metrics from audit.db",
	RunE:  runStats,
}

func init() {
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	logger, err := audit.NewLogger(getMemoryPath())
	if err != nil {
		return err
	}
	defer logger.Close()

	stats, err := logger.Stats()
	if err != nil {
		return err
	}

	fmt.Println("📊 Auxly Memory Stats")
	fmt.Println("─────────────────────")
	fmt.Printf("Total entries:  %d\n", stats.TotalEntries)
	fmt.Printf("Writes today:   %d\n", stats.WritesToday)

	if len(stats.ByProvider) > 0 {
		fmt.Println("\nBy Provider:")
		for provider, count := range stats.ByProvider {
			fmt.Printf("  %-15s %d\n", provider, count)
		}
	}

	if len(stats.ByAction) > 0 {
		fmt.Println("\nBy Action:")
		for action, count := range stats.ByAction {
			fmt.Printf("  %-15s %d\n", action, count)
		}
	}

	return nil
}
