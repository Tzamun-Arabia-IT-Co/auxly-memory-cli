package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func splitStore(t *testing.T, content string) *Store {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "projects.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Store{Root: root}
}

func splitExec(response string) organizeExecutor {
	return func(_ context.Context, _, _ string) (organizeRun, OrganizeResult, bool) {
		return organizeRun{jsonContent: response, modelUsed: "fake"}, OrganizeResult{Success: true}, true
	}
}

// TestPlanProjectsSplitGroups locks the happy path: verbatim grouping into
// sanitized slugs, general bullets preserved, junk slugs folded into general.
func TestPlanProjectsSplitGroups(t *testing.T) {
	s := splitStore(t, "# Projects\n- auxly ships v1.2\n- widget uses React\n- I like small diffs\n- orphan note\n")
	plan, err := s.PlanProjectsSplit(context.Background(), splitExec(`{
		"projects": {
			"Auxly Memory!": ["- auxly ships v1.2"],
			"widget": ["- widget uses React"],
			"???": ["- orphan note"]
		},
		"general": ["- I like small diffs"]
	}`))
	if err != nil {
		t.Fatalf("PlanProjectsSplit: %v", err)
	}
	if got := plan.Groups["auxly-memory"]; len(got) != 1 || got[0] != "- auxly ships v1.2" {
		t.Fatalf("slug sanitize/grouping wrong: %+v", plan.Groups)
	}
	if len(plan.Groups["widget"]) != 1 {
		t.Fatalf("widget group missing: %+v", plan.Groups)
	}
	// "???" sanitizes to "" → its bullet must fall to general, never a junk file.
	if len(plan.General) != 2 {
		t.Fatalf("general should hold preference + junk-slug bullet: %+v", plan.General)
	}
	if _, ok := plan.Groups[""]; ok {
		t.Fatalf("empty slug survived: %+v", plan.Groups)
	}
}

// TestPlanProjectsSplitRejectsFactLoss locks RULE 0 for migrations: a dropped,
// invented, or duplicated bullet rejects the entire plan — no force override.
func TestPlanProjectsSplitRejectsFactLoss(t *testing.T) {
	src := "# Projects\n- fact one\n- fact two\n"

	t.Run("dropped bullet", func(t *testing.T) {
		s := splitStore(t, src)
		_, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"a":["- fact one"]},"general":[]}`))
		if err == nil {
			t.Fatal("plan with a dropped bullet accepted")
		}
	})

	t.Run("invented bullet", func(t *testing.T) {
		s := splitStore(t, src)
		_, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"a":["- fact one","- hallucinated"]},"general":["- fact two"]}`))
		if err == nil {
			t.Fatal("plan with an invented bullet accepted")
		}
	})

	t.Run("duplicated bullet", func(t *testing.T) {
		s := splitStore(t, src)
		_, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"a":["- fact one","- fact one"]},"general":["- fact two"]}`))
		if err == nil {
			t.Fatal("plan duplicating a bullet accepted")
		}
	})

	t.Run("reworded bullet", func(t *testing.T) {
		s := splitStore(t, src)
		_, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"a":["- fact one (improved)"]},"general":["- fact two"]}`))
		if err == nil {
			t.Fatal("plan rewording a bullet accepted")
		}
	})
}

// TestPlanProjectsSplitSkipsMovedAndDuplicates locks the two-phase safety net:
// bullets already approved into sub-files and duplicate monolith bullets are
// excluded from the model input (dups would otherwise be merged by any
// reasonable model and deterministically fail the permutation gate).
func TestPlanProjectsSplitSkipsMovedAndDuplicates(t *testing.T) {
	s := splitStore(t, "# Projects\n- already moved fact\n- fresh fact\n- fresh fact\n")
	if err := os.MkdirAll(filepath.Join(s.Root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "projects", "done.md"), []byte("- already moved fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := s.MovedProjectBullets()
	if err != nil || len(moved) != 1 || moved[0] != "- already moved fact" {
		t.Fatalf("MovedProjectBullets = %v, %v", moved, err)
	}

	// Model sees ONLY the deduped, not-yet-moved bullet — echoing it back once
	// must validate cleanly.
	plan, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"fresh":["- fresh fact"]},"general":[]}`))
	if err != nil {
		t.Fatalf("PlanProjectsSplit: %v", err)
	}
	if len(plan.Groups["fresh"]) != 1 {
		t.Fatalf("unexpected plan: %+v", plan.Groups)
	}
}
