package session

import (
	"sync"
	"time"
)

// Snapshot is a single, cached view of the live process table used by the
// dashboard to find connected MCP servers, attribute them, and prune dead
// session records. Taking it ONCE per refresh matters on Windows, where every
// underlying query cold-starts PowerShell: the previous code spawned one
// PowerShell per live server (ancestry) PLUS one for the server list PLUS a
// tasklist — on every 1s dashboard tick and every SSH-screen repaint. Here a
// single platform snapshot serves all three, cached briefly so repeated
// ticks/repaints reuse it instead of re-spawning.
type Snapshot struct {
	serverPIDs []int
	ancestors  map[int][]string
	// alive, when authoritative (Windows: the snapshot enumerated every live
	// PID), answers liveness without a second subprocess. On Unix it is left nil
	// and PidsAlive falls back to the existing cheap ps probe.
	alive              map[int]bool
	aliveAuthoritative bool
}

// LiveServerPIDs returns the PIDs of every running `auxly mcp-server` captured
// in this snapshot.
func (s *Snapshot) LiveServerPIDs() []int { return s.serverPIDs }

// AncestorCommands returns the captured ancestor command lines for pid (nearest
// first), or nil if pid was not a live server in this snapshot.
func (s *Snapshot) AncestorCommands(pid int) []string { return s.ancestors[pid] }

// PidsAlive reports which of pids are currently running, reusing this snapshot's
// process table when it is authoritative (Windows) and otherwise falling back to
// the standalone probe (Unix, where that probe is a single cheap ps).
func (s *Snapshot) PidsAlive(pids []int) map[int]bool {
	if !s.aliveAuthoritative {
		return PidsAlive(pids)
	}
	out := make(map[int]bool, len(pids))
	for _, p := range pids {
		out[p] = s.alive[p]
	}
	return out
}

// snapshotTTL bounds how often the underlying process query runs. The dashboard
// ticks ~1/s and the SSH screen can repaint far more often, but the process
// table changes slowly, so a short TTL collapses that burst into one query. A
// pruned-but-just-died PID lingering ≤TTL is harmless (the dashboard already
// shows ~1s-stale data).
const snapshotTTL = 2500 * time.Millisecond

var (
	snapMu    sync.Mutex
	snapCache *Snapshot
	snapAt    time.Time
)

// CurrentSnapshot returns a process snapshot, refreshing it at most once per
// snapshotTTL. Safe for concurrent callers.
func CurrentSnapshot() *Snapshot {
	snapMu.Lock()
	defer snapMu.Unlock()
	if snapCache != nil && time.Since(snapAt) < snapshotTTL {
		return snapCache
	}
	snapCache = buildSnapshot()
	snapAt = time.Now()
	return snapCache
}
