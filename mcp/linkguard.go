package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"gopkg.in/yaml.v3"
)

// remoteHistoryPath is the append-only record of every remote memory host this
// box has EVER been wired to (one name per line). Written by the connect flow;
// never cleaned automatically. Its purpose: distinguish "this box never used
// remote memory" from "this box USED to read a host's memory and the wiring
// silently vanished" — the second case must never serve the stale local vault
// without saying so.
func remoteHistoryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, config.DefaultDir, ".remote-history")
}

// RecordRemoteHistory appends a host name to the box's remote-link history
// (deduplicated). Called by the consumer wiring path whenever a remote profile
// is saved.
func RecordRemoteHistory(name string) {
	path := remoteHistoryPath()
	if path == "" || strings.TrimSpace(name) == "" {
		return
	}
	existing, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(l) == name {
			return
		}
	}
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, name)
}

// ForgetRemoteHistory removes a host from the box's remote-link history — the
// deliberate-disconnect path. Without this, an intentional `auxly connect
// disconnect` would leave a permanent false "MEMORY LINK LOST" banner.
func ForgetRemoteHistory(name string) {
	path := remoteHistoryPath()
	if path == "" || strings.TrimSpace(name) == "" {
		return
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var kept []string
	for _, l := range strings.Split(string(existing), "\n") {
		if t := strings.TrimSpace(l); t != "" && t != name {
			kept = append(kept, t)
		}
	}
	_ = os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0600)
}

// staleLinkWarning returns a non-empty banner when this box's remote-memory
// wiring has vanished: it once had a profile for some host (history) but
// remotes.yaml no longer carries it. Every local tool response then names the
// situation instead of silently serving the possibly-stale local vault — the
// exact failure where a box served a 5-week-old snapshot without anyone
// noticing. Returns "" when history is empty or all historical links are still
// wired (recovery clears the warning with no state to reset).
func staleLinkWarning() string {
	histPath := remoteHistoryPath()
	if histPath == "" {
		return ""
	}
	hist, err := os.ReadFile(histPath)
	if err != nil {
		return "" // no history — a purely local box, nothing to guard
	}

	current := map[string]bool{}
	home, herr := os.UserHomeDir()
	if herr == nil {
		data, rerr := os.ReadFile(filepath.Join(home, config.DefaultDir, "remotes.yaml"))
		if rerr == nil {
			var rc struct {
				Remotes []struct {
					Name string `yaml:"name"`
				} `yaml:"remotes"`
			}
			if yaml.Unmarshal(data, &rc) == nil {
				for _, r := range rc.Remotes {
					current[r.Name] = true
				}
			}
		}
	}

	var missing []string
	for _, l := range strings.Split(string(hist), "\n") {
		name := strings.TrimSpace(l)
		if name != "" && !current[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n⚠️ **MEMORY LINK LOST**: this box previously read remote memory from %s but that wiring is gone — you are reading the box's LOCAL vault, which may be stale. Re-wire from the host (`auxly host reconnect <this-box>`) or here via `auxly connect auto`.",
		strings.Join(missing, ", "))
}
