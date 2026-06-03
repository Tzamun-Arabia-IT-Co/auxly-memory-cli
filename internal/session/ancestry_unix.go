//go:build !windows

package session

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// AncestorCommands walks the process ancestry of an auxly server PID and returns
// the command paths of its non-auxly ancestors, nearest first, up to a bounded
// depth. auxly wrapper processes (the binary itself and the macOS Gatekeeper
// "disclaimer" shim) are skipped so the first returned entry is the real
// launching agent. Feeds InferProvider for both server self-attribution and
// dashboard reconciliation of live-but-unregistered servers.
func AncestorCommands(pid int) []string {
	var ancestors []string
	cur := pid
	for i := 0; i < 12; i++ {
		out, err := exec.Command("ps", "-p", strconv.Itoa(cur), "-o", "ppid=,comm=").Output()
		if err != nil {
			break
		}
		line := strings.TrimSpace(string(out))
		if line == "" {
			break
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			break
		}
		ppid, atoiErr := strconv.Atoi(parts[0])
		comm := strings.Join(parts[1:], " ")
		if !strings.Contains(strings.ToLower(filepath.Base(comm)), "auxly") {
			ancestors = append(ancestors, comm)
		}
		if atoiErr != nil || ppid <= 1 {
			break
		}
		cur = ppid
	}
	return ancestors
}
