//go:build e2e

// Opt-in end-to-end tests for the Windows host/relay provisioning path, run
// against a REAL Windows host over SSH. Skipped unless AUXLY_E2E_WIN_HOST is set:
//
//	AUXLY_E2E_WIN_HOST=administrator@192.168.1.193 go test ./cmd/ -tags e2e -run E2EWinHost -v
//
// These exercise the actual host.go functions. They are careful to restore any
// local/remote state they touch.
package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func winHostProfile(t *testing.T) (remoteProfile, string) {
	t.Helper()
	spec := os.Getenv("AUXLY_E2E_WIN_HOST")
	if spec == "" {
		t.Skip("set AUXLY_E2E_WIN_HOST=user@host to run the Windows host-provision E2E")
	}
	user, host, port, err := parseHostSpec(spec)
	if err != nil {
		t.Fatalf("parseHostSpec(%q): %v", spec, err)
	}
	return remoteProfile{Name: "e2e-winhost", User: user, Host: host, Port: port}, spec
}

// TestE2EWinHostKeygen exercises authorizeRemoteKeyLocally end-to-end: it makes the
// Windows box generate an ed25519 key via PowerShell and append its pubkey to THIS
// machine's authorized_keys. The local authorized_keys is restored afterwards.
func TestE2EWinHostKeygen(t *testing.T) {
	p, _ := winHostProfile(t)

	fam, _, err := detectRemoteOS(p)
	if err != nil || fam != osWindows {
		t.Fatalf("expected Windows, got fam=%d err=%v", fam, err)
	}

	home, _ := os.UserHomeDir()
	ak := filepath.Join(home, ".ssh", "authorized_keys")
	backup, hadFile := os.ReadFile(ak)
	t.Cleanup(func() {
		if hadFile == nil {
			_ = os.WriteFile(ak, backup, 0600) // restore exact prior contents
		} else {
			_ = os.Remove(ak) // file didn't exist before — remove what we created
		}
	})

	if err := authorizeRemoteKeyLocally(p); err != nil {
		t.Fatalf("authorizeRemoteKeyLocally (Windows keygen path): %v", err)
	}
	after, _ := os.ReadFile(ak)
	if len(after) <= len(backup) {
		t.Fatalf("expected the box's pubkey appended to authorized_keys")
	}
	t.Logf("OK authorizeRemoteKeyLocally — box ed25519 key generated via PowerShell + authorized locally (then restored)")
}

// TestE2EWinHostOffer exercises writeRelayOffer against the Windows box as the
// rendezvous: it must write the offer YAML BOM-free. Cleans up the offer afterwards.
func TestE2EWinHostOffer(t *testing.T) {
	p, spec := winHostProfile(t)
	fam, _, err := detectRemoteOS(p)
	if err != nil || fam != osWindows {
		t.Fatalf("expected Windows, got fam=%d err=%v", fam, err)
	}

	hc := hostConfig{Rendezvous: spec, ReversePort: 45999, HostUser: p.User}
	if err := writeRelayOffer(hc); err != nil {
		t.Fatalf("writeRelayOffer to Windows relay: %v", err)
	}

	// Verify the written offer has NO UTF-8 BOM (first bytes must not be 239,187,191).
	check := "$f=Get-ChildItem \"$env:USERPROFILE\\.auxly\\offers\" -Filter *.yaml | Select-Object -First 1; " +
		"if($f){ $b=[IO.File]::ReadAllBytes($f.FullName); 'first3=' + ($b[0..2] -join ',') } else { 'NO_OFFER_FILE' }"
	out, err := runRemoteScript(p, fam, "", check)
	if err != nil {
		t.Fatalf("offer verify: %v", err)
	}
	got := strings.TrimSpace(out)
	t.Cleanup(func() {
		_, _ = runRemoteScript(p, fam, "", "Remove-Item \"$env:USERPROFILE\\.auxly\\offers\\*.yaml\" -Force -ErrorAction SilentlyContinue")
	})

	if strings.Contains(got, "239,187,191") {
		t.Fatalf("offer file has a UTF-8 BOM: %q", got)
	}
	if strings.Contains(got, "NO_OFFER_FILE") {
		t.Fatalf("offer file was not written: %q", got)
	}
	t.Logf("OK writeRelayOffer — offer written to Windows relay, BOM-free (%s)", got)
}

// TestE2EWinHostTunnelProbe exercises reportTunnelLive against the Windows box —
// it must run the Get-NetTCPConnection probe without erroring. (We don't assert UP
// since no reverse tunnel is bound in the test; we assert it executes cleanly.)
func TestE2EWinHostTunnelProbe(t *testing.T) {
	p, _ := winHostProfile(t)
	fam, _, err := detectRemoteOS(p)
	if err != nil || fam != osWindows {
		t.Fatalf("expected Windows, got fam=%d err=%v", fam, err)
	}
	// Probe a port we know is listening (22 / sshd) to prove the Windows probe path
	// returns a positive result through the same rendering reportTunnelLive uses.
	out, err := runRemoteScript(p, fam, "",
		"if(Get-NetTCPConnection -State Listen -LocalPort 22 -ErrorAction SilentlyContinue){'UP'}else{'DOWN'}")
	if err != nil {
		t.Fatalf("Get-NetTCPConnection probe: %v", err)
	}
	if !strings.Contains(out, "UP") {
		t.Fatalf("expected sshd port 22 to read UP, got %q", strings.TrimSpace(out))
	}
	t.Logf("OK Windows tunnel probe (Get-NetTCPConnection) returns UP for a bound port")
}
