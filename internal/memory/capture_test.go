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
		"{\"category\":\"infra\",\"fact\":\"prod server is 192.0.2.24\"}," +
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
		[]byte("# Infra\n- [2026-06-01] prod server is 192.0.2.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Root: root}
	facts := s.DedupCaptureFacts([]CaptureFact{
		{Category: "infra", Fact: "prod server is 192.0.2.24"}, // known (fuzzy: date prefix differs)
		{Category: "infra", Fact: "backup NAS lives at 192.0.2.50"},
	})
	if len(facts) != 1 || facts[0].Fact != "backup NAS lives at 192.0.2.50" {
		t.Fatalf("dedup wrong: %+v", facts)
	}
}

func TestLooksLikeSecret(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "openai/anthropic sk- key",
			in:   "uses API key sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz12",
			want: true,
		},
		{
			name: "github classic token",
			in:   "deploy with ghp_1234567890abcdefghijklmnopqrstuvwx",
			want: true,
		},
		{
			name: "github fine-grained pat",
			in:   "token github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
			want: true,
		},
		{
			name: "aws access key id AKIA",
			in:   "aws key AKIAIOSFODNN7EXAMPLE in staging",
			want: true,
		},
		{
			name: "aws sts key id ASIA",
			in:   "temp key ASIAIOSFODNN7EXAMPLE",
			want: true,
		},
		{
			name: "google AIza key",
			in:   "maps key AIza" + strings.Repeat("x", 35), // built at runtime so secret scanners skip the fixture
			want: true,
		},
		{
			name: "slack xox token",
			in:   "bot uses xox" + "b-1234567890-abcdefghij", // split so secret scanners skip the fixture
			want: true,
		},
		{
			name: "pem private key header",
			in:   "key starts with -----BEGIN OPENSSH PRIVATE KEY-----",
			want: true,
		},
		{
			name: "jwt bearer token",
			in:   "auth header eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			want: true,
		},
		{
			name: "generic password assignment",
			in:   `staging db password = "s3cr3tP@ssw0rdValue"`,
			want: true,
		},
		{
			name: "generic api_key colon assignment",
			in:   "service api_key: supersecretvalue123",
			want: true,
		},
		{
			name: "prose mentioning redacted sk-kimi prefix",
			in:   "Kimi Code subscription key (sk-kimi-...) only works at https://api.kimi.com/coding/v1",
			want: false,
		},
		{
			name: "prose mentioning API key file setting",
			in:   "leave 'API key file' empty",
			want: false,
		},
		{
			name: "prose mentioning token budget",
			in:   "token budget is 8000",
			want: false,
		},
		{
			name: "normal infra fact",
			in:   "prod server is 192.0.2.24 on port 2222",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeSecret(tc.in); got != tc.want {
				t.Fatalf("LooksLikeSecret(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCaptureNeighbors(t *testing.T) {
	// Ranking + threshold + deterministic tie-breaking + top-k cap.
	known := []string{
		"- [2026-10-01] completely unrelated editor colorscheme preference",
		"- [2026-10-02] staging postgres runs on port 5432",
		"- [2026-10-03] production postgres server runs on port 5432",
		"- [2026-10-04] backup postgres runs on port 5432",
	}
	got := CaptureNeighbors("production postgres database runs on port 5432", known, 2, CaptureNeighborMinScore)
	if len(got) != 2 {
		t.Fatalf("want top 2 neighbors, got %v", got)
	}
	if got[0] != known[2] {
		t.Fatalf("best neighbor should be known[2], got %q", got[0])
	}
	// known[1] and known[3] tie on Jaccard score — original order wins (known[1] before known[3]).
	if got[1] != known[1] {
		t.Fatalf("tie-break should preserve original order (known[1]), got %q", got[1])
	}

	// Stamp stripping: shared "[2026-10-29]" stamps on unrelated facts must not create overlap.
	stampOnly := []string{"- [2026-10-29] uses dark mode in neovim"}
	if nb := CaptureNeighbors("[2026-10-29] deploys containers tohetzner cloud", stampOnly, CaptureNeighborK, CaptureNeighborMinScore); len(nb) != 0 {
		t.Fatalf("shared date stamp must be stripped before scoring, got %v", nb)
	}

	// High threshold filters moderate overlap out.
	if nb := CaptureNeighbors("production postgres database runs on port 5432", known, 5, 0.95); len(nb) != 0 {
		t.Fatalf("expected 0 neighbors above 0.95 threshold, got %v", nb)
	}

	// Unicode / Arabic tokens survive tokenization and match reworded Arabic lines.
	arabicKnown := []string{
		"- [2026-10-01] يفضل المستخدم الوضع الليلي في المحرر",
		"- [2026-10-02] الخادم الرئيسي يعمل حاليا على منفذ 8080",
	}
	arabicGot := CaptureNeighbors("الخادم الرئيسي يعمل على منفذ 8080 في الإنتاج", arabicKnown, CaptureNeighborK, CaptureNeighborMinScore)
	if len(arabicGot) != 1 || arabicGot[0] != arabicKnown[1] {
		t.Fatalf("expected Arabic neighbor %q, got %v", arabicKnown[1], arabicGot)
	}
}

func TestParseKnownIndices(t *testing.T) {
	got, err := ParseKnownIndices(`{"known":[1,3]}`, 3)
	if err != nil {
		t.Fatalf("valid JSON failed: %v", err)
	}
	if len(got) != 2 || !got[0] || !got[2] {
		t.Fatalf("want indices {0,2}, got %v", got)
	}

	fenced, err := ParseKnownIndices("Here is the result:\n```json\n{\"known\": [2]}\n```\n", 3)
	if err != nil {
		t.Fatalf("fenced JSON failed: %v", err)
	}
	if len(fenced) != 1 || !fenced[1] {
		t.Fatalf("want index {1}, got %v", fenced)
	}

	outOfRange, err := ParseKnownIndices(`{"known":[-1, 0, 2, 4, 99]}`, 3)
	if err != nil {
		t.Fatalf("out-of-range JSON failed: %v", err)
	}
	if len(outOfRange) != 1 || !outOfRange[1] {
		t.Fatalf("out-of-range numbers must be ignored, got %v", outOfRange)
	}

	if _, err := ParseKnownIndices("no duplicates found here", 3); err == nil {
		t.Fatal("expected error when reply has no JSON object")
	}
	if _, err := ParseKnownIndices("[1, 2]", 3); err == nil {
		t.Fatal("expected error when reply is a JSON array rather than an object")
	}
}

func TestCaptureJudgePrompt(t *testing.T) {
	cands := []CaptureFact{
		{Category: "infra", Fact: "prod DB listens on 5432"},
		{Category: "preferences", Fact: "prefers gofmt over custom linters"},
	}
	neighbors := [][]string{
		{"- [2026-10-01] production database runs on port 5432", "- [2026-10-02] staging DB is 5433"},
		{"- [2026-09-15] always format Go code with gofmt"},
	}
	prompt := CaptureJudgePrompt(cands, neighbors)
	for _, want := range []string{
		"1.",
		"prod DB listens on 5432",
		"production database runs on port 5432",
		"staging DB is 5433",
		"2.",
		"prefers gofmt over custom linters",
		"always format Go code with gofmt",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("CaptureJudgePrompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestJudgeCaptureDuplicatesWithAgent(t *testing.T) {
	dir := t.TempDir()
	ok := fakeAgentScript(t, dir, "judge-ok.sh", `echo '{"known":[2]}'`)
	cands := []CaptureFact{
		{Category: "infra", Fact: "new server"},
		{Category: "infra", Fact: "known server"},
	}
	neighbors := [][]string{
		{"- existing one"},
		{"- existing two"},
	}
	known, err := JudgeCaptureDuplicatesWithAgent(context.Background(), "claude", ok, "", cands, neighbors)
	if err != nil {
		t.Fatalf("JudgeCaptureDuplicatesWithAgent failed: %v", err)
	}
	if len(known) != 1 || !known[1] {
		t.Fatalf("want known={1:true}, got %v", known)
	}

	noJSON := fakeAgentScript(t, dir, "judge-prose.sh", `echo 'Nothing looks duplicated.'`)
	if _, err := JudgeCaptureDuplicatesWithAgent(context.Background(), "claude", noJSON, "", cands, neighbors); err == nil {
		t.Fatal("expected error when agent judge emits no JSON object")
	}
}
