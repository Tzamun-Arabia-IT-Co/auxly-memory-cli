package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyRunes builds a rune key message ("j", "k", "x", …) the way the list-mode
// handler reads it (msg.String()).
func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func leftClick(y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: y}
}

// dualRoleModel reproduces the real config that exposed the bug: this machine is
// a host with two connected boxes AND still carries a consumer remote.
func dualRoleModel() sshModel {
	return sshModel{
		hostOK: true,
		clients: []clientRow{
			{Name: "SRV-A", Target: "root@10.0.0.39", Method: "relay"},
			{Name: "BOX1", Target: "root@10.0.0.147", Method: "relay", Hostname: "node-a"},
		},
		remotes: []remoteEntry{
			{Name: "10.0.0.147", Method: "lan", User: "root", Host: "10.0.0.147"},
		},
		width: 100,
	}
}

func TestListLenSpansBothLists(t *testing.T) {
	m := dualRoleModel()
	if got := m.listLen(); got != 3 {
		t.Fatalf("listLen() = %d, want 3 (2 boxes + 1 remote)", got)
	}
}

func TestCursorTargetMapping(t *testing.T) {
	m := dualRoleModel()

	cases := []struct {
		cursor     int
		wantClient string // "" if the cursor should be on a remote
		wantRemote string // "" if the cursor should be on a client
	}{
		{0, "SRV-A", ""},
		{1, "BOX1", ""},
		{2, "", "10.0.0.147"},
	}
	for _, tc := range cases {
		m.cursor = tc.cursor
		c, onClient := m.cursorOnClient()
		r, onRemote := m.cursorOnRemote()

		if tc.wantClient != "" {
			if !onClient || c.Name != tc.wantClient {
				t.Errorf("cursor %d: cursorOnClient = (%q,%v), want (%q,true)", tc.cursor, c.Name, onClient, tc.wantClient)
			}
			if onRemote {
				t.Errorf("cursor %d: cursorOnRemote should be false on a client row", tc.cursor)
			}
		}
		if tc.wantRemote != "" {
			if !onRemote || r.Name != tc.wantRemote {
				t.Errorf("cursor %d: cursorOnRemote = (%q,%v), want (%q,true)", tc.cursor, r.Name, onRemote, tc.wantRemote)
			}
			if onClient {
				t.Errorf("cursor %d: cursorOnClient should be false on a remote row", tc.cursor)
			}
			if got := m.selectedName(); got != tc.wantRemote {
				t.Errorf("cursor %d: selectedName() = %q, want %q", tc.cursor, got, tc.wantRemote)
			}
		}
	}
}

// TestKeyboardReachesStrandedRemote is the regression guard for the reported
// bug: with connected boxes present, ↓ must still walk the cursor onto the
// consumer remote so it can be acted on. Before the fix it stopped at the boxes.
func TestKeyboardReachesStrandedRemote(t *testing.T) {
	m := dualRoleModel()
	m.cursor = 0

	var cmd tea.Cmd
	m, cmd = m.Update(keyRunes("j"))
	_ = cmd
	m, _ = m.Update(keyRunes("j"))
	if m.cursor != 2 {
		t.Fatalf("after two ↓ the cursor = %d, want 2 (on the remote)", m.cursor)
	}
	r, ok := m.cursorOnRemote()
	if !ok || r.Name != "10.0.0.147" {
		t.Fatalf("cursor not on the consumer remote after navigation: (%q,%v)", r.Name, ok)
	}

	// One more ↓ must clamp at the last row, not run past it.
	m, _ = m.Update(keyRunes("j"))
	if m.cursor != 2 {
		t.Errorf("cursor overran the list: = %d, want clamp at 2", m.cursor)
	}
}

// TestRemoveTargetsRemoteNotBox proves the [x]→confirm path on the remote row
// resolves to the remote (a `connect remove`), never a connected box.
func TestRemoveTargetsRemoteNotBox(t *testing.T) {
	m := dualRoleModel()
	m.cursor = 2 // on the remote

	m, _ = m.Update(keyRunes("x"))
	if m.mode != sshModeConfirm {
		t.Fatalf("[x] on a remote should open the confirm prompt, mode = %q", m.mode)
	}
	if _, ok := m.cursorOnClient(); ok {
		t.Fatal("confirm target wrongly resolved to a connected box")
	}
	r, ok := m.cursorOnRemote()
	if !ok || r.Name != "10.0.0.147" {
		t.Fatalf("confirm target = (%q,%v), want the remote 10.0.0.147", r.Name, ok)
	}
}

// TestRemoveTargetsBoxWhenOnBox is the inverse guard: [x] on a box still removes
// the box (host forget), not a remote.
func TestRemoveTargetsBoxWhenOnBox(t *testing.T) {
	m := dualRoleModel()
	m.cursor = 0 // on a connected box

	m, _ = m.Update(keyRunes("x"))
	if m.mode != sshModeConfirm {
		t.Fatalf("[x] on a box should open the confirm prompt, mode = %q", m.mode)
	}
	c, ok := m.cursorOnClient()
	if !ok || c.Name != "SRV-A" {
		t.Fatalf("confirm target = (%q,%v), want the box SRV-A", c.Name, ok)
	}
	if _, ok := m.cursorOnRemote(); ok {
		t.Fatal("confirm target wrongly resolved to a remote")
	}
}

func TestMouseSelectsAcrossBothRegions(t *testing.T) {
	m := dualRoleModel()
	anchor := m.listAnchorY()

	// Click the first box row.
	m, _ = m.handleMouse(leftClick(anchor))
	if m.cursor != 0 {
		t.Errorf("click on first box row → cursor %d, want 0", m.cursor)
	}

	// Click the remote row: it sits below the 2 box rows + (blank + header +
	// blank) = anchor + 2 + 3.
	rAnchor := anchor + len(m.clients) + 3
	m, _ = m.handleMouse(leftClick(rAnchor))
	if m.cursor != 2 {
		t.Errorf("click on the remote row (Y=%d) → cursor %d, want 2", rAnchor, m.cursor)
	}
}

// TestPureConsumerStillWorks guards the no-host case: remotes are the only list
// and remain fully navigable.
func TestPureConsumerStillWorks(t *testing.T) {
	m := sshModel{
		hostOK: false,
		remotes: []remoteEntry{
			{Name: "alpha", Method: "lan", Host: "10.0.0.1"},
			{Name: "beta", Method: "public", Host: "example.com"},
		},
		width: 100,
	}
	if got := m.listLen(); got != 2 {
		t.Fatalf("listLen() = %d, want 2", got)
	}
	m.cursor = 1
	r, ok := m.cursorOnRemote()
	if !ok || r.Name != "beta" {
		t.Fatalf("cursorOnRemote at 1 = (%q,%v), want beta", r.Name, ok)
	}
	if _, ok := m.cursorOnClient(); ok {
		t.Error("no host configured, yet cursorOnClient reported a box")
	}
}

// TestHostWithZeroBoxes guards the host-but-no-boxes case: clientCount is 0 so
// the remote occupies cursor slot 0 and mouse maps past the placeholder line.
func TestHostWithZeroBoxes(t *testing.T) {
	m := sshModel{
		hostOK:  true,
		clients: nil,
		remotes: []remoteEntry{{Name: "10.0.0.147", Method: "lan", Host: "10.0.0.147"}},
		width:   100,
	}
	if got := m.listLen(); got != 1 {
		t.Fatalf("listLen() = %d, want 1", got)
	}
	if got := m.clientCount(); got != 0 {
		t.Fatalf("clientCount() = %d, want 0 (no boxes)", got)
	}
	m.cursor = 0
	if r, ok := m.cursorOnRemote(); !ok || r.Name != "10.0.0.147" {
		t.Fatalf("cursorOnRemote at 0 = (%q,%v), want the remote", r.Name, ok)
	}

	// Mouse: the remote sits below the 1-line "None yet" placeholder + 3 header
	// lines.
	anchor := m.listAnchorY()
	m, _ = m.handleMouse(leftClick(anchor + 1 + 3))
	if m.cursor != 0 {
		t.Errorf("click on the remote row → cursor %d, want 0", m.cursor)
	}
}
