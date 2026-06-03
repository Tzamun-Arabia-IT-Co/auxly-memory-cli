package tui

import (
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
)

func TestActivityTypeClassifies(t *testing.T) {
	cases := []struct {
		entry audit.Entry
		want  string
	}{
		{audit.Entry{AgentID: "auxly-organize", Action: "write"}, "Memory Org"},
		{audit.Entry{AgentID: "claude", Action: "write"}, "Write"},
		{audit.Entry{AgentID: "human", Action: "approve"}, "Approval"},
		{audit.Entry{AgentID: "human", Action: "reject"}, "Approval"},
		{audit.Entry{Action: "disconnect"}, "Session"},
		{audit.Entry{Action: "initialize"}, "Session"},
		{audit.Entry{Action: "custom"}, "Custom"},
		{audit.Entry{Action: ""}, "Event"},
	}
	for _, c := range cases {
		if got, _ := activityType(c.entry); got != c.want {
			t.Errorf("activityType(%+v) = %q, want %q", c.entry, got, c.want)
		}
	}
}

// TestAuditTableHasTypeColumnAndStaysAligned verifies the new Type column is present
// in the header, Memory Org rows are tagged, and every rendered row has an identical
// visible width so the box borders line up.
func TestAuditTableHasTypeColumnAndStaysAligned(t *testing.T) {
	entries := []audit.Entry{
		{Timestamp: "2026-06-03T16:56:40Z", AgentID: "auxly-organize", Provider: "codex", Action: "write", File: "business.md", Diff: "+ Fundraising line\n", Reason: "On-demand memory organization"},
		{Timestamp: "2026-06-03T16:55:54Z", AgentID: "codex", Provider: "codex", Action: "disconnect", Reason: "Activity log"},
		{Timestamp: "2026-06-03T16:49:34Z", AgentID: "human", Provider: "user", Action: "approve", Reason: "Approved pending change"},
	}
	out := renderTable(entries, 0, 0, len(entries), 140)

	if !strings.Contains(out, "Type") {
		t.Error("table header missing the Type column")
	}
	for _, w := range []string{"Memory Org", "Session", "Approval"} {
		if !strings.Contains(out, w) {
			t.Errorf("table missing type tag %q", w)
		}
	}

	// All non-empty lines must share one visible width (aligned borders).
	var width int
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		vw := visibleWidth(stripANSI(line))
		if width == 0 {
			width = vw
			continue
		}
		if vw != width {
			t.Errorf("misaligned row: width %d != %d for %q", vw, width, stripANSI(line))
		}
	}
}

// TestAuditFilterCyclesAndFilters verifies the 'f' key cycles the Type filter and
// that applyFilter narrows the visible entries to the selected type.
func TestAuditFilterCyclesAndFilters(t *testing.T) {
	m := newAuditTrailModel(nil)
	m.allEntries = []audit.Entry{
		{AgentID: "auxly-organize", Action: "write", File: "business.md"},
		{AgentID: "claude", Action: "write", File: "infra.md"},
		{Action: "disconnect"},
		{AgentID: "human", Action: "approve"},
	}
	m.applyFilter()
	if len(m.entries) != 4 {
		t.Fatalf("filterAll should show all 4 entries, got %d", len(m.entries))
	}

	// f → Memory Org (only the auxly-organize write).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if m.filter != filterMemoryOrg {
		t.Fatalf("first f should select Memory Org, got %v", m.filter.label())
	}
	if len(m.entries) != 1 || m.entries[0].File != "business.md" {
		t.Fatalf("Memory Org filter should show only business.md, got %d entries", len(m.entries))
	}

	// f → Writes (the non-organize write only; Memory Org is classified separately).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if m.filter != filterWrites || len(m.entries) != 1 || m.entries[0].File != "infra.md" {
		t.Fatalf("Writes filter should show only infra.md, got filter=%v n=%d", m.filter.label(), len(m.entries))
	}

	// Cursor must clamp into the filtered range.
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		t.Errorf("cursor %d out of range after filtering", m.cursor)
	}
}
