package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// fakeAgentScript writes an executable shell script that ignores its arguments
// and prints the given body — the stand-in for a headless CLI agent.
func fakeAgentScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExtractCaptureFactsWithAgent locks the CLI-agent backend: a well-formed
// reply yields facts, a reply with no JSON object is an error (not "no
// facts"), and a failing agent is an error naming the agent and its stderr.
func TestExtractCaptureFactsWithAgent(t *testing.T) {
	dir := t.TempDir()

	ok := fakeAgentScript(t, dir, "ok.sh",
		`echo '{"facts":[{"category":"preferences","fact":"x"}]}'`)
	facts, err := ExtractCaptureFactsWithAgent(context.Background(), "claude", ok, "", "user: I prefer Go")
	if err != nil {
		t.Fatalf("agent extraction failed: %v", err)
	}
	if len(facts) != 1 || facts[0].Fact != "x" || facts[0].Category != "preferences" {
		t.Fatalf("want 1 preference fact 'x', got %+v", facts)
	}

	// Prose with no JSON object is a broken extraction — it must surface as an
	// error so it reaches the capture breadcrumb, not as a silent "0 facts".
	prose := fakeAgentScript(t, dir, "prose.sh", `echo 'I could not find any durable facts.'`)
	if _, err := ExtractCaptureFactsWithAgent(context.Background(), "claude", prose, "", "user: hi"); err == nil {
		t.Fatal("a reply with no JSON object must be an error")
	}

	// A non-zero exit is an error too — naming the agent and quoting stderr.
	fail := fakeAgentScript(t, dir, "fail.sh", "echo 'boom' >&2\nexit 1")
	_, err = ExtractCaptureFactsWithAgent(context.Background(), "claude", fail, "", "user: hi")
	if err == nil {
		t.Fatal("a failing agent must be an error")
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should name the agent and quote stderr: %v", err)
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
