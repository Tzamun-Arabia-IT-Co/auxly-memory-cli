package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallAuxlySkills_Copilot verifies the auxly skills land in Copilot's
// slash-command dir (~/.copilot/skills/<name>/SKILL.md) when Copilot is present,
// and are skipped when it isn't.
func TestInstallAuxlySkills_Copilot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// installAuxlySkills also writes a local .claude/skills in the CWD; isolate
	// it into a scratch dir so it never pollutes the repo.
	orig, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// Case 1: Copilot installed → skills written.
	copilotHome := filepath.Join(home, ".copilot")
	if err := os.MkdirAll(copilotHome, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_HOME", copilotHome)

	installAuxlySkills("")

	skill := filepath.Join(copilotHome, "skills", "auxly-init", "SKILL.md")
	data, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("auxly-init not installed to Copilot: %v", err)
	}
	if !strings.Contains(string(data), "auxly_skill_init") {
		t.Errorf("Copilot SKILL.md missing tool reference:\n%s", data)
	}
	// A few more of the 10 should be present.
	for _, name := range []string{"auxly-sync", "auxly-max", "auxly-memory"} {
		if _, err := os.Stat(filepath.Join(copilotHome, "skills", name, "SKILL.md")); err != nil {
			t.Errorf("%s not installed to Copilot: %v", name, err)
		}
	}

	// Case 2: Copilot absent → nothing written there.
	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	t.Setenv("COPILOT_HOME", filepath.Join(home2, ".copilot")) // does not exist
	installAuxlySkills("")
	if _, err := os.Stat(filepath.Join(home2, ".copilot")); !os.IsNotExist(err) {
		t.Errorf("Copilot dir should not be created when Copilot is absent (err=%v)", err)
	}
}
