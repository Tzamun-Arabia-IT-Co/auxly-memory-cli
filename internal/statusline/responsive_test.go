package statusline

import (
	"strings"
	"testing"
)

func TestVisibleWidthStripsSGRAndCountsEmoji(t *testing.T) {
	// SGR codes are zero width; the visible text "ab" is 2 cells.
	if w := visibleWidth("\033[38;2;1;2;3mab\033[0m"); w != 2 {
		t.Errorf("colored 'ab' should be width 2, got %d", w)
	}
	// A wide emoji counts as 2 cells.
	if w := visibleWidth("📁"); w != 2 {
		t.Errorf("📁 should be width 2, got %d", w)
	}
}

func TestJoinFitDropsLowestPriorityFirst(t *testing.T) {
	segs := []lineSeg{
		{text: "AAA", prio: prioPinned}, // pinned, never dropped
		{text: "BB", prio: 90},
		{text: "CCCC", prio: 10}, // lowest — dropped first
		{text: "DD", prio: 50},
	}
	// Unconstrained: everything, in order.
	if got := joinFit(segs, " ", 0); got != "AAA BB CCCC DD" {
		t.Errorf("width 0 should keep all: %q", got)
	}
	// Width 12 forces dropping the lowest-prio "CCCC" (others fit: "AAA BB DD" = 9).
	got := joinFit(segs, " ", 12)
	if strings.Contains(got, "CCCC") {
		t.Errorf("lowest-priority segment should be dropped: %q", got)
	}
	if !strings.Contains(got, "AAA") || !strings.Contains(got, "BB") || !strings.Contains(got, "DD") {
		t.Errorf("higher-priority segments should survive: %q", got)
	}
	if visibleWidth(got) > 12 {
		t.Errorf("result should fit width 12, got width %d (%q)", visibleWidth(got), got)
	}
}

func TestJoinFitPinnedSurvivesAndTruncates(t *testing.T) {
	// A single pinned segment longer than the width is hard-truncated with an ellipsis.
	segs := []lineSeg{{text: "this-is-a-very-long-pinned-segment", prio: prioPinned}}
	got := joinFit(segs, " ", 10)
	if visibleWidth(got) > 10 {
		t.Errorf("truncated pinned segment should fit width 10, got %d (%q)", visibleWidth(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("over-long segment should be truncated with an ellipsis: %q", got)
	}
}

func TestTruncateVisiblePreservesShortStrings(t *testing.T) {
	s := "\033[31mhi\033[0m"
	if got := truncateVisible(s, 0); got != s {
		t.Errorf("width 0 should be a no-op")
	}
	if got := truncateVisible(s, 10); got != s {
		t.Errorf("fitting string should be unchanged")
	}
}
