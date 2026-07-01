package pending

import (
	"strings"
	"testing"
)

func TestLinesEquivalent(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// tier 1: normalization
		{"- John's married.", "- john is married", false}, // different words ("johns" vs "john is") but short — stays distinct
		{"- Prefers Go, for backends!", "-   prefers go for backends", true},
		{"- Server IP is 192.168.1.24", "- server ip is 192.168.1.24.", true},
		{"- Uses VS Code", "- uses vs code", true},
		// tier 2: trigram near-duplicates (≥20 normalized chars)
		{"- The production server IP address is 192.168.1.24", "- The production server IP address is: 192.168.1.24!", true},
		{"- Wael prefers tabs over spaces in all Go projects", "- Wael prefers tabs over spaces in all Go projects always", true},
		// distinct facts must never merge
		{"- Server IP is 192.168.1.24", "- Server IP is 192.168.1.25", false},
		{"- prefers Go", "- prefers Rust", false},
		{"- The staging environment runs on Ubuntu 22.04 LTS", "- The production environment runs on Windows Server 2022", false},
		// punctuation between digits is a boundary, not noise — these are DIFFERENT facts
		{"- Requires Node v1.2", "- Requires Node v12", false},
		{"- Meeting scheduled 12.1.2024", "- Meeting scheduled 1.21.2024", false},
		{"- staging config uses version v2.1 for the deploy", "- staging config uses version v21 for the deploy", false},
		// Arabic-Indic digits count as digits (٥٠٠٠=5000, ٦٠٠٠=6000)
		{"- قيمة العقد الشهري للمشروع ٥٠٠٠ ريال", "- قيمة العقد الشهري للمشروع ٦٠٠٠ ريال", false},
		{"- قيمة العقد الشهري للمشروع ٥٠٠٠ ريال", "- قيمة العقد الشهري للمشروع 5000 ريال", true},
	}
	for _, c := range cases {
		if got := linesEquivalent(c.a, c.b); got != c.want {
			t.Errorf("linesEquivalent(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestApplyDiffFuzzyDedup(t *testing.T) {
	existing := "- The production server IP address is 192.168.1.24\n- prefers Go\n"
	// Same fact, punctuation + trailing word drift → must NOT be added twice.
	got := ApplyDiff(existing, "+ - The production server IP address is: 192.168.1.24.\n")
	if strings.Count(got, "192.168.1.24") != 1 {
		t.Fatalf("near-duplicate added twice:\n%s", got)
	}
	// Genuinely different fact → must be added.
	got = ApplyDiff(existing, "+ - The staging server IP address is 192.168.1.30\n")
	if !strings.Contains(got, "192.168.1.30") {
		t.Fatalf("distinct fact wrongly deduped:\n%s", got)
	}
}
