//go:build !windows

package tui

import (
	"bytes"
	"io"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
)

// runConnectPTY runs `auxly connect <args>` attached to a pseudo-terminal so the
// SSH password prompt — which ssh reads from /dev/tty, not stdin — can be answered
// from INSIDE the TUI. We give ssh its own PTY, watch the output for "password:",
// and write the user's password to it. All output is captured and returned as a
// sshCapturedMsg for the result pane (ssh disables echo during the read, so the
// password never appears in the captured text).
func runConnectPTY(password string, args ...string) tea.Cmd {
	bin := exePath()
	return func() tea.Msg {
		c := exec.Command(bin, append([]string{"connect"}, args...)...)
		ptmx, err := pty.Start(c)
		if err != nil {
			return sshCapturedMsg{output: "Could not allocate a PTY for the password prompt: " + err.Error(), err: err}
		}
		defer func() { _ = ptmx.Close() }()

		var buf bytes.Buffer
		sent := 0
		chunk := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(chunk)
			if n > 0 {
				buf.Write(chunk[:n])
				// Answer each "password:" prompt (initial + up to 3 retries) once.
				if count := strings.Count(strings.ToLower(buf.String()), "password:"); count > sent {
					_, _ = io.WriteString(ptmx, password+"\n")
					sent = count
				}
			}
			if rerr != nil {
				break
			}
		}
		werr := c.Wait()
		out := buf.String()
		return sshCapturedMsg{output: out, err: werr, needsKey: strings.Contains(out, "AUXLY_KEY_REQUIRED")}
	}
}
