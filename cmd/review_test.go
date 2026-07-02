package cmd

import (
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

// TestReviewKeepAllFactsAllSkippedReturnsError is Finding 9's regression: a
// bulk run where every fact was skipped (vault changed since the scan) must
// surface a non-nil error — automation polling the exit code must be able to
// tell "nothing happened" from "success".
func TestReviewKeepAllFactsAllSkippedReturnsError(t *testing.T) {
	store := memory.NewStore(t.TempDir())
	store.WorkspaceRoot = ""
	facts := []memory.StaleFact{{File: "identity.md", Line: "- does not exist"}}

	err := reviewKeepAllFacts(store, nil, facts)
	if err == nil {
		t.Fatalf("expected an error when every fact was skipped")
	}
	if !strings.Contains(err.Error(), "no facts applied") {
		t.Fatalf("error = %q, want it to mention 'no facts applied'", err.Error())
	}
}

// TestReviewArchiveAllFactsAllSkippedReturnsError mirrors the keep-all case
// for --archive-all.
func TestReviewArchiveAllFactsAllSkippedReturnsError(t *testing.T) {
	store := memory.NewStore(t.TempDir())
	store.WorkspaceRoot = ""
	facts := []memory.StaleFact{{File: "identity.md", Line: "- does not exist"}}

	err := reviewArchiveAllFacts(store, store.Root, nil, facts)
	if err == nil {
		t.Fatalf("expected an error when every fact was skipped")
	}
	if !strings.Contains(err.Error(), "no facts applied") {
		t.Fatalf("error = %q, want it to mention 'no facts applied'", err.Error())
	}
}

// TestReviewKeepAllFactsSucceedsAndAuditLogs covers the success path: no
// error when at least one fact applies, and a best-effort audit entry lands
// per the AUDIT TRAIL requirement (review actions are human decisions that
// mutate the vault).
func TestReviewKeepAllFactsSucceedsAndAuditLogs(t *testing.T) {
	root := t.TempDir()
	store := memory.NewStore(root)
	store.WorkspaceRoot = ""
	line := "- Stamped fact [2020-01-01]"
	if err := store.Write("identity.md", "# Identity\n"+line+"\n"); err != nil {
		t.Fatal(err)
	}

	logger, err := audit.NewLogger(root)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	facts := []memory.StaleFact{{File: "identity.md", Line: line}}
	if err := reviewKeepAllFacts(store, logger, facts); err != nil {
		t.Fatalf("reviewKeepAllFacts: %v", err)
	}

	entries, err := logger.Tail(5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "review_keep" && e.File == "identity.md" && e.Reason == line {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a review_keep audit entry for %q, got %#v", line, entries)
	}
}
