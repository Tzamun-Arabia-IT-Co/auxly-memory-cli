//go:build windows

package session

import (
	"os/exec"
	"strconv"
	"strings"
)

// AncestorCommands walks the process ancestry of an auxly server PID via a
// single Win32_Process CIM snapshot, returning the command lines of its
// non-auxly ancestors, nearest first, up to a bounded depth. Best-effort:
// returns nil if PowerShell/CIM is unavailable, in which case attribution falls
// back to the AUXLY_PROVIDER env the IDE config sets. Mirrors the Unix walker so
// InferProvider behaves identically across platforms.
func AncestorCommands(pid int) []string {
	script := "Get-CimInstance Win32_Process | " +
		"ForEach-Object { \"$($_.ProcessId)`t$($_.ParentProcessId)`t$($_.CommandLine)\" }"
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil
	}

	type proc struct {
		ppid int
		cmd  string
	}
	table := make(map[int]proc)
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
		table[id] = proc{ppid: ppid, cmd: cmd}
	}

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
