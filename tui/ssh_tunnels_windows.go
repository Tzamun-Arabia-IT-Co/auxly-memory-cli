//go:build windows

package tui

import (
	"os/exec"
	"strings"
)

// hostTunnelsLive reports whether at least one reverse-tunnel ssh.exe process
// (`ssh -N -T … -R <port>:localhost:<port>`, spawned by superviseTunnel) is
// running. Windows has no pgrep, so enumerate ssh.exe command lines via CIM and
// match the reverse-forward spec directly. The argv0 "ssh" that host.go passes
// resolves to ssh.exe, whose process image name CIM reports as "ssh.exe".
func hostTunnelsLive() bool {
	script := `Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" | ` +
		`ForEach-Object { $_.CommandLine }`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		// tunnelArgs emits "-R" and "<port>:localhost:<port>" as separate argv
		// tokens, so on the reconstructed command line they are space-separated.
		// Unrelated ssh sessions won't contain ":localhost:", so no false positives.
		if strings.Contains(line, ":localhost:") && strings.Contains(line, "-R") {
			return true
		}
	}
	return false
}
