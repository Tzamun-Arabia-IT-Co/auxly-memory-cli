package cmd

import "testing"

// TestUpsertRemoteDedupsByHostMethod is the regression guard for the duplicate
// bug: re-adding the same host+method under a DIFFERENT name must update the
// existing entry in place, not create a second row for the same box.
func TestUpsertRemoteDedupsByHostMethod(t *testing.T) {
	isolateHome(t)

	first := remoteProfile{Name: "raqeb", Method: "lan", OS: "linux", User: "root", Host: "192.168.1.169"}
	if err := upsertRemote(first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same host+method+user, different name → must REPLACE, not add.
	if err := upsertRemote(remoteProfile{Name: "raqeb-renamed", Method: "lan", OS: "linux", User: "root", Host: "192.168.1.169"}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	cfg, err := loadRemotes()
	if err != nil {
		t.Fatalf("loadRemotes: %v", err)
	}
	if len(cfg.Remotes) != 1 {
		t.Fatalf("expected 1 remote (deduped by host+method), got %d: %+v", len(cfg.Remotes), cfg.Remotes)
	}
	if cfg.Remotes[0].Name != "raqeb-renamed" {
		t.Errorf("expected the entry updated in place to %q, got %q", "raqeb-renamed", cfg.Remotes[0].Name)
	}
}

// TestUpsertRemoteDistinctHostsCoexist verifies dedup does not over-collapse:
// different hosts (or same host, different method) remain separate rows.
func TestUpsertRemoteDistinctHostsCoexist(t *testing.T) {
	isolateHome(t)

	_ = upsertRemote(remoteProfile{Name: "a", Method: "lan", User: "root", Host: "192.168.1.10"})
	_ = upsertRemote(remoteProfile{Name: "b", Method: "lan", User: "root", Host: "192.168.1.11"})
	_ = upsertRemote(remoteProfile{Name: "c", Method: "bastion", User: "root", Host: "192.168.1.10"}) // same host, different method

	cfg, err := loadRemotes()
	if err != nil {
		t.Fatalf("loadRemotes: %v", err)
	}
	if len(cfg.Remotes) != 3 {
		t.Fatalf("expected 3 distinct remotes, got %d: %+v", len(cfg.Remotes), cfg.Remotes)
	}
}

// TestUpsertRemoteUpdatesByName keeps the original behaviour: same name updates
// the same row (e.g. changing a host's method) without duplicating.
func TestUpsertRemoteUpdatesByName(t *testing.T) {
	isolateHome(t)

	_ = upsertRemote(remoteProfile{Name: "box", Method: "lan", User: "root", Host: "192.168.1.20"})
	_ = upsertRemote(remoteProfile{Name: "box", Method: "vpn", User: "root", Host: "10.8.0.20"})

	cfg, _ := loadRemotes()
	if len(cfg.Remotes) != 1 {
		t.Fatalf("expected 1 remote (same name updated), got %d", len(cfg.Remotes))
	}
	if cfg.Remotes[0].Method != "vpn" || cfg.Remotes[0].Host != "10.8.0.20" {
		t.Errorf("expected in-place update to vpn/10.8.0.20, got %s/%s", cfg.Remotes[0].Method, cfg.Remotes[0].Host)
	}
}

// TestUpsertClientDedupsByTarget guards the inbound-client equivalent: the same
// box re-provisioned under a different name must not create a duplicate row
// (the auxly-server vs auxly-srvr case on the same .24 target).
func TestUpsertClientDedupsByTarget(t *testing.T) {
	isolateHome(t)

	if err := upsertClient(clientEntry{Name: "auxly-server", Target: "root@192.168.1.24", Method: "relay"}); err != nil {
		t.Fatalf("first upsertClient: %v", err)
	}
	if err := upsertClient(clientEntry{Name: "auxly-srvr", Target: "root@192.168.1.24", Method: "relay"}); err != nil {
		t.Fatalf("second upsertClient: %v", err)
	}

	cs, err := loadClients()
	if err != nil {
		t.Fatalf("loadClients: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("expected 1 client (deduped by target), got %d: %+v", len(cs), cs)
	}
	if cs[0].Name != "auxly-srvr" {
		t.Errorf("expected in-place update to %q, got %q", "auxly-srvr", cs[0].Name)
	}
}

// TestRemoveClientEntryPersists is the regression guard for "deleted clients
// come back": removeClientEntry must drop exactly the named box and persist the
// remaining set to disk.
func TestRemoveClientEntryPersists(t *testing.T) {
	isolateHome(t)

	_ = upsertClient(clientEntry{Name: "AiOPSSRV", Target: "root@192.168.1.39", Method: "relay"})
	_ = upsertClient(clientEntry{Name: "MM", Target: "root@192.168.1.166", Method: "relay"})

	if err := removeClientEntry("MM"); err != nil {
		t.Fatalf("removeClientEntry: %v", err)
	}

	cs, _ := loadClients()
	if len(cs) != 1 || cs[0].Name != "AiOPSSRV" {
		t.Fatalf("expected only AiOPSSRV to remain, got %+v", cs)
	}
}
