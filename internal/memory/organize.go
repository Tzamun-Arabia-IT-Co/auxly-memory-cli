package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// safeOrganizeProviders is the allowlist of agent CLIs PROVEN (via the temp-vault
// E2E: zero-loss + zero-contamination) to run the consolidation in a fully isolated,
// tool-less mode. It is EMPTY: agentic CLIs default-wander — even with tools off,
// MCP off, a scrubbed env, an empty cwd, AND settings/hooks disabled, the temp-vault
// E2E still caught real-vault content leaking into a foreign vault and facts being
// dropped. Reliable isolation needs more than flags (OS sandbox / a non-agentic API
// call) and is tracked as a follow-up. Until a provider's E2E passes, Organize routes
// only through the tool-less Direct LLM / Custom endpoints. The per-agent isolation
// scaffolding below (providerKey, buildAgentArgs, scrubbedOrganizeEnv) and the E2E
// gate are kept as the qualification harness for that work.
var safeOrganizeProviders = map[string]bool{}

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

// organizeAgentSafe reports whether the agentic-CLI consolidation path is permitted
// for this agent (i.e. its isolation has been E2E-verified).
func organizeAgentSafe(agentName string) bool {
	return safeOrganizeProviders[providerKey(agentName)]
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
const defaultOrganizeTimeout = 600 * time.Second

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
func buildAgentArgs(agentName, prompt string) []string {
	switch p := strings.ToLower(agentName); {
	case strings.Contains(p, "claude"):
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
			"--model", "haiku",
			prompt,
		}
	case strings.Contains(p, "codex"):
		// Codex automation mode is the `exec` subcommand; read-only sandbox blocks
		// any model-generated shell command from writing or escaping.
		return []string{"exec", "--sandbox", "read-only", prompt}
	case strings.Contains(p, "antigravity") || strings.Contains(p, "agy"):
		// `agy --print` runs a single prompt non-interactively (verified it exits on
		// EOF stdin); a bare prompt opens interactive mode and hangs.
		return []string{"--print", prompt}
	case strings.Contains(p, "gemini"):
		return []string{"-p", prompt}
	case strings.Contains(p, "cursor"):
		return []string{"-p", "--output-format", "text", prompt}
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
		return OrganizeProposal{}, OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse JSON vault payload: %v\nOutput content was: %s", err, jsonContent)}
	}
	prop := OrganizeProposal{ModelUsed: modelUsed, TokensUsed: tokensUsed}
	for _, rf := range parsed.Files {
		cleanedName := filepath.Clean(rf.Name)
		if strings.HasPrefix(cleanedName, "..") || filepath.IsAbs(cleanedName) {
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
	return prop, OrganizeResult{Success: true}
}

// ApplyOrganizeChanges writes the given (approved, possibly user-edited) changes to
// disk and returns a combined diff. This is the ONLY place an organize run writes —
// callers gather explicit approval first. Files whose content is unchanged are skipped.
func (s *Store) ApplyOrganizeChanges(changes []ProposedChange) string {
	var diffBuilder strings.Builder
	for _, c := range changes {
		if !c.Changed() {
			continue
		}
		_ = s.WriteScoped(c.Name, c.NewContent, c.Scope)
		if d := generateDiff(c.Name, c.OldContent, c.NewContent); d != "" {
			diffBuilder.WriteString(d + "\n")
		}
	}
	return diffBuilder.String()
}

// organizeSystemPrompt builds the canonical organize/re-classification system
// prompt. The category taxonomy is injected from RenderForPrompt() (the single
// source of truth) rather than hardcoded, so the file list never drifts. Used
// identically by every organize execution path (direct LLM, CLI agent, custom).
func organizeSystemPrompt() string {
	return fmt.Sprintf(`You are an expert Auxly Memory Architect. Your job is to RE-FILE and tidy the user's memory vault so every fact lives in the right file — WITHOUT EVER LOSING A SINGLE FACT.

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
   were given (plus personal.md if you moved personal facts into it):
{
  "files": [
    {
      "name": "filename.md",
      "content": "Full clean, consolidated, readable content — with every input fact preserved"
    }
  ]
}`, strings.TrimRight(RenderForPrompt(), "\n"))
}

// GetEstimatedTokens estimates token count based on vault file sizes.
func (s *Store) GetEstimatedTokens() int {
	files, err := s.List()
	if err != nil {
		return 800
	}
	var totalChars int64
	for _, f := range files {
		if f.Name == "unified_memory.md" || f.Name == ".audit.log" {
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

// OrganizeVaultWithAgent runs consolidation using either a local/remote LLM API or an installed CLI agent command.
func (s *Store) OrganizeVaultWithAgent(agentName string, agentPath string) OrganizeResult {
	// 1. Gather all files and compile them into a unified payload
	files, err := s.List()
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to list files: %v", err)}
	}

	var vaultPayload strings.Builder
	for _, f := range files {
		if f.Name == "unified_memory.md" || f.Name == ".audit.log" {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}
		vaultPayload.WriteString(fmt.Sprintf("=== FILE: %s ===\n%s\n=== END ===\n\n", f.Name, content))
	}

	if vaultPayload.Len() == 0 {
		return OrganizeResult{Success: true, Message: "Memory vault is empty. Nothing to organize."}
	}

	systemPrompt := organizeSystemPrompt()

	userPrompt := fmt.Sprintf("Here is the current memory vault contents to organize:\n\n%s", vaultPayload.String())
	fullPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, userPrompt)

	var jsonContent string
	var modelUsed string
	var tokensUsed int

	// 2. Route Execution based on agentPath presence
	if agentPath != "" {
		// SAFETY GATE: only agents whose isolation is E2E-verified (no tools, scrubbed
		// env, empty cwd → zero-loss + zero-contamination) may use the agentic path.
		// Unverified agents read the real vault and drop facts, so route them to the
		// tool-less Direct LLM / Custom endpoints instead.
		if !organizeAgentSafe(agentName) {
			return OrganizeResult{
				Success: false,
				Message: fmt.Sprintf("On-Demand Organization via %q isn't available yet: agent CLIs run with file tools and can read outside the vault and drop facts. Use the \"Direct LLM\" option or a Custom endpoint — these transform only the vault text, with no file access.", agentName),
			}
		}

		// Guard: only fork/exec an actual executable file. A config directory
		// (e.g. ~/.gemini/antigravity-cli) would fail with a cryptic
		// "permission denied"; fail clearly instead.
		if fi, statErr := os.Stat(agentPath); statErr != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s is not runnable: %v", agentName, statErr)}
		} else if fi.IsDir() {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s path is a directory, not an executable: %s", agentName, agentPath)}
		}

		// Run via CLI command with the verified per-agent headless invocation.
		timeout := organizeTimeout()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, agentPath, buildAgentArgs(agentName, fullPrompt)...)

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

		// Distinguish a timeout (context deadline) from a genuine execution failure.
		if ctx.Err() == context.DeadlineExceeded {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s execution timed out after %s. Stderr: %s", agentName, timeout, strings.TrimSpace(stderr.String()))}
		}

		if execErr != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s execution failed: %v\nStderr: %s\nStdout: %s", agentName, execErr, stderr.String(), stdout.String())}
		}

		output := stdout.String()
		if strings.TrimSpace(output) == "" {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("CLI agent %s returned empty output.", agentName)}
		}

		jsonContent = extractJSON(output)
		modelUsed = agentName
		tokensUsed = (len(fullPrompt) + len(jsonContent)) / 4
	} else {
		// Run via direct LLM API calls (Ollama / OpenAI / Gemini)
		apiURL := "http://localhost:11434/v1/chat/completions" // Ollama default
		apiKey := ""
		model := "qwen2.5-coder:7b" // Default fast local model

		if os.Getenv("OPENAI_API_KEY") != "" {
			apiURL = "https://api.openai.com/v1/chat/completions"
			apiKey = os.Getenv("OPENAI_API_KEY")
			model = "gpt-4o-mini"
		} else if os.Getenv("GEMINI_API_KEY") != "" {
			apiURL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
			apiKey = os.Getenv("GEMINI_API_KEY")
			model = "gemini-1.5-flash"
		} else if os.Getenv("OLLAMA_HOST") != "" {
			apiURL = os.Getenv("OLLAMA_HOST") + "/v1/chat/completions"
		} else if base := strings.TrimRight(os.Getenv("AUXLY_LLM_BASE"), "/"); base != "" {
			// Any OpenAI-compatible endpoint (vLLM, LM Studio, a gateway, etc.).
			apiURL = base + "/v1/chat/completions"
		} else {
			// Last resort: probe a local OpenAI-compatible server on the
			// conventional localhost port (vLLM / LM Studio default). Stays on
			// the loopback interface — never reaches out to the network.
			client := &http.Client{Timeout: 800 * time.Millisecond}
			if resp, err := client.Get("http://localhost:8000/v1/models"); err == nil {
				resp.Body.Close()
				apiURL = "http://localhost:8000/v1/chat/completions"
			}
		}

		// Dynamic self-healing model selector: query installed models on Ollama/vLLM to prevent 404s!
		modelsURL := strings.Replace(apiURL, "/chat/completions", "/models", 1)
		if client := (&http.Client{Timeout: 800 * time.Millisecond}); client != nil {
			if resp, err := client.Get(modelsURL); err == nil {
				defer resp.Body.Close()
				type modelInfo struct {
					ID string `json:"id"`
				}
				type modelsResp struct {
					Data []modelInfo `json:"data"`
				}
				var mr modelsResp
				if err := json.NewDecoder(resp.Body).Decode(&mr); err == nil && len(mr.Data) > 0 {
					model = mr.Data[0].ID
				}
			}
		}

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
			return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to encode request payload: %v", err)}
		}

		req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to create HTTP request: %v", err)}
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Authorization: Bearer "+apiKey)
		}

		httpClient := &http.Client{Timeout: 300 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM service is unreachable at %s: %v", apiURL, err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM request failed (Status %d): %s", resp.StatusCode, string(bodyBytes))}
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
			return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse chat response: %v", err)}
		}

		if len(chatResp.Choices) == 0 {
			return OrganizeResult{Success: false, Message: "LLM returned an empty response choices list."}
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

	// 3. Decode into a proposal, then apply (this entry point preserves the
	// original compute-and-write behavior; the TUI uses PlanOrganize* + the review
	// flow + ApplyOrganizeChanges to write only what the user approves).
	prop, errRes := s.buildProposalFromJSON(jsonContent, modelUsed, tokensUsed)
	if !errRes.Success {
		return errRes
	}
	diff := s.ApplyOrganizeChanges(prop.Changes)
	return OrganizeResult{Success: true, Message: fmt.Sprintf("✓ Memory vault organized successfully using %s!", modelUsed), Diff: diff, TokensUsed: tokensUsed}
}

// extractJSON isolates valid JSON brackets from any markdown code blocks or surrounding filler text.
func extractJSON(input string) string {
	if startIdx := strings.Index(input, "```json"); startIdx != -1 {
		rest := input[startIdx+7:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			return strings.TrimSpace(rest[:endIdx])
		}
	}
	if startIdx := strings.Index(input, "```"); startIdx != -1 {
		rest := input[startIdx+3:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			return strings.TrimSpace(rest[:endIdx])
		}
	}
	firstBrace := strings.Index(input, "{")
	lastBrace := strings.LastIndex(input, "}")
	if firstBrace != -1 && lastBrace != -1 && lastBrace > firstBrace {
		return input[firstBrace : lastBrace+1]
	}
	return input
}

// OrganizeVaultWithCustom performs memory vault consolidation against a custom HTTP LLM API (like local Ollama, LM Studio, etc.).
func (s *Store) OrganizeVaultWithCustom(endpoint string, model string) OrganizeResult {
	files, err := s.List()
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to list files: %v", err)}
	}

	var vaultPayload strings.Builder
	for _, f := range files {
		if f.Name == "unified_memory.md" || f.Name == ".audit.log" {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}
		vaultPayload.WriteString(fmt.Sprintf("=== FILE: %s ===\n%s\n=== END ===\n\n", f.Name, content))
	}

	if vaultPayload.Len() == 0 {
		return OrganizeResult{Success: true, Message: "Memory vault is empty. Nothing to organize."}
	}

	systemPrompt := organizeSystemPrompt()

	userPrompt := fmt.Sprintf("Here is the current memory vault contents to organize:\n\n%s", vaultPayload.String())
	fullPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, userPrompt)

	apiURL := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(apiURL, "/v1/chat/completions") && !strings.HasSuffix(apiURL, "/chat/completions") {
		apiURL = apiURL + "/v1/chat/completions"
	}

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
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to encode request payload: %v", err)}
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to create HTTP request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 300 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM service is unreachable at %s: %v", apiURL, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return OrganizeResult{Success: false, Message: fmt.Sprintf("LLM request failed (Status %d): %s", resp.StatusCode, string(bodyBytes))}
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
		return OrganizeResult{Success: false, Message: fmt.Sprintf("Failed to parse chat response: %v", err)}
	}

	if len(chatResp.Choices) == 0 {
		return OrganizeResult{Success: false, Message: "LLM returned an empty response choices list."}
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

	prop, errRes := s.buildProposalFromJSON(jsonContent, model, tokensUsed)
	if !errRes.Success {
		return errRes
	}
	diff := s.ApplyOrganizeChanges(prop.Changes)
	return OrganizeResult{Success: true, Message: fmt.Sprintf("✓ Memory vault organized successfully using custom model %s!", model), Diff: diff, TokensUsed: tokensUsed}
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
