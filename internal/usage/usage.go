// Package usage reads live quota / rate-limit data for the AI coding agents
// Auxly tracks, by reusing each agent's own locally-stored login token to call
// that provider's usage endpoint — exactly what a usage Stream Deck plugin does.
//
// This is OPT-IN and OFF by default: it makes outbound network calls and reads
// the OAuth token each CLI already wrote to disk, which crosses Auxly's
// local-first, zero-network default. A token is only ever sent to its own
// provider's official endpoint (Claude token -> Anthropic, Codex -> OpenAI,
// Gemini -> Google); tokens are never logged, cached to disk, or forwarded
// anywhere else. Every fetch fails safe: any error becomes Report.Err and the
// UI renders "—" rather than blocking the dashboard.
package usage

import (
	"context"
	"sort"
	"sync"
	"time"
)

// cacheTTL bounds how often a provider is refetched. Usage quotas move slowly
// and the endpoints (Anthropic's especially) are aggressively rate-limited, so
// we refresh at most every few minutes regardless of how often the TUI polls.
// The reference plugin polls only every 15 min; 3 min keeps the dashboard live
// without tripping 429s. The manual [r] force-refresh bypasses this for on-demand
// freshness.
const cacheTTL = 3 * time.Minute

// fetchTimeout bounds a single provider call so a hung endpoint can't stall the
// whole refresh. Google's Code Assist is the slowest, hence the generous cap.
const fetchTimeout = 10 * time.Second

// rateLimitCooldown is how long we stop calling a provider after it returns a
// 429, serving its last-good snapshot instead. This is a circuit breaker: it
// protects the shared OAuth token from being hammered (by relaunches, [r], or
// the same token's other clients) into a deeper throttle.
const rateLimitCooldown = 5 * time.Minute

// Window is one quota dimension for a provider: a percent-used plus an optional
// reset time. Providers expose different dimensions — Claude/Codex give a
// rolling Session and Week window; Google gives a single Overall bucket; Cursor
// gives an AI-authored-code share that is informational, not a limit.
type Window struct {
	Label    string    // "Session", "Week", "Overall", "AI code"
	Pct      float64   // 0–100, percent USED (higher = closer to the cap)
	ResetAt  time.Time // when this window rolls over; zero if unknown
	HasReset bool      // false when the provider gives no reset time
	IsLimit  bool      // false for informational metrics (Cursor AI-code %)
}

// Report is one agent's usage snapshot. Windows is ordered most-to-least
// significant; the card shows Windows[0] (and [1] if present), the popup lists
// all of them. A non-empty Err means the snapshot is unavailable and Reason
// explains why (missing token, offline, re-auth needed) for the popup detail.
type Report struct {
	Provider    string // "claude", "claude-code", "codex", "gemini", "antigravity", "cursor"
	Plan        string // human plan label when the endpoint reveals it; may be ""
	Source      string // host the data came from, for the popup's provenance line
	Windows     []Window
	FetchedAt   time.Time
	Err         string // non-empty => unavailable; UI shows "—"
	RateLimited bool   // true when the failure was a 429 (drives the cooldown)
}

// Available reports whether the snapshot carries at least one usable window.
func (r Report) Available() bool { return r.Err == "" && len(r.Windows) > 0 }

// fetcher retrieves a single provider's usage. Implementations must NOT return
// an error; transport/credential failures belong in Report.Err so one dead
// provider never poisons the others.
type fetcher interface {
	provider() string
	fetch(ctx context.Context) Report
}

// Manager owns the fetcher set and the per-provider snapshot cache. It is safe
// for concurrent use; the TUI calls Reports on each poll tick.
type Manager struct {
	mu       sync.Mutex
	cache    map[string]cached
	inFlight map[string]bool      // providers currently being fetched
	cooldown map[string]time.Time // provider -> time until which to skip fetching (post-429)
	fetchers []fetcher
	aliases  map[string]string // provider -> source provider it mirrors
	clock    func() time.Time  // injectable for tests
}

type cached struct {
	report Report
	at     time.Time
}

// New builds a Manager wired with every supported provider. Claude Code and
// Claude Desktop share one Anthropic account, so only "claude" is fetched and
// "claude-code" mirrors it — fetching both would hit the same endpoint twice and
// trip Anthropic's rate limit.
func New() *Manager {
	m := &Manager{
		cache:    map[string]cached{},
		inFlight: map[string]bool{},
		cooldown: map[string]time.Time{},
		clock:    time.Now,
		fetchers: []fetcher{
			anthropicFetcher{id: "claude"},
			codexFetcher{},
			googleFetcher{id: "gemini"},
			googleFetcher{id: "antigravity"},
			cursorFetcher{},
		},
		aliases: map[string]string{"claude-code": "claude"},
	}
	m.loadCache() // seed from the last session's last-good snapshot
	return m
}

// Reports returns the latest snapshot for every provider, refreshing any whose
// cached entry is older than cacheTTL. Providers are fetched in parallel and
// the call blocks until all complete (or time out via fetchTimeout).
func (m *Manager) Reports(ctx context.Context) []Report {
	m.mu.Lock()
	now := m.clock()
	var stale []fetcher
	for _, f := range m.fetchers {
		p := f.provider()
		if m.inFlight[p] {
			continue // a concurrent Reports call is already fetching this provider
		}
		if until, ok := m.cooldown[p]; ok && now.Before(until) {
			continue // in post-429 cooldown; serve last-good instead of re-hammering
		}
		if c, ok := m.cache[p]; !ok || now.Sub(c.at) >= cacheTTL {
			stale = append(stale, f)
			m.inFlight[p] = true
		}
	}
	m.mu.Unlock()

	if len(stale) > 0 {
		m.refresh(ctx, stale)
		m.saveCache()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Report, 0, len(m.fetchers)+len(m.aliases))
	for _, f := range m.fetchers {
		if c, ok := m.cache[f.provider()]; ok {
			out = append(out, c.report)
		}
	}
	// Aliased providers mirror their source's snapshot under their own name.
	for alias, src := range m.aliases {
		if c, ok := m.cache[src]; ok {
			r := c.report
			r.Provider = alias
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// Invalidate marks every cached snapshot stale so the next Reports call refetches
// immediately, bypassing the TTL. Used by the dashboard's manual "force refresh"
// ([r]). It deliberately KEEPS the existing report data: if the forced refetch
// fails (e.g. a 429), last-good preservation falls back to it, so pressing [r]
// can never blank a bar that was showing valid numbers.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for p, c := range m.cache {
		c.at = time.Time{} // zero time => stale on next check; report retained
		m.cache[p] = c
	}
}

// Report returns the cached snapshot for one provider, fetching it if missing or
// stale. Used by the per-agent popup tab.
func (m *Manager) Report(ctx context.Context, provider string) (Report, bool) {
	for _, r := range m.Reports(ctx) {
		if r.Provider == provider {
			return r, true
		}
	}
	return Report{}, false
}

func (m *Manager) refresh(ctx context.Context, fetchers []fetcher) {
	var wg sync.WaitGroup
	results := make([]Report, len(fetchers))
	for i, f := range fetchers {
		wg.Add(1)
		go func(i int, f fetcher) {
			defer wg.Done()
			fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
			defer cancel()
			r := f.fetch(fctx)
			r.Provider = f.provider()
			r.FetchedAt = m.clock()
			results[i] = r
		}(i, f)
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	for i, f := range fetchers {
		delete(m.inFlight, f.provider())
		newR := results[i]
		if newR.RateLimited {
			// Open the circuit: stop calling this provider for the cooldown window.
			m.cooldown[f.provider()] = m.clock().Add(rateLimitCooldown)
		}
		// Preserve last-good data on a transient failure (e.g. a 429): if we
		// already have a usable snapshot, keep showing it and just reset the
		// timestamp so we back off for another TTL rather than blanking the bar
		// and immediately retrying. Errors only surface when there's no prior
		// good data (never authorized, no endpoint, etc.).
		if newR.Err != "" {
			if prev, ok := m.cache[f.provider()]; ok && prev.report.Available() {
				// Keep the prior snapshot AND its original FetchedAt (so the UI can
				// show how stale it is); only bump the cache timestamp to back off.
				m.cache[f.provider()] = cached{report: prev.report, at: m.clock()}
				continue
			}
		}
		m.cache[f.provider()] = cached{report: newR, at: m.clock()}
	}
}
