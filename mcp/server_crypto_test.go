package mcp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// CRITICAL 1 regression: toolWriteScoped's auto-trust path must fail CLOSED
// when the target file is encrypted and its key is unreachable — never treat
// a non-NotExist View error as "empty file". Before the fix, this silently
// re-encrypted the file with ONLY the new diff, wiping every prior fact.
//
// "Key unreachable" is simulated hermetically: AUXLY_VAULT_KEY resolves to a
// VALID but WRONG identity, so env resolution itself succeeds (no keychain/
// file exec at all — same technique as cryptio_test.go's testVaultIdentity)
// but decrypting this file's ciphertext against it fails, producing exactly
// the class of error (non-NotExist) the fix must catch.
func TestToolWrite_EncryptedFileKeyUnreachable_FailsClosed(t *testing.T) {
	dir := t.TempDir()

	cfg := &trust.Config{Default: trust.LevelAuto, Providers: map[string]trust.ProviderConfig{}}
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_PROVIDER", "claude")

	seedIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	original := "- pre-existing fact one\n- pre-existing fact two\n"
	ciphertext, err := vaultcrypt.Encrypt([]byte(original), seedIdentity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dir, "identity.md")
	if err := os.WriteFile(targetPath, ciphertext, 0o644); err != nil {
		t.Fatal(err)
	}
	rawBefore, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	wrongIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", wrongIdentity.String())

	s := NewServer(dir)
	s.store.WorkspaceRoot = ""
	var buf bytes.Buffer
	s.outWriter = &buf

	res := s.toolWriteScoped("identity.md", "+ - a brand new fact\n", "test", "claude", "global")
	if !res.IsError {
		t.Fatalf("toolWriteScoped succeeded against an undecryptable encrypted file, want an error result; got: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "encrypt status") {
		t.Fatalf("error should mention `auxly encrypt status`, got: %s", resultText(res))
	}

	rawAfter, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawBefore, rawAfter) {
		t.Fatal("identity.md bytes changed despite the fail-closed error — prior facts may have been silently wiped")
	}
}

// MAJOR 10 regression: an encrypted file present in the vault must produce an
// honest degradation note on semantic recall output, not silent exclusion.
func TestToolRecall_NotesExcludedEncryptedFiles(t *testing.T) {
	dir := t.TempDir()
	const fact = "launching widget in October"
	if err := os.WriteFile(filepath.Join(dir, "projects.md"), []byte("# Projects\n- "+fact+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := vaultcrypt.Encrypt([]byte("- secret business fact\n"), identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "business.md"), ciphertext, 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewServer(dir)
	s.store.WorkspaceRoot = ""
	withStubEmbedder(s, true)

	out := resultText(s.toolRecall("- " + fact))
	if !strings.Contains(out, "projects.md") {
		t.Fatalf("expected a real hit alongside the note, got: %q", out)
	}
	if !strings.Contains(out, "1 encrypted file(s) are excluded from semantic recall") {
		t.Fatalf("recall output missing the encrypted-exclusion note: %q", out)
	}
	if !strings.Contains(out, "memory_read") {
		t.Fatalf("note should point at the memory_read escape hatch: %q", out)
	}
}
