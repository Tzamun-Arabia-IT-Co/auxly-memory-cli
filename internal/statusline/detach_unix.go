//go:build !windows

package statusline

import "syscall"

// detachSysProcAttr puts the refresh child in its own process group so it fully
// detaches from the statusline process and the controlling terminal, surviving
// the parent's immediate exit.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
