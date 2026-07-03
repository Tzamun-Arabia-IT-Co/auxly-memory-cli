package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/llm"
)

// providerKey canonicalizes a display agent name ("Claude Code / CLI", "Codex IDE
// Desktop", "Antigravity CLI", …) to a stable provider key used for both the safety
// allowlist and the per-agent invocation. Returns "" for unrecognized agents.
func providerKey(agentName string) string {
	switch p := strings.ToLower(agentName); {
	case strings.Contains(p, "claude"):
		return "claude"
	case strings.Contains(p, "codex"):
		return "codex"
	case strings.Contains(p, "antigravity") || strings.Contains(p, "agy"):
		return "antigravity"
	case strings.Contains(p, "gemini"):
		return "gemini"
	case strings.Contains(p, "cursor"):
		return "cursor"
	default:
		return ""
	}
}

// scrubbedOrganizeEnv returns the process env with every AUXLY_* var removed (so a
// spawned agent can't locate the real vault) plus a non-interactive, no-color shell.
func scrubbedOrganizeEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+3)
	for _, kv := range src {
		if strings.HasPrefix(kv, "AUXLY_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "CI=1", "NO_COLOR=1", "TERM=dumb")
}

// defaultOrganizeTimeout bounds a single CLI-agent consolidation run. A full-vault
// re-file is a large prompt, so this is generous; override with the env var
// AUXLY_ORGANIZE_TIMEOUT (whole seconds) for slow models or big vaults.
const defaultOrganizeTimeout = 900 * time.Second

// organizeTimeout returns the CLI-agent execution timeout, honoring
// AUXLY_ORGANIZE_TIMEOUT (seconds) when set to a positive integer.
func organizeTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("AUXLY_ORGANIZE_TIMEOUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultOrganizeTimeout
}

// buildAgentArgs returns the argv (after the binary path) to run a ONE-SHOT,
// NON-INTERACTIVE consolidation on the selected agent CLI, with the prompt as the
// final argument. Before this, only Claude got a real headless flag (`-p`); every
// other agent received the bare prompt and either errored (Codex: needs the `exec`
// subcommand) or opened interactive mode and hung to the timeout (Antigravity,
// Gemini, Cursor). Flags are verified against each CLI's --help.
//
// SECURITY: this is a pure text→JSON transform that needs NO tools, and the prompt
// embeds user-vault content (a prompt-injection vector). So we deliberately do NOT
// pass any permission-bypass flag (Claude's --dangerously-skip-permissions, agy's
// equivalent, Gemini's -y/--yolo, Cursor's --trust). In headless mode each CLI then
// auto-denies tool calls (no TTY to approve), and Codex runs under a read-only
// sandbox — so an injected "run this command" instruction cannot touch the host.
// Claude is additionally MCP-isolated (loads zero MCP servers, so it can't recurse
// into Auxly's own MCP server — the multi-minute startup that blew the old 300s cap)
// and pinned to a fast model. Note: Claude's --mcp-config is variadic, so --model
// must follow it to terminate the list before the positional prompt.
func buildAgentArgs(agentName, model, prompt string) []string {
	switch p := strings.ToLower(agentName); {
	case strings.Contains(p, "claude"):
		if model == "" {
			model = "haiku"
		}
		// Full isolation so the child only sees THIS prompt (verified clean E2E):
		//   --tools ""           → disable ALL built-in tools (no Read/Bash/etc.)
		//   --strict-mcp-config  → load zero MCP servers (no recursion into Auxly's)
		//   --setting-sources "" → load NO user/project settings, so SessionStart
		//                          hooks / CLAUDE.md can't inject the real vault into
		//                          context (this was the contamination source)
		// Each variadic flag (--mcp-config, --tools, --setting-sources) is followed by
		// another flag so it can't swallow the positional prompt.
		return []string{
			"-p",
			"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
			"--tools", "",
			"--setting-sources", "",
			"--model", model,
			prompt,
		}
	case strings.Contains(p, "codex"):
		// Codex automation mode is the `exec` subcommand; read-only sandbox blocks
		// any model-generated shell command from writing or escaping.
		// --skip-git-repo-check: the organize run executes in an isolated, non-git
		// working dir, and Codex otherwise aborts with "Not inside a trusted
		// directory and --skip-git-repo-check was not specified" (exit 1).
		args := []string{"exec", "--sandbox", "read-only", "--skip-git-repo-check"}
		if model != "" {
			args = append(args, "--model", model)
		}
		return append(args, prompt)
	case strings.Contains(p, "antigravity") || strings.Contains(p, "agy"):
		// `agy --print` runs a single prompt non-interactively (verified it exits on
		// EOF stdin); a bare prompt opens interactive mode and hangs.
		return []string{"--print", prompt}
	case strings.Contains(p, "gemini"):
		args := []string{"-p"}
		if model != "" {
			args = append(args, "-m", model)
		}
		return append(args, prompt)
	case strings.Contains(p, "cursor"):
		// SECURITY-CRITICAL flag pairing:
		//   --mode ask  → read-only Q&A mode: the agent can NOT run shell commands or
		//                 edit files, so a prompt-injection planted in the vault content
		//                 has no tools to abuse. This is what keeps cursor safe here,
		//                 since cursor's headless `-p` otherwise "has access to all tools".
		//   --trust     → cursor-agent blocks on a "Workspace Trust Required" prompt in
		//                 the fresh empty temp dir (exit 1, no output) and never emits
		//                 JSON without it. Safe ONLY because --mode ask already removed
		//                 every tool — trust over an empty, scrubbed throwaway dir grants
		//                 nothing. Never pass --trust WITHOUT --mode ask.
		args := []string{"-p", "--output-format", "text", "--mode", "ask", "--trust"}
		if model != "" {
			args = append(args, "--model", model)
		}
		return append(args, prompt)
	default:
		// Safe fallback: most agent CLIs treat `-p`/`--print` as headless mode; a
		// bare prompt opens interactive mode and hangs, so prefer `-p`.
		return []string{"-p", prompt}
	}
}

// OrganizeResult represents the outcome of the vault reorganization.
type OrganizeResult struct {
	Success    bool
	Message    string
	Diff       string
	TokensUsed int
	// Warning carries the fact-loss validator's finding (see
	// OrganizeProposal.Warning). Headless organize paths refuse to apply while
	// it is set; the TUI shows it during review.
	Warning string
}

// ProposedChange is one file's pending edit from an organize run — computed but
// NOT yet written. The review UI shows each before/after, lets the user
// approve/reject/edit, and only the approved set is written via ApplyOrganizeChanges.
type ProposedChange struct {
	Name       string // file name (e.g. "projects.md")
	OldContent string // current on-disk content ("" if new)
	NewContent string // proposed content (may be edited by the user before apply)
	Scope      string // "global" or "workspace"
	IsNew      bool   // file did not exist before
}

// Changed reports whether the proposed content actually differs from disk.
func (c ProposedChange) Changed() bool { return c.NewContent != c.OldContent }

// OrganizeProposal is the full set of pending changes from one organize run,
// computed WITHOUT writing anything to disk.
type OrganizeProposal struct {
	Changes    []ProposedChange
	ModelUsed  string
	TokensUsed int
	// Warning is set when the proposal appears to LOSE facts — the output has
	// >5% fewer bullet lines than the input (RULE 0 violation candidate; the
	// prompt promises zero loss but a weak model can drop facts silently).
	// Review UIs MUST show it before the user applies anything.
	Warning string
	// SkippedEncrypted lists organizable files excluded from this run because
	// the caller passed skipEncrypted=true to planOrganize (the CLI-agent
	// "skip encrypted file(s)" choice) — surfaced so callers can tell the
	// user which files never left the vault.
	SkippedEncrypted []string
	// DroppedDeltaOps lists delta-mode (organize_delta.go) ops that were
	// discarded because their bullet text wasn't found verbatim in the source
	// file — informational only. Deliberately NOT folded into Warning: a
	// dropped op leaves the fact exactly where it already was, so it is never
	// a fact-loss signal and must not block headless auto-apply.
	DroppedDeltaOps []string
}

// buildProposalFromJSON parses an organize model's JSON output into a set of
// proposed per-file changes WITHOUT writing anything. Path-unsafe names (absolute
// or parent-escaping) are dropped. On parse failure it returns a failed result.
func (s *Store) buildProposalFromJSON(jsonContent, modelUsed string, tokensUsed int) (OrganizeProposal, OrganizeResult) {
	type responseFile struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	type responseObj struct {
		Files []responseFile `json:"files"`
	}
	var parsed responseObj
	if err := json.Unmarshal([]byte(jsonContent), &parsed); err != nil {
		// Weaker agent models often emit unescaped quotes/newlines inside content
		// strings. Try a lenient repair before giving up; the user still reviews
		// every change, so a salvaged-but-imperfect parse can't corrupt the vault.
		repaired := repairAgentJSON(jsonContent)
		if err2 := json.Unmarshal([]byte(repaired), &parsed); err2 != nil || len(parsed.Files) == 0 {
			// Surface the original error rather than silently reporting "nothing to
			// organize" — a zero-file salvage from a failed strict parse means the
			// output was malformed or truncated, not that the vault was already tidy.
			return OrganizeProposal{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse JSON vault payload: %v\nOutput content was: %s", err, jsonContent)}
		}
	}
	prop := OrganizeProposal{ModelUsed: modelUsed, TokensUsed: tokensUsed}
	for _, rf := range parsed.Files {
		cleanedName := filepath.Clean(rf.Name)
		if strings.HasPrefix(cleanedName, "..") || filepath.IsAbs(cleanedName) {
			continue
		}
		// Defense in depth: never accept a write to a setup/instruction file or
		// agents.md even if the model returns one — organize only touches user memory.
		if !IsOrganizableFile(cleanedName) {
			continue
		}
		scope := "global"
		if s.WorkspaceRoot != "" {
			localFile := filepath.Join(s.WorkspaceRoot, cleanedName)
			if _, err := os.Stat(localFile); err == nil {
				scope = "workspace"
			}
		}
		oldContent, viewErr := s.View(rf.Name)
		prop.Changes = append(prop.Changes, ProposedChange{
			Name:       cleanedName,
			OldContent: oldContent,
			NewContent: rf.Content,
			Scope:      scope,
			IsNew:      viewErr != nil,
		})
	}
	prop.Changes = s.stripPersonalLeaks(prop.Changes)
	prop.Warning = factLossWarning(prop.Changes)
	return prop, OrganizeResult{Success: true}
}

// stripPersonalLeaks mechanically enforces the personal one-way privacy sink on
// a parsed proposal, whatever the model emitted: any bullet in a NON-personal
// file's proposed content that matches a bullet of the ORIGINAL personal.md is
// removed (the fact still lives in personal.md, so nothing is lost). Prompt
// rules alone can't be trusted — a confused or prompt-injected model could copy
// personal facts into a shared file, which factLossWarning can't see (it only
// detects MISSING facts, never leaked duplicates).
func (s *Store) stripPersonalLeaks(changes []ProposedChange) []ProposedChange {
	original, err := s.View("personal.md")
	if err != nil {
		return changes
	}
	personal := make(map[string]bool)
	for _, b := range bulletLines(original) {
		personal[normalizeFact(b)] = true
	}
	if len(personal) == 0 {
		return changes
	}
	for i, c := range changes {
		if IsPersonalFile(c.Name) {
			continue
		}
		var kept []string
		removed := false
		for _, l := range strings.Split(c.NewContent, "\n") {
			t := strings.TrimSpace(l)
			if (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")) && personal[normalizeFact(t)] {
				removed = true
				continue
			}
			kept = append(kept, l)
		}
		if removed {
			changes[i].NewContent = strings.Join(kept, "\n")
		}
	}
	return changes
}

// factLossWarning validates RULE 0 (zero fact loss) mechanically: counts bullet
// facts across the proposal's inputs vs outputs. Two triggers, either fires:
//   - aggregate: output has >5% fewer bullets than input (legitimate dedup
//     removes a few, so small shrinkage passes);
//   - per-file: one file lost >80% of its (≥3) facts with no trace anywhere in
//     the output — growth elsewhere must never mask a file being gutted.
//
// Facts are re-filed across files by design, so "missing" always means "in NO
// output file" (normalized). Rewording beyond case/whitespace can false-positive
// the per-file check — acceptable: this warns, humans decide.
func factLossWarning(changes []ProposedChange) string {
	before, after := 0, 0
	newSet := make(map[string]bool)
	for _, c := range changes {
		before += len(bulletLines(c.OldContent))
		newBullets := bulletLines(c.NewContent)
		after += len(newBullets)
		for _, b := range newBullets {
			newSet[normalizeFact(b)] = true
		}
	}
	if before == 0 {
		return ""
	}

	var missing []string
	guttedFile := ""
	for _, c := range changes {
		oldBullets := bulletLines(c.OldContent)
		fileMissing := 0
		for _, b := range oldBullets {
			if !newSet[normalizeFact(b)] {
				fileMissing++
				if len(missing) < 10 {
					missing = append(missing, b)
				}
			}
		}
		if len(oldBullets) >= 3 && fileMissing*100 > len(oldBullets)*80 && guttedFile == "" {
			guttedFile = c.Name
		}
	}

	aggregateShrink := after*100 < before*95
	if !aggregateShrink && guttedFile == "" {
		return ""
	}

	w := fmt.Sprintf("⚠ Possible fact loss: output has %d facts vs %d input (%d missing from the output entirely). Review carefully before applying.", after, before, len(missing))
	if guttedFile != "" {
		w = fmt.Sprintf("⚠ Possible fact loss: %s lost almost all of its facts with no trace elsewhere in the output (input %d facts → output %d). Review carefully before applying.", guttedFile, before, after)
	}
	if len(missing) > 0 {
		w += "\nMissing-fact candidates:\n  " + strings.Join(missing, "\n  ")
	}
	return w
}

// FactLossWarning exposes the RULE 0 validator for callers that must re-check a
// SUBSET of a proposal (the TUI validates the user's approved selection before
// applying — approving a move's source without its target would lose the fact).
func FactLossWarning(changes []ProposedChange) string {
	return factLossWarning(changes)
}

// bulletLines returns the trimmed "- " / "* " bullet lines of a memory file —
// the unit RULE 0 counts as one fact.
func bulletLines(content string) []string {
	var out []string
	for _, l := range strings.Split(content, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			out = append(out, t)
		}
	}
	return out
}

// normalizeFact makes bullet comparison robust to the cosmetic rewording an
// organize legitimately performs: bullet marker, case, and whitespace runs.
func normalizeFact(b string) string {
	b = strings.TrimPrefix(b, "- ")
	b = strings.TrimPrefix(b, "* ")
	return strings.Join(strings.Fields(strings.ToLower(b)), " ")
}

// ApplyOrganizeChanges writes the given (approved, possibly user-edited) changes to
// disk and returns a combined diff. This is the ONLY place an organize run writes —
// callers gather explicit approval first. Files whose content is unchanged are skipped.
//
// The whole batch runs under ONE vault lock so other writers can't interleave
// mid-apply, and each file is checked against its plan-time snapshot first: the
// LLM planning round-trip takes minutes, and a file edited meanwhile must not be
// silently overwritten with a proposal computed from its old content — it is
// skipped with a note in the returned diff instead.
func (s *Store) ApplyOrganizeChanges(changes []ProposedChange) string {
	var diffBuilder strings.Builder

	// Resolve the vault key before taking the lock: reading an encrypted
	// target below must never exec the keychain while every other process
	// waits on LockVault. Callers today warm the cache via gatherOrganizeFiles,
	// but that's an implicit invariant — this makes it self-contained.
	s.PrewarmCrypto()

	unlock, err := LockVault(s.Root)
	if err != nil {
		return fmt.Sprintf("⚠ nothing applied: %v\n", err)
	}
	defer unlock()

	// justSeen (Optimization 1) collects the post-apply content hash of every
	// file this run actually confirmed current — written successfully, or
	// already matching the plan with zero drift — merged into the dirty-file
	// ledger below so the NEXT organize skips it until it's actually edited
	// again. A file that hits any abort/skip/fail branch below is
	// DELIBERATELY left out: it must stay "dirty" so the next run retries it,
	// never silently marked done on a change that never actually landed.
	justSeen := make(map[string]string, len(changes))

	for _, c := range changes {
		current, viewErr := s.View(c.Name)
		if viewErr != nil {
			if !errors.Is(viewErr, os.ErrNotExist) {
				// Fail closed: a non-NotExist read error (e.g. an encrypted
				// file whose key is unreachable) must NEVER be treated as an
				// empty file — that would let the "changed while planning"
				// guard below pass trivially and overwrite the file with a
				// proposal computed from stale/empty content. Abort just this
				// change; every other file in the batch still applies.
				diffBuilder.WriteString(fmt.Sprintf("⚠ aborted %s: cannot verify current content: %v\n", c.Name, viewErr))
				continue
			}
			current = ""
		}
		if current != c.OldContent {
			diffBuilder.WriteString(fmt.Sprintf("⚠ skipped %s: file changed while organize was planning — re-run organize\n", c.Name))
			continue
		}
		if !c.Changed() {
			// Model/ops decided this file needed no edits — it's still fully
			// "organized" as of its current (verified-fresh) content, so mark
			// it seen without touching disk.
			justSeen[c.Name] = hashText(current)
			continue
		}
		if werr := s.writeScopedNoLock(c.Name, c.NewContent, c.Scope); werr != nil {
			diffBuilder.WriteString(fmt.Sprintf("⚠ failed %s: %v\n", c.Name, werr))
			continue
		}
		justSeen[c.Name] = hashText(c.NewContent)
		if d := generateDiff(c.Name, c.OldContent, c.NewContent); d != "" {
			diffBuilder.WriteString(d + "\n")
		}
	}
	if len(justSeen) > 0 {
		seen := loadOrganizeSeen(s.Root)
		for name, h := range justSeen {
			seen[name] = h
		}
		saveOrganizeSeen(s.Root, seen)
	}
	// unified_memory.md recompiles lazily on next read (Store.View mtime check).
	return diffBuilder.String()
}

// organizeSystemPrompt builds the canonical organize/re-classification system
// prompt. The category taxonomy is injected from RenderForPrompt() (the single
// source of truth) rather than hardcoded, so the file list never drifts. Used
// identically by every organize execution path (direct LLM, CLI agent, custom).
func organizeSystemPrompt() string {
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

You are an expert Auxly Memory Architect. Your job is to RE-FILE and tidy the user's memory vault so every fact lives in the right file — WITHOUT EVER LOSING A SINGLE FACT.

═══ RULE 0 — ZERO LOSS (ABSOLUTE, OVERRIDES EVERYTHING) ═══
Every distinct fact, name, number, date, ID, decision, server, IP, case number,
amount, and detail present in the INPUT must still be present in your OUTPUT.
- Deleting, dropping, omitting, or truncating ANY fact is STRICTLY FORBIDDEN.
- You may improve WORDING; you may NOT remove INFORMATION.
- A fact that seems "off-topic" for the file it sits in is NEVER deleted — you
  MOVE it to the correct file (see RE-CLASSIFICATION). Off-topic is a reason to
  RELOCATE, never to remove.
- If you are unsure where a fact belongs, KEEP IT (in its current file). Never
  drop it to resolve doubt.
- Count the facts before and after in your head: the output must contain at
  least as much information as the input. Losing one fact = task failed.

WORKED EXAMPLE (do exactly this):
  INPUT projects.md contains: "Personal loan of 5,000 from a relative, repaid
  monthly" (a personal financial matter sitting in the wrong file).
  CORRECT: remove it from projects.md AND write it verbatim into personal.md
  under a "## Finances" heading. WRONG: delete it because it isn't a software
  project. Deleting it is a critical failure.

Other principles (all subordinate to RULE 0):
1. RE-CLASSIFICATION: Place every fact in the file matching its MEANING,
   regardless of where it currently sits. Move misfiled facts to their correct
   home. The file set is fixed (do not invent/remove files); fact membership is
   yours to correct. Taxonomy:
%s
2. DE-DUPLICATION: Merge ONLY facts that are true exact/near-exact duplicates
   (same fact stated twice). Two different facts are never merged. When merging,
   keep every unique detail from both copies.
3. BRIEFS: Rewrite verbose chronological logs into clean, structured lists — but
   preserve every distinct fact, number, and identifier. Brevity of WORDING only.
4. INTEGRITY ON MOVE: Every fact ends up in EXACTLY ONE file (the correct one) —
   never dropped, never duplicated across files.
5. PERSONAL IS A ONE-WAY SINK (PRIVACY — CRITICAL):
   - If a PRIVATE-LIFE fact about the USER as an individual (their own family,
     health, relationships, or their OWN legal/financial matter — e.g. their
     personal lawsuit/court case, divorce, custody, personal loan, salary, bank
     details) is sitting in a SHARED file, you MUST MOVE it INTO personal.md.
     This is the one boundary crossing that is REQUIRED — it is a correction, not
     a violation (see the WORKED EXAMPLE above).
   - You must NEVER move a fact OUT of personal.md into a shared file. Personal
     content only ever flows TOWARD personal.md, never away from it.
   - Judge PERSONAL vs BUSINESS by CONTEXT, not by the topic word: a legal or
     financial matter about the USER or their family is PERSONAL (personal.md);
     the same topic about the COMPANY, a client, or the business is SHARED
     (business.md). When a matter is genuinely the user's private affair,
     personal.md ALWAYS wins.
6. JSON OUTPUT FORMAT: Output ONLY a single valid JSON object matching the schema
   below — no prose, no markdown fences outside the JSON. Include EVERY file you
   were given (plus personal.md if you moved personal facts into it).
   STRICT JSON ESCAPING (a single unescaped character breaks the whole result):
   - Escape every double quote inside content as \" (e.g. He said \"hi\").
   - Escape every newline inside content as \n and every tab as \t.
   - Escape every backslash as \\.
   - Do NOT use smart/curly quotes as JSON string delimiters; only straight ".
   - Emit the object as compact JSON; never wrap it in markdown fences.
{
  "files": [
    {
      "name": "filename.md",
      "content": "Full clean, consolidated, readable content — with every input fact preserved"
    }
  ]
}`, strings.TrimRight(RenderForPromptScoped(IsOrganizableFile, nil), "\n"))
}

// GetEstimatedTokens estimates token count based on vault file sizes.
func (s *Store) GetEstimatedTokens() int {
	files, err := s.List()
	if err != nil {
		return 800
	}
	var totalChars int64
	for _, f := range files {
		if !IsOrganizableFile(f.Name) {
			continue
		}
		totalChars += f.Size
	}
	// 4 characters per token + 800 tokens system prompt overhead
	return int(totalChars/4) + 800
}

// OrganizeVault executes a smart LLM consolidation batch across all memory files.
func (s *Store) OrganizeVault() OrganizeResult {
	return s.OrganizeVaultWithAgent("Direct LLM", "", false)
}

// organizeRun is the raw model output of one consolidation run, before it is
// parsed into a proposal. It carries no side effects.
type organizeRun struct {
	jsonContent string
	modelUsed   string
	tokensUsed  int
}

// organizeFile is one organizable vault file's snapshot taken at plan time.
type organizeFile struct {
	Name      string
	Content   string
	Encrypted bool // true when this file is encrypted at rest (see planOrganize's CLI-agent guard)
}

// gatherOrganizeFiles snapshots every USER-MEMORY taxonomy file that has
// changed since the last successful organize (see organize_seen.go). This is
// the stable wrapper kept for existing callers/tests — it always applies the
// dirty-file skip (forceAll=false); planOrganizeOpts is the entry point that
// can bypass it. See gatherOrganizeFilesOpts for the full contract.
func (s *Store) gatherOrganizeFiles(skipEncrypted bool) (files []organizeFile, skipped []string, err error) {
	files, skipped, _, err = s.gatherOrganizeFilesOpts(skipEncrypted, false)
	return files, skipped, err
}

// gatherOrganizeFilesOpts snapshots every USER-MEMORY taxonomy file. Setup/
// instruction files (CLAUDE.md, AGENTS.md, providers.md, …), the generated
// aggregate, and the agent-activity log (agents.md) are never read or
// reorganized.
//
// skipEncrypted, when true, excludes encrypted-at-rest files entirely
// (neither decrypted nor sent anywhere) instead of including their plaintext
// — the CLI-agent "skip encrypted file(s) this run" choice. Excluded names
// are returned in skipped.
//
// forceAll, when false (the default — Optimization 1), additionally excludes
// any file whose current content hash matches organize-seen.json: it hasn't
// changed since it was last successfully organized, so re-sending it to a
// model would just pay the (dominant) model-call cost for a no-op result.
// cleanCount reports how many organizable files were excluded this way, so
// callers can tell "nothing dirty" apart from "vault is empty". forceAll=true
// restores today's whole-vault behavior — the TUI "re-run everything" case.
func (s *Store) gatherOrganizeFilesOpts(skipEncrypted, forceAll bool) (files []organizeFile, skipped []string, cleanCount int, err error) {
	all, err := s.List()
	if err != nil {
		return nil, nil, 0, err
	}
	var seen map[string]string
	if !forceAll {
		seen = loadOrganizeSeen(s.Root)
	}
	for _, f := range all {
		if !IsOrganizableFile(f.Name) {
			continue
		}
		encrypted := s.fileIsEncrypted(f.Name)
		if skipEncrypted && encrypted {
			skipped = append(skipped, f.Name)
			continue
		}
		// ponytail: View decrypts an encrypted file, and its plaintext then
		// goes into the organize prompt sent to the LLM provider — same as
		// any recall would. Encryption-at-rest protects the file on disk,
		// not what a trusted flow does with it in memory. The Direct LLM/API
		// path still accepts that risk; planOrganize refuses the CLI-agent
		// path outright when any gathered file is encrypted (CRITICAL 3 —
		// that path would additionally expose the decrypted content on the
		// spawned process's argv, ps-visible for the run's whole duration)
		// unless skipEncrypted excluded it above or the caller ran
		// TempDecryptForOrganize first (so it's plaintext on disk already).
		content, verr := s.View(f.Name)
		if verr != nil {
			continue
		}
		if !forceAll {
			if h, ok := seen[f.Name]; ok && h == hashText(content) {
				cleanCount++
				continue
			}
		}
		files = append(files, organizeFile{Name: f.Name, Content: content, Encrypted: encrypted})
	}
	return files, skipped, cleanCount, nil
}

// EncryptedOrganizableFiles returns the names of organizable vault files (see
// IsOrganizableFile) that are currently encrypted at rest — a cheap header
// sniff, no key required. Callers (the TUI organize tab, `auxly organize`)
// use this to warn/offer choices BEFORE picking a CLI-agent provider, since
// planOrganize's guard refuses outright once any are present.
func (s *Store) EncryptedOrganizableFiles() []string {
	files, err := s.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range files {
		if IsOrganizableFile(f.Name) && s.fileIsEncrypted(f.Name) {
			out = append(out, f.Name)
		}
	}
	return out
}

// encryptedOrganizeFileNames returns the names of gathered files that are
// encrypted at rest.
func encryptedOrganizeFileNames(files []organizeFile) []string {
	var out []string
	for _, f := range files {
		if f.Encrypted {
			out = append(out, f.Name)
		}
	}
	return out
}

func vaultUserPrompt(files []organizeFile) string {
	var vaultPayload strings.Builder
	for _, f := range files {
		vaultPayload.WriteString(fmt.Sprintf("=== FILE: %s ===\n%s\n=== END ===\n\n", f.Name, f.Content))
	}
	return fmt.Sprintf("Here is the current memory vault contents to organize:\n\n%s", vaultPayload.String())
}

// runOrganizeModel runs the chosen model (CLI agent when agentPath != "", else
// a direct LLM API) over the given prompts. It performs NO disk writes. The
// returned bool `proceed` reports whether a model output is ready to be parsed
// into a proposal.
func (s *Store) runOrganizeModel(ctx context.Context, agentName string, agentPath string, model string, systemPrompt, userPrompt string) (organizeRun, OrganizeResult, bool) {
	fullPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, userPrompt)

	var jsonContent string
	var modelUsed string
	var tokensUsed int

	// 2. Route Execution based on agentPath presence
	if agentPath != "" {
		// Guard: only fork/exec an actual executable file. A config directory
		// (e.g. ~/.gemini/antigravity-cli) would fail with a cryptic
		// "permission denied"; fail clearly instead.
		if fi, statErr := os.Stat(agentPath); statErr != nil {
			return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s is not runnable: %v", agentName, statErr)}, false
		} else if fi.IsDir() {
			return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s path is a directory, not an executable: %s", agentName, agentPath)}, false
		}

		// Run via CLI command with the verified per-agent headless invocation. The
		// timeout is layered on the caller's context so either the deadline OR a user
		// cancel (esc on the running screen) tears the subprocess down.
		timeout := organizeTimeout()

		// Modern agentic CLIs (Claude Code, Antigravity, …) tend to IGNORE a buried
		// "output JSON only" rule and instead narrate ("Let me read the files…",
		// "input was truncated", "I'll use the MCP tools"). That prose has no JSON
		// object, so extractJSON yields garbage and the parse fails. The hardened
		// RESPONSE CONTRACT at the top of the prompt suppresses most of it; this loop
		// is the safety net: if the first reply still isn't a JSON object, retry ONCE
		// with a blunt corrective prepended. A timeout / user-cancel / exec failure is
		// fatal and never retried.
		var output string
		for attempt := 1; attempt <= 2; attempt++ {
			runCtx, cancel := context.WithTimeout(ctx, timeout)

			prompt := fullPrompt
			if attempt > 1 {
				prompt = "YOUR PREVIOUS REPLY WAS REJECTED: it contained prose/narration, not JSON. " +
					"Do NOT explain, do NOT narrate, do NOT mention reading files or tools. " +
					"Reply with ONLY the single JSON object now — first character `{`, last character `}`.\n\n" + fullPrompt
			}

			cmd := exec.CommandContext(runCtx, agentPath, buildAgentArgs(agentName, model, prompt)...)

			// ISOLATION: empty stdin (no TTY hang), a scrubbed env with no AUXLY_* (so the
			// agent can't locate the real vault), and an empty working directory (so any
			// relative file read finds nothing). Combined with the per-agent no-tools flags
			// this keeps the child to a pure text→JSON transform.
			cmd.Stdin = strings.NewReader("")
			cmd.Env = scrubbedOrganizeEnv()
			if workDir, err := os.MkdirTemp("", "auxly-organize-"); err == nil {
				defer os.RemoveAll(workDir)
				cmd.Dir = workDir
			}
			// Give a killed process a moment to flush before its pipes are closed.
			cmd.WaitDelay = 5 * time.Second

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			execErr := cmd.Run()

			// Distinguish a timeout (deadline on runCtx) and a user cancel (parent ctx
			// cancelled via esc) from a genuine execution failure. Check runCtx — the
			// parent ctx is never DeadlineExceeded since the deadline lives on the child.
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				cancel()
				return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s execution timed out after %s. Stderr: %s", agentName, timeout, strings.TrimSpace(stderr.String()))}, false
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				cancel()
				return organizeRun{}, OrganizeResult{Success: false, Message: "Run cancelled."}, false
			}
			if execErr != nil {
				cancel()
				return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s execution failed: %v\nStderr: %s\nStdout: %s", agentName, execErr, stderr.String(), stdout.String())}, false
			}
			cancel()

			output = stdout.String()
			if strings.TrimSpace(output) == "" {
				return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s returned empty output.", agentName)}, false
			}

			// Accept as soon as we have a parseable JSON object; otherwise loop to retry.
			if candidate := extractJSON(output); json.Valid([]byte(candidate)) {
				output = candidate
				break
			}
		}

		jsonContent = extractJSON(output)
		modelUsed = agentName
		tokensUsed = (len(fullPrompt) + len(jsonContent)) / 4
	} else {
		// Run via direct LLM API calls (Ollama / OpenAI / Gemini).
		// Endpoint resolution (base URL, API key, default model) and the
		// self-healing model selector are shared with internal/embed via the
		// internal/llm package.
		endpoint := llm.ResolveEndpoint()
		apiURL := endpoint.ChatURL()
		apiKey := endpoint.APIKey

		// Dynamic self-healing model selector: query installed models on Ollama/vLLM to prevent 404s!
		model := llm.SelfHealModel(endpoint.ModelsURL(), endpoint.Model)

		type msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		type reqPayload struct {
			Model          string `json:"model"`
			Messages       []msg  `json:"messages"`
			ResponseFormat *struct {
				Type string `json:"type"`
			} `json:"response_format,omitempty"`
		}

		payload := reqPayload{
			Model: model,
			Messages: []msg{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: userPrompt},
			},
		}

		payload.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}

		jsonData, err := json.Marshal(payload)
		if err != nil {
			return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to encode request payload: %v", err)}, false
		}

		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to create HTTP request: %v", err)}, false
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		httpClient := &http.Client{Timeout: 300 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("LLM service is unreachable at %s: %v", apiURL, err)}, false
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("LLM request failed (Status %d): %s", resp.StatusCode, string(bodyBytes))}, false
		}

		type chatChoice struct {
			Message msg `json:"message"`
		}
		type chatUsage struct {
			TotalTokens int `json:"total_tokens"`
		}
		type chatResponse struct {
			Choices []chatChoice `json:"choices"`
			Usage   chatUsage    `json:"usage"`
		}

		var chatResp chatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse chat response: %v", err)}, false
		}

		if len(chatResp.Choices) == 0 {
			return organizeRun{}, OrganizeResult{Success: false, Message: "LLM returned an empty response choices list."}, false
		}

		llmJSONContent := chatResp.Choices[0].Message.Content
		llmJSONContent = strings.TrimPrefix(llmJSONContent, "```json")
		llmJSONContent = strings.TrimPrefix(llmJSONContent, "```")
		llmJSONContent = strings.TrimSuffix(llmJSONContent, "```")
		jsonContent = strings.TrimSpace(llmJSONContent)
		modelUsed = model
		tokensUsed = chatResp.Usage.TotalTokens
		if tokensUsed == 0 {
			tokensUsed = (len(fullPrompt) + len(jsonContent)) / 4
		}
	}

	return organizeRun{jsonContent: jsonContent, modelUsed: modelUsed, tokensUsed: tokensUsed}, OrganizeResult{}, true
}

// organizeExecutor runs ONE model call over the given prompts — the transport
// (CLI agent, provider chat API, custom endpoint) is the closure's business.
type organizeExecutor func(ctx context.Context, systemPrompt, userPrompt string) (organizeRun, OrganizeResult, bool)

// OrganizeRunOpts are the opt-in knobs for a single organize run, layered on
// top of the stable planOrganize/PlanOrganizeWithAgent signatures so every
// existing caller (cmd/, tui/, tests) keeps compiling untouched. Both default
// false — see planOrganizeOpts and organize_delta.go for why DeltaMode ships
// OFF this release.
type OrganizeRunOpts struct {
	// ForceAll bypasses the dirty-file ledger (organize_seen.go) and replans
	// every organizable file, not just ones changed since the last successful
	// organize. Wired for the TUI's "re-run everything" action.
	ForceAll bool
	// DeltaMode asks the model for small move/merge/delete OPERATIONS instead
	// of rewriting every file's full content (organize_delta.go) — the
	// biggest latency lever, and the highest risk, so it stays opt-in.
	DeltaMode bool
}

// planOrganize is the shared organize planner, forceAll/deltaMode both off —
// kept as the stable entry point for existing callers/tests. See
// planOrganizeOpts for the full contract.
func (s *Store) planOrganize(ctx context.Context, agentPath string, skipEncrypted bool, exec organizeExecutor) (OrganizeProposal, OrganizeResult) {
	return s.planOrganizeOpts(ctx, agentPath, skipEncrypted, OrganizeRunOpts{}, exec)
}

// planOrganizeOpts is planOrganize with OrganizeRunOpts: snapshot the vault
// (honoring the dirty-file ledger unless ForceAll), then either one whole-vault
// model call (small vaults — existing behavior, or DeltaMode's op-based
// variant), or one call PER FILE when the payload exceeds the chunk threshold,
// so organize scales to any vault size instead of hitting model context limits.
//
// agentPath mirrors runOrganizeModel's: non-empty means a CLI agent will run
// this plan as a subprocess. CRITICAL 3: that subprocess receives the whole
// prompt (decrypted vault content included) as its FINAL ARGV ELEMENT, which
// stays ps-visible to any other local user/process for the run's entire
// duration (up to organizeTimeout, 900s by default) — unlike the Direct LLM/
// API path, which only sends it over an HTTPS body. So a CLI-agent run is
// refused outright when any gathered file is encrypted, BEFORE either the
// whole-vault or the chunked call is made (one guard covers both) — UNLESS
// skipEncrypted already excluded them (gatherOrganizeFiles) or the caller
// ran TempDecryptForOrganize first so nothing gathered is encrypted anymore.
func (s *Store) planOrganizeOpts(ctx context.Context, agentPath string, skipEncrypted bool, opts OrganizeRunOpts, exec organizeExecutor) (OrganizeProposal, OrganizeResult) {
	files, skipped, cleanCount, err := s.gatherOrganizeFilesOpts(skipEncrypted, opts.ForceAll)
	if err != nil {
		return OrganizeProposal{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to list files: %v", err)}
	}
	if len(files) == 0 {
		switch {
		case cleanCount > 0:
			// Optimization 1's payoff: nothing dirty since the last successful
			// organize — skip the model call entirely instead of re-sending an
			// already-tidy vault.
			return OrganizeProposal{}, OrganizeResult{Success: true, Message: fmt.Sprintf("already tidy — %d file(s) unchanged since last run", cleanCount)}
		case len(skipped) > 0:
			return OrganizeProposal{}, OrganizeResult{Success: true, Message: fmt.Sprintf("Nothing to organize — %d encrypted file(s) skipped: %s", len(skipped), strings.Join(skipped, ", "))}
		default:
			return OrganizeProposal{}, OrganizeResult{Success: true, Message: "Memory vault is empty. Nothing to organize."}
		}
	}
	if agentPath != "" {
		if enc := encryptedOrganizeFileNames(files); len(enc) > 0 {
			return OrganizeProposal{}, OrganizeResult{Success: false, Message: fmt.Sprintf(
				"organize via a CLI agent would expose decrypted content on the process command line; "+
					"use the Direct LLM provider, skip them with --skip-encrypted, decrypt just for this run with "+
					"--decrypt-temporarily, or decrypt first (encrypted: %s)",
				strings.Join(enc, ", "))}
		}
	}

	var prop OrganizeProposal
	var res OrganizeResult
	switch {
	case len(files) > 1 && estimateVaultTokens(files) > organizeChunkThreshold():
		// Chunked path already pays its own cost-per-call in file count, not
		// payload size, so DeltaMode (a payload-size lever) doesn't apply here.
		prop, res = s.planOrganizeChunked(ctx, exec, files)
	case opts.DeltaMode:
		prop, res = s.planOrganizeDelta(ctx, exec, files)
	default:
		run, r, proceed := exec(ctx, organizeSystemPrompt(), vaultUserPrompt(files))
		if !proceed {
			return OrganizeProposal{}, r
		}
		prop, res = s.buildProposalFromJSON(run.jsonContent, run.modelUsed, run.tokensUsed)
	}
	if res.Success {
		prop.SkippedEncrypted = skipped
	}
	return prop, res
}

func estimateVaultTokens(files []organizeFile) int {
	total := 0
	for _, f := range files {
		total += len(f.Content)
	}
	return total/4 + 800
}

// organizeChunkThreshold is the estimated-token size above which organize
// switches from one whole-vault model call to one call per file. Large vaults
// in a single payload risk model context limits and degrade output quality
// (the v1.1.5 field lesson). Override with AUXLY_ORGANIZE_CHUNK_TOKENS.
func organizeChunkThreshold() int {
	if v := os.Getenv("AUXLY_ORGANIZE_CHUNK_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 12000
}

func (s *Store) PlanOrganizeWithAgent(ctx context.Context, agentName, agentPath, model string, skipEncrypted bool) (OrganizeProposal, OrganizeResult) {
	return s.PlanOrganizeWithAgentOpts(ctx, agentName, agentPath, model, skipEncrypted, OrganizeRunOpts{})
}

// PlanOrganizeWithAgentOpts is PlanOrganizeWithAgent with OrganizeRunOpts —
// the TUI's "re-run everything" (ForceAll) and the dark-launched delta
// response contract (DeltaMode) both thread through here without touching
// PlanOrganizeWithAgent's stable signature.
func (s *Store) PlanOrganizeWithAgentOpts(ctx context.Context, agentName, agentPath, model string, skipEncrypted bool, opts OrganizeRunOpts) (OrganizeProposal, OrganizeResult) {
	return s.planOrganizeOpts(ctx, agentPath, skipEncrypted, opts, func(c context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		return s.runOrganizeModel(c, agentName, agentPath, model, sys, user)
	})
}

func (s *Store) OrganizeVaultWithAgent(agentName, agentPath string, skipEncrypted bool) OrganizeResult {
	prop, res := s.PlanOrganizeWithAgent(context.Background(), agentName, agentPath, "", skipEncrypted)
	if !res.Success {
		return res
	}
	if len(prop.Changes) == 0 {
		return res
	}
	if blocked, br := blockOnFactLoss(prop); blocked {
		return br
	}
	diff := s.ApplyOrganizeChanges(prop.Changes)
	msg := fmt.Sprintf("✓ Memory vault organized successfully using %s!", prop.ModelUsed)
	if len(prop.SkippedEncrypted) > 0 {
		msg += fmt.Sprintf(" (%d encrypted file(s) skipped: %s)", len(prop.SkippedEncrypted), strings.Join(prop.SkippedEncrypted, ", "))
	}
	return OrganizeResult{Success: true, Message: msg, Diff: diff, TokensUsed: prop.TokensUsed, Warning: prop.Warning}
}

// blockOnFactLoss enforces RULE 0 on the HEADLESS organize paths: these apply
// without a human review step, so a proposal flagged by the fact-loss validator
// must never be written blind. The interactive TUI path is exempt — it shows
// the warning and lets the user decide per file. AUXLY_ORGANIZE_FORCE=1
// overrides after the user has seen the warning once.
func blockOnFactLoss(prop OrganizeProposal) (bool, OrganizeResult) {
	if prop.Warning == "" || os.Getenv("AUXLY_ORGANIZE_FORCE") == "1" {
		return false, OrganizeResult{}
	}
	return true, OrganizeResult{
		Success: false,
		Warning: prop.Warning,
		Message: prop.Warning + "\n\nNothing was written. Review the proposal in the dashboard (auxly → Memory Org), or re-run with AUXLY_ORGANIZE_FORCE=1 to apply anyway.",
	}
}

// extractJSON isolates the JSON object from any markdown code fences or surrounding
// prose/log noise an agent CLI may print. It first unwraps a ```json fence, then
// returns the FIRST balanced top-level object via a string-aware brace scan, so
// trailing prose — even prose that itself contains braces (e.g. "Let me know if
// {…}") — is dropped instead of being concatenated onto the payload. Falls back to
// the first/last brace span when no balanced object is found.
func extractJSON(input string) string {
	if startIdx := strings.Index(input, "```json"); startIdx != -1 {
		rest := input[startIdx+7:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			input = rest[:endIdx]
		}
	} else if startIdx := strings.Index(input, "```"); startIdx != -1 {
		rest := input[startIdx+3:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			input = rest[:endIdx]
		}
	}
	if obj := firstBalancedObject(input); obj != "" {
		return obj
	}
	firstBrace := strings.Index(input, "{")
	lastBrace := strings.LastIndex(input, "}")
	if firstBrace != -1 && lastBrace != -1 && lastBrace > firstBrace {
		return input[firstBrace : lastBrace+1]
	}
	return strings.TrimSpace(input)
}

// firstBalancedObject returns the first brace-balanced {...} span, honoring JSON
// string literals and escapes so braces inside string values never skew the depth
// count. Returns "" if no balanced object is present.
func firstBalancedObject(input string) string {
	start := strings.IndexByte(input, '{')
	if start == -1 {
		return ""
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(input); i++ {
		c := input[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return input[start : i+1]
			}
		}
	}
	return ""
}

// repairAgentJSON makes a best-effort repair of the two JSON faults weaker agent
// models most often emit in their content strings: (1) raw control characters
// (literal newline/tab/CR) that JSON forbids inside a string, and (2) unescaped
// interior double quotes (e.g. a phrase the model wrote as "Loading" inside a
// content value), which prematurely close the string and desync the parser.
//
// A quote is treated as a real string DELIMITER only when the next non-space
// character is JSON-structural (, : } ]) or end-of-input; any other interior quote
// is escaped. This is a heuristic, not a parser, so it can mis-handle content that
// legitimately contains "...", patterns — but it only runs AFTER a strict parse has
// already failed, and the user reviews every resulting change before anything is
// written, so an imperfect salvage can never silently corrupt the vault.
func repairAgentJSON(input string) string {
	var b strings.Builder
	b.Grow(len(input) + 16)
	inStr := false
	escaped := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if !inStr {
			b.WriteByte(c)
			if c == '"' {
				inStr = true
			}
			continue
		}
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			escaped = true
		case '"':
			if isJSONStringCloser(input, i+1) {
				b.WriteByte(c)
				inStr = false
			} else {
				b.WriteString(`\"`)
			}
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// isJSONStringCloser reports whether the quote preceding index j closes a string,
// i.e. the next non-whitespace byte is a JSON structural token or end-of-input.
func isJSONStringCloser(s string, j int) bool {
	for j < len(s) {
		switch s[j] {
		case ' ', '\t', '\n', '\r':
			j++
		case ',', ':', '}', ']':
			return true
		default:
			return false
		}
	}
	return true
}

func normalizeOrganizeChatURL(endpoint string) string {
	apiURL := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(apiURL, "/v1/chat/completions") && !strings.HasSuffix(apiURL, "/chat/completions") {
		apiURL = apiURL + "/v1/chat/completions"
	}
	return apiURL
}

// resolveOrganizeProvider maps a provider id (+ custom URL) to an OpenAI-compatible
// base URL and API key. It never writes; failures are returned as OrganizeResult.
func resolveOrganizeProvider(provider, customURL string) (baseURL, apiKey string, res OrganizeResult, ok bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return "http://localhost:11434", "", OrganizeResult{}, true
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return "", "", OrganizeResult{Success: false, Message: "OPENAI_API_KEY not set"}, false
		}
		return "https://api.openai.com", key, OrganizeResult{}, true
	case "gemini":
		key := os.Getenv("GEMINI_API_KEY")
		if key == "" {
			return "", "", OrganizeResult{Success: false, Message: "GEMINI_API_KEY not set"}, false
		}
		return "https://generativelanguage.googleapis.com/v1beta/openai", key, OrganizeResult{}, true
	case "custom":
		base := strings.TrimRight(strings.TrimSpace(customURL), "/")
		if base == "" {
			return "", "", OrganizeResult{Success: false, Message: "Custom endpoint URL is empty"}, false
		}
		return base, os.Getenv("AUXLY_LLM_KEY"), OrganizeResult{}, true
	default:
		return "", "", OrganizeResult{Success: false, Message: fmt.Sprintf("Unknown organize provider %q", provider)}, false
	}
}

func (s *Store) runOrganizeChat(ctx context.Context, apiURL, model, apiKey string, systemPrompt, userPrompt string) (organizeRun, OrganizeResult, bool) {
	fullPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, userPrompt)

	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type reqPayload struct {
		Model          string `json:"model"`
		Messages       []msg  `json:"messages"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format,omitempty"`
	}

	payload := reqPayload{
		Model: model,
		Messages: []msg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	payload.ResponseFormat = &struct {
		Type string `json:"type"`
	}{Type: "json_object"}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to encode request payload: %v", err)}, false
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to create HTTP request: %v", err)}, false
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpClient := &http.Client{Timeout: 300 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("LLM service is unreachable at %s: %v", apiURL, err)}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("LLM request failed (Status %d): %s", resp.StatusCode, string(bodyBytes))}, false
	}

	type chatChoice struct {
		Message msg `json:"message"`
	}
	type chatUsage struct {
		TotalTokens int `json:"total_tokens"`
	}
	type chatResponse struct {
		Choices []chatChoice `json:"choices"`
		Usage   chatUsage    `json:"usage"`
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse chat response: %v", err)}, false
	}

	if len(chatResp.Choices) == 0 {
		return organizeRun{}, OrganizeResult{Success: false, Message: "LLM returned an empty response choices list."}, false
	}

	llmJSONContent := chatResp.Choices[0].Message.Content
	llmJSONContent = strings.TrimPrefix(llmJSONContent, "```json")
	llmJSONContent = strings.TrimPrefix(llmJSONContent, "```")
	llmJSONContent = strings.TrimSuffix(llmJSONContent, "```")
	jsonContent := strings.TrimSpace(llmJSONContent)

	tokensUsed := chatResp.Usage.TotalTokens
	if tokensUsed == 0 {
		tokensUsed = (len(fullPrompt) + len(jsonContent)) / 4
	}

	return organizeRun{jsonContent: jsonContent, modelUsed: model, tokensUsed: tokensUsed}, OrganizeResult{}, true
}

func (s *Store) PlanOrganizeWithProvider(ctx context.Context, provider, customURL, model string) (OrganizeProposal, OrganizeResult) {
	baseURL, apiKey, res, ok := resolveOrganizeProvider(provider, customURL)
	if !ok {
		return OrganizeProposal{}, res
	}
	return s.planOrganize(ctx, "", false, func(c context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		return s.runOrganizeChat(c, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", model, apiKey, sys, user)
	})
}

func (s *Store) PlanOrganizeWithCustom(ctx context.Context, endpoint, model string) (OrganizeProposal, OrganizeResult) {
	return s.planOrganize(ctx, "", false, func(c context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		return s.runOrganizeChat(c, normalizeOrganizeChatURL(endpoint), model, os.Getenv("AUXLY_LLM_KEY"), sys, user)
	})
}

// OrganizeVaultWithCustom performs memory vault consolidation against a custom HTTP LLM API (like local Ollama, LM Studio, etc.).
func (s *Store) OrganizeVaultWithCustom(endpoint string, model string) OrganizeResult {
	prop, res := s.PlanOrganizeWithCustom(context.Background(), endpoint, model)
	if !res.Success {
		return res
	}
	if len(prop.Changes) == 0 {
		return res
	}
	if blocked, br := blockOnFactLoss(prop); blocked {
		return br
	}
	diff := s.ApplyOrganizeChanges(prop.Changes)
	return OrganizeResult{Success: true, Message: fmt.Sprintf("✓ Memory vault organized successfully using custom model %s!", prop.ModelUsed), Diff: diff, TokensUsed: prop.TokensUsed, Warning: prop.Warning}
}

// generateDiff creates a clean line-by-line de-duplication diff showing exactly which lines were removed (-) and added (+).
func generateDiff(filename, oldStr, newStr string) string {
	if oldStr == newStr {
		return ""
	}
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	var diff strings.Builder
	diff.WriteString(fmt.Sprintf("### 📄 %s\n", filename))
	diff.WriteString("```diff\n")

	oldMap := make(map[string]bool)
	for _, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			oldMap[strings.TrimSpace(l)] = true
		}
	}

	newMap := make(map[string]bool)
	for _, l := range newLines {
		if strings.TrimSpace(l) != "" {
			newMap[strings.TrimSpace(l)] = true
		}
	}

	deletedCount := 0
	for _, l := range oldLines {
		tr := strings.TrimSpace(l)
		if tr != "" && !newMap[tr] {
			diff.WriteString(fmt.Sprintf("- %s\n", l))
			deletedCount++
		}
	}

	addedCount := 0
	for _, l := range newLines {
		tr := strings.TrimSpace(l)
		if tr != "" && !oldMap[tr] {
			diff.WriteString(fmt.Sprintf("+ %s\n", l))
			addedCount++
		}
	}

	diff.WriteString("```\n")
	if deletedCount == 0 && addedCount == 0 {
		return ""
	}
	return diff.String()
}
