package tui

import "testing"

func TestParseSessionRemote(t *testing.T) {
	cmd := "/Users/lab/.local/bin/auxly mcp-server --provider claude-code --source ssh-remote --remote-os linux --remote-host AiOPSSRV"
	clients := []clientRow{{Name: "AiOPSSRV", Target: "root@192.168.1.39", Method: "relay"}}

	s := parseSession(46288, cmd, map[int]procInfo{}, clients)

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
	if s.IP != "192.168.1.39" {
		t.Errorf("ip = %q, want 192.168.1.39 (resolved from clients.yaml)", s.IP)
	}
}

func TestParseSessionLocalEnvProvider(t *testing.T) {
	cmd := "/Users/lab/.local/bin/auxly --path /Users/lab/.auxly/memory mcp-server AUXLY_PROVIDER=cursor"
	s := parseSession(40044, cmd, map[int]procInfo{}, nil)

	if s.Remote {
		t.Fatalf("expected local session, got remote")
	}
	if s.Provider != "cursor" {
		t.Errorf("provider = %q, want cursor", s.Provider)
	}
	if s.PID != 40044 {
		t.Errorf("pid = %d, want 40044", s.PID)
	}
}

func TestParseSessionLocalFallback(t *testing.T) {
	cmd := "/Users/lab/.local/bin/auxly --path /Users/lab/.auxly/memory mcp-server"
	s := parseSession(64153, cmd, map[int]procInfo{}, nil)

	if s.Provider != "claude" {
		t.Errorf("provider = %q, want claude (default fallback)", s.Provider)
	}
}
