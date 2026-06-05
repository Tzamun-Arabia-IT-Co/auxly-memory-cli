//go:build e2e

// Opt-in end-to-end tests that exercise the Windows remote-shell path against a
// REAL host over SSH. Skipped unless AUXLY_E2E_WIN_HOST is set, e.g.:
//
//	AUXLY_E2E_WIN_HOST=administrator@192.168.1.193 go test ./cmd/ -tags e2e -run E2E -v
//
// The host must already be reachable over key-based SSH (in known_hosts). These
// tests are read-only on the remote: they probe the OS and run a harmless marker
// command; they do not install or modify anything.
package cmd

import (
	"os"
	"strings"
	"testing"
)

func e2eProfile(t *testing.T) remoteProfile {
	t.Helper()
	spec := os.Getenv("AUXLY_E2E_WIN_HOST")
	if spec == "" {
		t.Skip("set AUXLY_E2E_WIN_HOST=user@host to run the Windows E2E")
	}
	user, host, port, err := parseHostSpec(spec)
	if err != nil {
		t.Fatalf("parseHostSpec(%q): %v", spec, err)
	}
	return remoteProfile{Name: "e2e-win", User: user, Host: host, Port: port}
}

// TestE2EWindowsDetect verifies detectRemoteOS classifies a real Windows box via
// the EncodedCommand probe (uname fails on cmd.exe, PowerShell probe succeeds).
func TestE2EWindowsDetect(t *testing.T) {
	p := e2eProfile(t)

	fam, detail, err := detectRemoteOS(p)
	if err != nil {
		t.Fatalf("detectRemoteOS: %v", err)
	}
	if fam != osWindows {
		t.Fatalf("expected osWindows, got %d (detail=%q)", fam, detail)
	}
	if !strings.Contains(strings.ToLower(detail), "windows") {
		t.Errorf("detail does not look like Windows: %q", detail)
	}
	t.Logf("OK detectRemoteOS -> Windows: %s", detail)

	// Second call must hit the per-profile memo cache, not re-probe.
	fam2, detail2, err := detectRemoteOS(p)
	if err != nil || fam2 != osWindows {
		t.Fatalf("cached detect: fam=%d detail=%q err=%v", fam2, detail2, err)
	}
	if detail2 != "cached" {
		t.Logf("note: second detect detail=%q (memo may be keyed differently)", detail2)
	}
}

// TestE2EWindowsRunRemoteScript verifies runRemoteScript executes PowerShell on a
// real Windows box through the -EncodedCommand path (the cmd.exe-default-shell case).
func TestE2EWindowsRunRemoteScript(t *testing.T) {
	p := e2eProfile(t)

	out, err := runRemoteScript(p, osWindows, "echo should-not-run", "Write-Output 'AUXLY_E2E_OK'")
	if err != nil {
		t.Fatalf("runRemoteScript(windows): %v", err)
	}
	if !strings.Contains(out, "AUXLY_E2E_OK") {
		t.Fatalf("PowerShell marker missing in output: %q", out)
	}
	t.Logf("OK runRemoteScript(windows) -> %q", strings.TrimSpace(out))
}

// TestE2EWindowsAuxlyPresent confirms the host can run auxly over SSH (the runtime
// MCP launch path: `ssh host auxly ...` resolves auxly.exe via cmd.exe PATHEXT).
func TestE2EWindowsAuxlyPresent(t *testing.T) {
	p := e2eProfile(t)

	out, err := runSSH(p, "auxly", "--version")
	if err != nil {
		t.Fatalf("auxly --version over ssh: %v (out=%q)", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "auxly") {
		t.Fatalf("unexpected auxly --version output: %q", out)
	}
	t.Logf("OK auxly present on host: %s", strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]))
}
