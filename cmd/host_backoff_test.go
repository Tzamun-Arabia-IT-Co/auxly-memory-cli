package cmd

import (
	"strings"
	"testing"
	"time"
)

// TestNextTunnelBackoffEscalates locks the retry ladder: 5s → 30s → 2m → 10m
// cap. Flat 5s-forever hammering an auth-dead relay is the fail2ban recipe the
// ladder exists to prevent.
func TestNextTunnelBackoffEscalates(t *testing.T) {
	want := []time.Duration{
		5 * time.Second,  // 1st failure
		30 * time.Second, // 2nd
		2 * time.Minute,  // 3rd
		10 * time.Minute, // 4th
		10 * time.Minute, // 5th — capped
		10 * time.Minute, // 100th — still capped
	}
	for i, w := range want[:4] {
		if got := nextTunnelBackoff(i + 1); got != w {
			t.Fatalf("fail %d: want %v got %v", i+1, w, got)
		}
	}
	if got := nextTunnelBackoff(5); got != 10*time.Minute {
		t.Fatalf("5th failure not capped: %v", got)
	}
	if got := nextTunnelBackoff(100); got != 10*time.Minute {
		t.Fatalf("100th failure not capped: %v", got)
	}
	if got := nextTunnelBackoff(0); got != 5*time.Second {
		t.Fatalf("zero/negative clamps to first step, got %v", got)
	}
}

// TestTailBufferKeepsLastError locks the stderr capture: only the tail is kept
// (bounded memory on a chatty tunnel) and LastLine returns the final non-empty
// line — the one that distinguishes "Permission denied" from "port in use".
func TestTailBufferKeepsLastError(t *testing.T) {
	tb := &tailBuffer{}
	tb.Write([]byte(strings.Repeat("noise line\n", 1000))) // >4KB of chatter
	tb.Write([]byte("Warning: remote port forwarding failed for listen port 22001\n\n"))
	if got := tb.LastLine(); !strings.Contains(got, "remote port forwarding failed") {
		t.Fatalf("LastLine = %q", got)
	}
	if len(tb.buf) > 4096 {
		t.Fatalf("buffer unbounded: %d bytes", len(tb.buf))
	}

	empty := &tailBuffer{}
	if got := empty.LastLine(); got != "" {
		t.Fatalf("empty buffer LastLine = %q", got)
	}
}
