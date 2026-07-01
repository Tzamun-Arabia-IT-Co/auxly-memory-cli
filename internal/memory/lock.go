package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Vault mutations must be serialized across BOTH goroutines and processes:
// several MCP servers (one per agent) plus the CLI/TUI can write the same
// vault concurrently. An in-process mutex covers goroutines; an O_CREATE|O_EXCL
// lock file covers processes. Pure stdlib — no flock — so semantics are
// identical on Windows.
//
// ponytail: one coarse lock per vault root (workspace writes also take the
// global root's lock); per-file locks if write contention ever matters.

const (
	lockFileName   = ".lock"
	lockStaleAfter = 30 * time.Second      // holder presumed crashed → takeover
	lockAcquireMax = 10 * time.Second      // give up waiting after this
	lockPollEvery  = 25 * time.Millisecond // retry cadence
)

var (
	lockRegistryMu sync.Mutex
	lockRegistry   = map[string]*sync.Mutex{}
)

func inProcLock(root string) *sync.Mutex {
	lockRegistryMu.Lock()
	defer lockRegistryMu.Unlock()
	if mu, ok := lockRegistry[root]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	lockRegistry[root] = mu
	return mu
}

// LockVault serializes vault mutations for the given memory root across
// goroutines and processes. Callers MUST invoke the returned release func
// (defer it). Waits up to lockAcquireMax for a competing writer; a lock file
// older than lockStaleAfter is treated as leaked by a crashed process and
// taken over.
//
// CompileUnified deliberately does NOT take this lock (it runs inside
// Write/Approve which already hold it — re-acquiring would deadlock, and its
// output is a derived rollup whose atomic write keeps it consistent anyway).
func LockVault(root string) (func(), error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("cannot create memory directory: %w", err)
	}
	mu := inProcLock(root)
	mu.Lock()

	lockPath := filepath.Join(root, lockFileName)
	deadline := time.Now().Add(lockAcquireMax)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			// Heartbeat: staleness is judged by mtime, so a legitimate holder in a
			// long critical section (big CompileUnified, slow disk, antivirus
			// stalling renames on Windows) must keep the file fresh, or another
			// process would "take over" the lock mid-write.
			stop := make(chan struct{})
			go func() {
				t := time.NewTicker(lockStaleAfter / 4)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case <-t.C:
						now := time.Now()
						os.Chtimes(lockPath, now, now)
					}
				}
			}()
			return func() {
				close(stop)
				os.Remove(lockPath)
				mu.Unlock()
			}, nil
		}
		if !os.IsExist(err) {
			mu.Unlock()
			return nil, fmt.Errorf("cannot acquire vault lock: %w", err)
		}
		// Held by another process. Stale (crashed holder)? Remove and re-race —
		// O_EXCL on the next iteration picks exactly one winner.
		if st, serr := os.Stat(lockPath); serr == nil && time.Since(st.ModTime()) > lockStaleAfter {
			os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			mu.Unlock()
			return nil, fmt.Errorf("memory vault is locked by another auxly process (%s) — retry in a moment; if no other auxly is running, delete that file", lockPath)
		}
		time.Sleep(lockPollEvery)
	}
}
