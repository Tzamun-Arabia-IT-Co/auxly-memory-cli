package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileInfo represents metadata about a memory file.
type FileInfo struct {
	Name     string
	Path     string
	Size     int64
	ModTime  time.Time
	IsDir    bool
}

// Store manages both global and workspace-level memory folders.
type Store struct {
	Root          string
	WorkspaceRoot string
}

// NewStore creates a new memory store, dynamically detecting any local workspace.
func NewStore(root string) *Store {
	workspaceRoot := findWorkspaceRoot()
	return &Store{
		Root:          root,
		WorkspaceRoot: workspaceRoot,
	}
}

// findWorkspaceRoot walks up the current working directory to find a .git or .auxly marker.
func findWorkspaceRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := cwd
	for {
		// Look for active .auxly/memory directory
		target := filepath.Join(dir, ".auxly", "memory")
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			return target
		}
		// Look for .git repo as a workspace candidate
		gitTarget := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitTarget); err == nil && info.IsDir() {
			return filepath.Join(dir, ".auxly", "memory")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// List returns all .md files in the global memory folder, merged dynamically with workspace overrides.
func (s *Store) List() ([]FileInfo, error) {
	filesMap := make(map[string]FileInfo)

	// 1. Read global root files
	globalEntries, err := os.ReadDir(s.Root)
	if err == nil {
		for _, entry := range globalEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			filesMap[entry.Name()] = FileInfo{
				Name:    entry.Name(),
				Path:    filepath.Join(s.Root, entry.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
			}
		}
	}

	// 2. Read local workspace files (if it exists)
	if s.WorkspaceRoot != "" {
		if _, err := os.Stat(s.WorkspaceRoot); err == nil {
			localEntries, err := os.ReadDir(s.WorkspaceRoot)
			if err == nil {
				for _, entry := range localEntries {
					if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
						continue
					}
					info, err := entry.Info()
					if err != nil {
						continue
					}
					// Workspace overrides global file coordinates
					filesMap[entry.Name()] = FileInfo{
						Name:    entry.Name(),
						Path:    filepath.Join(s.WorkspaceRoot, entry.Name()),
						Size:    info.Size(),
						ModTime: info.ModTime(),
					}
				}
			}
		}
	}

	var files []FileInfo
	for _, f := range filesMap {
		files = append(files, f)
	}
	return files, nil
}

// View reads and returns the contents of a memory file, prioritizing local workspace overrides.
func (s *Store) View(filename string) (string, error) {
	if s.WorkspaceRoot != "" {
		localPath := filepath.Join(s.WorkspaceRoot, filepath.Clean(filename))
		if _, err := os.Stat(localPath); err == nil {
			data, err := os.ReadFile(localPath)
			if err == nil {
				return string(data), nil
			}
		}
	}

	path, err := s.resolvePath(filename)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", filename, err)
	}
	return string(data), nil
}

// Write writes content globally.
func (s *Store) Write(filename string, content string) error {
	return s.WriteScoped(filename, content, "global")
}

// WriteScoped writes content to either "workspace" or "global" memory directories.
func (s *Store) WriteScoped(filename string, content string, scope string) error {
	var path string
	var err error

	if scope == "workspace" && s.WorkspaceRoot != "" {
		if err := os.MkdirAll(s.WorkspaceRoot, 0755); err != nil {
			return fmt.Errorf("cannot create workspace memory directory: %w", err)
		}
		path = filepath.Join(s.WorkspaceRoot, filepath.Clean(filename))
	} else {
		path, err = s.resolvePath(filename)
		if err != nil {
			return err
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("cannot create directory: %w", err)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return err
	}

	// Trigger auto-compilation of unified memory
	if filename != "unified_memory.md" {
		_ = s.CompileUnified()
	}
	return nil
}

// CompileUnified compiles all memory files into a single unified_memory.md file.
func (s *Store) CompileUnified() error {
	files, err := s.List()
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# 🧠 Auxly Unified Memory Vault\n\n")
	b.WriteString("> This file is automatically compiled and managed by auxly-cli. Do not edit directly.\n")
	b.WriteString(fmt.Sprintf("> Last Synced: %s\n\n", time.Now().Format("02/01/2006 15:04:05")))

	for _, f := range files {
		if f.Name == "unified_memory.md" {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}

		b.WriteString(fmt.Sprintf("## 📄 File: %s\n\n", f.Name))
		b.WriteString(content)
		b.WriteString("\n\n---\n\n")
	}

	// Write directly without resolvePath recursion checks
	path := filepath.Join(s.Root, "unified_memory.md")
	return os.WriteFile(path, []byte(b.String()), 0644)
}

// Search performs a case-insensitive substring search across all .md files.
func (s *Store) Search(query string) (map[string][]string, error) {
	results := make(map[string][]string)
	query = strings.ToLower(query)

	files, err := s.List()
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		content, err := s.View(f.Name)
		if err != nil {
			continue
		}
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			if strings.Contains(strings.ToLower(line), query) {
				results[f.Name] = append(results[f.Name], line)
			}
		}
	}
	return results, nil
}

// Exists checks if a memory file exists either globally or locally.
func (s *Store) Exists(filename string) bool {
	if s.WorkspaceRoot != "" {
		localPath := filepath.Join(s.WorkspaceRoot, filepath.Clean(filename))
		if _, err := os.Stat(localPath); err == nil {
			return true
		}
	}
	path, err := s.resolvePath(filename)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// PendingDir returns the path to the .pending/ subdirectory.
func (s *Store) PendingDir() string {
	return filepath.Join(s.Root, ".pending")
}

func (s *Store) resolvePath(filename string) (string, error) {
	cleaned := filepath.Clean(filename)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("access denied: path traversal attempt for '%s'", filename)
	}
	resolved := filepath.Join(s.Root, cleaned)
	rel, err := filepath.Rel(s.Root, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("access denied: path escapes boundary")
	}
	return resolved, nil
}
