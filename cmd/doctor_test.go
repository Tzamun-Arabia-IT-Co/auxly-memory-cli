package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Doctor must diagnose an uninitialized vault (not panic, not lie) and flag an
// initialized-but-empty state sensibly.
func TestDoctorReportUninitialized(t *testing.T) {
	out := doctorReport(t.TempDir(), false)
	if !strings.Contains(out, "NOT initialized") {
		t.Fatalf("doctor missed the uninitialized state:\n%s", out)
	}
	if !strings.Contains(out, "auxly init") {
		t.Fatalf("doctor gave no fix hint:\n%s", out)
	}
}

func TestDoctorReportInitializedVault(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".initialized"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(dir, "identity.md"), []byte("- name wael\n"), 0644)

	out := doctorReport(dir, false)
	if !strings.Contains(out, "✓ memory initialized") {
		t.Fatalf("doctor missed init marker:\n%s", out)
	}
	// NewStore merges workspace-overlay files when tests run inside the repo, so
	// the count varies — assert the vault line exists for OUR path, not a count.
	if !strings.Contains(out, "✓ vault: "+dir+" — ") {
		t.Fatalf("doctor missed vault contents:\n%s", out)
	}
	if !strings.Contains(out, "no pending approvals") {
		t.Fatalf("doctor missed pending state:\n%s", out)
	}
}
