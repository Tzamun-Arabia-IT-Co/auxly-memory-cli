package vaultcrypt

import (
	"bytes"
	"fmt"
	"io"

	"filippo.io/age"
)

var ageHeader = []byte("age-encryption.org/v1")

// Encrypt encrypts plaintext to the supplied age recipient using binary age format.
func Encrypt(plaintext []byte, recipient age.Recipient) ([]byte, error) {
	var out bytes.Buffer

	w, err := age.Encrypt(&out, recipient)
	if err != nil {
		return nil, fmt.Errorf("create age encrypt writer: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("write age plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close age encrypt writer: %w", err)
	}

	return out.Bytes(), nil
}

// Decrypt decrypts ciphertext with the supplied age identity.
func Decrypt(ciphertext []byte, identity age.Identity) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		// WHY: encrypted vault reads must fail closed when the key is missing or wrong.
		return nil, fmt.Errorf("open age ciphertext: %w", err)
	}

	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read age plaintext: %w", err)
	}
	return plaintext, nil
}

// IsEncrypted reports whether data starts with the binary age v1 header.
func IsEncrypted(data []byte) bool {
	return bytes.HasPrefix(data, ageHeader)
}
