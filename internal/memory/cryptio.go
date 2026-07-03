package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

// --- organize's "decrypt temporarily" escape hatch ---------------------
//
// Organize via a CLI agent puts the vault content on the spawned process's
// argv (see organize.go's planOrganize). A user who explicitly consents can
// have Auxly decrypt the affected files for the duration of one run instead
// of refusing outright — TempDecryptForOrganize/ReencryptPending are that
// path's crash-safe plumbing.

// reencryptSentinelPath is the crash-recovery marker for an in-flight
// TempDecryptForOrganize: while it exists, the listed files are expected to
// be plaintext on disk and NEED re-encrypting. Living under .index/ mirrors
// the embeddings.db sidecar (see indexDBPath) — vault-internal bookkeeping,
// never a memory content file.
func (s *Store) reencryptSentinelPath() string {
	return filepath.Join(s.Root, ".index", "reencrypt-pending.json")
}

type reencryptSentinel struct {
	Files []string `json:"files"`
}

// readSentinelSet reads the current sentinel as a set. A missing file is an
// empty set (not an error); a corrupt one IS an error — callers must not
// silently treat corrupt bookkeeping as "nothing pending".
func (s *Store) readSentinelSet() (map[string]bool, error) {
	data, err := os.ReadFile(s.reencryptSentinelPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var sentinel reencryptSentinel
	if err := json.Unmarshal(data, &sentinel); err != nil {
		return nil, fmt.Errorf("corrupt reencrypt sentinel: %w", err)
	}
	set := make(map[string]bool, len(sentinel.Files))
	for _, f := range sentinel.Files {
		set[f] = true
	}
	return set, nil
}

// writeSentinelSetLocked persists set, or removes the sentinel file entirely
// once it's empty. Caller must hold LockVault.
func (s *Store) writeSentinelSetLocked(set map[string]bool) error {
	if len(set) == 0 {
		err := os.Remove(s.reencryptSentinelPath())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	names := make([]string, 0, len(set))
	for f := range set {
		names = append(names, f)
	}
	sort.Strings(names) // deterministic on-disk content
	data, err := json.Marshal(reencryptSentinel{Files: names})
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.reencryptSentinelPath(), data, 0o600)
}

// writeReencryptSentinel MERGES files into whatever the sentinel already
// lists, under LockVault.
//
// CRITICAL 2: writing the sentinel used to be an unlocked last-write-wins
// overwrite — two overlapping TempDecryptForOrganize calls (two organize
// runs, or an organize run racing a doctor heal) would stomp each other's
// file list, so a process killed after the SECOND write left the FIRST
// run's files plaintext with no crash-recovery record at all. Locking the
// read-modify-write and treating the sentinel as a set (never a raw
// overwrite) means every in-flight run's files stay listed until THAT run's
// own restore()/heal removes exactly its own entries.
func (s *Store) writeReencryptSentinel(files []string) error {
	unlock, err := LockVault(s.Root)
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.readSentinelSet()
	if err != nil {
		return err
	}
	for _, f := range files {
		set[f] = true
	}
	return s.writeSentinelSetLocked(set)
}

// removeFromSentinel removes exactly `files` from the shared sentinel set
// (never a blind overwrite/delete — see writeReencryptSentinel), deleting the
// sentinel file only once the set becomes empty. Locked, so it can't race a
// concurrent add/remove.
func (s *Store) removeFromSentinel(files []string) error {
	if len(files) == 0 {
		return nil
	}
	unlock, err := LockVault(s.Root)
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.readSentinelSet()
	if err != nil {
		return err
	}
	for _, f := range files {
		delete(set, f)
	}
	return s.writeSentinelSetLocked(set)
}

// healAndClearSentinel re-encrypts every currently-plaintext name in names
// and removes ONLY the names that resolved (re-encrypted, already-encrypted,
// or deleted meanwhile) from the shared crash-recovery sentinel. A name that
// still fails to re-encrypt stays listed so the next heal (doctor, TUI-open,
// or another restore) retries it. Shared by TempDecryptForOrganize's
// rollback/restore AND ReencryptPending, so there is exactly one place that
// mutates the sentinel after resolving files (CRITICAL 2b/2c).
func (s *Store) healAndClearSentinel(names []string) (healed []string, errs []error) {
	var resolved []string
	for _, name := range names {
		ok, err := s.reencryptIfPlaintext(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		resolved = append(resolved, name)
		if ok {
			healed = append(healed, name)
		}
	}
	if err := s.removeFromSentinel(resolved); err != nil {
		errs = append(errs, fmt.Errorf("update reencrypt sentinel: %w", err))
	}
	return healed, errs
}

// reencryptIfPlaintext re-encrypts name IF it currently exists on disk AND is
// plaintext; a missing file (deleted meanwhile) or an already-encrypted one
// (e.g. a second heal attempt) is a no-op, never an error — both are fine
// end states, not failures to report.
func (s *Store) reencryptIfPlaintext(name string) (healed bool, err error) {
	path, err := s.resolvePath(name)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if vaultcrypt.IsEncrypted(raw) {
		return false, nil
	}
	if err := s.EncryptFile(name); err != nil {
		return false, err
	}
	return true, nil
}

// TempDecryptForOrganize decrypts each of files IN PLACE on disk so a CLI
// agent's organize run can read them as plaintext for one run. The caller
// has already obtained explicit user consent (the TUI/CLI choice) —
// Store.DecryptFile itself never prompts, so this just drives it per file.
//
// SAFETY-CRITICAL: before decrypting ANYTHING, a crash-recovery sentinel is
// written listing every file about to be touched. If the process dies
// mid-run (kill -9, power loss — the caller's deferred restore() never
// runs), that sentinel survives on disk and ReencryptPending — called from
// `auxly doctor` and on every TUI organize-tab open — heals it later.
// Without this, an interrupted run could leave a file like personal.md
// silently plaintext forever.
//
// Callers MUST defer the returned restore() across every exit path. restore
// re-encrypts every file and clears the sentinel ONLY once all of them
// succeed; on partial failure the sentinel is left in place (so the next
// ReencryptPending retries) and the failing files are named in the error.
func (s *Store) TempDecryptForOrganize(files []string) (restore func() error, err error) {
	if len(files) == 0 {
		return func() error { return nil }, nil
	}
	if err := s.writeReencryptSentinel(files); err != nil {
		return nil, fmt.Errorf("write crash-recovery sentinel: %w", err)
	}
	for i, f := range files {
		if err := s.DecryptFile(f); err != nil {
			// Roll back whatever we already decrypted before surfacing the
			// error — restore() never runs (the caller's defer only starts
			// once this function returns successfully), so any file this
			// loop already touched must be put back itself.
			// CRITICAL 2: healAndClearSentinel clears ONLY the entries it
			// resolved (files[:i]) from the shared sentinel — a concurrent
			// TempDecryptForOrganize's own files, if any, are untouched.
			_, _ = s.healAndClearSentinel(files[:i])
			return nil, fmt.Errorf("decrypt %s for organize: %w", f, err)
		}
	}
	return func() error {
		_, errs := s.healAndClearSentinel(files)
		if len(errs) == 0 {
			return nil
		}
		return fmt.Errorf("re-encrypt after organize (run `auxly encrypt file <name>` on each): %w", errors.Join(errs...))
	}, nil
}

// ReencryptPending reads the crash-recovery sentinel (if any) and
// re-encrypts every listed file that is still plaintext on disk — healing a
// TempDecryptForOrganize run that was interrupted before its restore() could
// run. Tolerates files that are already encrypted (e.g. a previous partial
// heal) or no longer exist (deleted meanwhile). Returns the names it
// actually re-encrypted, for a caller (doctor, the TUI) to report.
//
// MINOR 6: this runs unprompted on every `auxly doctor` and every TUI
// organize-tab open, which could otherwise race a LIVE
// TempDecryptForOrganize run and re-encrypt a file the CLI agent is mid-read
// on. Reading the sentinel snapshot under LockVault, and routing every
// resolve+clear through healAndClearSentinel/removeFromSentinel (which are
// themselves locked), serializes this heal's BOOKKEEPING against a
// concurrent TempDecrypt/restore call — it can never lose or clobber that
// run's sentinel entries. The one narrow residual window — this heal
// observing a file that TempDecrypt's own DecryptFile call just finished,
// microseconds before the CLI agent has actually read it — is accepted as a
// MINOR (not CRITICAL) risk: TempDecryptForOrganize always writes the
// sentinel BEFORE touching any file, so that window is vanishingly small
// compared to the run's overall duration, and a "spurious refusal" there is
// merely re-runnable, never a data-loss event.
func (s *Store) ReencryptPending() ([]string, error) {
	unlock, err := LockVault(s.Root)
	if err != nil {
		return nil, err
	}
	set, err := s.readSentinelSet()
	unlock()
	if err != nil {
		// A corrupt sentinel can never heal itself — remove it rather than
		// get permanently stuck; a human can re-check with `auxly encrypt
		// status` for any straggler.
		_ = os.Remove(s.reencryptSentinelPath())
		return nil, fmt.Errorf("corrupt reencrypt sentinel (removed): %w", err)
	}
	if len(set) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(set))
	for f := range set {
		names = append(names, f)
	}
	sort.Strings(names)

	healed, errs := s.healAndClearSentinel(names)
	if len(errs) > 0 {
		return healed, fmt.Errorf("re-encrypt pending files (run `auxly encrypt file <name>` on each): %w", errors.Join(errs...))
	}
	return healed, nil
}
