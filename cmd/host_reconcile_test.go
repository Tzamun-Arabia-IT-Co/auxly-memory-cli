package cmd

import (
	"sort"
	"testing"
)

// TestReconcileRelaysStartsAndStops is the core guard for the hot-reload
// supervisor: adding/removing a relay must yield ONLY the delta — never touch
// the unchanged tunnels (which is what used to drop every box at once).
func TestReconcileRelaysStartsAndStops(t *testing.T) {
	active := map[string]bool{"A": true, "B": true} // two tunnels running
	want := map[string]bool{"B": true, "C": true}   // A removed, C added, B kept

	start, stop := reconcileRelays(active, want)
	sort.Strings(start)
	sort.Strings(stop)

	if len(start) != 1 || start[0] != "C" {
		t.Errorf("start = %v, want [C] (only the new relay)", start)
	}
	if len(stop) != 1 || stop[0] != "A" {
		t.Errorf("stop = %v, want [A] (only the removed relay)", stop)
	}
	// B is in both sets → must be in NEITHER (its tunnel stays untouched).
	for _, k := range append(start, stop...) {
		if k == "B" {
			t.Fatalf("unchanged relay B was disturbed (start=%v stop=%v)", start, stop)
		}
	}
}

// TestReconcileRelaysNoChange verifies a reload with identical config is a
// complete no-op — no tunnel is bounced when nothing changed.
func TestReconcileRelaysNoChange(t *testing.T) {
	active := map[string]bool{"A": true, "B": true}
	want := map[string]bool{"A": true, "B": true}

	start, stop := reconcileRelays(active, want)
	if len(start) != 0 || len(stop) != 0 {
		t.Errorf("identical config should be a no-op, got start=%v stop=%v", start, stop)
	}
}

// TestReconcileRelaysEmptyWantStopsAll verifies that downing the host (no
// relays wanted) cancels every running tunnel.
func TestReconcileRelaysEmptyWantStopsAll(t *testing.T) {
	active := map[string]bool{"A": true, "B": true}
	want := map[string]bool{}

	start, stop := reconcileRelays(active, want)
	if len(start) != 0 {
		t.Errorf("start = %v, want none", start)
	}
	if len(stop) != 2 {
		t.Errorf("stop = %v, want both relays cancelled", stop)
	}
}

// TestRelayKeyDistinctPerRelay guards that distinct relays get distinct keys
// (so they're supervised independently) and identical ones collide (so a reload
// recognises an unchanged relay and leaves it running).
func TestRelayKeyDistinctPerRelay(t *testing.T) {
	a := hostConfig{Rendezvous: "root@10.0.0.1", ReversePort: 2222, LocalSSHPort: 22}
	b := hostConfig{Rendezvous: "root@10.0.0.2", ReversePort: 2222, LocalSSHPort: 22}
	aAgain := hostConfig{Rendezvous: "root@10.0.0.1", ReversePort: 2222, LocalSSHPort: 22}

	if relayKey(a) == relayKey(b) {
		t.Errorf("distinct relays share a key: %q", relayKey(a))
	}
	if relayKey(a) != relayKey(aAgain) {
		t.Errorf("identical relays got different keys: %q vs %q", relayKey(a), relayKey(aAgain))
	}
}
