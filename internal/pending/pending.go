package pending

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/safepath"
)

// ErrConflict marks a pending change whose target file was modified (in a way
// that overlaps this change) after the pending was created. Approving it
// blindly would silently drop the intervening edit — callers must surface the
// conflict and require an explicit force (`auxly approve --force`).
var ErrConflict = errors.New("pending change conflicts with newer edits to its target")

// PendingFile represents a file waiting for approval.
type PendingFile struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
}

// Manager handles the .pending/ directory.
type Manager struct {
	pendingDir string
	memoryRoot string
}

// NewManager creates a new pending manager.
func NewManager(memoryRoot string) *Manager {
	return &Manager{
		pendingDir: filepath.Join(memoryRoot, ".pending"),
		memoryRoot: memoryRoot,
	}
}

// List returns all files in .pending/.
func (m *Manager) List() ([]PendingFile, error) {
	if err := os.MkdirAll(m.pendingDir, 0700); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(m.pendingDir)
	if err != nil {
		return nil, err
	}

	var files []PendingFile
	for _, entry := range entries {
		// Skip dirs and dotfiles (in-flight .auxly-tmp-* atomic-write temps,
		// .lock) — only real pending entries are listed.
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, _ := entry.Info()
		files = append(files, PendingFile{
			Name:    entry.Name(),
			Path:    filepath.Join(m.pendingDir, entry.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return files, nil
}

// Write writes content to a pending file.
// The filename carries a timestamp plus a random suffix so two agents queueing
// the same target in the same millisecond can never collide.
func (m *Manager) Write(targetFile, content string) (string, error) {
	// The target name is caller/agent-controlled and gets interpolated into the
	// frontmatter. An embedded newline could inject extra `key: value` lines
	// (e.g. a decoy empty `basehash:`) that defeat conflict detection — reject
	// control characters outright. Path traversal is separately caught by
	// ResolveSafe at approve time.
	for _, r := range targetFile {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("invalid target filename: contains control character")
		}
	}

	if err := os.MkdirAll(m.pendingDir, 0700); err != nil {
		return "", err
	}

	// Snapshot the target's current content hash so Approve can detect that the
	// file changed underneath this pending (conflict detection). Missing/invalid
	// target → empty-content hash, matching how Approve reads it.
	baseHash := m.targetHash(targetFile)

	// Unique pending filename: <timestamp>_<rand4hex>_<target>.md. O_EXCL makes
	// the filesystem enforce uniqueness — a same-ms same-suffix collision fails
	// loudly and retries with a fresh suffix instead of overwriting a queued change.
	base := strings.TrimSuffix(filepath.Base(targetFile), ".md")
	var pendingName, pendingPath string
	for {
		pendingName = fmt.Sprintf("%d_%s_%s.md", time.Now().UnixMilli(), randHex4(), base)
		pendingPath = filepath.Join(m.pendingDir, pendingName)
		f, err := os.OpenFile(pendingPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			break
		}
		if !os.IsExist(err) {
			return "", err
		}
	}

	// Write metadata header + content (atomic rename over the reserved name).
	header := fmt.Sprintf("---\ntarget: %s\ncreated: %s\nbasehash: %s\n---\n\n", targetFile, time.Now().UTC().Format(time.RFC3339), baseHash)
	if err := memory.AtomicWriteFile(pendingPath, []byte(header+content), 0600); err != nil {
		os.Remove(pendingPath)
		return "", err
	}
	return pendingName, nil
}

// targetHash returns the sha256 hex of the target file's current content
// ("" content when the file doesn't exist or the path is invalid).
func (m *Manager) targetHash(targetFile string) string {
	var content []byte
	if p, err := safepath.ResolveSafe(m.memoryRoot, targetFile); err == nil {
		content, _ = os.ReadFile(p)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func randHex4() string {
	var b [2]byte
	rand.Read(b[:]) // crypto/rand never fails on supported platforms
	return hex.EncodeToString(b[:])
}

// Approve moves a pending file's content to the target memory file. It fails
// with ErrConflict when the target changed underneath the pending in a way this
// change overlaps — use ForceApprove (CLI: `auxly approve --force`) to apply anyway.
func (m *Manager) Approve(pendingName string) error {
	return m.approve(pendingName, false)
}

// ForceApprove applies a pending change even if it conflicts with newer edits.
func (m *Manager) ForceApprove(pendingName string) error {
	return m.approve(pendingName, true)
}

func (m *Manager) approve(pendingName string, force bool) error {
	// Serialize with every other vault writer (concurrent approves from several
	// MCP servers / CLI / TUI) — and, crucially, take the lock BEFORE reading the
	// pending entry: a concurrent Reject (which also locks) can then never remove
	// an entry whose content we'd still go on to apply.
	unlock, err := memory.LockVault(m.memoryRoot)
	if err != nil {
		return err
	}
	defer unlock()

	pendingPath := filepath.Join(m.pendingDir, pendingName)
	data, err := os.ReadFile(pendingPath)
	if err != nil {
		return fmt.Errorf("pending file not found: %s", pendingName)
	}

	// Parse target from frontmatter
	target := extractTarget(string(data))
	if target == "" {
		return fmt.Errorf("cannot determine target file from pending entry")
	}

	// Extract content (everything after the frontmatter closing ---)
	diffContent := extractContent(string(data))

	// Write to target — validate the frontmatter-supplied target stays inside the
	// vault. A malicious/poisoned pending entry could carry target: ../../.ssh/...
	// (C3); ResolveSafe allows legitimate relative subpaths but rejects absolute
	// paths, ".." escapes, and symlink escapes.
	targetPath, err := safepath.ResolveSafe(m.memoryRoot, target)
	if err != nil {
		return fmt.Errorf("invalid pending target %q: %w", target, err)
	}

	// Read existing content
	var existingContent string
	if fileData, err := os.ReadFile(targetPath); err == nil {
		existingContent = string(fileData)
	}

	// Conflict detection: the pending recorded the target's content hash at
	// creation. If the target has changed since AND this diff overlaps the
	// changed material, applying blindly would silently drop the newer edit.
	// Pure additions and edits whose deletion targets are still intact merge
	// safely and proceed. Legacy pendings without basehash skip the check.
	if !force {
		if baseHash := extractField(string(data), "basehash"); baseHash != "" {
			sum := sha256.Sum256([]byte(existingContent))
			if hex.EncodeToString(sum[:]) != baseHash && diffOverlaps(existingContent, diffContent) {
				return fmt.Errorf("%w: %s was modified after this pending was created — review with `auxly view %s`, then `auxly approve --force %s` to apply anyway", ErrConflict, target, target, pendingName)
			}
		}
	}

	// Apply diff content cleanly
	mergedContent := ApplyDiff(existingContent, diffContent)

	if err := memory.AtomicWriteFile(targetPath, []byte(mergedContent), 0644); err != nil {
		return fmt.Errorf("failed to write to %s: %w", target, err)
	}

	// Trigger re-compilation of unified memory
	if target != "unified_memory.md" {
		store := memory.NewStore(m.memoryRoot)
		_ = store.CompileUnified()
	}

	// Remove pending file
	return os.Remove(pendingPath)
}

// diffOverlaps reports whether applying diff to a target that changed since the
// pending was created would collide with those newer edits. ApplyDiff's only
// destructive operation is deletion of matching lines — so the change overlaps
// exactly when some "-" line no longer exists in the current content (the fact
// it meant to remove/replace was itself edited meanwhile). Pure additions are
// order-independent and never conflict.
func diffOverlaps(existing, diff string) bool {
	current := make(map[string]bool)
	for _, l := range strings.Split(existing, "\n") {
		current[strings.TrimSpace(l)] = true
	}
	for _, dl := range strings.Split(diff, "\n") {
		if strings.HasPrefix(dl, "---") || strings.HasPrefix(dl, "+++") || strings.HasPrefix(dl, "@@") {
			continue
		}
		if strings.HasPrefix(dl, "-") {
			deletion := strings.TrimSpace(strings.TrimPrefix(dl, "-"))
			if deletion != "" && !current[deletion] {
				return true
			}
		}
	}
	return false
}

func ApplyDiff(existing, diff string) string {
	if existing == "" {
		var cleanLines []string
		lines := strings.Split(diff, "\n")

		isUnified := false
		for _, line := range lines {
			if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
				isUnified = true
				break
			}
		}

		for _, line := range lines {
			if isUnified {
				if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "@@") {
					continue
				}
				if strings.HasPrefix(line, "+") {
					cleanLines = append(cleanLines, strings.TrimPrefix(line, "+"))
				} else if strings.HasPrefix(line, "-") {
					continue
				} else {
					cleanLines = append(cleanLines, line)
				}
			} else {
				if strings.HasPrefix(line, "+") {
					cleanLines = append(cleanLines, strings.TrimPrefix(line, "+"))
				} else if strings.HasPrefix(line, "-") {
					continue
				} else {
					cleanLines = append(cleanLines, line)
				}
			}
		}
		return strings.Join(cleanLines, "\n")
	}

	lines := strings.Split(existing, "\n")
	diffLines := strings.Split(diff, "\n")

	for _, dl := range diffLines {
		if strings.HasPrefix(dl, "---") || strings.HasPrefix(dl, "+++") || strings.HasPrefix(dl, "@@") {
			continue
		}
		if strings.HasPrefix(dl, "+") {
			addition := strings.TrimPrefix(dl, "+")
			alreadyExists := false
			for _, el := range lines {
				if strings.TrimSpace(el) == strings.TrimSpace(addition) {
					alreadyExists = true
					break
				}
			}
			if !alreadyExists {
				lines = append(lines, addition)
			}
		} else if strings.HasPrefix(dl, "-") {
			deletion := strings.TrimPrefix(dl, "-")
			// A bare "-" (empty deletion target) would match and strip EVERY blank
			// line in the file — no meaningful target, skip it. Keeps diffOverlaps'
			// conflict model in sync: the only destructive op is a non-empty deletion.
			if strings.TrimSpace(deletion) == "" {
				continue
			}
			var newLines []string
			for _, el := range lines {
				if strings.TrimSpace(el) != strings.TrimSpace(deletion) {
					newLines = append(newLines, el)
				}
			}
			lines = newLines
		}
	}

	return strings.Join(lines, "\n")
}

// Reject deletes a pending file. Takes the vault lock so a rejection can never
// race an in-flight Approve of the same entry (approve reads under the lock).
func (m *Manager) Reject(pendingName string) error {
	unlock, err := memory.LockVault(m.memoryRoot)
	if err != nil {
		return err
	}
	defer unlock()

	pendingPath := filepath.Join(m.pendingDir, pendingName)
	if _, err := os.Stat(pendingPath); os.IsNotExist(err) {
		return fmt.Errorf("pending file not found: %s", pendingName)
	}
	return os.Remove(pendingPath)
}

// ViewDiff reads and returns the content of a pending file.
func (m *Manager) ViewDiff(pendingName string) (string, error) {
	pendingPath := filepath.Join(m.pendingDir, pendingName)
	data, err := os.ReadFile(pendingPath)
	if err != nil {
		return "", fmt.Errorf("pending file not found: %s", pendingName)
	}
	return string(data), nil
}

func extractTarget(content string) string {
	return extractField(content, "target")
}

// extractField returns the value of a `key: value` line ("" if absent), looking
// ONLY inside the frontmatter block (between the first and second `---`) — body
// content must never be able to shadow a metadata field.
func extractField(content, key string) string {
	prefix := key + ": "
	dashes := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "---" {
			dashes++
			if dashes >= 2 {
				return ""
			}
			continue
		}
		if dashes == 1 && strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func extractContent(data string) string {
	// Find the second "---" that closes the frontmatter
	count := 0
	for i, line := range strings.Split(data, "\n") {
		if strings.TrimSpace(line) == "---" {
			count++
			if count == 2 {
				// Return everything after this line
				rest := strings.Split(data, "\n")
				if i+1 < len(rest) {
					return strings.Join(rest[i+1:], "\n")
				}
				return ""
			}
		}
		_ = i
	}
	return data
}
