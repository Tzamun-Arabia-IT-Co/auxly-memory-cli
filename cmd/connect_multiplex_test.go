package cmd

import (
	"runtime"
	"strings"
	"testing"
)

// TestSSHConnArgsMultiplexing guards the connection-reuse fix: off-Windows, every
// short remote command during a connect/provision must share ONE SSH connection
// (ControlMaster) so a burst of them can't trip the remote sshd's MaxStartups cap.
// On a Windows client ControlMaster is unsupported and must NOT be emitted.
func TestSSHConnArgsMultiplexing(t *testing.T) {
	p := remoteProfile{Method: "public", User: "u", Host: "example.test", Port: 22}
	joined := strings.Join(sshConnArgs(p), " ")

	// Base options are always present.
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Errorf("BatchMode missing: %s", joined)
	}
	if !strings.Contains(joined, "ConnectTimeout=10") {
		t.Errorf("ConnectTimeout missing: %s", joined)
	}

	if runtime.GOOS == "windows" {
		if strings.Contains(joined, "ControlMaster") {
			t.Errorf("ControlMaster must NOT be set on a Windows client (unsupported): %s", joined)
		}
		return
	}

	for _, want := range []string{"ControlMaster=auto", "ControlPath=", "ControlPersist"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in multiplexed args, got: %s", want, joined)
		}
	}
}

// TestSSHControlPathStable returns the same per-target socket path across calls so
// the master connection is actually reused (a changing path would defeat reuse).
func TestSSHControlPathStable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ControlMaster not used on Windows clients")
	}
	p := remoteProfile{Method: "public", User: "u", Host: "example.test", Port: 22}
	a, b := sshControlPath(p), sshControlPath(p)
	if a == "" {
		t.Fatal("sshControlPath returned empty (could not prepare ~/.ssh/auxly-cm)")
	}
	if a != b {
		t.Errorf("control path not stable across calls: %q vs %q", a, b)
	}
	if !strings.HasSuffix(a, "%C") {
		t.Errorf("expected %%C token (ssh expands to a per-connection hash), got %q", a)
	}
}
