//go:build !windows

package tui

import (
	"os/exec"
	"strings"
)

// hostTunnelsLive reports whether at least one reverse-tunnel process
// (`ssh … -R <port>:localhost:…`) is currently running on this machine. See the
// doc comment in ssh.go for why this is split per-OS.
func hostTunnelsLive() bool {
	out, err := exec.Command("pgrep", "-f", "ssh.*-R [0-9].*:localhost:").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
