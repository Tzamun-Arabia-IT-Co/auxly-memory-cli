package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
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

// cooldownPath is a function var (like persistPath) so tests can redirect it.
var cooldownPath = func() string {
	return filepath.Join(homeDir(), ".auxly", "usage-cooldown.json")
}

// loadCooldown seeds the in-memory post-429 circuit-breaker windows from disk.
//
// The breaker lives only in Manager.cooldown, but the statusline refreshes via a
// FRESH `auxly statusline --refresh-usage` child each render — a new process with
// an empty cooldown map. Without persistence the breaker resets every render, so a
// rate-limited provider (Anthropic's usage endpoint especially, which 429s while an
// active session shares the same OAuth token) gets re-probed every TTL and its
// snapshot freezes at last-good forever ("⧗ as of HH:MM"). Persisting the window
// lets each child honor a cooldown an earlier process opened. Expired windows are
// ignored so a stale file never suppresses a legitimate refresh.
func (m *Manager) loadCooldown() {
	var stored map[string]time.Time
	b, err := os.ReadFile(cooldownPath())
	if err != nil || json.Unmarshal(b, &stored) != nil {
		return
	}
	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()
	for p, until := range stored {
		if until.After(now) {
			m.cooldown[p] = until
		}
	}
}

// saveCooldown persists the active (future) circuit-breaker windows, merging with
// what's on disk so a window another process just opened isn't dropped. Expired
// entries are pruned so the file can't grow unbounded; an empty result removes it.
func (m *Manager) saveCooldown() {
	now := m.clock()
	m.mu.Lock()
	current := make(map[string]time.Time, len(m.cooldown))
	for p, until := range m.cooldown {
		if until.After(now) {
			current[p] = until
		}
	}
	m.mu.Unlock()

	stored := map[string]time.Time{}
	if b, err := os.ReadFile(cooldownPath()); err == nil {
		_ = json.Unmarshal(b, &stored)
	}
	for p, until := range stored {
		if !until.After(now) {
			delete(stored, p) // prune expired
		}
	}
	for p, until := range current {
		stored[p] = until
	}

	if len(stored) == 0 {
		_ = os.Remove(cooldownPath())
		return
	}
	b, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(cooldownPath()), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(cooldownPath(), b, 0o600)
}
