//go:build !windows

package session

import "syscall"

// PidsAlive reports which of the given PIDs are currently running. Signal 0
// performs OS-level existence/permission checking without delivering a signal:
// nil means alive; EPERM means alive but owned by another user.
func PidsAlive(pids []int) map[int]bool {
	alive := make(map[int]bool, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		err := syscall.Kill(pid, 0)
		alive[pid] = err == nil || err == syscall.EPERM
	}
	return alive
}
