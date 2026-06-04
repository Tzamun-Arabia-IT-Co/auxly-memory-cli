package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/usage"
)

func seedUsageCache(t *testing.T, home string, reports map[string]usage.Report) {
	t.Helper()
	dir := filepath.Join(home, ".auxly")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(reports)
	if err := os.WriteFile(filepath.Join(dir, "usage-cache.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRenderUsagePerProvider checks the line-4 usage renders the right provider's
// windows with the right labels — including a 0%-used Cursor bucket (the zero-usage
// fix: an idle plan shows meters, not an error) — AND cross-checks that no provider
// ever leaks another's data (claude in cursor, antigravity in claude, etc.).
func TestRenderUsagePerProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now()
	seedUsageCache(t, home, map[string]usage.Report{
		"claude": {Provider: "claude", FetchedAt: now, Windows: []usage.Window{
			{Label: "Session", Pct: 5, IsLimit: true},
			{Label: "Week", Pct: 19, IsLimit: true},
		}},
		"cursor": {Provider: "cursor", FetchedAt: now, Windows: []usage.Window{
			{Label: "Total", Pct: 0, IsLimit: true}, // 0% must still render
			{Label: "Auto", Pct: 0, IsLimit: true},
		}},
		"antigravity": {Provider: "antigravity", FetchedAt: now, Windows: []usage.Window{
			{Label: "Overall", Pct: 40, IsLimit: true},
		}},
	})

	// Each provider shows ITS labels and brand, and NONE of the others'.
	cases := []struct {
		provider string
		want     []string
		notWant  []string
	}{
		{ProviderClaude, []string{"Claude", "5h", "wk"}, []string{"Cursor", "Antigravity", "plan", "all"}},
		{ProviderCursor, []string{"Cursor", "plan", "auto"}, []string{"Claude", "Antigravity", "5h", "all", "api"}},
		{ProviderAntigravity, []string{"Antigravity", "all"}, []string{"Claude", "Cursor", "plan", "5h"}},
	}
	for _, c := range cases {
		got := renderUsage(c.provider)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s usage missing %q in %q", c.provider, w, got)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(got, w) {
				t.Errorf("%s usage LEAKED %q (cross-contamination) in %q", c.provider, w, got)
			}
		}
	}
}

// TestRenderCursorSessionLine checks the line-2 quirks: Cursor's param_summary drives
// the 🧠 tag (not thinking keywords) and output tokens render.
func TestRenderCursorSessionLine(t *testing.T) {
	var in Input
	in.Model.DisplayName = "Composer"
	in.Model.ParamSummary = "(fast)"
	out := 12345
	in.ContextWindow.TotalOutputTokens = &out
	line := renderSession(in, nil, ProviderCursor)
	if !strings.Contains(line, "fast") {
		t.Errorf("cursor session should show the param tag: %q", line)
	}
	if !strings.Contains(line, "out:") {
		t.Errorf("cursor session should show output tokens: %q", line)
	}
}

func TestDefaultModelLabelPerProvider(t *testing.T) {
	var in Input // no model name at all
	if got := renderWhere(in, ProviderCursor); !strings.Contains(got, "Auto") {
		t.Errorf("cursor default model should be Auto: %q", got)
	}
	if got := renderWhere(in, ProviderAntigravity); !strings.Contains(got, "Gemini") {
		t.Errorf("antigravity default model should be Gemini: %q", got)
	}
	in.Model.Name = "gemini-2.5-flash" // model.name fallback (Gemini/Antigravity)
	if got := renderWhere(in, ProviderAntigravity); !strings.Contains(got, "gemini-2.5-flash") {
		t.Errorf("model.name should be used when display_name is empty: %q", got)
	}
}

func TestDetectProvider(t *testing.T) {
	var cur Input
	cur.Model.ParamSummary = "(fast)"
	if got := DetectProvider(cur); got != ProviderCursor {
		t.Errorf("param_summary payload → cursor, got %q", got)
	}
	var ag Input
	ag.Model.Name = "gemini-2.5"
	if got := DetectProvider(ag); got != ProviderAntigravity {
		t.Errorf("model.name-only payload → antigravity, got %q", got)
	}
	var cl Input
	cl.Model.DisplayName = "Claude"
	if got := DetectProvider(cl); got != ProviderClaude {
		t.Errorf("default payload → claude, got %q", got)
	}
	// Regression: Claude Code sends used_percentage too, so it must NOT be treated as
	// a Cursor signal (this misdetected a Claude session as Cursor on the live build).
	var clPct Input
	clPct.Model.DisplayName = "Opus 4.8"
	up := 41.0
	clPct.ContextWindow.UsedPercentage = &up
	if got := DetectProvider(clPct); got != ProviderClaude {
		t.Errorf("Claude payload with used_percentage must stay claude, got %q", got)
	}
}
