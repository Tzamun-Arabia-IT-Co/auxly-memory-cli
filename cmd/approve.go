package cmd

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var (
	approveForce bool
	approveAll   bool
	approveAgent string
	approveFile  string
)

var approveCmd = &cobra.Command{
	Use:   "approve [pending_file]",
	Short: "Approve pending changes (one by name, or in bulk with --all/--agent/--file)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runApprove,
}

func init() {
	approveCmd.Flags().BoolVar(&approveForce, "force", false, "apply even if the target file changed since the pending was created (conflict)")
	approveCmd.Flags().BoolVar(&approveAll, "all", false, "approve every pending change")
	approveCmd.Flags().StringVar(&approveAgent, "agent", "", "approve all pending changes queued by this agent/provider")
	approveCmd.Flags().StringVar(&approveFile, "file", "", "approve all pending changes targeting this memory file")
	rootCmd.AddCommand(approveCmd)
}

// normTarget canonicalizes a pending target path for filter comparison.
func normTarget(t string) string {
	return path.Clean(strings.ReplaceAll(strings.TrimSpace(t), "\\", "/"))
}

// selectPending resolves the bulk flags to a set of pending names. Used by both
// approve and reject so the filters can never drift apart.
func selectPending(mgr *pending.Manager, all bool, agent, file string) ([]string, error) {
	entries, err := mgr.List()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		info, ierr := mgr.Info(e.Name)
		if ierr != nil {
			continue
		}
		// "--agent claude-code" also matches auto-captured entries from that
		// agent ("capture:claude-code") — one flag covers one agent's queue.
		if agent != "" && info.Agent != agent && info.Agent != "capture:"+agent {
			continue
		}
		// Normalize both sides: writers store the target verbatim, so
		// "identity.md" and "./identity.md" are the same file spelled two ways.
		if file != "" && normTarget(info.Target) != normTarget(file) {
			continue
		}
		_ = all // --all is just "no extra filter"
		names = append(names, e.Name)
	}
	return names, nil
}

func runApprove(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	mgr := pending.NewManager(memPath)

	bulk := approveAll || approveAgent != "" || approveFile != ""
	if bulk == (len(args) == 1) {
		return fmt.Errorf("pass exactly one of: a pending name, or --all/--agent/--file")
	}
	// A conflicted entry means newer edits would be silently dropped — that
	// always needs eyes on the specific diff. Force is single-entry only.
	if bulk && approveForce {
		return fmt.Errorf("--force cannot be combined with bulk flags — review the conflicted entry and `auxly approve --force <name>` it individually")
	}

	var names []string
	if bulk {
		var err error
		if names, err = selectPending(mgr, approveAll, approveAgent, approveFile); err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println("Nothing pending matches.")
			return nil
		}
	} else {
		names = args[:1]
		// Show the diff before approving a single named change (bulk stays terse).
		if content, err := mgr.ViewDiff(names[0]); err == nil {
			fmt.Printf("📄 Approving: %s\n\n%s\n\n", names[0], content)
		} else {
			return err
		}
	}

	logger, lerr := audit.NewLogger(memPath)
	if lerr == nil {
		defer logger.Close()
	}

	applied, conflicted := 0, 0
	var firstErr error
	for _, name := range names {
		applyErr := mgr.Approve(name)
		if approveForce && errors.Is(applyErr, pending.ErrConflict) {
			applyErr = mgr.ForceApprove(name)
		}
		switch {
		case errors.Is(applyErr, pending.ErrConflict):
			// Bulk never force-applies conflicts implicitly — a conflicted entry
			// needs eyes, so it is skipped and named.
			conflicted++
			fmt.Printf("⚠ skipped (conflict): %s — approve it alone with --force after review\n", name)
		case applyErr != nil:
			if firstErr == nil {
				firstErr = applyErr
			}
			fmt.Printf("❌ %s: %v\n", name, applyErr)
		default:
			applied++
			if lerr == nil {
				logger.Log("human", "user", "approve", name, "", "Approved pending change", "auto")
			}
			fmt.Printf("✅ Approved and applied: %s\n", name)
		}
	}

	if applied > 0 {
		// Auto-commit if configured
		gitCfg, _ := git.LoadConfig(memPath)
		if gitCfg != nil && gitCfg.AutoCommit {
			// git stages the whole vault dir — refresh the lazily-compiled rollup
			// first so the commit never snapshots a stale unified_memory.md.
			memory.NewStore(memPath).EnsureUnified()
			git.AutoCommit(memPath, fmt.Sprintf("%d pending change(s)", applied), "Approved pending changes")
		}
	}
	if bulk {
		fmt.Printf("\n%d applied · %d conflict(s) skipped\n", applied, conflicted)
	}
	return firstErr
}
