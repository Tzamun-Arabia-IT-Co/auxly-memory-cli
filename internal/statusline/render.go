// Package statusline renders the Auxly Claude Code statusline — a productized,
// single Go render path that replaces the three prototype scripts
// (cc-auxly-status.py, cc-usage-line.py, statusline.sh). It surfaces the agent's
// working context (where/session) and the Auxly memory + Claude plan-usage lines.
//
// HARD RULE: rendering never makes a network call. The plan-usage line reads only
// the last-good snapshot the Live Usage subsystem already persists to disk, so it
// is safe to run on every statusline refresh.
package statusline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/usage"
)

// Truecolor palette — kept identical to the prototype so the productized
// statusline is visually indistinguishable from what the user already runs.
const (
	cReset  = "\033[0m"
	cGreen  = "\033[38;2;93;216;121m"  // <50  healthy
	cAmber  = "\033[38;2;230;180;80m"  // 50-79 caution
	cRed    = "\033[38;2;229;72;77m"   // >=80 near-limit
	cDim    = "\033[38;2;110;110;110m" // empty cells / separators
	cTeal   = "\033[38;2;115;203;173m" // ↻ live
	cWarn   = "\033[38;2;220;165;70m"  // ⧗ cached / rate-limited
	cAccent = "\033[38;2;217;119;87m"  // 🔋 Claude brand anchor
)

const barWidth = 10

// Provider identifies which agent's statusline we are rendering. It selects the
// usage snapshot (line 4) and the per-agent quirks on lines 1–2 (Cursor sends
// param_summary/max_mode/autorun and used_percentage; Gemini/Antigravity use
// model.name; Claude uses thinking/effort).
const (
	ProviderClaude      = "claude"
	ProviderCursor      = "cursor"
	ProviderAntigravity = "antigravity"
)

// Input is the agent session JSON delivered on the statusline command's stdin.
// The shape is shared across Claude Code, Cursor CLI, and Antigravity/Gemini CLI;
// fields that only one agent sends are simply absent (zero) for the others.
type Input struct {
	Model struct {
		DisplayName  string `json:"display_name"`
		Name         string `json:"name"` // Gemini / Antigravity
		ID           string `json:"id"`
		ParamSummary string `json:"param_summary"` // Cursor (e.g. "(fast)")
		MaxMode      bool   `json:"max_mode"`      // Cursor
	} `json:"model"`
	Version   string `json:"version"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	Cwd     string `json:"cwd"`
	Autorun bool   `json:"autorun"` // Cursor
	Vim     struct {
		Mode string `json:"mode"`
	} `json:"vim"`
	Worktree struct {
		Name string `json:"name"`
	} `json:"worktree"`
	Effort struct {
		Level string `json:"level"`
	} `json:"effort"`
	TranscriptPath string `json:"transcript_path"`
	Thinking       struct {
		Enabled bool `json:"enabled"`
	} `json:"thinking"`
	ContextWindow struct {
		UsedPercentage      *float64 `json:"used_percentage"` // Cursor (primary)
		RemainingPercentage *float64 `json:"remaining_percentage"`
		TotalInputTokens    *int     `json:"total_input_tokens"`
		TotalOutputTokens   *int     `json:"total_output_tokens"` // Cursor
		ContextWindowSize   *int     `json:"context_window_size"`
		Is1M                bool     `json:"is_1m"`
	} `json:"context_window"`
}

// ReadInput parses the session JSON from raw stdin bytes (empty/invalid => zero Input).
func ReadInput(raw []byte) Input {
	var in Input
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	return in
}

// Render returns the full multi-line statusline for the given provider when full
// is true, or only the Auxly segment lines (memory + plan usage) when full is false
// (the wrapper appends these after the user's own statusline). Lines with no data
// are omitted. An empty provider defaults to Claude for back-compat.
func Render(in Input, full bool, provider string) string {
	if provider == "" {
		provider = ProviderClaude
	}
	// Read the transcript tail ONCE — line 2 (thinking + tokens) and line 3
	// (activity) both derive from it, and it runs on every statusline refresh.
	var tx []string
	if in.TranscriptPath != "" {
		tx = tailLines(in.TranscriptPath, 512*1024)
	}
	var lines []string
	if full {
		lines = append(lines, renderWhere(in, provider), renderSession(in, tx, provider))
	}
	if mem := renderMemory(in, tx); mem != "" {
		lines = append(lines, mem)
	}
	if usageLine := renderUsage(provider); usageLine != "" {
		lines = append(lines, usageLine)
	}
	return strings.Join(lines, "\n")
}

// DetectProvider best-effort infers the agent from the payload shape. It is only the
// FALLBACK for a missing --provider flag (e.g. a hand-edited statusline command); the
// explicit flag is always preferred. Only CURSOR-EXCLUSIVE fields count as Cursor
// signals — param_summary / max_mode / autorun. (used_percentage is NOT exclusive:
// Claude Code sends it too, so keying on it misdetects a Claude session as Cursor.)
// A payload with model.name but no display_name is Gemini/Antigravity; everything
// else — including any normal Claude Code payload — is Claude.
func DetectProvider(in Input) string {
	switch {
	case in.Model.ParamSummary != "" || in.Model.MaxMode || in.Autorun:
		return ProviderCursor
	case in.Model.DisplayName == "" && in.Model.Name != "":
		return ProviderAntigravity
	default:
		return ProviderClaude
	}
}

// defaultModelLabel is the line-1 model name when the payload omits one — per agent.
func defaultModelLabel(provider string) string {
	switch provider {
	case ProviderCursor:
		return "Auto"
	case ProviderAntigravity:
		return "Gemini"
	default:
		return "Claude"
	}
}

func workdir(in Input) string {
	if in.Workspace.CurrentDir != "" {
		return in.Workspace.CurrentDir
	}
	if in.Cwd != "" {
		return in.Cwd
	}
	wd, _ := os.Getwd()
	return wd
}

// renderWhere = line 1: 📁 folder · 🌿 branch · [wt] · 🤖 model · 🔖 version · extras.
func renderWhere(in Input, provider string) string {
	dir := workdir(in)
	folder := filepath.Base(dir)
	if folder == "" || folder == "." {
		folder = dir
	}
	model := in.Model.DisplayName
	if model == "" {
		model = in.Model.Name
	}
	if model == "" {
		model = defaultModelLabel(provider)
	}
	out := "📁 " + folder
	if br := gitBranch(dir); br != "" {
		out += "  🌿 " + br
	}
	if in.Worktree.Name != "" {
		out += "  [wt:" + in.Worktree.Name + "]"
	}
	out += "  🤖 " + model
	if in.Version != "" {
		out += "  🔖 v" + in.Version
	}
	if in.Autorun {
		out += "  ⚡ autorun"
	}
	if in.Vim.Mode != "" {
		out += "  ⌨ " + in.Vim.Mode
	}
	return out
}

// renderSession = line 2: 🧠 mode · ⚡ effort · 🪙 tokens/window · 📊 context bar.
// The 🧠 tag is the thinking level for Claude/Antigravity; for Cursor (no thinking
// keywords) it reflects param_summary / max_mode instead.
func renderSession(in Input, tx []string, provider string) string {
	tokens, ctxSize, usedPct := contextStats(in, tx)
	out := "🧠 " + sessionModeTag(in, tx, provider)
	if in.Effort.Level != "" {
		out += "  ⚡ " + in.Effort.Level
	}
	out += "  🪙 " + fmtTokens(tokens) + "/" + fmtCtx(ctxSize)
	if in.ContextWindow.TotalOutputTokens != nil && *in.ContextWindow.TotalOutputTokens > 0 {
		out += " out:" + fmtTokens(*in.ContextWindow.TotalOutputTokens)
	}
	if usedPct >= 0 {
		out += fmt.Sprintf("  📊 %s %s%d%%%s", thresholdBar(usedPct), pctColor(usedPct), usedPct, cReset)
	} else {
		out += "  📊 " + cDim + strings.Repeat("▱", barWidth) + cReset + " -"
	}
	return out
}

// sessionModeTag returns the line-2 🧠 tag: Cursor reports param_summary/max_mode;
// every other provider uses the transcript-derived thinking level.
func sessionModeTag(in Input, tx []string, provider string) string {
	if provider == ProviderCursor {
		if s := strings.Trim(in.Model.ParamSummary, "()"); s != "" {
			return s
		}
		if in.Model.MaxMode {
			return "max"
		}
		return "off"
	}
	return thinkingMode(in, tx)
}

// renderMemory = line 3: 💾 Auxly · link dot · role · last op · pending. Ported from
// cc-auxly-status.py — role detection, transcript activity, audit fallback.
func renderMemory(in Input, tx []string) string {
	auxDir := auxlyDir()
	if fi, err := os.Stat(auxDir); err != nil || !fi.IsDir() {
		return ""
	}
	role, isRemote := detectRole(auxDir)
	act, errored := scanTranscriptActivity(tx)
	if act == "" {
		act = auditActivity()
	}
	connected := true
	if isRemote && errored != nil {
		connected = !*errored
	}
	dot := "🟢"
	if !connected {
		dot = "🔴"
	}
	out := "💾 Auxly · " + dot + " " + role + act
	if n := pendingCount(); n > 0 {
		out += fmt.Sprintf(" · 📥 %d pending", n)
	}
	return out
}

// renderUsage = line 4: 🔋 Claude · ⏳ 5h bar · 📅 wk bar · freshness. Ported from
// cc-usage-line.py — reads the cached snapshot only, never the network.
func renderUsage(provider string) string {
	rep, ok := loadUsageReport(provider)
	if !ok || rep.Err != "" || len(rep.Windows) == 0 {
		return ""
	}
	now := time.Now()
	labels := map[string]string{
		"session": "5h", "week": "wk", "weekly": "wk", "overall": "all", "opus": "opus",
		"total": "plan", "auto": "auto", "api": "api", // Cursor plan / auto / API buckets
	}
	icons := map[string]string{
		"5h": "⏳ ", "wk": "📅 ", "all": "", "opus": "🅾 ",
		"plan": "", "auto": "⚡ ", "api": "🔌 ",
	}
	sep := " " + cDim + "·" + cReset + " "

	var parts []string
	for _, w := range rep.Windows {
		label := labels[strings.ToLower(strings.TrimSpace(w.Label))]
		if label == "" {
			label = strings.ToLower(w.Label)
			if label == "" {
				label = "?"
			}
		}
		pct := int(w.Pct + 0.5)
		seg := fmt.Sprintf("%s%s%s %s%d%%%s", icons[label], label+" ", thresholdBar(pct), pctColor(pct), pct, cReset)
		if w.HasReset {
			if r := usage.FormatReset(w.ResetAt, now); r != "" {
				seg += " " + cDim + "resets " + r + cReset
			}
		}
		parts = append(parts, seg)
	}
	if len(parts) == 0 {
		return ""
	}

	stamp := cTeal + "↻ live" + cReset
	if !rep.FetchedAt.IsZero() && now.Sub(rep.FetchedAt) > 195*time.Second {
		stamp = cWarn + "⧗ as of " + rep.FetchedAt.Local().Format("15:04") + cReset
	}
	if rep.RateLimited {
		stamp = cWarn + "⧗ rate-limited" + cReset
	}
	name := strings.Title(provider) //nolint:staticcheck // ASCII provider id, Title is fine
	return cAccent + "🔋 " + name + cReset + sep + strings.Join(parts, sep) + sep + stamp
}

// --- bars & colors ----------------------------------------------------------

func thresholdBar(pct int) string {
	if pct < 0 {
		return cDim + strings.Repeat("▱", barWidth) + cReset
	}
	if pct > 100 {
		pct = 100
	}
	filled := (pct*barWidth + 50) / 100
	if filled > barWidth {
		filled = barWidth
	}
	return levelColor(pct) + strings.Repeat("▰", filled) + cDim + strings.Repeat("▱", barWidth-filled) + cReset
}

func levelColor(pct int) string {
	switch {
	case pct >= 80:
		return cRed
	case pct >= 50:
		return cAmber
	default:
		return cGreen
	}
}

func pctColor(pct int) string { return levelColor(pct) }

func fmtTokens(n int) string {
	if n <= 0 {
		return "?"
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func fmtCtx(n int) string {
	if n <= 0 {
		return "?"
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}

// --- context stats ----------------------------------------------------------

// contextStats returns (input tokens, context window size, used percent) with -1
// percent when it can't be derived. Mirrors statusline.sh's derivation order.
func contextStats(in Input, tx []string) (tokens, ctxSize, usedPct int) {
	usedPct = -1
	if in.ContextWindow.TotalInputTokens != nil {
		tokens = *in.ContextWindow.TotalInputTokens
	}
	if in.ContextWindow.ContextWindowSize != nil {
		ctxSize = *in.ContextWindow.ContextWindowSize
	}
	if ctxSize == 0 {
		if in.ContextWindow.Is1M || strings.Contains(strings.ToLower(in.Model.ID), "1m") {
			ctxSize = 1_000_000
		} else {
			ctxSize = 200_000
		}
	}
	if tokens == 0 {
		tokens = lastAssistantTokens(tx)
	}
	switch {
	case in.ContextWindow.UsedPercentage != nil: // Cursor sends this directly
		usedPct = int(*in.ContextWindow.UsedPercentage + 0.5)
	case in.ContextWindow.RemainingPercentage != nil:
		usedPct = int(100 - *in.ContextWindow.RemainingPercentage + 0.5)
	case tokens > 0 && ctxSize > 0:
		usedPct = tokens * 100 / ctxSize
	}
	return tokens, ctxSize, usedPct
}

// --- role / pending / usage cache ------------------------------------------

func auxlyDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auxly")
}

func memoryDir() string {
	if p := os.Getenv("AUXLY_MEMORY_PATH"); p != "" {
		return p
	}
	return filepath.Join(auxlyDir(), "memory")
}

// detectRole returns ("local", false) for a host or unprofiled box; ("remote→name",
// true) when a remotes.yaml names a non-localhost host.
func detectRole(auxDir string) (string, bool) {
	if fi, err := os.Stat(filepath.Join(auxDir, "host.yaml")); err == nil && fi.Size() > 0 {
		return "local", false
	}
	data, err := os.ReadFile(filepath.Join(auxDir, "remotes.yaml"))
	if err != nil {
		return "local", false
	}
	name := yamlScalar(string(data), "name")
	host := yamlScalar(string(data), "host")
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	host = strings.SplitN(host, ":", 2)[0]
	if host == "localhost" || host == "127.0.0.1" {
		host = ""
	}
	label := name
	if label == "" {
		label = host
	}
	if label == "" {
		return "local", false
	}
	return "remote→" + label, true
}

// yamlScalar pulls the first `key: value` scalar from a YAML blob without a full parser.
func yamlScalar(text, key string) string {
	for _, ln := range strings.Split(text, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, key+":") {
			v := strings.TrimSpace(strings.TrimPrefix(t, key+":"))
			v = strings.Trim(v, `"'`)
			if i := strings.IndexAny(v, " #[]{}"); i >= 0 {
				v = v[:i]
			}
			return v
		}
	}
	return ""
}

func pendingCount() int {
	entries, err := os.ReadDir(filepath.Join(memoryDir(), ".pending"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

func loadUsageReport(provider string) (usage.Report, bool) {
	data, err := os.ReadFile(filepath.Join(auxlyDir(), "usage-cache.json"))
	if err != nil {
		return usage.Report{}, false
	}
	var cache map[string]usage.Report
	if json.Unmarshal(data, &cache) != nil {
		return usage.Report{}, false
	}
	r, ok := cache[provider]
	return r, ok
}

// --- transcript & audit -----------------------------------------------------

// gitBranch resolves the current branch with a short, hard deadline: the statusline
// runs on every prompt render, so a slow/stuck git (network mount, repo mid-rebase)
// must never freeze the terminal.
func gitBranch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// tailLines reads the last n bytes of a file and returns its lines.
func tailLines(path string, maxBytes int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	if off := fi.Size() - maxBytes; off > 0 {
		_, _ = f.Seek(off, 0)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// stampOf renders an RFC3339 timestamp as an absolute local marker: today "14:32",
// yesterday "yest 14:32", older "Jun 1".
func stampOf(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		base := strings.TrimSuffix(strings.SplitN(ts, ".", 2)[0], "Z")
		if t, err = time.Parse("2006-01-02T15:04:05", base); err != nil {
			return ""
		}
		t = t.UTC()
	}
	lt := t.Local()
	hm := lt.Format("15:04")
	today := time.Now()
	days := int(today.Truncate(24*time.Hour).Sub(time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.Local)).Hours() / 24)
	switch {
	case days <= 0:
		return hm
	case days == 1:
		return "yest " + hm
	default:
		return lt.Format("Jan 2")
	}
}

type transcriptBlock struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Input     json.RawMessage `json:"input"`
}

type transcriptLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Message   struct {
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens           int `json:"input_tokens"`
			OutputTokens          int `json:"output_tokens"`
			CacheReadInputTokens  int `json:"cache_read_input_tokens"`
			CacheCreationInputTok int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// scanTranscriptActivity returns (activity segment, lastAuxlyCallErrored) by scanning
// the agent's transcript backwards for the most recent Auxly tool_use, matching its
// tool_result for the error state. Ported from cc-auxly-status.py.
func scanTranscriptActivity(lines []string) (string, *bool) {
	if len(lines) == 0 {
		return "", nil
	}
	results := map[string]bool{} // tool_use_id -> is_error (results precede the call going backwards)
	for i := len(lines) - 1; i >= 0; i-- {
		var tl transcriptLine
		if json.Unmarshal([]byte(lines[i]), &tl) != nil {
			continue
		}
		var blocks []transcriptBlock
		if len(tl.Message.Content) == 0 || json.Unmarshal(tl.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				if b.ToolUseID != "" {
					if _, seen := results[b.ToolUseID]; !seen {
						results[b.ToolUseID] = b.IsError
					}
				}
			case "tool_use":
				name := strings.ToLower(b.Name)
				if !strings.Contains(name, "auxly") {
					continue
				}
				stamp := stampOf(tl.Timestamp)
				suffix := ""
				if stamp != "" {
					suffix = " " + stamp
				}
				var errored *bool
				if e, ok := results[b.ID]; ok {
					errored = &e
				}
				return auxlyActivitySegment(name, b.Input, suffix), errored
			}
		}
	}
	return "", nil
}

func auxlyActivitySegment(name string, input json.RawMessage, suffix string) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string {
		if v, ok := in[k].(string); ok {
			return v
		}
		return ""
	}
	switch {
	case strings.Contains(name, "memory_write"):
		f := str("file")
		if f == "" {
			f = "memory"
		}
		return " · ✎ " + f + suffix
	case strings.Contains(name, "memory_read"):
		f := str("file")
		if f == "" {
			f = "memory"
		}
		return " · 📖 " + f + suffix
	case strings.Contains(name, "skill_sync"):
		cat := str("category")
		if cat == "" {
			cat = "memory"
		}
		if !strings.HasSuffix(cat, ".md") {
			cat += ".md"
		}
		return " · ✎ " + cat + suffix
	case strings.Contains(name, "memory_search"):
		q := str("query")
		if len(q) > 18 {
			q = q[:18]
		}
		return " · 🔍 " + q + suffix
	default:
		short := name
		if i := strings.LastIndex(short, "__"); i >= 0 {
			short = short[i+2:]
		}
		short = strings.ReplaceAll(short, "auxly_", "")
		return " · • " + short + suffix
	}
}

func lastAssistantTokens(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		// Claude transcripts mark assistant turns with "type":"assistant"; Cursor's
		// role-based JSONL uses "role":"assistant" — accept either.
		if !strings.Contains(lines[i], `"type":"assistant"`) && !strings.Contains(lines[i], `"role":"assistant"`) {
			continue
		}
		var tl transcriptLine
		if json.Unmarshal([]byte(lines[i]), &tl) != nil {
			continue
		}
		u := tl.Message.Usage
		total := u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTok
		if total > 0 {
			return total
		}
	}
	return 0
}

// thinkingMode reports the active thinking level: "off" when disabled, else "on"
// refined by the strongest think-keyword found in the last few user messages
// (think → think hard → think harder → megathink → ultrathink). Ported from
// statusline.sh.
func thinkingMode(in Input, lines []string) string {
	if !in.Thinking.Enabled {
		return "off"
	}
	if len(lines) == 0 {
		return "on"
	}
	// Collect text from the last 5 user messages.
	var texts []string
	for i := len(lines) - 1; i >= 0 && len(texts) < 5; i-- {
		if !strings.Contains(lines[i], `"type":"user"`) {
			continue
		}
		var tl transcriptLine
		if json.Unmarshal([]byte(lines[i]), &tl) != nil {
			continue
		}
		texts = append(texts, strings.ToLower(messageText(tl.Message.Content)))
	}
	joined := strings.Join(texts, " ")
	switch {
	case strings.Contains(joined, "megathink"):
		return "megathink"
	case strings.Contains(joined, "ultrathink"):
		return "ultrathink"
	case strings.Contains(joined, "think really harder"), strings.Contains(joined, "think harder"):
		return "think harder"
	case strings.Contains(joined, "think hard"):
		return "think hard"
	case containsWord(joined, "think"):
		return "think"
	default:
		return "on"
	}
}

// messageText flattens a transcript message's content (string or array of text
// blocks) into plain text.
func messageText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// containsWord reports whether word appears as a whole word in s.
func containsWord(s, word string) bool {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z')
	}) {
		if f == word {
			return true
		}
	}
	return false
}

// auditActivity is the fallback for the line-3 activity when no transcript op is
// found: the host's most recent LOCAL read/write from the audit log.
func auditActivity() string {
	lines := tailLines(filepath.Join(memoryDir(), ".audit.log"), 128*1024)
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Timestamp string `json:"timestamp"`
			Action    string `json:"action"`
			File      string `json:"file"`
			Source    string `json:"source"`
		}
		if json.Unmarshal([]byte(lines[i]), &e) != nil {
			continue
		}
		if e.File == "" || e.Source == "ssh-remote" {
			continue
		}
		if e.Action != "read" && e.Action != "write" {
			continue
		}
		glyph := "📖"
		if e.Action == "write" {
			glyph = "✎"
		}
		seg := " · " + glyph + " " + e.File
		if st := stampOf(e.Timestamp); st != "" {
			seg += " " + st
		}
		return seg
	}
	return ""
}
