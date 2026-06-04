package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode classifies the current statusLine.command slot for an agent.
const (
	ModeNone  = "none"  // no statusline configured
	ModeFull  = "full"  // Auxly's full statusline (`auxly statusline …`)
	ModeWrap  = "wrap"  // user's own + Auxly appended (`auxly statusline … --wrap`)
	ModeOther = "other" // some non-Auxly statusline the user set up
)

// State is a snapshot of one agent's current statusline configuration.
type State struct {
	Mode    string // ModeNone | ModeFull | ModeWrap | ModeOther
	Command string // the raw command in the agent's settings (may be empty)
	Backup  string // the backed-up original command, if any
}

// Target describes one agent's statusline wiring: where its settings live, which
// extra keys that agent's statusLine object carries (and we must preserve), and the
// `--provider` flag the installed command needs so render shows the right usage.
//
// All three agents converge on the same Claude-Code-style `statusLine: {type,
// command, …}` shape, just in different files:
//   - Claude Code  → ~/.claude/settings.json
//   - Cursor CLI   → ~/.cursor/cli-config.json                 (+ padding/updateIntervalMs/timeoutMs)
//   - Antigravity  → ~/.gemini/antigravity-cli/settings.json   (+ enabled) — NOT ~/.gemini, which is the Gemini CLI's.
type Target struct {
	Name         string         // "claude" | "cursor" | "antigravity" (matches the render provider id)
	Label        string         // human label for the TUI/CLI
	settingsPath string         // absolute path to the agent's settings file
	backupName   string         // backup filename under ~/.auxly
	providerFlag string         // "" for claude (the render default), else the --provider value
	extraFields  map[string]any // statusLine sibling keys to set when WE write (only if absent)
}

// targets is the source of truth for every statusline-capable agent.
func targets() []Target {
	home, _ := os.UserHomeDir()
	return []Target{
		{
			Name: ProviderClaude, Label: "Claude Code",
			settingsPath: filepath.Join(home, ".claude", "settings.json"),
			backupName:   "cc-statusline-original.txt", // unchanged → existing backups still restore
			providerFlag: ProviderClaude,               // explicit so render never auto-detects
		},
		{
			Name: ProviderCursor, Label: "Cursor CLI",
			settingsPath: filepath.Join(home, ".cursor", "cli-config.json"),
			backupName:   "cursor-statusline-original.txt",
			providerFlag: ProviderCursor,
			extraFields:  map[string]any{"padding": 2, "updateIntervalMs": 300, "timeoutMs": 2000},
		},
		{
			Name: ProviderAntigravity, Label: "Antigravity CLI",
			settingsPath: filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"),
			backupName:   "antigravity-statusline-original.txt",
			providerFlag: ProviderAntigravity,
			extraFields:  map[string]any{"enabled": true},
		},
	}
}

// Targets returns every supported statusline agent.
func Targets() []Target { return targets() }

// TargetByName resolves an agent id ("" defaults to claude). ok is false if unknown.
func TargetByName(name string) (Target, bool) {
	if name == "" {
		name = ProviderClaude
	}
	for _, t := range targets() {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// Available reports whether this agent is installed on the machine — its settings
// directory exists. Used to gate the TUI switcher and `install --agent all`.
func (t Target) Available() bool {
	fi, err := os.Stat(filepath.Dir(t.settingsPath))
	return err == nil && fi.IsDir()
}

func (t Target) backupPath() string { return filepath.Join(auxlyDir(), t.backupName) }

// selfCommand returns the install command for this binary + agent, e.g.
// `/usr/local/bin/auxly statusline --provider cursor` (+ ` --wrap`). The `--provider`
// flag is ALWAYS baked in (every agent, including Claude) so render is deterministic
// and never falls back to payload sniffing — which can misdetect (Claude Code and
// Cursor share fields like used_percentage).
func (t Target) selfCommand(wrap bool) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "auxly"
	}
	provider := t.providerFlag
	if provider == "" {
		provider = ProviderClaude
	}
	cmd := quoteIfNeeded(exe) + " statusline --provider " + provider
	if wrap {
		cmd += " --wrap"
	}
	return cmd
}

func quoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// classify maps a raw command string to a Mode by tokenizing it: our install always
// invokes the `statusline` SUBCOMMAND (a bare arg), optionally with `--wrap` (and a
// `--provider X` pair, which classify simply ignores). This is binary-name agnostic
// (auxly, auxly-dev, a renamed/test binary) and distinguishes us from a user's own
// `bash …/statusline.sh` (whose arg is "statusline.sh", not the bare "statusline").
func classify(cmd string) string {
	if strings.TrimSpace(cmd) == "" {
		return ModeNone
	}
	hasStatusline, hasWrap := false, false
	for _, f := range strings.Fields(cmd) {
		switch f {
		case "statusline":
			hasStatusline = true
		case "--wrap":
			hasWrap = true
		}
	}
	switch {
	case hasStatusline && hasWrap:
		return ModeWrap
	case hasStatusline:
		return ModeFull
	default:
		return ModeOther
	}
}

func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// writeSettings persists the settings map atomically (temp file + rename) so a crash
// or full disk mid-write can never truncate the user's settings file. Sibling keys in
// the map are preserved verbatim — only the caller's mutations are applied.
func writeSettings(path string, m map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// commandFromSettings extracts statusLine.command from an already-loaded settings map.
func commandFromSettings(m map[string]any) string {
	sl, ok := m["statusLine"].(map[string]any)
	if !ok {
		return ""
	}
	cmd, _ := sl["command"].(string)
	return cmd
}

// statusLineSiblings returns a fresh copy of the existing statusLine object's keys so
// a write preserves an agent's own extras (Cursor's padding/updateIntervalMs/timeoutMs,
// Antigravity's enabled) instead of dropping them.
func statusLineSiblings(m map[string]any) map[string]any {
	out := map[string]any{}
	if existing, ok := m["statusLine"].(map[string]any); ok {
		for k, v := range existing {
			out[k] = v
		}
	}
	return out
}

func (t Target) currentCommand() string {
	m, err := readSettings(t.settingsPath)
	if err != nil {
		return ""
	}
	return commandFromSettings(m)
}

// CurrentState reports the live statusline configuration for an agent so the UI can
// show what's active and which option is selected.
func CurrentState(name string) State {
	t, ok := TargetByName(name)
	if !ok {
		return State{Mode: ModeNone}
	}
	cmd := t.currentCommand()
	st := State{Command: cmd, Mode: classify(cmd)}
	if data, err := os.ReadFile(t.backupPath()); err == nil {
		st.Backup = strings.TrimSpace(string(data))
	}
	return st
}

// Install points an agent's statusLine at Auxly. It is additive and reversible: any
// prior NON-Auxly command is saved to that agent's backup file first, so Uninstall
// can restore it verbatim. wrap=true runs the user's original then appends the Auxly
// segment; wrap=false replaces it with Auxly's full statusline. The agent's own
// statusLine sibling keys are preserved, and every unrelated settings key is untouched.
func Install(name string, wrap bool) error {
	t, ok := TargetByName(name)
	if !ok {
		return fmt.Errorf("unknown statusline agent %q", name)
	}
	m, err := readSettings(t.settingsPath)
	if err != nil {
		return err
	}
	// Derive prev from the SAME map we will write back (no second read), so a
	// concurrent edit between reads can't be silently dropped.
	prev := commandFromSettings(m)
	// Only capture a backup when the current command is the user's OWN (not already
	// an Auxly command) — so switching full↔wrap or re-installing never clobbers the
	// real original (including the user's hand-rolled statusline.sh).
	if classify(prev) == ModeOther {
		if err := os.MkdirAll(auxlyDir(), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(t.backupPath(), []byte(prev+"\n"), 0o644); err != nil {
			return fmt.Errorf("back up original statusline: %w", err)
		}
	}
	sl := statusLineSiblings(m)
	sl["type"] = "command"
	sl["command"] = t.selfCommand(wrap)
	for k, v := range t.extraFields {
		if _, present := sl[k]; !present {
			sl[k] = v
		}
	}
	m["statusLine"] = sl
	return writeSettings(t.settingsPath, m)
}

// Uninstall removes the Auxly statusline for an agent, restoring its backed-up
// original verbatim when present, or clearing the slot otherwise. A no-op when Auxly
// isn't the active statusline for that agent.
func Uninstall(name string) error {
	t, ok := TargetByName(name)
	if !ok {
		return fmt.Errorf("unknown statusline agent %q", name)
	}
	m, err := readSettings(t.settingsPath)
	if err != nil {
		return err
	}
	if classify(commandFromSettings(m)) == ModeOther {
		return nil // not ours — leave it untouched
	}
	if data, err := os.ReadFile(t.backupPath()); err == nil {
		if orig := strings.TrimSpace(string(data)); orig != "" {
			sl := statusLineSiblings(m)
			sl["type"] = "command"
			sl["command"] = orig
			m["statusLine"] = sl
			if err := writeSettings(t.settingsPath, m); err != nil {
				return err
			}
			_ = os.Remove(t.backupPath())
			return nil
		}
	}
	delete(m, "statusLine")
	return writeSettings(t.settingsPath, m)
}

// AutoInstallMissing installs the full Auxly statusline for every detected agent that
// has NO statusline configured yet (ModeNone). It is idempotent and non-destructive:
// an agent that already runs its OWN statusline (ModeOther) or Auxly (ModeFull/Wrap)
// is left untouched, so re-running this never stomps a deliberate choice. Returns the
// labels of the agents it newly wired, for reporting. Used to make the statusline a
// follows-you preference — e.g. `auxly connect auto` calls it when onboarding a remote.
func AutoInstallMissing() []string {
	var done []string
	for _, t := range targets() {
		if !t.Available() {
			continue
		}
		if classify(t.currentCommand()) != ModeNone {
			continue // user's own, or already Auxly — don't touch it
		}
		if err := Install(t.Name, false); err == nil {
			done = append(done, t.Label)
		}
	}
	return done
}

// OriginalCommand returns an agent's backed-up user statusline command (for wrap mode
// to run), or "" when none was saved.
func OriginalCommand(name string) string {
	t, ok := TargetByName(name)
	if !ok {
		return ""
	}
	data, err := os.ReadFile(t.backupPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
