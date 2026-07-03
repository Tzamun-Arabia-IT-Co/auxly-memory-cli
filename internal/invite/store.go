package invite

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrUnknown     = errors.New("unknown invite")
	ErrExpired     = errors.New("expired invite")
	ErrFingerprint = errors.New("invite fingerprint mismatch")
)

const (
	lockFileName   = "invites.lock"
	lockStaleAfter = 30 * time.Second      // holder presumed crashed → takeover
	lockAcquireMax = 10 * time.Second      // give up waiting after this
	lockPollEvery  = 25 * time.Millisecond // retry cadence
)

// Store persists host-side pending invites.
type Store struct {
	path string
	mu   sync.Mutex
}

// Rec is a host-side pending invite record.
type Rec struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Expires     time.Time `json:"expires"`
	Created     time.Time `json:"created"`
}

type storeFile struct {
	Invites []Rec `json:"invites"`
}

// NewStore creates an invite store rooted at auxlyDir.
func NewStore(auxlyDir string) *Store {
	return &Store{path: filepath.Join(auxlyDir, "invites.json")}
}

// Add persists a pending invite without storing raw secret material.
func (s *Store) Add(t Token) error {
	release, err := s.lock()
	if err != nil {
		return err
	}
	defer release()

	file, err := s.load()
	if err != nil {
		return err
	}

	file.Invites = append(file.Invites, Rec{
		// WHY: invites.json leak must not allow joining.
		ID:          t.ID(),
		Fingerprint: t.Fingerprint,
		Host:        t.Host,
		Port:        t.Port,
		Expires:     t.Expires,
		Created:     time.Now(),
	})

	if err := s.save(file); err != nil {
		return fmt.Errorf("add invite: %w", err)
	}

	return nil
}

// Consume validates and deletes a pending invite.
func (s *Store) Consume(secret string, nowFingerprint string) (Rec, error) {
	release, err := s.lock()
	if err != nil {
		return Rec{}, err
	}
	defer release()

	file, err := s.load()
	if err != nil {
		return Rec{}, err
	}

	now := time.Now()
	wantID := idForSecret(secret)
	kept := file.Invites[:0]
	var matched Rec
	var found bool
	var expired bool

	for _, rec := range file.Invites {
		recExpired := isExpired(rec.Expires, now)
		if constantTimeStringEqual(rec.ID, wantID) {
			matched = rec
			found = true
			if recExpired {
				expired = true
			}
			continue
		}
		if !recExpired {
			kept = append(kept, rec)
		}
	}
	file.Invites = kept

	if !found {
		if err := s.save(file); err != nil {
			return Rec{}, fmt.Errorf("prune invites: %w", err)
		}
		return Rec{}, ErrUnknown
	}

	if expired {
		if err := s.save(file); err != nil {
			return Rec{}, fmt.Errorf("prune expired invite: %w", err)
		}
		return Rec{}, ErrExpired
	}

	if matched.Fingerprint != "" && matched.Fingerprint != nowFingerprint {
		file.Invites = append(file.Invites, matched)
		if err := s.save(file); err != nil {
			return Rec{}, fmt.Errorf("restore invite after fingerprint mismatch: %w", err)
		}
		return Rec{}, ErrFingerprint
	}

	if err := s.save(file); err != nil {
		return Rec{}, fmt.Errorf("consume invite: %w", err)
	}

	return matched, nil
}

// Lookup returns the pending invite matching secret WITHOUT consuming it, so
// a caller that needs a Rec field (e.g. the pinned port, to recompute its own
// verification fingerprint against the right port) can look it up before
// calling Consume. Read-only — never locks, mirrors List's unlocked fast
// path; Consume re-validates everything atomically regardless, so a stale
// read here is harmless.
func (s *Store) Lookup(secret string) (Rec, error) {
	file, err := s.load()
	if err != nil {
		return Rec{}, err
	}
	wantID := idForSecret(secret)
	now := time.Now()
	for _, rec := range file.Invites {
		if constantTimeStringEqual(rec.ID, wantID) {
			if isExpired(rec.Expires, now) {
				return Rec{}, ErrExpired
			}
			return rec, nil
		}
	}
	return Rec{}, ErrUnknown
}

// Revoke deletes a pending invite by its displayable ID (as returned by
// List), e.g. to back `host invite --revoke <id>`.
func (s *Store) Revoke(id string) error {
	release, err := s.lock()
	if err != nil {
		return err
	}
	defer release()

	file, err := s.load()
	if err != nil {
		return err
	}

	kept := file.Invites[:0]
	found := false
	for _, rec := range file.Invites {
		if rec.ID == id {
			found = true
			continue
		}
		kept = append(kept, rec)
	}
	if !found {
		return ErrUnknown
	}
	file.Invites = kept

	if err := s.save(file); err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}

	return nil
}

// List returns non-expired pending invites, pruning any expired records.
// A pure read never takes the lock or rewrites the file — only pruning
// does — so a read-only caller (e.g. `host invite --list` with nothing
// expired) never touches the write path at all.
func (s *Store) List() ([]Rec, error) {
	file, err := s.load()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if !anyExpired(file.Invites, now) {
		out := make([]Rec, len(file.Invites))
		copy(out, file.Invites)
		return out, nil
	}

	release, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer release()

	// The check above ran unlocked, so it may be stale; reload before pruning.
	file, err = s.load()
	if err != nil {
		return nil, err
	}
	kept := file.Invites[:0]
	for _, rec := range file.Invites {
		if !isExpired(rec.Expires, now) {
			kept = append(kept, rec)
		}
	}
	file.Invites = kept

	if err := s.save(file); err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}

	out := make([]Rec, len(file.Invites))
	copy(out, file.Invites)
	return out, nil
}

// isExpired reports whether exp has passed as of now.
//
// WHY: only Mint ever sets Expires; a zero value can only reach here via
// hand-built or corrupt data, so treat it as already expired (fail closed)
// rather than "never expires".
func isExpired(exp, now time.Time) bool {
	return exp.IsZero() || !exp.After(now)
}

func anyExpired(recs []Rec, now time.Time) bool {
	for _, r := range recs {
		if isExpired(r.Expires, now) {
			return true
		}
	}
	return false
}

func (s *Store) load() (storeFile, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return storeFile{}, nil
	}
	if err != nil {
		return storeFile{}, fmt.Errorf("read invites: %w", err)
	}

	var file storeFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return storeFile{}, nil
	}

	return file, nil
}

func (s *Store) save(file storeFile) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create invite dir: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("chmod invite dir: %w", err)
	}

	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal invites: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".invites-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp invite file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp invite file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp invite file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp invite file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename invite file: %w", err)
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		return fmt.Errorf("chmod invite file: %w", err)
	}

	return nil
}

// lock acquires the in-process mutex plus a cross-process file lock, and
// must be held across an entire load-mutate-save sequence.
//
// WHY: Store.mu alone only serializes goroutines within one process, but
// invites.json is shared by separate OS processes — e.g. `host invite
// --list` racing a join's Consume. Both load the file; Consume deletes the
// token and saves; then List's already-loaded (stale) snapshot saves over
// it, resurrecting a consumed invite (replay). O_CREATE|O_EXCL is atomic on
// every platform including Windows (no flock needed) — same technique as
// internal/memory/lock.go, kept local here since this is one small file and
// not worth a package dependency for ~30 lines.
//
// ponytail: no heartbeat (unlike lock.go) — Add/Consume/List/Revoke are fast
// JSON read-writes, never a long critical section that would need one.
func (s *Store) lock() (func(), error) {
	s.mu.Lock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("create invite dir: %w", err)
	}

	lockPath := filepath.Join(dir, lockFileName)
	deadline := time.Now().Add(lockAcquireMax)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() {
				os.Remove(lockPath)
				s.mu.Unlock()
			}, nil
		}
		if !os.IsExist(err) {
			s.mu.Unlock()
			return nil, fmt.Errorf("acquire invite lock: %w", err)
		}
		// Held by another process. Stale (crashed holder)? Remove and
		// re-race — O_EXCL on the next iteration picks exactly one winner.
		if st, serr := os.Stat(lockPath); serr == nil && time.Since(st.ModTime()) > lockStaleAfter {
			os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			s.mu.Unlock()
			return nil, fmt.Errorf("invites are locked by another auxly process (%s) — retry in a moment", lockPath)
		}
		time.Sleep(lockPollEvery)
	}
}

func constantTimeStringEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
