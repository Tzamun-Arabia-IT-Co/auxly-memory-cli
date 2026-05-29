package audit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// Entry represents a single audit log entry.
type Entry struct {
	Timestamp  string `json:"timestamp"`
	AgentID    string `json:"agent_id"`
	Provider   string `json:"provider"`
	Action     string `json:"action"`
	File       string `json:"file"`
	Diff       string `json:"diff"`
	Reason     string `json:"reason"`
	TrustLevel string `json:"trust_level"`
	RequestID  string `json:"request_id"`
	Signature  string `json:"signature,omitempty"`
	Source     string `json:"source,omitempty"`
	RemoteIP   string `json:"remote_ip,omitempty"`
	RemoteOS   string `json:"remote_os,omitempty"`
	RemoteHost string `json:"remote_host,omitempty"`
}

// SourceMeta carries attribution for where a write originated.
type SourceMeta struct {
	Source     string // "local" | "ssh-remote"
	RemoteIP   string
	RemoteOS   string
	RemoteHost string
}

// Logger handles dual-write to .audit.log and audit.db.
type Logger struct {
	logPath string
	db      *sql.DB
}

// NewLogger creates a new audit logger at the given memory root.
func NewLogger(memoryRoot string) (*Logger, error) {
	logPath := filepath.Join(memoryRoot, ".audit.log")
	dbPath := filepath.Join(memoryRoot, "audit.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit.db: %w", err)
	}

	// Create table if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			action TEXT NOT NULL,
			file TEXT NOT NULL,
			diff TEXT,
			reason TEXT,
			trust_level TEXT NOT NULL,
			request_id TEXT NOT NULL,
			signature TEXT,
			source TEXT,
			remote_ip TEXT,
			remote_os TEXT,
			remote_host TEXT
		)
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create audit table: %w", err)
	}

	// Lightweight migration for pre-existing DBs: add the SSH-remote
	// attribution columns. SQLite returns a "duplicate column" error if the
	// column already exists, which we intentionally ignore.
	for _, col := range []string{"source", "remote_ip", "remote_os", "remote_host"} {
		_, err := db.Exec(fmt.Sprintf("ALTER TABLE audit_entries ADD COLUMN %s TEXT", col))
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("failed to migrate audit table column %s: %w", col, err)
		}
	}

	return &Logger{logPath: logPath, db: db}, nil
}

// Log writes an entry to both .audit.log and audit.db. It attributes the entry
// to the local source. For SSH-remote attribution, use LogWithSource.
func (l *Logger) Log(agentID, provider, action, file, diff, reason, trustLevel string) (*Entry, error) {
	return l.LogWithSource(agentID, provider, action, file, diff, reason, trustLevel, SourceMeta{Source: "local"})
}

// LogWithSource writes an entry to both .audit.log and audit.db, attributing it
// to the origin described by meta.
func (l *Logger) LogWithSource(agentID, provider, action, file, diff, reason, trustLevel string, meta SourceMeta) (*Entry, error) {
	if meta.Source == "" {
		meta.Source = "local"
	}

	entry := &Entry{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		AgentID:    agentID,
		Provider:   provider,
		Action:     action,
		File:       file,
		Diff:       diff,
		Reason:     reason,
		TrustLevel: trustLevel,
		RequestID:  uuid.New().String(),
		Source:     meta.Source,
		RemoteIP:   meta.RemoteIP,
		RemoteOS:   meta.RemoteOS,
		RemoteHost: meta.RemoteHost,
	}

	// Write to .audit.log (append-only JSON lines)
	jsonLine, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal audit entry: %w", err)
	}

	f, err := os.OpenFile(l.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open .audit.log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(jsonLine, '\n')); err != nil {
		return nil, fmt.Errorf("failed to write to .audit.log: %w", err)
	}

	// Write to audit.db
	_, err = l.db.Exec(`
		INSERT INTO audit_entries (timestamp, agent_id, provider, action, file, diff, reason, trust_level, request_id, signature, source, remote_ip, remote_os, remote_host)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.Timestamp, entry.AgentID, entry.Provider, entry.Action, entry.File, entry.Diff, entry.Reason, entry.TrustLevel, entry.RequestID, entry.Signature, entry.Source, entry.RemoteIP, entry.RemoteOS, entry.RemoteHost)
	if err != nil {
		return nil, fmt.Errorf("failed to insert into audit.db: %w", err)
	}

	return entry, nil
}

// Tail reads the last N entries from audit.db for high performance, falling back to log file if db is unavailable.
func (l *Logger) Tail(n int) ([]Entry, error) {
	if l.db == nil {
		return l.tailFileFallback(n)
	}

	rows, err := l.db.Query(`
		SELECT timestamp, agent_id, provider, action, file, diff, reason, trust_level, request_id, signature, source, remote_ip, remote_os, remote_host
		FROM audit_entries
		ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return l.tailFileFallback(n)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		err := rows.Scan(&e.Timestamp, &e.AgentID, &e.Provider, &e.Action, &e.File, &e.Diff, &e.Reason, &e.TrustLevel, &e.RequestID, &e.Signature, &e.Source, &e.RemoteIP, &e.RemoteOS, &e.RemoteHost)
		if err != nil {
			continue
		}
		// Append to maintain descending chronological order (newest first)
		entries = append(entries, e)
	}
	return entries, nil
}

// TailWrites gets the last N entries where action = 'write' from audit.db.
func (l *Logger) TailWrites(n int) ([]Entry, error) {
	if l.db == nil {
		return l.tailWritesFileFallback(n)
	}

	rows, err := l.db.Query(`
		SELECT timestamp, agent_id, provider, action, file, diff, reason, trust_level, request_id, signature, source, remote_ip, remote_os, remote_host
		FROM audit_entries
		WHERE action = 'write'
		ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return l.tailWritesFileFallback(n)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		err := rows.Scan(&e.Timestamp, &e.AgentID, &e.Provider, &e.Action, &e.File, &e.Diff, &e.Reason, &e.TrustLevel, &e.RequestID, &e.Signature, &e.Source, &e.RemoteIP, &e.RemoteOS, &e.RemoteHost)
		if err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (l *Logger) tailWritesFileFallback(n int) ([]Entry, error) {
	all, err := l.tailFileFallback(0)
	if err != nil {
		return nil, err
	}
	var writes []Entry
	for _, e := range all {
		if e.Action == "write" {
			writes = append(writes, e)
			if len(writes) >= n {
				break
			}
		}
	}
	return writes, nil
}

func (l *Logger) tailFileFallback(n int) ([]Entry, error) {
	data, err := os.ReadFile(l.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	lines := splitLines(data)
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	var entries []Entry
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		// Prepend to maintain descending chronological order (newest first)
		entries = append([]Entry{entry}, entries...)
	}
	return entries, nil
}

// Stats returns aggregate statistics from audit.db.
type Stats struct {
	TotalEntries        int
	WritesToday         int
	ByProvider          map[string]int
	ByAction            map[string]int
	PendingCount        int
	TotalLogsByProvider map[string]int
}

func (l *Logger) Stats() (*Stats, error) {
	stats := &Stats{
		ByProvider:          make(map[string]int),
		ByAction:            make(map[string]int),
		TotalLogsByProvider: make(map[string]int),
	}

	// Total entries (writes only)
	row := l.db.QueryRow("SELECT COUNT(*) FROM audit_entries WHERE action = 'write'")
	row.Scan(&stats.TotalEntries)

	// Writes today
	today := time.Now().UTC().Format("2006-01-02")
	row = l.db.QueryRow("SELECT COUNT(*) FROM audit_entries WHERE timestamp LIKE ? AND action = 'write'", today+"%")
	row.Scan(&stats.WritesToday)

	rows, err := l.db.Query("SELECT provider, COUNT(*) FROM audit_entries WHERE action = 'write' GROUP BY provider")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var provider string
			var count int
			rows.Scan(&provider, &count)
			stats.ByProvider[provider] = count
		}
	}

	// Total logs (any action) by provider
	rows, err = l.db.Query("SELECT provider, COUNT(*) FROM audit_entries GROUP BY provider")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var provider string
			var count int
			rows.Scan(&provider, &count)
			stats.TotalLogsByProvider[provider] = count
		}
	}

	// By action
	rows, err = l.db.Query("SELECT action, COUNT(*) FROM audit_entries GROUP BY action")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var action string
			var count int
			rows.Scan(&action, &count)
			stats.ByAction[action] = count
		}
	}

	return stats, nil
}

// ActiveProviders returns all providers whose absolute latest log entry is not a 'disconnect'.
func (l *Logger) ActiveProviders(duration time.Duration) ([]string, error) {
	if l.db == nil {
		return nil, nil
	}
	// Return active providers whose absolute latest log entry is NOT a 'disconnect' action
	rows, err := l.db.Query(`
		SELECT DISTINCT provider FROM audit_entries a 
		WHERE id = (SELECT MAX(id) FROM audit_entries b WHERE b.provider = a.provider) 
		AND action != 'disconnect'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			providers = append(providers, p)
		}
	}
	return providers, nil
}

// Close closes the database connection.
func (l *Logger) Close() error {
	if l.db != nil {
		return l.db.Close()
	}
	return nil
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
