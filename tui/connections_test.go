package tui

import (
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/session"
)

func TestSessionFromRecordRemote(t *testing.T) {
	r := session.Record{
		PID:        46288,
		Provider:   "claude-code",
		Source:     "ssh-remote",
		RemoteHost: "AiOPSSRV",
		RemoteOS:   "linux",
		RemoteIP:   "::1",
	}
	clients := []clientRow{{Name: "AiOPSSRV", Target: "root@192.168.1.39", Method: "relay"}}

	s := sessionFromRecord(r, clients)

	if !s.Remote {
		t.Fatalf("expected remote session, got local")
	}
	if s.Provider != "claude-code" {
		t.Errorf("provider = %q, want claude-code", s.Provider)
	}
	if s.Host != "AiOPSSRV" {
		t.Errorf("host = %q, want AiOPSSRV", s.Host)
	}
	if s.OS != "linux" {
		t.Errorf("os = %q, want linux", s.OS)
	}
	// clients.yaml lookup must win over the useless tunnel IP (::1).
	if s.IP != "192.168.1.39" {
		t.Errorf("ip = %q, want 192.168.1.39 (resolved from clients.yaml)", s.IP)
	}
}

func TestSessionFromRecordRemoteFallbackIP(t *testing.T) {
	r := session.Record{PID: 1, Provider: "cursor", Source: "ssh-remote", RemoteHost: "Unknown", RemoteIP: "10.0.0.5"}
	s := sessionFromRecord(r, nil) // no clients.yaml entry

	if s.IP != "10.0.0.5" {
		t.Errorf("ip = %q, want 10.0.0.5 (record fallback when host not in clients.yaml)", s.IP)
	}
}

func TestSessionFromRecordLocal(t *testing.T) {
	r := session.Record{PID: 40044, Provider: "claude", Source: "local"}
	s := sessionFromRecord(r, nil)

	if s.Remote {
		t.Fatalf("expected local session, got remote")
	}
	if s.Provider != "claude" {
		t.Errorf("provider = %q, want claude", s.Provider)
	}
	if s.PID != 40044 {
		t.Errorf("pid = %d, want 40044", s.PID)
	}
}

func TestSessionFromRecordDefaultProvider(t *testing.T) {
	s := sessionFromRecord(session.Record{PID: 5, Source: "local"}, nil)
	if s.Provider != "claude" {
		t.Errorf("provider = %q, want claude (default)", s.Provider)
	}
}
