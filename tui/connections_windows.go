//go:build windows

package tui

import (
	"os/exec"
	"strconv"
	"strings"
)

// scanProcs returns every running process as pid -> {ppid, full command line},
// via PowerShell's CIM Win32_Process. Unlike `tasklist`, Win32_Process exposes
// the full CommandLine (with --provider / --source / --remote-* args and the
// auxly binary path), which is exactly what the parser needs. Fields are
// tab-delimited because command lines contain spaces.
func scanProcs() map[int]procInfo {
	// `t is the PowerShell tab escape inside the double-quoted output string.
	script := "Get-CimInstance Win32_Process | " +
		"ForEach-Object { \"$($_.ProcessId)`t$($_.ParentProcessId)`t$($_.CommandLine)\" }"

	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil
	}

	procs := make(map[int]procInfo)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		command := strings.TrimSpace(parts[2])
		if command == "" {
			continue // system processes expose no CommandLine
		}
		procs[pid] = procInfo{ppid: ppid, command: command}
	}
	return procs
}
