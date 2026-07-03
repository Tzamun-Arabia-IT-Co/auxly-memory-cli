package memory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/safepath"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// cryptio.go is the ONLY place vault crypto meets file IO. Every read of a
// vault CONTENT file (a memory .md — not metadata like the review-seen
// ledger or audit.db, which stay plaintext) goes through readVaultFile;
// every write goes through writeVaultFile. Keeping the boundary in one file
// makes "can this leak plaintext to disk" a single-file audit.
//
// ponytail — v1 limits, by design, not solved here:
//   - encryption-at-rest is a GLOBAL-vault feature: workspace shadow copies
//     (Store.WorkspaceRoot) are always plaintext. Extend readVaultFile/
//     writeVaultFile's callers to the workspace path if that's ever needed.
//   - organize/contradiction/recall LLM flows still see decrypted content —
//     same as any human recall would (see gatherOrganizeFiles).
//   - pending diffs of an encrypted target hold plaintext lines in
//     .pending/ (0600, transient, local) until approved/rejected (see
//     pending.Manager.WriteFrom).

// keystore returns a fresh handle to the vault key store. The global vault
// root is ~/.auxly/memory, so its parent (~/.auxly) is where vaultcrypt
// keeps keys/ — deliberately OUTSIDE the vault directory so a git-synced
// vault never ships the key next to the ciphertext it opens.
func (s *Store) keystore() *vaultcrypt.Keystore {
	return vaultcrypt.NewKeystore(filepath.Dir(s.Root))
}

// vaultIdentity resolves and caches the private decrypt identity on the
// Store. Resolution can exec `security` on macOS with a 10s timeout (a
// locked/headless keychain) — that exec ALWAYS happens outside cryptoMu, so
// a slow/hung probe only blocks the caller that triggered it, never another
// in-process caller waiting on the cache. Callers must never invoke this
// while already holding cryptoMu.
func (s *Store) vaultIdentity() (age.Identity, error) {
	s.cryptoMu.Lock()
	cached := s.cryptoIdentity
	s.cryptoMu.Unlock()
	if cached != nil {
		return cached, nil
	}

	identity, err := s.keystore().Identity()
	if err != nil {
		return nil, err
	}

	s.cryptoMu.Lock()
	s.cryptoIdentity = identity
	s.cryptoMu.Unlock()
	return identity, nil
}

// vaultRecipient resolves and caches the public encrypt recipient. Same
// exec/locking shape as vaultIdentity.
func (s *Store) vaultRecipient() (age.Recipient, error) {
	s.cryptoMu.Lock()
	cached := s.cryptoRecipient
	s.cryptoMu.Unlock()
	if cached != nil {
		return cached, nil
	}

	recipient, err := s.keystore().Recipient()
	if err != nil {
		return nil, err
	}

	s.cryptoMu.Lock()
	s.cryptoRecipient = recipient
	s.cryptoMu.Unlock()
	return recipient, nil
}

// readVaultFile reads path exactly like os.ReadFile (including returning an
// os.IsNotExist error for a missing file unchanged), transparently
// decrypting it first when the on-disk bytes carry the age v1 header.
// encrypted reports whether the file was encrypted on disk, so callers that
// write a DERIVED copy (decay's archive line, pending-approve's merged
// content) know to re-encrypt it too.
//
// Fails CLOSED: a missing/unreachable key on an encrypted file is always an
// error — this never falls back to returning raw ciphertext as content.
func (s *Store) readVaultFile(path string) (data []byte, encrypted bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if !vaultcrypt.IsEncrypted(raw) {
		return raw, false, nil
	}

	identity, err := s.vaultIdentity()
	if err != nil {
		return nil, true, fmt.Errorf("%s is encrypted and the vault key is not reachable — run `auxly encrypt status`: %w", path, err)
	}
	plain, err := vaultcrypt.Decrypt(raw, identity)
	if err != nil {
		return nil, true, fmt.Errorf("decrypt %s — run `auxly encrypt status`: %w", path, err)
	}
	return plain, true, nil
}

// writeVaultFile writes data to path, encrypting first when encrypt is true.
// Encryption happens BEFORE AtomicWriteFile so the temp file that briefly
// exists mid-write can never hold plaintext of an encrypted target.
func (s *Store) writeVaultFile(path string, data []byte, perm os.FileMode, encrypt bool) error {
	if !encrypt {
		return AtomicWriteFile(path, data, perm)
	}

	recipient, err := s.vaultRecipient()
	if err != nil {
		return fmt.Errorf("encrypt %s — vault key not reachable, run `auxly encrypt status`: %w", path, err)
	}
	ciphertext, err := vaultcrypt.Encrypt(data, recipient)
	if err != nil {
		return fmt.Errorf("encrypt %s: %w", path, err)
	}
	return AtomicWriteFile(path, ciphertext, perm)
}

// shouldStayEncrypted reports whether the file CURRENTLY on disk at path is
// age-encrypted. Encryption state lives in the file itself (the age
// header), never in config: a write that doesn't otherwise know better
// preserves whatever the file already is, and a brand-new file (nothing on
// disk yet) defaults to plaintext.
func (s *Store) shouldStayEncrypted(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return vaultcrypt.IsEncrypted(raw)
}

// ReadVaultFile and WriteVaultFile are the exported forms of the above, for
// the pending package: Manager applies queued diffs directly against a
// target file's on-disk bytes outside Store's normal View/Write path, and
// (unlike every in-package caller) is not itself a *Store.
func (s *Store) ReadVaultFile(path string) ([]byte, bool, error) {
	return s.readVaultFile(path)
}

func (s *Store) WriteVaultFile(path string, data []byte, perm os.FileMode, encrypt bool) error {
	return s.writeVaultFile(path, data, perm, encrypt)
}

// ReadRawVaultBytes returns filename's exact on-disk bytes (workspace
// override if one exists, else the global root) WITHOUT decrypting —
// for callers that must preserve ciphertext verbatim (a backup of an
// encrypted file must never become a plaintext shadow copy). encrypted
// reports whether those bytes carry the age header; a workspace override is
// always reported as plaintext (encryption-at-rest is a global-vault
// feature in v1).
func (s *Store) ReadRawVaultBytes(filename string) (data []byte, encrypted bool, err error) {
	if s.WorkspaceRoot != "" {
		if localPath, perr := safepath.ResolveSafe(s.WorkspaceRoot, filename); perr == nil {
			if _, statErr := os.Stat(localPath); statErr == nil {
				data, err = os.ReadFile(localPath)
				return data, false, err
			}
		}
	}
	path, err := s.resolvePath(filename)
	if err != nil {
		return nil, false, err
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return data, vaultcrypt.IsEncrypted(data), nil
}

// fileIsEncrypted reports whether the copy of filename Store.View would
// return is age-encrypted, WITHOUT decrypting it or requiring a reachable
// key — used by callers (unified compile, export, the semantic index) that
// must SKIP an encrypted file rather than embed/roll up its decrypted
// content (advisory: those derived artifacts would otherwise become a
// plaintext shadow copy of an encrypted file). Any read error is reported
// as "not encrypted" — the caller's subsequent View()/read then fails
// closed on its own if that was wrong, so this never leaks plaintext.
func (s *Store) fileIsEncrypted(filename string) bool {
	_, encrypted, err := s.ReadRawVaultBytes(filename)
	return err == nil && encrypted
}

// EncryptFile migrates filename from plaintext to encrypted-at-rest: read
// (must not already be encrypted) → encrypt → decrypt-verify the ciphertext
// against the plaintext just read → write ONLY if the verify matches
// exactly. On any mismatch it aborts with an error and the original file
// untouched — a scrambled key or a vault-crypt bug must never brick data.
//
// No plaintext backup is written: keeping one around would defeat the point
// of encrypting the file. unified_memory.md is rejected — it is a compiled
// artifact, not a source of truth.
//
// MAJOR 6/7 (TOCTOU + keychain-under-lock): identity/recipient resolution
// happens FIRST, entirely OUTSIDE the vault lock — it can exec `security` on
// macOS (10s timeout on a locked/headless keychain), and holding LockVault
// across that exec would starve every other vault writer for the duration.
// The read of the CURRENT content then happens INSIDE the lock, right before
// the write — reading it earlier (unlocked) would let this write silently
// revert a concurrent writer's fact with ciphertext computed from a stale
// snapshot.
func (s *Store) EncryptFile(filename string) error {
	if filename == unifiedMemoryFile {
		return errors.New("unified_memory.md is a compiled artifact — encrypt its sources instead")
	}

	path, err := s.resolvePath(filename)
	if err != nil {
		return err
	}

	recipient, err := s.vaultRecipient()
	if err != nil {
		return fmt.Errorf("vault key not reachable — run `auxly encrypt init` first: %w", err)
	}
	identity, err := s.vaultIdentity()
	if err != nil {
		return fmt.Errorf("verify %s: vault key not reachable: %w", filename, err)
	}

	unlock, err := LockVault(s.Root)
	if err != nil {
		return err
	}
	defer unlock()

	plain, encrypted, err := s.readVaultFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filename, err)
	}
	if encrypted {
		return fmt.Errorf("%s is already encrypted", filename)
	}

	ciphertext, err := vaultcrypt.Encrypt(plain, recipient)
	if err != nil {
		return fmt.Errorf("encrypt %s: %w", filename, err)
	}

	// Verify against the bytes we're about to trust, BEFORE the write is
	// accepted — never against a re-read of the source (which could itself
	// have changed underneath us).
	roundTrip, err := vaultcrypt.Decrypt(ciphertext, identity)
	if err != nil || !bytes.Equal(roundTrip, plain) {
		return fmt.Errorf("encrypt-verify mismatch for %s — aborted, original file untouched", filename)
	}

	if err := AtomicWriteFile(path, ciphertext, 0o644); err != nil {
		return err
	}
	// CRITICAL 4: the plaintext chunk text this file was embedded under (if
	// any) is now stale — a fact that just became encrypted-at-rest must not
	// keep living as plaintext rows in .index/embeddings.db until some future
	// recall happens to touch this file again.
	s.pruneIndexedFile(path)
	return nil
}

// DecryptFile removes encryption-at-rest from filename, writing it back as
// plaintext. This is the escape hatch behind `auxly decrypt file`; callers
// are responsible for confirming with the user first — this function itself
// does not prompt.
//
// Same TOCTOU shape as EncryptFile: identity resolution happens BEFORE the
// lock, the actual read happens INSIDE it.
func (s *Store) DecryptFile(filename string) error {
	path, err := s.resolvePath(filename)
	if err != nil {
		return err
	}

	if _, err := s.vaultIdentity(); err != nil {
		return fmt.Errorf("read %s: vault key not reachable: %w", filename, err)
	}

	unlock, err := LockVault(s.Root)
	if err != nil {
		return err
	}
	defer unlock()

	plain, encrypted, err := s.readVaultFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filename, err)
	}
	if !encrypted {
		return fmt.Errorf("%s is not encrypted", filename)
	}

	return AtomicWriteFile(path, plain, 0o644)
}

// pruneIndexedFile removes path's chunks from the semantic index, if any.
// Best-effort and silent: an absent or unopenable index is a no-op — the
// encrypt itself must never fail because of the index sidecar. Reuses the
// exact prune refreshFile does for an already-encrypted file (see recall.go),
// then VACUUMs so the deleted row's plaintext bytes are actually scrubbed
// from the file (a DELETE alone only frees the page, it doesn't erase it) —
// otherwise the leak this prune exists to close would just move from "an
// indexed row" to "an unreachable-but-present page" in the same file.
func (s *Store) pruneIndexedFile(path string) {
	dbPath := s.indexDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return
	}
	ix, err := OpenIndexReadOnly(dbPath)
	if err != nil {
		return
	}
	defer ix.Close()
	if err := ix.PruneExcept(path, map[string]bool{}); err != nil {
		return
	}
	_ = ix.Vacuum()
}

// EncryptedFileCount reports how many vault files are currently encrypted at
// rest — a cheap on-disk header sniff (via fileIsEncrypted), no key required.
// Used both to skip PrewarmCrypto's resolution when nothing would benefit
// from it, and to tell callers (mcp toolRecall, the TUI) when semantic
// recall or composition silently excluded some files.
func (s *Store) EncryptedFileCount() int {
	files, err := s.List()
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range files {
		if s.fileIsEncrypted(f.Name) {
			n++
		}
	}
	return n
}

// PrewarmCrypto resolves and caches the vault identity/recipient BEFORE a
// caller takes LockVault (MAJOR 7): resolution can exec `security` on macOS
// (10s timeout on a locked/headless keychain), and doing that while holding
// the vault lock would starve every other vault writer for the duration.
// No-op when already cached, or when the vault currently has no encrypted
// files (nothing to gain from probing). Errors are swallowed — this is a
// best-effort warm, not a gate; the caller's actual read/write still fails
// closed on its own if the key truly isn't reachable.
func (s *Store) PrewarmCrypto() {
	s.cryptoMu.Lock()
	cached := s.cryptoIdentity != nil && s.cryptoRecipient != nil
	s.cryptoMu.Unlock()
	if cached || s.EncryptedFileCount() == 0 {
		return
	}
	_, _ = s.vaultIdentity()
	_, _ = s.vaultRecipient()
}
