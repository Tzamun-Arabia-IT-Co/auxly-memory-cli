package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Per-file chunked organize: each category file is tidied by its OWN model call
// (same RESPONSE CONTRACT, single-file payload), so organize scales linearly
// with vault size instead of hitting one giant context window. Cross-file
// re-classification still works: a chunk call can't write other files, so it
// FLAGS facts that belong elsewhere as `moves`, and a final MECHANICAL routing
// pass (plain code, no extra LLM call) appends each moved fact to its target.
//
// PRIVACY INVARIANT — personal one-way sink: moves INTO personal.md are the
// required correction and always honored; personal.md's own chunk is prompted
// with moves FORBIDDEN, and any move it emits anyway is discarded with the fact
// restored, so content can never leave personal.md.

type chunkMove struct {
	To   string `json:"to"`
	Fact string `json:"fact"`
}

// routedMove pairs a flagged move with the index (in changes) of the file it
// came FROM, so a move that must be skipped (CRITICAL 2 — an unreadable
// target) can put the fact back where it started instead of losing it.
type routedMove struct {
	chunkMove
	fromIdx int
}

// planOrganizeChunked runs one exec call per file, sequentially (agent CLIs
// don't parallelize well), merges the per-file results and routed moves into a
// single OrganizeProposal, and validates it with the same fact-loss guard as
// the whole-vault path.
func (s *Store) planOrganizeChunked(ctx context.Context, exec organizeExecutor, files []organizeFile) (OrganizeProposal, OrganizeResult) {
	byName := make(map[string]int, len(files)) // file name → index in changes
	var changes []ProposedChange
	var pendingMoves []routedMove
	var skipNotes []string
	modelUsed := ""
	tokensUsed := 0

	// ponytail: sequential calls, each bounded by the per-call organize timeout —
	// worst case N×timeout wall-clock for N files. Parallel per-file calls (or a
	// scaled aggregate deadline) if huge vaults make this bite in practice.
	for i, f := range files {
		if s.OrganizeProgress != nil {
			s.OrganizeProgress(i+1, len(files), f.Name)
		}
		user := fmt.Sprintf("Here is the single memory file to organize:\n\n=== FILE: %s ===\n%s\n=== END ===", f.Name, f.Content)
		run, res, proceed := exec(ctx, organizeChunkSystemPrompt(f.Name), user)
		if !proceed {
			// Any per-file failure aborts the whole plan — a half-planned vault
			// proposal is worse than none, and nothing has been written yet.
			res.Message = fmt.Sprintf("organize of %s failed: %s", f.Name, res.Message)
			res.Success = false
			return OrganizeProposal{}, res
		}
		modelUsed = run.modelUsed
		tokensUsed += run.tokensUsed

		newContent, moves, err := parseChunkResponse(f.Name, run.jsonContent)
		if err != nil {
			return OrganizeProposal{}, OrganizeResult{Success: false, Message: fmt.Sprintf("organize of %s: %v", f.Name, err)}
		}

		// Enforce the one-way sink + valid targets mechanically, whatever the
		// model said: a discarded move puts its fact straight back in the file.
		var kept []chunkMove
		for _, mv := range moves {
			mv.Fact = ensureBullet(mv.Fact)
			switch {
			case f.Name == "personal.md":
				newContent = appendFact(newContent, mv.Fact) // nothing leaves personal.md
			case !IsOrganizableFile(mv.To) || mv.To == f.Name:
				newContent = appendFact(newContent, mv.Fact) // bogus target — keep, never lose
			default:
				kept = append(kept, mv)
			}
		}

		scope := "global"
		if s.isWorkspaceFile(f.Name) {
			scope = "workspace"
		}
		fromIdx := len(changes)
		byName[f.Name] = fromIdx
		changes = append(changes, ProposedChange{
			Name:       f.Name,
			OldContent: f.Content,
			NewContent: newContent,
			Scope:      scope,
			IsNew:      false,
		})
		for _, mv := range kept {
			pendingMoves = append(pendingMoves, routedMove{chunkMove: mv, fromIdx: fromIdx})
		}
	}

	// Mechanical routing pass: append every flagged fact to its target file's
	// proposed content (creating the target's change from disk if no chunk
	// touched it), skipping facts the target already holds.
	for _, mv := range pendingMoves {
		idx, ok := byName[mv.To]
		if !ok {
			old, viewErr := s.View(mv.To)
			if viewErr != nil && !errors.Is(viewErr, os.ErrNotExist) {
				// Fail closed: an unreadable target (e.g. encrypted, key
				// unreachable) must NEVER be treated as empty — that would
				// recreate the file containing ONLY the moved fact. Skip the
				// move and keep the fact in its origin file instead (safe —
				// nothing is lost).
				skipNotes = append(skipNotes, fmt.Sprintf("⚠ organize: skipped moving a fact to %s — target unreadable: %v (fact kept in %s)", mv.To, viewErr, changes[mv.fromIdx].Name))
				changes[mv.fromIdx].NewContent = appendFact(changes[mv.fromIdx].NewContent, mv.Fact)
				continue
			}
			if viewErr != nil {
				old = ""
			}
			scope := "global"
			if s.isWorkspaceFile(mv.To) {
				scope = "workspace"
			}
			byName[mv.To] = len(changes)
			idx = byName[mv.To]
			changes = append(changes, ProposedChange{
				Name:       mv.To,
				OldContent: old,
				NewContent: old,
				Scope:      scope,
				IsNew:      viewErr != nil,
			})
		}
		if !containsFact(changes[idx].NewContent, mv.Fact) {
			changes[idx].NewContent = appendFact(changes[idx].NewContent, mv.Fact)
		}
	}

	changes = s.stripPersonalLeaks(changes) // same mechanical sink guard as whole-vault
	prop := OrganizeProposal{Changes: changes, ModelUsed: modelUsed, TokensUsed: tokensUsed}
	prop.Warning = factLossWarning(prop.Changes)
	if len(skipNotes) > 0 {
		note := strings.Join(skipNotes, "\n")
		if prop.Warning != "" {
			note += "\n" + prop.Warning
		}
		prop.Warning = note
	}
	return prop, OrganizeResult{Success: true}
}

// parseChunkResponse parses one chunk call's JSON: the file's new content plus
// any flagged moves. Tolerates the same lenient repair as whole-vault parsing.
func parseChunkResponse(fileName, jsonContent string) (string, []chunkMove, error) {
	type responseFile struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	type responseObj struct {
		Files []responseFile `json:"files"`
		Moves []chunkMove    `json:"moves"`
	}
	var parsed responseObj
	if err := json.Unmarshal([]byte(jsonContent), &parsed); err != nil {
		repaired := repairAgentJSON(jsonContent)
		if err2 := json.Unmarshal([]byte(repaired), &parsed); err2 != nil || len(parsed.Files) == 0 {
			return "", nil, fmt.Errorf("failed to parse JSON output: %v\nOutput content was: %s", err, jsonContent)
		}
	}
	for _, rf := range parsed.Files {
		if rf.Name == fileName {
			return rf.Content, parsed.Moves, nil
		}
	}
	return "", nil, fmt.Errorf("model output did not include the file %s", fileName)
}

// organizeChunkSystemPrompt is the single-file variant of the organize prompt:
// same RESPONSE CONTRACT and RULE 0, but scoped to one file, with the `moves`
// mechanism replacing direct cross-file writes.
func organizeChunkSystemPrompt(fileName string) string {
	moveRules := fmt.Sprintf(`5. MOVES (cross-file re-classification): You are given ONLY %s. If a fact
   clearly belongs in a DIFFERENT taxonomy file, REMOVE it from "content" and
   list it in "moves" with its destination — never simply delete it.
   - PERSONAL IS A ONE-WAY SINK: a PRIVATE-LIFE fact about the USER as an
     individual (their own family, health, relationships, or their OWN
     legal/financial matter — personal lawsuit, divorce, custody, personal
     loan, salary) sitting in this shared file MUST be moved to personal.md.
   - When unsure where a fact belongs, KEEP IT in this file. Never move to
     resolve doubt.`, fileName)
	if fileName == "personal.md" {
		moveRules = `5. MOVES ARE FORBIDDEN FOR THIS FILE: personal.md is a one-way privacy sink —
   content NEVER leaves it. "moves" MUST be an empty list. Every fact stays in
   this file (tidied in place).`
	}
	return fmt.Sprintf(`═══ RESPONSE CONTRACT — READ FIRST, OBEY ABSOLUTELY ═══
You are a NON-INTERACTIVE text→JSON transformer, NOT an interactive agent.
- The COMPLETE file is included verbatim in this prompt below. NOTHING is
  truncated, cut off, or stored elsewhere. There are NO files on disk to open and
  you have NO tools — any attempt to read files or call tools will find nothing.
- Do NOT narrate, plan, think out loud, or explain. Do NOT write sentences like
  "Let me read…", "The input was truncated", "I'll use the MCP tools", or any
  analysis. Such output BREAKS the caller and fails the task.
- Your ENTIRE response MUST be exactly ONE JSON object and nothing else: the FIRST
  character you emit is `+"`{`"+` and the LAST is `+"`}`"+`. No prose before it, no prose
  after it, no markdown fences. Begin your reply with `+"`{`"+` immediately.

You are an expert Auxly Memory Architect. Your job is to tidy ONE file of the
user's memory vault — %s — WITHOUT EVER LOSING A SINGLE FACT.

═══ RULE 0 — ZERO LOSS (ABSOLUTE, OVERRIDES EVERYTHING) ═══
Every distinct fact, name, number, date, ID, decision, server, IP, case number,
amount, and detail present in the INPUT must still be present in your OUTPUT
(either in "content" or as a "moves" entry).
- Deleting, dropping, omitting, or truncating ANY fact is STRICTLY FORBIDDEN.
- You may improve WORDING; you may NOT remove INFORMATION.
- If you are unsure where a fact belongs, KEEP IT in this file. Never drop it
  to resolve doubt.

Other principles (all subordinate to RULE 0):
1. Tidy IN PLACE: group related facts under clear headings, fix structure.
2. DE-DUPLICATION: Merge ONLY facts that are true exact/near-exact duplicates
   (same fact stated twice). Two different facts are never merged. When merging,
   keep every unique detail from both copies.
3. BRIEFS: Rewrite verbose chronological logs into clean, structured lists — but
   preserve every distinct fact, number, and identifier. Brevity of WORDING only.
4. The taxonomy of destination files for "moves":
%s
%s
6. JSON OUTPUT FORMAT — output ONLY this object, STRICT JSON ESCAPING (escape
   " as \", newlines as \n, backslash as \\; no smart quotes; no fences):
{
  "files": [
    { "name": "%s", "content": "Full cleaned content of this file with every kept fact" }
  ],
  "moves": [
    { "to": "target-file.md", "fact": "- the exact fact text being relocated" }
  ]
}`, fileName, strings.TrimRight(RenderForPromptScoped(IsOrganizableFile, nil), "\n"), moveRules, fileName)
}

// isWorkspaceFile reports whether the file currently resolves to the workspace
// overlay (mirrors buildProposalFromJSON's scope detection).
func (s *Store) isWorkspaceFile(name string) bool {
	if s.WorkspaceRoot == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.WorkspaceRoot, name)); err == nil {
		return true
	}
	return false
}

func ensureBullet(fact string) string {
	f := strings.TrimSpace(fact)
	if f == "" {
		return f
	}
	if !strings.HasPrefix(f, "- ") && !strings.HasPrefix(f, "* ") {
		f = "- " + f
	}
	return f
}

// containsFact reports whether content already holds the fact (normalized).
func containsFact(content, fact string) bool {
	want := normalizeFact(fact)
	for _, b := range bulletLines(content) {
		if normalizeFact(b) == want {
			return true
		}
	}
	return false
}

// appendFact appends a bullet to the end of content, keeping a clean trailing
// newline structure.
func appendFact(content, fact string) string {
	if fact == "" {
		return content
	}
	c := strings.TrimRight(content, "\n")
	if c == "" {
		return fact + "\n"
	}
	return c + "\n" + fact + "\n"
}
