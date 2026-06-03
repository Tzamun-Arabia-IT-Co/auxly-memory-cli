package sharing

import (
	"os"
	"path/filepath"
	"testing"
)

var vault = []string{
	"identity.md", "preferences.md", "infra.md", "projects.md",
	"personal.md", "unified_memory.md",
}

func TestAllowedReads_DefaultExcludesPersonalAndDump(t *testing.T) {
	allowed := AllowedReads(nil, vault)
	if allowed["personal.md"] {
		t.Error("FAIL-CLOSED VIOLATION: personal.md must NOT be default-shared to a remote")
	}
	if allowed["unified_memory.md"] {
		t.Error("FAIL-CLOSED VIOLATION: unified_memory.md (aggregate) must NOT be default-shared")
	}
	for _, f := range []string{"identity.md", "infra.md", "projects.md", "preferences.md"} {
		if !allowed[f] {
			t.Errorf("shared file %s should be readable by default", f)
		}
	}
}

func TestAllowedReads_ExplicitListHonored(t *testing.T) {
	// host explicitly grants personal.md → it becomes readable
	share := &ClientShare{SharedFiles: []string{"infra.md", "personal.md"}}
	allowed := AllowedReads(share, vault)
	if !allowed["personal.md"] || !allowed["infra.md"] {
		t.Error("explicit SharedFiles must be honored exactly")
	}
	if allowed["projects.md"] {
		t.Error("files not in the explicit list must be denied")
	}
}

func TestCanWrite_DefaultReadOnly(t *testing.T) {
	// nil share = default = read-only → no writes at all
	if CanWrite(nil, "infra.md", vault) {
		t.Error("default (no config) must be read-only — writes denied")
	}
}

func TestCanWrite_RequiresWriteAccessAndVisibility(t *testing.T) {
	rw := &ClientShare{Access: AccessWrite} // write access, default file set (no personal)
	if !CanWrite(rw, "infra.md", vault) {
		t.Error("write access + visible shared file should allow write")
	}
	if CanWrite(rw, "personal.md", vault) {
		t.Error("personal.md must NOT be writable without an explicit grant")
	}
}

func TestCanWrite_PersonalRequiresExplicitGrant(t *testing.T) {
	rw := &ClientShare{Access: AccessWrite, SharedFiles: []string{"personal.md"}}
	if !CanWrite(rw, "personal.md", vault) {
		t.Error("explicit personal grant + write access should allow personal write")
	}
}

func TestCanWrite_PerFileWriteListIsAuthoritative(t *testing.T) {
	// New per-file model: write_files names exactly what is writable. infra.md is
	// writable, projects.md is shared but read-only, personal.md is not shared.
	share := &ClientShare{
		SharedFiles: []string{"infra.md", "projects.md"},
		WriteFiles:  []string{"infra.md"},
	}
	if !CanWrite(share, "infra.md", vault) {
		t.Error("infra.md is in write_files → must be writable")
	}
	if CanWrite(share, "projects.md", vault) {
		t.Error("projects.md is shared but NOT in write_files → must be read-only")
	}
	if !CanRead(share, "projects.md", vault) {
		t.Error("projects.md is shared → must stay readable")
	}
}

func TestCanWrite_WriteListOverridesLegacyAccess(t *testing.T) {
	// When write_files is present it wins over a legacy global Access=write, so a
	// stray access flag cannot widen the per-file grant.
	share := &ClientShare{
		SharedFiles: []string{"infra.md", "projects.md"},
		WriteFiles:  []string{"projects.md"},
		Access:      AccessWrite,
	}
	if CanWrite(share, "infra.md", vault) {
		t.Error("infra.md not in write_files → legacy Access=write must not grant it")
	}
	if !CanWrite(share, "projects.md", vault) {
		t.Error("projects.md in write_files → writable")
	}
}

// TestLoadForRemoteHost_MatchesByHostnameNotJustTargetIP reproduces the reported
// sharing bug: a relay box is keyed in clients.yaml by IP in `target`, but at
// connect time it reports its OWN hostname as RemoteHost (connect.go passes
// `--remote-host localHostname()`). The host stores that hostname in the `hostname`
// field. The ACL lookup must match on hostname/name too — otherwise the per-file
// write grants are silently ignored and the remote falls back to read-only
// ("Writable: none") even though the TUI shows Read+Write.
func TestLoadForRemoteHost_MatchesByHostnameNotJustTargetIP(t *testing.T) {
	dir := t.TempDir()
	clientsYAML := `clients:
    - name: auxly-server
      target: root@192.168.1.24
      method: relay
      hostname: auxly.tzamun.dev
      shared_files:
        - identity.md
        - infra.md
      write_files:
        - identity.md
        - infra.md
      access: read
`
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), []byte(clientsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	memoryPath := filepath.Join(dir, "memory") // clients.yaml sits in its parent dir

	// The box connects reporting its hostname (NOT the target IP) as RemoteHost.
	share := LoadForRemoteHost(memoryPath, "auxly.tzamun.dev")
	if share == nil {
		t.Fatal("ACL must load when the box reports its hostname (clients.yaml `hostname`) — got nil → read-only")
	}
	if !CanWrite(share, "identity.md", vault) {
		t.Error("identity.md is in write_files → must be writable for the matched client")
	}

	// Back-compat: matching by the target IP must still work.
	if LoadForRemoteHost(memoryPath, "192.168.1.24") == nil {
		t.Error("ACL must also load when RemoteHost is the target IP (back-compat)")
	}
	// And by the friendly name (clientIsLive matches it too).
	if LoadForRemoteHost(memoryPath, "auxly-server") == nil {
		t.Error("ACL should load when RemoteHost matches the client name")
	}
	// Case-insensitive: os.Hostname() casing varies across boxes.
	if LoadForRemoteHost(memoryPath, "Auxly.Tzamun.Dev") == nil {
		t.Error("hostname match must be case-insensitive")
	}
}

func TestHostMatches(t *testing.T) {
	cases := []struct {
		target, remote string
		want           bool
	}{
		{"root@192.168.1.5:22", "192.168.1.5", true},
		{"root@192.168.1.5", "192.168.1.5", true},
		{"192.168.1.5:2222", "192.168.1.5", true},
		{"user@boxA", "boxA", true},
		{"root@192.168.1.5:22", "192.168.1.9", false},
		{"", "192.168.1.5", false},
	}
	for _, c := range cases {
		if got := hostMatches(c.target, c.remote); got != c.want {
			t.Errorf("hostMatches(%q,%q)=%v want %v", c.target, c.remote, got, c.want)
		}
	}
}
