package update

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"1.2.0", "1.1.0", true},
		{"1.10.0", "1.9.0", true}, // numeric, not lexical
		{"1.0.1", "1.0.0", true},
		{"2.0.0", "1.9.9", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.1.0", false},
		{"1.0.0", "2.0.0", false},
		{"v1.2.0", "1.1.0", true},    // tolerate a leading v
		{"1.2.0-rc1", "1.1.0", true}, // tolerate a suffix
		{"", "1.0.0", false},
		{"<!doctype html>", "1.0.0", false}, // HTML/garbage is never "newer"
		{"not-a-version", "1.0.0", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.latest, c.current); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}
