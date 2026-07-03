package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/safepath"
	"github.com/spf13/cobra"
)

var organizeSplitProjects bool
var organizeContradictions bool

var organizeCmd = &cobra.Command{
	Use:   "organize",
	Short: "Run on-demand memory vault reorganization and consolidation",
	RunE:  runOrganize,
}

func init() {
	organizeCmd.Flags().BoolVar(&organizeSplitProjects, "split-projects", false,
		"split the projects.md monolith into projects/<slug>.md files (queued as pending changes for review)")
	organizeCmd.Flags().BoolVar(&organizeContradictions, "contradictions", false,
		"find cross-file contradicting or duplicate facts via embedding similarity (queued as pending changes for review)")
	rootCmd.AddCommand(organizeCmd)
}

func runOrganize(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	if organizeSplitProjects && organizeContradictions {
		return fmt.Errorf("--split-projects and --contradictions: one mode at a time")
	}
	store := memory.NewStore(getMemoryPath())
	if organizeContradictions {
		return runContradictions(store)
	}
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

	// MAJOR 9: if projects.md is encrypted at rest, each NEW sub-file must be
	// seeded as an empty ENCRYPTED file before its first pending addition is
	// queued — encryption state lives in the file (same trick as MAJOR 8), so
	// a sub-file that's created only when its first pending gets approved
	// would default to plaintext and stay that way forever.
	_, projectsEncrypted, encErr := store.ReadRawVaultBytes("projects.md")
	if encErr != nil {
		return fmt.Errorf("check projects.md encryption: %w", encErr)
	}

	var slugs []string
	for slug := range plan.Groups {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	queued := 0
	for _, slug := range slugs {
		subFile := "projects/" + slug + ".md"
		created, serr := seedEncryptedProjectSubFile(store, memPath, subFile, projectsEncrypted)
		if serr != nil {
			return serr
		}
		if created {
			fmt.Printf("   🔒 %s created encrypted at rest (projects.md is encrypted)\n", subFile)
		}
		bullets := plan.Groups[slug]
		addDiff := ""
		for _, b := range bullets {
			addDiff += "+" + b + "\n"
		}
		name, werr := mgr.WriteFrom(subFile, addDiff, "organize-split")
		if werr != nil {
			return fmt.Errorf("queue split for %s: %w", slug, werr)
		}
		queued += len(bullets)
		fmt.Printf("   ⏳ %s ← %d bullet(s)  (%s)\n", subFile, len(bullets), name)
	}

	fmt.Printf("\n✅ Split planned: %d bullet(s) → %d project file(s), %d staying in projects.md.\n", queued, len(slugs), len(plan.General))
	fmt.Println("   1. Review with `auxly pending`, apply with `auxly approve --agent organize-split`.")
	fmt.Println("   2. Re-run `auxly organize --split-projects` — it queues the projects.md cleanup")
	fmt.Println("      ONLY for bullets whose new sub-file copy was actually approved (no fact can be lost).")
	return nil
}

// seedEncryptedProjectSubFile pre-creates subFile as an empty ENCRYPTED file
// (MAJOR 9 — same state-lives-in-file trick as MAJOR 8's seedEncryptedPersonalMD)
// when projectsEncrypted is true and the sub-file doesn't exist yet on disk.
// No-op (created=false) when projects.md isn't encrypted or the sub-file is
// already there. Split out so the seeding itself is directly testable
// without going through the LLM planning call.
func seedEncryptedProjectSubFile(store *memory.Store, memPath, subFile string, projectsEncrypted bool) (created bool, err error) {
	if !projectsEncrypted || store.Exists(subFile) {
		return false, nil
	}
	subPath, perr := safepath.ResolveSafe(memPath, subFile)
	if perr != nil {
		return false, fmt.Errorf("resolve %s: %w", subFile, perr)
	}
	// The empty seed replaces whatever is at subPath — serialize with every
	// other vault writer and re-check existence INSIDE the lock, or a write
	// landing between the check above and this one gets clobbered.
	unlock, lerr := memory.LockVault(memPath)
	if lerr != nil {
		return false, lerr
	}
	defer unlock()
	if store.Exists(subFile) {
		return false, nil
	}
	if merr := os.MkdirAll(filepath.Dir(subPath), 0755); merr != nil {
		return false, fmt.Errorf("create projects dir: %w", merr)
	}
	if werr := store.WriteVaultFile(subPath, []byte{}, 0o644, true); werr != nil {
		return false, fmt.Errorf("seed encrypted %s: %w", subFile, werr)
	}
	return true, nil
}

// backupProjectsMonolith snapshots projects.md before any split pendings are
// queued — a migration deserves a recovery point. Reads the RAW on-disk bytes
// (not store.View, which decrypts): if projects.md is encrypted at rest, the
// backup must stay ciphertext too, never a plaintext shadow copy.
func backupProjectsMonolith(store *memory.Store, memPath string) error {
	raw, encrypted, err := store.ReadRawVaultBytes("projects.md")
	if err != nil {
		return fmt.Errorf("read projects.md: %w", err)
	}
	backup := filepath.Join(memPath, ".backup", "projects-"+time.Now().Format("20060102-150405")+".md")
	if err := memory.AtomicWriteFile(backup, raw, 0o644); err != nil {
		return fmt.Errorf("backup projects.md first: %w", err)
	}
	tag := ""
	if encrypted {
		tag = " (ciphertext copy — projects.md is encrypted at rest)"
	}
	fmt.Printf("   ✓ Backed up projects.md → %s%s\n", backup, tag)
	return nil
}

// runContradictions finds cross-file fact pairs the embedding index scores as
// similar, has the model judge each as a contradiction, duplicate, or merely
// similar (distinct — dropped with no action), then queues the LOSING side of
// every remaining finding as a pending change. Nothing is written directly —
// same review-first shape as runSplitProjects.
func runContradictions(store *memory.Store) error {
	emb := embed.New()

	fmt.Println("🧠 Scanning for cross-file contradictions and duplicates (embedding similarity)...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	findings, err := store.PlanContradictionsWithAgent(ctx, emb, "Direct LLM", "", "")
	if err != nil {
		if errors.Is(err, embed.ErrUnavailable) {
			fmt.Println("⚠️  Contradiction check needs embeddings — configure a provider (auxly index status).")
			return nil
		}
		if errors.Is(err, memory.ErrVaultTooLarge) {
			fmt.Println(err.Error())
			return nil
		}
		return err
	}
	if len(findings) == 0 {
		fmt.Println("✅ No cross-file contradictions or duplicates above the similarity floor.")
		return nil
	}

	mgr := pending.NewManager(getMemoryPath())
	today := time.Now().Format("2006-01-02")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "VERDICT\tLOSER\tREASON\tPENDING\n")
	// Two findings can resolve to the same losing line (e.g. it's the loser
	// in more than one similar pair) — queue it once. A second pending for
	// the same target line is redundant and, once the first is approved,
	// fails as a conflict needing --force.
	seen := make(map[string]bool)
	for _, f := range findings {
		winner, loser := f.Pair.A, f.Pair.B
		if f.Keep == "b" {
			winner, loser = f.Pair.B, f.Pair.A
		}

		key := loser.File + "\x00" + strconv.Itoa(loser.LineNo)
		if seen[key] {
			fmt.Printf("   (skipped duplicate finding for %s:%d)\n", loser.File, loser.LineNo)
			continue
		}
		seen[key] = true

		// Persist the model's verdict + reason as a leading comment line in
		// the queued diff. ApplyDiff only acts on "+"/"-" lines (everything
		// else is inert), so this never touches the target file — but
		// ViewDiff returns the raw pending body, so `auxly pending` /
		// `auxly approve <name>` shows WHY before a human (or a bulk
		// `--agent organize-contradictions` run) applies it. Strip embedded
		// newlines from the reason so model output can't smuggle in an extra
		// "-"-prefixed line that ApplyDiff would treat as a real deletion.
		reason := strings.ReplaceAll(f.Reason, "\n", " ")
		comment := fmt.Sprintf("# organize-contradictions: %s — %s (vs %s)\n", f.Verdict, reason, winner.File)

		var diff string
		switch f.Verdict {
		case "duplicate":
			// The surviving copy already exists elsewhere — pure removal.
			diff = comment + "-" + loser.Line + "\n"
		case "contradict":
			// RULE 0: a contradicted fact is never silently erased. Replace
			// (not delete) so the loser's file keeps a trace pointing at
			// whichever fact won — a human re-reading loser.File later can
			// still find where the truth moved instead of hitting a gap.
			diff = comment + "-" + loser.Line + "\n" +
				"+" + loser.Line + " (superseded " + today + "; see " + winner.File + ")\n"
		default:
			continue
		}

		name, werr := mgr.WriteFrom(loser.File, diff, "organize-contradictions")
		if werr != nil {
			return fmt.Errorf("queue %s for %s: %w", f.Verdict, loser.File, werr)
		}
		fmt.Fprintf(w, "%s\t%s:%d\t%s\t%s\n", f.Verdict, loser.File, loser.LineNo, f.Reason, name)
	}
	w.Flush()

	fmt.Println("\n   Review each: `auxly pending` then `auxly approve <name>` (shows the diff).")
	fmt.Println("   Bulk `auxly approve --agent organize-contradictions` applies WITHOUT preview — only after reviewing.")
	return nil
}
