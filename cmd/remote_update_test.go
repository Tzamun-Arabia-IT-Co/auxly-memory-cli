package cmd

import (
	"strings"
	"testing"
)

// TestStatuslineSyncMessage locks the HONEST per-box outcome wording so a sync never
// claims a usage line the box can't show, and reports the right one for each state.
func TestStatuslineSyncMessage(t *testing.T) {
	cases := []struct {
		name      string
		res       remoteStatuslineResult
		wantUsage bool
		contains  string
		absent    string
	}{
		{
			name:      "1.0.10+ box persisted Live Usage",
			res:       remoteStatuslineResult{persisted: true, refreshed: true},
			wantUsage: true,
			contains:  "Live Usage on the box",
		},
		{
			name:      "older box, usage primed via refresh",
			res:       remoteStatuslineResult{refreshed: true},
			wantUsage: true,
			contains:  "usage refreshed now",
		},
		{
			name:      "host wants usage but box could not prime it",
			res:       remoteStatuslineResult{},
			wantUsage: true,
			contains:  "couldn't be primed",
		},
		{
			name:      "host has Live Usage off — mode only, no usage claim",
			res:       remoteStatuslineResult{},
			wantUsage: false,
			contains:  "mirrors your mode",
			absent:    "usage",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := statuslineSyncMessage("BoxA", c.res, c.wantUsage)
			if !strings.Contains(got, "BoxA") {
				t.Errorf("message should name the box; got %q", got)
			}
			if !strings.Contains(got, c.contains) {
				t.Errorf("message %q should contain %q", got, c.contains)
			}
			if c.absent != "" && strings.Contains(got, c.absent) {
				t.Errorf("message %q should NOT mention %q", got, c.absent)
			}
		})
	}
}

func TestParseRemoteVersion(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "standard --version banner",
			out:  "\n🧠 Auxly-Memory CLI Version: 1.0.8\n   ↳ Platform: stdio-native\n   ↳ Revision: release-v1.0.8\n",
			want: "1.0.8",
		},
		{
			name: "bare version line",
			out:  "auxly version 1.2.10",
			want: "1.2.10",
		},
		{
			name: "two-component version",
			out:  "Version: 2.0",
			want: "2.0",
		},
		{
			name: "no version present",
			out:  "command not found: auxly",
			want: "",
		},
		{
			name: "empty output",
			out:  "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseRemoteVersion(c.out); got != c.want {
				t.Errorf("parseRemoteVersion(%q) = %q, want %q", c.out, got, c.want)
			}
		})
	}
}

// TestRemoteNeedsUpdate covers the decision gate: only update when opted-in AND a
// strictly-newer release exists AND the remote version is known.
func TestRemoteNeedsUpdate(t *testing.T) {
	cases := []struct {
		name      string
		remoteVer string
		latest    string
		optIn     bool
		want      bool
	}{
		{"opted-in and behind", "1.0.8", "1.0.9", true, true},
		{"opted-in but current", "1.0.9", "1.0.9", true, false},
		{"opted-in but ahead", "1.1.0", "1.0.9", true, false},
		{"behind but NOT opted-in", "1.0.8", "1.0.9", false, false},
		{"unknown remote version", "", "1.0.9", true, false},
		{"unknown latest", "1.0.8", "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := remoteNeedsUpdate(c.remoteVer, c.latest, c.optIn); got != c.want {
				t.Errorf("remoteNeedsUpdate(%q,%q,%v) = %v, want %v",
					c.remoteVer, c.latest, c.optIn, got, c.want)
			}
		})
	}
}
