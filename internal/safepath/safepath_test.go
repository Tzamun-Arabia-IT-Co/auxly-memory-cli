package safepath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolve_AllowsLegitimatePaths(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		in   string
		want string // relative to root
	}{
		{"flat file", "projects.md", "projects.md"},
		{"subdir file", "sub/dir/file.md", "sub/dir/file.md"},
		{"dot-slash normalizes", "./identity.md", "identity.md"},
		{"internal dotdot normalizes within root", "sub/../identity.md", "identity.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(root, tc.in)
			if err != nil {
				t.Fatalf("Resolve(%q) unexpected error: %v", tc.in, err)
			}
			want := filepath.Join(root, tc.want)
			if got != want {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.in, got, want)
			}
		})
	}
}

func TestResolve_RejectsEscapes(t *testing.T) {
	root := t.TempDir()
	bad := []string{
		"../etc/passwd",
		"../../secret",
		"..",
		"sub/../../escape.md",
	}
	if runtime.GOOS != "windows" {
		bad = append(bad, "/etc/passwd", "/abs/path.md")
	}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			if got, err := Resolve(root, in); err == nil {
				t.Fatalf("Resolve(%q) = %q, want error", in, got)
			}
		})
	}
}

func TestResolveSafe_AllowsLegitimate(t *testing.T) {
	root := t.TempDir()
	// existing flat file
	if err := os.WriteFile(filepath.Join(root, "projects.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSafe(root, "projects.md"); err != nil {
		t.Fatalf("ResolveSafe legit existing file: %v", err)
	}
	// non-existent target (write path) in a real subdir
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSafe(root, "sub/new.md"); err != nil {
		t.Fatalf("ResolveSafe legit new file in real subdir: %v", err)
	}
}

func TestResolveSafe_RejectsSymlinkedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// projects.md inside the vault is a symlink pointing OUT of the vault.
	if err := os.Symlink(outside, filepath.Join(root, "projects.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveSafe(root, "projects.md"); err == nil {
		t.Fatalf("ResolveSafe followed escaping symlink, got %q, want error", got)
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got: %v", err)
	}
}

func TestResolveSafe_RejectsSymlinkedParentDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outsideDir := t.TempDir()
	// vault/link -> outsideDir ; then a write to link/evil.md must be rejected.
	if err := os.Symlink(outsideDir, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveSafe(root, "link/evil.md"); err == nil {
		t.Fatalf("ResolveSafe followed symlinked parent dir, got %q, want error", got)
	}
}

func TestResolveSafe_FreshRootAllowsFirstWrite(t *testing.T) {
	// The vault root does not exist yet — the first write must still resolve.
	root := filepath.Join(t.TempDir(), "vault-not-created-yet")
	got, err := ResolveSafe(root, "identity.md")
	if err != nil {
		t.Fatalf("ResolveSafe into a fresh (nonexistent) root should succeed: %v", err)
	}
	if want := filepath.Join(root, "identity.md"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
