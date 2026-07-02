package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ContradictionFinding is one validated model verdict on a cross-file fact
// pair — a PLAN only, same contract as ProjectsSplitPlan: the caller queues it
// as pending changes for human review; nothing here writes the vault.
type ContradictionFinding struct {
	Pair    FactPair
	Verdict string // "contradict" | "duplicate" (never "distinct" — those are dropped)
	Keep    string // "a" | "b" — which side of Pair should survive
	Reason  string
}

const contradictSystemPrompt = `You are auditing pairs of memory-vault facts an embedding search flagged as similar, possibly living in different files.

RESPONSE CONTRACT — reply with EXACTLY ONE JSON object, nothing else:
{"findings":[{"pair":<index>,"verdict":"contradict|duplicate|distinct","keep":"a|b","reason":"<short>"}]}

For EVERY numbered pair below, judge it as exactly one of:
- "contradict": the two facts CANNOT both be true at once (e.g. two different IPs given for the same server, two different owners of the same project).
- "duplicate": the two facts restate the SAME information, just worded differently.
- "distinct": the two facts merely share a topic but are BOTH independently true — no action needed.

"keep" says which statement should survive:
- For "contradict": keep whichever reads NEWER or MORE SPECIFIC (a later date, more detail, a more recently stated fact).
- For "duplicate": keep the copy in the MORE SPECIFIC file — a projects/<slug>.md file beats a general category file (e.g. projects.md) for the same project's fact.

NEVER invent facts, and only report on pairs you were given, by their given index. If in doubt, mark a pair "distinct".`

// contradictUserPrompt numbers every candidate pair so the model's "pair"
// index in its JSON reply maps back unambiguously to pairs[i].
func contradictUserPrompt(pairs []FactPair) string {
	var b strings.Builder
	b.WriteString("Here are the candidate fact pairs to judge:\n\n")
	for i, p := range pairs {
		fmt.Fprintf(&b, "%d:\nA (%s:%d): %s\nB (%s:%d): %s\n\n", i, p.A.File, p.A.LineNo, p.A.Line, p.B.File, p.B.LineNo, p.B.Line)
	}
	return b.String()
}

// PlanContradictions sources cross-file candidate pairs from the embedding
// index, then asks the model to judge each one. No pairs (no embedder, empty
// vault, nothing similar enough) is not an error — it just means nothing to
// report. Errors from the pair source (e.g. embed.ErrUnavailable,
// ErrVaultTooLarge) propagate as-is so the caller can give a precise message.
func (s *Store) PlanContradictions(ctx context.Context, emb Embedder, exec organizeExecutor) ([]ContradictionFinding, error) {
	pairs, err := s.SimilarCrossFilePairs(ctx, emb, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	return planContradictionsFromPairs(ctx, pairs, exec)
}

// planContradictionsFromPairs is the thin, directly-testable core of
// PlanContradictions: everything after pair sourcing. Split out because
// SimilarCrossFilePairs lives in another file and can't be stubbed in tests —
// this function can be, with a hand-built []FactPair.
func planContradictionsFromPairs(ctx context.Context, pairs []FactPair, exec organizeExecutor) ([]ContradictionFinding, error) {
	run, res, proceed := exec(ctx, contradictSystemPrompt, contradictUserPrompt(pairs))
	if !proceed {
		return nil, fmt.Errorf("contradiction model call failed: %s", res.Message)
	}

	raw := strings.TrimSpace(run.jsonContent)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")

	var out struct {
		Findings []struct {
			Pair    int    `json:"pair"`
			Verdict string `json:"verdict"`
			Keep    string `json:"keep"`
			Reason  string `json:"reason"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(repairAgentJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("contradiction response is not the contracted JSON: %w", err)
	}

	var findings []ContradictionFinding
	for _, f := range out.Findings {
		if f.Pair < 0 || f.Pair >= len(pairs) {
			// Out-of-range index — never trust a reference we can't resolve
			// back to a real pair.
			continue
		}
		if f.Verdict != "contradict" && f.Verdict != "duplicate" {
			// "distinct", or anything the model made up, is dropped — the
			// enum's safe default is "no action needed".
			continue
		}
		// Model output is free text — normalize case/whitespace before the
		// enum check. A case-sensitive compare would silently coerce a
		// literal "B" to the "a" default, INVERTING which fact survives.
		keep := strings.ToLower(strings.TrimSpace(f.Keep))
		if keep != "a" && keep != "b" {
			keep = "a"
		}
		findings = append(findings, ContradictionFinding{
			Pair:    pairs[f.Pair],
			Verdict: f.Verdict,
			Keep:    keep,
			Reason:  strings.TrimSpace(f.Reason),
		})
	}
	return findings, nil
}

// PlanContradictionsWithAgent mirrors PlanProjectsSplitWithAgent: same model
// resolution (CLI agent when agentPath != "", else the direct LLM endpoint).
func (s *Store) PlanContradictionsWithAgent(ctx context.Context, emb Embedder, agentName, agentPath, model string) ([]ContradictionFinding, error) {
	return s.PlanContradictions(ctx, emb, func(c context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		return s.runOrganizeModel(c, agentName, agentPath, model, sys, user)
	})
}
