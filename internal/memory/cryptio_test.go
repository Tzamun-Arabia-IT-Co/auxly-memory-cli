package memory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

// testVaultIdentity generates a throwaway age identity and points
// AUXLY_VAULT_KEY at it for the duration of the test — Keystore.Identity()/
// Recipient() check the env override FIRST, before touching the keychain or
// any file, so this is fully hermetic (no keychain, no keys/ directory
// needed; vaultcrypt's own tests cover Generate()/keychain/file storage).
func testVaultIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())
	return identity
}

// seedCiphertext writes name directly as age ciphertext under identity's
// recipient — simulating a file that was already encrypted on disk, without
// going through Store.writeVaultFile (which is what's under test elsewhere).
func seedCiphertext(t *testing.T, s *Store, name string, identity *age.X25519Identity, plain string) {
	t.Helper()
	ciphertext, err := vaultcrypt.Encrypt([]byte(plain), identity.Recipient())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, name), ciphertext, 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func TestViewDecryptsEncryptedFile(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	identity := testVaultIdentity(t)
	plain := "- a secret business fact\n"
	seedCiphertext(t, s, "business.md", identity, plain)

	got, err := s.View("business.md")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got != plain {
		t.Fatalf("View = %q, want %q", got, plain)
	}
}

// TestWriteToEncryptedFileStaysEncrypted is the core wiring guarantee: once a
// file is encrypted on disk, an ordinary Store.Write must re-encrypt it — raw
// bytes carry the age header, plaintext is absent from both the final file
// and any .tmp leftover (AtomicWriteFile's temp file must never hold
// plaintext of an encrypted target — advisory #1).
func TestWriteToEncryptedFileStaysEncrypted(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	identity := testVaultIdentity(t)
	seedCiphertext(t, s, "business.md", identity, "- old fact\n")

	if err := s.Write("business.md", "- new fact\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path := filepath.Join(s.Root, "business.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatalf("business.md is not encrypted on disk after write: %q", raw)
	}
	if bytes.Contains(raw, []byte("new fact")) {
		t.Fatal("plaintext leaked into the on-disk file")
	}

	entries, err := os.ReadDir(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".auxly-tmp-") {
			data, _ := os.ReadFile(filepath.Join(s.Root, e.Name()))
			t.Fatalf("leftover temp file %s (contents %q)", e.Name(), data)
		}
	}

	got, err := s.View("business.md")
	if err != nil {
		t.Fatalf("View after write: %v", err)
	}
	if got != "- new fact\n" {
		t.Fatalf("View after write = %q", got)
	}
}

// TestArchiveFactOfEncryptedSourceStaysEncrypted covers decay.go's
// readViewCopy/ArchiveFact: the source stays encrypted after the fact is
// removed, and the .archive/<file> copy — created for the FIRST time by this
// call, so it has no prior on-disk state to inherit — is also encrypted,
// because ArchiveFact threads the source's encrypted flag through rather
// than defaulting a not-yet-existing archive file to plaintext.
func TestArchiveFactOfEncryptedSourceStaysEncrypted(t *testing.T) {
	s := newDecayTestStore(t)
	identity := testVaultIdentity(t)
	line := "- Old encrypted fact"
	seedCiphertext(t, s, "identity.md", identity, "# Identity\n"+line+"\n")

	if err := s.ArchiveFact("identity.md", line); err != nil {
		t.Fatalf("ArchiveFact: %v", err)
	}

	srcRaw, err := os.ReadFile(filepath.Join(s.Root, "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(srcRaw) {
		t.Fatal("identity.md lost its encryption after ArchiveFact")
	}
	if bytes.Contains(srcRaw, []byte(line)) {
		t.Fatal("plaintext leaked into the source file")
	}

	archiveRaw, err := os.ReadFile(filepath.Join(s.Root, ".archive", "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(archiveRaw) {
		t.Fatal(".archive/identity.md must stay encrypted for an encrypted source")
	}
	if bytes.Contains(archiveRaw, []byte(line)) {
		t.Fatal("plaintext leaked into the archive file")
	}

	plain, _, err := s.readVaultFile(filepath.Join(s.Root, ".archive", "identity.md"))
	if err != nil {
		t.Fatalf("decrypt archive: %v", err)
	}
	if !strings.Contains(string(plain), line) {
		t.Fatalf("archived content = %q, want it to contain %q", plain, line)
	}
}

// TestRefreshIndexSkipsEncryptedFile mirrors mcp/recall_analytics_test.go's
// auditDBBytes grep pattern: read the raw sidecar bytes and assert secret
// plaintext is nowhere in them, not even inside a BLOB. embeddings.db must
// never become a plaintext shadow copy of an encrypted file (advisory #3).
func TestRefreshIndexSkipsEncryptedFile(t *testing.T) {
	s := newAdminVault(t)
	identity := testVaultIdentity(t)
	secret := "zebra-9713-secret-business-fact"
	seedCiphertext(t, s, "business.md", identity, "# Business\n\n- "+secret+"\n")

	emb := adminStubEmbedder{enabled: true}
	if _, err := s.RebuildIndex(context.Background(), emb); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}

	dbBytes, err := os.ReadFile(s.indexDBPath())
	if err != nil {
		t.Fatalf("read index db: %v", err)
	}
	if bytes.Contains(dbBytes, []byte(secret)) {
		t.Fatal("encrypted file's plaintext leaked into embeddings.db")
	}

	ix, err := OpenIndexReadOnly(s.indexDBPath())
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer ix.Close()
	chunks, err := ix.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range chunks {
		if c.File == "business.md" {
			t.Fatalf("business.md was indexed despite being encrypted: %+v", c)
		}
	}
}

func TestCompileUnifiedMarksEncryptedFileAsNotCompiled(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	identity := testVaultIdentity(t)
	secret := "top-secret-business-detail"
	seedCiphertext(t, s, "business.md", identity, "# Business\n\n- "+secret+"\n")
	if err := s.Write("identity.md", "# Identity\n\n- plain fact\n"); err != nil {
		t.Fatal(err)
	}

	if err := s.CompileUnified(); err != nil {
		t.Fatalf("CompileUnified: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(s.Root, "unified_memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), secret) {
		t.Fatal("encrypted file's plaintext leaked into unified_memory.md")
	}
	if !strings.Contains(string(got), "business.md: encrypted — not compiled") {
		t.Fatalf("missing encrypted marker line, got:\n%s", got)
	}
	if !strings.Contains(string(got), "plain fact") {
		t.Fatal("plaintext file's content is missing from the unified compile")
	}
}

// TestReadEncryptedFileNoKeyFailsClosed points AUXLY_VAULT_KEY at a
// syntactically-invalid identity so Keystore.Identity() fails at env
// parsing — the FIRST resolution step, checked before keychain or file
// storage (see keystore.go) — making this hermetic (no real macOS keychain
// probe on darwin). It is still a genuine resolution failure, same class as
// a missing key. The point under test is that a key resolution failure on
// an encrypted file is an ERROR, mentioning the escape hatch, never a silent
// pass-through of ciphertext as content.
func TestReadEncryptedFileNoKeyFailsClosed(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	seedCiphertext(t, s, "business.md", identity, "- secret\n")
	t.Setenv("AUXLY_VAULT_KEY", "not-a-valid-age-identity")

	_, err = s.View("business.md")
	if err == nil {
		t.Fatal("View of an encrypted file with no reachable key succeeded, want error")
	}
	if !strings.Contains(err.Error(), "encrypt status") {
		t.Fatalf("error = %v, want it to mention `auxly encrypt status`", err)
	}
}

func TestEncryptFileMigratesPlaintextAndVerifies(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	testVaultIdentity(t)
	plain := "- personal fact\n"
	if err := os.WriteFile(filepath.Join(s.Root, "personal.md"), []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.EncryptFile("personal.md"); err != nil {
		t.Fatalf("EncryptFile: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(s.Root, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatal("personal.md not encrypted after EncryptFile")
	}
	if bytes.Contains(raw, []byte("personal fact")) {
		t.Fatal("plaintext leaked into on-disk personal.md")
	}

	got, err := s.View("personal.md")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got != plain {
		t.Fatalf("View after EncryptFile = %q, want %q", got, plain)
	}

	if err := s.EncryptFile("personal.md"); err == nil {
		t.Fatal("EncryptFile on an already-encrypted file succeeded, want rejection")
	}
	if err := s.EncryptFile(unifiedMemoryFile); err == nil {
		t.Fatal("EncryptFile(unified_memory.md) succeeded, want rejection (compiled artifact)")
	}
}

// TestEncryptFileAbortsOnVerifyMismatch drives EncryptFile's decrypt-verify
// gate deterministically: pre-seed the Store's cached RECIPIENT from key A
// (so the encrypt step uses A's public key) while AUXLY_VAULT_KEY resolves
// the IDENTITY to key B (so the verify-decrypt step uses B's private key).
// The round trip cannot succeed, so EncryptFile must abort — original file
// untouched, no ciphertext written.
func TestEncryptFileAbortsOnVerifyMismatch(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	original := "- fact that must survive\n"
	if err := os.WriteFile(filepath.Join(s.Root, "business.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	identityA, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	testVaultIdentity(t) // AUXLY_VAULT_KEY = identity B; vaultIdentity() resolves to B
	s.cryptoRecipient = identityA.Recipient()

	err = s.EncryptFile("business.md")
	if err == nil {
		t.Fatal("EncryptFile succeeded despite a verify mismatch, want abort")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("error = %v, want it to mention the abort", err)
	}

	got, err := os.ReadFile(filepath.Join(s.Root, "business.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("original file was modified after an aborted encrypt: %q", got)
	}
	if vaultcrypt.IsEncrypted(got) {
		t.Fatal("business.md became encrypted despite the abort")
	}
}

// CRITICAL 4 regression: encrypting a previously-embedded file must prune its
// plaintext chunk rows from the semantic index — they must not linger inside
// the (possibly-synced) vault's .index/embeddings.db until some future
// recall happens to re-touch the file.
func TestEncryptFile_PrunesStalePlaintextFromIndex(t *testing.T) {
	s := newAdminVault(t)
	testVaultIdentity(t)
	secret := "zebra-4471-personal-secret-fact"
	if err := s.Write("personal.md", "# Personal\n\n- "+secret+"\n"); err != nil {
		t.Fatal(err)
	}

	emb := adminStubEmbedder{enabled: true}
	if _, err := s.RebuildIndex(context.Background(), emb); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	before, err := os.ReadFile(s.indexDBPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte(secret)) {
		t.Fatal("test setup broken: secret was never indexed")
	}

	if err := s.EncryptFile("personal.md"); err != nil {
		t.Fatalf("EncryptFile: %v", err)
	}

	after, err := os.ReadFile(s.indexDBPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(after, []byte(secret)) {
		t.Fatal("plaintext chunk survived in embeddings.db after EncryptFile")
	}
}

// MAJOR 6 regression: EncryptFile's read of the CURRENT content must happen
// INSIDE the vault lock, immediately before the write. Reading it earlier
// (unlocked) — the old bug — let a concurrent, lock-disciplined writer's
// completed merge be silently reverted by encrypting a stale snapshot.
//
// ponytail: a true race is inherently probabilistic to catch without an
// invasive sync hook inside EncryptFile itself; run many trials with a
// scheduling yield in the writer goroutine to bias toward the interleaving
// that used to lose data. A regression (read moved back outside the lock)
// should fail this reliably.
func TestEncryptFile_ConcurrentMergeWriteNeverReverted(t *testing.T) {
	for trial := 0; trial < 30; trial++ {
		s := &Store{Root: t.TempDir()}
		testVaultIdentity(t)
		path := filepath.Join(s.Root, "business.md")
		if err := os.WriteFile(path, []byte("- original fact\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.EncryptFile("business.md")
		}()
		go func() {
			defer wg.Done()
			runtime.Gosched()
			unlock, err := LockVault(s.Root)
			if err != nil {
				return
			}
			defer unlock()
			data, _, _ := s.readVaultFile(path)
			merged := string(data) + "- concurrent fact\n"
			encrypt := s.shouldStayEncrypted(path)
			_ = s.writeVaultFile(path, []byte(merged), 0644, encrypt)
		}()
		wg.Wait()

		plain, _, err := s.readVaultFile(path)
		if err != nil {
			t.Fatalf("trial %d: read final content: %v", trial, err)
		}
		if !strings.Contains(string(plain), "concurrent fact") {
			t.Fatalf("trial %d: concurrent writer's fact was reverted by a stale EncryptFile snapshot — final content: %q", trial, plain)
		}
	}
}

// MAJOR 7 regression: PrewarmCrypto must be a no-op (nothing cached) when the
// vault has no encrypted files, and must populate the identity/recipient
// cache when it does — the property every ArchiveFact/RestampFact/
// MigratePersonal/pending-approve/cmd-write call site relies on to avoid
// resolving crypto material (possible keychain exec) while holding LockVault.
func TestPrewarmCrypto_NoopWithoutEncryptedFilesElseWarmsCache(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("identity.md", "- plain fact\n"); err != nil {
		t.Fatal(err)
	}
	s.PrewarmCrypto()
	s.cryptoMu.Lock()
	cachedNothing := s.cryptoIdentity == nil && s.cryptoRecipient == nil
	s.cryptoMu.Unlock()
	if !cachedNothing {
		t.Fatal("PrewarmCrypto resolved crypto material despite no encrypted files in the vault")
	}

	identity := testVaultIdentity(t)
	seedCiphertext(t, s, "business.md", identity, "- secret\n")

	s.PrewarmCrypto()
	s.cryptoMu.Lock()
	idCached, recCached := s.cryptoIdentity != nil, s.cryptoRecipient != nil
	s.cryptoMu.Unlock()
	if !idCached || !recCached {
		t.Fatal("PrewarmCrypto did not cache identity/recipient despite an encrypted file present")
	}
}
