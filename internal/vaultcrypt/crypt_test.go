package vaultcrypt

import (
	"bytes"
	"testing"

	"filippo.io/age"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity() error = %v", err)
	}
	plaintext := []byte("auxly memory payload")

	ciphertext, err := Encrypt(plaintext, identity.Recipient())
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if !IsEncrypted(ciphertext) {
		t.Fatal("IsEncrypted() = false, want true")
	}

	got, err := Decrypt(ciphertext, identity)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt() = %q, want %q", got, plaintext)
	}
}

func TestDecryptWithWrongIdentityFailsClosed(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity() error = %v", err)
	}
	wrongIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity() error = %v", err)
	}

	ciphertext, err := Encrypt([]byte("sealed"), identity.Recipient())
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	if got, err := Decrypt(ciphertext, wrongIdentity); err == nil {
		t.Fatalf("Decrypt() with wrong identity succeeded with %q, want error", got)
	}
}

func TestIsEncryptedRejectsPlaintext(t *testing.T) {
	if IsEncrypted([]byte("age-encryption.org/not-v1\n")) {
		t.Fatal("IsEncrypted() = true for non-age header")
	}
}

func TestDecryptTamperedCiphertextFailsClosed(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity() error = %v", err)
	}

	ciphertext, err := Encrypt([]byte("auxly memory payload, long enough to tamper mid-body"), identity.Recipient())
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	tampered := bytes.Clone(ciphertext)
	mid := len(tampered) / 2
	tampered[mid] ^= 0xFF // flip a byte inside the ciphertext body, past the header

	if got, err := Decrypt(tampered, identity); err == nil {
		t.Fatalf("Decrypt() of tampered ciphertext succeeded with %q, want error", got)
	}
}
