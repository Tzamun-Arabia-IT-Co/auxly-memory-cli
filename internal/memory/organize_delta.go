package memory

// Optimization 3 — delta-ops organize (DARK LAUNCH, default OFF): whole-vault
// organize has the model re-emit every file's FULL content, which is the
// single biggest cost in the ~485s baseline (huge generated output on top of
// the cold CLI-agent spawn). This file adds an ALTERNATIVE response contract
// where the model instead returns small OPERATIONS — move/merge/delete a
// single bullet — which planOrganizeDelta applies LOCALLY (plain Go, no LLM)
// to reconstruct the identical []ProposedChange before/after set the
// whole-file path produces. That set flows into the EXISTING review +
// factLossWarning pipeline unchanged — the safety gate and human approval
// step never change, only how the model's answer gets there.
//
// WHY DARK: op-based prompting is a new, unvalidated contract for weaker
// agent-CLI models that already struggle to stay in strict JSON (see
// repairAgentJSON). Shipping it OFF by default (OrganizeRunOpts.DeltaMode)
// means the plumbing, prompt, parser, and safety net all exist and are
// tested, but nothing in production depends on them until a follow-up
// release flips the default after real-world validation. Do not remove the
// guard when that happens — flip OrganizeRunOpts{DeltaMode: true} at the
// call site instead.
//
// SAFETY (non-negotiable): every op references a bullet by its EXACT existing
// line text. A bullet that doesn't match verbatim in the source file is a
// phantom op — DROPPED with a note, never fuzzy-matched, never invented (see
// applyDeltaOps). personal.md's one-way privacy sink is enforced the same way
// as the chunked path: a move OUT of personal.md is refused outright,
// whatever the model said. Finally, applyDeltaGuard is a PER-FILE hard
// fact-loss net: any file whose reconstruction would lose most of its facts
// with no trace elsewhere in the batch is reverted to UNCHANGED before the
// proposal is ever returned — a bad op costs that one file its reorganization
// this run, never a corrupted vault.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// deltaOp is one mechanical edit to a single bullet. Bullet must be the
// EXACT original line text (see file header SAFETY) — never paraphrased.
type deltaOp struct {
	Op         string `json:"op"`         // "move" | "merge" | "delete" | "keep"
	File       string `json:"file"`       // file the bullet currently lives in
	Bullet     string `json:"bullet"`     // EXACT existing line text of the fact
	ToFile     string `json:"toFile"`     // move: destination file
	MergedText string `json:"mergedText"` // merge: replacement text for this bullet
}

type deltaResponse struct {
	Ops []deltaOp `json:"ops"`
}

// planOrganizeDelta runs ONE whole-vault model call under the delta-ops
// response contract and applies the result locally to build the same
// []ProposedChange shape the whole-file path (buildProposalFromJSON) produces.
func (s *Store) planOrganizeDelta(ctx context.Context, exec organizeExecutor, files []organizeFile) (OrganizeProposal, OrganizeResult) {
	run, res, proceed := exec(ctx, organizeDeltaSystemPrompt(), vaultUserPrompt(files))
	if !proceed {
		return OrganizeProposal{}, res
	}
	ops, err := parseDeltaResponse(run.jsonContent)
	if err != nil {
		return OrganizeProposal{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse delta JSON payload: %v\nOutput content was: %s", err, run.jsonContent)}
	}

	changes, dropped := s.applyDeltaOps(files, ops)
	changes = s.stripPersonalLeaks(changes) // defense in depth, same sink guard as every other path
	changes, guardNotes := applyDeltaGuard(changes)

	prop := OrganizeProposal{Changes: changes, ModelUsed: run.modelUsed, TokensUsed: run.tokensUsed, DroppedDeltaOps: dropped}
	prop.Warning = factLossWarning(prop.Changes)
	if len(guardNotes) > 0 {
		// Unlike dropped phantom ops, a guard trip means a file's
		// reorganization was silently skipped this run — worth a human's
		// attention even though nothing was actually lost.
		note := strings.Join(guardNotes, "\n")
		if prop.Warning != "" {
			note += "\n" + prop.Warning
		}
		prop.Warning = note
	}
	return prop, OrganizeResult{Success: true}
}

// parseDeltaResponse parses one delta call's JSON. Tolerates the same lenient
// repair as the whole-file and chunked parsers.
func parseDeltaResponse(jsonContent string) ([]deltaOp, error) {
	var parsed deltaResponse
	if err := json.Unmarshal([]byte(jsonContent), &parsed); err != nil {
		repaired := repairAgentJSON(jsonContent)
		if err2 := json.Unmarshal([]byte(repaired), &parsed); err2 != nil {
			return nil, err
		}
	}
	return parsed.Ops, nil
}

// applyDeltaOps mechanically applies a parsed delta-ops response against the
// ORIGINAL per-file snapshots, producing the same []ProposedChange shape a
// whole-file response would. dropped lists every op that was discarded
// (phantom bullet text, invalid target, or a refused personal.md move) —
// informational, never a fact-loss signal.
func (s *Store) applyDeltaOps(files []organizeFile, ops []deltaOp) (changes []ProposedChange, dropped []string) {
	byName := make(map[string]int, len(files)) // file name → index into files
	lines := make(map[string][]string, len(files))
	for i, f := range files {
		byName[f.Name] = i
		lines[f.Name] = strings.Split(f.Content, "\n")
	}

	// findLine returns the index of the line in lines[file] whose TRIMMED
	// text exactly equals want, or -1. Exact match only — see SAFETY above.
	findLine := func(file, want string) int {
		want = strings.TrimSpace(want)
		for i, l := range lines[file] {
			if strings.TrimSpace(l) == want {
				return i
			}
		}
		return -1
	}
	removeLine := func(file string, idx int) {
		lines[file] = append(lines[file][:idx], lines[file][idx+1:]...)
	}

	type pendingAppend struct{ to, from, fact string }
	var appends []pendingAppend

	for _, op := range ops {
		if _, ok := byName[op.File]; !ok {
			dropped = append(dropped, fmt.Sprintf("op %q referenced unknown file %q — dropped", op.Op, op.File))
			continue
		}
		switch op.Op {
		case "keep":
			// No-op by definition: the bullet stays exactly where it is.
		case "delete":
			// Task spec: a delete with no matching line is already a no-op —
			// still logged below via the generic not-found path for visibility.
			if idx := findLine(op.File, op.Bullet); idx != -1 {
				removeLine(op.File, idx)
			} else {
				dropped = append(dropped, fmt.Sprintf("delete op bullet not found verbatim in %s — no-op: %q", op.File, op.Bullet))
			}
		case "move":
			if op.File == "personal.md" {
				// PRIVACY: personal.md is a one-way sink — content NEVER
				// leaves it, whatever the model said. Same mechanical
				// enforcement as planOrganizeChunked's identical guard.
				dropped = append(dropped, "refused to move a fact OUT of personal.md — kept in place")
				continue
			}
			idx := findLine(op.File, op.Bullet)
			if idx == -1 {
				dropped = append(dropped, fmt.Sprintf("move op bullet not found verbatim in %s — dropped: %q", op.File, op.Bullet))
				continue
			}
			if op.ToFile == "" || !IsOrganizableFile(op.ToFile) || op.ToFile == op.File {
				dropped = append(dropped, fmt.Sprintf("move op has an invalid target %q — dropped, bullet kept in %s", op.ToFile, op.File))
				continue
			}
			removeLine(op.File, idx)
			appends = append(appends, pendingAppend{to: op.ToFile, from: op.File, fact: ensureBullet(op.Bullet)})
		case "merge":
			idx := findLine(op.File, op.Bullet)
			if idx == -1 {
				dropped = append(dropped, fmt.Sprintf("merge op bullet not found verbatim in %s — dropped: %q", op.File, op.Bullet))
				continue
			}
			merged := strings.TrimSpace(op.MergedText)
			if merged == "" {
				merged = op.Bullet // nothing to merge into — keep the original rather than invent text
			}
			lines[op.File][idx] = ensureBullet(merged)
		default:
			dropped = append(dropped, fmt.Sprintf("unknown op %q — dropped", op.Op))
		}
	}

	changes = make([]ProposedChange, 0, len(files))
	idxOf := make(map[string]int, len(files))
	for _, f := range files {
		content := dedupeBulletLines(strings.Join(lines[f.Name], "\n"))
		scope := "global"
		if s.isWorkspaceFile(f.Name) {
			scope = "workspace"
		}
		idxOf[f.Name] = len(changes)
		changes = append(changes, ProposedChange{Name: f.Name, OldContent: f.Content, NewContent: content, Scope: scope})
	}

	// Mechanical routing pass for moves — mirrors planOrganizeChunked's
	// target resolution exactly, including its fail-closed unreadable-target
	// handling (put the fact back in its source rather than lose it).
	for _, a := range appends {
		idx, ok := idxOf[a.to]
		if !ok {
			old, viewErr := s.View(a.to)
			if viewErr != nil && !errors.Is(viewErr, os.ErrNotExist) {
				dropped = append(dropped, fmt.Sprintf("move target %s is unreadable (%v) — bullet kept in %s instead", a.to, viewErr, a.from))
				fromIdx := idxOf[a.from]
				changes[fromIdx].NewContent = appendFact(changes[fromIdx].NewContent, a.fact)
				continue
			}
			if viewErr != nil {
				old = ""
			}
			scope := "global"
			if s.isWorkspaceFile(a.to) {
				scope = "workspace"
			}
			idxOf[a.to] = len(changes)
			idx = idxOf[a.to]
			changes = append(changes, ProposedChange{Name: a.to, OldContent: old, NewContent: old, Scope: scope, IsNew: viewErr != nil})
		}
		if !containsFact(changes[idx].NewContent, a.fact) {
			changes[idx].NewContent = appendFact(changes[idx].NewContent, a.fact)
		}
	}

	return changes, dropped
}

// applyDeltaGuard is delta-mode's per-file HARD safety net — stronger than
// factLossWarning's whole-proposal warning: any file whose reconstructed
// content would lose most of its own facts with no trace anywhere else in the
// batch is reverted to fully UNCHANGED before the proposal is ever returned.
// Same >=3-facts/>80%-missing threshold as factLossWarning's per-file check,
// applied here as a correction rather than just a warning, and to every
// qualifying file, not only the first. WHY per-file, not proposal-wide: a bad
// delta op should only cost that op's file its reorganization this run, never
// risk the whole batch.
func applyDeltaGuard(changes []ProposedChange) (out []ProposedChange, notes []string) {
	newSet := make(map[string]bool)
	for _, c := range changes {
		for _, b := range bulletLines(c.NewContent) {
			newSet[normalizeFact(b)] = true
		}
	}
	out = make([]ProposedChange, len(changes))
	copy(out, changes)
	for i, c := range out {
		oldBullets := bulletLines(c.OldContent)
		if len(oldBullets) < 3 {
			continue // too few facts for the loss ratio to be a meaningful signal
		}
		missing := 0
		for _, b := range oldBullets {
			if !newSet[normalizeFact(b)] {
				missing++
			}
		}
		if missing*100 > len(oldBullets)*80 {
			notes = append(notes, fmt.Sprintf("⚠ delta organize: %s would have lost most of its facts — reverted to unchanged this run, review manually", c.Name))
			out[i].NewContent = c.OldContent
		}
	}
	return out, notes
}

// organizeDeltaSystemPrompt is the delta-ops variant of the organize prompt:
// same RULE 0 and taxonomy as organizeSystemPrompt, but the model emits small
// OPERATIONS instead of rewriting every file's full content — this is what
// makes delta mode fast, so the prompt is explicit that omitted bullets stay
// untouched rather than asking the model to enumerate every single one.
func organizeDeltaSystemPrompt() string {
	return fmt.Sprintf(`═══ RESPONSE CONTRACT — READ FIRST, OBEY ABSOLUTELY ═══
You are a NON-INTERACTIVE text→JSON transformer, NOT an interactive agent.
- The COMPLETE memory vault is included verbatim in this prompt below. NOTHING is
  truncated, cut off, or stored elsewhere. There are NO files on disk to open and
  you have NO tools — any attempt to read files or call tools will find nothing.
- Do NOT narrate, plan, think out loud, or explain. Do NOT write sentences like
  "Let me read…", "The input was truncated", "I'll use the MCP tools", or any
  analysis. Such output BREAKS the caller and fails the task.
- Your ENTIRE response MUST be exactly ONE JSON object and nothing else: the FIRST
  character you emit is `+"`{`"+` and the LAST is `+"`}`"+`. No prose before it, no prose
  after it, no markdown fences. Begin your reply with `+"`{`"+` immediately.

You are an expert Auxly Memory Architect. Your job is to RE-FILE and tidy the
user's memory vault by emitting a small list of OPERATIONS, not rewritten
files — WITHOUT EVER LOSING A SINGLE FACT.

═══ RULE 0 — ZERO LOSS (ABSOLUTE, OVERRIDES EVERYTHING) ═══
Every distinct fact, name, number, date, ID, decision, server, IP, case number,
amount, and detail present in the INPUT must still be present after your ops
are applied (either untouched, or moved/merged by an op).
- Deleting a bullet that represents real information is STRICTLY FORBIDDEN —
  only "delete" a bullet that is a true exact/near-exact duplicate of another
  bullet that survives.
- If you are unsure where a fact belongs, do NOT emit an op for it — leaving
  it untouched is always safe.

═══ OPERATIONS — ONLY LIST WHAT CHANGES ═══
Do NOT emit an op for every bullet. Any bullet you do not mention in "ops"
automatically stays exactly as-is — this is what makes your response fast to
generate and cheap to transmit. Only emit an op for a bullet that needs to:
  - "move":   relocate a misfiled bullet to its correct file (taxonomy below).
  - "merge":  rewrite a bullet that is a duplicate of another surviving bullet
              into the shared canonical text (emit one "merge" op per
              duplicate occurrence, all with the SAME "mergedText").
  - "delete": remove a bullet ONLY when it is a true duplicate with nothing
              unique in it (prefer "merge" if any detail differs).
  - "keep":   rarely needed; explicitly confirms a bullet's placement.

CRITICAL — "bullet" MUST be the EXACT existing line text from the input,
character-for-character (including its "- " or "* " prefix and any trailing
date stamp). If you paraphrase, summarize, or retype it even slightly, the op
cannot be matched and will be discarded. Copy it verbatim.

1. RE-CLASSIFICATION taxonomy for "move"/"toFile":
%s
2. PERSONAL IS A ONE-WAY SINK (PRIVACY — CRITICAL): a PRIVATE-LIFE fact about
   the USER as an individual (their own family, health, relationships, or
   their OWN legal/financial matter) sitting in a SHARED file MUST be moved
   INTO personal.md via a "move" op. A fact must NEVER move OUT of
   personal.md — do not emit any op with "file": "personal.md" and
   "op": "move".
3. JSON OUTPUT FORMAT — output ONLY this object, STRICT JSON ESCAPING (escape
   " as \", newlines as \n, backslash as \\; no smart quotes; no fences):
{
  "ops": [
    { "op": "move", "file": "source.md", "bullet": "- exact original line text", "toFile": "target.md" },
    { "op": "merge", "file": "source.md", "bullet": "- exact original line text", "mergedText": "- combined replacement text" },
    { "op": "delete", "file": "source.md", "bullet": "- exact original line text" }
  ]
}
An empty "ops" list is a valid answer when the vault is already tidy.`, strings.TrimRight(RenderForPromptScoped(IsOrganizableFile, nil), "\n"))
}

// dedupeBulletLines drops exact-duplicate bullet lines (by normalizeFact),
// keeping the first occurrence — a "merge" op can legitimately rewrite two
// different original bullets to the same canonical text, and this collapses
// them into one without ever removing a UNIQUE fact.
func dedupeBulletLines(content string) string {
	seen := make(map[string]bool)
	lines := strings.Split(content, "\n")
	out := lines[:0:0]
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			key := normalizeFact(t)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
