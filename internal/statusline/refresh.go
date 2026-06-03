package statusline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/usage"
)

// The statusline render path NEVER makes a network call (see render.go's HARD
// RULE). On its own that makes the plan-usage line a frozen snapshot: nothing
// refreshes ~/.auxly/usage-cache.json during a normal coding session unless the
// dashboard happens to be open, so the line shows "⧗ as of HH:MM" forever.
//
// To make it actually LIVE without breaking that rule, the render TRIGGERS a
// detached, guarded background refresh that updates the cache for the NEXT render.
// Render still reads only the on-disk snapshot and returns instantly; the network
// call happens out-of-band in a child process. This is the Stream-Deck pattern:
// show last-good immediately, refresh behind the scenes.

const (
	// usageRefreshAfter triggers a background refresh once the cached snapshot is
	// this old. It mirrors usage.cacheTTL (3m): the usage Manager refuses to refetch
	// a provider younger than its TTL, so triggering any sooner would spawn a child
	// that does nothing. The render's "↻ live" window (195s) is wider than the gap
	// between a render landing the refetch and the snapshot aging past it, so the
	// line reads live across an active session and only blips to "⧗ as of …" on a
	// sparse render before self-healing on the next one.
	usageRefreshAfter = 3 * time.Minute
	// usageRefreshLockTTL debounces renders while a refresh child is in flight, so
	// rapid prompts don't fork a pile of children. It must exceed a single provider
	// fetchTimeout (10s) so the lock outlives the call it represents.
	usageRefreshLockTTL = 20 * time.Second
)

func usageRefreshLockPath() string { return filepath.Join(auxlyDir(), ".usage-refresh.lock") }

// spawnRefresh is a package seam so tests can observe the trigger decision without
// actually exec'ing a child (which would hit the network).
var spawnRefresh = spawnDetachedRefresh

// MaybeRefreshUsage kicks a background usage-cache refresh when Live Usage is
// enabled and the cached "claude" snapshot (the only provider the statusline
// shows) is going stale, debounced by a short lock so concurrent renders don't
// pile up children. It returns immediately and never blocks the statusline; the
// render itself stays network-free.
func MaybeRefreshUsage() {
	if !config.LoadSettings().LiveUsage {
		return // user hasn't opted into networked usage — stay local-first.
	}
	if !usageSnapshotStale("claude") {
		return // snapshot is fresh enough; the render already shows it live.
	}
	if !acquireRefreshLock() {
		return // a refresh is already in flight / ran moments ago.
	}
	spawnRefresh()
}

// usageSnapshotStale reports whether the cached snapshot for provider is missing,
// timestamp-less, or older than usageRefreshAfter.
func usageSnapshotStale(provider string) bool {
	rep, ok := loadUsageReport(provider)
	if !ok || rep.FetchedAt.IsZero() {
		return true
	}
	return time.Since(rep.FetchedAt) >= usageRefreshAfter
}

// acquireRefreshLock returns true (and stamps the lock's mtime to now) when no
// refresh has started within usageRefreshLockTTL — the debounce bounding how often
// we spawn a child. The lock is advisory: a benign double-spawn is harmless because
// the usage Manager's own TTL + post-429 cooldown prevent any real network
// hammering. Returns false on any filesystem error so a wedged lock never spawns.
func acquireRefreshLock() bool {
	p := usageRefreshLockPath()
	if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) < usageRefreshLockTTL {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return false
	}
	return os.WriteFile(p, []byte("refresh\n"), 0o600) == nil
}

// spawnDetachedRefresh starts `auxly statusline --refresh-usage` as a detached
// child that outlives this process, with all stdio discarded so it can never
// corrupt the rendered statusline. Failures are intentionally ignored — a missed
// refresh just means the line shows "⧗ as of …" until the next attempt.
func spawnDetachedRefresh() {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return
	}
	c := exec.Command(exe, "statusline", "--refresh-usage")
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	c.SysProcAttr = detachSysProcAttr()
	if c.Start() == nil && c.Process != nil {
		_ = c.Process.Release() // don't reap; let it run independently of us.
	}
}

// RefreshUsageCache performs the actual networked refresh + cache persist. It is
// the body of the hidden `statusline --refresh-usage` child and the ONLY place in
// the statusline package that touches the network — deliberately off the render
// path. Reports() refetches any provider past its TTL and persists usage-cache.json.
func RefreshUsageCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_ = usage.New().Reports(ctx)
}
