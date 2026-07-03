package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var organizeSplitProjects bool
var organizeContradictions bool
var organizeAgent string
var organizeSkipEncrypted bool
var organizeDecryptTemporarily bool
var organizeAssumeYes bool

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
	organizeCmd.Flags().StringVar(&organizeAgent, "agent", "",
		"run via an installed CLI agent instead of the Direct LLM provider (e.g. claude, codex, gemini — see `auxly agents`)")
	organizeCmd.Flags().BoolVar(&organizeSkipEncrypted, "skip-encrypted", false,
		"with --agent: exclude encrypted file(s) from this run instead of refusing")
	organizeCmd.Flags().BoolVar(&organizeDecryptTemporarily, "decrypt-temporarily", false,
		"with --agent: decrypt encrypted file(s) for this run only, re-encrypting automatically when it finishes — "+decryptTempPSWarning)
	organizeCmd.Flags().BoolVarP(&organizeAssumeYes, "yes", "y", false,
		"don't prompt before --decrypt-temporarily decrypts files (required when stdin isn't a terminal)")
	rootCmd.AddCommand(organizeCmd)
}

func runOrganize(cmd *cobra.Command, args []string) error {
	if err := requireInit(); err != nil {
		return err
	}
	if organizeSplitProjects && organizeContradictions {
		return fmt.Errorf("--split-projects and --contradictions: one mode at a time")
	}
	if organizeSkipEncrypted && organizeDecryptTemporarily {
		return fmt.Errorf("--skip-encrypted and --decrypt-temporarily: one mode at a time")
	}
	store := memory.NewStore(getMemoryPath())
	if organizeContradictions {
		return runContradictions(store)
	}
	if organizeSplitProjects {
		return runSplitProjects(store)
	}

	agentName, agentPath, aerr := resolveHeadlessAgent(organizeAgent)
	if aerr != nil {
		return aerr
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

	if agentPath == "" {
		res := store.OrganizeVault()
		if !res.Success {
			return fmt.Errorf("organize failed: %s", res.Message)
		}
		fmt.Println(res.Message)
		return nil
	}

	// CLI-agent path: an encrypted file rides the spawned process's argv for
	// the whole run (ps-visible). Check BEFORE spending time on a model call
	// so a refusal — or the chosen way around it — is immediate.
	enc := store.EncryptedOrganizableFiles()
	switch {
	case len(enc) == 0 || organizeSkipEncrypted:
		res := store.OrganizeVaultWithAgent(agentName, agentPath, organizeSkipEncrypted)
		if !res.Success {
			return fmt.Errorf("organize failed: %s", res.Message)
		}
		fmt.Println(res.Message)
		return nil
	case organizeDecryptTemporarily:
		return runOrganizeDecryptTemporarily(store, agentName, agentPath, enc)
	default:
		return fmt.Errorf(
			"organize via %s would expose decrypted content on the process command line (encrypted: %s)\n"+
				"Choose one: --skip-encrypted to exclude them, --decrypt-temporarily to decrypt just for this run "+
				"(re-encrypted automatically after), or drop --agent to use the Direct LLM provider instead",
			agentName, strings.Join(enc, ", "))
	}
}

// resolveHeadlessAgent maps --agent's value (a provider key like "claude", or
// a substring of an installed agent's display name) to that agent's canonical
// name + executable path, via the same detection the TUI's provider picker
// uses (buildOrgProviders in tui/organize.go). Empty name means "no agent" —
// the Direct LLM default, unaffected by anything below.
func resolveHeadlessAgent(name string) (agentName, agentPath string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", nil
	}
	for _, a := range detect.InstalledAgents() {
		isCLI := strings.Contains(a.Name, "CLI") || strings.Contains(a.Name, "Code") || a.Connection == "MCP+Shell" || a.Connection == "Shell"
		if !isCLI || a.Command == "" {
			continue
		}
		if strings.EqualFold(a.Provider, name) || strings.Contains(strings.ToLower(a.Name), strings.ToLower(name)) {
			return a.Name, a.Command, nil
		}
	}
	return "", "", fmt.Errorf("no installed CLI agent matches --agent %q (see `auxly agents`)", name)
}

// isStdinTTY reports whether stdin is an interactive terminal — used to
// decide whether --decrypt-temporarily may prompt for confirmation, or must
// refuse instead of hanging on a read that will never come.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// decryptTempPSWarning is the informed-consent line MAJOR 3 requires
// everywhere --decrypt-temporarily can decrypt a file: the flag help, the
// interactive prompt, and the --yes non-interactive path all state it — none
// of them may let the user consent without knowing the decrypted content
// rides the process command line (visible via `ps` to other local users) for
// the whole run. Mirrors the TUI's encChoiceView() warning.
const decryptTempPSWarning = "decrypted content is visible on the process command line (ps) to other local users for the run"

// decryptTemporarilyPromptText builds the [y/N] confirmation prompt for
// --decrypt-temporarily. Split out from runOrganizeDecryptTemporarily so the
// consent wording is directly testable without capturing stdout.
func decryptTemporarilyPromptText(files []string) string {
	return fmt.Sprintf("decrypt %d file(s) for this run and re-encrypt after — %s? [y/N] (%s): ",
		len(files), decryptTempPSWarning, strings.Join(files, ", "))
}

// runOrganizeDecryptTemporarily implements --decrypt-temporarily: confirm
// (a TTY prompt, or --yes for non-interactive runs — both stating the ps/argv
// exposure, MAJOR 3), decrypt the encrypted files in place, run the
// CLI-agent organize, then ALWAYS re-encrypt — success or failure, via
// defer, so no exit path skips it (MAJOR 4: a re-encrypt failure must also
// make this return a non-nil error, or the process exits 0 with plaintext
// left on disk).
func runOrganizeDecryptTemporarily(store *memory.Store, agentName, agentPath string, files []string) error {
	if !organizeAssumeYes {
		if !isStdinTTY() {
			return fmt.Errorf("--decrypt-temporarily needs confirmation and stdin isn't a terminal — re-run with --yes to confirm non-interactively")
		}
		fmt.Print(decryptTemporarilyPromptText(files))
		resp, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(resp)); a != "y" && a != "yes" {
			fmt.Println("Aborted — nothing changed.")
			return nil
		}
	} else {
		fmt.Printf("⚠ %s\n", decryptTempPSWarning)
	}

	restore, derr := store.TempDecryptForOrganize(files)
	if derr != nil {
		return fmt.Errorf("decrypt for organize: %w", derr)
	}
	return runOrganizeWithRestore(func() memory.OrganizeResult {
		return store.OrganizeVaultWithAgent(agentName, agentPath, false)
	}, restore, files)
}

// runOrganizeWithRestore runs the organize call then ALWAYS restores
// (re-encrypts) via defer, and folds a restore failure into the returned
// error.
//
// MAJOR 4: the previous version's deferred restore only printed on failure
// and always returned nil — a re-encrypt failure left plaintext on disk but
// the process still exited 0, looking like success to any script or CI
// checking the exit code. The named return + defer-sets-err here makes a
// restore failure propagate no matter how the organize call itself went.
func runOrganizeWithRestore(run func() memory.OrganizeResult, restore func() error, files []string) (err error) {
	defer func() {
		if rerr := restore(); rerr != nil {
			fmt.Printf("⚠ RE-ENCRYPT FAILED after organize — %s may still be PLAINTEXT on disk: %v\n   Run `auxly encrypt file <name>` on each to fix.\n", strings.Join(files, ", "), rerr)
			err = errors.Join(err, fmt.Errorf("re-encrypt after organize failed: %w", rerr))
		} else {
			fmt.Printf("🔒 re-encrypted %d file(s) after organize.\n", len(files))
		}
	}()

	res := run()
	if !res.Success {
		err = fmt.Errorf("organize failed: %s", res.Message)
		return
	}
	fmt.Println(res.Message)
	return
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
//
// The matching/backup/seeding logic itself lives in
// memory.Store.PlanSplitProjectsRun — the ONE shared implementation this and
// the TUI's Split projects mode both call, so it exists exactly once. This
// function is now just: run it (with hooks reproducing the original live
// CLI progress lines), then queue what it computed via the pending package
// (which memory.Store can't import — pending already imports memory).
func runSplitProjects(store *memory.Store) error {
	memPath := getMemoryPath()
	mgr := pending.NewManager(memPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	hooks := &memory.SplitProjectsHooks{
		BackedUp: func(path string, encrypted bool) {
			tag := ""
			if encrypted {
				tag = " (ciphertext copy — projects.md is encrypted at rest)"
			}
			fmt.Printf("   ✓ Backed up projects.md → %s%s\n", path, tag)
		},
		Planning: func() {
			fmt.Println("🧠 Planning projects.md split (LLM groups bullets by project)...")
		},
		Seeded: func(subFile string) {
			fmt.Printf("   🔒 %s created encrypted at rest (projects.md is encrypted)\n", subFile)
		},
	}
	result, err := store.PlanSplitProjectsRun(ctx, memPath, hooks)

	// The cleanup diff (if any) was computed — and backed up — BEFORE the
	// (possibly failing) LLM planning call, exactly like the original
	// single-function version: queue and report it regardless of what err
	// says below, so a planning failure never drops an already-computed
	// cleanup.
	// ponytail: queuing (not just computing) has to happen out here — pending
	// imports memory, so PlanSplitProjectsRun can't call WriteFrom itself.
	// That also means this line necessarily prints after Planning/the LLM
	// call instead of before it, unlike the original's live interleaving.
	if result.CleanupWrite != nil {
		name, werr := mgr.WriteFrom(result.CleanupWrite.TargetFile, result.CleanupWrite.Diff, "organize-split")
		if werr != nil {
			return fmt.Errorf("queue projects.md cleanup: %w", werr)
		}
		fmt.Printf("   ⏳ projects.md — remove %d bullet(s) already moved to sub-files  (%s)\n", result.CleanupWrite.Count, name)
	}
	if err != nil {
		return err
	}
	if result.NothingToSplit {
		fmt.Println("Nothing to split — no bullets could be attributed to a specific project.")
		return nil
	}
	if result.CleanupOnly {
		fmt.Println("\n✅ Cleanup queued. Approve it with `auxly approve --agent organize-split`.")
		return nil
	}

	queued := 0
	unit := "bullet(s)"
	if result.HeaderMode {
		unit = "line(s)"
	}
	for _, w := range result.Writes {
		name, werr := mgr.WriteFrom(w.TargetFile, w.Diff, "organize-split")
		if werr != nil {
			return fmt.Errorf("queue split for %s: %w", w.TargetFile, werr)
		}
		queued += w.Count
		fmt.Printf("   ⏳ %s ← %d %s  (%s)\n", w.TargetFile, w.Count, unit, name)
	}

	if result.HeaderMode {
		fmt.Printf("\n✅ Split planned: %d section(s) queued as project file(s); H1 title and any content outside a `## ` header stay in projects.md.\n", len(result.Writes))
	} else {
		fmt.Printf("\n✅ Split planned: %d bullet(s) → %d project file(s), %d staying in projects.md.\n", queued, len(result.Writes), result.GeneralCount)
	}
	fmt.Println("   1. Review with `auxly pending`, apply with `auxly approve --agent organize-split`.")
	fmt.Println("   2. Re-run `auxly organize --split-projects` — it queues the projects.md cleanup")
	fmt.Println("      ONLY for bullets whose new sub-file copy was actually approved (no fact can be lost).")
	return nil
}

// runContradictions finds cross-file fact pairs the embedding index scores as
// similar, has the model judge each as a contradiction, duplicate, or merely
// similar (distinct — dropped with no action), then queues the LOSING side of
// every remaining finding as a pending change. Nothing is written directly —
// same review-first shape as runSplitProjects.
//
// The verdict resolution (de-dupe by losing line, diff construction) lives in
// memory.Store.PlanContradictionsRun — the ONE shared implementation this and
// the TUI's Find contradictions mode both call.
func runContradictions(store *memory.Store) error {
	emb := embed.New()

	fmt.Println("🧠 Scanning for cross-file contradictions and duplicates (embedding similarity)...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := store.PlanContradictionsRun(ctx, emb)
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
	if result.TotalFindings == 0 {
		fmt.Println("✅ No cross-file contradictions or duplicates above the similarity floor.")
		return nil
	}

	mgr := pending.NewManager(getMemoryPath())
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "VERDICT\tLOSER\tREASON\tPENDING\n")
	for _, it := range result.Items {
		if it.Skipped {
			// A second finding resolving to the same losing line — queuing
			// it again would be redundant and, once the first is approved,
			// fail as a conflict needing --force.
			fmt.Printf("   (skipped duplicate finding for %s:%d)\n", it.TargetFile, it.LoserLineNo)
			continue
		}
		name, werr := mgr.WriteFrom(it.TargetFile, it.Diff, "organize-contradictions")
		if werr != nil {
			return fmt.Errorf("queue %s for %s: %w", it.Verdict, it.TargetFile, werr)
		}
		fmt.Fprintf(w, "%s\t%s:%d\t%s\t%s\n", it.Verdict, it.TargetFile, it.LoserLineNo, it.Reason, name)
	}
	w.Flush()

	fmt.Println("\n   Review each: `auxly pending` then `auxly approve <name>` (shows the diff).")
	fmt.Println("   Bulk `auxly approve --agent organize-contradictions` applies WITHOUT preview — only after reviewing.")
	return nil
}
