package vaultcrypt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"filippo.io/age"
)

const (
	envVaultKey     = "AUXLY_VAULT_KEY"
	keychainAccount = "auxly"
	keychainService = "auxly-vault-key"
	privateKeyFile  = "vault.key"
	publicKeyFile   = "vault.pub"

	// keychainTimeout bounds every `security` invocation. Run headlessly (SSH,
	// CI, launchd), a locked keychain makes `security` hang on a GUI unlock
	// prompt that will never be answered - without a deadline that hang is
	// forever.
	//
	// NEVER call storeKeychain/findKeychain while holding the vault lock: a
	// slow/hung keychain prompt would then hold the lock for up to this long
	// and block every other process touching the vault.
	keychainTimeout = 10 * time.Second
)

// ErrNoKey is returned when no vault key exists in env, keychain, or file storage.
var ErrNoKey = errors.New("vault key not found")

// ErrKeyExists is returned by Generate when key material already exists -
// in the keychain, the private key file, or the public recipient file.
// Generate refuses rather than overwrite: a clobbered key permanently
// strands every ciphertext encrypted under the old one. Back the existing
// key up with ExportKey first; key rotation (retiring an old key and
// re-encrypting under a new one) is a designed future feature, not
// something Generate does by accident.
var ErrKeyExists = errors.New("vault key already exists: back it up with ExportKey before replacing it (rotation is not supported by Generate yet)")

// ErrKeychainUnavailable is returned when the macOS keychain could not be
// queried - locked, access denied, or timed out. This is deliberately NOT
// ErrNoKey: treating a locked keychain as "no key" would make callers
// regenerate a new one while the old key (and everything encrypted under
// it) is still sitting there, unreadable, in the keychain.
var ErrKeychainUnavailable = errors.New("vault keychain unavailable (locked, access denied, or timed out)")

// errKeychainNotFound is findKeychain's internal "genuinely absent" signal.
// It is the only keychain-probe outcome that may fall through to file-based
// storage/lookup; every other error must propagate as ErrKeychainUnavailable.
var errKeychainNotFound = errors.New("keychain entry not found")

// Keystore manages the vault-scoped age identity.
type Keystore struct {
	dir         string
	useKeychain bool
}

// NewKeystore returns a keystore rooted at auxlyDir.
func NewKeystore(auxlyDir string) *Keystore {
	return &Keystore{
		dir:         auxlyDir,
		useKeychain: runtime.GOOS == "darwin",
	}
}

// Generate creates and stores a new X25519 identity and public recipient.
// It refuses (ErrKeyExists) if key material already exists anywhere -
// overwriting would permanently strand every ciphertext encrypted under the
// old key. There is no force flag: rotation is a deliberate future
// operation, not a mistake one flag away.
func (k *Keystore) Generate() error {
	if err := k.refuseIfKeyExists(); err != nil {
		return err
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return fmt.Errorf("generate age identity: %w", err)
	}

	if err := os.MkdirAll(k.keysDir(), 0o700); err != nil {
		return fmt.Errorf("create vault key directory: %w", err)
	}
	// MkdirAll does not chmod a directory that already existed; tighten it
	// explicitly so a pre-existing loose keys/ never survives Generate.
	if err := os.Chmod(k.keysDir(), 0o700); err != nil {
		return fmt.Errorf("tighten vault key directory permissions: %w", err)
	}

	privateKey := identity.String()
	if k.useKeychain {
		// WHY: macOS uses the security CLI to keep v1 zero-CGO with no keyring dependency.
		// refuseIfKeyExists just confirmed the keychain probe says not-found,
		// so a store failure here is safe to fall back to file: we know this
		// Generate call is not shadowing an existing keychain entry.
		if err := storeKeychain(privateKey); err != nil {
			if err := k.writePrivateKey(privateKey); err != nil {
				return fmt.Errorf("store keychain failed and file fallback failed: %w", err)
			}
		}
	} else if err := k.writePrivateKey(privateKey); err != nil {
		return err
	}

	if err := os.WriteFile(k.publicKeyPath(), []byte(identity.Recipient().String()+"\n"), 0o644); err != nil {
		return fmt.Errorf("write vault public recipient: %w", err)
	}

	return nil
}

// refuseIfKeyExists reports ErrKeyExists if key material is already present
// in any of the places Generate would write to, and ErrKeychainUnavailable
// if the keychain can't be probed at all (locked/denied/timeout) - in which
// case we cannot know whether a key exists, so we refuse rather than guess.
func (k *Keystore) refuseIfKeyExists() error {
	if _, err := os.Stat(k.publicKeyPath()); err == nil {
		return ErrKeyExists
	}
	if _, err := os.Stat(k.privateKeyPath()); err == nil {
		return ErrKeyExists
	}
	if k.useKeychain {
		key, err := findKeychain()
		switch {
		case err == nil && strings.TrimSpace(key) != "":
			return ErrKeyExists
		case err == nil, errors.Is(err, errKeychainNotFound):
			// genuinely absent - fine to proceed
		default:
			return err
		}
	}
	return nil
}

// Identity returns the private age identity from env, keychain, or file storage.
func (k *Keystore) Identity() (age.Identity, error) {
	if key := strings.TrimSpace(os.Getenv(envVaultKey)); key != "" {
		return parseIdentity(key)
	}

	if k.useKeychain {
		key, err := findKeychain()
		switch {
		case err == nil && strings.TrimSpace(key) != "":
			return parseIdentity(key)
		case err == nil, errors.Is(err, errKeychainNotFound):
			// genuinely absent - fall through to file storage
		default:
			// Locked/denied/timeout is NOT "no key": falling through here
			// would let a caller treat ErrNoKey as license to regenerate
			// over a key that still exists in the keychain.
			return nil, err
		}
	}

	key, err := os.ReadFile(k.privateKeyPath())
	if err == nil {
		return parseIdentity(string(key))
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKey
	}
	return nil, fmt.Errorf("read vault private key: %w", err)
}

// Recipient returns the public age recipient. AUXLY_VAULT_KEY, when set, is
// honored here too, deriving the recipient from the env identity: the env
// override swaps the whole active key, both directions, or encrypt (via
// Recipient) and decrypt (via Identity) can silently diverge onto different
// keys.
func (k *Keystore) Recipient() (age.Recipient, error) {
	if key := strings.TrimSpace(os.Getenv(envVaultKey)); key != "" {
		identity, err := parseIdentity(key)
		if err != nil {
			return nil, err
		}
		return identity.Recipient(), nil
	}

	key, err := os.ReadFile(k.publicKeyPath())
	if err != nil {
		return nil, fmt.Errorf("read vault public recipient: %w", err)
	}

	recipient, err := age.ParseX25519Recipient(strings.TrimSpace(string(key)))
	if err != nil {
		return nil, fmt.Errorf("parse vault public recipient: %w", err)
	}
	return recipient, nil
}

// Exists reports whether the public recipient or a private identity is available.
func (k *Keystore) Exists() bool {
	if _, err := os.Stat(k.publicKeyPath()); err == nil {
		return true
	}
	if strings.TrimSpace(os.Getenv(envVaultKey)) != "" {
		return true
	}
	if k.useKeychain {
		if key, err := findKeychain(); err == nil && strings.TrimSpace(key) != "" {
			return true
		}
	}
	if _, err := os.Stat(k.privateKeyPath()); err == nil {
		return true
	}
	return false
}

// ExportKey returns the private identity string for user backup.
func (k *Keystore) ExportKey() (string, error) {
	// Caller must warn that this is the raw decrypt key and exposes the vault.
	identity, err := k.Identity()
	if err != nil {
		return "", err
	}

	x25519, ok := identity.(*age.X25519Identity)
	if !ok {
		return "", fmt.Errorf("vault identity is not X25519")
	}
	return x25519.String(), nil
}

func (k *Keystore) keysDir() string {
	// WHY: keys live outside the memory vault so git sync never ships the key next to ciphertext.
	return filepath.Join(k.dir, "keys")
}

func (k *Keystore) privateKeyPath() string {
	return filepath.Join(k.keysDir(), privateKeyFile)
}

func (k *Keystore) publicKeyPath() string {
	return filepath.Join(k.keysDir(), publicKeyFile)
}

// writePrivateKey writes the key atomically: temp file in the same
// directory, explicit chmod 0600, then rename over the target. os.WriteFile
// alone leaves a pre-existing file's mode untouched, so a loose 0644
// vault.key would otherwise survive a rewrite; the temp+chmod+rename dance
// mirrors internal/memory/atomicwrite.go's pattern (kept local - vaultcrypt
// does not import internal/memory).
func (k *Keystore) writePrivateKey(key string) error {
	dir := k.keysDir()
	tmp, err := os.CreateTemp(dir, ".vault-key-*")
	if err != nil {
		return fmt.Errorf("create temp vault private key: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(e error) error {
		tmp.Close()
		os.Remove(tmpName)
		return e
	}

	if _, err := tmp.WriteString(key + "\n"); err != nil {
		return fail(fmt.Errorf("write vault private key: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("sync vault private key: %w", err))
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("chmod vault private key: %w", err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close vault private key: %w", err)
	}
	if err := os.Rename(tmpName, k.privateKeyPath()); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename vault private key into place: %w", err)
	}
	return nil
}

func parseIdentity(key string) (*age.X25519Identity, error) {
	identity, err := age.ParseX25519Identity(strings.TrimSpace(key))
	if err != nil {
		return nil, fmt.Errorf("parse vault private key: %w", err)
	}
	return identity, nil
}

// buildStoreKeychainCmd constructs the `security -i` command that stores key
// in the login keychain. Interactive mode (-i) reads commands from stdin
// instead of argv, so the private key never appears in the process's
// argument list - visible to any local user via `ps`, and to shell/audit
// history if this were ever invoked from a shell. Verified against a
// throwaway keychain entry (Args do not contain the key; see
// TestBuildStoreKeychainCmdKeyNotInArgs).
func buildStoreKeychainCmd(ctx context.Context, key string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(
		"add-generic-password -a %s -s %s -w %s -U\n",
		keychainAccount, keychainService, key,
	))
	return cmd
}

func storeKeychain(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()

	cmd := buildStoreKeychainCmd(ctx, key)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("store vault private key in keychain: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func findKeychain() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-a", keychainAccount,
		"-s", keychainService,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: security command timed out", ErrKeychainUnavailable)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", classifyKeychainErr(exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("%w: %v", ErrKeychainUnavailable, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// classifyKeychainErr turns a `security` exit code + stderr into either
// errKeychainNotFound (genuinely absent - safe to fall through to file) or a
// wrapped ErrKeychainUnavailable (locked/denied/timeout - must NOT be
// treated as "no key"). Exit 44 / "could not be found" in stderr is the
// documented not-found signal for find-generic-password; everything else is
// treated as unavailable rather than guessed at.
func classifyKeychainErr(exitCode int, stderr string) error {
	if exitCode == 44 || strings.Contains(stderr, "could not be found") {
		return errKeychainNotFound
	}
	return fmt.Errorf("%w (exit %d): %s", ErrKeychainUnavailable, exitCode, strings.TrimSpace(stderr))
}
