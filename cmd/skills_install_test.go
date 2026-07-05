package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInjectAuxlyContextBlock_AppendReplaceIdempotent locks the behavior
// instruction-based agents (Codex, Gemini, Antigravity, Cursor) depend on:
// a fresh file gets the marker-delimited block, a second run replaces it in
// place rather than duplicating it, and pre-existing content is preserved.
func TestInjectAuxlyContextBlock_AppendReplaceIdempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "AGENTS.md")

	injectAuxlyContextBlock(file)

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("block was not written: %v", err)
	}
	text := string(got)
	if !strings.Contains(text, auxlyContextBlockStart) || !strings.Contains(text, auxlyContextBlockEnd) {
		t.Fatalf("missing markers in:\n%s", text)
	}
	if !strings.Contains(text, "auxly_skill_sync") {
		t.Fatalf("block does not mention auxly_skill_sync:\n%s", text)
	}

	// Second call must replace, not duplicate.
	injectAuxlyContextBlock(file)
	got2, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got2), auxlyContextBlockStart); n != 1 {
		t.Fatalf("want exactly 1 block after re-run, got %d:\n%s", n, got2)
	}

	// Pre-existing unrelated content in another file must survive the inject.
	file2 := filepath.Join(dir, "existing.md")
	preamble := "# My Notes\nSome existing content the agent already had.\n"
	if err := os.WriteFile(file2, []byte(preamble), 0644); err != nil {
		t.Fatal(err)
	}
	injectAuxlyContextBlock(file2)
	got3, err := os.ReadFile(file2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got3), strings.TrimRight(preamble, "\n")) {
		t.Fatalf("pre-existing content was not preserved:\n%s", got3)
	}
	if n := strings.Count(string(got3), auxlyContextBlockStart); n != 1 {
		t.Fatalf("want exactly 1 block, got %d:\n%s", n, got3)
	}
}

// TestInstallAuxlyContextBlocks_OnlyDetectedAgents verifies the block is
// injected only for an agent whose detect dir actually exists — it must never
// fabricate a config dir (e.g. ~/.gemini) for a tool that isn't installed.
func TestInstallAuxlyContextBlocks_OnlyDetectedAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}

	installAuxlyContextBlocks(home)

	codexAgents := filepath.Join(home, ".codex", "AGENTS.md")
	data, err := os.ReadFile(codexAgents)
	if err != nil {
		t.Fatalf("expected %s to be written: %v", codexAgents, err)
	}
	if !strings.Contains(string(data), auxlyContextBlockStart) {
		t.Fatalf("codex AGENTS.md missing the auxly block:\n%s", data)
	}

	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("gemini dir should not have been created (absent = not installed), stat err = %v", err)
	}
}

// TestInstallAuxlySkills_SkipsInstructionBasedSkillDirs guards against the
// original bug: Codex and Gemini never read a SKILL.md directory, so
// installAuxlySkills must not write one there anymore.
func TestInstallAuxlySkills_SkipsInstructionBasedSkillDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // keep local skill writes out of the real repo tree

	installAuxlySkills("")

	for _, dir := range []string{
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(home, ".gemini", "config", "skills"),
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected %s to not be created, stat err = %v", dir, err)
		}
	}

	// Claude is skills-native and must still get the skills.
	if entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills")); err != nil || len(entries) == 0 {
		t.Fatalf("expected Claude global skills dir to be populated, err=%v entries=%v", err, entries)
	}
}
