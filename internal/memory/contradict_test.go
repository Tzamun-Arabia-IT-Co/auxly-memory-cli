package memory

import (
	"context"
	"testing"
)

func fakePairs() []FactPair {
	return []FactPair{
		{
			A: FactRef{File: "infra.md", Line: "- server IP is 10.0.0.1", LineNo: 3},
			B: FactRef{File: "projects/auxly.md", Line: "- server IP is 10.0.0.2", LineNo: 7},
		},
		{
			A: FactRef{File: "projects.md", Line: "- auxly uses Postgres", LineNo: 5},
			B: FactRef{File: "projects/auxly.md", Line: "- auxly uses Postgres for storage", LineNo: 2},
		},
		{
			A: FactRef{File: "products.md", Line: "- widget has a dashboard", LineNo: 1},
			B: FactRef{File: "projects/widget.md", Line: "- widget ships weekly", LineNo: 9},
		},
	}
}

// TestPlanContradictionsFromPairsHappyPath locks the parsing/validation
// contract: verdicts map through, "keep" is honored, and distinct is excluded.
func TestPlanContradictionsFromPairsHappyPath(t *testing.T) {
	pairs := fakePairs()
	exec := splitExec(`{"findings":[
		{"pair":0,"verdict":"contradict","keep":"b","reason":"conflicting IP"},
		{"pair":1,"verdict":"duplicate","keep":"b","reason":"same fact, sub-file more specific"},
		{"pair":2,"verdict":"distinct","keep":"a","reason":"unrelated facts"}
	]}`)

	findings, err := planContradictionsFromPairs(context.Background(), pairs, exec)
	if err != nil {
		t.Fatalf("planContradictionsFromPairs: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 findings (distinct excluded), got %d: %+v", len(findings), findings)
	}
	if findings[0].Verdict != "contradict" || findings[0].Keep != "b" || findings[0].Pair.A.File != "infra.md" {
		t.Fatalf("finding 0 wrong: %+v", findings[0])
	}
	if findings[1].Verdict != "duplicate" || findings[1].Keep != "b" {
		t.Fatalf("finding 1 wrong: %+v", findings[1])
	}
}

// TestPlanContradictionsFromPairsUnknownVerdict locks that an unrecognized
// verdict string is dropped exactly like "distinct" (safe default: no action).
func TestPlanContradictionsFromPairsUnknownVerdict(t *testing.T) {
	pairs := fakePairs()
	exec := splitExec(`{"findings":[{"pair":0,"verdict":"maybe","keep":"a","reason":"unsure"}]}`)

	findings, err := planContradictionsFromPairs(context.Background(), pairs, exec)
	if err != nil {
		t.Fatalf("planContradictionsFromPairs: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("unknown verdict should be dropped, got %+v", findings)
	}
}

// TestPlanContradictionsFromPairsOutOfRangeIndex locks that a pair index
// outside the input slice is skipped rather than panicking or fabricating a
// finding.
func TestPlanContradictionsFromPairsOutOfRangeIndex(t *testing.T) {
	pairs := fakePairs()
	exec := splitExec(`{"findings":[
		{"pair":99,"verdict":"contradict","keep":"a","reason":"bogus index"},
		{"pair":-1,"verdict":"duplicate","keep":"a","reason":"negative index"},
		{"pair":0,"verdict":"contradict","keep":"a","reason":"valid"}
	]}`)

	findings, err := planContradictionsFromPairs(context.Background(), pairs, exec)
	if err != nil {
		t.Fatalf("planContradictionsFromPairs: %v", err)
	}
	if len(findings) != 1 || findings[0].Pair.A.File != "infra.md" {
		t.Fatalf("want only the valid in-range finding, got %+v", findings)
	}
}

// TestPlanContradictionsFromPairsKeepDefault locks that a missing/invalid
// "keep" value defaults to "a" rather than rejecting the finding.
func TestPlanContradictionsFromPairsKeepDefault(t *testing.T) {
	pairs := fakePairs()
	exec := splitExec(`{"findings":[{"pair":0,"verdict":"duplicate","keep":"","reason":"no keep given"}]}`)

	findings, err := planContradictionsFromPairs(context.Background(), pairs, exec)
	if err != nil {
		t.Fatalf("planContradictionsFromPairs: %v", err)
	}
	if len(findings) != 1 || findings[0].Keep != "a" {
		t.Fatalf("want keep defaulted to \"a\", got %+v", findings)
	}
}

// TestPlanContradictionsFromPairsKeepCaseInsensitive locks that "keep" is
// normalized (case + whitespace) before the enum check. A case-sensitive
// compare would silently coerce a literal "B" to the "a" default, INVERTING
// which fact survives.
func TestPlanContradictionsFromPairsKeepCaseInsensitive(t *testing.T) {
	pairs := fakePairs()
	exec := splitExec(`{"findings":[{"pair":0,"verdict":"contradict","keep":"B","reason":"newer IP"}]}`)

	findings, err := planContradictionsFromPairs(context.Background(), pairs, exec)
	if err != nil {
		t.Fatalf("planContradictionsFromPairs: %v", err)
	}
	if len(findings) != 1 || findings[0].Keep != "b" {
		t.Fatalf("want keep normalized to \"b\", got %+v", findings)
	}
}

// TestPlanContradictionsFromPairsFencedJSON locks that a ```json fenced
// response (a common weaker-model habit) is stripped before parsing.
func TestPlanContradictionsFromPairsFencedJSON(t *testing.T) {
	pairs := fakePairs()
	exec := splitExec("```json\n" + `{"findings":[{"pair":1,"verdict":"duplicate","keep":"b","reason":"fenced"}]}` + "\n```")

	findings, err := planContradictionsFromPairs(context.Background(), pairs, exec)
	if err != nil {
		t.Fatalf("planContradictionsFromPairs: %v", err)
	}
	if len(findings) != 1 || findings[0].Reason != "fenced" {
		t.Fatalf("fenced JSON not parsed correctly: %+v, err=%v", findings, err)
	}
}

// TestPlanContradictionsFromPairsEmpty locks that zero pairs in ⇒ zero
// findings out, without even calling exec — no model call needed for
// nothing to judge. (PlanContradictions itself short-circuits the same way
// before reaching planContradictionsFromPairs; SimilarCrossFilePairs, the
// pair source, is owned elsewhere and isn't stubbable here.)
func TestPlanContradictionsFromPairsEmpty(t *testing.T) {
	findings, err := planContradictionsFromPairs(context.Background(), nil, splitExec(`{"findings":[]}`))
	if err != nil {
		t.Fatalf("planContradictionsFromPairs: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want no findings, got %+v", findings)
	}
}
