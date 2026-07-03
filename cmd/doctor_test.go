package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
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

// TestDoctorReport_HealsInterruptedOrganize simulates a "decrypt temporarily"
// organize run that got killed before its restore() ran: the crash-recovery
// sentinel is present and personal.md is plaintext on disk despite being
// encrypted-at-rest. doctor REPAIRS this on every run (not just reports it).
func TestDoctorReport_HealsInterruptedOrganize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".initialized"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())

	store := memory.NewStore(dir)
	if err := store.Write("identity.md", "- name wael\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("personal.md", "- secret\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.EncryptFile("personal.md"); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash: file back to plaintext on disk (as
	// TempDecryptForOrganize left it), sentinel present, restore() never ran.
	if err := os.WriteFile(filepath.Join(dir, "personal.md"), []byte("- secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".index"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".index", "reencrypt-pending.json"), []byte(`{"files":["personal.md"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out := doctorReport(dir, false)
	if !strings.Contains(out, "healed 1 file(s) left plaintext by an interrupted organize") {
		t.Fatalf("doctor did not report the heal:\n%s", out)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatal("personal.md still plaintext after doctorReport")
	}
	if _, err := os.Stat(filepath.Join(dir, ".index", "reencrypt-pending.json")); !os.IsNotExist(err) {
		t.Fatal("sentinel not cleared after doctor heal")
	}

	// A second run has nothing left to heal.
	out2 := doctorReport(dir, false)
	if !strings.Contains(out2, "no interrupted organize to heal") {
		t.Fatalf("second doctor run should report nothing to heal:\n%s", out2)
	}
}
