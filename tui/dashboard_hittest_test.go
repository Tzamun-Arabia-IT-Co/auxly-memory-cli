package tui

import "testing"

func TestVisibleColumnOf(t *testing.T) {
	// Border + space + 2-cell emoji + space => name begins at column 5.
	if got := visibleColumnOf("│ 🧠 Claude Desktop", "Claude Desktop"); got != 5 {
		t.Errorf("visibleColumnOf with emoji prefix = %d, want 5", got)
	}
	if got := visibleColumnOf("plain text here", "missing"); got != -1 {
		t.Errorf("visibleColumnOf(absent) = %d, want -1", got)
	}
	if got := visibleColumnOf("abc", "abc"); got != 0 {
		t.Errorf("visibleColumnOf at start = %d, want 0", got)
	}
}

func TestTabAtColumn(t *testing.T) {
	row := "  [1 Info & Diagnostics]    [2 Recent Writes]    [3 Connected]"
	c1 := visibleColumnOf(row, "[1 ")
	c2 := visibleColumnOf(row, "[2 ")
	c3 := visibleColumnOf(row, "[3 ")

	cases := []struct {
		name   string
		clickX int
		want   int
	}{
		{"left margin selects tab 0", 0, 0},
		{"on tab 1 marker", c1, 0},
		{"inside tab 1 label", c1 + 5, 0},
		{"just left of tab 2 still tab 1", c2 - 1, 0},
		{"on tab 2 marker", c2, 1},
		{"inside tab 2 label", c2 + 4, 1},
		{"on tab 3 marker", c3, 2},
		{"far right stays tab 3", c3 + 20, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tabAtColumn(row, 0, tc.clickX); got != tc.want {
				t.Errorf("tabAtColumn(clickX=%d) = %d, want %d", tc.clickX, got, tc.want)
			}
		})
	}

	if got := tabAtColumn("no markers here", 0, 3); got != -1 {
		t.Errorf("tabAtColumn(no markers) = %d, want -1", got)
	}
}

func TestAgentCardOrderStable(t *testing.T) {
	cards := agentCardOrder()
	if len(cards) != 6 {
		t.Fatalf("expected 6 agent cards, got %d", len(cards))
	}
	// The hit-tester scans rendered lines for these exact names; a rename in the
	// renderer without updating the shared list would silently break clicks.
	wantFirst := agentCard{"claude", "Claude Desktop", "🧠", "99"}
	if cards[0] != wantFirst {
		t.Errorf("first card = %+v, want %+v", cards[0], wantFirst)
	}
}
