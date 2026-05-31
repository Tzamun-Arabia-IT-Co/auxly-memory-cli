package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedDefaultFiles_CreatesPersonalAndBackfills(t *testing.T) {
	dir := t.TempDir()
	created, err := SeedDefaultFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "personal.md")); statErr != nil {
		t.Error("SeedDefaultFiles must create personal.md")
	}
	// idempotent: second run creates nothing
	again, _ := SeedDefaultFiles(dir)
	if len(again) != 0 {
		t.Errorf("second seed should create nothing, got %v", again)
	}
	// never overwrites: change a file, re-seed, content preserved
	p := filepath.Join(dir, "personal.md")
	os.WriteFile(p, []byte("MINE"), 0o644)
	SeedDefaultFiles(dir)
	if b, _ := os.ReadFile(p); string(b) != "MINE" {
		t.Error("SeedDefaultFiles must never overwrite existing files")
	}
	_ = created
}
