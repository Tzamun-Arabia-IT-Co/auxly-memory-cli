package cmd

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   `search "<query>"`,
	Short: "Fuzzy search across all memory files",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearch,
}

func init() {
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	store := memory.NewStore(getMemoryPath())
	results, err := store.Search(args[0])
	if err != nil {
		return err
	}

	if len(results) == 0 {
		fmt.Printf("No results for \"%s\"\n", args[0])
		return nil
	}

	for file, lines := range results {
		fmt.Printf("\n📄 %s\n", file)
		for _, line := range lines {
			fmt.Printf("   %s\n", line)
		}
	}
	return nil
}
