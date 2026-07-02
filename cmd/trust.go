package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	"github.com/spf13/cobra"
)

var trustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Manage provider trust levels",
}

var trustListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all provider trust levels",
	RunE:  runTrustList,
}

var trustSetCmd = &cobra.Command{
	Use:   "set <provider> <level>",
	Short: "Set trust level for a provider (auto, require_approval, read_only)",
	Args:  cobra.ExactArgs(2),
	RunE:  runTrustSet,
}

func init() {
	trustCmd.AddCommand(trustListCmd)
	trustCmd.AddCommand(trustSetCmd)
	rootCmd.AddCommand(trustCmd)
}

func runTrustList(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	cfg, err := trust.Load(memPath)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "PROVIDER\tTRUST LEVEL\n")
	fmt.Fprintf(w, "────────\t───────────\n")
	for provider, pc := range cfg.Providers {
		fmt.Fprintf(w, "%s\t%s\n", provider, pc.TrustLevel)
	}
	fmt.Fprintf(w, "\n(default)\t%s\n", cfg.Default)
	w.Flush()
	return nil
}

func runTrustSet(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	cfg, err := trust.Load(memPath)
	if err != nil {
		return err
	}

	provider := args[0]
	level := args[1]
	oldLevel := cfg.GetTrustLevel(provider)

	if err := cfg.SetTrustLevel(provider, level); err != nil {
		return err
	}

	if err := cfg.Save(memPath); err != nil {
		return err
	}

	// Trust changes gate every future write — they belong in the same audit
	// trail as the writes they authorize.
	if logger, err := audit.NewLogger(memPath); err == nil {
		defer logger.Close()
		logger.Log("human", "user", "trust_change", "trust.yaml", "",
			fmt.Sprintf("%s: %s → %s", provider, oldLevel, level), level)
	}

	fmt.Printf("✅ Set %s trust level to: %s (was %s)\n", provider, level, oldLevel)
	return nil
}
