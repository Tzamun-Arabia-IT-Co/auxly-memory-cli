package trust

import (
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
)

func newTestCfg(provider, level string) *Config {
	return &Config{
		Default:   LevelRequireApproval,
		Providers: map[string]ProviderConfig{provider: {TrustLevel: level}},
	}
}

func TestSuggestChanges_PromoteThresholdEdges(t *testing.T) {
	// 49 decided at a high approval rate: below tuneMinDecided, and the
	// rejection rate is nowhere near the demote threshold either — no suggestion.
	cfg := newTestCfg("codex", LevelRequireApproval)
	stats := []audit.AgentApprovalStats{{Provider: "codex", Approved: 47, Rejected: 2}}
	if got := SuggestChanges(cfg, stats); len(got) != 0 {
		t.Fatalf("49 decided: want no suggestion, got %+v", got)
	}

	// 50 decided at 96% approval: clears both thresholds — promote to auto.
	stats = []audit.AgentApprovalStats{{Provider: "codex", Approved: 48, Rejected: 2}}
	got := SuggestChanges(cfg, stats)
	if len(got) != 1 {
		t.Fatalf("50 decided @96%%: want 1 suggestion, got %+v", got)
	}
	if got[0].Suggested != LevelAuto || got[0].Current != LevelRequireApproval {
		t.Errorf("want require_approval -> auto, got %+v", got[0])
	}
	if got[0].Evidence != "48/50 approved over 90d" {
		t.Errorf("evidence = %q", got[0].Evidence)
	}
}

func TestSuggestChanges_DemoteOnlyRequireApprovalToReadOnly(t *testing.T) {
	// auto never demotes: an auto provider's writes apply immediately and
	// never queue a pending_approve/reject row, so there is no live rejection
	// signal to act on — the auto->require_approval branch was dead code and
	// has been removed. Even at a high rejection rate, no suggestion fires.
	cfg := newTestCfg("gemini", LevelAuto)
	stats := []audit.AgentApprovalStats{{Provider: "gemini", Approved: 7, Rejected: 3}}
	if got := SuggestChanges(cfg, stats); len(got) != 0 {
		t.Fatalf("auto source: want no demotion (no live signal), got %+v", got)
	}

	// require_approval -> read_only at a 50% rejection rate.
	cfg = newTestCfg("gemini", LevelRequireApproval)
	stats = []audit.AgentApprovalStats{{Provider: "gemini", Approved: 5, Rejected: 5}}
	got := SuggestChanges(cfg, stats)
	if len(got) != 1 || got[0].Suggested != LevelReadOnly {
		t.Fatalf("require_approval demote: want read_only, got %+v", got)
	}

	// read_only never demotes further — no suggestion regardless of rejections.
	cfg = newTestCfg("gemini", LevelReadOnly)
	got = SuggestChanges(cfg, stats)
	if len(got) != 0 {
		t.Fatalf("read_only: want no suggestion, got %+v", got)
	}
}

func TestSuggestChanges_TuningOff(t *testing.T) {
	cfg := newTestCfg("codex", LevelRequireApproval)
	cfg.Tuning = "off"
	stats := []audit.AgentApprovalStats{{Provider: "codex", Approved: 100, Rejected: 0}}
	if got := SuggestChanges(cfg, stats); got != nil {
		t.Fatalf("tuning off: want nil, got %+v", got)
	}
}

func TestSuggestChanges_MergesRowsThatCollapseToTheSameProvider(t *testing.T) {
	// Two decision-log rows for the same provider that only differ by
	// case/whitespace (e.g. "Claude" vs "claude") must sum into one evaluation
	// instead of each individually falling short of the threshold.
	cfg := newTestCfg("claude", LevelRequireApproval)
	stats := []audit.AgentApprovalStats{
		{Provider: " Claude ", Approved: 30, Rejected: 1},
		{Provider: "claude", Approved: 18, Rejected: 1},
	}
	got := SuggestChanges(cfg, stats)
	if len(got) != 1 {
		t.Fatalf("want merged rows to clear the 50-decided threshold, got %+v", got)
	}
	if got[0].Evidence != "48/50 approved over 90d" {
		t.Errorf("merged evidence = %q, want 48/50 approved over 90d", got[0].Evidence)
	}
}

// TestSuggestChanges_CaptureRowsDoNotMergeWithDirectProvider is defense in
// depth for the evidence-laundering fix: audit.ApprovalStats already excludes
// capture:*/organize-* rows before SuggestChanges ever sees them, but even if
// one leaked through, NormalizeDecisionAgent no longer strips the "capture:"
// prefix — so it stays a distinct provider id and cannot merge into the bare
// provider's direct-write totals to launder a promotion.
func TestSuggestChanges_CaptureRowsDoNotMergeWithDirectProvider(t *testing.T) {
	cfg := newTestCfg("claude", LevelRequireApproval)
	stats := []audit.AgentApprovalStats{
		{Provider: "capture:claude", Approved: 48, Rejected: 2},
		{Provider: "claude", Approved: 3, Rejected: 0},
	}
	got := SuggestChanges(cfg, stats)
	for _, s := range got {
		if s.Provider == "claude" {
			t.Fatalf("claude has only 3 directly-decided rows (below threshold) — capture:claude's 50 must not have merged in: %+v", got)
		}
	}
}

func TestSuggestChanges_NilCfg(t *testing.T) {
	if got := SuggestChanges(nil, nil); got != nil {
		t.Fatalf("nil cfg: want nil, got %+v", got)
	}
}
