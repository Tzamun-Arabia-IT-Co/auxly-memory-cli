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

// TestPlanProjectsSplitSkipsMovedAndDuplicates locks the two-phase safety net:
// bullets already approved into sub-files and duplicate monolith bullets are
// excluded from the model input (dups would otherwise be merged by any
// reasonable model).
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

// TestPlanProjectsSplitToleratesBoldFormatting reproduces the real vault bug:
// a bold-prefixed bullet comes back from the model with its "**...**"
// emphasis stripped. It must still be matched (by normalized form), and the
// ORIGINAL bold text — never the model's mangled copy — is what gets queued.
func TestPlanProjectsSplitToleratesBoldFormatting(t *testing.T) {
	original := "- **Private umbrella:** `Tzamun-Arabia-IT-Co/Auxly` (local path: `/Users/lab/projects/Auxly`, workspace: `auxly-cli`)"
	s := splitStore(t, "# Projects\n"+original+"\n")
	modelCopy := "Private umbrella: `Tzamun-Arabia-IT-Co/Auxly` (local path: `/Users/lab/projects/Auxly`, workspace: `auxly-cli`)"
	plan, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"auxly":["`+modelCopy+`"]},"general":[]}`))
	if err != nil {
		t.Fatalf("PlanProjectsSplit rejected a bold-stripped echo: %v", err)
	}
	if got := plan.Groups["auxly"]; len(got) != 1 || got[0] != original {
		t.Fatalf("expected ORIGINAL bold bullet queued, got %+v", got)
	}
}

// TestPlanProjectsSplitSkipsUnmatchedBullets: a truncated/invented bullet the
// model returns matches no original — it is dropped (reported in Skipped),
// not fatal. The rest of the run still succeeds.
func TestPlanProjectsSplitSkipsUnmatchedBullets(t *testing.T) {
	s := splitStore(t, "# Projects\n- fact one\n- fact two\n")
	plan, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"a":["- fact one","- fact one truncat"]},"general":["- fact two"]}`))
	if err != nil {
		t.Fatalf("PlanProjectsSplit: %v", err)
	}
	if len(plan.Groups["a"]) != 1 || plan.Groups["a"][0] != "- fact one" {
		t.Fatalf("matched bullet wrong: %+v", plan.Groups)
	}
	if len(plan.General) != 1 || plan.General[0] != "- fact two" {
		t.Fatalf("general bullet wrong: %+v", plan.General)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0] != "- fact one truncat" {
		t.Fatalf("expected invented bullet reported as skipped, got %+v", plan.Skipped)
	}
}

// TestPlanProjectsSplitDedupesModelDuplicates: a model bullet that repeats
// another's normalized form keeps the first slug and doesn't duplicate the
// fact into two files.
func TestPlanProjectsSplitDedupesModelDuplicates(t *testing.T) {
	s := splitStore(t, "# Projects\n- fact one\n")
	plan, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"a":["- fact one"],"b":["- fact one"]},"general":[]}`))
	if err != nil {
		t.Fatalf("PlanProjectsSplit: %v", err)
	}
	if len(plan.Groups["a"]) != 1 {
		t.Fatalf("expected first slug to keep the bullet: %+v", plan.Groups)
	}
	if len(plan.Groups["b"]) != 0 {
		t.Fatalf("duplicate should not also land in the second slug: %+v", plan.Groups)
	}
}

// TestPlanProjectsSplitHardFailsOnGarbage: only a response matching NONE of
// the input bullets is a hard failure.
func TestPlanProjectsSplitHardFailsOnGarbage(t *testing.T) {
	s := splitStore(t, "# Projects\n- fact one\n- fact two\n")
	_, err := s.PlanProjectsSplit(context.Background(), splitExec(`{"projects":{"a":["- nonsense one","- nonsense two"]},"general":[]}`))
	if err == nil {
		t.Fatal("plan matching zero input bullets was accepted")
	}
}

// TestMovedProjectBulletsToleratesBoldFormatting: phase-2 cleanup must
// recognize a moved bullet even when the sub-file's copy lost its bold
// formatting along the way — same normalization as phase 1.
func TestMovedProjectBulletsToleratesBoldFormatting(t *testing.T) {
	s := splitStore(t, "# Projects\n- **Umbrella:** `x/y`\n")
	if err := os.MkdirAll(filepath.Join(s.Root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "projects", "x.md"), []byte("- Umbrella: `x/y`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, err := s.MovedProjectBullets()
	if err != nil {
		t.Fatalf("MovedProjectBullets: %v", err)
	}
	if len(moved) != 1 || moved[0] != "- **Umbrella:** `x/y`" {
		t.Fatalf("expected bold original recognized as moved, got %+v", moved)
	}
}

// TestPlanProjectsSplitKeepsNestedBulletsTogether: a parent bullet and its
// indented children move (or stay) as a unit, regardless of how the model
// classified the child individually.
func TestPlanProjectsSplitKeepsNestedBulletsTogether(t *testing.T) {
	src := "# Projects\n- **Auxly:** the memory CLI\n  - child detail one\n  - child detail two\n- unrelated fact\n"
	s := splitStore(t, src)
	// Model correctly groups the parent, but drops one child and misfiles the
	// other into general — both must still follow the parent into "auxly".
	plan, err := s.PlanProjectsSplit(context.Background(), splitExec(`{
		"projects": {"auxly": ["Auxly: the memory CLI"]},
		"general": ["- child detail two", "- unrelated fact"]
	}`))
	if err != nil {
		t.Fatalf("PlanProjectsSplit: %v", err)
	}
	got := plan.Groups["auxly"]
	want := []string{
		"- **Auxly:** the memory CLI",
		"- child detail one",
		"- child detail two",
	}
	if len(got) != len(want) {
		t.Fatalf("expected parent+children to move together, got %+v", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("bullet %d: got %q want %q (full: %+v)", i, got[i], w, got)
		}
	}
	if len(plan.General) != 1 || plan.General[0] != "- unrelated fact" {
		t.Fatalf("unrelated fact should stay in general alone: %+v", plan.General)
	}
}
