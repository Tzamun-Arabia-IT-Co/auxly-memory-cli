package sharing

import "testing"

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
