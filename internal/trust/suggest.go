package trust

import (
	"fmt"
	"sort"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
)

// Trust auto-tuning thresholds. Deliberately conservative in both directions:
// promotion needs a long, clean track record; demotion reacts fast to a bad
// one. Under-suggesting just means a human keeps clicking `trust set` by hand
// a bit longer — over-suggesting erodes trust in the suggestion feature itself.
const (
	tuneMinDecided       = 50   // promote: minimum approved+rejected before trusting the rate
	tuneMinApproval      = 0.95 // promote: approval rate required to suggest auto
	tuneDemoteMinDecided = 10   // demote: fewer events needed — a bad run should surface fast
	tuneDemoteRejection  = 0.30 // demote: rejection rate that triggers a downgrade suggestion
)

// Suggestion is one trust-level change recommendation, evidence attached so a
// human can judge it before applying. See SuggestChanges.
type Suggestion struct {
	Provider  string
	Current   string
	Suggested string
	Evidence  string
}

// SuggestChanges evaluates 90-day approval history against cfg's current trust
// levels and returns suggested changes, sorted by provider. It NEVER applies
// anything — trust levels are a security boundary, so every change stays a
// human decision (`auxly trust set ...`); this only surfaces the evidence.
// Pure (no I/O), so it unit-tests without a database.
func SuggestChanges(cfg *Config, stats []audit.AgentApprovalStats) []Suggestion {
	if cfg == nil || !cfg.TuningEnabled() {
		return nil
	}

	// Raw provider ids may vary in case/whitespace across logging call sites
	// (e.g. "Codex" vs "codex") — merge after normalizing so a split history
	// doesn't dilute the decided count below a threshold it should clear.
	// This does NOT collapse capture:*/organize-* rows into a provider's
	// direct-write totals: those are excluded upstream by audit.ApprovalStats,
	// precisely so approving low-stakes queued facts can't launder a
	// promotion for the provider's direct writes.
	type totals struct{ approved, rejected int }
	merged := make(map[string]totals)
	var order []string
	for _, s := range stats {
		p := audit.NormalizeDecisionAgent(s.Provider)
		if p == "" {
			continue
		}
		if _, ok := merged[p]; !ok {
			order = append(order, p)
		}
		t := merged[p]
		t.approved += s.Approved
		t.rejected += s.Rejected
		merged[p] = t
	}

	var out []Suggestion
	for _, p := range order {
		t := merged[p]
		decided := t.approved + t.rejected
		if decided == 0 {
			continue
		}
		current := cfg.GetTrustLevel(p)
		approvalRate := float64(t.approved) / float64(decided)
		rejectionRate := float64(t.rejected) / float64(decided)

		switch {
		case current == LevelRequireApproval && decided >= tuneMinDecided && approvalRate >= tuneMinApproval:
			out = append(out, Suggestion{
				Provider: p, Current: current, Suggested: LevelAuto,
				Evidence: fmt.Sprintf("%d/%d approved over 90d", t.approved, decided),
			})
		// Demote only covers require_approval -> read_only. An auto provider's
		// writes apply immediately and never queue a pending_approve/reject
		// row, so there is no live rejection signal to demote auto on —
		// faking one from stale pre-promotion approval history would not be
		// honest. Needs a supersede/revert signal on auto writes before
		// auto->require_approval can exist here; future work.
		case current == LevelRequireApproval && decided >= tuneDemoteMinDecided && rejectionRate >= tuneDemoteRejection:
			out = append(out, Suggestion{
				Provider: p, Current: current, Suggested: LevelReadOnly,
				Evidence: fmt.Sprintf("%d/%d rejected over 90d", t.rejected, decided),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}
