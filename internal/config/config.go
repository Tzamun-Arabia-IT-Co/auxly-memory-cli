package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	EnvMemoryPath = "AUXLY_MEMORY_PATH"
	DefaultDir    = ".auxly"
	MemoryDir     = "memory"
	SettingsFile  = "settings.json"
)

// ResolveMemoryPath returns the effective memory folder path.
// Priority: AUXLY_MEMORY_PATH env > ~/.auxly/memory/
func ResolveMemoryPath() string {
	if envPath := os.Getenv(EnvMemoryPath); envPath != "" {
		return envPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, DefaultDir, MemoryDir)
}

// Settings holds persisted user preferences not derivable from the environment.
// It lives at ~/.auxly/settings.json. Fields default to their zero value so a
// missing file behaves like all-defaults.
type Settings struct {
	// LiveUsage opts into the dashboard usage panel, which calls each agent's
	// provider with its stored login token. Off by default to preserve Auxly's
	// local-first, zero-network behavior.
	LiveUsage bool `json:"liveUsage"`
}

// settingsPath returns ~/.auxly/settings.json.
func settingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, DefaultDir, SettingsFile)
}

// LoadSettings reads persisted settings, returning defaults if the file is
// missing or unreadable (never errors — settings are best-effort).
func LoadSettings() Settings {
	var s Settings
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// SaveSettings persists settings to ~/.auxly/settings.json (mode 0600).
func SaveSettings(s Settings) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), b, 0o600)
}
