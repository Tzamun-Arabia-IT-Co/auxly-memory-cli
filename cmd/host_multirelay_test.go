package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points HOME at a fresh temp dir so host.yaml reads/writes never
// touch the real ~/.auxly during tests.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func relay(rendezvous string) hostConfig {
	return hostConfig{Rendezvous: rendezvous, ReversePort: defaultReversePort, LocalSSHPort: defaultSSHPort, HostUser: "lab"}
}

// TestUpsertAppendsNotOverwrites is the core regression guard for the singleton
// bug: connecting a second box must ADD a relay, leaving the first intact.
func TestUpsertAppendsNotOverwrites(t *testing.T) {
	isolateHome(t)

	if err := upsertHostConfig(relay("root@10.0.0.168")); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := upsertHostConfig(relay("root@10.0.0.141")); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	relays, ok, err := loadHostConfigs()
	if err != nil || !ok {
		t.Fatalf("loadHostConfigs: ok=%v err=%v", ok, err)
	}
	if len(relays) != 2 {
		t.Fatalf("relay count = %d, want 2 (the bug was the 2nd overwriting the 1st)", len(relays))
	}
	got := map[string]bool{}
	for _, r := range relays {
		got[r.Rendezvous] = true
	}
	if !got["root@10.0.0.168"] || !got["root@10.0.0.141"] {
		t.Errorf("both relays should survive, got %v", got)
	}
}

func TestUpsertReplacesSameRendezvous(t *testing.T) {
	isolateHome(t)
	_ = upsertHostConfig(relay("root@10.0.0.168"))

	dup := relay("root@10.0.0.168")
	dup.HostUser = "changed"
	if err := upsertHostConfig(dup); err != nil {
		t.Fatalf("upsert dup: %v", err)
	}

	relays, _, _ := loadHostConfigs()
	if len(relays) != 1 {
		t.Fatalf("relay count = %d, want 1 (same rendezvous must replace, not duplicate)", len(relays))
	}
	if relays[0].HostUser != "changed" {
		t.Errorf("HostUser = %q, want the replacement value", relays[0].HostUser)
	}
}

func TestRemoveHostConfig(t *testing.T) {
	isolateHome(t)
	_ = upsertHostConfig(relay("root@10.0.0.168"))
	_ = upsertHostConfig(relay("root@10.0.0.141"))

	remaining, err := removeHostConfig("root@10.0.0.168")
	if err != nil {
		t.Fatalf("removeHostConfig: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining = %d, want 1", remaining)
	}
	relays, _, _ := loadHostConfigs()
	if len(relays) != 1 || relays[0].Rendezvous != "root@10.0.0.141" {
		t.Errorf("after remove, relays = %+v, want just .141", relays)
	}
}

// TestLegacySingleRelayMigrates ensures a pre-existing single-relay host.yaml is
// read as one relay and rewritten into the new list form on next save.
func TestLegacySingleRelayMigrates(t *testing.T) {
	home := isolateHome(t)
	dir := filepath.Join(home, ".auxly")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	legacy := "rendezvous: root@10.0.0.168\nreverse_port: 2222\nlocal_ssh_port: 22\nhost_user: admin\n"
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	relays, ok, err := loadHostConfigs()
	if err != nil || !ok {
		t.Fatalf("loadHostConfigs on legacy: ok=%v err=%v", ok, err)
	}
	if len(relays) != 1 || relays[0].Rendezvous != "root@10.0.0.168" {
		t.Fatalf("legacy parse = %+v, want one relay .168", relays)
	}

	// Adding a second box migrates the file to the list form.
	if err := upsertHostConfig(relay("root@10.0.0.141")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "host.yaml"))
	if !strings.Contains(string(data), "relays:") {
		t.Errorf("host.yaml not migrated to list form:\n%s", data)
	}
	relays, _, _ = loadHostConfigs()
	if len(relays) != 2 {
		t.Errorf("after migration relay count = %d, want 2", len(relays))
	}
}

func TestLoadHostConfigReturnsFirst(t *testing.T) {
	isolateHome(t)
	_ = upsertHostConfig(relay("root@10.0.0.168"))
	_ = upsertHostConfig(relay("root@10.0.0.141"))

	hc, ok, err := loadHostConfig()
	if err != nil || !ok {
		t.Fatalf("loadHostConfig: ok=%v err=%v", ok, err)
	}
	if hc.Rendezvous != "root@10.0.0.168" {
		t.Errorf("first relay = %q, want root@10.0.0.168", hc.Rendezvous)
	}
}

func TestNoConfigIsNotServing(t *testing.T) {
	isolateHome(t)
	if _, ok, err := loadHostConfigs(); ok || err != nil {
		t.Errorf("empty home: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

// TestTunnelArgsPerRelay verifies each relay builds its own independent reverse
// forward — distinct target hosts, same loopback reverse port.
func TestTunnelArgsPerRelay(t *testing.T) {
	for _, host := range []string{"root@10.0.0.39", "root@10.0.0.141"} {
		args := tunnelArgs(relay(host))
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-R 2222:localhost:22") {
			t.Errorf("%s: missing reverse forward in %q", host, joined)
		}
		if args[len(args)-1] != host {
			t.Errorf("%s: tunnel target = %q, want %q", host, args[len(args)-1], host)
		}
	}
}
