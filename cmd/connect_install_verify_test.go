package cmd

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestWithoutMuxIsolatesConnection guards the v1.0.18 socket-isolation fix: the
// install + readiness poll must run on their OWN SSH connection (no shared
// ControlMaster), so a lingering Windows install session can never wedge the
// socket the later provisioning steps reuse. Off-Windows, a normal profile is
// multiplexed; withoutMux must drop the ControlMaster options while leaving the
// rest of the connection args intact.
func TestWithoutMuxIsolatesConnection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ControlMaster is never emitted on a Windows client")
	}
	p := remoteProfile{Method: "public", User: "u", Host: "example.test", Port: 22}

	muxed := strings.Join(sshConnArgs(p), " ")
	if !strings.Contains(muxed, "ControlMaster") {
		t.Fatalf("baseline profile should be multiplexed off-Windows, got: %s", muxed)
	}

	isolated := strings.Join(sshConnArgs(withoutMux(p)), " ")
	if strings.Contains(isolated, "ControlMaster") {
		t.Errorf("withoutMux must NOT emit ControlMaster: %s", isolated)
	}
	// The rest of the connection contract is unchanged.
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout=10"} {
		if !strings.Contains(isolated, want) {
			t.Errorf("withoutMux dropped %q: %s", want, isolated)
		}
	}
	// withoutMux is a value copy — the original profile stays multiplexable.
	if p.noMux {
		t.Errorf("withoutMux mutated the original profile")
	}
}

func TestPollVerifyAuxly(t *testing.T) {
	t.Run("returns version once the box reports ready", func(t *testing.T) {
		calls := 0
		probe := func() (string, error) {
			calls++
			if calls < 3 {
				return "", errors.New("ssh: command not found: auxly")
			}
			return "auxly 1.0.18\nextra", nil
		}
		ver, err := pollVerifyWith(probe, 5*time.Second, 0)
		if err != nil {
			t.Fatalf("want nil after install finishes, got %v", err)
		}
		if ver != "auxly 1.0.18" {
			t.Fatalf("want first line of version, got %q", ver)
		}
		if calls != 3 {
			t.Fatalf("want 3 probes (fail, fail, ready), got %d", calls)
		}
	})

	t.Run("surfaces the last error if it never becomes ready", func(t *testing.T) {
		want := errors.New("connection refused")
		ver, err := pollVerifyWith(func() (string, error) { return "", want }, 5*time.Millisecond, time.Millisecond)
		if err == nil {
			t.Fatalf("want a timeout error, got ver=%q", ver)
		}
		if !errors.Is(err, want) {
			t.Fatalf("want wrapped last error %v, got %v", want, err)
		}
	})

	t.Run("ignores empty-but-no-error output and keeps polling", func(t *testing.T) {
		calls := 0
		probe := func() (string, error) {
			calls++
			if calls == 1 {
				return "   ", nil // whitespace only — not ready yet
			}
			return "auxly 1.0.18", nil
		}
		ver, err := pollVerifyWith(probe, time.Second, 0)
		if err != nil {
			t.Fatalf("want nil, got %v", err)
		}
		if ver != "auxly 1.0.18" {
			t.Fatalf("want version, got %q", ver)
		}
		if calls != 2 {
			t.Fatalf("want 2 probes, got %d", calls)
		}
	})
}
