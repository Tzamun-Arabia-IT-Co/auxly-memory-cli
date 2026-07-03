package pending

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// TestApprove_EncryptedTargetStaysEncrypted is the ApplyDiff-on-an-encrypted-
// target acceptance test: approve() must decrypt the target to apply the
// diff's text lines, then re-encrypt on write — the merged fact lands, and
// the on-disk file never loses its age header or leaks plaintext.
func TestApprove_EncryptedTargetStaysEncrypted(t *testing.T) {
	root := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())

	original := "- existing encrypted fact\n"
	ciphertext, err := vaultcrypt.Encrypt([]byte(original), identity.Recipient())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	targetPath := filepath.Join(root, "business.md")
	if err := os.WriteFile(targetPath, ciphertext, 0o644); err != nil {
		t.Fatalf("seed encrypted target: %v", err)
	}

	m := NewManager(root)
	name, err := m.WriteFrom("business.md", "+new fact from an agent", "")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if err := m.Approve(name); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	raw, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatalf("business.md lost its encryption after approve: %q", raw)
	}
	if strings.Contains(string(raw), "new fact") {
		t.Fatal("plaintext leaked into the on-disk file")
	}

	store := &memory.Store{Root: root}
	plain, encrypted, err := store.ReadVaultFile(targetPath)
	if err != nil {
		t.Fatalf("decrypt target after approve: %v", err)
	}
	if !encrypted {
		t.Fatal("ReadVaultFile reports business.md as not encrypted")
	}
	if !strings.Contains(string(plain), "existing encrypted fact") || !strings.Contains(string(plain), "new fact from an agent") {
		t.Fatalf("merged content = %q, want both the original and new fact", plain)
	}
}
