package pending

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

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
	if err := os.MkdirAll(m.pendingDir, 0755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(m.pendingDir)
	if err != nil {
		return nil, err
	}

	var files []PendingFile
	for _, entry := range entries {
		if entry.IsDir() {
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
// The filename is prefixed with a timestamp to avoid collisions.
func (m *Manager) Write(targetFile, content string) (string, error) {
	if err := os.MkdirAll(m.pendingDir, 0755); err != nil {
		return "", err
	}

	// Create a unique pending filename: <timestamp>_<target>.md
	base := strings.TrimSuffix(filepath.Base(targetFile), ".md")
	pendingName := fmt.Sprintf("%d_%s.md", time.Now().UnixMilli(), base)
	pendingPath := filepath.Join(m.pendingDir, pendingName)

	// Write metadata header + content
	header := fmt.Sprintf("---\ntarget: %s\ncreated: %s\n---\n\n", targetFile, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(pendingPath, []byte(header+content), 0644); err != nil {
		return "", err
	}
	return pendingName, nil
}

// Approve moves a pending file's content to the target memory file.
func (m *Manager) Approve(pendingName string) error {
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

	// Write to target
	targetPath := filepath.Join(m.memoryRoot, target)

	// Read existing content
	var existingContent string
	if fileData, err := os.ReadFile(targetPath); err == nil {
		existingContent = string(fileData)
	}

	// Apply diff content cleanly
	mergedContent := ApplyDiff(existingContent, diffContent)

	if err := os.WriteFile(targetPath, []byte(mergedContent), 0644); err != nil {
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

// Reject deletes a pending file.
func (m *Manager) Reject(pendingName string) error {
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
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "target: ") {
			return strings.TrimPrefix(line, "target: ")
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
