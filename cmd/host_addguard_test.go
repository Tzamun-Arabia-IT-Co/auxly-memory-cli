package cmd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These guard the "adding a new server always errors" fix. A failed add used to
// persist its relay into host.yaml anyway, so every attempt left behind an
// entry the keep-alive would then dial forever.

// TestRelaySSHReachable_RefusedNamesThePort verifies the pre-save probe fails on
// a dead port AND says which port it tried — the real-world cause is an sshd on
// a non-default port, which is invisible if the error just says "unreachable".
func TestRelaySSHReachable_RefusedNamesThePort(t *testing.T) {
	// Bind then immediately close, so the port is near-certainly free/refusing.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	err = relaySSHReachable("127.0.0.1", port)
	if err == nil {
		t.Fatal("relaySSHReachable succeeded against a closed port — a dead relay would be saved to host.yaml")
	}
	if !strings.Contains(err.Error(), "user@127.0.0.1:PORT") {
		t.Errorf("error should tell the user how to supply a non-default port, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Nothing was saved") {
		t.Errorf("error should state that nothing was persisted, got: %v", err)
	}
}

// TestRelaySSHReachable_OpenPortPasses proves the probe is a plain TCP dial and
// not an auth check — setup's whole job is to install the key, so a relay that
// answers the connection must pass even though nothing SSH-ish is listening.
func TestRelaySSHReachable_OpenPortPasses(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	if err := relaySSHReachable("127.0.0.1", port); err != nil {
		t.Fatalf("relaySSHReachable rejected a reachable port: %v", err)
	}
}

// TestHostConfigExists_RollbackDiscrimination covers the rollback guard: a
// failed add must remove only an entry IT added, never one the user already had.
func TestHostConfigExists_RollbackDiscrimination(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows
	if err := os.MkdirAll(filepath.Join(home, ".auxly"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if hostConfigExists("root@example.com") {
		t.Fatal("hostConfigExists reported a relay in an empty config")
	}

	hc := hostConfig{Rendezvous: "root@example.com", ReversePort: 2222, HostUser: "lab"}
	if err := upsertHostConfig(hc); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !hostConfigExists("root@example.com") {
		t.Fatal("hostConfigExists missed a saved relay — a retry would wrongly delete the user's own entry")
	}
	// Match must ignore case/padding, same as upsert/remove do.
	if !hostConfigExists("  ROOT@EXAMPLE.COM  ") {
		t.Error("hostConfigExists should match case-insensitively and ignore surrounding space")
	}
	if hostConfigExists("root@other.example.com") {
		t.Error("hostConfigExists matched a relay that was never saved")
	}
}
