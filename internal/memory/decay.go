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

// ArchiveFacts moves exact stale snapshots to .archive for human-auditable
// retention. RULE 0: decay never deletes facts; humans can grep .archive forever.
// Lines that no longer match exactly are skipped, not errors. Returns the count
// of facts successfully archived.
func (s *Store) ArchiveFacts(file string, lines []string) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	// MAJOR 7: resolve/cache the vault identity BEFORE the lock — see
	// PrewarmCrypto's doc comment for why this must never happen while
	// holding LockVault.
	s.PrewarmCrypto()

	unlock, err := LockVault(s.Root)
	if err != nil {
		return 0, err
	}
	defer unlock()

	content, sourcePath, root, encrypted, err := s.readViewCopy(file)
	if err != nil {
		return 0, err
	}

	targets := make(map[string]int, len(lines))
	for _, l := range lines {
		targets[l]++
	}

	fileLines := strings.SplitAfter(content, "\n")
	kept := make([]string, 0, len(fileLines))
	var archivedOriginals []string
	for _, raw := range fileLines {
		trimmedEOL := strings.TrimRight(raw, "\r\n")
		trimmedSpace := strings.TrimSpace(trimmedEOL)
		if targets[trimmedSpace] > 0 {
			targets[trimmedSpace]--
			archivedOriginals = append(archivedOriginals, trimmedEOL)
		} else {
			kept = append(kept, raw)
		}
	}

	applied := len(archivedOriginals)
	if applied == 0 {
		return 0, nil
	}

	archivePath, err := safepath.ResolveSafe(root, filepath.Join(".archive", file))
	if err != nil {
		return 0, err
	}
	// Read through readVaultFile (not raw os.ReadFile) — a prior archive pass
	// may have already encrypted this file.
	existing, _, err := s.readVaultFile(archivePath)
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("cannot read archive %s: %w", file, err)
	}

	var archBuf strings.Builder
	archBuf.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		archBuf.WriteByte('\n')
	}
	for _, orig := range archivedOriginals {
		archBuf.WriteString(orig)
		archBuf.WriteByte('\n')
	}

	// CRITICAL 5: encryption is STICKY. `encrypted` alone (the SOURCE's
	// CURRENT state) covers the archive's very first write, when
	// .archive/<file> doesn't exist yet to carry any state of its own — but
	// `auxly decrypt file <name>` never touches .archive/<name>, so a source
	// that was later decrypted must not silently drag an already-encrypted
	// archive back to plaintext on the next append. OR in
	// shouldStayEncrypted(archivePath) (the archive's OWN on-disk state, "" /
	// missing = false) makes that stick.
	archiveEncrypted := encrypted || s.shouldStayEncrypted(archivePath)
	if err := s.writeVaultFile(archivePath, []byte(archBuf.String()), 0644, archiveEncrypted); err != nil {
		return 0, fmt.Errorf("cannot write archive %s: %w", file, err)
	}

	if err := s.writeVaultFile(sourcePath, []byte(strings.Join(kept, "")), 0644, encrypted); err != nil {
		return applied, fmt.Errorf("cannot write %s: %w", file, err)
	}
	return applied, nil
}

// ArchiveFact moves one exact stale snapshot to .archive for human-auditable
// retention. RULE 0: decay never deletes facts; humans can grep .archive forever.
func (s *Store) ArchiveFact(file, line string) error {
	applied, err := s.ArchiveFacts(file, []string{line})
	if err != nil {
		return err
	}
	if applied == 0 {
		return errors.New("fact not found (changed since review?)")
	}
	return nil
}

// RestampFacts refreshes exact fact lines in place, keeping review human-led.
// Lines that no longer match exactly are skipped, not errors. Returns the
// count of facts successfully re-stamped.
func (s *Store) RestampFacts(file string, lines []string) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	// MAJOR 7: see ArchiveFact — resolve/cache the identity before the lock.
	s.PrewarmCrypto()

	unlock, err := LockVault(s.Root)
	if err != nil {
		return 0, err
	}
	defer unlock()

	content, sourcePath, _, encrypted, err := s.readViewCopy(file)
	if err != nil {
		return 0, err
	}

	targets := make(map[string]int, len(lines))
	for _, l := range lines {
		targets[l]++
	}

	today := time.Now().Format("2006-01-02")
	fileLines := strings.SplitAfter(content, "\n")
	applied := 0
	for i, raw := range fileLines {
		trimmedEOL := strings.TrimRight(raw, "\r\n")
		trimmedSpace := strings.TrimSpace(trimmedEOL)
		if targets[trimmedSpace] > 0 {
			targets[trimmedSpace]--
			eol := raw[len(trimmedEOL):]
			// restampLine gets the full original (indentation intact) line so a
			// nested bullet's leading whitespace survives the round trip.
			nextLine := restampLine(trimmedEOL, today)
			fileLines[i] = nextLine + eol
			applied++
		}
	}

	if applied == 0 {
		return 0, nil
	}

	if err := s.writeVaultFile(sourcePath, []byte(strings.Join(fileLines, "")), 0644, encrypted); err != nil {
		return 0, fmt.Errorf("cannot write %s: %w", file, err)
	}
	return applied, nil
}

// RestampFact refreshes an exact fact line in place, keeping review human-led.
func (s *Store) RestampFact(file, line string) error {
	applied, err := s.RestampFacts(file, []string{line})
	if err != nil {
		return err
	}
	if applied == 0 {
		return errors.New("fact not found (changed since review?)")
	}
	return nil
}

func isBulletLine(line string) bool {
	return strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")
}

// readViewCopy locates and reads the EXACT copy of file that Store.View would
// return: the workspace override when one exists, else the global root.
// ArchiveFact/RestampFact must mutate that same copy — StaleFacts finds facts
// via View, so a workspace-shadowed fact edited against the global root would
// either report "not found" or silently rewrite a file nothing reads. Returns
// the content, the resolved file path, the root it came from (so the caller
// can archive alongside the copy it actually touched), and whether the
// source was encrypted on disk (workspace overrides never are — v1 limit).
func (s *Store) readViewCopy(file string) (content, path, root string, encrypted bool, err error) {
	if s.WorkspaceRoot != "" {
		if localPath, perr := safepath.ResolveSafe(s.WorkspaceRoot, file); perr == nil {
			if _, statErr := os.Stat(localPath); statErr == nil {
				data, rerr := os.ReadFile(localPath)
				if rerr != nil {
					return "", "", "", false, fmt.Errorf("cannot read %s: %w", file, rerr)
				}
				return string(data), localPath, s.WorkspaceRoot, false, nil
			}
		}
	}
	path, err = s.resolvePath(file)
	if err != nil {
		return "", "", "", false, err
	}
	data, enc, err := s.readVaultFile(path)
	if err != nil {
		return "", "", "", false, fmt.Errorf("cannot read %s: %w", file, err)
	}
	return string(data), path, s.Root, enc, nil
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
