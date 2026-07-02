package vaultcrypt

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"filippo.io/age"
)

func testKeystore(t *testing.T) *Keystore {
	t.Helper()
	ks := NewKeystore(t.TempDir())
	ks.useKeychain = false
	return ks
}

func TestGenerateExistsIdentityRoundTrip(t *testing.T) {
	ks := testKeystore(t)

	if err := ks.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !ks.Exists() {
		t.Fatal("Exists() = false, want true")
	}
	identity, err := ks.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	recipient, err := ks.Recipient()
	if err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}

	ciphertext, err := Encrypt([]byte("vault"), recipient)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	plaintext, err := Decrypt(ciphertext, identity)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(plaintext) != "vault" {
		t.Fatalf("Decrypt() = %q, want %q", plaintext, "vault")
	}
}

func TestIdentityReturnsErrNoKeyWhenAbsent(t *testing.T) {
	ks := testKeystore(t)

	_, err := ks.Identity()
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("Identity() error = %v, want ErrNoKey", err)
	}
}

func TestVaultKeyEnvironmentOverrideWins(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	override, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity() error = %v", err)
	}
	t.Setenv("AUXLY_VAULT_KEY", override.String())

	identity, err := ks.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}

	ciphertext, err := Encrypt([]byte("override"), override.Recipient())
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := Decrypt(ciphertext, identity); err != nil {
		t.Fatalf("Decrypt() with env identity error = %v", err)
	}
}

func TestExportKeyParseable(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	key, err := ks.ExportKey()
	if err != nil {
		t.Fatalf("ExportKey() error = %v", err)
	}
	if _, err := age.ParseX25519Identity(key); err != nil {
		t.Fatalf("ParseX25519Identity(ExportKey()) error = %v", err)
	}
}

func TestRecipientOnlyEncryptionWorksWithoutIdentity(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	recipient, err := ks.Recipient()
	if err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}

	if _, err := Encrypt([]byte("no unlock"), recipient); err != nil {
		t.Fatalf("Encrypt() with Recipient() error = %v", err)
	}
}

// --- finding 1: Generate must refuse rather than clobber existing key material ---

func TestGenerateTwiceReturnsErrKeyExists(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.Generate(); err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}

	if err := ks.Generate(); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("second Generate() error = %v, want ErrKeyExists", err)
	}
}

func TestGenerateRefusesWhenPublicKeyOnlyPresent(t *testing.T) {
	ks := testKeystore(t)
	if err := os.MkdirAll(ks.keysDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(ks.publicKeyPath(), []byte("age1stalepublickeyonly\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := ks.Generate(); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("Generate() error = %v, want ErrKeyExists", err)
	}
}

// --- finding 2: perms must be tightened even on pre-existing file/dir ---

func TestWritePrivateKeyTightensPreExistingLoosePerms(t *testing.T) {
	ks := testKeystore(t)
	if err := os.MkdirAll(ks.keysDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(ks.privateKeyPath(), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := ks.writePrivateKey("test-key-material"); err != nil {
		t.Fatalf("writePrivateKey() error = %v", err)
	}

	info, err := os.Stat(ks.privateKeyPath())
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("vault.key mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestGenerateTightensPreExistingLooseKeysDir(t *testing.T) {
	ks := testKeystore(t)
	if err := os.MkdirAll(ks.keysDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := ks.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	info, err := os.Stat(ks.keysDir())
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("keys/ mode = %v, want 0700", info.Mode().Perm())
	}
}

// --- finding 3: the key must never land on the child process's argv ---

func TestBuildStoreKeychainCmdKeyNotInArgs(t *testing.T) {
	key := "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	cmd := buildStoreKeychainCmd(context.Background(), key)

	for _, arg := range cmd.Args {
		if strings.Contains(arg, key) {
			t.Fatalf("cmd.Args contains key material: %v", cmd.Args)
		}
	}
	if len(cmd.Args) < 2 || cmd.Args[1] != "-i" {
		t.Fatalf("cmd.Args = %v, want [security -i]", cmd.Args)
	}

	stdin, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatalf("read cmd.Stdin error = %v", err)
	}
	if !strings.Contains(string(stdin), key) {
		t.Fatal("expected key to be delivered via stdin, not found")
	}
}

// --- finding 4: Recipient must honor AUXLY_VAULT_KEY too, or encrypt/decrypt can diverge ---

func TestRecipientHonorsEnvironmentOverride(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	override, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity() error = %v", err)
	}
	t.Setenv("AUXLY_VAULT_KEY", override.String())

	recipient, err := ks.Recipient()
	if err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}
	x25519, ok := recipient.(*age.X25519Recipient)
	if !ok {
		t.Fatalf("Recipient() type = %T, want *age.X25519Recipient", recipient)
	}
	if x25519.String() != override.Recipient().String() {
		t.Fatalf("Recipient() = %v, want env identity's recipient %v", x25519.String(), override.Recipient().String())
	}
}

// --- finding 5: locked/denied/timeout keychain errors must not look like "no key" ---

func TestClassifyKeychainErrNotFound(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stderr   string
	}{
		{"exit 44", 44, "unrelated stderr text"},
		{"could not be found phrase", 1, "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyKeychainErr(tt.exitCode, tt.stderr)
			if !errors.Is(err, errKeychainNotFound) {
				t.Fatalf("classifyKeychainErr(%d, %q) = %v, want errKeychainNotFound", tt.exitCode, tt.stderr, err)
			}
		})
	}
}

func TestClassifyKeychainErrUnavailable(t *testing.T) {
	err := classifyKeychainErr(1, "The user name or passphrase you entered is not correct.")
	if !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("classifyKeychainErr() = %v, want ErrKeychainUnavailable", err)
	}
	if errors.Is(err, errKeychainNotFound) {
		t.Fatal("classifyKeychainErr() misclassified a locked/denied failure as not-found")
	}
}

// --- finding 7: corrupt/empty vault.pub must error cleanly, not panic ---

func TestRecipientCorruptPublicKeyFileReturnsError(t *testing.T) {
	ks := testKeystore(t)
	if err := os.MkdirAll(ks.keysDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(ks.publicKeyPath(), []byte("not-a-valid-age-recipient"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ks.Recipient(); err == nil {
		t.Fatal("Recipient() with corrupt vault.pub succeeded, want error")
	}
}

func TestRecipientEmptyPublicKeyFileReturnsError(t *testing.T) {
	ks := testKeystore(t)
	if err := os.MkdirAll(ks.keysDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(ks.publicKeyPath(), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ks.Recipient(); err == nil {
		t.Fatal("Recipient() with empty vault.pub succeeded, want error")
	}
}
