package invite

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMintEncodeDecodeRoundTrip(t *testing.T) {
	before := time.Now().Add(5 * time.Minute)
	token, err := Mint("host.example", 2200, "fp", 5*time.Minute)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	after := time.Now().Add(5 * time.Minute)

	decoded, err := Decode(token.Encode())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if decoded.Host != token.Host || decoded.Port != token.Port || decoded.Fingerprint != token.Fingerprint || decoded.Secret != token.Secret {
		t.Fatalf("decoded token mismatch: got %#v want %#v", decoded, token)
	}
	if decoded.Expires.Before(before.Add(-time.Second)) || decoded.Expires.After(after.Add(time.Second)) {
		t.Fatalf("decoded Expires = %s, want near minted TTL window", decoded.Expires)
	}
}

func TestEncodeShape(t *testing.T) {
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)

	if !regexp.MustCompile(`^auxly1-[a-z2-7]+$`).MatchString(token.Encode()) {
		t.Fatalf("Encode() = %q, want lowercase base32 token", token.Encode())
	}
}

func TestDecodeRejectsInvalidInput(t *testing.T) {
	tests := []string{
		"garbage",
		"auxly0-abcdef",
		"auxly1-a",
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			if _, err := Decode(tt); err == nil {
				t.Fatalf("Decode(%q) error = nil, want error", tt)
			}
		})
	}
}

func TestStoreAddConsumeSingleUse(t *testing.T) {
	store := NewStore(t.TempDir())
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)

	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	rec, err := store.Consume(token.Secret, "fp")
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if rec.ID != token.ID() || rec.Host != token.Host || rec.Port != token.Port || rec.Fingerprint != token.Fingerprint {
		t.Fatalf("Consume() record = %#v, want token-derived record", rec)
	}

	if _, err := store.Consume(token.Secret, "fp"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("second Consume() error = %v, want ErrUnknown", err)
	}

	recs, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("List() len = %d, want 0", len(recs))
	}
}

func TestExpiredConsumePrunesFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	expired := mustMint(t, "host.example", 2200, "fp", -time.Hour)
	active := mustMint(t, "host.example", 2201, "fp", time.Hour)

	if err := store.Add(expired); err != nil {
		t.Fatalf("Add(expired) error = %v", err)
	}
	if err := store.Add(active); err != nil {
		t.Fatalf("Add(active) error = %v", err)
	}

	if _, err := store.Consume(expired.Secret, "fp"); !errors.Is(err, ErrExpired) {
		t.Fatalf("Consume(expired) error = %v, want ErrExpired", err)
	}

	raw := readInvites(t, dir)
	if strings.Contains(string(raw), expired.ID()) {
		t.Fatalf("expired invite id still present in invites.json")
	}
	if !strings.Contains(string(raw), active.ID()) {
		t.Fatalf("active invite id missing after pruning expired invite")
	}
}

func TestFingerprintMismatch(t *testing.T) {
	store := NewStore(t.TempDir())
	token := mustMint(t, "host.example", 2200, "expected", time.Hour)

	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := store.Consume(token.Secret, "actual"); !errors.Is(err, ErrFingerprint) {
		t.Fatalf("Consume() error = %v, want ErrFingerprint", err)
	}
	if _, err := store.Consume(token.Secret, "expected"); err != nil {
		t.Fatalf("Consume() after mismatch error = %v", err)
	}
}

func TestEmptyFingerprintAcceptsAnyNowFingerprint(t *testing.T) {
	store := NewStore(t.TempDir())
	// Mint refuses empty fingerprints (TestMintRejectsEmptyFingerprint), but
	// an empty-pin Rec can still exist on disk from hand-built or pre-fix
	// data, so build one directly to exercise Consume's forward-tolerance
	// path rather than through Mint.
	token := Token{
		Host:        "host.example",
		Port:        2200,
		Fingerprint: "",
		Secret:      "deadbeefdeadbeefdead",
		Expires:     time.Now().Add(time.Hour),
	}

	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := store.Consume(token.Secret, "anything"); err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
}

func TestMintRejectsEmptyFingerprint(t *testing.T) {
	if _, err := Mint("host.example", 2200, "", time.Hour); err == nil {
		t.Fatal("Mint() error = nil, want error for empty fingerprint")
	}
}

func TestDecodeFoldsCase(t *testing.T) {
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)
	encoded := token.Encode()

	autocapitalized := strings.ToUpper(encoded[:1]) + encoded[1:] // "Auxly1-..."
	shouty := strings.ToUpper(encoded)

	for _, s := range []string{autocapitalized, shouty} {
		decoded, err := Decode(s)
		if err != nil {
			t.Fatalf("Decode(%q) error = %v", s, err)
		}
		if decoded.Secret != token.Secret || decoded.Host != token.Host {
			t.Fatalf("Decode(%q) = %#v, want token round-trip", s, decoded)
		}
	}
}

func TestCorruptInvitesFileTolerated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invites.json"), []byte("{not-json"), 0600); err != nil {
		t.Fatalf("write corrupt invites.json: %v", err)
	}

	store := NewStore(dir)
	recs, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("List() len = %d, want 0", len(recs))
	}

	token := mustMint(t, "host.example", 2200, "fp", time.Hour)
	if err := store.Add(token); err != nil {
		t.Fatalf("Add() after corrupt file error = %v", err)
	}
	if _, err := store.Consume(token.Secret, "fp"); err != nil {
		t.Fatalf("Consume() after corrupt file error = %v", err)
	}
}

func TestRawSecretNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)

	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	raw := readInvites(t, dir)
	if strings.Contains(string(raw), token.Secret) {
		t.Fatalf("raw secret was persisted in invites.json")
	}
}

func TestConcurrentConsumeSingleSuccess(t *testing.T) {
	store := NewStore(t.TempDir())
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)
	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	var successes int32
	var wg sync.WaitGroup
	errs := make(chan error, 32)

	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Consume(token.Secret, "fp")
			if err == nil {
				atomic.AddInt32(&successes, 1)
				return
			}
			errs <- err
		}()
	}

	wg.Wait()
	close(errs)

	if successes != 1 {
		t.Fatalf("successes = %d, want 1", successes)
	}
	for err := range errs {
		if !errors.Is(err, ErrUnknown) {
			t.Fatalf("concurrent Consume() error = %v, want ErrUnknown", err)
		}
	}
}

func TestZeroExpiresTreatedAsExpired(t *testing.T) {
	store := NewStore(t.TempDir())
	// Only Mint sets Expires; a zero value can only come from hand-built or
	// corrupt data, so build one directly.
	token := Token{Host: "host.example", Port: 2200, Fingerprint: "fp", Secret: "zeroexpirestoken"}

	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := store.Consume(token.Secret, "fp"); !errors.Is(err, ErrExpired) {
		t.Fatalf("Consume() error = %v, want ErrExpired for zero Expires", err)
	}
}

func TestListPrunesZeroExpires(t *testing.T) {
	store := NewStore(t.TempDir())
	zero := Token{Host: "host.example", Port: 2200, Fingerprint: "fp", Secret: "zeroexpirestoken"}
	active := mustMint(t, "host.example", 2201, "fp", time.Hour)

	if err := store.Add(zero); err != nil {
		t.Fatalf("Add(zero) error = %v", err)
	}
	if err := store.Add(active); err != nil {
		t.Fatalf("Add(active) error = %v", err)
	}

	recs, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(recs) != 1 || recs[0].ID != active.ID() {
		t.Fatalf("List() = %#v, want only the active invite (zero-Expires pruned as expired)", recs)
	}
}

func TestStoreRevoke(t *testing.T) {
	store := NewStore(t.TempDir())
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)
	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := store.Revoke(token.ID()); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, err := store.Consume(token.Secret, "fp"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Consume() after Revoke() error = %v, want ErrUnknown", err)
	}
}

func TestStoreRevokeUnknown(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Revoke("nonexistent"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Revoke() error = %v, want ErrUnknown", err)
	}
}

func TestListNoRewriteWhenNothingPruned(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)
	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	path := filepath.Join(dir, "invites.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat invites.json: %v", err)
	}

	if _, err := store.List(); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat invites.json: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("List() rewrote invites.json with nothing to prune: mtime %s -> %s", before.ModTime(), after.ModTime())
	}
}

// TestCrossProcessLockPreventsConsumedInviteResurrection simulates two OS
// processes sharing invites.json via two separate Store instances. storeB
// loads a snapshot before storeA's Consume runs (as `host invite --list`
// might race ahead of a join), then holds the cross-process lock while it
// "finishes" its save — proving the lock, not scheduling luck, is what
// forces storeA's Consume to wait and then operate on fresh data instead of
// having its deletion clobbered by storeB's stale snapshot.
func TestCrossProcessLockPreventsConsumedInviteResurrection(t *testing.T) {
	dir := t.TempDir()
	storeA := NewStore(dir) // simulates the join process: Consume
	storeB := NewStore(dir) // simulates `host invite --list`

	active := mustMint(t, "host.example", 2200, "fp", time.Hour)
	if err := storeA.Add(active); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	releaseB, err := storeB.lock()
	if err != nil {
		t.Fatalf("storeB.lock() error = %v", err)
	}
	staleSnapshot, err := storeB.load()
	if err != nil {
		t.Fatalf("storeB.load() error = %v", err)
	}

	consumeDone := make(chan error, 1)
	go func() {
		_, err := storeA.Consume(active.Secret, "fp")
		consumeDone <- err
	}()

	// storeA must block on the lock storeB holds, not interleave with it.
	select {
	case <-consumeDone:
		t.Fatal("storeA.Consume() returned before storeB released the lock")
	case <-time.After(100 * time.Millisecond):
	}

	if err := storeB.save(staleSnapshot); err != nil {
		t.Fatalf("storeB.save() error = %v", err)
	}
	releaseB()

	if err := <-consumeDone; err != nil {
		t.Fatalf("Consume() error = %v", err)
	}

	recs, err := storeA.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, r := range recs {
		if r.ID == active.ID() {
			t.Fatalf("consumed invite %q resurrected by a racing List save", active.ID())
		}
	}
}

func TestStaleLockTakenOver(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	token := mustMint(t, "host.example", 2200, "fp", time.Hour)
	if err := store.Add(token); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	lockPath := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(lockPath, []byte("leaked\n"), 0600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	stale := time.Now().Add(-2 * lockStaleAfter)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("backdate stale lock: %v", err)
	}

	if _, err := store.Consume(token.Secret, "fp"); err != nil {
		t.Fatalf("Consume() error = %v, want stale lock takeover to succeed", err)
	}
}

func mustMint(t *testing.T, host string, port int, fingerprint string, ttl time.Duration) Token {
	t.Helper()

	token, err := Mint(host, port, fingerprint, ttl)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	return token
}

func readInvites(t *testing.T, dir string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, "invites.json"))
	if err != nil {
		t.Fatalf("read invites.json: %v", err)
	}
	return raw
}
