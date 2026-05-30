//go:build !windows

package tui

import (
	"bytes"
	"io"
	"os/exec"
	"strings"

	"github.com/creack/pty"
)

// startPTYRun spawns `auxly connect <args>` attached to a pseudo-terminal so the
// SSH password prompt — which ssh reads from /dev/tty, not stdin — can be answered
// from INSIDE the TUI. It streams output lines into ch (for live progress), watches
// for "password:" and writes the user's password (ssh disables echo, so it never
// appears in the stream).
func startPTYRun(ch chan progressEvent, password, sub string, args ...string) {
	if sub == "" {
		sub = "connect"
	}
	go func() {
		c := exec.Command(exePath(), append([]string{sub}, args...)...)
		ptmx, err := pty.Start(c)
		if err != nil {
			ch <- progressEvent{done: true, err: err, out: "Could not allocate a PTY: " + err.Error()}
			return
		}
		defer func() { _ = ptmx.Close() }()

		var all bytes.Buffer
		var line bytes.Buffer
		sent := 0
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				all.Write(buf[:n])
				// Answer each "password:" prompt (initial + retries) once.
				if count := strings.Count(strings.ToLower(all.String()), "password:"); count > sent {
					_, _ = io.WriteString(ptmx, password+"\n")
					sent = count
				}
				for _, b := range buf[:n] {
					switch b {
					case '\n':
						ch <- progressEvent{line: line.String()}
						line.Reset()
					case '\r':
						// ignore carriage returns
					default:
						line.WriteByte(b)
					}
				}
			}
			if rerr != nil {
				break
			}
		}
		if line.Len() > 0 {
			ch <- progressEvent{line: line.String()}
		}
		werr := c.Wait()
		out := all.String()
		ch <- progressEvent{done: true, err: werr, out: out, needsKey: strings.Contains(out, "AUXLY_KEY_REQUIRED")}
	}()
}
