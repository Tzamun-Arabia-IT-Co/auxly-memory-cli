package cmd

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var organizeCmd = &cobra.Command{
	Use:   "organize",
	Short: "Run on-demand memory vault reorganization and consolidation",
	RunE:  runOrganize,
}

func init() {
	rootCmd.AddCommand(organizeCmd)
}

func runOrganize(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	store := memory.NewStore(getMemoryPath())
	// Chunked organize (large vaults) runs one model call per file and can take
	// minutes each — show progress so a headless run never looks hung.
	store.OrganizeProgress = func(current, total int, file string) {
		fmt.Printf("📂 Organizing %s (%d/%d)…\n", file, current, total)
	}

	estTokens := store.GetEstimatedTokens()
	fmt.Printf("🧠 Starting On-Demand Memory Organize...\n")
	fmt.Printf("📊 Estimated Token Cost: ~%d tokens\n", estTokens)
	fmt.Printf("⌛ Contacting active LLM provider for batch consolidation...\n\n")

	res := store.OrganizeVault()
	if !res.Success {
		return fmt.Errorf("organize failed: %s", res.Message)
	}

	fmt.Println(res.Message)
	return nil
}
