package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/usage"
)

// writeUsageCache seeds ~/.auxly/usage-cache.json with a single "claude" snapshot
// stamped at fetchedAt, matching the on-disk shape loadUsageReport reads.
func writeUsageCache(t *testing.T, home string, fetchedAt time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".auxly")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := map[string]usage.Report{
		"claude": {
			Provider:  "claude",
			Windows:   []usage.Window{{Label: "Session", Pct: 10}},
			FetchedAt: fetchedAt,
		},
	}
	b, _ := json.Marshal(cache)
	if err := os.WriteFile(filepath.Join(dir, "usage-cache.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLiveUsageSetting seeds ~/.auxly/settings.json with the LiveUsage opt-in.
func writeLiveUsageSetting(t *testing.T, home string, live bool) {
	t.Helper()
	dir := filepath.Join(home, ".auxly")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"liveUsage": live})
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUsageSnapshotStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No cache file at all → stale (a refresh is warranted to seed it).
	if !usageSnapshotStale("claude") {
		t.Error("missing cache should be stale")
	}
	// Fresh snapshot → not stale.
	writeUsageCache(t, home, time.Now())
	if usageSnapshotStale("claude") {
		t.Error("fresh snapshot should not be stale")
	}
	// Snapshot older than the trigger threshold → stale.
	writeUsageCache(t, home, time.Now().Add(-usageRefreshAfter-time.Second))
	if !usageSnapshotStale("claude") {
		t.Error("snapshot older than usageRefreshAfter should be stale")
	}
}

func TestAcquireRefreshLockDebounces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if !acquireRefreshLock() {
		t.Fatal("first acquire on a clean lock should succeed")
	}
	if acquireRefreshLock() {
		t.Error("second acquire within the lock TTL must be debounced")
	}
	// Backdate the lock past its TTL → acquirable again.
	old := time.Now().Add(-usageRefreshLockTTL - time.Second)
	if err := os.Chtimes(usageRefreshLockPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if !acquireRefreshLock() {
		t.Error("acquire after the lock TTL elapsed should succeed")
	}
}

func TestMaybeRefreshUsageGating(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var spawned int
	orig := spawnRefresh
	spawnRefresh = func() { spawned++ }
	t.Cleanup(func() { spawnRefresh = orig })

	// Live Usage OFF → never spawns, even with a missing/stale snapshot.
	writeLiveUsageSetting(t, home, false)
	MaybeRefreshUsage("claude")
	if spawned != 0 {
		t.Fatalf("LiveUsage off must not spawn; got %d", spawned)
	}

	// Live Usage ON + stale snapshot → spawns once; the immediate retry is debounced.
	writeLiveUsageSetting(t, home, true)
	writeUsageCache(t, home, time.Now().Add(-usageRefreshAfter-time.Second))
	MaybeRefreshUsage("claude")
	MaybeRefreshUsage("claude")
	if spawned != 1 {
		t.Fatalf("expected exactly one spawn (second debounced by the lock); got %d", spawned)
	}

	// Fresh snapshot → no spawn. Clear the lock first so a non-spawn proves the
	// freshness gate, not the debounce.
	os.Remove(usageRefreshLockPath())
	writeUsageCache(t, home, time.Now())
	MaybeRefreshUsage("claude")
	if spawned != 1 {
		t.Fatalf("fresh snapshot must not spawn; got %d", spawned)
	}
}
