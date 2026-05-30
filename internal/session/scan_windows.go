//go:build windows

package session

import (
	"os/exec"
	"strconv"
	"strings"
)

// LiveServerPIDs returns the PIDs of every running `auxly mcp-server` process,
// via PowerShell CIM. Used by the dashboard's force-refresh to detect agents
// that are running but not yet reflected in the session registry.
func LiveServerPIDs() []int {
	script := "Get-CimInstance Win32_Process | " +
		"Where-Object { $_.CommandLine -like '*mcp-server*' } | " +
		"ForEach-Object { \"$($_.ProcessId)`t$($_.CommandLine)\" }"

	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil
	}

	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		// The command line must reference the auxly binary (the node-based
		// legacy server never matches).
		if !strings.Contains(strings.ToLower(parts[1]), "auxly") {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
