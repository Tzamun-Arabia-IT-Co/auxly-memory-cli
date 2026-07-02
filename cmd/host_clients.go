package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Clients registry — the remote boxes THIS machine (as a host) has wired to use its
// memory. Lets the user see + manage (disconnect / reconnect / rename / remove)
// every connection from one place. Stored at ~/.auxly/clients.yaml.
// ---------------------------------------------------------------------------

type clientEntry struct {
	Name   string `yaml:"name"`             // friendly label
	Target string `yaml:"target"`           // [user@]host[:port] of the box
	Method string `yaml:"method,omitempty"` // relay
	// Hostname is the box's own self-reported hostname, captured at provision
	// time. A box wired by IP/target (e.g. "BOX1" → root@10.0.0.7) reports
	// a different string as its session RemoteHost (e.g. "node-a"); storing it
	// here lets the live-status match the box to its session instead of surfacing
	// a phantom duplicate row.
	Hostname string `yaml:"hostname,omitempty"`
	// SharedFiles is the §10 per-remote file-sharing allow-list: which memory
	// files this box may READ. Empty/nil means the safe default (all non-personal
	// files). The matching reader lives in internal/sharing.
	SharedFiles []string `yaml:"shared_files,omitempty"`
	// WriteFiles is the per-file writable subset (each also in SharedFiles). It
	// must be carried here so a struct round-trip via saveClients does not silently
	// drop the per-file write grants set in the TUI sharing modal.
	WriteFiles []string `yaml:"write_files,omitempty"`
	// Access is the legacy global write flag ("read"/"write"), superseded by
	// WriteFiles but preserved for back-compat.
	Access string `yaml:"access,omitempty"`
}

// clientIsLive reports whether a configured box currently holds a live
// ssh-remote session, matching the live-host set against the box name, its
// captured hostname, or the host part of its target.
func clientIsLive(live map[string]bool, c clientEntry) bool {
	if c.Name != "" && live[strings.ToLower(c.Name)] {
		return true
	}
	if c.Hostname != "" && live[strings.ToLower(c.Hostname)] {
		return true
	}
	if _, h, _, err := parseHostSpec(c.Target); err == nil && h != "" {
		return live[strings.ToLower(h)]
	}
	return false
}

type clientsFile struct {
	Clients []clientEntry `yaml:"clients"`
}

func clientsPath() (string, error) {
	dir, err := auxlyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clients.yaml"), nil
}

func loadClients() ([]clientEntry, error) {
	path, err := clientsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f clientsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Clients, nil
}

func saveClients(cs []clientEntry) error {
	dir, err := auxlyDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(clientsFile{Clients: cs})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "clients.yaml")
	return os.WriteFile(path, data, 0600)
}

// sameClient reports whether two entries refer to the same box: either the same
// name, or the same SSH target + method. Matching on target stops a second row
// being created when the same box is re-provisioned under a different name.
func sameClient(a, b clientEntry) bool {
	if a.Name == b.Name {
		return true
	}
	return a.Target == b.Target && a.Method == b.Method
}

func upsertClient(c clientEntry) error {
	cs, _ := loadClients()
	out := make([]clientEntry, 0, len(cs)+1)
	replaced := false
	for _, e := range cs {
		if sameClient(e, c) {
			if !replaced {
				out = append(out, c) // update in place at the first match
				replaced = true
			}
			continue // drop any further duplicates of the same box
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, c)
	}
	return saveClients(out)
}

func findClient(name string) (clientEntry, bool) {
	cs, _ := loadClients()
	for _, e := range cs {
		if e.Name == name {
			return e, true
		}
	}
	return clientEntry{}, false
}

func removeClientEntry(name string) error {
	cs, _ := loadClients()
	out := make([]clientEntry, 0, len(cs))
	for _, e := range cs {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return saveClients(out)
}

// clientProfile builds an SSH profile for reaching a client box.
func clientProfile(c clientEntry) (remoteProfile, error) {
	u, h, p, err := parseHostSpec(c.Target)
	if err != nil {
		return remoteProfile{}, err
	}
	return remoteProfile{Name: c.Name, Method: "public", User: u, Host: h, Port: p}, nil
}

// ---------------------------------------------------------------------------
// Commands (also driven by the TUI Remote tab)
// ---------------------------------------------------------------------------

var hostClientsCmd = &cobra.Command{
	Use:          "clients",
	Short:        "List the remote boxes using this machine's memory",
	SilenceUsage: true,
	RunE:         runHostClients,
}

var hostDisconnectCmd = &cobra.Command{
	Use:          "disconnect <name>",
	Short:        "Remove this machine's memory wiring from a connected box (leave no trace)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHostDisconnect,
}

var hostReconnectCmd = &cobra.Command{
	Use:          "reconnect <name>",
	Short:        "Re-wire a box to this machine's memory",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHostReconnect,
}

var hostForgetCmd = &cobra.Command{
	Use:          "forget <name>",
	Short:        "Disconnect a box and drop it from the connections list",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHostForget,
}

// clientHealth is one probed row of the `auxly host clients` health table.
type clientHealth struct {
	entry        clientEntry
	reachable    bool
	version      string
	wired        bool
	remoteEntry  *remoteProfile // the box's remotes.yaml entry for THIS host (nil if none)
	lastActivity string
	detail       string // failure detail when unreachable/unwired
}

// probeClient checks one box live over SSH: reachable + auxly version in one
// round-trip, wiring (does its remotes.yaml still carry THIS host's entry) in a
// second. Bounded — a dead box costs its timeout, never a hang.
func probeClient(ctx context.Context, c clientEntry) clientHealth {
	h := clientHealth{entry: c}
	p, err := clientProfile(c)
	if err != nil {
		h.detail = err.Error()
		return h
	}
	out, err := runSSHCtx(ctx, p, "auxly", "--version")
	if err != nil {
		h.detail = firstLine(out)
		return h
	}
	h.reachable = true
	h.version = strings.TrimSpace(firstLine(out))
	// Wiring: the box's remotes.yaml must still carry this host's entry. POSIX
	// first, Windows fallback — the file lives at ~/.auxly/remotes.yaml either way.
	wout, werr := runSSHCtx(ctx, p, "cat", "$HOME/.auxly/remotes.yaml")
	if werr != nil {
		wout, werr = runSSHCtx(ctx, p, "cmd", "/c", "type", "%USERPROFILE%\\.auxly\\remotes.yaml")
	}
	if werr == nil {
		var rc remotesConfig
		if yaml.Unmarshal([]byte(wout), &rc) == nil {
			for i := range rc.Remotes {
				if rc.Remotes[i].Name == offerName() {
					h.wired = true
					h.remoteEntry = &rc.Remotes[i]
					break
				}
			}
		}
	}
	if !h.wired {
		h.detail = "remotes.yaml has no entry for this host — `auxly host reconnect " + c.Name + "`"
	}
	return h
}

func runHostClients(cmd *cobra.Command, args []string) error {
	cs, err := loadClients()
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		fmt.Println("No connected boxes yet. Set one up with `auxly host provision`.")
		return nil
	}

	fmt.Printf("Probing %d box(es)…\n\n", len(cs))
	rows := make([]clientHealth, len(cs))
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(1)
		go func(i int, c clientEntry) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			rows[i] = probeClient(ctx, c)
		}(i, c)
	}
	wg.Wait()

	// Last activity comes from the local audit trail (the box's reads/writes land
	// here) — no network involved.
	var logger *audit.Logger
	if lg, lerr := audit.NewLogger(getMemoryPath()); lerr == nil {
		logger = lg
		defer logger.Close()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "BOX\tTARGET\tREACHABLE\tAUXLY\tWIRED\tLAST ACTIVITY\n")
	problems := 0
	for _, r := range rows {
		reach, wired, version, last := "✗", "✗", "-", "-"
		if r.reachable {
			reach = "✓"
			if r.version != "" {
				version = r.version
			}
		}
		if r.wired {
			wired = "✓"
		}
		if logger != nil {
			hostKey := r.entry.Hostname
			if hostKey == "" {
				hostKey = r.entry.Name
			}
			if ts := logger.LastRemoteActivity(hostKey); ts != "" {
				if t, terr := time.Parse(time.RFC3339, ts); terr == nil {
					last = humanAge(time.Since(t)) + " ago"
				}
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.entry.Name, r.entry.Target, reach, version, wired, last)
		if !r.reachable || !r.wired {
			problems++
		}
	}
	w.Flush()
	for _, r := range rows {
		if r.detail != "" && (!r.reachable || !r.wired) {
			fmt.Printf("   ⚠ %s: %s\n", r.entry.Name, r.detail)
		}
	}
	if problems == 0 {
		fmt.Println("\n✅ all boxes healthy")
	}

	// Boxes that self-connected (`auxly connect auto`) are in no host-side
	// registry — this host has no address to probe or repair them with. Listing
	// them from the audit trail turns that blind spot into visible state.
	if logger != nil {
		seen, _ := logger.RemoteHostsSeen()
		managed := map[string]bool{}
		for _, c := range cs {
			managed[strings.ToLower(c.Name)] = true
			managed[strings.ToLower(c.Hostname)] = true
		}
		var unmanaged []string
		for host, ts := range seen {
			if !managed[host] {
				unmanaged = append(unmanaged, fmt.Sprintf("%s (last activity %s)", host, ts))
			}
		}
		if len(unmanaged) > 0 {
			sort.Strings(unmanaged)
			fmt.Println("\nSelf-connected boxes (seen in audit, not managed from this host —")
			fmt.Println("cannot be probed or auto-repaired; provision from here to manage):")
			for _, u := range unmanaged {
				fmt.Printf("   • %s\n", u)
			}
		}
	}
	return nil
}

func runHostDisconnect(cmd *cobra.Command, args []string) error {
	c, ok := findClient(args[0])
	if !ok {
		return fmt.Errorf("no connection named %q", args[0])
	}
	p, err := clientProfile(c)
	if err != nil {
		return err
	}
	fmt.Printf("🔌 Disconnecting %s (%s)...\n", c.Name, c.Target)
	out, err := runSSH(p, "auxly", "connect", "disconnect", offerName(), "--purge")
	if err != nil {
		fmt.Printf("   ⚠ remote disconnect failed: %v\n   %s\n", err, firstLine(out))
		return err
	}
	fmt.Printf("   ✓ Removed the launcher + skills on %s (no trace left)\n", c.Name)
	fmt.Println("   👉 Restart the agent on that box to drop the link.")
	return nil
}

func runHostReconnect(cmd *cobra.Command, args []string) error {
	c, ok := findClient(args[0])
	if !ok {
		return fmt.Errorf("no connection named %q", args[0])
	}
	p, err := clientProfile(c)
	if err != nil {
		return err
	}
	// The box reaches this machine's memory THROUGH this machine's reverse tunnel.
	// If that tunnel is down, the box's `connect auto` can't reach localhost:2222 and
	// reconnect fails — so bring the host keep-alive up first. This makes `[r]`
	// self-healing instead of failing precisely when the tunnel is what's broken.
	ensureHostTunnelUp()
	fmt.Printf("🔗 Reconnecting %s (%s)...\n", c.Name, c.Target)
	// Fresh connection (withoutMux): never reuse a ControlMaster that may have been
	// opened before auxly was installed on the box (its session carries the stale
	// pre-install PATH, so `auxly …` would fail with 'auxly is not recognized').
	out, err := runSSH(withoutMux(p), "auxly", "connect", "auto")
	if err != nil {
		fmt.Printf("   ⚠ remote reconnect failed: %v\n   %s\n", err, firstLine(out))
		return err
	}
	fmt.Printf("   ✓ Re-wired %s to this machine's memory\n", c.Name)
	fmt.Println("   👉 Restart the agent on that box to load the link.")
	return nil
}

// ensureHostTunnelUp brings this machine's reverse-tunnel keep-alive online if it
// isn't already, so a box being reconnected can actually reach the memory through
// it. Best-effort: it only acts when this machine is configured as a host, and never
// blocks the reconnect — a failure here is reported but the wiring proceeds (the
// tunnel may come back on its own / via `auxly host up`).
func ensureHostTunnelUp() {
	if _, ok, err := loadHostConfig(); err != nil || !ok {
		return // not a host — nothing to keep alive
	}
	if live, _ := keepAliveStatus(); live {
		return // service already loaded; launchd/systemd respawns the tunnel itself
	}
	fmt.Println("🛰️  Host tunnel was down — starting the keep-alive so the box can reach your memory...")
	if err := installKeepAlive(); err != nil {
		fmt.Printf("   ⚠ Couldn't start the host tunnel automatically (%v) — run `auxly host up` once.\n", err)
		return
	}
	fmt.Println("   ✓ Host tunnel keep-alive started")
}

// forgetDisconnectTimeout bounds the best-effort remote disconnect so a slow or
// unreachable box can never delay (or, when interrupted, prevent) the local
// removal that already happened above.
const forgetDisconnectTimeout = 15 * time.Second

func runHostForget(cmd *cobra.Command, args []string) error {
	c, ok := findClient(args[0])
	if !ok {
		return fmt.Errorf("no connection named %q", args[0])
	}
	// Persist the local removal FIRST so the delete always sticks — even if the
	// box is slow or unreachable. Everything below is best-effort cleanup; a hang
	// there must never leave a "removed" box still in clients.yaml.
	if err := removeClientEntry(c.Name); err != nil {
		return err
	}
	// Drop this box's reverse tunnel too (its relay rendezvous == the box target).
	// When other relays remain, do NOT reinstall the keep-alive — the running
	// supervisor hot-reloads host.yaml and cancels only this relay's tunnel, so
	// the sibling boxes' live sessions are never disturbed. Only tear the service
	// down when the last relay is gone.
	if remaining, rerr := removeHostConfig(c.Target); rerr == nil {
		if remaining == 0 {
			_ = uninstallKeepAlive()
		}
	}
	// Best-effort: tell the box to disconnect so we don't leave it wired. Bounded
	// by a short timeout — the local removal is already saved either way.
	if p, err := clientProfile(c); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), forgetDisconnectTimeout)
		if _, derr := runSSHCtx(ctx, p, "auxly", "connect", "disconnect", offerName(), "--purge"); derr == nil {
			fmt.Printf("   ✓ Disconnected %s\n", c.Name)
		} else {
			fmt.Printf("   ⚠ Could not reach %s to disconnect (already removed locally)\n", c.Name)
		}
		cancel()
	}
	fmt.Printf("🗑️  Removed %q from the connections list.\n", c.Name)
	return nil
}
