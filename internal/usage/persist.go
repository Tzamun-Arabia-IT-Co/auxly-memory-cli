package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Usage snapshots are persisted to disk so the dashboard shows the last-known
// numbers immediately on launch and survives a transient rate-limit (429) on the
// provider endpoints — which are tight, especially Anthropic's. Without this a
// fresh launch with an empty in-memory cache would render the raw error; with it
// we show last-good and refresh opportunistically, the way a usage Stream Deck
// plugin behaves. Only successful (Available) snapshots are stored.

// persistPath is a function var so tests can redirect it to a temp file instead
// of polluting the real ~/.auxly/usage-cache.json.
var persistPath = func() string {
	return filepath.Join(homeDir(), ".auxly", "usage-cache.json")
}

// loadCache seeds the in-memory cache from disk. Entries are timestamped with
// their original FetchedAt (old), so the first Reports call still tries a live
// refresh; if that fails, last-good preservation keeps these values on screen.
func (m *Manager) loadCache() {
	var stored map[string]Report
	b, err := os.ReadFile(persistPath())
	if err != nil || json.Unmarshal(b, &stored) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.fetchers {
		if r, ok := stored[f.provider()]; ok && r.Available() {
			m.cache[f.provider()] = cached{report: r, at: r.FetchedAt}
		}
	}
}

// saveCache MERGES the current Available snapshots into the on-disk cache. It
// must never drop a provider that was previously persisted just because it has
// no fresh value this round (e.g. it's rate limited) — otherwise a single bad
// session would erase that provider's last-good, and a fresh launch could no
// longer seed it. Newer good data replaces older; everything else is retained.
func (m *Manager) saveCache() {
	m.mu.Lock()
	current := make(map[string]Report, len(m.cache))
	for p, c := range m.cache {
		if c.report.Available() {
			current[p] = c.report
		}
	}
	m.mu.Unlock()

	if len(current) == 0 {
		return
	}

	// Start from what's already on disk so untouched providers survive.
	stored := map[string]Report{}
	if b, err := os.ReadFile(persistPath()); err == nil {
		_ = json.Unmarshal(b, &stored)
	}
	for p, r := range current {
		stored[p] = r
	}

	b, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(persistPath()), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(persistPath(), b, 0o600)
}
