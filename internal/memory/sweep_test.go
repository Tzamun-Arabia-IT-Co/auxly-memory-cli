package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sweepStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	// A real taxonomy target with existing content — the presence-check
	// fixture for the cleanup phase.
	if err := os.WriteFile(filepath.Join(root, "infra.md"), []byte("# Infrastructure\n- uses AWS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Store{Root: root}
}

func writeOrphan(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOrphanRootFilesWhitelist locks detection: taxonomy files, seeded
// templates, auxly-owned files, projects/, and dot-entries are NOT orphans;
// everything else in the root is (md or otherwise — Store.List would have
// hidden the non-md half).
func TestOrphanRootFilesWhitelist(t *testing.T) {
	s := sweepStore(t)
	root := s.Root

	writeOrphan(t, root, "identity.md", "- name wael\n")     // taxonomy
	writeOrphan(t, root, "CLAUDE.md", "rules\n")             // seeded template
	writeOrphan(t, root, "trust.yaml", "default: auto\n")    // seeded template
	writeOrphan(t, root, "AGENTS.md", "guide\n")             // setup-owned
	writeOrphan(t, root, "providers.md", "protocol\n")       // setup-owned
	writeOrphan(t, root, "unified_memory.md", "aggregate\n") // generated
	writeOrphan(t, root, "infrastructure.md", "- stray note\n")
	writeOrphan(t, root, "notes.txt", "not memory\n")
	writeOrphan(t, root, "infra.md.bak-123", "old copy\n")
	writeOrphan(t, root, "audit.db", "sqlite\n")      // auxly-owned index
	writeOrphan(t, root, "audit.db-wal", "wal\n")     // …and its sidecars
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrphan(t, root, filepath.Join("projects", "alpha.md"), "- project fact\n")
	if err := os.MkdirAll(filepath.Join(root, ".backup"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrphan(t, root, filepath.Join(".backup", "old.md"), "- old\n")
	writeOrphan(t, root, ".hidden.md", "- dot\n")

	mdFiles, others, err := s.OrphanRootFiles()
	if err != nil {
		t.Fatalf("OrphanRootFiles: %v", err)
	}
	if len(mdFiles) != 1 || mdFiles[0] != "infrastructure.md" {
		t.Fatalf("md orphans = %v, want [infrastructure.md]", mdFiles)
	}
	if len(others) != 2 || others[0] != "infra.md.bak-123" || others[1] != "notes.txt" {
		t.Fatalf("other orphans = %v, want [infra.md.bak-123 notes.txt]", others)
	}
}

// TestPlanSweepRunQueuesRefileAndCleanup locks the two-phase happy path: the
// bullet already present in a taxonomy target becomes a cleanup DELETION
// (and stays out of the model input), the model's verbatim-matched bullets
// become additions, and the backup was taken before anything was queued.
func TestPlanSweepRunQueuesRefileAndCleanup(t *testing.T) {
	s := sweepStore(t)
	writeOrphan(t, s.Root, "infrastructure.md",
		"# Infrastructure (orphan)\n- uses AWS\n- runs the motormind cluster\n- likes dark terminals\n")

	exec := func(_ context.Context, _, user string) (organizeRun, OrganizeResult, bool) {
		// The cleanup-matched bullet must NOT be in the model input; the
		// re-fileable ones must be.
		if strings.Contains(user, "- uses AWS") {
			t.Errorf("model input contains cleanup-matched bullet: %s", user)
		}
		if !strings.Contains(user, "- runs the motormind cluster") {
			t.Errorf("model input missing re-fileable bullet: %s", user)
		}
		return organizeRun{jsonContent: `{"moves":[
			{"target":"infra.md","bullets":["- runs the motormind cluster"]},
			{"target":"preferences.md","bullets":["- likes dark terminals"]}
		]}`, modelUsed: "fake"}, OrganizeResult{Success: true}, true
	}

	result, err := s.planSweep(context.Background(), s.Root, nil, exec)
	if err != nil {
		t.Fatalf("planSweep: %v", err)
	}
	if len(result.CleanupWrites) != 1 {
		t.Fatalf("cleanup writes = %+v, want exactly 1", result.CleanupWrites)
	}
	cw := result.CleanupWrites[0]
	// "-" diff marker + the bullet line "- uses AWS" → "-- uses AWS".
	if cw.TargetFile != "infrastructure.md" || cw.Diff != "-- uses AWS\n" || cw.Count != 1 {
		t.Fatalf("cleanup write wrong: %+v", cw)
	}
	if len(result.Writes) != 2 {
		t.Fatalf("writes = %+v, want 2 (infra.md, preferences.md)", result.Writes)
	}
	if result.Writes[0].TargetFile != "infra.md" || result.Writes[0].Diff != "+- runs the motormind cluster\n" {
		t.Fatalf("infra addition wrong: %+v", result.Writes[0])
	}
	if result.Writes[1].TargetFile != "preferences.md" || result.Writes[1].Diff != "+- likes dark terminals\n" {
		t.Fatalf("preferences addition wrong: %+v", result.Writes[1])
	}
	if result.SkippedCount != 0 {
		t.Fatalf("skipped = %d, want 0", result.SkippedCount)
	}
	entries, rerr := os.ReadDir(filepath.Join(s.Root, ".backup"))
	if rerr != nil || len(entries) == 0 {
		t.Fatalf("no backup taken: %v", rerr)
	}
}

// TestPlanSweepRunRejectsInvalidTargets locks the mechanical allowlist: a
// model-proposed destination outside taxonomy/existing-projects is counted
// skipped, never queued — the sweep cannot mint the next orphan.
func TestPlanSweepRunRejectsInvalidTargets(t *testing.T) {
	s := sweepStore(t)
	writeOrphan(t, s.Root, "stray.md", "- fact one\n")

	exec := func(_ context.Context, _, _ string) (organizeRun, OrganizeResult, bool) {
		return organizeRun{jsonContent: `{"moves":[
			{"target":"stray2.md","bullets":["- fact one"]},
			{"target":"inbox.md","bullets":["- fact one"]}
		]}`, modelUsed: "fake"}, OrganizeResult{Success: true}, true
	}
	result, err := s.planSweep(context.Background(), s.Root, nil, exec)
	if err != nil {
		t.Fatalf("planSweep: %v", err)
	}
	if len(result.Writes) != 0 {
		t.Fatalf("invalid targets must queue nothing, got %+v", result.Writes)
	}
	if result.SkippedCount != 2 {
		t.Fatalf("skipped = %d, want 2 (invented file + inbox)", result.SkippedCount)
	}
}

// TestPlanSweepRunSkipsRewordedAndGarbage: a model bullet that matches no
// original verbatim is skipped; a response matching NOTHING is a hard
// rejection (the garbage guard, same rule as split).
func TestPlanSweepRunSkipsRewordedAndGarbage(t *testing.T) {
	s := sweepStore(t)
	writeOrphan(t, s.Root, "stray.md", "- fact one\n")

	exec := func(_ context.Context, _, _ string) (organizeRun, OrganizeResult, bool) {
		return organizeRun{jsonContent: `{"moves":[
			{"target":"infra.md","bullets":["- a totally reworded fact"]}
		]}`, modelUsed: "fake"}, OrganizeResult{Success: true}, true
	}
	result, err := s.planSweep(context.Background(), s.Root, nil, exec)
	if err == nil || !strings.Contains(err.Error(), "REJECTED") {
		t.Fatalf("zero-match response must be a rejection, got err=%v result=%+v", err, result)
	}

	// Partial match: the reworded bullet is skipped, but a valid one still
	// queues — skip is not fatal.
	writeOrphan(t, s.Root, "stray2.md", "- fact two\n")
	exec2 := func(_ context.Context, _, _ string) (organizeRun, OrganizeResult, bool) {
		return organizeRun{jsonContent: `{"moves":[
			{"target":"infra.md","bullets":["- a totally reworded fact","- fact two"]}
		]}`, modelUsed: "fake"}, OrganizeResult{Success: true}, true
	}
	result, err = s.planSweep(context.Background(), s.Root, nil, exec2)
	if err != nil {
		t.Fatalf("planSweep: %v", err)
	}
	if result.SkippedCount != 1 || len(result.Writes) != 1 {
		t.Fatalf("skip-not-fatal wrong: skipped=%d writes=%+v", result.SkippedCount, result.Writes)
	}
}

// TestPlanSweepRunSpecialCases locks the non-LLM classifications: empty md
// orphans become removal candidates, bullet-less and encrypted orphans are
// reported untouched, duplicates across orphans are sent to the model once,
// and non-md orphans ride along as OtherOrphans.
func TestPlanSweepRunSpecialCases(t *testing.T) {
	s := sweepStore(t)
	writeOrphan(t, s.Root, "empty.md", "   \n")
	writeOrphan(t, s.Root, "prose.md", "just a paragraph, no bullets\n")
	writeOrphan(t, s.Root, "a.md", "- shared fact\n")
	writeOrphan(t, s.Root, "b.md", "- shared fact\n- unique fact\n")
	writeOrphan(t, s.Root, "junk.txt", "x\n")

	var seenInput string
	exec := func(_ context.Context, _, user string) (organizeRun, OrganizeResult, bool) {
		seenInput = user
		return organizeRun{jsonContent: `{"moves":[{"target":"infra.md","bullets":["- shared fact","- unique fact"]}]}`, modelUsed: "fake"}, OrganizeResult{Success: true}, true
	}
	result, err := s.planSweep(context.Background(), s.Root, nil, exec)
	if err != nil {
		t.Fatalf("planSweep: %v", err)
	}
	if len(result.RemoveEmpty) != 1 || result.RemoveEmpty[0] != "empty.md" {
		t.Fatalf("RemoveEmpty = %v, want [empty.md]", result.RemoveEmpty)
	}
	if len(result.NoBulletFiles) != 1 || result.NoBulletFiles[0] != "prose.md" {
		t.Fatalf("NoBulletFiles = %v, want [prose.md]", result.NoBulletFiles)
	}
	if strings.Count(seenInput, "- shared fact") != 1 {
		t.Fatalf("cross-orphan duplicate must appear once in model input:\n%s", seenInput)
	}
	if len(result.OtherOrphans) != 1 || result.OtherOrphans[0] != "junk.txt" {
		t.Fatalf("OtherOrphans = %v, want [junk.txt]", result.OtherOrphans)
	}
	if len(result.Writes) != 1 || result.Writes[0].Count != 2 {
		t.Fatalf("writes = %+v, want one infra.md addition of 2 bullets", result.Writes)
	}
}

// TestPlanSweepRunNothingToSweep: a clean root reports and skips the model
// call entirely.
func TestPlanSweepRunNothingToSweep(t *testing.T) {
	s := sweepStore(t)
	exec := func(_ context.Context, _, _ string) (organizeRun, OrganizeResult, bool) {
		t.Fatal("no md orphans — the model must not be called")
		return organizeRun{}, OrganizeResult{}, false
	}
	result, err := s.planSweep(context.Background(), s.Root, nil, exec)
	if err != nil {
		t.Fatalf("planSweep: %v", err)
	}
	if !result.NothingToSweep {
		t.Fatalf("expected NothingToSweep, got %+v", result)
	}
}

// TestRemoveEmptyVaultFiles locks the removal contract: emptiness is
// re-verified at removal time, non-empty files survive, and path-shaped or
// hidden names are refused outright.
func TestRemoveEmptyVaultFiles(t *testing.T) {
	s := sweepStore(t)
	writeOrphan(t, s.Root, "empty.md", "")
	writeOrphan(t, s.Root, "full.md", "- still here\n")

	removed := s.RemoveEmptyVaultFiles([]string{"empty.md", "full.md", "projects/x.md", ".hidden.md", ""})
	if len(removed) != 1 || removed[0] != "empty.md" {
		t.Fatalf("removed = %v, want [empty.md]", removed)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "empty.md")); !os.IsNotExist(err) {
		t.Fatal("empty.md should be gone")
	}
	if _, err := os.Stat(filepath.Join(s.Root, "full.md")); err != nil {
		t.Fatalf("full.md must survive: %v", err)
	}
}
