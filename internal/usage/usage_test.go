package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMain redirects the persist path to a throwaway temp file so the test
// suite never reads or writes the real ~/.auxly/usage-cache.json.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "usage-test-*")
	if err != nil {
		panic(err)
	}
	persistPath = func() string { return filepath.Join(dir, "usage-cache.json") }
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// gatedFetcher counts how many times it is invoked and blocks inside fetch until
// its gate is closed, so a test can hold a fetch "in flight" while it fires more
// concurrent Reports calls.
type gatedFetcher struct {
	id    string
	calls *int32
	gate  chan struct{}
}

func (f gatedFetcher) provider() string { return f.id }

func (f gatedFetcher) fetch(_ context.Context) Report {
	atomic.AddInt32(f.calls, 1)
	<-f.gate
	return Report{Provider: f.id, Windows: []Window{{Label: "Session", Pct: 10, IsLimit: true}}}
}

// TestReportsDeduplicatesConcurrentFetches verifies the in-flight guard: when
// many Reports calls race before the first fetch completes (the TUI fires one
// per tick), only ONE network fetch per provider runs — not one per call. This
// is the fix for the self-inflicted rate limiting on Anthropic's usage endpoint.
func TestReportsDeduplicatesConcurrentFetches(t *testing.T) {
	var calls int32
	gate := make(chan struct{})
	m := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		clock:    time.Now,
		fetchers: []fetcher{gatedFetcher{id: "x", calls: &calls, gate: gate}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Reports(context.Background())
		}()
	}

	// Let the racing callers reach the in-flight check while the one fetch blocks.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 fetch for 8 concurrent Reports, got %d", got)
	}
}

// TestReportsServesFromCacheWithinTTL verifies a second call inside the TTL does
// not refetch.
func TestReportsServesFromCacheWithinTTL(t *testing.T) {
	var calls int32
	open := make(chan struct{})
	close(open) // never blocks
	m := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		clock:    time.Now,
		fetchers: []fetcher{gatedFetcher{id: "x", calls: &calls, gate: open}},
	}

	m.Reports(context.Background())
	m.Reports(context.Background())

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 fetch across two in-TTL calls, got %d", got)
	}
}

// flakyFetcher returns good data on the first fetch, then an error on every
// subsequent fetch — modelling a provider that succeeds once then gets rate
// limited.
type flakyFetcher struct {
	id    string
	calls *int32
}

func (f flakyFetcher) provider() string { return f.id }

func (f flakyFetcher) fetch(_ context.Context) Report {
	n := atomic.AddInt32(f.calls, 1)
	if n == 1 {
		return Report{Provider: f.id, Windows: []Window{{Label: "Session", Pct: 42, IsLimit: true}}}
	}
	return Report{Provider: f.id, Err: "rate limited — try later"}
}

// TestForceRefreshKeepsLastGoodOnError reproduces the [r] regression: after a
// good read, Invalidate() (force refresh) followed by a failing refetch must
// keep showing the last-good numbers, never blanking the bar.
func TestForceRefreshKeepsLastGoodOnError(t *testing.T) {
	var calls int32
	m := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		clock:    time.Now,
		fetchers: []fetcher{flakyFetcher{id: "claude", calls: &calls}},
	}

	first := m.Reports(context.Background())
	if len(first) != 1 || !first[0].Available() || first[0].Windows[0].Pct != 42 {
		t.Fatalf("first read should be good 42%%, got %+v", first)
	}

	m.Invalidate() // simulate [r]
	after := m.Reports(context.Background())
	if len(after) != 1 {
		t.Fatalf("expected 1 report after force refresh, got %d", len(after))
	}
	if !after[0].Available() || after[0].Windows[0].Pct != 42 {
		t.Fatalf("force refresh + 429 must preserve last-good 42%%, got %+v (err=%q)", after[0], after[0].Err)
	}
	if calls != 2 {
		t.Fatalf("expected a refetch attempt on force refresh, calls=%d", calls)
	}
}

// rateLimitedFetcher always returns a 429 — models Anthropic's usage endpoint
// throttling the probe while an active session shares the same OAuth token.
type rateLimitedFetcher struct {
	id    string
	calls *int32
}

func (f rateLimitedFetcher) provider() string { return f.id }

func (f rateLimitedFetcher) fetch(_ context.Context) Report {
	atomic.AddInt32(f.calls, 1)
	return Report{Provider: f.id, Err: "rate limited — try later", RateLimited: true}
}

// TestCooldownPersistsAcrossManagers locks in the statusline-staleness fix: the
// post-429 circuit breaker must survive across separate processes. The statusline
// refreshes via a fresh `auxly statusline --refresh-usage` child each render, so a
// cooldown that lived only in memory reset every time and re-hammered the rate-
// limited endpoint — freezing the line at "⧗ as of HH:MM". A 429 in one Manager
// must persist a cooldown that a brand-new Manager honors by skipping the fetch.
func TestCooldownPersistsAcrossManagers(t *testing.T) {
	dir := t.TempDir()
	oldP, oldC := persistPath, cooldownPath
	persistPath = func() string { return filepath.Join(dir, "usage-cache.json") }
	cooldownPath = func() string { return filepath.Join(dir, "usage-cooldown.json") }
	defer func() { persistPath, cooldownPath = oldP, oldC }()

	var calls1 int32
	m1 := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		cooldown: map[string]time.Time{},
		clock:    time.Now,
		fetchers: []fetcher{rateLimitedFetcher{id: "claude", calls: &calls1}},
	}
	m1.Reports(context.Background()) // 429 → opens + persists the cooldown window
	if calls1 != 1 {
		t.Fatalf("first manager should attempt one fetch, got %d", calls1)
	}

	// A brand-new manager (the next detached refresh child) must load the persisted
	// cooldown and SKIP the provider instead of re-probing a throttled endpoint.
	var calls2 int32
	m2 := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		cooldown: map[string]time.Time{},
		clock:    time.Now,
		fetchers: []fetcher{rateLimitedFetcher{id: "claude", calls: &calls2}},
	}
	m2.loadCooldown()
	m2.Reports(context.Background())
	if calls2 != 0 {
		t.Fatalf("second manager must honor the persisted cooldown and NOT re-fetch, got %d", calls2)
	}
}

// TestCooldownExpiryAllowsRefetch verifies an EXPIRED window never suppresses a
// legitimate refresh: once the cooldown passes, a new Manager refetches.
func TestCooldownExpiryAllowsRefetch(t *testing.T) {
	dir := t.TempDir()
	oldC := cooldownPath
	cooldownPath = func() string { return filepath.Join(dir, "usage-cooldown.json") }
	defer func() { cooldownPath = oldC }()

	// Write an already-expired cooldown directly to disk.
	expired := map[string]time.Time{"claude": time.Now().Add(-time.Minute)}
	b, _ := json.MarshalIndent(expired, "", "  ")
	_ = os.WriteFile(cooldownPath(), b, 0o600)

	var calls int32
	open := make(chan struct{})
	close(open)
	m := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		cooldown: map[string]time.Time{},
		clock:    time.Now,
		fetchers: []fetcher{gatedFetcher{id: "claude", calls: &calls, gate: open}},
	}
	m.loadCooldown()
	m.Reports(context.Background())
	if calls != 1 {
		t.Fatalf("expired cooldown must not block a refetch, got %d calls", calls)
	}
}

// TestAliasMirrorsSource verifies an aliased provider reports the source's data
// under its own name without a second fetch.
func TestAliasMirrorsSource(t *testing.T) {
	var calls int32
	open := make(chan struct{})
	close(open)
	m := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		clock:    time.Now,
		fetchers: []fetcher{gatedFetcher{id: "claude", calls: &calls, gate: open}},
		aliases:  map[string]string{"claude-code": "claude"},
	}

	reports := m.Reports(context.Background())
	var sawClaude, sawCode bool
	for _, r := range reports {
		switch r.Provider {
		case "claude":
			sawClaude = true
		case "claude-code":
			sawCode = true
			if len(r.Windows) == 0 {
				t.Fatal("aliased claude-code has no windows")
			}
		}
	}
	if !sawClaude || !sawCode {
		t.Fatalf("expected both claude and claude-code reports, got claude=%v code=%v", sawClaude, sawCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("alias should not trigger a second fetch, got %d calls", got)
	}
}
