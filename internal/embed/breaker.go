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
	breakerMu               sync.Mutex
	breakerOpenedAt         time.Time          // zero = closed
	breakerHalfOpenInFlight bool               // true once a half-open retest has been claimed
	breakerCooldown         = 60 * time.Second //nolint:gochecknoglobals // overridable in tests
	breakerNow              = time.Now         // overridable in tests
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

// breakerAllow reports whether a request may proceed and, when it admits a
// half-open retest, claims the single-flight slot. Closed -> always true. Open
// within cooldown -> false. Open past cooldown (half-open) -> true for the FIRST
// caller only; concurrent callers get false until that caller's breakerRecord*
// call closes or re-opens the breaker. Use this in Embed (NOT in New, which must
// not consume the half-open slot).
func breakerAllow() bool {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	if breakerOpenedAt.IsZero() {
		return true
	}
	if breakerNow().Sub(breakerOpenedAt) < breakerCooldown {
		return false // still open within cooldown
	}
	// Half-open: admit exactly one caller to retest.
	if breakerHalfOpenInFlight {
		return false
	}
	breakerHalfOpenInFlight = true
	return true
}

// breakerRecordFailure opens the breaker, stamping the current time, and clears
// any half-open in-flight claim so the next cooldown can admit a fresh retest.
func breakerRecordFailure() {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breakerOpenedAt = breakerNow()
	breakerHalfOpenInFlight = false
}

// breakerRecordSuccess closes the breaker and clears any half-open claim.
func breakerRecordSuccess() {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breakerOpenedAt = time.Time{}
	breakerHalfOpenInFlight = false
}

// resetBreaker is a test helper: it closes the breaker and restores the default
// cooldown and clock so global state never bleeds between tests.
func resetBreaker() {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breakerOpenedAt = time.Time{}
	breakerHalfOpenInFlight = false
	breakerCooldown = 60 * time.Second
	breakerNow = time.Now
}
