package memory

import (
	"context"
	"strings"
	"testing"
)

// TestOrganizeDirtySkip_UnchangedVaultMakesZeroModelCalls proves the biggest
// win of Optimization 1: once a vault has been successfully organized, a
// second run over the SAME content never calls the model at all.
func TestOrganizeDirtySkip_UnchangedVaultMakesZeroModelCalls(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- name wael\n")
	writeVaultFile(t, root, "infra.md", "- box one\n")

	calls := 0
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		calls++
		resp := `{"files":[{"name":"identity.md","content":"- name wael\n"},{"name":"infra.md","content":"- box one\n"}]}`
		return organizeRun{jsonContent: resp, modelUsed: "fake", tokensUsed: 10}, OrganizeResult{}, true
	}

	// First run: nothing in the ledger yet, both files are "dirty".
	prop, res := s.planOrganize(context.Background(), "", false, exec)
	if !res.Success {
		t.Fatalf("first run failed: %s", res.Message)
	}
	if calls != 1 {
		t.Fatalf("first run should make exactly 1 model call, got %d", calls)
	}
	s.ApplyOrganizeChanges(prop.Changes)

	// Second run: content is byte-identical to what was just organized —
	// ledger should skip everything, zero model calls.
	_, res2 := s.planOrganize(context.Background(), "", false, exec)
	if !res2.Success {
		t.Fatalf("second run failed: %s", res2.Message)
	}
	if calls != 1 {
		t.Fatalf("unchanged vault should make zero additional model calls, got %d total", calls)
	}
	if !strings.Contains(res2.Message, "already tidy") {
		t.Fatalf("expected an 'already tidy' message, got: %q", res2.Message)
	}
}

// TestOrganizeDirtySkip_OnlyEditedFileGathered proves the ledger filters
// per-file, not per-vault: editing one file after a successful organize means
// only THAT file is gathered (and sent to the model) on the next run.
func TestOrganizeDirtySkip_OnlyEditedFileGathered(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- name wael\n")
	writeVaultFile(t, root, "infra.md", "- box one\n")

	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		resp := `{"files":[{"name":"identity.md","content":"- name wael\n"},{"name":"infra.md","content":"- box one\n"}]}`
		return organizeRun{jsonContent: resp, modelUsed: "fake", tokensUsed: 10}, OrganizeResult{}, true
	}
	prop, res := s.planOrganize(context.Background(), "", false, exec)
	if !res.Success {
		t.Fatalf("first run failed: %s", res.Message)
	}
	s.ApplyOrganizeChanges(prop.Changes)

	// Edit only infra.md directly on disk (bypassing organize).
	writeVaultFile(t, root, "infra.md", "- box one\n- box two\n")

	files, _, cleanCount, err := s.gatherOrganizeFilesOpts(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "infra.md" {
		t.Fatalf("expected only infra.md gathered as dirty, got %+v", files)
	}
	if cleanCount != 1 {
		t.Fatalf("expected identity.md counted as clean, got cleanCount=%d", cleanCount)
	}
}

// TestOrganizeDirtySkip_LedgerUpdatesOnlyAfterSuccessfulApply proves the
// ledger is written post-apply, keyed to the content that actually landed on
// disk, and that a file whose apply is skipped (stale/aborted) is NOT marked
// seen — it must remain dirty and get retried on the next run.
func TestOrganizeDirtySkip_LedgerUpdatesOnlyAfterSuccessfulApply(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- name wael\n")

	seenBefore := loadOrganizeSeen(root)
	if len(seenBefore) != 0 {
		t.Fatalf("ledger should start empty, got %v", seenBefore)
	}

	changes := []ProposedChange{
		{Name: "identity.md", OldContent: "- name wael\n", NewContent: "- name Wael\n", Scope: "global"},
	}
	s.ApplyOrganizeChanges(changes)

	seen := loadOrganizeSeen(root)
	wantHash := hashText("- name Wael\n")
	if seen["identity.md"] != wantHash {
		t.Fatalf("ledger not updated with the applied content's hash: got %v", seen)
	}

	// Now simulate a stale apply: the on-disk content no longer matches the
	// plan-time OldContent snapshot, so ApplyOrganizeChanges must skip it —
	// and the ledger must NOT advance for it.
	writeVaultFile(t, root, "identity.md", "- name Wael EDITED ELSEWHERE\n")
	staleChanges := []ProposedChange{
		{Name: "identity.md", OldContent: "- name Wael\n", NewContent: "- name Wael tidied\n", Scope: "global"},
	}
	s.ApplyOrganizeChanges(staleChanges)

	seenAfter := loadOrganizeSeen(root)
	if seenAfter["identity.md"] != wantHash {
		t.Fatalf("ledger must not advance on a skipped/stale apply, got %v", seenAfter)
	}
}

// TestOrganizeDirtySkip_ForceAllBypassesLedger proves OrganizeRunOpts.ForceAll
// restores the whole-vault replan behavior (the TUI "re-run everything" case)
// even when nothing has changed since the last successful organize.
func TestOrganizeDirtySkip_ForceAllBypassesLedger(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- name wael\n")

	calls := 0
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		calls++
		return organizeRun{jsonContent: `{"files":[{"name":"identity.md","content":"- name wael\n"}]}`, modelUsed: "fake"}, OrganizeResult{}, true
	}

	prop, res := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{}, exec)
	if !res.Success {
		t.Fatalf("first run failed: %s", res.Message)
	}
	s.ApplyOrganizeChanges(prop.Changes)
	if calls != 1 {
		t.Fatalf("expected 1 call after first run, got %d", calls)
	}

	// Default (ForceAll=false): ledger skips it, no new call.
	_, res2 := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{}, exec)
	if !res2.Success || calls != 1 {
		t.Fatalf("expected the dirty-skip to hold (still 1 call), got calls=%d res=%v", calls, res2)
	}

	// ForceAll=true: bypasses the ledger, replans everything again.
	_, res3 := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{ForceAll: true}, exec)
	if !res3.Success {
		t.Fatalf("force-all run failed: %s", res3.Message)
	}
	if calls != 2 {
		t.Fatalf("ForceAll should bypass the ledger and make a new call, got %d total calls", calls)
	}
}
