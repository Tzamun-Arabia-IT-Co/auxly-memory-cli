package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var statsRecall bool

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show agent usage metrics from audit.db",
	RunE:  runStats,
}

func init() {
	statsCmd.Flags().BoolVar(&statsRecall, "recall", false, "show recall-usage analytics: hot files, dead files, fallback rate")
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

	if statsRecall {
		return runRecallStats(logger, memory.NewStore(getMemoryPath()))
	}

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

// runRecallStats renders recall-usage analytics: which files agents actually
// recall from, which never get hit (candidates for pruning), the hottest
// individual facts, and how often recall falls back to non-semantic search
// (a proxy for embeddings being unavailable).
func runRecallStats(logger *audit.Logger, store *memory.Store) error {
	_, totalQueries, err := logger.RecallFallbackRate(30)
	if err != nil {
		return fmt.Errorf("failed to compute recall fallback rate: %w", err)
	}
	if totalQueries == 0 {
		fmt.Println("No recall activity recorded yet — analytics appear after agents start recalling.")
		return nil
	}

	fmt.Println("🔎 Recall usage (accepted hits)")
	fmt.Println("────────────────────────────────")

	fileStats, err := logger.RecallStatsByFile()
	if err != nil {
		return fmt.Errorf("failed to load recall stats by file: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "FILE\t7D\t30D\t90D\tLAST HIT\n")
	seen := make(map[string]bool, len(fileStats))
	for _, s := range fileStats {
		seen[s.File] = true
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n", s.File, s.Hits7, s.Hits30, s.Hits90, humanLastHit(s.LastHit))
	}
	w.Flush()

	// Dead files: vault files that never show up in recall telemetry are
	// pruning candidates. Skip dotfiles, dirs, and the generated unified
	// compile output — none of those are ever recalled by design.
	files, err := store.List()
	if err != nil {
		return fmt.Errorf("failed to list vault files: %w", err)
	}
	var dead []string
	for _, f := range files {
		if f.IsDir || f.Name == "unified_memory.md" || len(f.Name) > 0 && f.Name[0] == '.' {
			continue
		}
		if !seen[f.Name] {
			dead = append(dead, f.Name)
		}
	}
	fmt.Println("\nDead files (zero recall hits):")
	if len(dead) == 0 {
		fmt.Println("  every file has recall hits 🎉")
	} else {
		for _, name := range dead {
			fmt.Printf("  %s\n", name)
		}
	}

	hotFacts, err := logger.HotFacts(30, 5)
	if err != nil {
		return fmt.Errorf("failed to load hot facts: %w", err)
	}
	fmt.Println("\nHot facts (30d):")
	if len(hotFacts) == 0 {
		fmt.Println("  none yet")
	} else {
		for _, hf := range hotFacts {
			fmt.Printf("  %s · %d× (fact %s)\n", hf.File, hf.Hits, hf.LineHash)
		}
	}

	fallbackQueries, totalQueries, err := logger.RecallFallbackRate(30)
	if err != nil {
		return fmt.Errorf("failed to compute recall fallback rate: %w", err)
	}
	pct := 0
	if totalQueries > 0 {
		pct = fallbackQueries * 100 / totalQueries
	}
	fmt.Printf("\nFallback rate (30d): %d/%d queries (%d%%)\n", fallbackQueries, totalQueries, pct)
	if pct > 50 {
		fmt.Println("⚠ high fallback — embeddings often unavailable; check `auxly index status`")
	}

	return nil
}

// humanLastHit renders a coarse "Nd ago" age like humanAge in pendinglist.go,
// but at day granularity since recall history spans up to 90 days.
func humanLastHit(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 24*time.Hour {
		return "today"
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
