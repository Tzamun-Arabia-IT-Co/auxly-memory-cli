package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseCaptureFacts locks the contract parsing: fenced output repaired,
// unknown categories re-routed (never dropped), empty facts dropped, burst cap.
func TestParseCaptureFacts(t *testing.T) {
	raw := "```json\n{\"facts\":[" +
		"{\"category\":\"infra\",\"fact\":\"prod server is 192.168.1.24\"}," +
		"{\"category\":\"nonsense\",\"fact\":\"wife's birthday is in May\"}," +
		"{\"category\":\"preferences\",\"fact\":\"\"}" +
		"]}\n```"
	facts := ParseCaptureFacts(raw)
	if len(facts) != 2 {
		t.Fatalf("want 2 facts, got %+v", facts)
	}
	if facts[0].Category != "infra" {
		t.Fatalf("valid category rewritten: %+v", facts[0])
	}
	// "wife" routes to personal via the keyword router — a private-life fact
	// with a junk category must land in the personal tier, nowhere else.
	if facts[1].Category != "personal" {
		t.Fatalf("private fact not routed to personal: %+v", facts[1])
	}

	if got := ParseCaptureFacts("garbage not json"); got != nil {
		t.Fatalf("garbage should parse to nil, got %+v", got)
	}

	// Burst cap: 15 facts in, captureMaxFacts out.
	big := `{"facts":[`
	for i := 0; i < 15; i++ {
		if i > 0 {
			big += ","
		}
		big += `{"category":"infra","fact":"fact number ` + string(rune('a'+i)) + `"}`
	}
	big += `]}`
	if got := ParseCaptureFacts(big); len(got) != captureMaxFacts {
		t.Fatalf("burst cap: want %d, got %d", captureMaxFacts, len(got))
	}
}

// TestDedupCaptureFacts locks the vault dedup: an already-known fact (fuzzy
// equivalent) never re-queues; new facts pass.
func TestDedupCaptureFacts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "infra.md"),
		[]byte("# Infra\n- [2026-06-01] prod server is 192.168.1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Root: root}
	facts := s.DedupCaptureFacts([]CaptureFact{
		{Category: "infra", Fact: "prod server is 192.168.1.24"}, // known (fuzzy: date prefix differs)
		{Category: "infra", Fact: "backup NAS lives at 192.168.1.50"},
	})
	if len(facts) != 1 || facts[0].Fact != "backup NAS lives at 192.168.1.50" {
		t.Fatalf("dedup wrong: %+v", facts)
	}
}
