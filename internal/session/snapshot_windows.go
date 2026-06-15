//go:build windows

package session

import (
	"os/exec"
	"strconv"
	"strings"
)

// winProc is one row of the Windows process table.
type winProc struct {
	ppid int
	cmd  string
}

// winProcTable enumerates every process ONCE via a single PowerShell CIM query,
// returning pid -> {ppid, command line}. This is THE expensive Windows call
// (PowerShell cold start + CIM enumeration); buildSnapshot runs it a single time
// and derives the server list, ancestry, and liveness from the result in-process
// — replacing the previous one-PowerShell-per-PID storm.
func winProcTable() map[int]winProc {
	script := "Get-CimInstance Win32_Process | " +
		"ForEach-Object { \"$($_.ProcessId)`t$($_.ParentProcessId)`t$($_.CommandLine)\" }"
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil
	}
	table := make(map[int]winProc)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		id, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		ppid, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			continue
		}
		cmd := ""
		if len(parts) == 3 {
			cmd = parts[2]
		}
		table[id] = winProc{ppid: ppid, cmd: cmd}
	}
	return table
}

// buildSnapshot takes one Win32_Process snapshot and derives everything the
// dashboard needs from it, eliminating the N+1 PowerShell spawns per refresh.
func buildSnapshot() *Snapshot {
	table := winProcTable()
	s := &Snapshot{
		ancestors:          map[int][]string{},
		alive:              make(map[int]bool, len(table)),
		aliveAuthoritative: true,
	}
	if table == nil {
		// Query failed: report nothing live, but stay NON-authoritative so callers
		// fall back to the standalone probes rather than treating every PID as dead
		// (which would wrongly prune every live session record).
		s.aliveAuthoritative = false
		return s
	}
	for pid := range table {
		s.alive[pid] = true
	}
	// Live `auxly mcp-server` processes: the command line references both the
	// auxly binary and the mcp-server subcommand (the node-based legacy server
	// never matches). Mirrors scan_windows.go's LiveServerPIDs predicate.
	for pid, p := range table {
		cl := strings.ToLower(p.cmd)
		if strings.Contains(cl, "mcp-server") && strings.Contains(cl, "auxly") {
			s.serverPIDs = append(s.serverPIDs, pid)
		}
	}
	// Walk each server's ancestry over the in-memory table (zero extra exec
	// calls). Mirrors ancestry_windows.go's AncestorCommands walk verbatim.
	for _, pid := range s.serverPIDs {
		s.ancestors[pid] = walkAncestors(table, pid)
	}
	return s
}

// walkAncestors returns the command lines of pid's non-auxly ancestors, nearest
// first, up to a bounded depth — the same walk as AncestorCommands but over an
// already-captured table instead of a fresh CIM query.
func walkAncestors(table map[int]winProc, pid int) []string {
	var ancestors []string
	cur := pid
	for i := 0; i < 12; i++ {
		p, ok := table[cur]
		if !ok {
			break
		}
		if p.cmd != "" && !strings.Contains(strings.ToLower(p.cmd), "auxly") {
			ancestors = append(ancestors, p.cmd)
		}
		if p.ppid <= 0 || p.ppid == cur {
			break
		}
		cur = p.ppid
	}
	return ancestors
}
