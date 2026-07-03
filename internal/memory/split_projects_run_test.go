package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// applyPlusDiff mimics ApplyDiff's "existing == ”" case for a pure-addition
// diff (every line "+"-prefixed) — enough to verify a PendingWrite.Diff
// reproduces a section's body verbatim without importing internal/pending
// (which imports internal/memory — an import cycle, exactly why queuing a
// PendingWrite is left to the caller; see PendingWrite's doc).
func applyPlusDiff(t *testing.T, diff string) string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		if !strings.HasPrefix(l, "+") {
			t.Fatalf("diff line missing '+' prefix: %q (full diff: %q)", l, diff)
		}
		out = append(out, strings.TrimPrefix(l, "+"))
	}
	return strings.Join(out, "\n")
}

// headerRunStore writes projects.md content to a fresh vault root and
// returns a Store plus that root as memPath — PlanSplitProjectsRun needs
// both (memPath separately, for backup/seed paths).
func headerRunStore(t *testing.T, content string) (*Store, string) {
	t.Helper()
	memPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(memPath, "projects.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Store{Root: memPath}, memPath
}

// TestPlanSplitProjectsRun_HeaderModeQueuesSections is the header-mode
// end-to-end proof of the reproduced live-vault gap: 3 `## ` sections queue
// as 3 additions with VERBATIM bodies, nothing is cleaned up yet (first
// run), and the trailing loose bullet never appears in any queued diff.
func TestPlanSplitProjectsRun_HeaderModeQueuesSections(t *testing.T) {
	s, memPath := headerRunStore(t, realShapeProjectsMD)
	result, err := s.PlanSplitProjectsRun(context.Background(), memPath, nil)
	if err != nil {
		t.Fatalf("PlanSplitProjectsRun: %v", err)
	}
	if !result.HeaderMode {
		t.Fatal("expected HeaderMode=true for a ## -structured projects.md")
	}
	if result.CleanupWrite != nil {
		t.Fatalf("first run should queue no cleanup, got %+v", result.CleanupWrite)
	}
	if result.NothingToSplit || result.CleanupOnly {
		t.Fatalf("expected a normal queued run, got NothingToSplit=%v CleanupOnly=%v", result.NothingToSplit, result.CleanupOnly)
	}
	if len(result.Writes) != 3 {
		t.Fatalf("expected 3 section writes, got %d: %+v", len(result.Writes), result.Writes)
	}
	wantFiles := []string{"projects/auxly-cli.md", "projects/open-source-publish-plan.md", "projects/odysseus-evaluation.md"}
	for i, w := range result.Writes {
		if w.TargetFile != wantFiles[i] {
			t.Fatalf("write %d target = %q, want %q", i, w.TargetFile, wantFiles[i])
		}
		if strings.Contains(w.Diff, "Smart Sync") {
			t.Fatalf("trailing loose bullet leaked into %s diff:\n%s", w.TargetFile, w.Diff)
		}
	}
	odysseusBody := applyPlusDiff(t, result.Writes[2].Diff)
	if !strings.Contains(odysseusBody, "### Overview & Stack") || !strings.Contains(odysseusBody, "_Updated: 2026-06-03_") {
		t.Fatalf("odysseus section body not verbatim, got:\n%s", odysseusBody)
	}
	auxlyBody := applyPlusDiff(t, result.Writes[0].Diff)
	if !strings.Contains(auxlyBody, "  - Public release repo: `auxly-memory-cli`") {
		t.Fatalf("nested child not preserved verbatim in queued diff, got:\n%s", auxlyBody)
	}
}

// TestPlanSplitProjectsRun_HeaderModeCleanupPhase2 proves phase-2 cleanup:
// once a section's sub-file already holds its exact content (as if a prior
// run's addition was approved), a second run queues removing ONLY that
// section from projects.md, and still queues the remaining sections.
func TestPlanSplitProjectsRun_HeaderModeCleanupPhase2(t *testing.T) {
	s, memPath := headerRunStore(t, realShapeProjectsMD)

	// Simulate "auxly-cli"'s addition already approved: its sub-file exists
	// with the exact section content splitProjectsByHeaders computed.
	sections := splitProjectsByHeaders(realShapeProjectsMD)
	if err := os.MkdirAll(filepath.Join(memPath, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memPath, "projects", "auxly-cli.md"), []byte(sections[0].body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.PlanSplitProjectsRun(context.Background(), memPath, nil)
	if err != nil {
		t.Fatalf("PlanSplitProjectsRun: %v", err)
	}
	if result.CleanupWrite == nil {
		t.Fatal("expected a cleanup write for the already-approved section")
	}
	if result.CleanupWrite.Count != 1 {
		t.Fatalf("cleanup count = %d, want 1", result.CleanupWrite.Count)
	}
	if !strings.Contains(result.CleanupWrite.Diff, "-## Auxly-CLI") {
		t.Fatalf("cleanup diff should remove the Auxly-CLI header line, got:\n%s", result.CleanupWrite.Diff)
	}
	if strings.Contains(result.CleanupWrite.Diff, "Odysseus") || strings.Contains(result.CleanupWrite.Diff, "Open-Source") {
		t.Fatalf("cleanup diff must not touch unapproved sections, got:\n%s", result.CleanupWrite.Diff)
	}
	if len(result.Writes) != 2 {
		t.Fatalf("expected the 2 remaining sections still queued, got %d: %+v", len(result.Writes), result.Writes)
	}
	for _, w := range result.Writes {
		if w.TargetFile == "projects/auxly-cli.md" {
			t.Fatalf("already-moved section must not be re-queued: %+v", result.Writes)
		}
	}
}

// TestPlanSplitProjectsRun_HeaderModeCleanupOnly: once EVERY section's
// sub-file already holds its content, the run is cleanup-only — the
// rejected/never-approved-addition safety net never applies here since
// every addition was, by construction, already approved.
func TestPlanSplitProjectsRun_HeaderModeCleanupOnly(t *testing.T) {
	s, memPath := headerRunStore(t, realShapeProjectsMD)
	sections := splitProjectsByHeaders(realShapeProjectsMD)
	if err := os.MkdirAll(filepath.Join(memPath, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, sec := range sections {
		if err := os.WriteFile(filepath.Join(memPath, "projects", sec.slug+".md"), []byte(sec.body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := s.PlanSplitProjectsRun(context.Background(), memPath, nil)
	if err != nil {
		t.Fatalf("PlanSplitProjectsRun: %v", err)
	}
	if !result.CleanupOnly {
		t.Fatal("expected CleanupOnly once every section is already moved")
	}
	if result.CleanupWrite == nil || result.CleanupWrite.Count != 3 {
		t.Fatalf("expected cleanup for all 3 sections, got %+v", result.CleanupWrite)
	}
	if len(result.Writes) != 0 {
		t.Fatalf("cleanup-only run should queue no new writes, got %+v", result.Writes)
	}
}

// TestPlanSplitProjectsRun_HeaderModeEncryptedSeedsSubFiles is MAJOR 9's
// header-mode counterpart: an encrypted projects.md must seed each new
// projects/<slug>.md as an empty ENCRYPTED file before queueing its first
// addition, or approving it would create the sub-file as plaintext.
func TestPlanSplitProjectsRun_HeaderModeEncryptedSeedsSubFiles(t *testing.T) {
	memPath := t.TempDir()
	testVaultIdentity(t)
	s := &Store{Root: memPath}
	if err := s.Write("projects.md", realShapeProjectsMD); err != nil {
		t.Fatal(err)
	}
	if err := s.EncryptFile("projects.md"); err != nil {
		t.Fatalf("EncryptFile: %v", err)
	}

	result, err := s.PlanSplitProjectsRun(context.Background(), memPath, nil)
	if err != nil {
		t.Fatalf("PlanSplitProjectsRun: %v", err)
	}
	if len(result.SeededFiles) != 3 {
		t.Fatalf("expected all 3 sub-files seeded encrypted, got %+v", result.SeededFiles)
	}
	for _, f := range result.SeededFiles {
		raw, err := os.ReadFile(filepath.Join(memPath, f))
		if err != nil {
			t.Fatal(err)
		}
		if !vaultcrypt.IsEncrypted(raw) {
			t.Fatalf("%s not encrypted at rest after seeding: %q", f, raw)
		}
	}
}
