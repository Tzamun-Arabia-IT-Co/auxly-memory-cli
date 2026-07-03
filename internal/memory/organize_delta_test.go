package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func deltaResp(ops ...deltaOp) string {
	b, _ := json.Marshal(deltaResponse{Ops: ops})
	return string(b)
}

// TestDeltaOps_MoveDeleteApplyPhantomDropped is the exact scenario from the
// optimization spec: a valid move + a valid delete + an op referencing text
// that doesn't exist verbatim in the source. The move and delete must apply,
// the phantom op must be dropped with a logged (DroppedDeltaOps) note, and
// the result must pass the HARD per-file fact-loss guard (applyDeltaGuard —
// the file is not reverted). A large enough filler count keeps the ONE real
// dedup within factLossWarning's existing small-shrink allowance (same 5%
// rule TestFactLossWarningAllowsSmallDedup locks for every other path), so
// the soft advisory Warning is empty too — nothing was actually lost.
func TestDeltaOps_MoveDeleteApplyPhantomDropped(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	content := bullets(30, "keep") + "- Personal loan of 5,000 from a relative\n- duplicate fact\n- duplicate fact\n"
	writeVaultFile(t, root, "projects.md", content)
	writeVaultFile(t, root, "personal.md", "")

	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		resp := deltaResp(
			deltaOp{Op: "move", File: "projects.md", Bullet: "- Personal loan of 5,000 from a relative", ToFile: "personal.md"},
			deltaOp{Op: "delete", File: "projects.md", Bullet: "- duplicate fact"},
			deltaOp{Op: "move", File: "projects.md", Bullet: "- this bullet does not exist anywhere", ToFile: "personal.md"},
		)
		return organizeRun{jsonContent: resp, modelUsed: "fake", tokensUsed: 5}, OrganizeResult{}, true
	}

	prop, res := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{DeltaMode: true}, exec)
	if !res.Success {
		t.Fatalf("delta plan failed: %s", res.Message)
	}

	byName := map[string]ProposedChange{}
	for _, c := range prop.Changes {
		byName[c.Name] = c
	}
	if strings.Contains(byName["projects.md"].NewContent, "Personal loan") {
		t.Fatalf("moved fact still present in source: %q", byName["projects.md"].NewContent)
	}
	if !strings.Contains(byName["personal.md"].NewContent, "Personal loan of 5,000") {
		t.Fatalf("moved fact did not land in personal.md: %q", byName["personal.md"].NewContent)
	}
	if strings.Count(byName["projects.md"].NewContent, "duplicate fact") != 1 {
		t.Fatalf("expected exactly one surviving 'duplicate fact' line, got: %q", byName["projects.md"].NewContent)
	}
	if !strings.Contains(byName["projects.md"].NewContent, "keep fact") {
		t.Fatalf("untouched filler bullets were lost: %q", byName["projects.md"].NewContent)
	}

	if len(prop.DroppedDeltaOps) != 1 || !strings.Contains(prop.DroppedDeltaOps[0], "does not exist") {
		t.Fatalf("expected exactly 1 dropped phantom op logged, got %v", prop.DroppedDeltaOps)
	}
	if prop.Warning != "" {
		t.Fatalf("nothing was actually lost — result should pass the fact-loss guard, got warning: %s", prop.Warning)
	}
}

// TestDeltaOps_LossyOpCaughtByGuardFileUnchanged: an op that would gut a
// file's facts (simulated here via applyDeltaOps directly returning a
// content string that drops almost everything) must be caught by
// applyDeltaGuard and that ONE file reverted to fully unchanged.
func TestDeltaOps_LossyOpCaughtByGuardFileUnchanged(t *testing.T) {
	old := "- fact one\n- fact two\n- fact three\n- fact four\n- fact five\n"
	changes := []ProposedChange{
		{Name: "infra.md", OldContent: old, NewContent: ""}, // lost all 5, nowhere else
	}
	out, notes := applyDeltaGuard(changes)
	if len(notes) != 1 || !strings.Contains(notes[0], "infra.md") {
		t.Fatalf("expected the guard to fire and name infra.md, got notes=%v", notes)
	}
	if out[0].NewContent != old {
		t.Fatalf("guard must revert the file to fully unchanged, got: %q", out[0].NewContent)
	}

	// Wire it through planOrganizeDelta end-to-end too.
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "infra.md", old)

	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		// A "delete" per bullet — a model gutting the file entirely.
		resp := deltaResp(
			deltaOp{Op: "delete", File: "infra.md", Bullet: "- fact one"},
			deltaOp{Op: "delete", File: "infra.md", Bullet: "- fact two"},
			deltaOp{Op: "delete", File: "infra.md", Bullet: "- fact three"},
			deltaOp{Op: "delete", File: "infra.md", Bullet: "- fact four"},
			deltaOp{Op: "delete", File: "infra.md", Bullet: "- fact five"},
		)
		return organizeRun{jsonContent: resp, modelUsed: "fake"}, OrganizeResult{}, true
	}

	prop, res := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{DeltaMode: true}, exec)
	if !res.Success {
		t.Fatalf("delta plan failed: %s", res.Message)
	}
	var infra ProposedChange
	for _, c := range prop.Changes {
		if c.Name == "infra.md" {
			infra = c
		}
	}
	if infra.NewContent != old {
		t.Fatalf("guard should have reverted infra.md to unchanged, got: %q", infra.NewContent)
	}
	if prop.Warning == "" {
		t.Fatal("a guard trip should still surface a warning for human review")
	}
}

// TestDeltaOps_PersonalSinkRefusesMoveOut: a move OUT of personal.md must be
// refused mechanically, whatever the model said — content never leaves
// personal.md.
func TestDeltaOps_PersonalSinkRefusesMoveOut(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "personal.md", "- my divorce case #123\n")
	writeVaultFile(t, root, "business.md", "- company contract with client X\n")

	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		resp := deltaResp(
			deltaOp{Op: "move", File: "personal.md", Bullet: "- my divorce case #123", ToFile: "business.md"},
		)
		return organizeRun{jsonContent: resp, modelUsed: "fake"}, OrganizeResult{}, true
	}

	prop, res := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{DeltaMode: true}, exec)
	if !res.Success {
		t.Fatalf("delta plan failed: %s", res.Message)
	}
	for _, c := range prop.Changes {
		if c.Name == "personal.md" && !strings.Contains(c.NewContent, "divorce case #123") {
			t.Fatalf("personal fact lost from personal.md: %q", c.NewContent)
		}
		if c.Name == "business.md" && strings.Contains(c.NewContent, "divorce") {
			t.Fatalf("personal fact leaked into business.md: %q", c.NewContent)
		}
	}
}

// TestDeltaMode_DefaultOff proves the dark launch: PlanOrganizeWithAgent (the
// existing, stable entry point) never engages delta mode — the whole-file
// contract is untouched unless a caller explicitly opts in via
// OrganizeRunOpts{DeltaMode: true}.
func TestDeltaMode_DefaultOff(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- name wael\n")

	sawOpsPrompt := false
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		if strings.Contains(sys, `"ops"`) {
			sawOpsPrompt = true
		}
		return organizeRun{jsonContent: `{"files":[{"name":"identity.md","content":"- name wael\n"}]}`, modelUsed: "fake"}, OrganizeResult{}, true
	}
	_, res := s.planOrganize(context.Background(), "", false, exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	if sawOpsPrompt {
		t.Fatal("default (non-opt-in) path must never send the delta-ops prompt")
	}
}

// TestDeltaMode_EnvOptIn proves AUXLY_ORGANIZE_DELTA routes a plain run (no
// OrganizeRunOpts{DeltaMode}) through the delta path — the opt-in the user
// flips to get the single-run speedup without every caller passing the flag.
func TestDeltaMode_EnvOptIn(t *testing.T) {
	t.Setenv("AUXLY_ORGANIZE_DELTA", "1")
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "projects.md", bullets(30, "keep")+"- duplicate fact\n- duplicate fact\n")

	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		resp := deltaResp(deltaOp{Op: "delete", File: "projects.md", Bullet: "- duplicate fact"})
		return organizeRun{jsonContent: resp, modelUsed: "fake", tokensUsed: 5}, OrganizeResult{}, true
	}

	// DeltaMode deliberately UNSET on opts — the env alone must route to delta.
	prop, res := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{}, exec)
	if !res.Success {
		t.Fatalf("env-opt-in delta plan failed: %s", res.Message)
	}
	byName := map[string]ProposedChange{}
	for _, c := range prop.Changes {
		byName[c.Name] = c
	}
	if strings.Count(byName["projects.md"].NewContent, "duplicate fact") != 1 {
		t.Fatalf("delta delete did not run via env opt-in: %q", byName["projects.md"].NewContent)
	}
}

// TestDeltaMode_LiftsChunkThreshold proves a vault that would normally chunk
// (over organizeChunkThreshold but under deltaChunkThreshold) instead runs as
// ONE whole-vault delta call when delta is on — the fix for the user's ~40k
// vault paying N CLI-agent cold-starts.
func TestDeltaMode_LiftsChunkThreshold(t *testing.T) {
	t.Setenv("AUXLY_ORGANIZE_DELTA", "1")
	root := t.TempDir()
	s := &Store{Root: root}
	// ~15k tokens: over the 12k default chunk threshold, under the 80k delta
	// ceiling. Without the lift this chunks (per-file); with it, one delta call.
	writeVaultFile(t, root, "projects.md", bullets(3200, "keep")+"- duplicate fact\n- duplicate fact\n")
	writeVaultFile(t, root, "infra.md", bullets(1200, "infra"))

	calls := 0
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		calls++
		resp := deltaResp(deltaOp{Op: "delete", File: "projects.md", Bullet: "- duplicate fact"})
		return organizeRun{jsonContent: resp, modelUsed: "fake", tokensUsed: 5}, OrganizeResult{}, true
	}
	if est := estimateVaultTokens([]organizeFile{{Content: bullets(3200, "keep")}, {Content: bullets(1200, "infra")}}); est <= organizeChunkThreshold() {
		t.Fatalf("test vault too small to exercise the lift: est=%d threshold=%d", est, organizeChunkThreshold())
	}
	_, res := s.planOrganizeOpts(context.Background(), "", false, OrganizeRunOpts{}, exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	if calls != 1 {
		t.Fatalf("expected ONE whole-vault delta call, got %d (chunked per-file?)", calls)
	}
}
