package cmd

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/memory"
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
	store := memory.NewStore(getMemoryPath())
	
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
