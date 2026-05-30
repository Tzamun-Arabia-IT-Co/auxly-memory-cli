// Package session maintains a registry of live MCP server sessions under
// ~/.auxly/sessions. Each running `auxly mcp-server` writes one <pid>.json
// record describing itself (provider + source), so the TUI can show exactly
// which agents are connected — without guessing from process ancestry.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Record describes one live MCP server session. The server knows its own
// provider (from its own AUXLY_PROVIDER env) and source, so these values are
// authoritative — unlike external process inspection.
type Record struct {
	PID        int    `json:"pid"`
	Provider   string `json:"provider"`
	Source     string `json:"source"` // "local" | "ssh-remote"
	RemoteHost string `json:"remote_host,omitempty"`
	RemoteOS   string `json:"remote_os,omitempty"`
	RemoteIP   string `json:"remote_ip,omitempty"`
	StartedAt  string `json:"started_at"`
}

func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".auxly", "sessions"), nil
}

func recordPath(pid int) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, strconv.Itoa(pid)+".json"), nil
}

// Write records a live session. Best-effort: a failure only degrades the
// dashboard, never the server itself.
func Write(r Record) error {
	d, err := dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	if r.StartedAt == "" {
		r.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	p := filepath.Join(d, strconv.Itoa(r.PID)+".json")
	return os.WriteFile(p, data, 0o644)
}

// Remove deletes a session record (on clean shutdown or when found stale).
func Remove(pid int) error {
	p, err := recordPath(pid)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns every recorded session. Callers should prune dead PIDs with
// PidsAlive. Corrupt or unreadable files are skipped.
func List() []Record {
	d, err := dir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return nil
	}
	var out []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			continue
		}
		var r Record
		if json.Unmarshal(data, &r) != nil || r.PID == 0 {
			continue
		}
		out = append(out, r)
	}
	return out
}
