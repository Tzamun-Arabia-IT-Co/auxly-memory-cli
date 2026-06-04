package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// clientVersionStatus mirrors the JSON emitted by `auxly host versions --json`.
// The Remote tab and Dashboard exec that command (no import cycle) and decode
// this to drive the per-box "update available" badge and the update prompt.
type clientVersionStatus struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Version   string `json:"version"`
	Latest    string `json:"latest"`
	Outdated  bool   `json:"outdated"`
	Live      bool   `json:"live"`
	Reachable bool   `json:"reachable"`
}

// remoteVersionsMsg delivers a completed version sweep into a model's Update.
type remoteVersionsMsg struct {
	statuses []clientVersionStatus
}

// probeRemoteVersionsCmd runs `auxly host versions --json` off the UI thread and
// returns the parsed statuses. It is SSH-bound (one round-trip per box, run
// concurrently by the command) so it must never be called on a hot tick — only on
// screen-enter, after an update, or on an explicit refresh. A failure yields an
// empty sweep (no badges) rather than an error.
func probeRemoteVersionsCmd() tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command(exePath(), "host", "versions", "--json").Output()
		if err != nil {
			return remoteVersionsMsg{}
		}
		var statuses []clientVersionStatus
		if json.Unmarshal(out, &statuses) != nil {
			return remoteVersionsMsg{}
		}
		return remoteVersionsMsg{statuses: statuses}
	}
}

// boxesUpdatedMsg reports the outcome of a one-key "update all boxes" sweep.
type boxesUpdatedMsg struct {
	summary string
	err     error
}

// updateAllBoxesCmd runs `auxly host update --all` off the UI thread: it bumps
// every connected box that is outdated and idle, skipping live ones. Returns the
// command's final summary line for a compact dashboard notice.
func updateAllBoxesCmd() tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command(exePath(), "host", "update", "--all").CombinedOutput()
		return boxesUpdatedMsg{summary: lastNonEmptyLine(string(out)), err: err}
	}
}

// lastNonEmptyLine returns the final non-blank line of s (the summary), trimmed.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// versionsByName keys a sweep by lowercased box name for O(1) row lookup.
func versionsByName(statuses []clientVersionStatus) map[string]clientVersionStatus {
	m := make(map[string]clientVersionStatus, len(statuses))
	for _, s := range statuses {
		m[strings.ToLower(s.Name)] = s
	}
	return m
}

// outdatedCount returns how many boxes in a sweep are reachable and behind.
func outdatedCount(statuses []clientVersionStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Outdated {
			n++
		}
	}
	return n
}

// versionCell renders a probed box's version for the Remote row and classifies
// it so the caller can colour it: kind is "outdated" (show current→latest),
// "current" (✓ on the latest), or "unreachable". An empty text/kind means the box
// hasn't been probed yet. Showing the actual version for EVERY box — not just a
// nudge for outdated ones — is what makes the list answer "what's each box on?".
func versionCell(st clientVersionStatus) (text, kind string) {
	switch {
	case st.Outdated && st.Version != "" && st.Latest != "":
		return fmt.Sprintf("%s ⬆%s", st.Version, st.Latest), "outdated"
	case st.Reachable && st.Version != "":
		return "✓ " + st.Version, "current"
	case !st.Reachable:
		return "unreachable", "unreachable"
	}
	return "", ""
}

// updateResultKind classifies the captured output of a `host update` run so the
// result panel can show the TRUE outcome instead of a blanket "✓ Done" — a skip
// (live box) and an already-current box both exit 0. Returns "updated", "failed",
// "skipped", "current", or "" (unrecognised). "updated" wins when anything was
// actually bumped (e.g. an update-all summary with a non-zero count).
func updateResultKind(lines []string) string {
	kind := ""
	for _, l := range lines {
		switch {
		case strings.Contains(l, "updated to"):
			return "updated"
		case strings.Contains(l, "Updated ") && !strings.Contains(l, "Updated 0 "):
			return "updated"
		case strings.Contains(l, "update failed:"):
			kind = "failed"
		case strings.Contains(l, "unreachable — skipped"):
			if kind == "" {
				kind = "failed"
			}
		case strings.Contains(l, "serving a live session"):
			if kind == "" {
				kind = "skipped"
			}
		case strings.Contains(l, "already current"):
			if kind == "" {
				kind = "current"
			}
		}
	}
	return kind
}

// permissionLabel describes a connected box's effective memory access for the
// Remote list. defaultWrite is the opt-in config (DefaultRemoteWrite): when on, a
// box with no explicit per-file or legacy write grant is shown as read+write.
// Returns (label, isWrite) so the caller can colour write green, read-only dim.
func permissionLabel(c clientRow, defaultWrite bool) (string, bool) {
	switch {
	case len(c.WriteFiles) > 0:
		return fmt.Sprintf("read+write·%df", len(c.WriteFiles)), true
	case c.Access == "write":
		return "read+write", true
	case defaultWrite:
		return "read+write*", true
	default:
		return "read-only", false
	}
}
