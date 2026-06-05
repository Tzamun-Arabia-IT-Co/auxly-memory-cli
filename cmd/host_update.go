package cmd

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/spf13/cobra"
)

// clientVersionStatus is one connected box's update status, emitted by
// `auxly host versions` (and consumed as JSON by the TUI Remote tab + Dashboard).
type clientVersionStatus struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Version   string `json:"version"` // the box's auxly version; "" if unreachable
	Latest    string `json:"latest"`  // newest published release
	Outdated  bool   `json:"outdated"`
	Live      bool   `json:"live"`      // serving a live session right now
	Reachable bool   `json:"reachable"` // SSH + `auxly --version` succeeded
}

// probeClientVersions SSHes every connected box CONCURRENTLY and reports each
// one's auxly version against the latest published release. Network-bound but
// parallel, so the whole sweep is ~one SSH round-trip, not N. Order matches
// loadClients().
func probeClientVersions() []clientVersionStatus {
	clients, _ := loadClients()
	if len(clients) == 0 {
		return nil // no boxes → no SSH, no /version network call (stays local-first)
	}
	latest, _ := update.Latest() // "" on failure → nothing is flagged outdated
	live := liveRemoteHosts()

	out := make([]clientVersionStatus, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := clientVersionStatus{Name: c.Name, Target: c.Target, Latest: latest, Live: clientIsLive(live, c)}
			if p, err := clientProfile(c); err == nil {
				if banner, verr := runSSH(p, "auxly", "--version"); verr == nil {
					st.Version = parseRemoteVersion(banner)
					st.Reachable = st.Version != ""
					st.Outdated = remoteNeedsUpdate(st.Version, latest, true)
				}
			}
			out[i] = st
		}()
	}
	wg.Wait()
	return out
}

var hostVersionsJSON bool

var hostVersionsCmd = &cobra.Command{
	Use:          "versions",
	Short:        "Show each connected box's auxly version and whether an update is available",
	SilenceUsage: true,
	RunE:         runHostVersions,
}

func runHostVersions(cmd *cobra.Command, args []string) error {
	statuses := probeClientVersions()
	if hostVersionsJSON {
		b, err := json.Marshal(statuses)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if len(statuses) == 0 {
		fmt.Println("No connected boxes.")
		return nil
	}
	for _, s := range statuses {
		switch {
		case !s.Reachable:
			fmt.Printf("  • %-20s %-22s unreachable\n", s.Name, s.Target)
		case s.Outdated:
			live := ""
			if s.Live {
				live = " (live — will update on next idle)"
			}
			fmt.Printf("  • %-20s %-22s ⬆ %s → %s%s\n", s.Name, s.Target, s.Version, s.Latest, live)
		default:
			fmt.Printf("  • %-20s %-22s ✓ %s (current)\n", s.Name, s.Target, s.Version)
		}
	}
	return nil
}

var hostUpdateForce bool

var hostUpdateCmd = &cobra.Command{
	Use:          "update [name|--all]",
	Short:        "Update a connected box's auxly over SSH (skips boxes serving a live session)",
	SilenceUsage: true,
	RunE:         runHostUpdate,
}

var hostUpdateAll bool

func runHostUpdate(cmd *cobra.Command, args []string) error {
	clients, err := loadClients()
	if err != nil {
		return err
	}
	if len(clients) == 0 {
		return fmt.Errorf("no connected boxes to update")
	}
	live := liveRemoteHosts()
	latest, lerr := update.Latest()
	if lerr != nil || latest == "" {
		return fmt.Errorf("could not determine the latest version (offline?): %v", lerr)
	}

	if hostUpdateAll {
		// --force with --all also updates LIVE boxes (ending their session); without
		// it, live boxes are skipped as usual. This is what the dashboard's [f] runs.
		updated, skipped := 0, 0
		for _, c := range clients {
			done := updateOneClient(c, latest, live, hostUpdateForce)
			if done {
				updated++
			} else {
				skipped++
			}
		}
		fmt.Printf("\n✓ Updated %d box(es); skipped %d (current, live, or unreachable).\n", updated, skipped)
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("name required (or pass --all)")
	}
	var target *clientEntry
	for i := range clients {
		if clients[i].Name == args[0] {
			target = &clients[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no connected box named %q", args[0])
	}
	updateOneClient(*target, latest, live, hostUpdateForce)
	return nil
}

// updateOneClient bumps one box over SSH when it is outdated and idle. It returns
// whether an update was actually applied. The live-session guard is skipped when
// force is set (an explicit single-box request). Best-effort + narrated.
func updateOneClient(c clientEntry, latest string, live map[string]bool, force bool) bool {
	p, err := clientProfile(c)
	if err != nil {
		fmt.Printf("   ⚠ %s: bad target %q: %v\n", c.Name, c.Target, err)
		return false
	}
	banner, verr := runSSH(p, "auxly", "--version")
	if verr != nil {
		fmt.Printf("   ⚠ %s unreachable — skipped\n", c.Name)
		return false
	}
	ver := parseRemoteVersion(banner)

	if !remoteNeedsUpdate(ver, latest, true) {
		// Already current: no binary change, but STILL (re)apply the statusline +
		// usage preference. A box wired before `--enable-usage` existed would never
		// show its usage line otherwise, since the update path is skipped for it.
		fmt.Printf("   ✓ %s already current (%s)\n", c.Name, ver)
		ensureBoxStatusline(p, c.Name)
		return false
	}
	if clientIsLive(live, c) && !force {
		// Don't swap a live box's binary — but the statusline config edit is
		// non-disruptive (the agent re-reads it on its next render), so still apply.
		fmt.Printf("   ⏭  %s is serving a live session (%s → %s) — binary update skipped; retry when idle or use --force\n", c.Name, ver, latest)
		ensureBoxStatusline(p, c.Name)
		return false
	}
	fmt.Printf("   ⬆ Updating %s (%s → %s)...\n", c.Name, ver, latest)
	if uerr := updateRemoteAuxly(p); uerr != nil {
		fmt.Printf("   ⚠ %s update failed: %v\n", c.Name, uerr)
		return false
	}
	fmt.Printf("   ✓ %s updated to %s\n", c.Name, latest)
	ensureBoxStatusline(p, c.Name)
	return true
}

var hostStatuslineAll bool

var hostStatuslineCmd = &cobra.Command{
	Use:          "statusline [name]",
	Short:        "Push this host's statusline preference (mode + usage) to a connected box, or --all",
	SilenceUsage: true,
	RunE:         runHostStatusline,
}

// runHostStatusline pushes the host's statusline preference to box(es) WITHOUT a
// version bump — the "sync now" engine for Settings → Customizations. Config edits
// are non-disruptive (the agent re-reads on its next render), so live boxes are fine.
func runHostStatusline(cmd *cobra.Command, args []string) error {
	clients, err := loadClients()
	if err != nil {
		return err
	}
	if len(clients) == 0 {
		return fmt.Errorf("no connected boxes")
	}
	targets := clients
	if !hostStatuslineAll {
		if len(args) == 0 {
			return fmt.Errorf("a box name is required (or pass --all)")
		}
		// Accept one or more names so the TUI auto-sync can push to a selected subset.
		byName := make(map[string]clientEntry, len(clients))
		for _, c := range clients {
			byName[c.Name] = c
		}
		targets = nil
		for _, name := range args {
			c, ok := byName[name]
			if !ok {
				fmt.Printf("   ⚠ no connected box named %q — skipped\n", name)
				continue
			}
			targets = append(targets, c)
		}
		if len(targets) == 0 {
			return fmt.Errorf("none of the named boxes are connected")
		}
	}
	// Push concurrently — each box is several SSH round-trips (reachability + install,
	// plus a fallback retry on older boxes), so doing them in parallel keeps the whole
	// sweep to roughly one box's latency instead of the sum. Output is collected and
	// printed in order so the lines don't interleave.
	lines := make([]string, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, c := range targets {
		i, c := i, c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			lines[i] = syncBoxStatuslineResult(c)
		}()
	}
	wg.Wait()
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// syncBoxStatuslineResult pushes the host's statusline preference to one box and
// returns a single-line result (so concurrent callers don't interleave output).
func syncBoxStatuslineResult(c clientEntry) string {
	p, perr := clientProfile(c)
	if perr != nil {
		return fmt.Sprintf("   ⚠ %s: bad target %q: %v", c.Name, c.Target, perr)
	}
	if _, verr := runSSH(p, "auxly", "--version"); verr != nil {
		return fmt.Sprintf("   ⚠ %s unreachable — skipped", c.Name)
	}
	res, serr := installRemoteStatusline(p)
	if serr != nil {
		return fmt.Sprintf("   • %s: statusline not applied (older box, or no scriptable agent)", c.Name)
	}
	return statuslineSyncMessage(c.Name, res, config.LoadSettings().LiveUsage)
}

// ensureBoxStatusline applies the host's statusline preference (wrap-vs-replace
// mode + Live Usage) on a box, regardless of whether its binary was just updated.
// Best-effort + narrated; requires the box on a build that supports it (≥1.0.9).
func ensureBoxStatusline(p remoteProfile, name string) {
	res, serr := installRemoteStatusline(p)
	if serr != nil {
		fmt.Printf("   • statusline not applied on %s (older box, or no scriptable agent)\n", name)
		return
	}
	fmt.Println(statuslineSyncMessage(name, res, config.LoadSettings().LiveUsage))
}
