package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var organizeSplitProjects bool

var organizeCmd = &cobra.Command{
	Use:   "organize",
	Short: "Run on-demand memory vault reorganization and consolidation",
	RunE:  runOrganize,
}

func init() {
	organizeCmd.Flags().BoolVar(&organizeSplitProjects, "split-projects", false,
		"split the projects.md monolith into projects/<slug>.md files (queued as pending changes for review)")
	rootCmd.AddCommand(organizeCmd)
}

func runOrganize(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	store := memory.NewStore(getMemoryPath())
	if organizeSplitProjects {
		return runSplitProjects(store)
	}
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

// runSplitProjects migrates the projects.md monolith into per-project
// sub-files — entirely through the pending queue (a human approves every
// piece), with the original backed up first and the grouping mechanically
// validated to be fact-preserving (no force override exists).
//
// Two-phase by design: this run queues ADDITIONS only. The monolith cleanup
// is queued on a LATER run, and only for bullets whose normalized form is
// already readable in a sub-file — so rejecting (or never approving) an
// addition can never lose the fact: the deletion for it simply never exists.
func runSplitProjects(store *memory.Store) error {
	memPath := getMemoryPath()
	mgr := pending.NewManager(memPath)

	// Phase 2 first: bullets an earlier approved split already moved.
	moved, merr := store.MovedProjectBullets()
	if merr == nil && len(moved) > 0 {
		if err := backupProjectsMonolith(store, memPath); err != nil {
			return err
		}
		delDiff := ""
		for _, b := range moved {
			delDiff += "-" + b + "\n"
		}
		name, werr := mgr.WriteFrom("projects.md", delDiff, "organize-split")
		if werr != nil {
			return fmt.Errorf("queue projects.md cleanup: %w", werr)
		}
		fmt.Printf("   ⏳ projects.md — remove %d bullet(s) already moved to sub-files  (%s)\n", len(moved), name)
	}

	fmt.Println("🧠 Planning projects.md split (LLM groups bullets by project)...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	plan, err := store.PlanProjectsSplitWithAgent(ctx, "Direct LLM", "", "")
	if err != nil {
		if len(moved) > 0 && strings.Contains(err.Error(), "no bullets to split") {
			// Everything remaining was already moved — cleanup above is the
			// whole job this run.
			fmt.Println("\n✅ Cleanup queued. Approve it with `auxly approve --agent organize-split`.")
			return nil
		}
		return err
	}
	if len(plan.Groups) == 0 {
		fmt.Println("Nothing to split — no bullets could be attributed to a specific project.")
		return nil
	}
	if len(moved) == 0 {
		if err := backupProjectsMonolith(store, memPath); err != nil {
			return err
		}
	}

	var slugs []string
	for slug := range plan.Groups {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	queued := 0
	for _, slug := range slugs {
		bullets := plan.Groups[slug]
		addDiff := ""
		for _, b := range bullets {
			addDiff += "+" + b + "\n"
		}
		name, werr := mgr.WriteFrom("projects/"+slug+".md", addDiff, "organize-split")
		if werr != nil {
			return fmt.Errorf("queue split for %s: %w", slug, werr)
		}
		queued += len(bullets)
		fmt.Printf("   ⏳ projects/%s.md ← %d bullet(s)  (%s)\n", slug, len(bullets), name)
	}

	fmt.Printf("\n✅ Split planned: %d bullet(s) → %d project file(s), %d staying in projects.md.\n", queued, len(slugs), len(plan.General))
	fmt.Println("   1. Review with `auxly pending`, apply with `auxly approve --agent organize-split`.")
	fmt.Println("   2. Re-run `auxly organize --split-projects` — it queues the projects.md cleanup")
	fmt.Println("      ONLY for bullets whose new sub-file copy was actually approved (no fact can be lost).")
	return nil
}

// backupProjectsMonolith snapshots projects.md before any split pendings are
// queued — a migration deserves a recovery point.
func backupProjectsMonolith(store *memory.Store, memPath string) error {
	content, err := store.View("projects.md")
	if err != nil {
		return fmt.Errorf("read projects.md: %w", err)
	}
	backup := filepath.Join(memPath, ".backup", "projects-"+time.Now().Format("20060102-150405")+".md")
	if err := memory.AtomicWriteFile(backup, []byte(content), 0o644); err != nil {
		return fmt.Errorf("backup projects.md first: %w", err)
	}
	fmt.Printf("   ✓ Backed up projects.md → %s\n", backup)
	return nil
}
