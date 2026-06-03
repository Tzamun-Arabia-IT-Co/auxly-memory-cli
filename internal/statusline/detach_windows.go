//go:build windows

package statusline

import "syscall"

// detachSysProcAttr is a no-op on Windows (no process groups via Setpgid); the
// child still outlives the parent's exit. Windows isn't a Claude Code statusline
// target in practice, but this keeps the package building for all GOOS.
func detachSysProcAttr() *syscall.SysProcAttr { return nil }
