package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// goodEmbedServer returns an httptest server that always answers with one vector
// and increments hits on every request, so a test can prove whether HTTP was
// actually attempted.
func goodEmbedServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{1, 2, 3}, "index": 0}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestBreakerOpensAndFailsFast proves a failed Embed opens the breaker, and a
// subsequent Embed inside the cooldown returns ErrUnavailable WITHOUT any HTTP
// request (the new server records zero hits).
func TestBreakerOpensAndFailsFast(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)

	// First call: point at a closed server so the request fails and opens the breaker.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	t.Setenv("AUXLY_EMBED_ENDPOINT", deadURL)
	c := New()
	if _, err := c.Embed(context.Background(), []string{"hi"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first Embed error = %v, want ErrUnavailable", err)
	}
	if !breakerOpen() {
		t.Fatal("breaker should be open after a request failure")
	}

	// Fix the clock just after open, well inside the cooldown.
	opened := breakerOpenedAt
	breakerNow = func() time.Time { return opened.Add(time.Second) }

	// Second call against a NEW, healthy, request-counting server: must fail fast
	// with zero HTTP hits.
	var hits int32
	srv := goodEmbedServer(t, &hits)
	t.Setenv("AUXLY_EMBED_ENDPOINT", srv.URL)

	c2 := New()
	if _, err := c2.Embed(context.Background(), []string{"hi"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("second Embed error = %v, want fast-fail ErrUnavailable", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("fast-fail path made %d HTTP request(s), want 0", got)
	}
}

// TestBreakerAutoHalfOpenCloseOnSuccess opens the breaker, advances the clock
// past the cooldown so it auto half-opens, then a successful Embed closes it.
func TestBreakerAutoHalfOpenCloseOnSuccess(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)

	breakerRecordFailure()
	if !breakerOpen() {
		t.Fatal("breaker should be open after recordFailure")
	}

	opened := breakerOpenedAt
	breakerNow = func() time.Time { return opened.Add(breakerCooldown + time.Second) }
	if breakerOpen() {
		t.Fatal("breaker should auto half-open once cooldown elapses")
	}

	var hits int32
	srv := goodEmbedServer(t, &hits)
	t.Setenv("AUXLY_EMBED_ENDPOINT", srv.URL)

	c := New()
	if _, err := c.Embed(context.Background(), []string{"hi"}); err != nil {
		t.Fatalf("Embed error = %v, want nil on healthy server", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected exactly 1 HTTP hit on half-open retest")
	}
	if breakerOpen() {
		t.Fatal("breaker should be closed after a successful Embed")
	}
}

// TestBreakerHalfOpenSingleFlight proves that once the breaker half-opens (past
// cooldown), only the FIRST caller of breakerAllow() is admitted; concurrent
// callers are rejected until a record* call resolves the half-open slot.
func TestBreakerHalfOpenSingleFlight(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)

	breakerRecordFailure()
	opened := breakerOpenedAt
	breakerNow = func() time.Time { return opened.Add(breakerCooldown + time.Second) }

	// Two sequential half-open claims: exactly one succeeds.
	first := breakerAllow()
	second := breakerAllow()
	if !(first != second) {
		t.Fatalf("half-open admitted both/neither caller: first=%v second=%v, want exactly one true", first, second)
	}
	if !first || second {
		t.Fatalf("first caller should claim the half-open slot: first=%v second=%v", first, second)
	}

	// Still single-flight until resolved: another caller is rejected.
	if breakerAllow() {
		t.Fatal("breakerAllow admitted a third caller while half-open slot is in flight")
	}

	// A success closes the breaker and clears the in-flight slot.
	breakerRecordSuccess()
	if !breakerAllow() {
		t.Fatal("breakerAllow should admit once the breaker is closed")
	}
}

// TestBreakerHalfOpenConcurrent runs two goroutines racing on breakerAllow() the
// instant the breaker half-opens; exactly one must be admitted.
func TestBreakerHalfOpenConcurrent(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)

	breakerRecordFailure()
	opened := breakerOpenedAt
	breakerNow = func() time.Time { return opened.Add(breakerCooldown + time.Second) }

	var allowed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if breakerAllow() {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&allowed); got != 1 {
		t.Fatalf("half-open admitted %d callers concurrently, want exactly 1", got)
	}
}

// TestNewFastPathWhenOpen confirms New() returns a disabled client without
// probing when the breaker is open.
func TestNewFastPathWhenOpen(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)

	breakerRecordFailure()

	start := time.Now()
	c := New()
	elapsed := time.Since(start)

	if c.Enabled() {
		t.Fatal("New() should return a disabled client while the breaker is open")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("New() took %v while open, want fast (no endpoint probe)", elapsed)
	}
}

// TestBreakerStaysClosedOnSuccess verifies a healthy Embed keeps the breaker
// closed and a subsequent Embed still reaches the server.
func TestBreakerStaysClosedOnSuccess(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)

	var hits int32
	srv := goodEmbedServer(t, &hits)
	t.Setenv("AUXLY_EMBED_ENDPOINT", srv.URL)

	c := New()
	if _, err := c.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("first Embed error = %v, want nil", err)
	}
	if breakerOpen() {
		t.Fatal("breaker should stay closed after a successful Embed")
	}
	if _, err := c.Embed(context.Background(), []string{"b"}); err != nil {
		t.Fatalf("second Embed error = %v, want nil", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 HTTP hits with a closed breaker, got %d", got)
	}
}
