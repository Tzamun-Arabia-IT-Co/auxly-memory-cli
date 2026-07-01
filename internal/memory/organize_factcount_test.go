package memory

import (
	"os"
	"strings"
	"testing"
)

func change(old, new string) ProposedChange {
	return ProposedChange{Name: "x.md", OldContent: old, NewContent: new}
}

func bullets(n int, prefix string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("- " + prefix + " fact " + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + "\n")
	}
	return b.String()
}

func TestFactLossWarningFiresOnShrink(t *testing.T) {
	old := "- fact one\n- fact two\n- fact three\n- fact four\n- fact five\n" +
		"- fact six\n- fact seven\n- fact eight\n- fact nine\n- fact ten\n" +
		"- fact eleven\n- fact twelve\n- fact thirteen\n- fact fourteen\n- fact fifteen\n" +
		"- fact sixteen\n- fact seventeen\n- fact eighteen\n- fact nineteen\n- fact twenty\n"
	// model dropped 3 of 20 (15% loss — over the 5% dedup allowance)
	newContent := strings.Join(strings.Split(strings.TrimRight(old, "\n"), "\n")[:17], "\n")

	w := factLossWarning([]ProposedChange{change(old, newContent)})
	if w == "" {
		t.Fatal("expected fact-loss warning, got none")
	}
	if !strings.Contains(w, "17 facts vs 20") {
		t.Fatalf("warning lacks counts: %s", w)
	}
	if !strings.Contains(w, "fact eighteen") {
		t.Fatalf("warning lacks missing-fact candidates: %s", w)
	}
}

func TestFactLossWarningAllowsSmallDedup(t *testing.T) {
	old := bullets(40, "keep")
	// 1 of 40 removed (2.5%) — legitimate dedup, under the 5% allowance
	lines := strings.Split(strings.TrimRight(old, "\n"), "\n")
	newContent := strings.Join(lines[:39], "\n")
	if w := factLossWarning([]ProposedChange{change(old, newContent)}); w != "" {
		t.Fatalf("small dedup should not warn: %s", w)
	}
}

func TestFactLossWarningIgnoresMovesAcrossFiles(t *testing.T) {
	// Fact relocated from a.md to b.md — zero loss, no warning.
	a := change("- server ip is 10.0.0.1\n- prefers Go\n", "- prefers Go\n")
	b := ProposedChange{Name: "b.md", OldContent: "", NewContent: "- Server IP  is 10.0.0.1\n"}
	if w := factLossWarning([]ProposedChange{a, b}); w != "" {
		t.Fatalf("cross-file move should not warn: %s", w)
	}
}

func TestFactLossWarningNoInputNoWarning(t *testing.T) {
	if w := factLossWarning([]ProposedChange{change("", "- new fact\n")}); w != "" {
		t.Fatalf("growth should not warn: %s", w)
	}
}

// A file losing ALL its facts must warn even when another file gains enough
// unrelated bullets to keep the aggregate count level (growth must never mask a
// gutted file).
func TestFactLossWarningCatchesGuttedFileDespiteGrowth(t *testing.T) {
	gutted := ProposedChange{Name: "infra.md", OldContent: bullets(10, "server"), NewContent: ""}
	grown := ProposedChange{Name: "daily.md", OldContent: "", NewContent: bullets(11, "note")}
	w := factLossWarning([]ProposedChange{gutted, grown})
	if w == "" {
		t.Fatal("gutted file masked by growth elsewhere — no warning fired")
	}
	if !strings.Contains(w, "infra.md") {
		t.Fatalf("warning does not name the gutted file: %s", w)
	}
}

// ApplyOrganizeChanges must skip (not overwrite) a file edited after the
// proposal snapshot was taken, and still apply untouched files.
func TestApplyOrganizeChangesSkipsConcurrentlyEditedFile(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}

	if err := AtomicWriteFile(root+"/a.md", []byte("- fact a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(root+"/b.md", []byte("- fact b\n"), 0644); err != nil {
		t.Fatal(err)
	}

	changes := []ProposedChange{
		{Name: "a.md", OldContent: "- fact a\n", NewContent: "- fact a improved\n", Scope: "global"},
		{Name: "b.md", OldContent: "- fact b\n", NewContent: "- fact b improved\n", Scope: "global"},
	}

	// a.md gets edited between planning and applying.
	if err := AtomicWriteFile(root+"/a.md", []byte("- fact a EDITED MEANWHILE\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diff := s.ApplyOrganizeChanges(changes)

	aData, _ := os.ReadFile(root + "/a.md")
	if string(aData) != "- fact a EDITED MEANWHILE\n" {
		t.Fatalf("concurrent edit was overwritten: %s", aData)
	}
	bData, _ := os.ReadFile(root + "/b.md")
	if string(bData) != "- fact b improved\n" {
		t.Fatalf("untouched file not applied: %s", bData)
	}
	if !strings.Contains(diff, "skipped a.md") {
		t.Fatalf("skip not reported in diff output: %s", diff)
	}
}
