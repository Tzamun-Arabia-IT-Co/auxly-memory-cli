package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
)

func TestFactDateVariants(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"stamp", "- Alpha [2026-07-02]", "2026-07-02"},
		{"updated trace", "- Alpha (updated 2026-06-01 from beta)", "2026-06-01"},
		{"both picks newest", "- Alpha [2025-01-01] (updated 2026-07-02 from beta)", "2026-07-02"},
		{"none", "- Alpha", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := factDate(tc.line)
			if tc.want == "" {
				if !got.IsZero() {
					t.Fatalf("factDate(%q)=%v, want zero", tc.line, got)
				}
				return
			}
			want, err := time.Parse("2006-01-02", tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) {
				t.Fatalf("factDate(%q)=%v, want %v", tc.line, got, want)
			}
		})
	}
}

// TestStaleFactsUndatedFactIgnoresFileMtime is Finding 5's regression: mtime
// is reset by every AtomicWriteFile rewrite (organize, restamp, ...), so an
// old mtime on an actively-maintained vault must NOT drive staleness anymore.
// A never-before-seen undated fact bootstraps into the first-seen ledger at
// "now" and is not flagged yet, regardless of how stale the file's mtime is.
func TestStaleFactsUndatedFactIgnoresFileMtime(t *testing.T) {
	store := newDecayTestStore(t)
	oldLine := "- Old undated fact"
	if err := store.Write("identity.md", "# Identity\n"+oldLine+"\n"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -120)
	if err := os.Chtimes(filepath.Join(store.Root, "identity.md"), old, old); err != nil {
		t.Fatal(err)
	}

	got, err := store.StaleFacts(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("first-seen undated fact must bootstrap via the ledger, not the stale file mtime; got %#v", got)
	}
}

// TestStaleFactsUndatedFactBootstrapsLedgerOnFirstScan locks the
// self-bootstrapping half of Finding 5: scanning a vault with an undated fact
// creates .index/review-seen.json recording that fact's hash as first-seen
// "now" — the vehicle that lets it become eligible ageDays later.
func TestStaleFactsUndatedFactBootstrapsLedgerOnFirstScan(t *testing.T) {
	store := newDecayTestStore(t)
	line := "- Undated ledger fact"
	if err := store.Write("identity.md", "# Identity\n"+line+"\n"); err != nil {
		t.Fatal(err)
	}

	got, err := store.StaleFacts(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("first scan should bootstrap (not flag), got %#v", got)
	}

	data, err := os.ReadFile(filepath.Join(store.Root, ".index", "review-seen.json"))
	if err != nil {
		t.Fatalf("ledger file should be created by the scan: %v", err)
	}
	var seen map[string]string
	if err := json.Unmarshal(data, &seen); err != nil {
		t.Fatal(err)
	}
	ts, ok := seen[HashRecallText(line)]
	if !ok {
		t.Fatalf("ledger missing first-seen entry for %q: %v", line, seen)
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("first-seen timestamp not RFC3339: %q", ts)
	}
}

// TestStaleFactsUndatedFactFlaggedWhenLedgerBackdated is the other half of
// Finding 5: once the ledger has recorded a fact's first-seen date far enough
// in the past, it becomes eligible for review — same as a dated fact would.
func TestStaleFactsUndatedFactFlaggedWhenLedgerBackdated(t *testing.T) {
	store := newDecayTestStore(t)
	line := "- Undated ledger fact"
	if err := store.Write("identity.md", "# Identity\n"+line+"\n"); err != nil {
		t.Fatal(err)
	}

	ledgerDir := filepath.Join(store.Root, ".index")
	if err := os.MkdirAll(ledgerDir, 0755); err != nil {
		t.Fatal(err)
	}
	backdated := time.Now().AddDate(0, 0, -100).Format(time.RFC3339)
	seen := map[string]string{HashRecallText(line): backdated}
	data, err := json.Marshal(seen)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerDir, "review-seen.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := store.StaleFacts(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Line != line {
		t.Fatalf("backdated ledger entry should flag the fact, got %#v", got)
	}
}

func TestStaleFactsSkipsRecentlyRecalled(t *testing.T) {
	store := newDecayTestStore(t)
	line := "- Old recalled fact [2020-01-01]"
	if err := store.Write("identity.md", "# Identity\n"+line+"\n"); err != nil {
		t.Fatal(err)
	}

	got, err := store.StaleFacts(func(file string) (map[string]time.Time, error) {
		return map[string]time.Time{
			HashRecallText(strings.TrimSpace(line)): time.Now(),
		}, nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("recent recall should suppress stale fact, got %#v", got)
	}
}

func TestStaleFactsSkipsPersonalUnlessIncluded(t *testing.T) {
	store := newDecayTestStore(t)
	line := "- Private old fact [2020-01-01]"
	if err := store.Write("personal.md", "# Personal\n"+line+"\n"); err != nil {
		t.Fatal(err)
	}

	got, err := store.StaleFacts(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("personal.md should be skipped by default, got %#v", got)
	}

	got, err = store.StaleFacts(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File != "personal.md" {
		t.Fatalf("includePersonal should include personal.md, got %#v", got)
	}
}

func TestStaleFactsReviewAgeZeroDisables(t *testing.T) {
	t.Setenv("AUXLY_REVIEW_AGE_DAYS", "0")
	store := newDecayTestStore(t)
	if err := store.Write("identity.md", "# Identity\n- Old fact [2020-01-01]\n"); err != nil {
		t.Fatal(err)
	}

	got, err := store.StaleFacts(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("review age 0 should return nil slice, got %#v", got)
	}
}

func TestArchiveFactMovesExactLineAndAppends(t *testing.T) {
	store := newDecayTestStore(t)
	first := "- First old fact"
	second := "- Second old fact"
	if err := store.Write("identity.md", "# Identity\n"+first+"\n"+second+"\n"); err != nil {
		t.Fatal(err)
	}

	if err := store.ArchiveFact("identity.md", first); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(store.Root, "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), first) || !strings.Contains(string(source), second) {
		t.Fatalf("source after first archive = %q", source)
	}

	if err := store.ArchiveFact("identity.md", second); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(filepath.Join(store.Root, ".archive", "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(archive) != first+"\n"+second+"\n" {
		t.Fatalf("archive content = %q", archive)
	}
}

func TestRestampFactUpdatesStampAndAppendsWhenUndated(t *testing.T) {
	store := newDecayTestStore(t)
	stamped := "- Stamped fact [2020-01-01]"
	undated := "- Undated fact"
	if err := store.Write("identity.md", "# Identity\n"+stamped+"\n"+undated+"\n"); err != nil {
		t.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	if err := store.RestampFact("identity.md", stamped); err != nil {
		t.Fatal(err)
	}
	if err := store.RestampFact("identity.md", undated); err != nil {
		t.Fatal(err)
	}

	content, err := store.View("identity.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "- Stamped fact ["+today+"]") {
		t.Fatalf("stamped fact was not updated to today in %q", content)
	}
	if !strings.Contains(content, "- Undated fact ["+today+"]") {
		t.Fatalf("undated fact did not get appended stamp in %q", content)
	}
}

// TestArchiveFactUsesWorkspaceCopyWhenShadowed is Finding 1's regression:
// StaleFacts finds facts via Store.View, which prefers a workspace override
// over the global copy. ArchiveFact/RestampFact must mutate that SAME copy —
// otherwise a workspace-scoped fact either "fact not found"s or silently
// edits the shadowed global file while claiming success.
func TestArchiveFactUsesWorkspaceCopyWhenShadowed(t *testing.T) {
	globalRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	store := NewStore(globalRoot)
	store.WorkspaceRoot = workspaceRoot

	globalContent := "# Identity\n- Global driver: alpha\n"
	workspaceContent := "# Identity\n- Workspace driver: alpha\n"
	if err := os.WriteFile(filepath.Join(globalRoot, "identity.md"), []byte(globalContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "identity.md"), []byte(workspaceContent), 0644); err != nil {
		t.Fatal(err)
	}

	line := "- Workspace driver: alpha"
	if err := store.ArchiveFact("identity.md", line); err != nil {
		t.Fatal(err)
	}

	wsAfter, err := os.ReadFile(filepath.Join(workspaceRoot, "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wsAfter), line) {
		t.Fatalf("workspace copy should have lost the archived line: %q", wsAfter)
	}

	archived, err := os.ReadFile(filepath.Join(workspaceRoot, ".archive", "identity.md"))
	if err != nil {
		t.Fatalf("workspace .archive should gain the line: %v", err)
	}
	if !strings.Contains(string(archived), line) {
		t.Fatalf("workspace archive missing line: %q", archived)
	}

	globalAfter, err := os.ReadFile(filepath.Join(globalRoot, "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(globalAfter) != globalContent {
		t.Fatalf("global copy must stay untouched, got %q", globalAfter)
	}
	if _, err := os.Stat(filepath.Join(globalRoot, ".archive", "identity.md")); !os.IsNotExist(err) {
		t.Fatalf("global .archive should not exist")
	}
}

// TestIndentedBulletRoundTrip is Finding 2's regression: StaleFacts stores
// Line as strings.TrimSpace(raw), so a nested bullet ("  - Driver: ...") must
// still match during restamp/archive, and the ORIGINAL indentation must
// survive both the in-place restamp and the archived copy.
func TestIndentedBulletRoundTrip(t *testing.T) {
	store := newDecayTestStore(t)
	indented := "  - Driver: nested detail [2020-01-01]"
	if err := store.Write("identity.md", "# Identity\n"+indented+"\n"); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(indented) // what StaleFacts hands back as StaleFact.Line

	today := time.Now().Format("2006-01-02")
	if err := store.RestampFact("identity.md", line); err != nil {
		t.Fatal(err)
	}
	content, err := store.View("identity.md")
	if err != nil {
		t.Fatal(err)
	}
	wantRestamped := "  - Driver: nested detail [" + today + "]"
	if !strings.Contains(content, wantRestamped) {
		t.Fatalf("indented bullet not restamped in place (indentation lost?): %q", content)
	}

	restampedLine := strings.TrimSpace(wantRestamped)
	if err := store.ArchiveFact("identity.md", restampedLine); err != nil {
		t.Fatal(err)
	}
	after, err := store.View("identity.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, "Driver: nested detail") {
		t.Fatalf("indented bullet should have been archived: %q", after)
	}
	archived, err := os.ReadFile(filepath.Join(store.Root, ".archive", "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(archived) != wantRestamped+"\n" {
		t.Fatalf("archived line should preserve original indentation: %q", archived)
	}
}

// TestRestampLineReplacesOnlyLastBracketedDate is Finding 4's regression:
// restampLine must not clobber every [YYYY-MM-DD] token — only the last one
// is the freshness stamp; earlier ones are content (e.g. an incident date).
func TestRestampLineReplacesOnlyLastBracketedDate(t *testing.T) {
	line := "- Incident ref [2021-03-04] resolved [2022-05-06]"
	today := time.Now().Format("2006-01-02")
	got := restampLine(line, today)
	want := "- Incident ref [2021-03-04] resolved [" + today + "]"
	if got != want {
		t.Fatalf("restampLine = %q, want %q", got, want)
	}
}

func TestArchiveFactAndRestampFactMissingLine(t *testing.T) {
	store := newDecayTestStore(t)
	if err := store.Write("identity.md", "# Identity\n- Existing\n"); err != nil {
		t.Fatal(err)
	}

	for name, err := range map[string]error{
		"archive": store.ArchiveFact("identity.md", "- Missing"),
		"restamp": store.RestampFact("identity.md", "- Missing"),
	} {
		if !errors.Is(err, errors.New("fact not found (changed since review?)")) && (err == nil || !strings.Contains(err.Error(), "fact not found")) {
			t.Fatalf("%s missing line error = %v", name, err)
		}
	}
}

func newDecayTestStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	store.WorkspaceRoot = ""
	return store
}

// CRITICAL 5 regression: archive encryption must be STICKY. If .archive/<file>
// is already encrypted (e.g. from a prior pass, back when the source was
// still encrypted) but the SOURCE is now plaintext — `auxly decrypt file
// <name>` never touches .archive/<name> — the next ArchiveFact append must
// keep the archive encrypted, never silently plaintext-ify it just because
// the source currently isn't.
func TestArchiveFact_StickyArchiveEncryption(t *testing.T) {
	store := newDecayTestStore(t)
	identity := testVaultIdentity(t)

	archiveDir := filepath.Join(store.Root, ".archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedCiphertext(t, &Store{Root: archiveDir}, "identity.md", identity, "- older archived fact\n")

	// SOURCE is plaintext now (simulating a decrypt after the archive above
	// was written).
	line := "- fact to archive now"
	if err := store.Write("identity.md", "# Identity\n"+line+"\n"); err != nil {
		t.Fatal(err)
	}

	if err := store.ArchiveFact("identity.md", line); err != nil {
		t.Fatalf("ArchiveFact: %v", err)
	}

	archiveRaw, err := os.ReadFile(filepath.Join(archiveDir, "identity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(archiveRaw) {
		t.Fatal("archive lost its encryption after appending from a now-plaintext source — must stay encrypted (sticky)")
	}

	plain, _, err := store.readVaultFile(filepath.Join(archiveDir, "identity.md"))
	if err != nil {
		t.Fatalf("decrypt archive: %v", err)
	}
	if !strings.Contains(string(plain), "older archived fact") || !strings.Contains(string(plain), line) {
		t.Fatalf("archive content = %q, want both the older and new archived facts", plain)
	}
}
