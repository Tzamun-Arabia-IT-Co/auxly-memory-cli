package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/safepath"
)

// StaleFact is a review candidate: old enough and not recently recalled.
type StaleFact struct {
	File       string
	Line       string
	LineNo     int
	FactDate   time.Time
	LastRecall time.Time
}

var factDateRE = regexp.MustCompile(`\[(\d{4}-\d{2}-\d{2})\]|\(updated\s+(\d{4}-\d{2}-\d{2})`)

func reviewAgeDays() int {
	v := strings.TrimSpace(os.Getenv("AUXLY_REVIEW_AGE_DAYS"))
	if v == "" {
		return 90
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 90
	}
	return n
}

func factDate(line string) time.Time {
	var newest time.Time
	for _, m := range factDateRE.FindAllStringSubmatch(line, -1) {
		dateText := m[1]
		if dateText == "" {
			dateText = m[2]
		}
		t, err := time.Parse("2006-01-02", dateText)
		if err != nil {
			continue
		}
		if newest.IsZero() || t.After(newest) {
			newest = t
		}
	}
	return newest
}

// StaleFacts returns facts that are old and recall-silent for human review.
func (s *Store) StaleFacts(lastRecall func(file string) (map[string]time.Time, error), includePersonal bool) ([]StaleFact, error) {
	ageDays := reviewAgeDays()
	if ageDays == 0 {
		return nil, nil
	}

	files, err := s.List()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	cutoff := now.AddDate(0, 0, -ageDays)
	var stale []StaleFact

	// Undated facts can't lean on file mtime for age: AtomicWriteFile (every
	// organize run, every restamp, any rewrite) creates a fresh temp file and
	// renames it over the target, resetting mtime — so an actively-maintained
	// vault's mtime says "just written" forever and decay never fires. Instead
	// track first-seen-by-content in a ledger, keyed by a hash immune to file
	// rewrites. Self-bootstrapping: a fact's first scan records "now" (so it
	// isn't flagged yet); it becomes eligible ageDays later, same as a dated
	// fact would be.
	seen := loadReviewSeen(s.Root)
	seenDirty := false

	for _, f := range files {
		if f.IsDir || strings.HasPrefix(f.Name, ".") || f.Name == "unified_memory.md" {
			continue
		}
		if !IsOrganizableFile(f.Name) {
			continue
		}
		if !includePersonal && IsPersonalFile(f.Name) {
			continue
		}

		content, err := s.View(f.Name)
		if err != nil {
			return nil, err
		}

		recalls := map[string]time.Time(nil)
		if lastRecall != nil {
			recalls, _ = lastRecall(f.Name)
		}

		for lineNo, raw := range strings.Split(content, "\n") {
			line := strings.TrimSpace(raw)
			if !isBulletLine(line) {
				continue
			}
			fd := factDate(line)
			ageDate := fd
			if ageDate.IsZero() {
				h := HashRecallText(line)
				if ts, ok := seen[h]; ok {
					if t, perr := time.Parse(time.RFC3339, ts); perr == nil {
						ageDate = t
					} else {
						ageDate = now
					}
				} else {
					seen[h] = now.Format(time.RFC3339)
					seenDirty = true
					ageDate = now
				}
			}
			if !ageDate.Before(cutoff) {
				continue
			}

			lr := recalls[HashRecallText(strings.TrimSpace(line))]
			if !lr.IsZero() && !lr.Before(cutoff) {
				continue
			}

			stale = append(stale, StaleFact{
				File:       f.Name,
				Line:       line,
				LineNo:     lineNo + 1,
				FactDate:   fd,
				LastRecall: lr,
			})
		}
	}

	// Best-effort: a failed/skipped save just means first-seen dates re-bootstrap
	// on the next scan — never worth failing the whole review over.
	if seenDirty {
		if unlock, err := LockVault(s.Root); err == nil {
			saveReviewSeen(s.Root, seen)
			unlock()
		}
	}

	sort.Slice(stale, func(i, j int) bool {
		di := stale[i].FactDate
		dj := stale[j].FactDate
		if di.IsZero() != dj.IsZero() {
			return di.IsZero()
		}
		if !di.Equal(dj) {
			return di.Before(dj)
		}
		if stale[i].File != stale[j].File {
			return stale[i].File < stale[j].File
		}
		return stale[i].LineNo < stale[j].LineNo
	})
	return stale, nil
}

// ArchiveFact moves one exact stale snapshot to .archive for human-auditable
// retention. RULE 0: decay never deletes facts; humans can grep .archive forever.
func (s *Store) ArchiveFact(file, line string) error {
	unlock, err := LockVault(s.Root)
	if err != nil {
		return err
	}
	defer unlock()

	content, sourcePath, root, err := s.readViewCopy(file)
	if err != nil {
		return err
	}

	lines := strings.SplitAfter(content, "\n")
	idx := -1
	var original string
	for i, raw := range lines {
		trimmedEOL := strings.TrimRight(raw, "\r\n")
		if strings.TrimSpace(trimmedEOL) == line {
			idx = i
			original = trimmedEOL
			break
		}
	}
	if idx < 0 {
		return errors.New("fact not found (changed since review?)")
	}

	kept := append([]string{}, lines[:idx]...)
	kept = append(kept, lines[idx+1:]...)
	if err := AtomicWriteFile(sourcePath, []byte(strings.Join(kept, "")), 0644); err != nil {
		return fmt.Errorf("cannot write %s: %w", file, err)
	}

	archivePath, err := safepath.ResolveSafe(root, filepath.Join(".archive", file))
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(archivePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot read archive %s: %w", file, err)
	}
	// Preserve the original line's indentation (real bullets nest, e.g.
	// "  - Driver: ...") rather than the caller's trimmed StaleFact.Line.
	next := string(existing) + original + "\n"
	if err := AtomicWriteFile(archivePath, []byte(next), 0644); err != nil {
		return fmt.Errorf("cannot write archive %s: %w", file, err)
	}
	return nil
}

// RestampFact refreshes an exact fact line in place, keeping review human-led.
func (s *Store) RestampFact(file, line string) error {
	unlock, err := LockVault(s.Root)
	if err != nil {
		return err
	}
	defer unlock()

	content, sourcePath, _, err := s.readViewCopy(file)
	if err != nil {
		return err
	}

	today := time.Now().Format("2006-01-02")
	lines := strings.SplitAfter(content, "\n")
	for i, raw := range lines {
		trimmedEOL := strings.TrimRight(raw, "\r\n")
		if strings.TrimSpace(trimmedEOL) != line {
			continue
		}
		eol := raw[len(trimmedEOL):]
		// restampLine gets the full original (indentation intact) line so a
		// nested bullet's leading whitespace survives the round trip.
		nextLine := restampLine(trimmedEOL, today)
		lines[i] = nextLine + eol
		if err := AtomicWriteFile(sourcePath, []byte(strings.Join(lines, "")), 0644); err != nil {
			return fmt.Errorf("cannot write %s: %w", file, err)
		}
		return nil
	}
	return errors.New("fact not found (changed since review?)")
}

func isBulletLine(line string) bool {
	return strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")
}

// readViewCopy locates and reads the EXACT copy of file that Store.View would
// return: the workspace override when one exists, else the global root.
// ArchiveFact/RestampFact must mutate that same copy — StaleFacts finds facts
// via View, so a workspace-shadowed fact edited against the global root would
// either report "not found" or silently rewrite a file nothing reads. Returns
// the content, the resolved file path, and the root it came from (so the
// caller can archive alongside the copy it actually touched).
func (s *Store) readViewCopy(file string) (content, path, root string, err error) {
	if s.WorkspaceRoot != "" {
		if localPath, perr := safepath.ResolveSafe(s.WorkspaceRoot, file); perr == nil {
			if _, statErr := os.Stat(localPath); statErr == nil {
				data, rerr := os.ReadFile(localPath)
				if rerr != nil {
					return "", "", "", fmt.Errorf("cannot read %s: %w", file, rerr)
				}
				return string(data), localPath, s.WorkspaceRoot, nil
			}
		}
	}
	path, err = s.resolvePath(file)
	if err != nil {
		return "", "", "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", fmt.Errorf("cannot read %s: %w", file, err)
	}
	return string(data), path, s.Root, nil
}

var stampRE = regexp.MustCompile(`\[\d{4}-\d{2}-\d{2}\]`)

// restampLine re-dates a fact line. Only the LAST bracketed date is touched —
// earlier ones are content (e.g. an incident reference date), not the
// freshness stamp, and must survive a re-stamp untouched.
func restampLine(line, today string) string {
	idxs := stampRE.FindAllStringIndex(line, -1)
	if len(idxs) == 0 {
		return line + " [" + today + "]"
	}
	last := idxs[len(idxs)-1]
	return line[:last[0]] + "[" + today + "]" + line[last[1]:]
}

// reviewSeenPath is the first-seen ledger for undated facts (Finding 5): a
// content hash isn't reset by AtomicWriteFile's rewrite-and-rename the way
// mtime is.
func reviewSeenPath(root string) string {
	return filepath.Join(root, ".index", "review-seen.json")
}

// loadReviewSeen reads the ledger, or an empty map if it doesn't exist yet or
// is unreadable — the caller treats every fact as newly-seen in that case,
// which is exactly the safe/self-bootstrapping behavior we want.
func loadReviewSeen(root string) map[string]string {
	data, err := os.ReadFile(reviewSeenPath(root))
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return map[string]string{}
	}
	return m
}

// saveReviewSeen persists the ledger. Best-effort — called under LockVault by
// StaleFacts, but a failure here just means first-seen dates re-bootstrap.
func saveReviewSeen(root string, seen map[string]string) {
	data, err := json.Marshal(seen)
	if err != nil {
		return
	}
	_ = AtomicWriteFile(reviewSeenPath(root), data, 0644)
}
