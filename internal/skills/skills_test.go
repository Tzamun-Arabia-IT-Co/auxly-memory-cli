package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// contentFingerprint pins the canonical skill content (name + body + Desktop flag
// of every skill). The guard below fails whenever that content changes — which is
// exactly when a download SHOULD be re-triggered. When it fails because you
// deliberately edited a skill:
//
//  1. bump skills.Version by significance (it gates the export folder + re-download)
//  2. set contentFingerprint to the hash printed by the failure
//
// in the SAME commit. This makes a stale export OR a forgotten version bump
// impossible — the build won't go green until both are done.
const contentFingerprint = "d603f7d4f03a5f1e1a9ae161fe91003bec2fb86f5d11d8b40bbea870b535e04f"

func TestSkillContentFingerprint(t *testing.T) {
	h := sha256.New()
	for _, s := range All() {
		fmt.Fprintf(h, "%s\x00%s\x00%t\x00", s.Name, s.Body, s.Desktop)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != contentFingerprint {
		t.Fatalf("skill content changed.\n  → bump skills.Version (now %q) by significance\n  → set contentFingerprint = %q\nin the same commit.", Version, got)
	}
}

// TestRegistryShape locks the registry membership so a skill can't be dropped or
// silently un-exported without a test change.
func TestRegistryShape(t *testing.T) {
	all := All()
	if len(all) != 10 {
		t.Fatalf("expected 10 skills, got %d", len(all))
	}
	var desktop int
	seen := map[string]bool{}
	for _, s := range all {
		if seen[s.Name] {
			t.Errorf("duplicate skill %q", s.Name)
		}
		seen[s.Name] = true
		if s.Body == "" {
			t.Errorf("skill %q has empty body", s.Name)
		}
		if s.Desktop {
			desktop++
		}
	}
	if desktop != 9 {
		t.Errorf("expected 9 Desktop-exported skills (all but remote-connect), got %d", desktop)
	}
	if seen["auxly-remote-connect"] && DesktopContains("auxly-remote-connect") {
		t.Error("auxly-remote-connect must NOT be in the Claude Desktop export")
	}
}

// DesktopContains is a test helper.
func DesktopContains(name string) bool {
	for _, s := range DesktopSkills() {
		if s.Name == name {
			return true
		}
	}
	return false
}
