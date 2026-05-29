package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds git sync configuration from git.yaml.
type Config struct {
	AutoCommit          bool   `yaml:"auto_commit"`
	AutoPush            bool   `yaml:"auto_push"`
	CommitMessagePrefix string `yaml:"commit_message_prefix"`
	Branch              string `yaml:"branch"`
}

// LoadConfig reads git.yaml from the memory root.
func LoadConfig(memoryRoot string) (*Config, error) {
	path := filepath.Join(memoryRoot, "git.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{AutoCommit: true, AutoPush: false, CommitMessagePrefix: "auxly:", Branch: "main"}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// AutoCommit performs a git add + commit for a specific file change.
func AutoCommit(memoryRoot, file, reason string) error {
	cfg, err := LoadConfig(memoryRoot)
	if err != nil {
		return err
	}

	if !isGitRepo(memoryRoot) {
		return nil
	}

	// git add
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = memoryRoot
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// git commit
	msg := fmt.Sprintf("%s %s — %s", cfg.CommitMessagePrefix, file, reason)
	commitCmd := exec.Command("git", "commit", "-m", msg, "--allow-empty")
	commitCmd.Dir = memoryRoot
	if err := commitCmd.Run(); err != nil {
		// Ignore "nothing to commit" errors
		return nil
	}

	return nil
}

// Sync performs git add + commit + push.
func Sync(memoryRoot string) error {
	cfg, err := LoadConfig(memoryRoot)
	if err != nil {
		return err
	}

	if !isGitRepo(memoryRoot) {
		return fmt.Errorf("memory folder is not a git repository. Run 'git init' in %s first", memoryRoot)
	}

	// git add
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = memoryRoot
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s", out)
	}

	// git commit
	msg := fmt.Sprintf("%s sync", cfg.CommitMessagePrefix)
	commitCmd := exec.Command("git", "commit", "-m", msg, "--allow-empty")
	commitCmd.Dir = memoryRoot
	commitCmd.CombinedOutput() // ignore error (nothing to commit)

	// git push
	if cfg.AutoPush || true { // sync always pushes
		branch := cfg.Branch
		if branch == "" {
			branch = "main"
		}
		pushCmd := exec.Command("git", "push", "origin", branch)
		pushCmd.Dir = memoryRoot
		if out, err := pushCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git push failed: %s", out)
		}
	}

	return nil
}

func isGitRepo(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}
