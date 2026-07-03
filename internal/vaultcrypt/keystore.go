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
	envVaultKey        = "AUXLY_VAULT_KEY"
	envVaultPassphrase = "AUXLY_VAULT_PASSPHRASE"
	keychainAccount    = "auxly"
	keychainService    = "auxly-vault-key"
	privateKeyFile     = "vault.key"
	publicKeyFile      = "vault.pub"

	// Passphrase mode: the password itself is the secret, stored the same
	// way (keychain, else 0600 file) the private key is in keypair mode.
	keychainServicePass = "auxly-vault-pass"
	passphraseFile      = "vault.pass"
	verifierFile        = "vault.verify"
	modeFile            = "mode"

	modeKeypair    = "keypair"
	modePassphrase = "passphrase"

	// verifierPlaintext is a known constant encrypted to <keys>/vault.verify
	// under the passphrase at GeneratePassphrase time. It holds NO secret of
	// its own — decrypting it successfully is just proof the supplied
	// passphrase is the right one, so a wrong password fails with a clean
	// error (PassphraseOK) instead of a garbled decrypt failure surfacing
	// deep inside a real content file.
	verifierPlaintext = "auxly-verify\n"

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
		if err := storeKeychainFor(keychainService, privateKey); err != nil {
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
// in any of the places Generate OR GeneratePassphrase would write to - either
// mode's material counts, so switching modes on a vault that already has one
// requires deliberately clearing the old material first, not a silent
// clobber. Returns ErrKeychainUnavailable if the keychain can't be probed at
// all (locked/denied/timeout) - in which case we cannot know whether a key
// exists, so we refuse rather than guess.
func (k *Keystore) refuseIfKeyExists() error {
	if _, err := os.Stat(k.publicKeyPath()); err == nil {
		return ErrKeyExists
	}
	if _, err := os.Stat(k.privateKeyPath()); err == nil {
		return ErrKeyExists
	}
	if _, err := os.Stat(k.modePath()); err == nil {
		return ErrKeyExists
	}
	if _, err := os.Stat(k.passphrasePath()); err == nil {
		return ErrKeyExists
	}
	if k.useKeychain {
		key, err := findKeychainFor(keychainService)
		switch {
		case err == nil && strings.TrimSpace(key) != "":
			return ErrKeyExists
		case err == nil, errors.Is(err, errKeychainNotFound):
			// genuinely absent - fine to proceed
		default:
			return err
		}
		pass, err := findKeychainFor(keychainServicePass)
		switch {
		case err == nil && strings.TrimSpace(pass) != "":
			return ErrKeyExists
		case err == nil, errors.Is(err, errKeychainNotFound):
			// genuinely absent - fine to proceed
		default:
			return err
		}
	}
	return nil
}

// Identity returns the private age identity from env, keychain, or file
// storage. In passphrase mode this is a scrypt identity derived from the
// resolved passphrase; keypair mode is unchanged from before passphrase mode
// existed.
func (k *Keystore) Identity() (age.Identity, error) {
	if k.Mode() == modePassphrase {
		pass, err := k.resolvePassphrase()
		if err != nil {
			return nil, err
		}
		identity, err := age.NewScryptIdentity(pass)
		if err != nil {
			return nil, fmt.Errorf("create passphrase identity: %w", err)
		}
		return identity, nil
	}

	if key := strings.TrimSpace(os.Getenv(envVaultKey)); key != "" {
		return parseIdentity(key)
	}

	if k.useKeychain {
		key, err := findKeychainFor(keychainService)
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

// Recipient returns the public age recipient. In passphrase mode this is a
// scrypt recipient derived from the resolved passphrase. In keypair mode,
// AUXLY_VAULT_KEY, when set, is honored here too, deriving the recipient
// from the env identity: the env override swaps the whole active key, both
// directions, or encrypt (via Recipient) and decrypt (via Identity) can
// silently diverge onto different keys.
func (k *Keystore) Recipient() (age.Recipient, error) {
	if k.Mode() == modePassphrase {
		pass, err := k.resolvePassphrase()
		if err != nil {
			return nil, err
		}
		return newScryptRecipient(pass)
	}

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

// Exists reports whether a usable key (either mode) is available.
func (k *Keystore) Exists() bool {
	if k.Mode() == modePassphrase {
		_, err := k.passphraseSource()
		return err == nil
	}
	if _, err := os.Stat(k.publicKeyPath()); err == nil {
		return true
	}
	if strings.TrimSpace(os.Getenv(envVaultKey)) != "" {
		return true
	}
	if k.useKeychain {
		if key, err := findKeychainFor(keychainService); err == nil && strings.TrimSpace(key) != "" {
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

// Source reports where the active vault key would be resolved from: "env",
// "keychain", or "file" — the same precedence order as Identity(), but
// reporting only the origin, not the key material. Returns ErrNoKey if none
// of the three has key material, or a wrapped ErrKeychainUnavailable if the
// keychain can't be probed to tell (locked/denied/timeout).
func (k *Keystore) Source() (string, error) {
	if k.Mode() == modePassphrase {
		return k.passphraseSource()
	}
	if strings.TrimSpace(os.Getenv(envVaultKey)) != "" {
		return "env", nil
	}
	if k.useKeychain {
		key, err := findKeychainFor(keychainService)
		switch {
		case err == nil && strings.TrimSpace(key) != "":
			return "keychain", nil
		case err == nil, errors.Is(err, errKeychainNotFound):
			// genuinely absent - fall through to file
		default:
			return "", err
		}
	}
	if _, err := os.Stat(k.privateKeyPath()); err == nil {
		return "file", nil
	}
	return "", ErrNoKey
}

// Mode reports whether this keystore is in "passphrase" or "keypair" mode.
// State lives on disk (the mode marker file under keys/) rather than in any
// in-memory flag, so every process - a CLI invocation, the MCP server, the
// TUI - resolves the same way without needing to share state.
func (k *Keystore) Mode() string {
	data, err := os.ReadFile(k.modePath())
	if err != nil {
		return modeKeypair
	}
	if mode := strings.TrimSpace(string(data)); mode == modePassphrase {
		return modePassphrase
	}
	return modeKeypair
}

// passphraseSource resolves WHERE the active passphrase would come from -
// env, keychain, or file - mirroring Source()'s keypair walk. Used by both
// Source() and Exists() in passphrase mode.
func (k *Keystore) passphraseSource() (string, error) {
	if strings.TrimSpace(os.Getenv(envVaultPassphrase)) != "" {
		return "env", nil
	}
	if k.useKeychain {
		pass, err := findKeychainFor(keychainServicePass)
		switch {
		case err == nil && strings.TrimSpace(pass) != "":
			return "keychain", nil
		case err == nil, errors.Is(err, errKeychainNotFound):
			// genuinely absent - fall through to file
		default:
			return "", err
		}
	}
	if _, err := os.Stat(k.passphrasePath()); err == nil {
		return "file", nil
	}
	return "", ErrNoKey
}

// resolvePassphrase resolves the active passphrase's VALUE from env,
// keychain, or file storage, in that order - same precedence as
// passphraseSource, but returning the secret instead of its location.
func (k *Keystore) resolvePassphrase() (string, error) {
	if pass := strings.TrimSpace(os.Getenv(envVaultPassphrase)); pass != "" {
		return pass, nil
	}
	if k.useKeychain {
		pass, err := findKeychainFor(keychainServicePass)
		switch {
		case err == nil && strings.TrimSpace(pass) != "":
			return strings.TrimSpace(pass), nil
		case err == nil, errors.Is(err, errKeychainNotFound):
			// genuinely absent - fall through to file
		default:
			return "", err
		}
	}
	data, err := os.ReadFile(k.passphrasePath())
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoKey
	}
	return "", fmt.Errorf("read vault passphrase: %w", err)
}

// GeneratePassphrase switches this vault to passphrase mode: the password
// itself becomes the secret, stored the same way the private key is in
// keypair mode - macOS keychain, falling back to a 0600 file - so it's typed
// once at init and then remembered transparently on every later read.
// Refuses (ErrKeyExists) if ANY key material already exists, keypair or
// passphrase: same reasoning as Generate - clobbering strands whatever was
// encrypted under the old secret. There is deliberately no recovery key in
// this mode: forgetting the passphrase means the vault is gone, by design
// (that trade is the whole point - no 60-char backup key to lose track of).
func (k *Keystore) GeneratePassphrase(pass string) error {
	if err := k.refuseIfKeyExists(); err != nil {
		return err
	}
	if len(pass) == 0 {
		return errors.New("passphrase can't be empty")
	}

	if err := os.MkdirAll(k.keysDir(), 0o700); err != nil {
		return fmt.Errorf("create vault key directory: %w", err)
	}
	if err := os.Chmod(k.keysDir(), 0o700); err != nil {
		return fmt.Errorf("tighten vault key directory permissions: %w", err)
	}

	if err := k.storePassphraseSecret(pass); err != nil {
		return err
	}
	if err := k.writeVerifier(pass); err != nil {
		return err
	}
	// Mode is written LAST, only once the secret and its verifier are safely
	// on disk: a crash before this point leaves Mode() reporting "keypair"
	// (the default) with no keypair material present either, so a retry
	// just sees ErrNoKey and re-runs init cleanly rather than landing in a
	// half-switched state.
	if err := os.WriteFile(k.modePath(), []byte(modePassphrase+"\n"), 0o600); err != nil {
		return fmt.Errorf("write vault mode marker: %w", err)
	}
	return nil
}

// storePassphraseSecret stores pass exactly like Generate stores the private
// key: keychain first (when available), file only as a fallback.
func (k *Keystore) storePassphraseSecret(pass string) error {
	if k.useKeychain {
		if err := storeKeychainFor(keychainServicePass, pass); err != nil {
			if ferr := k.writePassphraseFile(pass); ferr != nil {
				return fmt.Errorf("store keychain failed and file fallback failed: %w", ferr)
			}
		}
		return nil
	}
	return k.writePassphraseFile(pass)
}

// writeVerifier encrypts a known constant to <keys>/vault.verify under pass,
// so PassphraseOK can catch a wrong password with a clean error instead of a
// garbled decrypt failure surfacing deep inside real content. The verifier
// file holds only the encrypted constant - never the password itself.
func (k *Keystore) writeVerifier(pass string) error {
	recipient, err := newScryptRecipient(pass)
	if err != nil {
		return err
	}
	ciphertext, err := Encrypt([]byte(verifierPlaintext), recipient)
	if err != nil {
		return fmt.Errorf("create vault passphrase verifier: %w", err)
	}
	if err := os.WriteFile(k.verifierPath(), ciphertext, 0o600); err != nil {
		return fmt.Errorf("write vault passphrase verifier: %w", err)
	}
	return nil
}

// PassphraseOK reports whether pass is the vault's current passphrase, by
// attempting to decrypt the verifier written by GeneratePassphrase. It never
// touches real vault content, so a wrong guess costs nothing beyond the
// scrypt work factor.
func (k *Keystore) PassphraseOK(pass string) (bool, error) {
	raw, err := os.ReadFile(k.verifierPath())
	if err != nil {
		return false, fmt.Errorf("read vault passphrase verifier: %w", err)
	}
	identity, err := age.NewScryptIdentity(pass)
	if err != nil {
		return false, fmt.Errorf("create passphrase identity: %w", err)
	}
	plain, err := Decrypt(raw, identity)
	if err != nil {
		return false, nil // wrong passphrase - not a plumbing error
	}
	return string(plain) == verifierPlaintext, nil
}

// newScryptRecipient wraps age.NewScryptRecipient. age's default work factor
// (2^18, ~1s on a modern machine, per the age package's own comment) is
// already the sane interactive value this CLI wants: every encrypt AND every
// decrypt (including PassphraseOK's verifier check) pays it once per
// invocation, so raising it via SetWorkFactor would trade UX for marginal
// brute-force resistance this CLI's threat model doesn't call for.
func newScryptRecipient(pass string) (*age.ScryptRecipient, error) {
	r, err := age.NewScryptRecipient(pass)
	if err != nil {
		return nil, fmt.Errorf("create passphrase recipient: %w", err)
	}
	return r, nil
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

func (k *Keystore) passphrasePath() string {
	return filepath.Join(k.keysDir(), passphraseFile)
}

func (k *Keystore) verifierPath() string {
	return filepath.Join(k.keysDir(), verifierFile)
}

func (k *Keystore) modePath() string {
	return filepath.Join(k.keysDir(), modeFile)
}

// writePrivateKey writes key to <keys>/vault.key atomically at 0600.
func (k *Keystore) writePrivateKey(key string) error {
	return writeSecretFile(k.keysDir(), k.privateKeyPath(), key)
}

// writePassphraseFile writes pass to <keys>/vault.pass atomically at 0600 -
// the passphrase-mode file-fallback counterpart to writePrivateKey.
func (k *Keystore) writePassphraseFile(pass string) error {
	return writeSecretFile(k.keysDir(), k.passphrasePath(), pass)
}

// writeSecretFile atomically writes content+"\n" to target inside dir at
// mode 0600: temp file, explicit chmod, then rename over the target.
// os.WriteFile alone leaves a pre-existing file's mode untouched, so a loose
// 0644 secret file would otherwise survive a rewrite; the temp+chmod+rename
// dance mirrors internal/memory/atomicwrite.go's pattern (kept local -
// vaultcrypt does not import internal/memory). Shared by writePrivateKey and
// writePassphraseFile - same secret-file shape, different content.
func writeSecretFile(dir, target, content string) error {
	tmp, err := os.CreateTemp(dir, ".vault-secret-*")
	if err != nil {
		return fmt.Errorf("create temp vault secret file: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(e error) error {
		tmp.Close()
		os.Remove(tmpName)
		return e
	}

	if _, err := tmp.WriteString(content + "\n"); err != nil {
		return fail(fmt.Errorf("write vault secret file: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("sync vault secret file: %w", err))
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("chmod vault secret file: %w", err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close vault secret file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename vault secret file into place: %w", err)
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
	return buildStoreKeychainCmdFor(ctx, keychainService, key)
}

// buildStoreKeychainCmdFor is buildStoreKeychainCmd generalized over which
// keychain service the entry is stored under - keychainService for the
// keypair's private key, keychainServicePass for a passphrase-mode secret.
// Same stdin-not-argv reasoning applies to both.
func buildStoreKeychainCmdFor(ctx context.Context, service, key string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(
		"add-generic-password -a %s -s %s -w %s -U\n",
		keychainAccount, service, key,
	))
	return cmd
}

func storeKeychainFor(service, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()

	cmd := buildStoreKeychainCmdFor(ctx, service, key)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("store vault secret in keychain: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func findKeychainFor(service string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-a", keychainAccount,
		"-s", service,
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
