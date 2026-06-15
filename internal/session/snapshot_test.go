package session

import (
	"testing"
	"time"
)

// CurrentSnapshot must reuse a cached snapshot within the TTL window so the 1s
// dashboard tick (and far more frequent SSH-screen repaints) don't re-spawn the
// underlying process query — the whole point of the Windows N+1 fix.
func TestCurrentSnapshotCachesWithinTTL(t *testing.T) {
	// Reset cache so the test is independent of prior calls.
	snapMu.Lock()
	snapCache = nil
	snapAt = time.Time{}
	snapMu.Unlock()

	first := CurrentSnapshot()
	if first == nil {
		t.Fatal("CurrentSnapshot returned nil")
	}
	second := CurrentSnapshot()
	if first != second {
		t.Error("within TTL, CurrentSnapshot must return the SAME cached snapshot pointer")
	}

	// Force expiry and confirm a new snapshot is built.
	snapMu.Lock()
	snapAt = time.Now().Add(-2 * snapshotTTL)
	snapMu.Unlock()
	third := CurrentSnapshot()
	if third == first {
		t.Error("after TTL expiry, CurrentSnapshot must rebuild (new pointer)")
	}
}

// A snapshot must answer queries for a PID it never captured without panicking
// (nil-map reads), since gatherSessions asks ancestry for arbitrary live PIDs.
func TestSnapshotUnknownPID(t *testing.T) {
	s := CurrentSnapshot()
	if got := s.AncestorCommands(-1); got != nil {
		t.Errorf("AncestorCommands for an unknown PID should be nil, got %v", got)
	}
	alive := s.PidsAlive([]int{-1})
	if alive[-1] {
		t.Error("an impossible PID (-1) must not read as alive")
	}
}
