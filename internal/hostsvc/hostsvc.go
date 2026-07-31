// Package hostsvc reports whether the Auxly host keep-alive service — the
// background service that `auxly host up` installs and `auxly host down`
// removes — is currently loaded.
//
// It lives in its own package because BOTH callers need the same answer and
// cannot share it any other way: the cmd layer prints it in `auxly host`
// status, and the TUI's Remote tab draws the serving light from it. The TUI
// cannot import cmd (cmd imports the TUI), so before this package existed the
// TUI guessed from a bare `pgrep` for `ssh … -R …:localhost:` instead. That
// scan is blind to the service, so `auxly host down` left the light green —
// and any unrelated reverse tunnel of the user's own turned it green too.
// One predicate, both callers.
package hostsvc

import (
	"os/exec"
	"runtime"
	"strings"
)

// Service identifiers, one per platform supervisor.
const (
	LaunchdLabel    = "io.auxly.host"
	SystemdUnitName = "auxly-host.service"
	WindowsTaskName = "Auxly-Host"
)

// Loaded reports whether the keep-alive service is installed and running, plus
// a short human-readable state for the status line.
func Loaded() (bool, string) {
	switch runtime.GOOS {
	case "darwin":
		if err := exec.Command("launchctl", "list", LaunchdLabel).Run(); err != nil {
			return false, "not loaded (start with `auxly host up`)"
		}
		return true, "loaded (launchd)"
	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-active", SystemdUnitName).CombinedOutput()
		state := strings.TrimSpace(string(out))
		if err != nil || state != "active" {
			if state == "" {
				state = "inactive"
			}
			return false, state + " (start with `auxly host up`)"
		}
		return true, "active (systemd --user)"
	case "windows":
		out, err := exec.Command("schtasks", "/Query", "/TN", WindowsTaskName).CombinedOutput()
		if err != nil {
			return false, "not registered (start with `auxly host up`)"
		}
		if strings.Contains(string(out), "Running") {
			return true, "running (Task Scheduler)"
		}
		return true, "registered (Task Scheduler)"
	default:
		return false, "unmanaged on this OS"
	}
}
