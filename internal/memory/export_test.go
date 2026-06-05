package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportTagsFilesWithNameAndTimestamp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "projects.md"), []byte("# Projects\nbuild auxly"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "identity.md"), []byte("# Identity\nWael"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(root)
	store.WorkspaceRoot = "" // isolate to the tempdir (ignore the repo workspace)

	dest := t.TempDir()
	now := time.Date(2026, 6, 5, 2, 17, 18, 0, time.UTC)
	res, err := store.Export(dest, now)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Folder is tagged with the timestamp.
	stamp := "2026-06-05_021718"
	wantDir := filepath.Join(dest, "auxly-memory-export-"+stamp)
	if res.Dir != wantDir {
		t.Errorf("export dir = %q, want %q", res.Dir, wantDir)
	}
	if len(res.Files) != 2 {
		t.Fatalf("want 2 exported files, got %d (%v)", len(res.Files), res.Files)
	}

	// Each file's NAME carries the original name + timestamp.
	projects := filepath.Join(wantDir, "projects__"+stamp+".md")
	data, err := os.ReadFile(projects)
	if err != nil {
		t.Fatalf("expected exported file %q: %v", projects, err)
	}
	body := string(data)

	// The header tags the file with its original name + the export time, and the
	// original content is preserved below it.
	if !strings.Contains(body, "file: projects.md") || !strings.Contains(body, now.Format(time.RFC3339)) {
		t.Errorf("export header missing name/timestamp tag:\n%s", body)
	}
	if !strings.Contains(body, "build auxly") {
		t.Error("export must preserve the original file content")
	}

	// A MANIFEST records the set.
	manifest, err := os.ReadFile(filepath.Join(wantDir, "MANIFEST.txt"))
	if err != nil {
		t.Fatalf("expected a MANIFEST: %v", err)
	}
	if !strings.Contains(string(manifest), "projects.md") || !strings.Contains(string(manifest), "identity.md") {
		t.Errorf("MANIFEST should list every exported file:\n%s", manifest)
	}
}

func TestExportEmptyVaultErrors(t *testing.T) {
	store := NewStore(t.TempDir())
	store.WorkspaceRoot = "" // no .md files anywhere
	if _, err := store.Export(t.TempDir(), time.Now()); err == nil {
		t.Error("exporting an empty vault should return an error, not a stray folder")
	}
}
