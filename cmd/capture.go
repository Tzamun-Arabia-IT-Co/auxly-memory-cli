package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/tui"
	"github.com/spf13/cobra"
)

var (
	captureTranscript      string
	captureStopHook        bool
	captureProvider        string
	captureCodexNotifyFlag bool
	captureConfigAgent     string
	captureConfigModel     string
	captureConfigAutoSave  string
)

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Extract durable facts from a session transcript into the pending queue",
	Long: `capture reads a session transcript (Claude Code JSONL via --transcript or
--stop-hook, plain text via stdin), extracts durable facts with the configured
LLM, and drops exact, pending, and reworded duplicates. All extracted facts are
held in the pending queue by default. When auto-save is enabled (see 'auxly capture config --auto-save on'),
normal facts from providers with trust level auto are saved directly, while risky
facts (untrusted providers, personal/identity categories, possible secrets, or uncertain
duplicate checks) are still held. Designed to run fire-and-forget from an agent Stop
hook (see 'auxly hooks install').

Extraction backend: the Direct LLM by default, a persisted setting (see 'auxly capture config'),
or env vars AUXLY_CAPTURE_AGENT / AUXLY_CAPTURE_MODEL.`,
	SilenceUsage: true,
	RunE:         runCapture,
}

var captureConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "View or configure the capture backend (agent and model)",
	Long: `config views or updates the backend and options used by session capture.

Without flags, prints the effective capture backend and auto-save status.
With --agent or --model, updates persisted settings in ~/.auxly/settings.json.
Pass --agent none to clear the configured agent and revert to the Direct LLM.
Use --auto-save on|off to toggle direct auto-saving of captured facts (default off;
when on, facts from providers with trust level auto are saved directly; risky facts
are still held).`,
	SilenceUsage: true,
	RunE:         runCaptureConfig,
}

func init() {
	captureCmd.Flags().StringVar(&captureTranscript, "transcript", "", "path to a Claude Code transcript (.jsonl)")
	captureCmd.Flags().BoolVar(&captureStopHook, "stop-hook", false, "read the Claude Code Stop-hook JSON from stdin (extracts transcript_path itself)")
	captureCmd.Flags().StringVar(&captureProvider, "provider", "claude-code", "provider attribution for the queued facts")
	captureCmd.Flags().BoolVar(&captureCodexNotifyFlag, "codex-notify", false, "read a Codex notify-hook JSON payload (arg or stdin) and capture the session (wired by `hooks install --agent codex`)")
	captureConfigCmd.Flags().StringVar(&captureConfigAgent, "agent", "", "CLI agent to use (claude, codex, gemini, antigravity, cursor, or 'none' to clear)")
	captureConfigCmd.Flags().StringVar(&captureConfigModel, "model", "", "model for the CLI agent")
	captureConfigCmd.Flags().StringVar(&captureConfigAutoSave, "auto-save", "", "auto-save facts from trusted providers directly (on|off)")
	captureCmd.AddCommand(captureConfigCmd)
	rootCmd.AddCommand(captureCmd)
}

// captureMinChars skips trivial sessions (~2K tokens) — nothing durable there.
const captureMinChars = 8_000

// captureCooldown throttles back-to-back hook fires (agents can stop often).
const captureCooldown = 10 * time.Minute

// captureMarkerPath is the throttle marker's path for a given vault — shared
// so every capture entry point (Stop-hook, codex notify, shell wrapper) reads
// and writes the exact same file.
func captureMarkerPath(memPath string) string {
	return filepath.Join(memPath, ".capture-last")
}

// captureErrorPath is the last-capture-error breadcrumb for a given vault.
// Hooks discard stderr, so this file is the only durable trace of a failed
// extraction — surfaced by `auxly hooks status` and `auxly doctor`.
func captureErrorPath(memPath string) string {
	return filepath.Join(memPath, ".capture-last-error")
}

// recordCaptureError writes the breadcrumb (best effort — never fail a
// fire-and-forget capture over a breadcrumb write).
func recordCaptureError(memPath string, err error) {
	// The error can carry an LLM server's response body, and doctor/hooks
	// status print this file to a terminal: drop control characters (ESC
	// included, so escape sequences turn inert) and cap the length.
	msg := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, err.Error())
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500]) + "…"
	}
	_ = os.WriteFile(captureErrorPath(memPath), []byte(time.Now().Format(time.RFC3339)+" "+msg+"\n"), 0600)
}

// clearCaptureError removes the breadcrumb once a capture run succeeds.
func clearCaptureError(memPath string) {
	_ = os.Remove(captureErrorPath(memPath))
}

// inCaptureCooldown reports whether memPath's throttle marker is still fresh.
// Every capture entry point checks this FIRST, before doing any real work —
// walking a session tree, reading a transcript, calling an LLM.
func inCaptureCooldown(memPath string) bool {
	st, err := os.Stat(captureMarkerPath(memPath))
	return err == nil && time.Since(st.ModTime()) < captureCooldown
}

// captureTimeout bounds a single capture LLM/agent call (extraction or judge).
const captureTimeout = 4 * time.Minute

func runCapture(cmd *cobra.Command, args []string) error {
	// Fired from inside a capture agent's own run (e.g. its notify hook):
	// never recurse.
	if os.Getenv(memory.CaptureChildEnv) != "" {
		return nil
	}
	// codex's notify hook has its own full pipeline (cooldown, transcript
	// resolution, extraction) — route to it before any other mode, and
	// before touching memPath/the marker below (runCodexNotify does its own).
	if captureCodexNotifyFlag {
		return captureCodexNotify()
	}

	memPath := getMemoryPath()

	// Throttle FIRST — the marker costs nothing, the LLM call doesn't.
	if inCaptureCooldown(memPath) {
		return nil // silent: hook-driven, cooldown active
	}

	transcript, err := captureInput()
	if err != nil {
		return err
	}
	if len(transcript) < captureMinChars {
		return nil // too small to teach anything durable — silent no-op
	}
	_ = os.WriteFile(captureMarkerPath(memPath), []byte(time.Now().Format(time.RFC3339)), 0600)

	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	facts, err := extractCaptureFacts(ctx, transcript)
	if err != nil {
		// Fire-and-forget contract: an unreachable model must never fail a
		// session teardown. Named on stderr, exit 0. Also recorded to the
		// breadcrumb — hooks discard stderr, so this is the visible trace.
		fmt.Fprintf(os.Stderr, "auxly capture: %v\n", err)
		recordCaptureError(memPath, err)
		return nil
	}
	clearCaptureError(memPath)
	storeCaptureFacts(memPath, facts, captureProvider)
	return nil
}

// captureAutoSaveEnabled controls whether captured facts from auto-trusted
// providers are saved directly to memory vs held in the pending queue.
var captureAutoSaveEnabled = func() bool { return config.LoadSettings().CaptureAutoSave }

// storeCaptureFacts is the single post-extraction pipeline shared by every
// capture entry point: exact + pending + reworded dedup, trust/risk routing,
// pending write (+ immediate Approve for auto-saved facts), and audit logging.
func storeCaptureFacts(memPath string, facts []memory.CaptureFact, provider string) {
	autoSaveEnabled := captureAutoSaveEnabled()
	cleaned := make([]memory.CaptureFact, 0, len(facts))
	for _, f := range facts {
		f.Fact = cleanCaptureFact(f.Fact)
		if f.Fact == "" {
			continue
		}
		if _, ok := memory.CategoryBySlug(f.Category); !ok {
			f.Category = memory.RouteCategory(f.Fact)
		}
		cleaned = append(cleaned, f)
	}
	if len(cleaned) == 0 {
		return
	}

	// 2a. Exact vault dedup (date-stamp-stripped).
	store := memory.NewStore(memPath)
	facts = store.DedupCaptureFacts(cleaned)
	if len(facts) == 0 {
		return
	}

	// 2b. Pending-queue exact dedup.
	mgr := pending.NewManager(memPath)
	pendingLines := pendingAdditionsByTarget(mgr)
	pendingNorm := make(map[string]map[string]bool, len(pendingLines))
	for target, lines := range pendingLines {
		set := make(map[string]bool, len(lines))
		for _, l := range lines {
			set[memory.NormalizeCaptureFact(l)] = true
		}
		pendingNorm[target] = set
	}

	afterPending := make([]memory.CaptureFact, 0, len(facts))
	for _, f := range facts {
		file := captureTargetFile(store, f.Category)
		if pendingNorm[file][memory.NormalizeCaptureFact(f.Fact)] {
			continue
		}
		afterPending = append(afterPending, f)
	}
	facts = afterPending
	if len(facts) == 0 {
		return
	}

	// 2c. Reworded-duplicate neighbor retrieval (vault bullets + pending lines).
	knownByFile := make(map[string][]string)
	knownLinesFor := func(file string) []string {
		if lines, ok := knownByFile[file]; ok {
			return lines
		}
		vaultLines := store.FileBulletLines(file)
		pend := pendingLines[file]
		combined := make([]string, 0, len(vaultLines)+len(pend))
		combined = append(combined, vaultLines...)
		combined = append(combined, pend...)
		knownByFile[file] = combined
		return combined
	}

	var (
		judgeCands     []memory.CaptureFact
		judgeNeighbors [][]string
		judgeOrigIdx   []int
	)
	for i, f := range facts {
		file := captureTargetFile(store, f.Category)
		nb := memory.CaptureNeighbors(f.Fact, knownLinesFor(file), memory.CaptureNeighborK, memory.CaptureNeighborMinScore)
		if len(nb) > 0 {
			judgeCands = append(judgeCands, f)
			judgeNeighbors = append(judgeNeighbors, nb)
			judgeOrigIdx = append(judgeOrigIdx, i)
		}
	}

	// 2d. Single batch judge call for all candidates that had neighbors.
	dropped := make(map[int]bool)
	uncertain := make(map[int]bool)
	if len(judgeCands) > 0 {
		jctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
		knownIdx, jerr := captureJudge(jctx, judgeCands, judgeNeighbors)
		cancel()
		if jerr != nil {
			for _, orig := range judgeOrigIdx {
				uncertain[orig] = true
			}
		} else {
			for pos, orig := range judgeOrigIdx {
				if knownIdx[pos] {
					dropped[orig] = true
				}
			}
		}
	}

	// 3. Trust + sensitivity routing (fail closed if trust.yaml cannot load).
	trustLevel := trust.LevelRequireApproval
	if trustCfg, terr := trust.Load(memPath); terr == nil {
		if lvl := trustCfg.GetTrustLevel(captureTrustProvider(provider)); lvl != "" {
			trustLevel = lvl
		}
	}

	// 4. Apply via pending.Manager (WriteFrom, then Approve for auto-save) + audit.
	logger, lerr := audit.NewLogger(memPath)
	if lerr == nil {
		defer logger.Close()
	}
	date := time.Now().Format("2006-01-02")
	agentAttr := "capture:" + strings.TrimPrefix(provider, "capture:")
	saved, held := 0, 0
	heldForAutoSaveOff := false
	for i, f := range facts {
		if dropped[i] {
			continue
		}
		holdReason := ""
		switch {
		case !autoSaveEnabled:
			holdReason = "held: capture auto-save is off"
		case trustLevel != trust.LevelAuto:
			holdReason = "held: provider trust is " + trustLevel
		case captureNeedsReviewCategory(f.Category):
			holdReason = "held: personal/operational category"
		case memory.LooksLikeSecret(f.Fact):
			holdReason = "held: possible secret"
		case uncertain[i]:
			holdReason = "held: duplicate check unavailable"
		}

		file := captureTargetFile(store, f.Category)
		diff := fmt.Sprintf("+ - [%s] %s\n", date, f.Fact)
		name, werr := mgr.WriteFrom(file, diff, agentAttr)
		if werr != nil {
			continue
		}

		if holdReason == "" {
			if !isSingleAddLineDiff(diff) {
				holdReason = "held: unexpected diff shape"
			} else if aerr := mgr.Approve(name); aerr == nil {
				saved++
				if lerr == nil {
					logger.Log("capture", provider, "write", file, diff, "auto-captured (trusted provider)", trust.LevelAuto)
				}
				continue
			} else {
				holdReason = "held: apply conflict"
			}
		}

		if holdReason == "held: capture auto-save is off" {
			heldForAutoSaveOff = true
		}
		held++
		if lerr == nil {
			logger.Log("capture", provider, "write", file, diff, holdReason, trust.LevelRequireApproval)
		}
	}

	var parts []string
	if saved > 0 {
		parts = append(parts, fmt.Sprintf("%d saved to memory", saved))
	}
	if held > 0 {
		parts = append(parts, fmt.Sprintf("%d held for review (`auxly pending`)", held))
	}
	if len(parts) > 0 {
		summary := fmt.Sprintf("🪄 capture: %s", strings.Join(parts, ", "))
		if heldForAutoSaveOff {
			summary += "  — turn on auto-save: auxly capture config --auto-save on"
		}
		fmt.Println(summary)
	}

	// Hook processes exit right after storeCaptureFacts returns, so run
	// BackupNow synchronously (30 s timeout) instead of MaybeAutoBackup's
	// detached goroutine whenever auto-backup is enabled on a configured repo.
	if saved > 0 {
		if gitCfg, _ := git.LoadConfig(memPath); gitCfg != nil && gitCfg.AutoPush {
			if st := git.BackupStatus(memPath); st.Configured {
				store.EnsureUnified()
				bctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if _, berr := git.BackupNow(bctx, memPath); berr != nil {
					fmt.Fprintf(os.Stderr, "⚠ auxly: auto-backup failed: %s\n", oneLineGitErr(berr))
				}
				cancel()
			}
		}
	}
}

func isSingleAddLineDiff(diff string) bool {
	return strings.HasPrefix(diff, "+ - ") && strings.HasSuffix(diff, "\n") && strings.Count(diff, "\n") == 1
}

func cleanCaptureFact(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\u2028' || r == '\u2029' || r == '\u0085' || unicode.IsSpace(r) {
			b.WriteRune(' ')
		} else if unicode.IsControl(r) {
			continue
		} else {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func captureNeedsReviewCategory(slug string) bool {
	cat, ok := memory.CategoryBySlug(slug)
	if !ok {
		return true
	}
	if cat.Tier == memory.TierPersonal || cat.Operational || slug == "identity" {
		return true
	}
	return false
}

func captureTargetFile(store *memory.Store, category string) string {
	if category == "projects" {
		return memory.ProjectFile(store.WorkspaceRoot)
	}
	return memory.FileForCategory(category)
}

// captureTrustProvider maps a capture provider flag/attribution string onto the
// canonical provider key used in trust.yaml.
func captureTrustProvider(provider string) string {
	p := strings.TrimSpace(provider)
	p = strings.TrimPrefix(p, "capture:")
	if p == "claude-code" {
		return "claude"
	}
	return p
}

// pendingAdditionsByTarget walks .pending/ via mgr.List() and collects the
// "+ - ..." addition lines grouped by target file.
func pendingAdditionsByTarget(mgr *pending.Manager) map[string][]string {
	files, err := mgr.List()
	if err != nil {
		return nil
	}
	out := make(map[string][]string)
	for _, pf := range files {
		raw, rerr := os.ReadFile(pf.Path)
		if rerr != nil {
			continue
		}
		target, bullets := parsePendingAdditions(string(raw))
		if target == "" || len(bullets) == 0 {
			continue
		}
		out[target] = append(out[target], bullets...)
	}
	return out
}

func parsePendingAdditions(raw string) (target string, bullets []string) {
	lines := strings.Split(raw, "\n")
	dashes := 0
	bodyStart := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			dashes++
			if dashes == 2 {
				bodyStart = i + 1
				break
			}
			continue
		}
		if dashes == 1 && strings.HasPrefix(line, "target: ") {
			target = strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(line, "target: ")), "\\", "/")
		}
	}
	if dashes < 2 || target == "" {
		return "", nil
	}
	for _, dl := range lines[bodyStart:] {
		if strings.HasPrefix(dl, "+++") {
			continue
		}
		if !strings.HasPrefix(dl, "+") {
			continue
		}
		added := strings.TrimSpace(strings.TrimPrefix(dl, "+"))
		if strings.HasPrefix(added, "- ") || strings.HasPrefix(added, "* ") {
			bullets = append(bullets, added)
		}
	}
	return target, bullets
}

// captureAgentEnv names the CLI-agent backend for capture (e.g. "claude").
// Empty means the Direct LLM provider.
const captureAgentEnv = "AUXLY_CAPTURE_AGENT"

// captureModelEnv picks the CLI agent's model (e.g. "haiku", "sonnet"); empty
// keeps the agent's own default.
const captureModelEnv = "AUXLY_CAPTURE_MODEL"

// resolveCaptureBackend determines the effective capture agent and model,
// along with where the agent was configured from ("env", "settings", or "default").
// Priority: env var > persisted setting > default (empty = Direct LLM).
func resolveCaptureBackend() (agent, model, source string) {
	s := config.LoadSettings()
	envAgent := strings.TrimSpace(os.Getenv(captureAgentEnv))
	envModel := strings.TrimSpace(os.Getenv(captureModelEnv))

	if envAgent != "" {
		agent = envAgent
		source = "env"
	} else if strings.TrimSpace(s.CaptureAgent) != "" {
		agent = strings.TrimSpace(s.CaptureAgent)
		source = "settings"
	} else {
		agent = ""
		source = "default"
	}

	if envModel != "" {
		model = envModel
	} else {
		model = strings.TrimSpace(s.CaptureModel)
	}
	return agent, model, source
}

// formatCaptureBackendSummary formats the capture backend and its source for display.
func formatCaptureBackendSummary(agent, model, source string) string {
	backend := "Direct LLM"
	if agent != "" {
		if model != "" {
			backend = fmt.Sprintf("%s · %s", agent, model)
		} else {
			backend = agent
		}
	}
	if agent == "" && source == "default" {
		return "Direct LLM (default)"
	}
	return fmt.Sprintf("%s (%s)", backend, source)
}

// extractCaptureFacts routes extraction to the CLI-agent backend when
// configured (via env AUXLY_CAPTURE_AGENT or persisted CaptureAgent setting),
// and to the Direct LLM otherwise. Both capture entry points (the Claude Code
// Stop hook and the Codex notify hook) go through it, so the two behave identically.
func extractCaptureFacts(ctx context.Context, transcript string) ([]memory.CaptureFact, error) {
	agent, model, _ := resolveCaptureBackend()
	if agent == "" {
		return memory.ExtractCaptureFacts(ctx, transcript)
	}
	agentName, agentPath, err := resolveHeadlessAgent(agent)
	if err != nil {
		return nil, err
	}
	return memory.ExtractCaptureFactsWithAgent(ctx, agentName, agentPath, model, transcript)
}

// captureJudge runs the reworded-duplicate judge over candidates with neighbors;
// package-level var so tests can stub it without hitting an LLM or agent CLI.
var captureJudge = judgeCaptureDuplicates

func judgeCaptureDuplicates(ctx context.Context, cands []memory.CaptureFact, neighbors [][]string) (map[int]bool, error) {
	agent, model, _ := resolveCaptureBackend()
	if agent == "" {
		return memory.JudgeCaptureDuplicates(ctx, cands, neighbors)
	}
	agentName, agentPath, err := resolveHeadlessAgent(agent)
	if err != nil {
		return nil, err
	}
	return memory.JudgeCaptureDuplicatesWithAgent(ctx, agentName, agentPath, model, cands, neighbors)
}

func runCaptureConfig(cmd *cobra.Command, args []string) error {
	agentChanged := cmd.Flags().Changed("agent")
	modelChanged := cmd.Flags().Changed("model")
	autoSaveChanged := cmd.Flags().Changed("auto-save")

	if !agentChanged && !modelChanged && !autoSaveChanged {
		printCaptureConfigSummary(cmd)
		return nil
	}

	settings := config.LoadSettings()

	if autoSaveChanged {
		v := strings.ToLower(strings.TrimSpace(captureConfigAutoSave))
		switch v {
		case "on", "true", "yes":
			settings.CaptureAutoSave = true
		case "off", "false", "no":
			settings.CaptureAutoSave = false
		default:
			return fmt.Errorf("invalid value %q for --auto-save: valid values are on, off, true, false, yes, no", captureConfigAutoSave)
		}
	}

	if agentChanged {
		rawAgent := strings.TrimSpace(captureConfigAgent)
		if strings.EqualFold(rawAgent, "none") || rawAgent == "" {
			settings.CaptureAgent = ""
			if !modelChanged {
				settings.CaptureModel = ""
			}
		} else {
			agentName, _, err := resolveHeadlessAgent(rawAgent)
			if err != nil {
				return err
			}
			key := tui.OrgProviderKey(agentName)
			if key == "" {
				key = strings.ToLower(rawAgent)
			}
			settings.CaptureAgent = key
		}
	}

	if modelChanged {
		settings.CaptureModel = strings.TrimSpace(captureConfigModel)
	}

	if err := config.SaveSettings(settings); err != nil {
		return err
	}

	printCaptureConfigSummary(cmd)
	return nil
}

func printCaptureConfigSummary(cmd *cobra.Command) {
	agent, model, source := resolveCaptureBackend()
	cmd.Println(formatCaptureBackendSummary(agent, model, source))
	if config.LoadSettings().CaptureAutoSave {
		cmd.Println("auto-save: on")
	} else {
		cmd.Println("auto-save: off")
	}
}

// captureInput assembles the transcript text from whichever source was given.
func captureInput() (string, error) {
	switch {
	case captureStopHook:
		// Claude Code Stop hook: JSON on stdin carrying transcript_path.
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return "", err
		}
		var hook struct {
			TranscriptPath string `json:"transcript_path"`
		}
		if err := json.Unmarshal(data, &hook); err != nil || hook.TranscriptPath == "" {
			return "", nil // malformed hook payload — silent no-op
		}
		return readTranscriptJSONL(hook.TranscriptPath)
	case captureTranscript != "":
		return readTranscriptJSONL(captureTranscript)
	default:
		// `auxly capture` reads a session transcript from stdin — it's a hook
		// command (the session-end hook pipes the transcript in). Run bare in a
		// terminal, ReadAll would block forever on a TTY waiting for EOF, which
		// looks like a hang. If stdin is a terminal (not a pipe/file), print
		// guidance and no-op instead.
		if stat, _ := os.Stdin.Stat(); stat != nil && stat.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintln(os.Stderr, "auxly capture reads a session transcript from stdin and runs automatically via")
			fmt.Fprintln(os.Stderr, "the session-end hook (`auxly hooks install`). Nothing piped in — nothing to do.")
			fmt.Fprintln(os.Stderr, "Capture a file manually with: auxly capture --transcript <transcript.jsonl>")
			return "", nil
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		return string(data), err
	}
}

// readTranscriptJSONL extracts human-relevant text (user + assistant turns)
// from a Claude Code transcript, skipping tool dumps — those are code and
// command noise, not knowledge about the user. Falls back to the raw file
// content when no line parses as JSON at all — a `script`-captured shell
// session (gemini/kimi wrapper) is plain text, not JSONL, and the whole file
// IS the session text in that case.
func readTranscriptJSONL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil // transcript already cleaned up — silent no-op
	}
	var b strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}
		// Content is either a plain string or a block array; keep text blocks only.
		var asString string
		if json.Unmarshal(entry.Message.Content, &asString) == nil {
			b.WriteString(entry.Message.Role + ": " + asString + "\n")
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) == nil {
			for _, blk := range blocks {
				if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
					b.WriteString(entry.Message.Role + ": " + blk.Text + "\n")
				}
			}
		}
	}
	// ponytail: nothing extracted (whether the file had zero valid JSON
	// lines, or was valid JSON that just never matched a user/assistant
	// turn — e.g. one incidental `{}` in a script(1)-captured plain-text
	// session) means there's no structured transcript to trust — fall back
	// to the raw file content. A lone JSON log line must not veto an
	// otherwise plain-text transcript; captureMinChars still gates junk.
	if b.Len() == 0 {
		return ansiEscapeRE.ReplaceAllString(string(data), ""), nil
	}
	return b.String(), nil
}

// ansiEscapeRE strips terminal color/cursor-control sequences — CSI
// (`\x1b[...<letter>`) and OSC (`\x1b]...<BEL>`) — that a `script`-captured
// raw transcript carries. Only applied to the raw fallback above: noise for
// an LLM doing fact extraction, not signal.
var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07`)
