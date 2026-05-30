//go:build windows

package session

import (
	"os/exec"
	"strconv"
	"strings"
)

// PidsAlive reports which of the given PIDs are currently running, using a
// single `tasklist` CSV snapshot rather than one query per PID.
func PidsAlive(pids []int) map[int]bool {
	alive := make(map[int]bool, len(pids))

	out, err := exec.Command("tasklist", "/NH", "/FO", "CSV").Output()
	if err != nil {
		// If tasklist is unavailable, assume recorded sessions are alive — a
		// stale entry is less confusing than silently hiding live ones.
		for _, pid := range pids {
			alive[pid] = true
		}
		return alive
	}

	running := make(map[int]bool)
	for _, line := range strings.Split(string(out), "\n") {
		// CSV columns: "Image","PID","Session","Session#","MemUsage".
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		pidStr := strings.Trim(strings.TrimSpace(fields[1]), `"`)
		if pid, err := strconv.Atoi(pidStr); err == nil {
			running[pid] = true
		}
	}
	for _, pid := range pids {
		alive[pid] = running[pid]
	}
	return alive
}
