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

var trustSuggestCmd = &cobra.Command{
	Use:   "suggest",
	Short: "Suggest trust-level changes from 90d approval history (never auto-applied)",
	RunE:  runTrustSuggest,
}

func init() {
	trustCmd.AddCommand(trustListCmd)
	trustCmd.AddCommand(trustSetCmd)
	trustCmd.AddCommand(trustSuggestCmd)
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

// runTrustSuggest evaluates 90-day approval history and prints suggested trust
// changes. This is read-only by design: trust levels are a security boundary,
// so a human always applies the change (`auxly trust set ...`), with the
// evidence right in front of them — never a background auto-apply.
func runTrustSuggest(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	cfg, err := trust.Load(memPath)
	if err != nil {
		return err
	}
	if !cfg.TuningEnabled() {
		fmt.Println("trust tuning is off (trust.yaml: tuning: off)")
		return nil
	}

	logger, err := audit.NewLogger(memPath)
	if err != nil {
		return err
	}
	defer logger.Close()

	stats, _ := logger.ApprovalStats(90)
	suggestions := trust.SuggestChanges(cfg, stats)

	if len(suggestions) == 0 {
		fmt.Println("✅ No trust changes suggested — current levels match the evidence.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "PROVIDER\tCURRENT\tSUGGESTED\tEVIDENCE\n")
	fmt.Fprintf(w, "────────\t───────\t─────────\t────────\n")
	for _, s := range suggestions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Provider, s.Current, s.Suggested, s.Evidence)
	}
	w.Flush()
	fmt.Println("\napply with: auxly trust set <provider> <level>   (suggestions are never auto-applied)")
	return nil
}
