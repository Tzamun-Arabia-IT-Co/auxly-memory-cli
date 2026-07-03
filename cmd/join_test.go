package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/invite"
)

// fakeProbe returns a canned (fingerprint, error) pair — the seam
// joinPreflight uses in place of a live ssh-keyscan, mirroring the injected
// probe/dialer pattern used by pollVerifyWith/retryProbe's tests.
func fakeProbe(fp string, err error) func(string, int) (string, error) {
	return func(string, int) (string, error) { return fp, err }
}

func TestJoinPreflight(t *testing.T) {
	now := time.Now()

	t.Run("expired token rejected without contacting the host", func(t *testing.T) {
		tok := invite.Token{Host: "h", Port: 22, Fingerprint: "SHA256:x", Expires: now.Add(-time.Minute)}
		called := false
		probe := func(string, int) (string, error) { called = true; return "SHA256:x", nil }
		err := joinPreflight(tok, now, probe)
		if err == nil {
			t.Fatal("want an error for an expired token, got nil")
		}
		if called {
			t.Error("expired token must fail BEFORE the host is ever probed")
		}
	})

	t.Run("zero Expires treated as expired (fail closed)", func(t *testing.T) {
		tok := invite.Token{Host: "h", Port: 22, Fingerprint: "SHA256:x"}
		if err := joinPreflight(tok, now, fakeProbe("SHA256:x", nil)); err == nil {
			t.Fatal("want an error for a zero-value Expires, got nil")
		}
	})

	t.Run("probe failure surfaces as connectivity, not a bad invite", func(t *testing.T) {
		tok := invite.Token{Host: "h", Port: 22, Fingerprint: "SHA256:x", Expires: now.Add(time.Hour)}
		probeErr := errors.New("dial tcp: connection refused")
		err := joinPreflight(tok, now, fakeProbe("", probeErr))
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		if !strings.Contains(err.Error(), "connectivity") {
			t.Errorf("error = %q, want it to name a connectivity problem", err.Error())
		}
	})

	t.Run("fingerprint mismatch hard-aborts and names MITM", func(t *testing.T) {
		tok := invite.Token{Host: "h", Port: 22, Fingerprint: "SHA256:pinned", Expires: now.Add(time.Hour)}
		err := joinPreflight(tok, now, fakeProbe("SHA256:actual", nil))
		if err == nil {
			t.Fatal("want an error for a fingerprint mismatch, got nil")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "mitm") {
			t.Errorf("error = %q, want it to name a possible MITM", err.Error())
		}
	})

	t.Run("matching fingerprint on an unexpired token passes", func(t *testing.T) {
		tok := invite.Token{Host: "h", Port: 22, Fingerprint: "SHA256:match", Expires: now.Add(time.Hour)}
		if err := joinPreflight(tok, now, fakeProbe("SHA256:match", nil)); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})
}

func TestValidateInviteToken(t *testing.T) {
	valid := invite.Token{Host: "host.example.com", Port: 22, Secret: "abcdefg234567", Fingerprint: "SHA256:x"}

	t.Run("a well-formed token passes", func(t *testing.T) {
		if err := validateInviteToken(valid); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("secret with shell metacharacters is rejected — malformed invite", func(t *testing.T) {
		tok := valid
		tok.Secret = "abc;touch /tmp/pwned;"
		err := validateInviteToken(tok)
		if err == nil || err.Error() != "malformed invite" {
			t.Fatalf("validateInviteToken() = %v, want \"malformed invite\"", err)
		}
	})

	t.Run("secret with uppercase (outside the base32 alphabet) is rejected", func(t *testing.T) {
		tok := valid
		tok.Secret = "ABCDEFG234567"
		if err := validateInviteToken(tok); err == nil {
			t.Fatal("want an error for an uppercase secret, got nil")
		}
	})

	t.Run("empty host is rejected", func(t *testing.T) {
		tok := valid
		tok.Host = ""
		if err := validateInviteToken(tok); err == nil {
			t.Fatal("want an error for an empty host, got nil")
		}
	})

	t.Run("host with a leading '-' is rejected (argv flag smuggling)", func(t *testing.T) {
		tok := valid
		tok.Host = "-oProxyCommand=evil"
		if err := validateInviteToken(tok); err == nil {
			t.Fatal("want an error for a leading '-' host, got nil")
		}
	})

	t.Run("host with shell metacharacters is rejected", func(t *testing.T) {
		tok := valid
		tok.Host = "host;rm -rf ~"
		if err := validateInviteToken(tok); err == nil {
			t.Fatal("want an error for a host with shell metacharacters, got nil")
		}
	})

	t.Run("port 0 is rejected", func(t *testing.T) {
		tok := valid
		tok.Port = 0
		if err := validateInviteToken(tok); err == nil {
			t.Fatal("want an error for port 0, got nil")
		}
	})

	t.Run("port above 65535 is rejected", func(t *testing.T) {
		tok := valid
		tok.Port = 70000
		if err := validateInviteToken(tok); err == nil {
			t.Fatal("want an error for an out-of-range port, got nil")
		}
	})
}

// TestBuildConsumeArgvBlocksInjection proves the quoting independently of
// validateInviteToken: even a Secret carrying a shell command separator must
// come out the other end of buildConsumeArgv as ONE inert argument to `sh
// -c`, never as syntax the shell acts on. It actually runs the constructed
// command through /bin/sh (the same interpreter OpenSSH would hand it to on
// a POSIX host) and checks the injected side effect never happened — a
// direct proof, not just a string-matching guess.
func TestBuildConsumeArgvBlocksInjection(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	tok := invite.Token{Secret: "x;touch " + marker + ";", Host: "h", Port: 22}

	argv, err := buildConsumeArgv("auxly-definitely-not-a-real-binary-xyz123", tok, "victim'; touch "+marker+"; echo '")
	if err != nil {
		t.Fatalf("buildConsumeArgv() error = %v", err)
	}
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("buildConsumeArgv() = %v, want [\"sh\" \"-c\" <script>]", argv)
	}

	// Run it for real: the fake hostBin will fail with "command not found",
	// which is fine — we only care that `touch` never ran as a SEPARATE
	// command.
	_ = exec.Command("sh", "-c", argv[2]).Run()

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("shell injection: the marker file was created — Secret/client was interpreted as shell syntax, not quoted as a literal argument")
	}
}

func TestJoinCompletionMessage(t *testing.T) {
	t.Run("selftest failure: warns the invite is already consumed and reports failure", func(t *testing.T) {
		msg, joined := joinCompletionMessage("alice@host", "host", "⚠ selftest: FAIL probe: dial tcp: refused", false)
		if joined {
			t.Fatal("want joined=false when the selftest failed")
		}
		low := strings.ToLower(msg)
		if !strings.Contains(low, "consumed") {
			t.Errorf("message = %q, want it to mention the invite was already consumed", msg)
		}
		if !strings.Contains(low, "host invite") {
			t.Errorf("message = %q, want it to suggest re-minting an invite", msg)
		}
	})

	t.Run("selftest success: reports joined", func(t *testing.T) {
		msg, joined := joinCompletionMessage("alice@host", "host", "✅ OK (3 files)", true)
		if !joined {
			t.Fatal("want joined=true when the selftest passed")
		}
		if !strings.Contains(msg, "Joined") {
			t.Errorf("message = %q, want a success message", msg)
		}
	})
}

func TestClassifyJoinConsumeError(t *testing.T) {
	sshErr := errors.New("ssh exit status 1")

	tests := []struct {
		name        string
		out         string
		wantContain string
	}{
		{"unknown/reused invite", "invite already used or unknown\n", "invite already used or unknown"},
		{"expired invite", "invite expired\n", "invite expired"},
		{"fingerprint mismatch", "invite fingerprint mismatch — this host's current SSH key does not match what was pinned at mint time\n", "fingerprint mismatch"},
		{"ssh connectivity problem, not a bad invite", "ssh: connect to host h port 22: Connection refused\n", "connectivity"},
		{"auth failure", "Permission denied (publickey).\n", "connectivity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyJoinConsumeError(tt.out, sshErr)
			if err == nil {
				t.Fatal("want a non-nil error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantContain)) {
				t.Errorf("classifyJoinConsumeError(%q) = %q, want it to contain %q", tt.out, err.Error(), tt.wantContain)
			}
		})
	}
}
