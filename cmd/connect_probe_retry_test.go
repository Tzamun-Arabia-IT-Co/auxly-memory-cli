package cmd

import (
	"errors"
	"testing"
)

// TestRetryProbe guards the host-reachability retry policy that makes box wiring
// resilient to the tunnel-startup race: the probe is retried across the window
// where the host's supervisor is still dialing the box's reverse tunnel, and the
// LAST error is surfaced (callers wire anyway, but report what they saw).
func TestRetryProbe(t *testing.T) {
	t.Run("succeeds on first attempt, no extra calls", func(t *testing.T) {
		calls := 0
		err := retryProbe(func() error { calls++; return nil }, 8, 0)
		if err != nil {
			t.Fatalf("want nil, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("want 1 call, got %d", calls)
		}
	})

	t.Run("succeeds after transient failures (tunnel comes up mid-retry)", func(t *testing.T) {
		calls := 0
		err := retryProbe(func() error {
			calls++
			if calls < 3 {
				return errors.New("connect to localhost port 2222: connection refused")
			}
			return nil
		}, 8, 0)
		if err != nil {
			t.Fatalf("want nil after retries, got %v", err)
		}
		if calls != 3 {
			t.Fatalf("want 3 calls (fail, fail, succeed), got %d", calls)
		}
	})

	t.Run("returns last error after exhausting attempts", func(t *testing.T) {
		calls := 0
		want := errors.New("context deadline exceeded")
		err := retryProbe(func() error { calls++; return want }, 4, 0)
		if !errors.Is(err, want) {
			t.Fatalf("want %v, got %v", want, err)
		}
		if calls != 4 {
			t.Fatalf("want 4 attempts, got %d", calls)
		}
	})
}
