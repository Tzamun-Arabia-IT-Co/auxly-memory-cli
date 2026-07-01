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
	prop.Warning = factLossWarning(prop.Changes)
	return prop, OrganizeResult{Success: true}
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

	unlock, err := LockVault(s.Root)
	if err != nil {
		return fmt.Sprintf("⚠ nothing applied: %v\n", err)
	}
	defer unlock()

	wrote := false
	for _, c := range changes {
		if !c.Changed() {
			continue
		}
		current, viewErr := s.View(c.Name)
		if viewErr != nil {
			current = ""
		}
		if current != c.OldContent {
			diffBuilder.WriteString(fmt.Sprintf("⚠ skipped %s: file changed while organize was planning — re-run organize\n", c.Name))
			continue
		}
		if werr := s.writeScopedNoLock(c.Name, c.NewContent, c.Scope); werr != nil {
			diffBuilder.WriteString(fmt.Sprintf("⚠ failed %s: %v\n", c.Name, werr))
			continue
		}
		wrote = true
		if d := generateDiff(c.Name, c.OldContent, c.NewContent); d != "" {
			diffBuilder.WriteString(d + "\n")
		}
	}
	if wrote {
		_ = s.CompileUnified() // once for the batch, while still holding the lock
	}
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
	return s.OrganizeVaultWithAgent("Direct LLM", "")
}

// organizeRun is the raw model output of one consolidation run, before it is
// parsed into a proposal. It carries no side effects.
type organizeRun struct {
	jsonContent string
	modelUsed   string
	tokensUsed  int
}

// runOrganizeModel gathers the vault, builds the prompt, and runs the chosen
// model (CLI agent when agentPath != "", else a direct LLM API). It performs NO
// disk writes. The returned bool `proceed` reports whether a model output is
// ready to be parsed into a proposal: it is false both on error (res.Success
// false) and on the benign empty-vault case (res.Success true with a message),
// so callers can short-circuit on either without parsing.
func (s *Store) runOrganizeModel(ctx context.Context, agentName string, agentPath string, model string) (organizeRun, OrganizeResult, bool) {
	// 1. Gather all files and compile them into a unified payload
	files, err := s.List()
	if err != nil {
		return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to list files: %v", err)}, false
	}

	var vaultPayload strings.Builder
	for _, f := range files {
		// Only send USER-MEMORY taxonomy files. Setup/instruction files
		// (CLAUDE.md, AGENTS.md, providers.md, …), the generated aggregate, and the
		// agent-activity log (agents.md) are never read or reorganized.
		if !IsOrganizableFile(f.Name) {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}
		vaultPayload.WriteString(fmt.Sprintf("=== FILE: %s ===\n%s\n=== END ===\n\n", f.Name, content))
	}

	if vaultPayload.Len() == 0 {
		return organizeRun{}, OrganizeResult{Success: true, Message: "Memory vault is empty. Nothing to organize."}, false
	}

	systemPrompt := organizeSystemPrompt()

	userPrompt := fmt.Sprintf("Here is the current memory vault contents to organize:\n\n%s", vaultPayload.String())
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

func (s *Store) PlanOrganizeWithAgent(ctx context.Context, agentName, agentPath, model string) (OrganizeProposal, OrganizeResult) {
	run, res, proceed := s.runOrganizeModel(ctx, agentName, agentPath, model)
	if !proceed {
		return OrganizeProposal{}, res
	}
	return s.buildProposalFromJSON(run.jsonContent, run.modelUsed, run.tokensUsed)
}

func (s *Store) OrganizeVaultWithAgent(agentName, agentPath string) OrganizeResult {
	prop, res := s.PlanOrganizeWithAgent(context.Background(), agentName, agentPath, "")
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
	return OrganizeResult{Success: true, Message: fmt.Sprintf("✓ Memory vault organized successfully using %s!", prop.ModelUsed), Diff: diff, TokensUsed: prop.TokensUsed, Warning: prop.Warning}
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

func (s *Store) runOrganizeChat(ctx context.Context, apiURL, model, apiKey string) (organizeRun, OrganizeResult, bool) {
	files, err := s.List()
	if err != nil {
		return organizeRun{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to list files: %v", err)}, false
	}

	var vaultPayload strings.Builder
	for _, f := range files {
		// Only send USER-MEMORY taxonomy files. Setup/instruction files
		// (CLAUDE.md, AGENTS.md, providers.md, …), the generated aggregate, and the
		// agent-activity log (agents.md) are never read or reorganized.
		if !IsOrganizableFile(f.Name) {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}
		vaultPayload.WriteString(fmt.Sprintf("=== FILE: %s ===\n%s\n=== END ===\n\n", f.Name, content))
	}

	if vaultPayload.Len() == 0 {
		return organizeRun{}, OrganizeResult{Success: true, Message: "Memory vault is empty. Nothing to organize."}, false
	}

	systemPrompt := organizeSystemPrompt()

	userPrompt := fmt.Sprintf("Here is the current memory vault contents to organize:\n\n%s", vaultPayload.String())
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

func (s *Store) runOrganizeModelCustom(ctx context.Context, endpoint string, model string) (organizeRun, OrganizeResult, bool) {
	return s.runOrganizeChat(ctx, normalizeOrganizeChatURL(endpoint), model, os.Getenv("AUXLY_LLM_KEY"))
}

func (s *Store) PlanOrganizeWithProvider(ctx context.Context, provider, customURL, model string) (OrganizeProposal, OrganizeResult) {
	baseURL, apiKey, res, ok := resolveOrganizeProvider(provider, customURL)
	if !ok {
		return OrganizeProposal{}, res
	}
	run, res, proceed := s.runOrganizeChat(ctx, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", model, apiKey)
	if !proceed {
		return OrganizeProposal{}, res
	}
	return s.buildProposalFromJSON(run.jsonContent, run.modelUsed, run.tokensUsed)
}

func (s *Store) PlanOrganizeWithCustom(ctx context.Context, endpoint, model string) (OrganizeProposal, OrganizeResult) {
	run, res, proceed := s.runOrganizeModelCustom(ctx, endpoint, model)
	if !proceed {
		return OrganizeProposal{}, res
	}
	return s.buildProposalFromJSON(run.jsonContent, run.modelUsed, run.tokensUsed)
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
