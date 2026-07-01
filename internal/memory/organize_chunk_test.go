package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakeExec returns a canned per-file organize response keyed by the file name
// found in the user prompt, and records which files were sent.
func fakeExec(t *testing.T, responses map[string]string, called *[]string) organizeExecutor {
	return func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		for name, resp := range responses {
			if strings.Contains(user, "=== FILE: "+name+" ===") {
				*called = append(*called, name)
				return organizeRun{jsonContent: resp, modelUsed: "fake", tokensUsed: 10}, OrganizeResult{}, true
			}
		}
		t.Fatalf("fakeExec got unexpected prompt: %.120s", user)
		return organizeRun{}, OrganizeResult{}, false
	}
}

func chunkResp(name, content string, moves ...map[string]string) string {
	obj := map[string]any{
		"files": []map[string]string{{"name": name, "content": content}},
		"moves": moves,
	}
	b, _ := json.Marshal(obj)
	return string(b)
}

func writeVaultFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(root+"/"+name, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestChunkedOrganizePerFileWithMoves: large vault → one call per file; a fact
// flagged for another file lands there via the mechanical routing pass.
func TestChunkedOrganizePerFileWithMoves(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "projects.md", "- building auxly\n- Personal loan of 5,000 from a relative\n")
	writeVaultFile(t, root, "infra.md", "- server ip 192.168.1.24\n")

	t.Setenv("AUXLY_ORGANIZE_CHUNK_TOKENS", "1") // force chunked path

	var called []string
	exec := fakeExec(t, map[string]string{
		"projects.md": chunkResp("projects.md", "- building auxly\n",
			map[string]string{"to": "personal.md", "fact": "- Personal loan of 5,000 from a relative"}),
		"infra.md": chunkResp("infra.md", "- server ip 192.168.1.24\n"),
	}, &called)

	prop, res := s.planOrganize(context.Background(), exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	if len(called) != 2 {
		t.Fatalf("expected 2 per-file calls, got %v", called)
	}

	byName := map[string]ProposedChange{}
	for _, c := range prop.Changes {
		byName[c.Name] = c
	}
	if !strings.Contains(byName["personal.md"].NewContent, "Personal loan of 5,000") {
		t.Fatalf("moved fact did not land in personal.md: %+v", byName["personal.md"])
	}
	if strings.Contains(byName["projects.md"].NewContent, "Personal loan") {
		t.Fatalf("moved fact still in source file")
	}
	if prop.Warning != "" {
		t.Fatalf("clean move should not trigger fact-loss warning: %s", prop.Warning)
	}
}

// TestChunkedOrganizePersonalSinkOneWay: a move OUT of personal.md must be
// discarded and the fact restored — content never leaves personal.md.
func TestChunkedOrganizePersonalSinkOneWay(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "personal.md", "- my divorce case #123\n- my salary details\n")
	writeVaultFile(t, root, "business.md", "- company contract with client X\n")

	t.Setenv("AUXLY_ORGANIZE_CHUNK_TOKENS", "1")

	var called []string
	exec := fakeExec(t, map[string]string{
		// Misbehaving model tries to move a personal fact into business.md.
		"personal.md": chunkResp("personal.md", "- my salary details\n",
			map[string]string{"to": "business.md", "fact": "- my divorce case #123"}),
		"business.md": chunkResp("business.md", "- company contract with client X\n"),
	}, &called)

	prop, res := s.planOrganize(context.Background(), exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	for _, c := range prop.Changes {
		if c.Name == "personal.md" && !strings.Contains(c.NewContent, "divorce case #123") {
			t.Fatalf("personal fact lost from personal.md: %s", c.NewContent)
		}
		if c.Name == "business.md" && strings.Contains(c.NewContent, "divorce") {
			t.Fatalf("personal fact leaked into business.md: %s", c.NewContent)
		}
	}
}

// TestChunkedOrganizeInvalidMoveTargetKeepsFact: a bogus move target must not
// lose the fact — it stays in its source file.
func TestChunkedOrganizeInvalidMoveTargetKeepsFact(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "daily.md", "- did a thing today\n")
	writeVaultFile(t, root, "infra.md", "- box one\n")

	t.Setenv("AUXLY_ORGANIZE_CHUNK_TOKENS", "1")

	var called []string
	exec := fakeExec(t, map[string]string{
		"daily.md": chunkResp("daily.md", "",
			map[string]string{"to": "CLAUDE.md", "fact": "- did a thing today"}),
		"infra.md": chunkResp("infra.md", "- box one\n"),
	}, &called)

	prop, res := s.planOrganize(context.Background(), exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	for _, c := range prop.Changes {
		if c.Name == "daily.md" && !strings.Contains(c.NewContent, "did a thing today") {
			t.Fatalf("fact lost on invalid move target: %q", c.NewContent)
		}
		if c.Name == "CLAUDE.md" {
			t.Fatalf("non-organizable file appeared in proposal")
		}
	}
}

// TestSmallVaultStaysWholeVault: below the threshold, exactly one call carries
// the whole vault (existing behavior preserved).
func TestSmallVaultStaysWholeVault(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- name wael\n")
	writeVaultFile(t, root, "infra.md", "- box one\n")

	calls := 0
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		calls++
		if !strings.Contains(user, "=== FILE: identity.md ===") || !strings.Contains(user, "=== FILE: infra.md ===") {
			t.Fatalf("whole-vault prompt missing files: %.200s", user)
		}
		resp := `{"files":[{"name":"identity.md","content":"- name wael\n"},{"name":"infra.md","content":"- box one\n"}]}`
		return organizeRun{jsonContent: resp, modelUsed: "fake", tokensUsed: 10}, OrganizeResult{}, true
	}

	_, res := s.planOrganize(context.Background(), exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	if calls != 1 {
		t.Fatalf("small vault should be one whole-vault call, got %d", calls)
	}
}

// TestChunkedOrganizeAbortsOnFileFailure: one failed per-file call aborts the
// whole plan — never a half-planned proposal.
func TestChunkedOrganizeAbortsOnFileFailure(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- name wael\n")
	writeVaultFile(t, root, "infra.md", "- box one\n")

	t.Setenv("AUXLY_ORGANIZE_CHUNK_TOKENS", "1")

	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		if strings.Contains(user, "infra.md") {
			return organizeRun{}, OrganizeResult{Success: false, Message: "model exploded"}, false
		}
		return organizeRun{jsonContent: chunkResp("identity.md", "- name wael\n"), modelUsed: "fake"}, OrganizeResult{}, true
	}

	prop, res := s.planOrganize(context.Background(), exec)
	if res.Success {
		t.Fatal("expected failure result")
	}
	if len(prop.Changes) != 0 {
		t.Fatalf("failed plan must carry no changes, got %d", len(prop.Changes))
	}
	if !strings.Contains(res.Message, "infra.md") {
		t.Fatalf("failure should name the file: %s", res.Message)
	}
}

// TestStripPersonalLeaks: a model that copies a personal.md fact into a shared
// file's proposed content gets it mechanically stripped (the fact stays in
// personal.md — the sink is enforced in code, not prompt).
func TestStripPersonalLeaks(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "personal.md", "- my divorce case #123\n")

	changes := []ProposedChange{
		{Name: "personal.md", OldContent: "- my divorce case #123\n", NewContent: "- my divorce case #123\n"},
		{Name: "business.md", OldContent: "- client contract\n", NewContent: "- client contract\n- My divorce case #123\n"},
	}
	out := s.stripPersonalLeaks(changes)
	for _, c := range out {
		if c.Name == "business.md" && strings.Contains(strings.ToLower(c.NewContent), "divorce") {
			t.Fatalf("personal fact leaked into shared file: %s", c.NewContent)
		}
		if c.Name == "personal.md" && !strings.Contains(c.NewContent, "divorce case #123") {
			t.Fatalf("personal fact lost from personal.md")
		}
	}
}

// TestFactLossWarningSubsetDetectsBrokenMove: validating an approved SUBSET —
// move source approved (fact removed) but target pinned to disk (fact never
// added) — must flag the loss. This is the TUI submit guard's core logic.
func TestFactLossWarningSubsetDetectsBrokenMove(t *testing.T) {
	effective := []ProposedChange{
		// source approved: fact stripped out
		{Name: "projects.md", OldContent: "- building auxly\n- personal loan of 5,000\n- fact a\n", NewContent: "- building auxly\n- fact a\n"},
		// target rejected: pinned to its on-disk (empty) state
		{Name: "personal.md", OldContent: "", NewContent: ""},
	}
	if w := FactLossWarning(effective); w == "" {
		t.Fatal("broken move (source approved, target rejected) not flagged")
	}
}

// TestUnifiedCompilesLazily: writes no longer regenerate the rollup; the first
// read after a change does, and an unchanged vault doesn't recompile.
func TestUnifiedCompilesLazily(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}

	for i := 0; i < 3; i++ {
		if err := s.Write(fmt.Sprintf("f%d.md", i), fmt.Sprintf("- fact %d\n", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(root + "/unified_memory.md"); !os.IsNotExist(err) {
		t.Fatal("write eagerly compiled unified_memory.md — should be lazy")
	}

	content, err := s.View("unified_memory.md")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(content, fmt.Sprintf("- fact %d", i)) {
			t.Fatalf("unified missing fact %d", i)
		}
	}

	st1, _ := os.Stat(root + "/unified_memory.md")
	if _, err := s.View("unified_memory.md"); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(root + "/unified_memory.md")
	if !st2.ModTime().Equal(st1.ModTime()) {
		t.Fatal("unchanged vault recompiled unified on second read")
	}

	if err := s.Write("f9.md", "- brand new fact\n"); err != nil {
		t.Fatal(err)
	}
	content, _ = s.View("unified_memory.md")
	if !strings.Contains(content, "brand new fact") {
		t.Fatal("stale unified served after a write")
	}
}
