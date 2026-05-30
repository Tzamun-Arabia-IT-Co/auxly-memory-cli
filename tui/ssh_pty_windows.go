//go:build windows

package tui

// startPTYRun is unavailable on Windows (no /dev/tty + PTY semantics here yet),
// so in-TUI password entry falls back to the terminal-based key setup ([K]).
func startPTYRun(ch chan progressEvent, password, sub string, args ...string) {
	_ = password
	_ = sub
	_ = args
	go func() {
		ch <- progressEvent{
			done:     true,
			out:      "In-TUI password entry isn't available on Windows yet.\nPress [K] to finish key setup in a terminal instead.",
			needsKey: true,
		}
	}()
}
