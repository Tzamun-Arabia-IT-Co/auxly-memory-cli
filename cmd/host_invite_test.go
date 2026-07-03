package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/invite"
)

// TestConsumeFingerprintPort proves `host consume`'s fingerprint check uses
// the PORT the invite itself pinned (from the Store record), not a
// hardcoded default — a token minted with `--port 2222` must have its
// recomputed fingerprint checked against 2222 or every consume on a
// nonstandard sshd port would mismatch.
func TestConsumeFingerprintPort(t *testing.T) {
	t.Run("uses the invite's own pinned port", func(t *testing.T) {
		store := invite.NewStore(t.TempDir())
		tok, err := invite.Mint("host.example", 2222, "SHA256:hostfp", time.Hour)
		if err != nil {
			t.Fatalf("Mint() error = %v", err)
		}
		if err := store.Add(tok); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		if got := consumeFingerprintPort(store, tok.Secret); got != 2222 {
			t.Fatalf("consumeFingerprintPort() = %d, want 2222", got)
		}
	})

	t.Run("falls back to the default port for an unknown secret", func(t *testing.T) {
		store := invite.NewStore(t.TempDir())
		if got := consumeFingerprintPort(store, "never-minted"); got != defaultSSHPort {
			t.Fatalf("consumeFingerprintPort() = %d, want default %d", got, defaultSSHPort)
		}
	})
}

// TestRegisterConsumedClient exercises the joiner-controlled --client/
// --hostname path in isolation: format validation (rejecting terminal-
// escape/YAML-hostile characters before they ever reach clients.yaml or the
// `auxly host clients` tabwriter table) and the name-collision guard
// (refusing to silently overwrite a DIFFERENT box's row).
func TestRegisterConsumedClient(t *testing.T) {
	isolateHome(t)

	t.Run("valid name/hostname registers cleanly", func(t *testing.T) {
		if err := registerConsumedClient("box-one", "box-one.local", "10.0.0.1:22"); err != nil {
			t.Fatalf("registerConsumedClient() error = %v", err)
		}
		c, ok := findClient("box-one")
		if !ok || c.Target != "10.0.0.1:22" {
			t.Fatalf("findClient(box-one) = %+v, %v, want a registered client with target 10.0.0.1:22", c, ok)
		}
	})

	t.Run("client name with a terminal escape sequence is rejected", func(t *testing.T) {
		if err := registerConsumedClient("evil\x1b[31m", "host", "10.0.0.2:22"); err == nil {
			t.Fatal("want an error for a client name containing an escape sequence, got nil")
		}
	})

	t.Run("hostname with shell/terminal metacharacters is rejected", func(t *testing.T) {
		if err := registerConsumedClient("box-two", "host\twith\ttabs", "10.0.0.3:22"); err == nil {
			t.Fatal("want an error for a hostname containing tabs, got nil")
		}
	})

	t.Run("name collision with a DIFFERENT existing box is refused, not overwritten", func(t *testing.T) {
		if err := registerConsumedClient("collide", "first.local", "10.0.0.4:22"); err != nil {
			t.Fatalf("registerConsumedClient() first call error = %v", err)
		}
		err := registerConsumedClient("collide", "second.local", "10.0.0.5:22")
		if err == nil {
			t.Fatal("want an error when re-registering an existing name under a different target, got nil")
		}
		if !strings.Contains(err.Error(), "already registered") {
			t.Errorf("error = %q, want it to say the client is already registered", err.Error())
		}
		// The original row must be untouched.
		c, ok := findClient("collide")
		if !ok || c.Target != "10.0.0.4:22" {
			t.Fatalf("findClient(collide) = %+v, %v, want the ORIGINAL target 10.0.0.4:22 preserved", c, ok)
		}
	})

	t.Run("re-registering the SAME box (same target) under its own name upserts in place", func(t *testing.T) {
		if err := registerConsumedClient("samebox", "a.local", "10.0.0.6:22"); err != nil {
			t.Fatalf("first registerConsumedClient() error = %v", err)
		}
		if err := registerConsumedClient("samebox", "b.local", "10.0.0.6:22"); err != nil {
			t.Fatalf("re-registering the same box: error = %v, want nil", err)
		}
		c, ok := findClient("samebox")
		if !ok || c.Hostname != "b.local" {
			t.Fatalf("findClient(samebox) = %+v, %v, want the updated hostname b.local", c, ok)
		}
	})
}

func TestClientAddrFromSSHConnection(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"typical sshd value", "203.0.113.7 54321 198.51.100.2 22", "203.0.113.7"},
		{"empty", "", ""},
		{"single token", "203.0.113.7", "203.0.113.7"},
		{"whitespace only", "   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clientAddrFromSSHConnection(tt.in); got != tt.want {
				t.Errorf("clientAddrFromSSHConnection(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestConsumeInviteAgainstTempStore exercises `host consume`'s core against a
// real invite.Store rooted in a temp dir — no HOME override, no live sshd, no
// SSH — proving the single-use + expiry + fingerprint-pin plumbing that
// `runHostConsume` wires up for real.
func TestConsumeInviteAgainstTempStore(t *testing.T) {
	t.Run("consumes a valid invite exactly once", func(t *testing.T) {
		store := invite.NewStore(t.TempDir())
		tok, err := invite.Mint("host.example", 22, "SHA256:hostfp", time.Hour)
		if err != nil {
			t.Fatalf("Mint() error = %v", err)
		}
		if err := store.Add(tok); err != nil {
			t.Fatalf("Add() error = %v", err)
		}

		rec, err := consumeInvite(store, tok.Secret, "SHA256:hostfp")
		if err != nil {
			t.Fatalf("consumeInvite() error = %v", err)
		}
		if rec.ID != tok.ID() {
			t.Fatalf("consumeInvite() rec.ID = %q, want %q", rec.ID, tok.ID())
		}

		// Re-join with the same (now consumed) token: friendly, greppable
		// "already used or unknown" — exactly what classifyJoinConsumeError
		// in join.go matches on.
		if _, err := consumeInvite(store, tok.Secret, "SHA256:hostfp"); err == nil {
			t.Fatal("second consumeInvite() with the same secret: want an error, got nil")
		} else if err.Error() != "invite already used or unknown" {
			t.Fatalf("second consumeInvite() error = %q, want %q", err.Error(), "invite already used or unknown")
		}
	})

	t.Run("expired invite", func(t *testing.T) {
		store := invite.NewStore(t.TempDir())
		tok, err := invite.Mint("host.example", 22, "SHA256:hostfp", -time.Hour)
		if err != nil {
			t.Fatalf("Mint() error = %v", err)
		}
		if err := store.Add(tok); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		if _, err := consumeInvite(store, tok.Secret, "SHA256:hostfp"); err == nil || err.Error() != "invite expired" {
			t.Fatalf("consumeInvite() on an expired invite: error = %v, want %q", err, "invite expired")
		}
	})

	t.Run("fingerprint mismatch — the host's current key doesn't match the pin", func(t *testing.T) {
		store := invite.NewStore(t.TempDir())
		tok, err := invite.Mint("host.example", 22, "SHA256:mintedwith", time.Hour)
		if err != nil {
			t.Fatalf("Mint() error = %v", err)
		}
		if err := store.Add(tok); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		_, err = consumeInvite(store, tok.Secret, "SHA256:rotatedkey")
		if err == nil {
			t.Fatal("consumeInvite() with a mismatched fingerprint: want an error, got nil")
		}
		if err.Error() != "invite fingerprint mismatch — this host's current SSH key does not match what was pinned at mint time" {
			t.Fatalf("unexpected error message: %q", err.Error())
		}
	})

	t.Run("unknown secret", func(t *testing.T) {
		store := invite.NewStore(t.TempDir())
		if _, err := consumeInvite(store, "never-minted", "SHA256:anything"); err == nil || err.Error() != "invite already used or unknown" {
			t.Fatalf("consumeInvite() on an unknown secret: error = %v, want %q", err, "invite already used or unknown")
		}
	})
}
