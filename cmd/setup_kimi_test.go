package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegisterKimiSkillDir verifies the in-place edit of the extra_skill_dirs
// line in Kimi's config.toml: empty array gets the path, a populated array gets
// it appended, a second registration is a no-op (idempotent), and a missing key
// is appended.
func TestRegisterKimiSkillDir(t *testing.T) {
	skillsRoot := "/home/u/.kimi-code/auxly-skills"

	cases := []struct {
		name      string
		initial   string
		wantLine  string
		wantNoDup bool
	}{
		{
			name:     "empty array",
			initial:  "merge_all_available_skills = true\nextra_skill_dirs = []\n",
			wantLine: `extra_skill_dirs = ["/home/u/.kimi-code/auxly-skills"]`,
		},
		{
			name:     "populated array",
			initial:  "extra_skill_dirs = [\"/existing/dir\"]\n",
			wantLine: `extra_skill_dirs = ["/existing/dir", "/home/u/.kimi-code/auxly-skills"]`,
		},
		{
			name:     "missing key appended",
			initial:  "merge_all_available_skills = true\n",
			wantLine: `extra_skill_dirs = ["/home/u/.kimi-code/auxly-skills"]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(cfg, []byte(tc.initial), 0644); err != nil {
				t.Fatal(err)
			}

			registerKimiSkillDir(cfg, skillsRoot)

			got, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tc.wantLine) {
				t.Fatalf("want line %q in config, got:\n%s", tc.wantLine, got)
			}

			// Idempotency: a second call must not change the file.
			before := string(got)
			registerKimiSkillDir(cfg, skillsRoot)
			after, _ := os.ReadFile(cfg)
			if string(after) != before {
				t.Fatalf("second registration changed the file (not idempotent):\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if strings.Count(string(after), skillsRoot) != 1 {
				t.Fatalf("path appears %d times, want exactly 1:\n%s", strings.Count(string(after), skillsRoot), after)
			}
		})
	}
}

// TestRegisterKimiSkillDir_NoConfig verifies a missing config file is a safe
// no-op (Kimi writes config.toml on first run; we don't fabricate one).
func TestRegisterKimiSkillDir_NoConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "does-not-exist.toml")
	registerKimiSkillDir(cfg, "/x/auxly-skills") // must not panic or create the file
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("config file should not be created, stat err = %v", err)
	}
}
