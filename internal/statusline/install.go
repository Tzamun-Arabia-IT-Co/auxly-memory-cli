package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode classifies the current Claude Code statusLine.command slot.
const (
	ModeNone  = "none"  // no statusline configured
	ModeFull  = "full"  // Auxly's full statusline (`auxly statusline`)
	ModeWrap  = "wrap"  // user's own + Auxly appended (`auxly statusline --wrap`)
	ModeOther = "other" // some non-Auxly statusline the user set up
)

// State is a snapshot of the user's current statusline configuration.
type State struct {
	Mode    string // ModeNone | ModeFull | ModeWrap | ModeOther
	Command string // the raw command in settings.json (may be empty)
	Backup  string // the backed-up original command, if any
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func backupPath() string {
	return filepath.Join(auxlyDir(), "cc-statusline-original.txt")
}

// selfCommand returns the install command for this binary, e.g.
// `/usr/local/bin/auxly statusline` (or `… --wrap`).
func selfCommand(wrap bool) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "auxly"
	}
	cmd := quoteIfNeeded(exe) + " statusline"
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
// invokes the `statusline` SUBCOMMAND (a bare arg), optionally with `--wrap`. This is
// binary-name agnostic (works for auxly, auxly-dev, a renamed binary, or the test
// binary) and distinguishes us from a user's own `bash …/statusline.sh` (whose arg is
// "statusline.sh", not the bare "statusline" token).
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

func readSettings() (map[string]any, error) {
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read settings.json: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse settings.json: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// writeSettings persists the settings map atomically (temp file + rename) so a crash
// or full disk mid-write can never truncate the user's settings.json.
func writeSettings(m map[string]any) error {
	path := settingsPath()
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

func currentCommand() string {
	m, err := readSettings()
	if err != nil {
		return ""
	}
	return commandFromSettings(m)
}

// CurrentState reports the live statusline configuration so the UI can show what's
// active and which option is selected.
func CurrentState() State {
	cmd := currentCommand()
	st := State{Command: cmd, Mode: classify(cmd)}
	if data, err := os.ReadFile(backupPath()); err == nil {
		st.Backup = strings.TrimSpace(string(data))
	}
	return st
}

// Install points Claude Code's statusLine at Auxly. It is additive and reversible:
// any prior NON-Auxly command is saved to the backup file first, so Uninstall can
// restore it verbatim. wrap=true runs the user's original then appends the Auxly
// segment; wrap=false replaces it with Auxly's full statusline.
func Install(wrap bool) error {
	m, err := readSettings()
	if err != nil {
		return err
	}
	// Derive prev from the SAME map we will write back (no second read), so a
	// concurrent edit between reads can't be silently dropped.
	prev := commandFromSettings(m)
	// Only capture a backup when the current command is the user's OWN (not already
	// an Auxly command) — so switching full↔wrap or re-installing never clobbers the
	// real original.
	if classify(prev) == ModeOther {
		if err := os.MkdirAll(auxlyDir(), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(backupPath(), []byte(prev+"\n"), 0o644); err != nil {
			return fmt.Errorf("back up original statusline: %w", err)
		}
	}
	m["statusLine"] = map[string]any{"type": "command", "command": selfCommand(wrap)}
	return writeSettings(m)
}

// Uninstall removes the Auxly statusline, restoring the user's backed-up original
// verbatim when present, or clearing the slot otherwise. A no-op when Auxly isn't
// the active statusline.
func Uninstall() error {
	m, err := readSettings()
	if err != nil {
		return err
	}
	if classify(commandFromSettings(m)) == ModeOther {
		return nil // not ours — leave it untouched
	}
	if data, err := os.ReadFile(backupPath()); err == nil {
		if orig := strings.TrimSpace(string(data)); orig != "" {
			m["statusLine"] = map[string]any{"type": "command", "command": orig}
			if err := writeSettings(m); err != nil {
				return err
			}
			_ = os.Remove(backupPath())
			return nil
		}
	}
	delete(m, "statusLine")
	return writeSettings(m)
}

// OriginalCommand returns the backed-up user statusline command (for wrap mode to
// run), or "" when none was saved.
func OriginalCommand() string {
	data, err := os.ReadFile(backupPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
