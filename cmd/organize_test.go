package cmd

import (
	"os"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// MAJOR 9 regression: splitting an ENCRYPTED projects.md must pre-create each
// missing projects/<slug>.md as an empty ENCRYPTED file before queueing its
// first pending addition — otherwise approving that addition would create
// the sub-file as plaintext (state lives in the file, not config).
func TestSeedEncryptedProjectSubFile_ApprovedSplitStaysEncrypted(t *testing.T) {
	memPath := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())

	store := memory.NewStore(memPath)
	subFile := "projects/widget.md"
	if store.Exists(subFile) {
		t.Fatal("test premise broken: sub-file already exists")
	}

	created, err := seedEncryptedProjectSubFile(store, memPath, subFile, true)
	if err != nil {
		t.Fatalf("seedEncryptedProjectSubFile: %v", err)
	}
	if !created {
		t.Fatal("expected the sub-file to be created")
	}

	mgr := pending.NewManager(memPath)
	name, err := mgr.WriteFrom(subFile, "+- first fact about widget\n", "organize-split")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if err := mgr.Approve(name); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	raw, err := os.ReadFile(memPath + "/" + subFile)
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatalf("%s is not encrypted at rest after approval: %q", subFile, raw)
	}

	created2, err := seedEncryptedProjectSubFile(store, memPath, subFile, true)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("seedEncryptedProjectSubFile re-created an already-existing sub-file")
	}
}
