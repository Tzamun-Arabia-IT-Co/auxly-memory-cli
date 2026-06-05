package tui

import (
	"strings"
	"testing"
)

// TestClientsCountedWhenHostDown is the regression guard for the "deleted boxes
// linger invisibly" bug: configured clients must stay counted (and therefore
// selectable/removable) even when the host tunnel is down (hostOK == false).
func TestClientsCountedWhenHostDown(t *testing.T) {
	m := sshModel{
		hostOK: false, // host tunnel is DOWN
		clients: []clientRow{
			{Name: "ERPAI", Target: "root@192.168.1.168", Method: "relay"},
			{Name: "MM", Target: "root@192.168.1.166", Method: "relay"},
		},
		width: 100,
	}

	if got := m.clientCount(); got != 2 {
		t.Fatalf("clientCount() with host down = %d, want 2 (clients must stay selectable)", got)
	}
	if got := m.listLen(); got != 2 {
		t.Fatalf("listLen() = %d, want 2", got)
	}
	// Cursor on the first client must resolve to that client, not fall through.
	m.cursor = 0
	if c, ok := m.cursorOnClient(); !ok || c.Name != "ERPAI" {
		t.Fatalf("cursorOnClient() = (%v, %v), want (ERPAI, true)", c.Name, ok)
	}
}

// TestClientsRenderedWhenHostDown verifies the boxes are actually drawn (not
// hidden) when the host is down, with a warning that the tunnel is down.
func TestClientsRenderedWhenHostDown(t *testing.T) {
	m := sshModel{
		hostOK: false,
		clients: []clientRow{
			{Name: "ERPAI", Target: "root@192.168.1.168", Method: "relay"},
		},
		width:  100,
		height: 40,
	}
	out := m.View()
	if !strings.Contains(out, "CONNECTED BOXES") {
		t.Errorf("host down + clients present: expected CONNECTED BOXES section to render, it did not")
	}
	if !strings.Contains(out, "ERPAI") {
		t.Errorf("host down: expected the ERPAI box row to be visible")
	}
	if !strings.Contains(out, "host tunnel down") {
		t.Errorf("host down: expected a 'host tunnel down' warning")
	}
}
