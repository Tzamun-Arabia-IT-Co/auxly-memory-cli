package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- Characterization: legitimate behavior must be preserved exactly ---

func TestWriteAndView_GlobalFlatFile(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("identity.md", "hello"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := s.View("identity.md")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got != "hello" {
		t.Fatalf("View = %q, want %q", got, "hello")
	}
}

func TestWriteAndView_LegitRelativeSubpath(t *testing.T) {
	// The vault legitimately supports subdirectory writes — the C3 fix must NOT
	// break this (Codex caught the original "reject separators" plan).
	s := &Store{Root: t.TempDir()}
	if err := s.Write("sub/dir/notes.md", "x"); err != nil {
		t.Fatalf("Write subpath: %v", err)
	}
	got, err := s.View("sub/dir/notes.md")
	if err != nil {
		t.Fatalf("View subpath: %v", err)
	}
	if got != "x" {
		t.Fatalf("View subpath = %q, want %q", got, "x")
	}
}

func TestWorkspaceOverridesGlobal(t *testing.T) {
	s := &Store{Root: t.TempDir(), WorkspaceRoot: t.TempDir()}
	if err := s.Write("identity.md", "global"); err != nil {
		t.Fatalf("global write: %v", err)
	}
	if err := s.WriteScoped("identity.md", "workspace", "workspace"); err != nil {
		t.Fatalf("workspace write: %v", err)
	}
	got, err := s.View("identity.md")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got != "workspace" {
		t.Fatalf("View = %q, want workspace override", got)
	}
}

// --- Security: traversal / symlink escapes must be blocked (H5, M1, M2) ---

func TestView_RejectsWorkspaceTraversal(t *testing.T) {
	g := t.TempDir()
	w := t.TempDir()
	// A secret sitting next to (outside) the workspace root.
	secret := filepath.Join(filepath.Dir(w), "secret.md")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Root: g, WorkspaceRoot: w}
	if got, err := s.View("../" + filepath.Base(secret)); err == nil && got == "TOPSECRET" {
		t.Fatalf("View leaked file outside workspace via traversal: %q", got)
	}
}

func TestWriteScoped_RejectsWorkspaceTraversal(t *testing.T) {
	g := t.TempDir()
	w := t.TempDir()
	s := &Store{Root: g, WorkspaceRoot: w}
	if err := s.WriteScoped("../evil.md", "x", "workspace"); err == nil {
		t.Fatalf("WriteScoped allowed traversal write")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(w), "evil.md")); err == nil {
		t.Fatalf("traversal write escaped the workspace root")
	}
}

func TestExists_RejectsWorkspaceTraversal(t *testing.T) {
	g := t.TempDir()
	w := t.TempDir()
	secret := filepath.Join(filepath.Dir(w), "secret.md")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Root: g, WorkspaceRoot: w}
	if s.Exists("../" + filepath.Base(secret)) {
		t.Fatalf("Exists returned true for a path outside the workspace")
	}
}

func TestView_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	g := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the vault pointing out.
	if err := os.Symlink(secret, filepath.Join(g, "link.md")); err != nil {
		t.Fatal(err)
	}
	s := &Store{Root: g}
	if got, err := s.View("link.md"); err == nil && got == "TOPSECRET" {
		t.Fatalf("View followed an escaping symlink: %q", got)
	}
}
