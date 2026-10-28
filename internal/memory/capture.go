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
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/llm"
)

// CaptureFact is one durable fact extracted from a session transcript, routed
// to a taxonomy category. Facts NEVER write the vault directly — the caller
// queues them as pending changes for human review.
type CaptureFact struct {
	Category string `json:"category"`
	Fact     string `json:"fact"`
}

// captureTailChars bounds the transcript window sent to the model (~15K tokens).
const captureTailChars = 60_000

// captureMaxFacts is the per-run burst cap: a runaway extraction can never
// flood the pending queue (also closes the audit's pending-DoS gap for this path).
const captureMaxFacts = 10

const captureSystemPrompt = `You extract durable memory facts from an AI-coding-session transcript.

RESPONSE CONTRACT — reply with EXACTLY ONE JSON object, nothing else:
{"facts": [{"category": "<slug>", "fact": "<one-line fact>"}]}

RULES:
- A fact is durable knowledge about the USER or their systems: preferences, decisions, infrastructure coordinates, product/project facts, workflow habits. NOT session narration, NOT code content, NOT things true only for this task.
- 0 facts is a perfectly good answer — most sessions teach nothing durable. Never invent.
- Each fact: one line, self-contained, no "the user said".
- Category slugs and their meaning:
%s
- The user's OWN private life (family, health, their personal legal/financial matters) is category "personal" — never any other.
- At most %d facts, most important first.`

// ExtractCaptureFacts runs the configured LLM over the transcript tail and
// returns routed facts. Errors are for the CALLER to swallow — capture is
// fire-and-forget and must never block or break a session teardown.
func ExtractCaptureFacts(ctx context.Context, transcript string) ([]CaptureFact, error) {
	transcript = strings.TrimSpace(transcript)
	if len(transcript) > captureTailChars {
		transcript = transcript[len(transcript)-captureTailChars:]
	}
	if transcript == "" {
		return nil, nil
	}

	ep := llm.ResolveEndpoint()
	sys := fmt.Sprintf(captureSystemPrompt, RenderForPrompt(), captureMaxFacts)

	// Pick a model the endpoint actually serves and that can chat — the
	// resolved default may be missing (404) and the first-served id may be an
	// embedder. Cloud endpoints keep their model: the probe is unauthenticated.
	model := ep.Model
	if !ep.IsCloud {
		model = llm.PickChatModel(ep.ModelsURL(), ep.Model)
	}

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": sys},
			{"role": "user", "content": "Transcript (tail):\n\n" + transcript},
		},
		"response_format": map[string]string{"type": "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", ep.ChatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("capture LLM unreachable at %s: %w", ep.ChatURL(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("capture LLM status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
		return nil, err
	}
	if len(chat.Choices) == 0 {
		return nil, nil
	}
	return ParseCaptureFacts(chat.Choices[0].Message.Content), nil
}

// ExtractCaptureFactsWithAgent runs a headless CLI agent (e.g. `claude -p`)
// over the transcript tail instead of the Direct LLM. Same prompt, same
// trimming, same parsing — only the transport differs — so the agent inherits
// the organize path's isolation (organize.go): no tools, no MCP servers, a
// scrubbed env, an empty working dir. Errors are for the CALLER to swallow,
// exactly like ExtractCaptureFacts.
func ExtractCaptureFactsWithAgent(ctx context.Context, agentName, agentPath, model, transcript string) ([]CaptureFact, error) {
	transcript = strings.TrimSpace(transcript)
	if len(transcript) > captureTailChars {
		transcript = transcript[len(transcript)-captureTailChars:]
	}
	if transcript == "" {
		return nil, nil
	}

	sys := fmt.Sprintf(captureSystemPrompt, RenderForPrompt(), captureMaxFacts)
	prompt := sys + "\n\nTranscript (tail):\n\n" + transcript

	out, err := runCaptureAgent(ctx, agentName, agentPath, model, prompt)
	if err != nil {
		return nil, err
	}

	// A reply with NO JSON object at all is a broken extraction, not "no
	// facts" — surface it so it lands in the capture breadcrumb. A VALID
	// object with zero facts is a real answer (nothing durable) and stays a
	// success.
	payload := extractJSON(out)
	facts := ParseCaptureFacts(payload)
	if len(facts) == 0 && !strings.Contains(payload, "{") {
		return nil, fmt.Errorf("capture agent %s reply had no JSON object", agentName)
	}
	return facts, nil
}

// CaptureChildEnv is set in a capture agent's environment; `auxly capture`
// exits immediately when it sees it, so the child can never re-trigger capture.
const CaptureChildEnv = "AUXLY_CAPTURE_CHILD"

// runCaptureAgent executes the headless agent once and returns its stdout.
// Mirrors the organize agent branch: empty stdin (no TTY hang), a scrubbed env
// with no AUXLY_* vars (so the child can't locate the real vault), an empty
// temp working dir (so a relative file read finds nothing), and the agent's
// no-tools/no-MCP flags — the prompt embeds user content, so the child stays a
// pure text→JSON transform.
func runCaptureAgent(ctx context.Context, agentName, agentPath, model, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, agentPath, buildAgentArgs(agentName, model, prompt)...)
	cmd.Stdin = strings.NewReader("")
	// CaptureChildEnv marks the child so any capture hook it fires (e.g. a
	// Codex notify hook) exits at once instead of recursing.
	cmd.Env = append(scrubbedOrganizeEnv(), CaptureChildEnv+"=1")
	workDir, err := os.MkdirTemp("", "auxly-capture-")
	if err != nil {
		return "", fmt.Errorf("capture agent %s: %w", agentName, err)
	}
	defer os.RemoveAll(workDir)
	cmd.Dir = workDir
	// Give a killed process a moment to flush before its pipes are closed.
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		tail := stderrTail(stderr.String())
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("capture agent %s: %w (stderr: %s)", agentName, ctxErr, tail)
		}
		return "", fmt.Errorf("capture agent %s failed: %v (stderr: %s)", agentName, err, tail)
	}
	return stdout.String(), nil
}

// stderrTail keeps the last ~300 chars of an agent's stderr — enough to see
// why it failed, small enough to keep in the capture breadcrumb.
func stderrTail(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= 300 {
		return s
	}
	return "…" + string(r[len(r)-300:])
}

// ParseCaptureFacts parses the model's JSON (lenient — fenced/dirty output is
// repaired), validates categories against the taxonomy, and applies the burst
// cap. Unknown categories fall back to the keyword router, never dropped.
func ParseCaptureFacts(raw string) []CaptureFact {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")

	var out struct {
		Facts []CaptureFact `json:"facts"`
	}
	if err := json.Unmarshal([]byte(repairAgentJSON(raw)), &out); err != nil {
		return nil
	}
	kept := make([]CaptureFact, 0, len(out.Facts))
	for _, f := range out.Facts {
		f.Fact = strings.TrimSpace(f.Fact)
		if f.Fact == "" {
			continue
		}
		if _, ok := CategoryBySlug(f.Category); !ok {
			f.Category = RouteCategory(f.Fact)
		}
		kept = append(kept, f)
		if len(kept) >= captureMaxFacts {
			break
		}
	}
	return kept
}

// DedupCaptureFacts drops facts the vault already knows. Vault bullets carry
// "[YYYY-MM-DD]" stamps that the freshly-extracted fact doesn't — both sides
// are compared with the stamp stripped, so a re-learned fact never re-queues
// just because it was learned on a different day.
func (s *Store) DedupCaptureFacts(facts []CaptureFact) []CaptureFact {
	cache := map[string]map[string]bool{}
	kept := make([]CaptureFact, 0, len(facts))
	for _, f := range facts {
		file := FileForCategory(f.Category)
		if f.Category == "projects" {
			file = ProjectFile(s.WorkspaceRoot)
		}
		known, ok := cache[file]
		if !ok {
			known = map[string]bool{}
			content, _ := s.View(file)
			for _, b := range bulletLines(content) {
				known[stripDateStamp(normalizeFact(b))] = true
			}
			cache[file] = known
		}
		if known[stripDateStamp(normalizeFact(f.Fact))] {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

// stripDateStamp removes a leading "[YYYY-MM-DD]" (with optional surrounding
// space) from a fact line so dating never defeats dedup.
func stripDateStamp(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "[") {
		if end := strings.Index(t, "]"); end == 11 {
			return strings.TrimSpace(t[end+1:])
		}
	}
	return t
}
