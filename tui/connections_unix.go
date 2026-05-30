//go:build !windows

package tui

import (
	"os/exec"
	"strconv"
	"strings"
)

// scanProcs returns every running process as pid -> {ppid, full command line},
// using the Unix `ps` tool. `-ww` keeps long command lines from being truncated.
func scanProcs() map[int]procInfo {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil
	}

	procs := make(map[int]procInfo)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		command := strings.TrimSpace(line[strings.Index(line, fields[2]):])
		procs[pid] = procInfo{ppid: ppid, command: command}
	}
	return procs
}
