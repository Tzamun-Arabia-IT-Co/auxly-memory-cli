package embed

import (
	"sync"
	"time"
)

// A process-global circuit breaker for the embedding provider. Once an embed
// request fails, the breaker opens for breakerCooldown so subsequent recalls
// fail fast to substring search instead of paying the full endpoint-probe +
// request-timeout latency on a box with no model.
//
// States:
//   - closed:    breakerOpenedAt is zero; requests flow normally.
//   - open:      breakerOpenedAt is set and now-opened < cooldown; fail fast.
//   - half-open: breakerOpenedAt is set but now-opened >= cooldown; the next
//     request is allowed through to retest. Its success closes the
//     breaker; its failure re-opens (re-stamps) it.
var (
	breakerMu       sync.Mutex
	breakerOpenedAt time.Time          // zero = closed
	breakerCooldown = 60 * time.Second //nolint:gochecknoglobals // overridable in tests
	breakerNow      = time.Now         // overridable in tests
)

// breakerOpen reports whether the breaker is currently open (opened and still
// within the cooldown). It returns false once the cooldown has elapsed, which
// auto half-opens the breaker so the next call is allowed through to retest.
func breakerOpen() bool {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	if breakerOpenedAt.IsZero() {
		return false
	}
	return breakerNow().Sub(breakerOpenedAt) < breakerCooldown
}

// breakerRecordFailure opens the breaker, stamping the current time.
func breakerRecordFailure() {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breakerOpenedAt = breakerNow()
}

// breakerRecordSuccess closes the breaker.
func breakerRecordSuccess() {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breakerOpenedAt = time.Time{}
}

// resetBreaker is a test helper: it closes the breaker and restores the default
// cooldown and clock so global state never bleeds between tests.
func resetBreaker() {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breakerOpenedAt = time.Time{}
	breakerCooldown = 60 * time.Second
	breakerNow = time.Now
}
