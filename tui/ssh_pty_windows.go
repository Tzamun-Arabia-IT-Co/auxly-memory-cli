//go:build windows

package tui

import tea "github.com/charmbracelet/bubbletea"

// runConnectPTY is unavailable on Windows (no /dev/tty + PTY semantics here yet),
// so in-TUI password entry falls back to the terminal-based key setup ([K]).
func runConnectPTY(password string, args ...string) tea.Cmd {
	_ = password
	_ = args
	return func() tea.Msg {
		return sshCapturedMsg{
			output:   "In-TUI password entry isn't available on Windows yet.\nPress [K] to finish key setup in a terminal instead.",
			needsKey: true,
		}
	}
}
