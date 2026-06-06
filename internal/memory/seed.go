package memory

import (
	"os"
	"path/filepath"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/templates"
)

// SeedDefaultFiles copies every embedded default template (*.md / *.yaml) into the
// memory root, creating ONLY files that are missing. It is idempotent and safe to
// call on every init / setup / update — existing files are never overwritten, so
// new default files (e.g. personal.md added in a release) are back-filled for
// existing users without disturbing their data. Returns the names created.
func SeedDefaultFiles(root string) ([]string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Join(root, ".pending"), 0o700)

	entries, err := templates.FS.ReadDir(".")
	if err != nil {
		return nil, err
	}

	var created []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "embed.go" {
			continue
		}
		dest := filepath.Join(root, e.Name())
		if _, statErr := os.Stat(dest); statErr == nil {
			continue // already present — never overwrite user data
		}
		data, readErr := templates.FS.ReadFile(e.Name())
		if readErr != nil {
			continue
		}
		if writeErr := os.WriteFile(dest, data, 0o644); writeErr != nil {
			continue
		}
		created = append(created, e.Name())
	}
	return created, nil
}
