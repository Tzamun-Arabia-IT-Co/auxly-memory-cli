//go:build !windows

package statusline

import (
	"os"

	"golang.org/x/sys/unix"
)

// ttyWidth reads the column count from the controlling terminal via TIOCGWINSZ. The
// statusline's stdout is a pipe to the agent, so we query /dev/tty (which the agent's
// child process inherits) rather than a std stream. Returns 0 on any failure.
func ttyWidth() int {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return 0
	}
	defer f.Close()
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws == nil {
		return 0
	}
	return int(ws.Col)
}
