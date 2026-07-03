package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// MAJOR 8 regression: `encrypt init` on a vault with no personal.md yet must
// seed an EMPTY ENCRYPTED personal.md — otherwise a personal.md created
// later by an agent's first write would default to plaintext and never pick
// up encryption-at-rest (state lives in the file, not config).
func TestSeedEncryptedPersonalMD_CreatesEncryptedEmptyFileAndSticks(t *testing.T) {
	memPath := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())

	store := memory.NewStore(memPath)
	if store.Exists("personal.md") {
		t.Fatal("test premise broken: personal.md already exists")
	}

	if err := seedEncryptedPersonalMD(store, memPath); err != nil {
		t.Fatalf("seedEncryptedPersonalMD: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(memPath, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatalf("personal.md is not encrypted at rest: %q", raw)
	}

	// A subsequent WriteScoped (the shape of an agent's first personal
	// write) must preserve the encryption — state lives in the file.
	if err := store.WriteScoped("personal.md", "# Personal\n\n- a new fact\n", "global"); err != nil {
		t.Fatalf("WriteScoped: %v", err)
	}
	raw2, err := os.ReadFile(filepath.Join(memPath, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw2) {
		t.Fatal("personal.md became plaintext after a subsequent write — encryption did not stick")
	}
}
