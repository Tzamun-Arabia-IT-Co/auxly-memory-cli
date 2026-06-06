// Package safepath provides vault-boundary path validation shared by the memory
// and pending packages. It allows legitimate relative subpaths (the vault
// supports writing to subdirectories) while rejecting absolute paths, ".."
// escapes, and symlink escapes out of the root.
//
// Resolve performs string-level boundary checks only and is behaviorally
// identical to the historical memory.resolvePath, so existing callers see no
// change for legitimate inputs. ResolveSafe adds symlink-escape defense by
// resolving the deepest existing ancestor of the target and verifying the real
// path stays within the real root.
package safepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sep is the OS path separator as a string (e.g. "/" on POSIX).
var sep = string(filepath.Separator)

// Resolve validates that name stays within root, allowing relative subpaths but
// rejecting absolute paths and ".." escapes. It returns the cleaned path joined
// under root. No symlink resolution is performed — use ResolveSafe for that.
//
// The checks intentionally mirror the original memory.resolvePath so legitimate
// inputs (including "sub/dir/file.md") resolve exactly as before.
func Resolve(root, name string) (string, error) {
	cleaned := filepath.Clean(name)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("access denied: path traversal attempt for %q", name)
	}
	resolved := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("access denied: path escapes boundary")
	}
	return resolved, nil
}

// ResolveSafe is Resolve plus symlink-escape defense. After the string-level
// boundary check it resolves symlinks on the deepest existing ancestor of the
// target and verifies that real path remains within the real root. This blocks
// both a symlinked target file (projects.md -> ~/.ssh/config) and a symlinked
// parent directory. Safe for both reads (existing file) and writes (target may
// not exist yet).
func ResolveSafe(root, name string) (string, error) {
	resolved, err := Resolve(root, name)
	if err != nil {
		return "", err
	}

	// Fresh vault: if the root itself doesn't exist yet, Resolve already guarantees
	// lexical containment and a non-existent tree has no symlinks to escape through.
	// Without this early return the ancestor walk would climb above root to "/" and
	// wrongly reject the first write into a brand-new vault.
	if _, rerr := os.Lstat(root); rerr != nil {
		return resolved, nil
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = filepath.Clean(root)
	}

	// Walk up to the deepest ancestor that actually exists and resolve it.
	probe := resolved
	for {
		if _, lerr := os.Lstat(probe); lerr == nil {
			real, rerr := filepath.EvalSymlinks(probe)
			if rerr != nil {
				return "", fmt.Errorf("access denied: cannot resolve %q: %w", name, rerr)
			}
			rel, rerr := filepath.Rel(realRoot, real)
			if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+sep) {
				return "", fmt.Errorf("access denied: symlink escapes boundary for %q", name)
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break // reached filesystem root without finding an existing ancestor
		}
		probe = parent
	}
	return resolved, nil
}
