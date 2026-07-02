package audit

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	// recallRetentionDays limits historical recall telemetry to the window used
	// by visibility and decay features.
	recallRetentionDays = 90
	// recallMaxRows caps the table so a busy shared vault cannot grow the audit
	// database without bound.
	recallMaxRows = 100_000
)

// RecallHitRecord mirrors memory.RecallEventHit without importing memory. The
// audit layer stays dependency-free and stores hashes only: recall queries can
// contain secrets, as shown by the personal-leak regression.
type RecallHitRecord struct {
	File     string
	LineHash string
	Score    float32
	Rank     int
	Accepted bool
}

// RecallFileStats summarizes accepted recall hits by file.
type RecallFileStats struct {
	File    string
	Hits7   int
	Hits30  int
	Hits90  int
	LastHit time.Time
}

// HotFact identifies a frequently recalled fact chunk.
type HotFact struct {
	File     string
	LineHash string
	Hits     int
}

// ensureRecallTable creates the table on first use. Success is cached per
// Logger; a FAILURE is returned but never latched — a transient "database is
// locked" at startup must not kill analytics for the process lifetime.
func (l *Logger) ensureRecallTable() error {
	l.recallMu.Lock()
	defer l.recallMu.Unlock()
	if l.recallReady {
		return nil
	}
	if err := createRecallTable(l.db); err != nil {
		return err
	}
	l.recallReady = true
	return nil
}

func createRecallTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS recall_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TEXT NOT NULL,
			event_id TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			remote_host TEXT NOT NULL DEFAULT '',
			query_hash TEXT NOT NULL,
			fallback INTEGER NOT NULL,
			file TEXT NOT NULL,
			line_hash TEXT NOT NULL,
			score REAL NOT NULL,
			rank INTEGER NOT NULL,
			accepted INTEGER NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create recall_events table: %w", err)
	}

	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_recall_file_ts ON recall_events(file, ts)"); err != nil {
		return fmt.Errorf("failed to create recall file index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_recall_ts ON recall_events(ts)"); err != nil {
		return fmt.Errorf("failed to create recall timestamp index: %w", err)
	}
	return nil
}

// RecallMeta carries the per-recall context. QueryHash only, never raw query
// text (recall queries can contain secrets — the personal-leak regression).
// Source/RemoteHost mirror audit_entries attribution so a host can tell local
// recalls from a remote consumer's.
type RecallMeta struct {
	Provider   string
	QueryHash  string
	Fallback   bool
	Source     string // "local" | "ssh-remote"
	RemoteHost string
}

// RecordRecall records one completed recall event.
func (l *Logger) RecordRecall(meta RecallMeta, hits []RecallHitRecord) error {
	if l == nil || l.db == nil || len(hits) == 0 {
		return nil
	}
	if err := l.ensureRecallTable(); err != nil {
		return err
	}

	tx, err := l.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin recall transaction: %w", err)
	}
	defer tx.Rollback()

	ts := time.Now().UTC().Format(time.RFC3339)
	eventID := uuid.New().String()
	fallbackInt := boolToInt(meta.Fallback)

	stmt, err := tx.Prepare(`
		INSERT INTO recall_events (ts, event_id, provider, source, remote_host, query_hash, fallback, file, line_hash, score, rank, accepted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare recall insert: %w", err)
	}
	defer stmt.Close()

	for _, hit := range hits {
		if _, err := stmt.Exec(ts, eventID, meta.Provider, meta.Source, meta.RemoteHost, meta.QueryHash, fallbackInt, hit.File, hit.LineHash, hit.Score, hit.Rank, boolToInt(hit.Accepted)); err != nil {
			return fmt.Errorf("failed to insert recall event: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit recall transaction: %w", err)
	}

	// Prune ~1 in 32 CALLS. A per-Logger counter, not rowid parity: a fixed
	// batch size of 32 (the emission cap) would make rowid%32 fire on EVERY
	// call — exactly on the busy vaults where prune cost matters most.
	l.recallMu.Lock()
	l.recallCalls++
	doPrune := l.recallCalls%32 == 0
	l.recallMu.Unlock()
	if doPrune {
		if err := l.pruneRecallEvents(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Logger) pruneRecallEvents() error {
	if l == nil || l.db == nil {
		return nil
	}
	if err := l.ensureRecallTable(); err != nil {
		return err
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -recallRetentionDays).Format(time.RFC3339)
	if _, err := l.db.Exec("DELETE FROM recall_events WHERE ts < ?", cutoff); err != nil {
		return fmt.Errorf("failed to prune old recall events: %w", err)
	}

	var cutoffID int64
	err := l.db.QueryRow("SELECT id FROM recall_events ORDER BY id DESC LIMIT 1 OFFSET ?", recallMaxRows).Scan(&cutoffID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to find recall row cap cutoff: %w", err)
	}
	if _, err := l.db.Exec("DELETE FROM recall_events WHERE id <= ?", cutoffID); err != nil {
		return fmt.Errorf("failed to prune capped recall events: %w", err)
	}
	return nil
}

func (l *Logger) RecallStatsByFile() ([]RecallFileStats, error) {
	if l == nil || l.db == nil {
		return []RecallFileStats{}, nil
	}
	if err := l.ensureRecallTable(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	cutoff7 := now.AddDate(0, 0, -7).Format(time.RFC3339)
	cutoff30 := now.AddDate(0, 0, -30).Format(time.RFC3339)
	cutoff90 := now.AddDate(0, 0, -90).Format(time.RFC3339)

	rows, err := l.db.Query(`
		SELECT file,
		       SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END) AS hits7,
		       SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END) AS hits30,
		       SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END) AS hits90,
		       MAX(ts) AS last_hit
		FROM recall_events
		WHERE accepted = 1 AND fallback = 0
		GROUP BY file
		ORDER BY hits90 DESC, file ASC
	`, cutoff7, cutoff30, cutoff90)
	if err != nil {
		return nil, fmt.Errorf("failed to query recall file stats: %w", err)
	}
	defer rows.Close()

	stats := []RecallFileStats{}
	for rows.Next() {
		var stat RecallFileStats
		var lastHit string
		if err := rows.Scan(&stat.File, &stat.Hits7, &stat.Hits30, &stat.Hits90, &lastHit); err != nil {
			return nil, fmt.Errorf("failed to scan recall file stats: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, lastHit)
		if err != nil {
			return nil, fmt.Errorf("failed to parse recall last hit: %w", err)
		}
		stat.LastHit = parsed
		stats = append(stats, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate recall file stats: %w", err)
	}
	return stats, nil
}

func (l *Logger) HotFacts(days, limit int) ([]HotFact, error) {
	if l == nil || l.db == nil || limit <= 0 {
		return []HotFact{}, nil
	}
	if err := l.ensureRecallTable(); err != nil {
		return nil, err
	}
	if days <= 0 {
		days = recallRetentionDays
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	rows, err := l.db.Query(`
		SELECT file, line_hash, COUNT(*) AS hits
		FROM recall_events
		WHERE accepted = 1 AND fallback = 0 AND line_hash != '' AND ts >= ?
		GROUP BY file, line_hash
		ORDER BY hits DESC, file ASC, line_hash ASC
		LIMIT ?
	`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query hot facts: %w", err)
	}
	defer rows.Close()

	facts := []HotFact{}
	for rows.Next() {
		var fact HotFact
		if err := rows.Scan(&fact.File, &fact.LineHash, &fact.Hits); err != nil {
			return nil, fmt.Errorf("failed to scan hot fact: %w", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate hot facts: %w", err)
	}
	return facts, nil
}

func (l *Logger) RecallFallbackRate(days int) (fallbackQueries, totalQueries int, err error) {
	if l == nil || l.db == nil {
		return 0, 0, nil
	}
	if err := l.ensureRecallTable(); err != nil {
		return 0, 0, err
	}
	if days <= 0 {
		days = recallRetentionDays
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	err = l.db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN fallback = 1 THEN 1 ELSE 0 END), 0), COUNT(*)
		FROM (
			SELECT event_id, MAX(fallback) AS fallback
			FROM recall_events
			WHERE ts >= ?
			GROUP BY event_id
		)
	`, cutoff).Scan(&fallbackQueries, &totalQueries)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to query recall fallback rate: %w", err)
	}
	return fallbackQueries, totalQueries, nil
}

func (l *Logger) LastRecallByLine(file string) (map[string]time.Time, error) {
	if l == nil || l.db == nil {
		return map[string]time.Time{}, nil
	}
	if err := l.ensureRecallTable(); err != nil {
		return nil, err
	}

	rows, err := l.db.Query(`
		SELECT line_hash, MAX(ts)
		FROM recall_events
		WHERE accepted = 1 AND fallback = 0 AND file = ? AND line_hash != ''
		GROUP BY line_hash
	`, file)
	if err != nil {
		return nil, fmt.Errorf("failed to query last recall by line: %w", err)
	}
	defer rows.Close()

	recalls := map[string]time.Time{}
	for rows.Next() {
		var lineHash, ts string
		if err := rows.Scan(&lineHash, &ts); err != nil {
			return nil, fmt.Errorf("failed to scan last recall by line: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil, fmt.Errorf("failed to parse last recall timestamp: %w", err)
		}
		recalls[lineHash] = parsed
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate last recall by line: %w", err)
	}
	return recalls, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
