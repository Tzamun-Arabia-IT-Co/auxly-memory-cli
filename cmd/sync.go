package cmd

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Git commit and push memory changes to remote",
	RunE:  runSync,
}

func init() {
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()

	fmt.Println("🔄 Syncing memory to remote...")
	if err := git.Sync(memPath); err != nil {
		return err
	}

	fmt.Println("✅ Memory synced successfully.")
	return nil
}
