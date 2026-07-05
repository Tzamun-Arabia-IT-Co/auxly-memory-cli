package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
)

const (
	codexHookHeader = "# added by auxly hooks install"
	codexNotifyLine = `notify = ["auxly", "capture", "--codex-notify"]`
)

func codexConfigPath() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".codex", "config.toml")
	}
	return filepath.Join(home, ".codex", "config.toml")
}

func codexHomeDir() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func installCodexHook() error {
	changed, err := installCodexHookQuiet()
	if err != nil {
		return err
	}
	if changed {
		fmt.Println("✓ Codex notify hook installed.")
	} else {
		fmt.Println("✓ Codex notify hook already installed — nothing to do.")
	}
	return nil
}

// installCodexHookQuiet does the actual read-merge-write with no printing, so
// callers that install on a caller's behalf (autoWireCleanHooks) can decide
// for themselves whether/what to report. changed is true only when the hook
// was actually written or appended — false when it was already present.
func installCodexHookQuiet() (changed bool, err error) {
	path := codexConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		out := codexHookHeader + "\n" + codexNotifyLine + "\n"
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return false, err
		}
		if err := writeFileAtomic(path, []byte(out), 0644); err != nil {
			return false, err
		}
		return true, nil
	}

	lines := strings.SplitAfter(string(data), "\n")
	scanned := scanCodexLines(lines)
	for _, l := range scanned {
		if !l.topLevel {
			continue // a notify-shaped line inside [table]/a """ string isn't ours to see
		}
		if strings.HasPrefix(normalizeCodexSpacing(l.trimmed), codexNotifyOursPrefix) {
			return false, nil
		}
		if isCodexNotifyAssignment(l.trimmed) {
			return false, fmt.Errorf("Codex already has a notify program (%q) — Codex supports only one; remove it or chain manually", l.trimmed)
		}
	}

	insertAt := len(lines)
	for i, l := range scanned {
		if l.topLevel && strings.HasPrefix(l.trimmed, "[") {
			insertAt = i
			break
		}
	}
	block := codexHookHeader + "\n" + codexNotifyLine + "\n"
	out := append([]string{}, lines[:insertAt]...)
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" && !strings.HasSuffix(out[len(out)-1], "\n") {
		out[len(out)-1] += "\n"
	}
	out = append(out, block)
	out = append(out, lines[insertAt:]...)
	if err := writeFileAtomic(path, []byte(strings.Join(out, "")), 0644); err != nil {
		return false, err
	}
	return true, nil
}

func uninstallCodexHook() error {
	path := codexConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("✓ No Codex notify hook found — nothing to remove.")
			return nil
		}
		return err
	}

	lines := strings.SplitAfter(string(data), "\n")
	scanned := scanCodexLines(lines)
	keep := make([]string, 0, len(lines))
	removed := false
	for i := 0; i < len(lines); i++ {
		if scanned[i].topLevel && strings.HasPrefix(normalizeCodexSpacing(scanned[i].trimmed), codexNotifyOursPrefix) {
			removed = true
			if len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == codexHookHeader {
				keep = keep[:len(keep)-1]
			}
			continue
		}
		if scanned[i].trimmed == codexHookHeader {
			nextIsOurs := i+1 < len(lines) && scanned[i+1].topLevel &&
				strings.HasPrefix(normalizeCodexSpacing(scanned[i+1].trimmed), codexNotifyOursPrefix)
			if !nextIsOurs {
				continue
			}
		}
		keep = append(keep, lines[i])
	}
	if !removed {
		fmt.Println("✓ No Codex notify hook found — nothing to remove.")
		return nil
	}
	if err := writeFileAtomic(path, []byte(strings.Join(keep, "")), 0644); err != nil {
		return err
	}
	fmt.Println("✓ Codex notify hook removed.")
	return nil
}

// codexHookState is codexHookStatus's typed result — the compiler, not a
// string-matched human message, owns whether a caller is looking at "wired"
// vs "foreign" vs "not installed".
type codexHookState int

const (
	codexHookNone codexHookState = iota
	codexHookWired
	codexHookForeign
)

func codexHookStatus() (codexHookState, string) {
	data, err := os.ReadFile(codexConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return codexHookNone, "no Codex config found"
		}
		return codexHookNone, err.Error()
	}
	scanned := scanCodexLines(strings.Split(string(data), "\n"))
	for _, l := range scanned {
		if !l.topLevel {
			continue
		}
		if strings.HasPrefix(normalizeCodexSpacing(l.trimmed), codexNotifyOursPrefix) {
			return codexHookWired, "Codex notify hook is installed"
		}
		if isCodexNotifyAssignment(l.trimmed) {
			return codexHookForeign, fmt.Sprintf("Codex has a different notify program: %s", l.trimmed)
		}
	}
	return codexHookNone, "Codex notify hook is not installed"
}

func captureCodexNotify() error {
	if err := runCodexNotify(); err != nil {
		fmt.Fprintf(os.Stderr, "auxly codex notify: %v\n", err)
	}
	return nil
}

func runCodexNotify() error {
	// Cooldown FIRST — cheaper than resolving a payload, and much cheaper
	// than walking the whole sessions tree below.
	if inCaptureCooldown(getMemoryPath()) {
		return nil
	}
	payload, err := codexNotifyPayload()
	if err != nil {
		return err
	}
	path, err := resolveCodexTranscript(payload)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return runCaptureTranscript(path, "codex")
}

func codexNotifyPayload() (map[string]any, error) {
	var data []byte
	var err error
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[len(os.Args)-1]) != "" {
		data = []byte(os.Args[len(os.Args)-1])
	} else {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return nil, err
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func resolveCodexTranscript(payload map[string]any) (string, error) {
	if path := findJSONLPath(payload); path != "" {
		return path, nil
	}
	return newestCodexRollout()
}

func newestCodexRollout() (string, error) {
	root := filepath.Join(codexHomeDir(), "sessions")
	// filepath.WalkDir lstats its root: if the sessions dir is itself a
	// symlink (common when it's been moved to another disk), WalkDir sees a
	// non-directory entry and never descends into it. Resolve first; a
	// missing/broken link falls through to WalkDir's own not-exist handling.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	var newest string
	var newestTime time.Time
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(filepath.Base(path), "rollout-") || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = path
			newestTime = info.ModTime()
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return newest, nil
}

func runCaptureTranscript(path, provider string) error {
	transcript, err := readTranscriptJSONL(path)
	if err != nil {
		return err
	}
	if len(transcript) < captureMinChars {
		fallback, ferr := readCodexRolloutText(path)
		if ferr != nil {
			return ferr
		}
		if len(fallback) > len(transcript) {
			transcript = fallback
		}
	}
	return runCaptureText(transcript, provider)
}

func runCaptureText(transcript, provider string) error {
	memPath := getMemoryPath()
	if inCaptureCooldown(memPath) {
		return nil
	}
	if len(transcript) < captureMinChars {
		return nil
	}
	_ = os.WriteFile(captureMarkerPath(memPath), []byte(time.Now().Format(time.RFC3339)), 0600)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	facts, err := memory.ExtractCaptureFacts(ctx, transcript)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auxly capture: %v\n", err)
		return nil
	}
	store := memory.NewStore(memPath)
	facts = store.DedupCaptureFacts(facts)
	if len(facts) == 0 {
		return nil
	}

	mgr := pending.NewManager(memPath)
	logger, lerr := audit.NewLogger(memPath)
	if lerr == nil {
		defer logger.Close()
	}
	date := time.Now().Format("2006-01-02")
	queued := 0
	for _, f := range facts {
		file := memory.FileForCategory(f.Category)
		if f.Category == "projects" {
			file = memory.ProjectFile(store.WorkspaceRoot)
		}
		diff := fmt.Sprintf("+ - [%s] %s\n", date, f.Fact)
		name, werr := mgr.WriteFrom(file, diff, "capture:"+provider)
		if werr != nil {
			continue
		}
		queued++
		if lerr == nil {
			logger.Log("capture", provider, "write", file, diff, "auto-captured from session transcript", "require_approval")
		}
		_ = name
	}
	if queued > 0 {
		fmt.Printf("🪄 %d fact(s) captured → pending review (`auxly pending`)\n", queued)
	}
	return nil
}

func readCodexRolloutText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		// Codex rollout JSONL has changed across CLI versions. Keep this
		// recursive and key-based so notify capture survives minor schema drift.
		for _, text := range extractCodexStrings(entry) {
			text = strings.TrimSpace(text)
			if text == "" || seen[text] {
				continue
			}
			seen[text] = true
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// extractCodexStrings recursively pulls "text"/"content" string values out of
// a decoded rollout JSON entry — but, like readTranscriptJSONL's user/
// assistant-only filtering, only from nodes that are conversation MESSAGES
// (item type "message", or role user/assistant). A node whose type marks it
// as a function call, tool call/output, or reasoning trace is skipped
// entirely — not just its "text"/"content" keys — because tool output can be
// anything a shell command printed (`cat .env`, `env`, ...) and must never
// reach the LLM doing fact extraction. Once inside an allowed message node,
// recursion stays lenient (content blocks don't re-declare "type":"message").
func extractCodexStrings(v any) []string {
	var out []string
	var walk func(cur any, key string, allowed bool)
	walk = func(cur any, key string, allowed bool) {
		switch x := cur.(type) {
		case map[string]any:
			if !allowed {
				if isCodexDeniedNode(x) {
					return
				}
				allowed = isCodexMessageNode(x)
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k], k, allowed)
			}
		case []any:
			for _, item := range x {
				walk(item, key, allowed)
			}
		case string:
			if allowed && (key == "text" || key == "content") {
				out = append(out, x)
			}
		}
	}
	walk(v, "", false)
	return out
}

// isCodexDeniedNode reports whether a rollout node's own "type" marks it as
// a function/tool call, its output, or a reasoning trace — never descended
// into for text extraction, regardless of what it contains.
func isCodexDeniedNode(m map[string]any) bool {
	t, _ := m["type"].(string)
	return strings.Contains(t, "function_call") || strings.Contains(t, "tool") ||
		strings.Contains(t, "output") || strings.Contains(t, "reasoning")
}

// isCodexMessageNode reports whether a rollout node is a conversation
// message: item type "message", or a user/assistant role.
func isCodexMessageNode(m map[string]any) bool {
	if t, _ := m["type"].(string); t == "message" {
		return true
	}
	role, _ := m["role"].(string)
	return role == "user" || role == "assistant"
}

func findJSONLPath(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := x[k].(string); ok && strings.HasSuffix(s, ".jsonl") {
				return s
			}
			if found := findJSONLPath(x[k]); found != "" {
				return found
			}
		}
	case []any:
		for _, item := range x {
			if found := findJSONLPath(item); found != "" {
				return found
			}
		}
	}
	return ""
}

func isCodexNotifyAssignment(trimmed string) bool {
	before, _, ok := strings.Cut(trimmed, "=")
	return ok && strings.TrimSpace(before) == "notify"
}

// codexNotifyOursPrefix identifies a "notify = [...]" line as ours — spacing
// stripped, since Codex/TOML tolerates `notify=["auxly"...` with no spaces
// and that variant must be exactly as visible to install/uninstall/status.
const codexNotifyOursPrefix = `notify=["auxly"`

// normalizeCodexSpacing strips spaces and tabs so callers can prefix-compare
// against codexNotifyOursPrefix regardless of how the line was hand-written.
func normalizeCodexSpacing(s string) string {
	return strings.NewReplacer(" ", "", "\t", "").Replace(s)
}

// codexLine annotates one line of a scanned config.toml with whether it sits
// at the TOML root — outside every [table] section AND outside a """
// multi-line string. Only root-level lines are ours to install into, remove,
// or read as install/foreign-notify signal; a notify-shaped line nested in a
// [table], or text that merely LOOKS like a header/notify line inside a
// """string value, must not false-trigger any of those checks.
type codexLine struct {
	trimmed  string
	topLevel bool
}

// scanCodexLines walks TOML lines tracking section headers and triple-quoted
// string state. A line starting with `[` changes the current section (and so
// ends top-level scope) unless it's inside a """ string; an odd count of
// `"""` on a line toggles string state (even counts — a string opened and
// closed on the same line — cancel out, matching TOML's single-line form).
func scanCodexLines(lines []string) []codexLine {
	out := make([]codexLine, len(lines))
	section := ""
	inString := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		out[i] = codexLine{trimmed: trimmed, topLevel: section == "" && !inString}
		if !inString && strings.HasPrefix(trimmed, "[") {
			section = trimmed
		}
		if strings.Count(line, `"""`)%2 == 1 {
			inString = !inString
		}
	}
	return out
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auxly-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
