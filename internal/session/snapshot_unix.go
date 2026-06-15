//go:build !windows

package session

// buildSnapshot on Unix delegates to the existing cheap probes (a single
// `ps -axww` for the server list, then a short `ps -p` chain per server), so
// per-PID behavior is byte-identical to calling them directly — `ps` has no
// PowerShell-style cold start, so there is nothing to collapse. The shared TTL
// cache in CurrentSnapshot still coalesces bursts of dashboard repaints into one
// pass. Liveness stays non-authoritative: PidsAlive falls back to its own ps.
func buildSnapshot() *Snapshot {
	pids := LiveServerPIDs()
	anc := make(map[int][]string, len(pids))
	for _, pid := range pids {
		anc[pid] = AncestorCommands(pid)
	}
	return &Snapshot{
		serverPIDs:         pids,
		ancestors:          anc,
		aliveAuthoritative: false,
	}
}
