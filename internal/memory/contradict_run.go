package memory

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ContradictionItem is one contradiction/duplicate finding resolved into
// either the diff to queue against the losing fact's file, or a skip (a
// later finding that resolved to the SAME losing line — queuing it twice is
// redundant and, once the first is approved, fails as a conflict).
type ContradictionItem struct {
	Skipped     bool
	TargetFile  string // loser.File
	LoserLineNo int
	Verdict     string // "contradict" | "duplicate"; "" when Skipped
	Reason      string
	WinnerFile  string
	Diff        string // "" when Skipped
}

// ContradictionsRunResult is the full computed outcome of one contradiction
// sweep — everything both `auxly organize --contradictions`
// (cmd/organize.go's runContradictions) and the TUI's Find contradictions
// mode need to queue pendings and report a summary.
type ContradictionsRunResult struct {
	TotalFindings int // findings BEFORE same-loser de-duplication; 0 means nothing to report
	Items         []ContradictionItem
}

// PlanContradictionsRun is the shared implementation behind both
// `auxly organize --contradictions` and the TUI's Find contradictions mode:
// everything after the model's verdicts — same-loser de-duplication and diff
// construction — so that logic exists exactly once. Queuing pendings (see
// PendingWrite doc in split_projects_run.go) is left to the caller.
func (s *Store) PlanContradictionsRun(ctx context.Context, emb Embedder) (ContradictionsRunResult, error) {
	findings, err := s.PlanContradictionsWithAgent(ctx, emb, "Direct LLM", "", "")
	if err != nil {
		return ContradictionsRunResult{}, err
	}
	result := ContradictionsRunResult{TotalFindings: len(findings)}
	today := time.Now().Format("2006-01-02")
	// Two findings can resolve to the same losing line (e.g. it's the loser
	// in more than one similar pair) — queue it once.
	seen := make(map[string]bool)
	for _, f := range findings {
		winner, loser := f.Pair.A, f.Pair.B
		if f.Keep == "b" {
			winner, loser = f.Pair.B, f.Pair.A
		}

		key := loser.File + "\x00" + strconv.Itoa(loser.LineNo)
		if seen[key] {
			result.Items = append(result.Items, ContradictionItem{Skipped: true, TargetFile: loser.File, LoserLineNo: loser.LineNo})
			continue
		}
		seen[key] = true

		// Persist the model's verdict + reason as a leading comment line in
		// the queued diff — ApplyDiff only acts on "+"/"-" lines, so this
		// never touches the target file, but ViewDiff returns the raw
		// pending body so a human sees WHY before approving. Strip embedded
		// newlines from the reason so model output can't smuggle in an extra
		// "-"-prefixed line ApplyDiff would treat as a real deletion.
		reason := strings.ReplaceAll(f.Reason, "\n", " ")
		comment := fmt.Sprintf("# organize-contradictions: %s — %s (vs %s)\n", f.Verdict, reason, winner.File)

		var diff string
		switch f.Verdict {
		case "duplicate":
			// The surviving copy already exists elsewhere — pure removal.
			diff = comment + "-" + loser.Line + "\n"
		case "contradict":
			// RULE 0: a contradicted fact is never silently erased. Replace
			// (not delete) so the loser's file keeps a trace pointing at
			// whichever fact won.
			diff = comment + "-" + loser.Line + "\n" +
				"+" + loser.Line + " (superseded " + today + "; see " + winner.File + ")\n"
		default:
			continue
		}
		result.Items = append(result.Items, ContradictionItem{
			TargetFile:  loser.File,
			Diff:        diff,
			Verdict:     f.Verdict,
			LoserLineNo: loser.LineNo,
			WinnerFile:  winner.File,
			Reason:      f.Reason,
		})
	}
	return result, nil
}
