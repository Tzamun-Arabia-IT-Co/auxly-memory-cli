package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
)

// connectUpdateRemote is the per-invocation override set by the --update-remote
// flag on the connect commands. When false, the persisted
// Settings.UpdateRemotesOnConnect opt-in decides.
var connectUpdateRemote bool

// remoteVersionRe matches the first semver-looking token in an `auxly --version`
// banner (e.g. "🧠 Auxly-Memory CLI Version: 1.0.8").
var remoteVersionRe = regexp.MustCompile(`[0-9]+\.[0-9]+(?:\.[0-9]+){0,2}`)

// parseRemoteVersion pulls the version out of an `auxly --version` banner.
// Returns "" when no version token is present (e.g. a "command not found" error).
func parseRemoteVersion(out string) string {
	return remoteVersionRe.FindString(out)
}

// remoteNeedsUpdate reports whether a remote running remoteVer should be bumped:
// the caller opted in, both versions are known, and latest is strictly newer.
func remoteNeedsUpdate(remoteVer, latest string, optIn bool) bool {
	if !optIn || remoteVer == "" || latest == "" {
		return false
	}
	return update.IsNewer(latest, remoteVer)
}

// remoteUpdateOptIn resolves the opt-in: the --update-remote flag wins, otherwise
// the persisted UpdateRemotesOnConnect setting (default off). Both default to a
// no-op so connect never mutates a remote binary unless explicitly asked.
func remoteUpdateOptIn() bool {
	if connectUpdateRemote {
		return true
	}
	return config.LoadSettings().UpdateRemotesOnConnect
}

// remoteHasLiveSession reports whether the remote is currently serving an auxly
// mcp-server (a live relay session). Best-effort: a probe error is treated as
// "not live". The result is the live-box guard — we never replace a binary out
// from under an active session.
func remoteHasLiveSession(p remoteProfile) bool {
	fam, _, err := detectRemoteOS(p)
	if err != nil {
		return false
	}
	posix := `pgrep -f "auxly mcp-server" >/dev/null 2>&1 && echo LIVE || true`
	powershell := `if(Get-CimInstance Win32_Process -Filter "name='auxly.exe'" | Where-Object {$_.CommandLine -like '*mcp-server*'}){'LIVE'}`
	out, err := runRemoteScript(p, fam, posix, powershell)
	if err != nil {
		return false
	}
	return strings.Contains(out, "LIVE")
}

// updateRemoteAuxly bumps auxly on the remote in place via the public installer,
// over the same SSH transport the doctor already uses.
func updateRemoteAuxly(p remoteProfile) error {
	fam, _, err := detectRemoteOS(p)
	if err != nil {
		return err
	}
	posix := "curl -fsSL " + remoteInstallURL + " | sh"
	powershell := winInstallCmd(remoteInstallPS)
	_, err = runRemoteScript(p, fam, posix, powershell)
	return err
}

// remoteStatuslineResult reports what installRemoteStatusline achieved on a box, so
// callers can narrate the truth instead of claiming a usage line the box can't show.
type remoteStatuslineResult struct {
	persisted bool // box stored Live Usage on (1.0.10+ --enable-usage) and self-refreshes
	refreshed bool // usage-cache.json was primed now, so the usage line renders immediately
}

// installRemoteStatusline wires the statusline on a box and — when THIS host has Live
// Usage on — makes the box's usage line actually render, across versions:
//   - 1.0.10+ box: passes --enable-usage so the box PERSISTS Live Usage and self-refreshes;
//     we then prime the cache once so the line shows on the very next render, not in ~3m.
//   - older box (no --enable-usage flag): installs plain, then runs the hidden
//     `statusline --refresh-usage` to populate usage-cache.json NOW. The render shows the
//     usage line whenever that cache has data (it does NOT gate on the setting), so the
//     line appears even on a box that can't persist the opt-in — it just won't self-refresh
//     until the box is updated.
func installRemoteStatusline(p remoteProfile) (res remoteStatuslineResult, err error) {
	base := []string{"statusline", "install", "--agent", "all"}
	if hostPrefersWrapStatusline() {
		base = append(base, "--wrap")
	}
	wantUsage := config.LoadSettings().LiveUsage

	if wantUsage {
		withUsage := append(append([]string{}, base...), "--enable-usage")
		if _, e := runSSH(p, append([]string{hostAuxlyBin(p)}, withUsage...)...); e == nil {
			// Box understood --enable-usage and persisted Live Usage. Prime the cache so
			// the line is visible immediately rather than after the first self-refresh.
			res.persisted = true
			res.refreshed = refreshRemoteUsage(p)
			return res, nil
		}
		// Older box: --enable-usage is an unknown flag and cobra hard-fails the whole
		// command. Fall through to a plain install, then prime the cache directly.
	}

	if _, err = runSSH(p, append([]string{hostAuxlyBin(p)}, base...)...); err != nil {
		return res, err
	}
	if wantUsage {
		// Version-agnostic usage: populate usage-cache.json now so the usage line renders
		// on this box's next statusline draw, even though it can't persist the opt-in.
		res.refreshed = refreshRemoteUsage(p)
	}
	return res, nil
}

// refreshRemoteUsage runs the box's hidden `statusline --refresh-usage` to fetch and
// persist its usage snapshot, so the usage line renders on the next draw. Best-effort:
// the command exits 0 even when the box has no fetchable agent token (the cache then
// stays empty and no line shows), so a true result means "refresh ran", not "usage is
// guaranteed visible". Returns false only when the box lacks the command (pre-1.0.8)
// or SSH itself failed.
func refreshRemoteUsage(p remoteProfile) bool {
	_, err := runSSH(p, hostAuxlyBin(p), "statusline", "--refresh-usage")
	return err == nil
}

// statuslineSyncMessage renders a one-line, HONEST outcome for a box statusline sync,
// given the install result and whether the host wants usage mirrored (its own Live
// Usage opt-in). Pure, so it's unit-testable without SSH.
func statuslineSyncMessage(name string, res remoteStatuslineResult, wantUsage bool) string {
	switch {
	case res.persisted:
		return fmt.Sprintf("   ✓ %s: statusline applied (mirrors your mode + Live Usage on the box)", name)
	case res.refreshed:
		return fmt.Sprintf("   ✓ %s: statusline applied (mirrors your mode + usage refreshed now — update the box to 1.0.10+ to keep it self-refreshing)", name)
	case wantUsage:
		return fmt.Sprintf("   ✓ %s: statusline applied (mirrors your mode; usage couldn't be primed — the box needs a fetchable agent token)", name)
	default:
		return fmt.Sprintf("   ✓ %s: statusline applied (mirrors your mode)", name)
	}
}

// hostPrefersWrapStatusline reports whether THIS machine's own statusline is in
// wrap mode (Auxly appended to the user's original) rather than a full replace —
// so the remote install mirrors the user's chosen style. Checks each agent and
// returns true if any is wrapped.
func hostPrefersWrapStatusline() bool {
	for _, t := range statusline.Targets() {
		if statusline.CurrentState(t.Name).Mode == statusline.ModeWrap {
			return true
		}
	}
	return false
}

// ensureRemoteCurrentAndWired is the opt-in remote-maintenance step the doctor
// runs once auxly is confirmed present on the host: update it when a newer
// release exists (skipping a host that's mid-session), then ensure the statusline
// is wired for its agents. Everything is best-effort and narrated; a failure here
// never aborts the connect — the link still works on the existing binary.
func ensureRemoteCurrentAndWired(p remoteProfile, versionBanner string, optIn bool) {
	if !optIn {
		return
	}
	remoteVer := parseRemoteVersion(versionBanner)
	latest, err := update.Latest()
	if err != nil {
		latest = "" // offline / no /version published — skip the update, still try the statusline
	}

	if remoteNeedsUpdate(remoteVer, latest, true) {
		if remoteHasLiveSession(p) {
			fmt.Printf("   ⏭  %s is on %s (newer %s available) but is serving a live session — skipped; it'll pick up the update on its next idle connect.\n", p.Host, remoteVer, latest)
		} else {
			fmt.Printf("   ⬆ Updating auxly on %s (%s → %s)...\n", p.Host, remoteVer, latest)
			if uerr := updateRemoteAuxly(p); uerr != nil {
				fmt.Printf("   ⚠ Remote update failed (continuing on %s): %v\n", remoteVer, uerr)
			} else {
				fmt.Printf("   ✓ %s updated to %s\n", p.Host, latest)
			}
		}
	}

	// Ensure the Auxly statusline on the far side for its detected agents, and prime
	// the usage line when this host shows usage (mirrors the user's preference).
	if res, serr := installRemoteStatusline(p); serr == nil {
		fmt.Println(statuslineSyncMessage(p.Host, res, config.LoadSettings().LiveUsage))
	}
}
