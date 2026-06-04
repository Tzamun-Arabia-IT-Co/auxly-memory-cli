package tui

import "testing"

func TestPermissionLabel(t *testing.T) {
	cases := []struct {
		name         string
		c            clientRow
		defaultWrite bool
		wantLabel    string
		wantWrite    bool
	}{
		{"read-only default", clientRow{Name: "a"}, false, "read-only", false},
		{"legacy write", clientRow{Name: "a", Access: "write"}, false, "read+write", true},
		{"per-file write authoritative", clientRow{Name: "a", WriteFiles: []string{"x.md", "y.md"}}, false, "read+write·2f", true},
		{"default-write opt-in upgrades", clientRow{Name: "a"}, true, "read+write*", true},
		{"per-file beats default-write label", clientRow{Name: "a", WriteFiles: []string{"x.md"}}, true, "read+write·1f", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			label, isWrite := permissionLabel(c.c, c.defaultWrite)
			if label != c.wantLabel || isWrite != c.wantWrite {
				t.Errorf("permissionLabel(%+v, %v) = (%q,%v), want (%q,%v)",
					c.c, c.defaultWrite, label, isWrite, c.wantLabel, c.wantWrite)
			}
		})
	}
}

func TestVersionCell(t *testing.T) {
	cases := []struct {
		name     string
		st       clientVersionStatus
		wantText string
		wantKind string
	}{
		{"outdated shows current→latest", clientVersionStatus{Outdated: true, Version: "1.0.0", Latest: "1.0.9", Reachable: true}, "1.0.0 ⬆1.0.9", "outdated"},
		{"current shows ✓version", clientVersionStatus{Version: "1.0.9", Latest: "1.0.9", Reachable: true}, "✓ 1.0.9", "current"},
		{"unreachable", clientVersionStatus{Reachable: false}, "unreachable", "unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, kind := versionCell(c.st)
			if text != c.wantText || kind != c.wantKind {
				t.Errorf("versionCell(%+v) = (%q,%q), want (%q,%q)", c.st, text, kind, c.wantText, c.wantKind)
			}
		})
	}
}

func TestUpdateResultKind(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"skipped live box (the reported bug)", []string{"   ⏭  OC147 is serving a live session (1.0.0 → 1.0.9) — skipped; retry when idle or use --force"}, "skipped"},
		{"updated", []string{"   ⬆ Updating MM...", "   ✓ MM updated to 1.0.9"}, "updated"},
		{"already current", []string{"   ✓ raqeb already current (1.0.9)"}, "current"},
		{"failed", []string{"   ⚠ MM update failed: ssh: exit status 1"}, "failed"},
		{"unreachable box", []string{"   ⚠ MM unreachable — skipped"}, "failed"},
		{"update-all mixed → updated wins", []string{"   ✓ AiOPSSRV updated to 1.0.9", "   ⏭  OC147 is serving a live session — skipped", "✓ Updated 1 box(es); skipped 5 (current, live, or unreachable)."}, "updated"},
		{"update-all all skipped → not 'failed' from summary text", []string{"   ⏭  OC147 is serving a live session — skipped", "✓ Updated 0 box(es); skipped 6 (current, live, or unreachable)."}, "skipped"},
		{"unrecognised", []string{"some other output"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := updateResultKind(c.lines); got != c.want {
				t.Errorf("updateResultKind = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOutdatedCount(t *testing.T) {
	statuses := []clientVersionStatus{
		{Name: "a", Outdated: true},
		{Name: "b", Outdated: false},
		{Name: "c", Outdated: true},
		{Name: "d", Reachable: false},
	}
	if got := outdatedCount(statuses); got != 2 {
		t.Errorf("outdatedCount = %d, want 2", got)
	}
	if got := outdatedCount(nil); got != 0 {
		t.Errorf("outdatedCount(nil) = %d, want 0", got)
	}
}

func TestVersionsByName(t *testing.T) {
	m := versionsByName([]clientVersionStatus{{Name: "MM", Version: "1.0.8"}})
	if _, ok := m["mm"]; !ok {
		t.Error("versionsByName should key by lowercased name")
	}
}
