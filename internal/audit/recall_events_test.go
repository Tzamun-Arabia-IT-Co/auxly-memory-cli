package audit

import (
	"testing"
	"time"
)

func TestRecallRecordStatsRoundTrip(t *testing.T) {
	l := newTestRecallLogger(t)

	if err := l.RecordRecall(RecallMeta{Provider: "codex", QueryHash: "aaaaaaaaaaaaaaaa", Fallback: false}, []RecallHitRecord{
		{File: "projects.md", LineHash: "bbbbbbbbbbbbbbbb", Score: 0.92, Rank: 0, Accepted: true},
	}); err != nil {
		t.Fatalf("RecordRecall failed: %v", err)
	}

	stats, err := l.RecallStatsByFile()
	if err != nil {
		t.Fatalf("RecallStatsByFile failed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d stats, want 1", len(stats))
	}
	if stats[0].File != "projects.md" {
		t.Fatalf("file = %q, want projects.md", stats[0].File)
	}
	if stats[0].Hits90 != 1 {
		t.Fatalf("Hits90 = %d, want 1", stats[0].Hits90)
	}
	if stats[0].LastHit.IsZero() {
		t.Fatal("LastHit is zero")
	}
}

func TestRecallAcceptedFalseExcludedFromStatsAndHotFacts(t *testing.T) {
	l := newTestRecallLogger(t)

	if err := l.RecordRecall(RecallMeta{Provider: "codex", QueryHash: "aaaaaaaaaaaaaaaa", Fallback: false}, []RecallHitRecord{
		{File: "projects.md", LineHash: "bbbbbbbbbbbbbbbb", Score: 0.92, Rank: 0, Accepted: true},
		{File: "private.md", LineHash: "cccccccccccccccc", Score: 0.88, Rank: 1, Accepted: false},
	}); err != nil {
		t.Fatalf("RecordRecall failed: %v", err)
	}

	stats, err := l.RecallStatsByFile()
	if err != nil {
		t.Fatalf("RecallStatsByFile failed: %v", err)
	}
	if len(stats) != 1 || stats[0].File != "projects.md" {
		t.Fatalf("stats = %#v, want only accepted projects.md row", stats)
	}

	facts, err := l.HotFacts(7, 10)
	if err != nil {
		t.Fatalf("HotFacts failed: %v", err)
	}
	if len(facts) != 1 || facts[0].LineHash != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("facts = %#v, want only accepted line hash", facts)
	}
}

func TestRecallFallbackRateCountsQueryEventsNotRows(t *testing.T) {
	l := newTestRecallLogger(t)

	if err := l.RecordRecall(RecallMeta{Provider: "codex", QueryHash: "fallback00000000", Fallback: true}, []RecallHitRecord{
		{File: "a.md", Rank: 0, Accepted: true},
		{File: "b.md", Rank: 1, Accepted: true},
		{File: "c.md", Rank: 2, Accepted: true},
	}); err != nil {
		t.Fatalf("RecordRecall fallback failed: %v", err)
	}
	if err := l.RecordRecall(RecallMeta{Provider: "codex", QueryHash: "semantic000000", Fallback: false}, []RecallHitRecord{
		{File: "d.md", LineHash: "dddddddddddddddd", Score: 0.75, Rank: 0, Accepted: true},
	}); err != nil {
		t.Fatalf("RecordRecall semantic failed: %v", err)
	}

	fallback, total, err := l.RecallFallbackRate(7)
	if err != nil {
		t.Fatalf("RecallFallbackRate failed: %v", err)
	}
	if fallback != 1 || total != 2 {
		t.Fatalf("fallback/total = %d/%d, want 1/2", fallback, total)
	}
}

func TestRecallPruneRemovesBackdatedRows(t *testing.T) {
	l := newTestRecallLogger(t)
	if err := l.ensureRecallTable(); err != nil {
		t.Fatalf("ensureRecallTable failed: %v", err)
	}

	oldTS := time.Now().UTC().AddDate(0, 0, -(recallRetentionDays + 1)).Format(time.RFC3339)
	_, err := l.db.Exec(`
		INSERT INTO recall_events (ts, provider, query_hash, fallback, file, line_hash, score, rank, accepted)
		VALUES (?, 'codex', 'oldquery0000000', 0, 'old.md', 'oldline00000000', 0.5, 0, 1)
	`, oldTS)
	if err != nil {
		t.Fatalf("insert old row failed: %v", err)
	}

	if err := l.pruneRecallEvents(); err != nil {
		t.Fatalf("pruneRecallEvents failed: %v", err)
	}

	var count int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM recall_events WHERE file = 'old.md'").Scan(&count); err != nil {
		t.Fatalf("count old rows failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("old row count = %d, want 0", count)
	}
}

func TestRecordRecallNilLoggerAndEmptyHitsAreNoOps(t *testing.T) {
	var nilLogger *Logger
	if err := nilLogger.RecordRecall(RecallMeta{Provider: "codex", QueryHash: "aaaaaaaaaaaaaaaa", Fallback: false}, []RecallHitRecord{
		{File: "projects.md", LineHash: "bbbbbbbbbbbbbbbb", Accepted: true},
	}); err != nil {
		t.Fatalf("nil logger RecordRecall failed: %v", err)
	}

	l := newTestRecallLogger(t)
	if err := l.RecordRecall(RecallMeta{Provider: "codex", QueryHash: "aaaaaaaaaaaaaaaa", Fallback: false}, nil); err != nil {
		t.Fatalf("empty hits RecordRecall failed: %v", err)
	}

	stats, err := l.RecallStatsByFile()
	if err != nil {
		t.Fatalf("RecallStatsByFile failed: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("stats length = %d, want 0", len(stats))
	}
}

func TestLastRecallByLineReturnsNewestTimestamp(t *testing.T) {
	l := newTestRecallLogger(t)
	if err := l.ensureRecallTable(); err != nil {
		t.Fatalf("ensureRecallTable failed: %v", err)
	}

	oldTS := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	newTS := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	otherTS := time.Now().UTC().Format(time.RFC3339)
	for _, ts := range []string{oldTS, newTS, otherTS} {
		lineHash := "line111111111111"
		file := "projects.md"
		if ts == otherTS {
			lineHash = "line222222222222"
		}
		_, err := l.db.Exec(`
			INSERT INTO recall_events (ts, provider, query_hash, fallback, file, line_hash, score, rank, accepted)
			VALUES (?, 'codex', 'query11111111111', 0, ?, ?, 0.5, 0, 1)
		`, ts, file, lineHash)
		if err != nil {
			t.Fatalf("insert recall row failed: %v", err)
		}
	}

	recalls, err := l.LastRecallByLine("projects.md")
	if err != nil {
		t.Fatalf("LastRecallByLine failed: %v", err)
	}
	if got := recalls["line111111111111"].Format(time.RFC3339); got != newTS {
		t.Fatalf("line111 newest = %s, want %s", got, newTS)
	}
	if got := recalls["line222222222222"].Format(time.RFC3339); got != otherTS {
		t.Fatalf("line222 newest = %s, want %s", got, otherTS)
	}
}

func newTestRecallLogger(t *testing.T) *Logger {
	t.Helper()

	l, err := NewLogger(t.TempDir())
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})
	return l
}
