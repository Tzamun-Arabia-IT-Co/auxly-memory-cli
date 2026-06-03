//go:build !windows

package session

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// LiveServerPIDs returns the PIDs of every running `auxly mcp-server` process,
// regardless of whether it registered a session file. Used by the dashboard's
// force-refresh to detect agents that are running but not yet reflected (e.g.
// servers started before the session-registry feature existed).
func LiveServerPIDs() []int {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil
	}

	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(line, "mcp-server") {
			continue
		}
		// argv[0] must be the auxly binary itself — excludes the macOS
		// Gatekeeper "disclaimer" wrapper and the node-based legacy server.
		base := strings.ToLower(filepath.Base(fields[1]))
		if base != "auxly" && base != "auxly.exe" {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
