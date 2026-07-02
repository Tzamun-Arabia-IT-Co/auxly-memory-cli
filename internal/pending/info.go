package pending

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

// Info is the parsed metadata of one pending entry, for list/bulk UX.
type Info struct {
	Name      string
	Target    string
	Agent     string // "" for legacy entries written before attribution
	Created   time.Time
	Additions int
	Deletions int
}

// Info parses one pending entry's frontmatter + a ±line summary of its diff.
func (m *Manager) Info(pendingName string) (Info, error) {
	data, err := os.ReadFile(filepath.Join(m.pendingDir, pendingName))
	if err != nil {
		return Info{}, err
	}
	content := string(data)
	info := Info{
		Name:   pendingName,
		Target: extractTarget(content),
		Agent:  extractField(content, "agent"),
	}
	if ts, terr := time.Parse(time.RFC3339, extractField(content, "created")); terr == nil {
		info.Created = ts
	}
	for _, l := range strings.Split(extractContent(content), "\n") {
		switch {
		case strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			info.Additions++
		case strings.HasPrefix(l, "-") && strings.TrimSpace(l) != "-":
			info.Deletions++
		}
	}
	return info, nil
}

// pendingTTL is how long an unreviewed pending entry lives before it is swept
// into .pending/archive/ (default 30 days, override AUXLY_PENDING_TTL_DAYS;
// 0 disables the sweep).
func pendingTTL() time.Duration {
	if v := os.Getenv("AUXLY_PENDING_TTL_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil && d >= 0 {
			return time.Duration(d) * 24 * time.Hour
		}
	}
	return 30 * 24 * time.Hour
}

// SweepExpired moves pending entries older than the TTL into .pending/archive/
// (nothing is deleted — stale queue noise disappears from the review list but
// every queued change stays recoverable). Age comes from the frontmatter
// `created` field, falling back to file mtime for damaged entries. Returns the
// archived names.
//
// It takes the vault lock, serializing against Approve/Reject: without it a
// sweep could rename an entry in the window between Approve's read and its
// final remove — the change would apply but be reported as failed AND survive
// as a ghost in the archive. Callers are the explicit surfaces (`auxly
// pending`, doctor) — never the TUI's per-second List poll.
func (m *Manager) SweepExpired() ([]string, error) {
	ttl := pendingTTL()
	if ttl == 0 {
		return nil, nil
	}
	unlock, err := memory.LockVault(m.memoryRoot)
	if err != nil {
		return nil, err
	}
	defer unlock()
	entries, err := os.ReadDir(m.pendingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cutoff := time.Now().Add(-ttl)
	var archived []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		created := time.Time{}
		if info, ierr := m.Info(e.Name()); ierr == nil {
			created = info.Created
		}
		if created.IsZero() {
			if fi, ferr := e.Info(); ferr == nil {
				created = fi.ModTime()
			} else {
				continue
			}
		}
		if !created.Before(cutoff) {
			continue
		}
		archiveDir := filepath.Join(m.pendingDir, "archive")
		if err := os.MkdirAll(archiveDir, 0700); err != nil {
			return archived, err
		}
		// A concurrent approve/reject may have removed the entry — a failed
		// rename is that race resolving, not an error worth failing the sweep.
		if err := os.Rename(filepath.Join(m.pendingDir, e.Name()), filepath.Join(archiveDir, e.Name())); err == nil {
			archived = append(archived, e.Name())
		}
	}
	return archived, nil
}
