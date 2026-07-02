package audit

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ActivityEvent is a cursor-friendly projection of audit_entries for live UI
// feeds. It intentionally includes all audit actions; recall telemetry lives in
// recall_events and is surfaced separately.
type ActivityEvent struct {
	ID         int64
	TS         time.Time
	Provider   string
	Action     string
	File       string
	Source     string
	RemoteHost string
}

// SizePoint is one recorded vault size sample for a UTC calendar day.
type SizePoint struct {
	Day   string
	Bytes int64
}

var vaultSizeReady sync.Map

// ensureVaultSizeTable creates the table on first use. Success is cached per
// Logger; a FAILURE is returned but never latched — a transient "database is
// locked" at startup must not kill dashboard history for the process lifetime.
func (l *Logger) ensureVaultSizeTable() error {
	if l == nil || l.db == nil {
		return nil
	}

	l.recallMu.Lock()
	defer l.recallMu.Unlock()
	if ready, ok := vaultSizeReady.Load(l); ok && ready.(bool) {
		return nil
	}
	if err := createVaultSizeTable(l.db); err != nil {
		return err
	}
	vaultSizeReady.Store(l, true)
	return nil
}

func createVaultSizeTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vault_size_daily (
			day TEXT PRIMARY KEY,
			bytes INTEGER NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create vault_size_daily table: %w", err)
	}
	return nil
}

// EventsSince returns audit entries after lastID in ascending id order. This is
// incremental so dashboard ticks can advance a cursor instead of rescanning the
// table.
func (l *Logger) EventsSince(lastID int64, limit int) ([]ActivityEvent, error) {
	if l == nil || l.db == nil {
		return []ActivityEvent{}, nil
	}
	if limit <= 0 {
		limit = 50
	}

	rows, err := l.db.Query(`
		SELECT id, timestamp, provider, action, file, COALESCE(source, ''), COALESCE(remote_host, '')
		FROM audit_entries
		WHERE id > ?
		ORDER BY id ASC
		LIMIT ?
	`, lastID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query activity events: %w", err)
	}
	defer rows.Close()

	events := []ActivityEvent{}
	for rows.Next() {
		var event ActivityEvent
		var ts string
		if err := rows.Scan(&event.ID, &ts, &event.Provider, &event.Action, &event.File, &event.Source, &event.RemoteHost); err != nil {
			return nil, fmt.Errorf("failed to scan activity event: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, ts)
		if err == nil {
			event.TS = parsed
		}
		// A single legacy malformed timestamp must not kill the live feed; the
		// renderer can still show the event with an unknown timestamp.
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate activity events: %w", err)
	}
	return events, nil
}

// LatestEventID returns the current audit_entries high-water mark so callers can
// seed a cursor at "now" instead of replaying full history.
func (l *Logger) LatestEventID() (int64, error) {
	if l == nil || l.db == nil {
		return 0, nil
	}

	var id sql.NullInt64
	err := l.db.QueryRow("SELECT MAX(id) FROM audit_entries").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to query latest activity event id: %w", err)
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

// RecordVaultSize records today's UTC vault size, replacing an earlier same-day
// sample. It stays cheap enough for opportunistic hot-path calls.
func (l *Logger) RecordVaultSize(bytes int64) error {
	if l == nil || l.db == nil {
		return nil
	}
	if err := l.ensureVaultSizeTable(); err != nil {
		return err
	}

	day := time.Now().UTC().Format("2006-01-02")
	if _, err := l.db.Exec(`
		INSERT OR REPLACE INTO vault_size_daily (day, bytes)
		VALUES (?, ?)
	`, day, bytes); err != nil {
		return fmt.Errorf("failed to record vault size: %w", err)
	}
	return nil
}

// VaultSizeHistory returns recorded samples within the last days in ascending
// day order. Missing days are deliberately absent rather than zero-filled; the
// renderer interpolates gaps so storage stays sparse.
func (l *Logger) VaultSizeHistory(days int) ([]SizePoint, error) {
	if l == nil || l.db == nil {
		return []SizePoint{}, nil
	}
	if err := l.ensureVaultSizeTable(); err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 30
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	rows, err := l.db.Query(`
		SELECT day, bytes
		FROM vault_size_daily
		WHERE day >= ?
		ORDER BY day ASC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query vault size history: %w", err)
	}
	defer rows.Close()

	points := []SizePoint{}
	for rows.Next() {
		var point SizePoint
		if err := rows.Scan(&point.Day, &point.Bytes); err != nil {
			return nil, fmt.Errorf("failed to scan vault size point: %w", err)
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate vault size history: %w", err)
	}
	return points, nil
}
