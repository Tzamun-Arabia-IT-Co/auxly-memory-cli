package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// organizeChunkMaxWorkers bounds per-file organize concurrency. Each worker
// forks/execs a full CLI-agent process (Claude Code, Codex, …) or opens an
// HTTP connection to a provider — scaling with runtime.NumCPU alone could
// fork-bomb a laptop on a big vault or blow through a provider's rate limit,
// so this caps the pool at a small fixed ceiling regardless of core count.
const organizeChunkMaxWorkers = 4

// organizeChunkWorkers returns the worker pool size: min(organizeChunkMaxWorkers,
// NumCPU), never less than 1.
func organizeChunkWorkers() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	if n > organizeChunkMaxWorkers {
		return organizeChunkMaxWorkers
	}
	return n
}

// chunkCallResult is one worker's outcome for one file, collected by index
// (not completion order) so the merge pass below stays deterministic
// regardless of which goroutine finishes first.
type chunkCallResult struct {
	newContent string
	moves      []chunkMove
	modelUsed  string
	tokensUsed int
	err        error // non-nil = this file's call failed; collected, not fatal to the batch
}

// planOrganizeChunked runs one exec call per file over a small bounded worker
// pool (organizeChunkWorkers), merges the per-file results and routed moves
// into a single OrganizeProposal, and validates it with the same fact-loss
// guard as the whole-vault path.
//
// Each call is independent: runOrganizeModel allocates its own stdout/stderr
// buffers, temp dir, and HTTP client per invocation (verified — nothing
// shared across calls), so no extra locking is needed around exec itself.
// s.OrganizeProgress may be called from multiple workers concurrently, so
// calls to it are serialized with progressMu — the callback's own internals
// (e.g. a TUI redraw) are not assumed to be goroutine-safe.
//
// One file's call failing must not sink the batch: its error is collected
// and the file is left out of the proposal (so it stays untouched on disk and
// the dirty-file ledger never marks it seen — it's simply retried next run),
// while every other file's result still gets applied.
func (s *Store) planOrganizeChunked(ctx context.Context, exec organizeExecutor, files []organizeFile) (OrganizeProposal, OrganizeResult) {
	results := make([]chunkCallResult, len(files))
	var progressMu sync.Mutex
	sem := make(chan struct{}, organizeChunkWorkers())
	var wg sync.WaitGroup

	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f organizeFile) {
			defer wg.Done()
			defer func() { <-sem }()

			if s.OrganizeProgress != nil {
				progressMu.Lock()
				s.OrganizeProgress(i+1, len(files), f.Name)
				progressMu.Unlock()
			}

			user := fmt.Sprintf("Here is the single memory file to organize:\n\n=== FILE: %s ===\n%s\n=== END ===", f.Name, f.Content)
			run, res, proceed := exec(ctx, organizeChunkSystemPrompt(f.Name), user)
			if !proceed {
				results[i] = chunkCallResult{err: fmt.Errorf("organize of %s failed: %s", f.Name, res.Message)}
				return
			}
			newContent, moves, err := parseChunkResponse(f.Name, run.jsonContent)
			if err != nil {
				results[i] = chunkCallResult{err: fmt.Errorf("organize of %s: %w", f.Name, err)}
				return
			}
			results[i] = chunkCallResult{newContent: newContent, moves: moves, modelUsed: run.modelUsed, tokensUsed: run.tokensUsed}
		}(i, f)
	}
	wg.Wait()

	byName := make(map[string]int, len(files)) // file name → index in changes
	var changes []ProposedChange
	var pendingMoves []routedMove
	var skipNotes []string
	modelUsed := ""
	tokensUsed := 0

	// Merge results IN INDEX ORDER (not completion order) so output is
	// deterministic regardless of goroutine scheduling.
	for i, f := range files {
		r := results[i]
		if r.err != nil {
			skipNotes = append(skipNotes, fmt.Sprintf("⚠ organize: %v — %s left unchanged this run, will retry next time", r.err, f.Name))
			continue
		}
		modelUsed = r.modelUsed
		tokensUsed += r.tokensUsed
		newContent := r.newContent

		// Enforce the one-way sink + valid targets mechanically, whatever the
		// model said: a discarded move puts its fact straight back in the file.
		var kept []chunkMove
		for _, mv := range r.moves {
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

	// Every file failed — nothing to propose, so report it as a hard failure
	// rather than a silent empty success.
	if len(changes) == 0 && len(skipNotes) > 0 {
		return OrganizeProposal{}, OrganizeResult{Success: false, Message: strings.Join(skipNotes, "\n")}
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
