package cmd

import "testing"

// TestShouldRetryLauncher is the guard for connect-mcp resilience: retry ONLY on
// an ssh transport failure (exit 255 — remote mcp-server never ran) while
// attempts remain. Never on a non-255 exit (remote ran; stdin consumed; retry
// would corrupt the stateful MCP stream), never on a non-exit error, and never
// on the last attempt.
func TestShouldRetryLauncher(t *testing.T) {
	cases := []struct {
		name        string
		exitCode    int
		isExit      bool
		attempt     int
		maxAttempts int
		want        bool
	}{
		{"transport failure, attempts left", 255, true, 1, 3, true},
		{"transport failure, mid attempts", 255, true, 2, 3, true},
		{"transport failure, last attempt", 255, true, 3, 3, false},
		{"remote ran then failed (exit 1)", 1, true, 1, 3, false},
		{"remote clean-ish non-255 (exit 2)", 2, true, 1, 3, false},
		{"not a process exit (ssh failed to start)", 0, false, 1, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRetryLauncher(tc.exitCode, tc.isExit, tc.attempt, tc.maxAttempts)
			if got != tc.want {
				t.Errorf("shouldRetryLauncher(%d, %v, %d, %d) = %v, want %v",
					tc.exitCode, tc.isExit, tc.attempt, tc.maxAttempts, got, tc.want)
			}
		})
	}
}
