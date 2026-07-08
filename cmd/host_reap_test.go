package cmd

import (
	"strings"
	"testing"
)

// TestPgrepMatchArgs_HasDashTerminator guards the `host down` reap fix: the
// per-relay kill patterns start with "-R …", and `pgrep -f` treats a
// dash-leading pattern as a flag (exit 2) unless it is passed after `--`.
// Without the terminator findTunnelPIDs matched nothing, so `host down` reaped
// no tunnels and the host stayed "serving". If someone drops the `--`, this
// fails.
func TestPgrepMatchArgs_HasDashTerminator(t *testing.T) {
	args := pgrepMatchArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--") {
		t.Fatalf("pgrepMatchArgs() = %v — missing the `--` terminator; a dash-leading pattern like `-R 2222:localhost:22` will make pgrep error and reap nothing", args)
	}
	// `--` must come LAST (right before the pattern) so it terminates options.
	if args[len(args)-1] != "--" {
		t.Errorf("`--` must be the final flag before the pattern, got %v", args)
	}
	// -f (match full command line) must still be present.
	if !strings.Contains(joined, "-f") {
		t.Errorf("pgrepMatchArgs() lost -f (full command-line match): %v", args)
	}
}
