package update

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

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

// TestCachedIsNetworkFree verifies Cached() reads only the on-disk cache and reports a
// newer version without any network call.
func TestCached(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No cache file → no update.
	if latest, newer := Cached(); newer || latest != "" {
		t.Errorf("missing cache should yield (\"\", false), got (%q, %v)", latest, newer)
	}

	auxlyDir := home + "/.auxly"
	if err := os.MkdirAll(auxlyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(latest string) {
		// CheckedAt is intentionally old to prove Cached() ignores freshness.
		c := cacheFile{CheckedAt: time.Now().Add(-72 * time.Hour), Latest: latest}
		b, _ := json.Marshal(c)
		if err := os.WriteFile(auxlyDir+"/.update-check.json", b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A strictly newer cached version (vs the running build) → reported even when stale.
	write("999.0.0")
	if latest, newer := Cached(); !newer || latest != "999.0.0" {
		t.Errorf("newer cached version should be reported, got (%q, %v)", latest, newer)
	}

	// An older/equal cached version → no update.
	write(Current)
	if _, newer := Cached(); newer {
		t.Errorf("equal version should not report an update")
	}
}
