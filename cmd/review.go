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

var (
	reviewIncludePersonal bool
	reviewKeepAll         bool
	reviewArchiveAll      bool
	reviewLimit           int
)

var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Review stale facts — old AND unrecalled memory goes nowhere without your say-so",
	RunE:  runReview,
}

func init() {
	reviewCmd.Flags().BoolVar(&reviewIncludePersonal, "include-personal", false, "include personal facts (they don't decay like infra facts by default)")
	reviewCmd.Flags().BoolVar(&reviewKeepAll, "keep-all", false, "re-stamp everything listed (resets the clock)")
	reviewCmd.Flags().BoolVar(&reviewArchiveAll, "archive-all", false, "move everything listed to .archive/ (never deleted)")
	reviewCmd.Flags().IntVar(&reviewLimit, "limit", 0, "cap the number of facts reviewed/acted on, oldest first (0 = all)")
	rootCmd.AddCommand(reviewCmd)
}

func runReview(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	// One queue of facts, one action — doing both at once is ambiguous.
	if reviewKeepAll && reviewArchiveAll {
		return fmt.Errorf("pick one of --keep-all / --archive-all")
	}

	memPath := getMemoryPath()
	store := memory.NewStore(memPath)

	// audit.db is best-effort context (last-recall dates), not a hard
	// dependency — a missing/unreadable log just means everything looks
	// "never recalled" rather than blocking the command.
	logger, _ := audit.NewLogger(memPath)
	var lastRecall func(string) (map[string]time.Time, error)
	if logger != nil {
		defer logger.Close()
		lastRecall = logger.LastRecallByLine
	}

	facts, err := store.StaleFacts(lastRecall, reviewIncludePersonal)
	if err != nil {
		return err
	}
	if len(facts) == 0 {
		fmt.Println("✅ Nothing stale — every fact is either fresh or recently recalled.")
		return nil
	}

	// --limit truncates AFTER the scan, so the oldest-first order StaleFacts
	// returns is preserved for both the printed list and any bulk action.
	if reviewLimit > 0 && reviewLimit < len(facts) {
		facts = facts[:reviewLimit]
	}

	switch {
	case reviewKeepAll:
		return reviewKeepAllFacts(store, logger, facts)
	case reviewArchiveAll:
		return reviewArchiveAllFacts(store, memPath, logger, facts)
	default:
		printReviewTable(facts)
		fmt.Printf("\n%d stale fact(s)\n", len(facts))
		fmt.Println("  auxly review --keep-all      re-stamp everything listed (resets the clock)")
		fmt.Println("  auxly review --archive-all   move everything listed to .archive/ (never deleted)")
		fmt.Println("  (or open the dashboard's Review tab to act per fact)")
		return nil
	}
}

// printReviewTable renders the numbered stale-fact table shared by the
// default view and the --archive-all confirmation printout.
func printReviewTable(facts []memory.StaleFact) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "#\tFILE\tFACT\tAGE\tLAST RECALL\n")
	for i, f := range facts {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
			i+1, f.File, truncateFact(f.Line, 60), factAge(f.FactDate, "undated"), factAge(f.LastRecall, "never"))
	}
	w.Flush()
}

// truncateFact shortens a fact line for table display. Runes, not bytes, so
// it can't split a multi-byte character mid-way.
func truncateFact(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// factAge renders a StaleFact date with humanAge, or zeroLabel when the date
// is the zero value (undated fact, or never recalled).
func factAge(t time.Time, zeroLabel string) string {
	if t.IsZero() {
		return zeroLabel
	}
	return humanAge(time.Since(t))
}

// reviewKeepAllFacts re-dates every listed fact in place. Per-fact errors are
// tolerated and counted rather than aborting the run — the vault can change
// between the scan and the action (edited, already moved, etc). Logging is
// best-effort: a review action is a human decision that mutates the vault and
// belongs in the same audit trail as any other write.
func reviewKeepAllFacts(store *memory.Store, logger *audit.Logger, facts []memory.StaleFact) error {
	kept, skipped := 0, 0
	for _, f := range facts {
		if err := store.RestampFact(f.File, f.Line); err != nil {
			fmt.Printf("❌ %s: %v\n", f.File, err)
			skipped++
			continue
		}
		kept++
		if logger != nil {
			logger.Log("human", "dashboard", "review_keep", f.File, "", f.Line, "auto")
		}
	}
	fmt.Printf("✓ re-stamped %d fact(s), %d skipped (changed since scan)\n", kept, skipped)
	// Automation (cron'd `auxly review --keep-all`) must be able to tell "did
	// anything actually happen" from the exit code — a 0-applied run reporting
	// success hides a vault that changed out from under the scan.
	if kept == 0 && skipped > 0 {
		return fmt.Errorf("no facts applied — %d skipped (vault changed since scan?)", skipped)
	}
	return nil
}

// reviewArchiveAllFacts moves every listed fact to .archive/. Shows the full
// list before acting since archiving, unlike a re-stamp, moves the fact out
// of the active file. Same error tolerance and audit logging as
// reviewKeepAllFacts.
func reviewArchiveAllFacts(store *memory.Store, memPath string, logger *audit.Logger, facts []memory.StaleFact) error {
	printReviewTable(facts)
	fmt.Printf("\nArchiving %d fact(s) to %s/.archive/ — they are never deleted and stay greppable.\n\n", len(facts), memPath)

	archived, skipped := 0, 0
	for _, f := range facts {
		if err := store.ArchiveFact(f.File, f.Line); err != nil {
			fmt.Printf("❌ %s: %v\n", f.File, err)
			skipped++
			continue
		}
		archived++
		if logger != nil {
			logger.Log("human", "dashboard", "review_archive", f.File, "", f.Line, "auto")
		}
	}
	fmt.Printf("✓ archived %d fact(s), %d skipped (changed since scan)\n", archived, skipped)
	if archived == 0 && skipped > 0 {
		return fmt.Errorf("no facts applied — %d skipped (vault changed since scan?)", skipped)
	}
	return nil
}
