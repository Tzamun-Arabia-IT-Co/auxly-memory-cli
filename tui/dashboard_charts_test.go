package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
)

// TestFeedGlyphMapping locks the action → glyph table for the live activity feed.
func TestFeedGlyphMapping(t *testing.T) {
	cases := map[string]string{
		"write":           "✎",
		"pending_approve": "✓",
		"pending_reject":  "✗",
		"trust_change":    "🛂",
		"review_keep":     "♻",
		"review_archive":  "♻",
		"connect":         "·", // unmapped action falls back to the dot
		"":                "·",
	}
	for action, want := range cases {
		if got := feedGlyph(action); got != want {
			t.Errorf("feedGlyph(%q) = %q, want %q", action, got, want)
		}
	}
}

// TestAppendFeedNewestFirstAndTrim locks the feed merge: new events (ascending
// id, EventsSince' order) are prepended newest-first ahead of the existing
// feed, then trimmed to maxLen.
func TestAppendFeedNewestFirstAndTrim(t *testing.T) {
	existing := []audit.ActivityEvent{{ID: 3}, {ID: 2}, {ID: 1}} // newest-first
	newEvents := []audit.ActivityEvent{{ID: 4}, {ID: 5}}         // ascending, as EventsSince returns

	got := appendFeed(existing, newEvents, 4)
	wantIDs := []int64{5, 4, 3, 2}
	if len(got) != len(wantIDs) {
		t.Fatalf("length = %d, want %d: %+v", len(got), len(wantIDs), got)
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("position %d: got id %d, want %d (%+v)", i, got[i].ID, id, got)
		}
	}
}

// TestAppendFeedEmptyNewEventsNoOp confirms a no-op EventsSince result (the
// dashboardFeedCmd sentinel for "nothing new") leaves the feed untouched.
func TestAppendFeedEmptyNewEventsNoOp(t *testing.T) {
	existing := []audit.ActivityEvent{{ID: 2}, {ID: 1}}
	got := appendFeed(existing, nil, 8)
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
		t.Fatalf("empty new events must leave the feed unchanged, got %+v", got)
	}
}

// TestAppendFeedDedupesOverlappingIDs locks the defensive dedup: two
// overlapping fetches (e.g. two dashboardFeedCmd runs sharing a stale cursor
// under DB contention, before feedInFlight existed to prevent that) must
// never leave a duplicate event ID in the merged feed.
func TestAppendFeedDedupesOverlappingIDs(t *testing.T) {
	feed := appendFeed(nil, []audit.ActivityEvent{{ID: 1}, {ID: 2}}, 8)
	// Second fetch overlaps: re-delivers id 2 alongside the genuinely new id 3.
	feed = appendFeed(feed, []audit.ActivityEvent{{ID: 2}, {ID: 3}}, 8)

	seen := map[int64]int{}
	for _, e := range feed {
		seen[e.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("id %d appears %d times in the feed, want at most 1: %+v", id, n, feed)
		}
	}
	if len(feed) != 3 {
		t.Fatalf("expected 3 distinct events (1,2,3), got %d: %+v", len(feed), feed)
	}
}

// TestDashboardFeedMsgAdvancesCursorAndAppends is a pure state-transition test
// (no live audit.Logger) for the Update handler: the cursor advances to the
// message's cursor and new events land newest-first at the front.
func TestDashboardFeedMsgAdvancesCursorAndAppends(t *testing.T) {
	m := dashboardModel{feedCursor: 5}
	events := []audit.ActivityEvent{
		{ID: 6, Provider: "claude", Action: "write", File: "a.md"},
		{ID: 7, Provider: "claude", Action: "write", File: "b.md"},
	}
	got, cmd := m.Update(dashboardFeedMsg{events: events, cursor: 7})
	if cmd != nil {
		t.Fatalf("dashboardFeedMsg must not return a follow-up command")
	}
	if got.feedCursor != 7 {
		t.Fatalf("feedCursor = %d, want 7", got.feedCursor)
	}
	if len(got.activityFeed) != 2 || got.activityFeed[0].ID != 7 || got.activityFeed[1].ID != 6 {
		t.Fatalf("expected newest-first [7,6], got %+v", got.activityFeed)
	}

	// A no-op fetch (nothing new) still advances/holds the cursor without touching the feed.
	same, _ := got.Update(dashboardFeedMsg{cursor: 7})
	if len(same.activityFeed) != 2 {
		t.Fatalf("a no-op feed message must not change feed length, got %d", len(same.activityFeed))
	}
}

// TestRenderSparklineScaling locks the block-glyph scaling contract: empty,
// single-point/flat (mid glyph, no divide-by-zero), and proportional scaling
// across the full min/max span.
func TestRenderSparklineScaling(t *testing.T) {
	if got := renderSparkline(nil); got != "" {
		t.Fatalf("empty series: got %q, want \"\"", got)
	}

	mid := string(sparkGlyphs[len(sparkGlyphs)/2])
	if got := renderSparkline([]int64{42}); got != mid {
		t.Fatalf("single point: got %q, want mid glyph %q", got, mid)
	}
	if got := renderSparkline([]int64{7, 7, 7, 7}); got != mid+mid+mid+mid {
		t.Fatalf("flat series: got %q, want all mid glyphs", got)
	}

	lo := string(sparkGlyphs[0])
	hi := string(sparkGlyphs[len(sparkGlyphs)-1])
	got := renderSparkline([]int64{0, 100})
	want := lo + hi
	if got != want {
		t.Fatalf("min/max series: got %q, want %q (lowest then highest glyph)", got, want)
	}

	// A rising series must be monotonically non-decreasing in glyph level.
	rows := renderSparkline([]int64{0, 25, 50, 75, 100})
	runes := []rune(rows)
	prev := -1
	for i, r := range runes {
		level := -1
		for l, g := range sparkGlyphs {
			if g == r {
				level = l
			}
		}
		if level < prev {
			t.Fatalf("glyph level decreased at position %d: %q", i, rows)
		}
		prev = level
	}
}

// TestVaultSizeSparklineInterpolatesAndGates locks the "no history yet" gate
// (<2 points) and forward-fill interpolation across days with no snapshot.
func TestVaultSizeSparklineInterpolatesAndGates(t *testing.T) {
	if got := vaultSizeSparkline(nil, 30); got != "" {
		t.Fatalf("no points: got %q, want \"\"", got)
	}
	if got := vaultSizeSparkline([]audit.SizePoint{{Day: "2020-01-01", Bytes: 1}}, 30); got != "" {
		t.Fatalf("single point: got %q, want \"\" (below the 2-point gate)", got)
	}

	// vaultSizeSparkline anchors its window on time.Now(), so exercise the
	// interpolation/width contract at whatever "today" resolves to rather than
	// pinning a fixed date.
	now := time.Now().UTC()
	points := []audit.SizePoint{
		{Day: now.AddDate(0, 0, -10).Format("2006-01-02"), Bytes: 10},
		{Day: now.Format("2006-01-02"), Bytes: 100},
	}
	got := vaultSizeSparkline(points, 30)
	if got == "" {
		t.Fatalf("2 points should render a sparkline, got empty")
	}
	if len([]rune(got)) != 30 {
		t.Fatalf("sparkline width = %d, want 30 (one glyph per day)", len([]rune(got)))
	}
	// The last (today's) glyph must be the highest level — it holds the max value.
	runes := []rune(got)
	if runes[len(runes)-1] != sparkGlyphs[len(sparkGlyphs)-1] {
		t.Fatalf("today's glyph = %q, want the highest-level glyph %q", string(runes[len(runes)-1]), string(sparkGlyphs[len(sparkGlyphs)-1]))
	}
	// A day before the first snapshot (day 0..9) forward-fills from 0 (no
	// earlier value to carry), landing on the lowest glyph alongside day 10's
	// own value of 10 (still the min of the series).
	if runes[0] != sparkGlyphs[0] {
		t.Fatalf("day before any snapshot = %q, want the lowest-level glyph %q", string(runes[0]), string(sparkGlyphs[0]))
	}
}

// TestRenderVaultSizeChartNoHistoryText locks renderVaultSizeChart's own
// "no history yet" copy for <2 points, independent of renderBody's decision
// to only reserve a dashboard row once there's real history to show.
func TestRenderVaultSizeChartNoHistoryText(t *testing.T) {
	m := dashboardModel{}
	if got := stripANSI(m.renderVaultSizeChart()); !strings.Contains(got, "no history yet") {
		t.Fatalf("empty history: got %q, want it to contain 'no history yet'", got)
	}

	m.vaultSizeHistory = []audit.SizePoint{{Day: "2020-01-01", Bytes: 1000}, {Day: "2020-01-02", Bytes: 2000}}
	got := stripANSI(m.renderVaultSizeChart())
	if strings.Contains(got, "no history yet") {
		t.Fatalf("with 2+ points should not show the empty-state copy, got %q", got)
	}
	if !strings.Contains(got, "2.0KiB") {
		t.Fatalf("expected the latest sample rendered via humanBytes, got %q", got)
	}
}

// TestRenderAgentWriteBarsTopFourScaled locks the write-bar scaling: capped
// to the top 4 (busiest first, as AgentWriteCounts already orders them),
// proportional to the max, and a non-zero count always shows at least one
// filled cell.
func TestRenderAgentWriteBarsTopFourScaled(t *testing.T) {
	if got := renderAgentWriteBars(nil); got != "" {
		t.Fatalf("no counts: got %q, want \"\"", got)
	}

	counts := []audit.AgentWriteCount{
		{Provider: "codex", Count: 40},
		{Provider: "claude", Count: 20},
		{Provider: "cursor", Count: 10},
		{Provider: "gemini", Count: 1},
		{Provider: "trae", Count: 1}, // 5th agent must be dropped
	}
	got := stripANSI(renderAgentWriteBars(counts))
	for _, want := range []string{"codex", "claude", "cursor", "gemini"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got %q", want, got)
		}
	}
	if strings.Contains(got, "trae") {
		t.Errorf("only the top 4 agents should render, got %q", got)
	}
	// The smallest non-zero count (gemini: 1) must still show a filled cell,
	// never rounding down to an invisible empty bar.
	if barFill(got, "▰") == 0 {
		t.Errorf("expected at least one filled bar cell across all rows, got none: %q", got)
	}
}

// TestShouldRecordVaultSizeOnlyOnChangeOrDayRoll locks Refresh's write-amplification
// guard: RecordVaultSize (a DB write) is only warranted when the byte count actually
// changed or the UTC day rolled over since the last recorded snapshot — not on every
// ~1s dashboard tick.
func TestShouldRecordVaultSizeOnlyOnChangeOrDayRoll(t *testing.T) {
	if shouldRecordVaultSize(1000, "2026-07-02", 1000, "2026-07-02") {
		t.Error("same bytes, same day must NOT re-record")
	}
	if !shouldRecordVaultSize(1000, "2026-07-02", 1500, "2026-07-02") {
		t.Error("a changed byte count must record")
	}
	if !shouldRecordVaultSize(1000, "2026-07-02", 1000, "2026-07-03") {
		t.Error("a rolled-over UTC day must record even with unchanged bytes")
	}
	if !shouldRecordVaultSize(0, "", 0, "2026-07-02") {
		t.Error("the zero-value cache (never recorded yet) must record on the first call")
	}
}
