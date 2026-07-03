package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Regression coverage for the intermittently-blank dashboard: a transient
// read hiccup (audit.Logger.Stats / memory.Store.List swallow their own
// internal errors and return a valid-looking blank/empty result rather than a
// real error) used to be applied unconditionally, wiping "Last write",
// composition bars, and the recent-writes feed until the next healthy tick —
// and a single `compact` bit gated ALL enrichment (including the "what just
// happened" feeds) at once, so a terminal that had been showing the rich
// layout could blank the feed along with the decorative charts on a later,
// otherwise-identical refresh.

// TestStatsLooksBlank locks the heuristic used to detect a hollowed-out Stats
// snapshot (the shape audit.Logger.Stats returns on an internal query error).
func TestStatsLooksBlank(t *testing.T) {
	if !statsLooksBlank(nil) {
		t.Error("nil stats must look blank")
	}
	if !statsLooksBlank(&audit.Stats{}) {
		t.Error("zero-value stats must look blank")
	}
	if statsLooksBlank(&audit.Stats{TotalEntries: 1}) {
		t.Error("a non-zero TotalEntries must not look blank")
	}
	if statsLooksBlank(&audit.Stats{LastWriteTime: time.Now().Format(time.RFC3339)}) {
		t.Error("a non-empty LastWriteTime must not look blank")
	}
}

// TestDashboardRefreshRetainsDataOnTransientEmpty is the core fix: a refresh
// message that regresses straight from populated to blank/empty (the shape a
// hiccup produces, not a real error) must be discarded in favor of the
// model's last-known-good stats/composition/recentWrites — never blank a
// section that was already showing real content. A GENUINELY fresh refresh
// (real content, or replacing an already-blank state) is still accepted, so
// the guard isn't a permanent freeze.
func TestDashboardRefreshRetainsDataOnTransientEmpty(t *testing.T) {
	populated := dashboardModel{
		stats: &audit.Stats{
			TotalEntries:  143,
			WritesToday:   2,
			LastWriteTime: time.Now().Format(time.RFC3339),
		},
		composition:  []categoryStat{{label: "infra", items: 12, size: 900}},
		recentWrites: []audit.Entry{{Provider: "claude", Action: "write", File: "infra.md"}},
	}

	hiccup := dashboardRefreshMsg{
		stats: &audit.Stats{ByProvider: map[string]int{}, ByAction: map[string]int{}},
		at:    time.Now(),
	}
	got, _ := populated.Update(hiccup)
	if got.stats == nil || got.stats.TotalEntries != 143 || got.stats.LastWriteTime == "" {
		t.Errorf("a blank/hiccup refresh must not wipe stats, got %+v", got.stats)
	}
	if len(got.composition) != 1 {
		t.Errorf("a blank/hiccup refresh must not wipe composition, got %+v", got.composition)
	}
	if len(got.recentWrites) != 1 {
		t.Errorf("a blank/hiccup refresh must not wipe recentWrites, got %+v", got.recentWrites)
	}

	fresh := dashboardRefreshMsg{
		stats:        &audit.Stats{TotalEntries: 200, LastWriteTime: time.Now().Format(time.RFC3339)},
		composition:  []categoryStat{{label: "daily", items: 5}},
		recentWrites: []audit.Entry{{Provider: "codex", Action: "write", File: "daily.md"}},
		at:           time.Now(),
	}
	got2, _ := got.Update(fresh)
	if got2.stats.TotalEntries != 200 {
		t.Errorf("a genuinely fresh refresh must be accepted, got %+v", got2.stats)
	}
	if len(got2.composition) != 1 || got2.composition[0].label != "daily" {
		t.Errorf("a genuinely fresh composition must be accepted, got %+v", got2.composition)
	}
	if len(got2.recentWrites) != 1 || got2.recentWrites[0].File != "daily.md" {
		t.Errorf("a genuinely fresh recentWrites must be accepted, got %+v", got2.recentWrites)
	}

	// A blank model accepting a blank refresh is a no-op, not a lockout — this is
	// what a genuinely empty, freshly-opened vault looks like.
	blankModel := dashboardModel{stats: &audit.Stats{}}
	got3, _ := blankModel.Update(dashboardRefreshMsg{stats: &audit.Stats{TotalEntries: 5}, at: time.Now()})
	if got3.stats.TotalEntries != 5 {
		t.Errorf("a blank model must accept its first real data, got %+v", got3.stats)
	}
}

// TestDashboardFeedSurvivesHeightPressure locks the reordered enrichment
// priority: the "what just happened" feeds (Recent Memory Changes, Live
// Activity) must be the LAST thing dropped under height pressure — the
// sparkline/write-bars charts drop first (already gated to the tallest
// enrichment tier), then composition detail (compact mode), and only then,
// if there is truly no room left, the feed.
func TestDashboardFeedSurvivesHeightPressure(t *testing.T) {
	writeEntry := audit.Entry{Timestamp: time.Now().Format(time.RFC3339), Provider: "claude", Action: "write", File: "infra.md", Diff: "+ x\n"}
	feedEvent := audit.ActivityEvent{ID: 1, TS: time.Now(), Provider: "claude", Action: "write", File: "infra.md"}

	newSmallDashboard := func(t *testing.T) model {
		t.Helper()
		m := *NewApp(t.TempDir())
		m.dashboard.stats = &audit.Stats{WritesToday: 2, TotalEntries: 40, LastWriteTime: time.Now().Format(time.RFC3339)}
		m.dashboard.cards = []agentCard{{id: "claude", name: "Claude Desktop"}, {id: "cursor", name: "Cursor"}}
		m.dashboard.sessions = []agentSession{{Provider: "claude", PID: 1}}
		m.dashboard.composition = []categoryStat{{label: "infra", items: 12, size: 900}}
		m.dashboard.recentWrites = []audit.Entry{writeEntry}
		m.dashboard.activityFeed = []audit.ActivityEvent{feedEvent}
		m.screen = screenDashboard
		return m
	}

	// Case 1: a middling height (enrichN < 9) drops the vault-size sparkline and
	// write-count bars — the lowest-priority enrichment — while composition AND
	// the feed both stay, in a wide (non-compact) terminal.
	m := newSmallDashboard(t)
	m.dashboard.vaultSizeHistory = []audit.SizePoint{
		{Day: time.Now().AddDate(0, 0, -1).UTC().Format("2006-01-02"), Bytes: 1000},
		{Day: time.Now().UTC().Format("2006-01-02"), Bytes: 2000},
	}
	m.dashboard.agentWriteCounts = []audit.AgentWriteCount{{Provider: "claude-code", Count: 10}}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 60})
	m = updated.(model)
	if m.dashboard.bodyCompact() {
		t.Fatal("170x60 with a small roster must stay in full mode")
	}
	out := stripANSI(m.View())
	if strings.Contains(out, "Vault size") || strings.Contains(out, "Writes (7d)") {
		t.Error("charts must be dropped at this height (enrichN < 9) — they are the lowest priority")
	}
	if !strings.Contains(out, "Memory by category") {
		t.Error("composition should still show in full mode once charts are dropped")
	}
	if !strings.Contains(out, "Recent Memory Changes") || !strings.Contains(out, "Live Activity") {
		t.Error("feed sections must stay visible even though the charts were dropped")
	}

	// Case 2: a narrow terminal forces compact mode regardless of height — the
	// reported bug used to drop the feed here too (a single `compact` bit gated
	// every enrichment section at once), even with plenty of VERTICAL room to
	// spare. Composition (lower priority) is still dropped in compact; the feed
	// must now survive.
	m2 := newSmallDashboard(t)
	updated2, _ := m2.Update(tea.WindowSizeMsg{Width: 79, Height: 50})
	m2 = updated2.(model)
	if !m2.dashboard.bodyCompact() {
		t.Fatal("width 79 must force compact regardless of height")
	}
	out2 := stripANSI(m2.View())
	if strings.Contains(out2, "Memory by category") {
		t.Error("composition detail should still be dropped in compact mode")
	}
	if !strings.Contains(out2, "Recent Memory Changes") || !strings.Contains(out2, "Live Activity") {
		t.Errorf("feed must survive compact mode when there is vertical room to spare:\n%s", out2)
	}
	if h := lipgloss.Height(m2.View()); h > 50 {
		t.Errorf("compact dashboard must still fit the terminal, height %d > 50", h)
	}
}

// TestDashboardPreFirstRefreshShowsHeaders is the third guard: before the
// first async Refresh() resolves, the dashboard must still render its
// structural section headers (System Diagnostics, Memory Store, Active
// Connections) instead of the old "Loading dashboard..." full-page skeleton —
// which used to omit the whole box layout and then jump once real data
// arrived a moment later.
func TestDashboardPreFirstRefreshShowsHeaders(t *testing.T) {
	m := *NewApp(t.TempDir())
	m.screen = screenDashboard
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = updated.(model)
	out := stripANSI(m.View())
	for _, w := range []string{"System Diagnostics", "Memory Store:", "Active Connections"} {
		if !strings.Contains(out, w) {
			t.Errorf("pre-first-refresh dashboard missing header %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "Loading dashboard...") {
		t.Error("pre-first-refresh dashboard must not render the dataless full-page skeleton")
	}
}
