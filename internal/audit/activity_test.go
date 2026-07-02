package audit

import (
	"testing"
	"time"
)

func TestEventsSinceCursorSemantics(t *testing.T) {
	l := newTestActivityLogger(t)

	for _, file := range []string{"one.md", "two.md", "three.md"} {
		if _, err := l.Log("agent", "codex", "write", file, "", "", "trusted"); err != nil {
			t.Fatalf("Log failed: %v", err)
		}
	}

	first, err := l.EventsSince(0, 2)
	if err != nil {
		t.Fatalf("EventsSince first batch failed: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first batch length = %d, want 2", len(first))
	}

	second, err := l.EventsSince(first[len(first)-1].ID, 2)
	if err != nil {
		t.Fatalf("EventsSince second batch failed: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second batch length = %d, want 1", len(second))
	}
	if first[0].ID >= first[1].ID || first[1].ID >= second[0].ID {
		t.Fatalf("ids are not strictly ascending across batches: %#v %#v", first, second)
	}
	if first[0].File != "one.md" || first[1].File != "two.md" || second[0].File != "three.md" {
		t.Fatalf("unexpected files across batches: %#v %#v", first, second)
	}
}

func TestLatestEventIDEmptyAndSeeded(t *testing.T) {
	l := newTestActivityLogger(t)

	id, err := l.LatestEventID()
	if err != nil {
		t.Fatalf("LatestEventID empty failed: %v", err)
	}
	if id != 0 {
		t.Fatalf("empty latest id = %d, want 0", id)
	}

	if _, err := l.Log("agent", "codex", "write", "one.md", "", "", "trusted"); err != nil {
		t.Fatalf("first Log failed: %v", err)
	}
	firstID, err := l.LatestEventID()
	if err != nil {
		t.Fatalf("LatestEventID after first row failed: %v", err)
	}
	if _, err := l.Log("agent", "codex", "approve", "two.md", "", "", "trusted"); err != nil {
		t.Fatalf("second Log failed: %v", err)
	}
	secondID, err := l.LatestEventID()
	if err != nil {
		t.Fatalf("LatestEventID after second row failed: %v", err)
	}
	if secondID <= firstID {
		t.Fatalf("latest id did not advance: first=%d second=%d", firstID, secondID)
	}

	var maxID int64
	if err := l.db.QueryRow("SELECT MAX(id) FROM audit_entries").Scan(&maxID); err != nil {
		t.Fatalf("query max id failed: %v", err)
	}
	if secondID != maxID {
		t.Fatalf("latest id = %d, want max id %d", secondID, maxID)
	}
}

func TestEventsSinceMalformedTimestampTolerance(t *testing.T) {
	l := newTestActivityLogger(t)

	if _, err := l.Log("agent", "codex", "write", "bad.md", "", "", "trusted"); err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	id, err := l.LatestEventID()
	if err != nil {
		t.Fatalf("LatestEventID failed: %v", err)
	}
	if _, err := l.db.Exec("UPDATE audit_entries SET timestamp = 'not-rfc3339' WHERE id = ?", id); err != nil {
		t.Fatalf("corrupt timestamp failed: %v", err)
	}

	events, err := l.EventsSince(0, 10)
	if err != nil {
		t.Fatalf("EventsSince failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	if !events[0].TS.IsZero() {
		t.Fatalf("timestamp = %v, want zero time", events[0].TS)
	}
}

func TestRecordVaultSizeSameDayReplace(t *testing.T) {
	l := newTestActivityLogger(t)

	if err := l.RecordVaultSize(123); err != nil {
		t.Fatalf("first RecordVaultSize failed: %v", err)
	}
	if err := l.RecordVaultSize(456); err != nil {
		t.Fatalf("second RecordVaultSize failed: %v", err)
	}

	points, err := l.VaultSizeHistory(1)
	if err != nil {
		t.Fatalf("VaultSizeHistory failed: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points length = %d, want 1", len(points))
	}
	today := time.Now().UTC().Format("2006-01-02")
	if points[0].Day != today || points[0].Bytes != 456 {
		t.Fatalf("point = %#v, want day %s bytes 456", points[0], today)
	}

	var count int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM vault_size_daily WHERE day = ?", today).Scan(&count); err != nil {
		t.Fatalf("count vault size rows failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("same-day row count = %d, want 1", count)
	}
}

func TestVaultSizeHistoryWindowAndOrdering(t *testing.T) {
	l := newTestActivityLogger(t)
	if err := l.ensureVaultSizeTable(); err != nil {
		t.Fatalf("ensureVaultSizeTable failed: %v", err)
	}

	now := time.Now().UTC()
	rows := []struct {
		day   string
		bytes int64
	}{
		{now.AddDate(0, 0, -8).Format("2006-01-02"), 80},
		{now.AddDate(0, 0, -5).Format("2006-01-02"), 50},
		{now.AddDate(0, 0, -2).Format("2006-01-02"), 20},
		{now.Format("2006-01-02"), 10},
	}
	for _, row := range rows {
		if _, err := l.db.Exec("INSERT INTO vault_size_daily (day, bytes) VALUES (?, ?)", row.day, row.bytes); err != nil {
			t.Fatalf("insert vault size row failed: %v", err)
		}
	}

	points, err := l.VaultSizeHistory(7)
	if err != nil {
		t.Fatalf("VaultSizeHistory failed: %v", err)
	}
	want := []SizePoint{
		{Day: now.AddDate(0, 0, -5).Format("2006-01-02"), Bytes: 50},
		{Day: now.AddDate(0, 0, -2).Format("2006-01-02"), Bytes: 20},
		{Day: now.Format("2006-01-02"), Bytes: 10},
	}
	if len(points) != len(want) {
		t.Fatalf("points length = %d, want %d: %#v", len(points), len(want), points)
	}
	for i := range want {
		if points[i] != want[i] {
			t.Fatalf("point %d = %#v, want %#v", i, points[i], want[i])
		}
		if i > 0 && points[i-1].Day >= points[i].Day {
			t.Fatalf("points not strictly ascending: %#v", points)
		}
	}
}

func newTestActivityLogger(t *testing.T) *Logger {
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
