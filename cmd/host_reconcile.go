package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
)

// clientReconcileInterval is how often the tunnel supervisor re-verifies every
// client box's wiring. Hourly: drift is rare but silent, and the repair path
// costs one SSH round-trip per box.
const clientReconcileInterval = time.Hour

// reconcileRunning guards against overlapping passes: with many clients a pass
// full of 45s probe timeouts can outlast the next hourly tick, and two passes
// repairing the same box concurrently means duplicate SSH sessions and racy
// audit entries.
var reconcileRunning sync.Mutex

// reconcileClients is the continuous version of connect-time wiring: for every
// box in clients.yaml it verifies, over the existing SSH path, that
//  1. auxly is still installed on the box,
//  2. the box's remotes.yaml still carries THIS host's entry,
//  3. that entry's host_bin still exists on THIS host,
//  4. (relay clients) the entry still points at this relay's reverse port.
//
// Any drift → re-push the current offer and run `auxly connect auto` on the
// box — the same idempotent write a manual reconnect does — with one audit
// entry per repaired box. Unreachable boxes are skipped silently (tunnel
// backoff owns that failure class). Entries belonging to OTHER hosts are never
// touched: `connect auto` only writes this host's own offer.
//
// This is what turns the "remotes: [] while serving a 5-week-stale vault"
// incident from a debugging night into a ≤1h self-repair.
// KNOWN GAP: only boxes provisioned FROM this host (clients.yaml) are covered —
// a box that self-connected via `auxly connect auto` leaves no reachable
// address here, so it cannot be probed or repaired from this side. Those boxes
// are surfaced (not silently ignored) by `auxly host clients` via the audit
// trail; Sprint 5.6's push-first connect makes host-side registration the
// default path.
func reconcileClients() {
	if os.Getenv("AUXLY_HOST_SELFHEAL") == "off" {
		return
	}
	if !reconcileRunning.TryLock() {
		return // a pass is still running — skip, don't stack
	}
	defer reconcileRunning.Unlock()

	cs, err := loadClients()
	if err != nil || len(cs) == 0 {
		return
	}
	relays, _, _ := loadHostConfigs()

	for _, c := range cs {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		h := probeClient(ctx, c)
		cancel()
		if !h.reachable {
			continue
		}

		drift := driftReason(h, relays)
		if drift == "" {
			continue
		}
		repairClientWiring(c, drift)
	}
}

// driftReason returns "" when the box's wiring is healthy, else a short
// human-readable description of what drifted (also the audit reason).
func driftReason(h clientHealth, relays []hostConfig) string {
	if !h.wired || h.remoteEntry == nil {
		return "box has no remotes.yaml entry for this host"
	}
	e := h.remoteEntry
	// (3) the entry's host_bin must exist on THIS host — it is the command the
	// box execs here over SSH.
	if strings.TrimSpace(e.HostBin) != "" && !strings.ContainsAny(e.HostBin, "$%") {
		if _, err := os.Stat(e.HostBin); err != nil {
			return fmt.Sprintf("host_bin %s no longer exists on this host", e.HostBin)
		}
	}
	// (4) relay route: the box reaches this host through the relay's reverse
	// port — a port drift (host re-setup with a new port) leaves the box dialing
	// a dead listener. Match the entry against ANY currently-published relay.
	// ponytail: direct-route matching lands with Sprint 5.6's route:direct offers.
	if e.Method == "rendezvous" {
		portOK := false
		for _, r := range relays {
			if e.Port == r.ReversePort {
				portOK = true
				break
			}
		}
		if len(relays) > 0 && !portOK {
			return fmt.Sprintf("box dials reverse port %d but this host publishes none matching", e.Port)
		}
	}
	return ""
}

// repairClientWiring re-publishes the offer and re-runs `connect auto` on the
// box — the identical idempotent wiring a manual `auxly host reconnect` does.
func repairClientWiring(c clientEntry, why string) {
	// Refresh the relay offer first so `connect auto` reads current coordinates
	// (including this binary's real path as host_bin).
	if relays, ok, _ := loadHostConfigs(); ok {
		for _, r := range relays {
			_ = writeRelayOffer(r)
		}
	}
	p, err := clientProfile(c)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Target THIS host's offer by name: a box wired to multiple memory hosts
	// has several offers on disk, and a bare `connect auto` refuses to choose.
	if out, err := runSSHCtx(ctx, withoutMux(p), "auxly", "connect", "auto", offerName()); err != nil {
		// Reachable but unrepaired must never be silent — this is the exact
		// "drift persists with no diagnostic trail" trap.
		fmt.Fprintf(os.Stderr, "⚠ re-wire of %s failed (%s): %v — %s\n", c.Name, why, err, firstLine(out))
		if logger, lerr := audit.NewLogger(getMemoryPath()); lerr == nil {
			defer logger.Close()
			logger.Log("host-tunnel", "system", "client_rewire_failed", c.Name, "",
				fmt.Sprintf("wiring drift on %s (%s) but repair failed: %v", c.Name, why, err), "auto")
		}
		return // next cycle retries
	}
	fmt.Fprintf(os.Stderr, "🔧 re-wired %s (%s)\n", c.Name, why)
	if logger, lerr := audit.NewLogger(getMemoryPath()); lerr == nil {
		defer logger.Close()
		logger.Log("host-tunnel", "system", "client_rewire", c.Name, "",
			fmt.Sprintf("wiring drift on %s: %s — re-pushed via connect auto", c.Name, why), "auto")
	}
}
