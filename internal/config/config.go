package config

import (
	"os"
	"path/filepath"
)

const (
	EnvMemoryPath = "AUXLY_MEMORY_PATH"
	DefaultDir    = ".auxly"
	MemoryDir     = "memory"
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
