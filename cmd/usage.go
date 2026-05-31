package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/usage"
	"github.com/spf13/cobra"
)

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Live agent usage / quota (opt-in; calls each provider with its stored login)",
	Long: "Read live session/week quota for the AI agents Auxly tracks, reusing each\n" +
		"agent's own stored login token. This makes outbound network calls and is\n" +
		"opt-in; enable the dashboard panel in Settings. Antigravity needs a one-time\n" +
		"login: `auxly usage auth antigravity`.",
}

var usageShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print current usage for every agent",
	Run: func(cmd *cobra.Command, args []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		now := time.Now()
		for _, r := range usage.New().Reports(ctx) {
			if r.Err != "" {
				fmt.Printf("  %-13s —  %s\n", r.Provider, r.Err)
				continue
			}
			id := r.Account
			if r.Plan != "" {
				if id != "" {
					id += "  ·  "
				}
				id += r.Plan
			}
			if r.Org != "" {
				id += "  ·  " + r.Org
			}
			if id == "" {
				id = "(no account info)"
			}
			fmt.Printf("  %-13s %s\n", r.Provider, id)
			for _, w := range r.Windows {
				reset := usage.FormatReset(w.ResetAt, now)
				if reset != "" {
					reset = "resets " + reset
				}
				fmt.Printf("      %-8s %5.0f%%  %s\n", w.Label, w.Pct, reset)
			}
		}
	},
}

var usageAuthCmd = &cobra.Command{
	Use:   "auth [antigravity]",
	Short: "Authorize a provider that needs its own login (currently: antigravity)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "antigravity" {
			return fmt.Errorf("unknown provider %q (only 'antigravity' needs separate auth)", args[0])
		}
		fmt.Println("Opening your browser to authorize Antigravity…")
		fmt.Println("Approve the Google consent screen, then return here.")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		email, err := usage.AntigravityLogin(ctx)
		if err != nil {
			return fmt.Errorf("antigravity authorization failed: %w", err)
		}
		if email != "" {
			fmt.Printf("\n✅ Antigravity connected as %s. Usage will now appear in the dashboard.\n", email)
		} else {
			fmt.Println("\n✅ Antigravity connected. Usage will now appear in the dashboard.")
		}
		return nil
	},
}

func init() {
	usageCmd.AddCommand(usageShowCmd)
	usageCmd.AddCommand(usageAuthCmd)
	rootCmd.AddCommand(usageCmd)
}
