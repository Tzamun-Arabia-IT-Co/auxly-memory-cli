package cmd

import (
	"strings"
	"testing"
)

// TestTunnelKillPatterns locks the safety contract of the `host down` reaper:
// the supervisor pattern is the FULL "auxly host tunnel" argv (never a bare
// "host tunnel" substring that a stray grep could match), and each ssh pattern
// pins BOTH the reverse port and the local port so it can't collide with an
// unrelated `ssh -R 2222:localhost:...` (2222 being a common Vagrant/Docker
// default as well as auxly's own).
func TestTunnelKillPatterns(t *testing.T) {
	relays := []hostConfig{
		{Rendezvous: "relay1", ReversePort: 2222, LocalSSHPort: 2201},
		{Rendezvous: "relay2", ReversePort: 3333}, // no LocalSSHPort → defaultSSHPort
		{Rendezvous: "bad", ReversePort: 0},       // skipped
	}
	got := tunnelKillPatterns(relays)

	if got[0] != "auxly host tunnel" {
		t.Fatalf("supervisor pattern must be the full anchored argv, got %q", got[0])
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "-R 2222:localhost:2201") {
		t.Errorf("ssh pattern must pin reverse AND local port, missing 2222:localhost:2201 in %v", got)
	}
	// relay2 has no LocalSSHPort, so it must fall back to defaultSSHPort — never
	// an open-ended "-R 3333:localhost:" that matches any local port.
	if !strings.Contains(joined, "-R 3333:localhost:") || strings.Contains(joined, "-R 3333:localhost:\n") {
		t.Errorf("relay2 pattern must carry a concrete local port, got %v", got)
	}
	for _, p := range got {
		if strings.HasSuffix(p, "localhost:") {
			t.Errorf("pattern %q ends open-ended (no local port) — too broad to safely kill", p)
		}
	}
	// ReversePort 0 must not produce a pattern.
	if len(got) != 3 { // supervisor + relay1 + relay2
		t.Fatalf("expected 3 patterns (supervisor + 2 valid relays), got %d: %v", len(got), got)
	}
}
