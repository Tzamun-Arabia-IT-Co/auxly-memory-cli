package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// forceNonTerminalStdin makes readPassphrase take its piped-stdin fallback
// path for the duration of the test, regardless of whether the test process
// itself happens to have a real terminal attached.
func forceNonTerminalStdin(t *testing.T) {
	t.Helper()
	prev := isTerminalStdin
	isTerminalStdin = func() bool { return false }
	t.Cleanup(func() { isTerminalStdin = prev })
}

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

// migratePersonalMD must produce the same encrypted-from-day-one outcome in
// passphrase mode as it does in keypair mode above. The mode marker and
// AUXLY_VAULT_PASSPHRASE are set directly on disk/env here (rather than via
// vaultcrypt.Keystore.GeneratePassphrase) so this stays hermetic — Generate
// calls try the macOS keychain first, which this test must not touch.
func TestMigratePersonalMDPassphraseModeCreatesEncryptedEmptyFileAndSticks(t *testing.T) {
	memPath := t.TempDir()
	keysDir := filepath.Join(filepath.Dir(memPath), "keys")
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, "mode"), []byte("passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_PASSPHRASE", "a-strong-test-password")

	store := memory.NewStore(memPath)
	if store.Exists("personal.md") {
		t.Fatal("test premise broken: personal.md already exists")
	}

	migratePersonalMD(store, memPath)

	raw, err := os.ReadFile(filepath.Join(memPath, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatalf("personal.md is not encrypted at rest: %q", raw)
	}

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

// --- promptNewPassphrase ------------------------------------------------

func TestPromptNewPassphraseAcceptsMatchingPair(t *testing.T) {
	forceNonTerminalStdin(t)
	r := bufio.NewReader(strings.NewReader("goodpassword\ngoodpassword\n"))

	pass, err := promptNewPassphrase(r)
	if err != nil {
		t.Fatalf("promptNewPassphrase() error = %v", err)
	}
	if pass != "goodpassword" {
		t.Fatalf("promptNewPassphrase() = %q, want %q", pass, "goodpassword")
	}
}

func TestPromptNewPassphraseRejectsTooShortThenAccepts(t *testing.T) {
	forceNonTerminalStdin(t)
	// "short" (5 chars) must be rejected before either prompt reads the
	// following confirmation line.
	r := bufio.NewReader(strings.NewReader("short\nlongenough1\nlongenough1\n"))

	pass, err := promptNewPassphrase(r)
	if err != nil {
		t.Fatalf("promptNewPassphrase() error = %v", err)
	}
	if pass != "longenough1" {
		t.Fatalf("promptNewPassphrase() = %q, want %q", pass, "longenough1")
	}
}

func TestPromptNewPassphraseRejectsMismatchThenAccepts(t *testing.T) {
	forceNonTerminalStdin(t)
	r := bufio.NewReader(strings.NewReader("password-one\npassword-two\npassword-one\npassword-one\n"))

	pass, err := promptNewPassphrase(r)
	if err != nil {
		t.Fatalf("promptNewPassphrase() error = %v", err)
	}
	if pass != "password-one" {
		t.Fatalf("promptNewPassphrase() = %q, want %q", pass, "password-one")
	}
}

func TestPromptNewPassphraseNoInputReturnsErrorNotInfiniteLoop(t *testing.T) {
	forceNonTerminalStdin(t)
	r := bufio.NewReader(strings.NewReader(""))

	if _, err := promptNewPassphrase(r); err == nil {
		t.Fatal("promptNewPassphrase() with no input succeeded, want error")
	}
}
