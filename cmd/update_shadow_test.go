package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSameBinary_SymlinkIsNotAShadow guards against a false "you have a stale
// copy" warning: a symlinked install (Homebrew's shim into the Caskroom, or
// ~/.local/bin/auxly -> a real path) is ONE binary reached two ways, not two
// competing copies.
func TestSameBinary_SymlinkIsNotAShadow(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "auxly-real")
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "auxly-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !sameBinary(link, real) {
		t.Error("sameBinary said a symlink and its target are different binaries — update would warn about a shadow that does not exist")
	}
}

// TestSameBinary_DistinctCopiesAreAShadow is the real-world failure this whole
// check exists for: two SEPARATE auxly binaries at different versions, where
// the one earlier in PATH silently wins and the update looks like a no-op.
func TestSameBinary_DistinctCopiesAreAShadow(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "auxly-new")
	b := filepath.Join(dir, "auxly-stale")
	if err := os.WriteFile(a, []byte("v1.4.5"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(b, []byte("v1.4.3"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if sameBinary(a, b) {
		t.Error("sameBinary treated two distinct copies as one — a stale binary shadowing the update would go unreported")
	}
}

// TestSameBinary_HardLinkIsNotAShadow covers the same-file-different-path case
// that EvalSymlinks alone cannot see.
func TestSameBinary_HardLinkIsNotAShadow(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "auxly-a")
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	hard := filepath.Join(dir, "auxly-b")
	if err := os.Link(real, hard); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if !sameBinary(real, hard) {
		t.Error("sameBinary missed a hard link to the same file — would warn about a nonexistent shadow")
	}
}

// TestWarnIfShadowedInstall_EmptyPathIsNoop keeps the check from doing anything
// when the update path is unknown; the update itself already succeeded and this
// warning must never turn into a failure.
func TestWarnIfShadowedInstall_EmptyPathIsNoop(t *testing.T) {
	warnIfShadowedInstall("") // must not panic
}
