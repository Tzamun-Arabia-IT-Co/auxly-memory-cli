package cmd

import (
	"strings"
	"testing"
)

// TestHostTopologyWarnings locks the duplicate/orphan detection classes.
func TestHostTopologyWarnings(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic clients.yaml (empty)
	relays := []hostConfig{{Rendezvous: "relay@vps.example.com", ReversePort: 2222}}
	warnings := hostTopologyWarnings(relays)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "orphan tunnel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unreferenced relay not flagged: %v", warnings)
	}
}
