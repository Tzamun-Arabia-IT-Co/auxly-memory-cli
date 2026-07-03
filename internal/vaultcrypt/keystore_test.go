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

// --- passphrase mode ---------------------------------------------------

func TestGeneratePassphraseIdentityRoundTrip(t *testing.T) {
	ks := testKeystore(t)

	if err := ks.GeneratePassphrase("correct horse battery staple"); err != nil {
		t.Fatalf("GeneratePassphrase() error = %v", err)
	}
	if ks.Mode() != modePassphrase {
		t.Fatalf("Mode() = %q, want %q", ks.Mode(), modePassphrase)
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

func TestPassphraseOKWrongPassFailsClosed(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.GeneratePassphrase("the-real-password"); err != nil {
		t.Fatalf("GeneratePassphrase() error = %v", err)
	}

	ok, err := ks.PassphraseOK("the-real-password")
	if err != nil || !ok {
		t.Fatalf("PassphraseOK(correct) = (%v, %v), want (true, nil)", ok, err)
	}

	ok, err = ks.PassphraseOK("a-wrong-guess")
	if err != nil {
		t.Fatalf("PassphraseOK(wrong) unexpected error = %v", err)
	}
	if ok {
		t.Fatal("PassphraseOK(wrong) = true, want false")
	}

	// Decrypt itself must also fail closed under the wrong passphrase - not
	// just PassphraseOK's verifier check.
	wrongIdentity, err := age.NewScryptIdentity("a-wrong-guess")
	if err != nil {
		t.Fatalf("NewScryptIdentity() error = %v", err)
	}
	recipient, err := ks.Recipient()
	if err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}
	ciphertext, err := Encrypt([]byte("secret"), recipient)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := Decrypt(ciphertext, wrongIdentity); err == nil {
		t.Fatal("Decrypt() with wrong passphrase identity succeeded, want error")
	}
}

func TestGeneratePassphraseRefusesWhenKeypairAlreadyExists(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if err := ks.GeneratePassphrase("some-password-1"); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("GeneratePassphrase() error = %v, want ErrKeyExists", err)
	}
}

func TestGenerateRefusesWhenPassphraseAlreadyExists(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.GeneratePassphrase("some-password-1"); err != nil {
		t.Fatalf("GeneratePassphrase() error = %v", err)
	}

	if err := ks.Generate(); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("Generate() error = %v, want ErrKeyExists", err)
	}
}

func TestGeneratePassphraseTwiceReturnsErrKeyExists(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.GeneratePassphrase("some-password-1"); err != nil {
		t.Fatalf("first GeneratePassphrase() error = %v", err)
	}

	if err := ks.GeneratePassphrase("some-password-2"); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("second GeneratePassphrase() error = %v, want ErrKeyExists", err)
	}
}

// Mode must be resolved from disk, not cached in-memory: a fresh Keystore
// instance pointed at the same directory (a separate CLI invocation, in
// practice) must see the same mode.
func TestModeResolvedAcrossFreshKeystoreInstance(t *testing.T) {
	dir := t.TempDir()
	ks1 := NewKeystore(dir)
	ks1.useKeychain = false
	if err := ks1.GeneratePassphrase("some-password-1"); err != nil {
		t.Fatalf("GeneratePassphrase() error = %v", err)
	}

	ks2 := NewKeystore(dir)
	ks2.useKeychain = false
	if ks2.Mode() != modePassphrase {
		t.Fatalf("fresh Keystore Mode() = %q, want %q", ks2.Mode(), modePassphrase)
	}
	if _, err := ks2.Identity(); err != nil {
		t.Fatalf("fresh Keystore Identity() error = %v", err)
	}
}

func TestPassphraseEnvironmentOverrideWins(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.GeneratePassphrase("stored-on-disk-password"); err != nil {
		t.Fatalf("GeneratePassphrase() error = %v", err)
	}
	t.Setenv("AUXLY_VAULT_PASSPHRASE", "env-override-password")

	source, err := ks.Source()
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if source != "env" {
		t.Fatalf("Source() = %q, want %q", source, "env")
	}

	identity, err := ks.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	envIdentity, err := age.NewScryptIdentity("env-override-password")
	if err != nil {
		t.Fatalf("NewScryptIdentity() error = %v", err)
	}
	recipient, err := newScryptRecipient("env-override-password")
	if err != nil {
		t.Fatalf("newScryptRecipient() error = %v", err)
	}
	ciphertext, err := Encrypt([]byte("override"), recipient)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := Decrypt(ciphertext, identity); err != nil {
		t.Fatalf("Decrypt() with env-resolved identity error = %v", err)
	}
	if _, err := Decrypt(ciphertext, envIdentity); err != nil {
		t.Fatalf("Decrypt() with independently-derived env identity error = %v", err)
	}
}

func TestVerifierRejectsWrongPassWithoutTouchingRealFiles(t *testing.T) {
	ks := testKeystore(t)
	if err := ks.GeneratePassphrase("the-real-password"); err != nil {
		t.Fatalf("GeneratePassphrase() error = %v", err)
	}
	recipient, err := ks.Recipient()
	if err != nil {
		t.Fatalf("Recipient() error = %v", err)
	}
	realCiphertext, err := Encrypt([]byte("real content"), recipient)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	ok, err := ks.PassphraseOK("wrong-password")
	if err != nil {
		t.Fatalf("PassphraseOK() unexpected error = %v", err)
	}
	if ok {
		t.Fatal("PassphraseOK(wrong) = true, want false")
	}

	// The verifier check must not have disturbed the real content's
	// decryptability under the correct passphrase.
	identity, err := ks.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	plain, err := Decrypt(realCiphertext, identity)
	if err != nil || string(plain) != "real content" {
		t.Fatalf("Decrypt() after failed PassphraseOK = (%q, %v), want (\"real content\", nil)", plain, err)
	}
}

func TestEncryptStatusReportsMode(t *testing.T) {
	ks := testKeystore(t)
	if ks.Mode() != modeKeypair {
		t.Fatalf("Mode() on empty keystore = %q, want %q (default)", ks.Mode(), modeKeypair)
	}

	if err := ks.GeneratePassphrase("some-password-1"); err != nil {
		t.Fatalf("GeneratePassphrase() error = %v", err)
	}
	if ks.Mode() != modePassphrase {
		t.Fatalf("Mode() = %q, want %q", ks.Mode(), modePassphrase)
	}
}
