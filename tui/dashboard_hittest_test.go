package tui

import (
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
)

func TestProvidersWithActivity(t *testing.T) {
	// nil stats → no providers (no panic).
	if got := providersWithActivity(nil); got != nil {
		t.Errorf("providersWithActivity(nil) = %v, want nil", got)
	}
	// Unions both maps, dedupes, sorts deterministically. android-studio appears
	// from activity even though it is never statically detected.
	stats := &audit.Stats{
		TotalLogsByProvider: map[string]int{"android-studio": 4, "claude": 2, "": 9},
		ByProvider:          map[string]int{"claude": 1, "void": 3},
	}
	got := providersWithActivity(stats)
	want := []string{"android-studio", "claude", "void"}
	if len(got) != len(want) {
		t.Fatalf("providersWithActivity = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("providersWithActivity = %v, want %v", got, want)
		}
	}
}

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

func ids(cards []agentCard) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.id
	}
	return out
}

func TestBuildAgentCards(t *testing.T) {
	// Canonical order regardless of input order, deduped.
	cards := buildAgentCards([]string{"gemini", "claude", "cursor", "claude"})
	if got := ids(cards); len(got) != 3 || got[0] != "claude" || got[1] != "cursor" || got[2] != "gemini" {
		t.Fatalf("order/dedup = %v, want [claude cursor gemini]", got)
	}
	// Known brand carries its display metadata (the hit-tester scans for c.name).
	if cards[0] != (agentCard{"claude", "Claude Desktop", "🧠", "99"}) {
		t.Errorf("claude card = %+v", cards[0])
	}
	// Newly added IDEs are known brands.
	if got := ids(buildAgentCards([]string{"void", "warp"})); len(got) != 2 || got[0] != "warp" || got[1] != "void" {
		t.Errorf("warp/void order = %v, want [warp void]", got)
	}
	// Android Studio is a known brand (surfaced via audit activity, not detection).
	asCards := buildAgentCards([]string{"android-studio"})
	if len(asCards) != 1 || asCards[0].name != "Android Studio" || asCards[0].icon != "🤖" {
		t.Errorf("android-studio card = %+v, want name 'Android Studio' icon 🤖", asCards)
	}
	// Unknown provider → neutral card appended AFTER known ones.
	cards = buildAgentCards([]string{"mystery-agent", "claude"})
	if got := ids(cards); len(got) != 2 || got[0] != "claude" || got[1] != "mystery-agent" {
		t.Fatalf("unknown append = %v, want [claude mystery-agent]", got)
	}
	if cards[1].name != "Mystery Agent" || cards[1].icon != "🔌" {
		t.Errorf("unknown card = %+v, want name 'Mystery Agent' icon 🔌", cards[1])
	}
	// Empty input → no cards (dashboard shows the empty-state).
	if got := buildAgentCards(nil); len(got) != 0 {
		t.Errorf("nil input → %d cards, want 0", len(got))
	}
	// Canonicalization: the four antigravity surface tags collapse to ONE card,
	// "system" is dropped, and the stray "AS" tag folds into android-studio.
	cards = buildAgentCards([]string{"antigravity-ide", "antigravity-cli", "antigravity-agent", "antigravity", "system", "AS", "android-studio"})
	if got := ids(cards); len(got) != 2 || got[0] != "antigravity" || got[1] != "android-studio" {
		t.Fatalf("canonicalization = %v, want [antigravity android-studio]", got)
	}
}

func TestFilterHiddenProviders(t *testing.T) {
	// Empty hide set is a pass-through.
	if got := filterHiddenProviders([]string{"claude", "cursor"}, nil); len(got) != 2 {
		t.Fatalf("nil hide set = %v, want pass-through", got)
	}
	// Hiding is by canonical brand: the stray "AS" folds to android-studio and a
	// hidden android-studio removes it; antigravity-ide stays (brand not hidden).
	hidden := map[string]bool{"claude": true, "android-studio": true}
	got := filterHiddenProviders([]string{"claude", "cursor", "AS", "antigravity-ide"}, hidden)
	if len(got) != 2 || got[0] != "cursor" || got[1] != "antigravity-ide" {
		t.Fatalf("filterHiddenProviders = %v, want [cursor antigravity-ide]", got)
	}
	// End-to-end: a hidden brand never produces a dashboard card.
	cards := buildAgentCards(filterHiddenProviders([]string{"claude", "cursor"}, map[string]bool{"claude": true}))
	if got := ids(cards); len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("hidden claude still carded: %v", got)
	}
}

func TestCanonicalProvider(t *testing.T) {
	cases := map[string]string{
		"antigravity-ide": "antigravity",
		"AS":              "android-studio",
		"system":          "",
		"":                "",
		"  Claude ":       "claude",
		"mystery":         "mystery",
		// Phantom "Claude Code (Recommended)" from Memory Org folds into the real
		// Claude Code brand; the "organize" op tag gets no card.
		"Claude Code (Recommended)": "claude-code",
		"claude code (recommended)": "claude-code",
		"organize":                  "",
	}
	for in, want := range cases {
		if got := canonicalProvider(in); got != want {
			t.Errorf("canonicalProvider(%q) = %q, want %q", in, got, want)
		}
	}
}
