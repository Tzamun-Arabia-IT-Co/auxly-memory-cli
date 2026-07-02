package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Install/remove agent hooks that auto-capture session facts (opt-in)",
}

var hooksInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Add a Claude Code Stop hook that runs `auxly capture` after each session",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := claudeSettingsPath()
		if err != nil {
			return err
		}
		changed, err := installCaptureHook(path)
		if err != nil {
			return err
		}
		if changed {
			fmt.Println("✅ Stop hook installed — facts from each Claude Code session flow into `auxly pending`.")
			fmt.Println("   Remove any time with `auxly hooks uninstall`.")
		} else {
			fmt.Println("✓ Stop hook already installed — nothing to do.")
		}
		return nil
	},
}

var hooksUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Remove the auxly capture Stop hook from Claude Code settings",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := claudeSettingsPath()
		if err != nil {
			return err
		}
		removed, err := uninstallCaptureHook(path)
		if err != nil {
			return err
		}
		if removed {
			fmt.Println("🗑️  auxly capture hook removed.")
		} else {
			fmt.Println("✓ No auxly capture hook found — nothing to remove.")
		}
		return nil
	},
}

func init() {
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	rootCmd.AddCommand(hooksCmd)
}

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// captureHookCommand is the marker by which install/uninstall recognize their
// own entry — never touch anything else in the user's settings.
const captureHookCommand = "auxly capture --stop-hook"

// installCaptureHook merges a Stop hook into settings.json (read-merge-write,
// never clobber). Returns false when the hook is already present (idempotent).
func installCaptureHook(path string) (bool, error) {
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if jerr := json.Unmarshal(data, &settings); jerr != nil {
			return false, fmt.Errorf("%s is not valid JSON — fix it first, refusing to clobber: %w", path, jerr)
		}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	stop, _ := hooks["Stop"].([]any)
	for _, entry := range stop {
		if entryHasCaptureCommand(entry) {
			return false, nil
		}
	}

	stop = append(stop, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": captureHookCommand,
			"timeout": 120,
		}},
	})
	hooks["Stop"] = stop
	settings["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(data, '\n'), 0644)
}

// uninstallCaptureHook removes exactly the entries carrying our command,
// preserving every other hook byte-for-byte (via the same parse/merge).
func uninstallCaptureHook(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, fmt.Errorf("%s is not valid JSON — refusing to touch it: %w", path, err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	if len(stop) == 0 {
		return false, nil
	}
	kept := make([]any, 0, len(stop))
	removed := false
	for _, entry := range stop {
		if entryHasCaptureCommand(entry) {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return false, nil
	}
	if len(kept) > 0 {
		hooks["Stop"] = kept
	} else {
		delete(hooks, "Stop")
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(out, '\n'), 0644)
}

func entryHasCaptureCommand(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, captureHookCommand) {
			return true
		}
	}
	return false
}
