package detect

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAppSupportDir_CurrentOS verifies the exact path for the OS this test
// binary is running on. darwin and linux are asserted with full equality;
// the default (windows/other) branch asserts non-empty + correct app suffix.
func TestAppSupportDir_CurrentOS(t *testing.T) {
	home := t.TempDir()

	got := AppSupportDir(home, "Claude")

	switch runtime.GOOS {
	case "darwin":
		want := filepath.Join(home, "Library", "Application Support", "Claude")
		if got != want {
			t.Errorf("darwin: got %q, want %q", got, want)
		}
	case "linux":
		want := filepath.Join(home, ".config", "Claude")
		if got != want {
			t.Errorf("linux: got %q, want %q", got, want)
		}
	default: // windows/other — APPDATA value varies across environments
		if got == "" {
			t.Errorf("default OS: got empty string, want non-empty path")
		}
		if !strings.HasSuffix(got, "Claude") {
			t.Errorf("default OS: got %q, expected suffix %q", got, "Claude")
		}
	}
}

// TestAppSupportDir_Invariants checks OS-independent guarantees that must hold
// on every platform.
func TestAppSupportDir_Invariants(t *testing.T) {
	home := t.TempDir()

	tests := []struct {
		name string
		app  string
	}{
		{"Claude", "Claude"},
		{"Cursor", "Cursor"},
		{"Codex", "Codex"},
	}

	for _, tc := range tests {
		t.Run("non_empty_"+tc.name, func(t *testing.T) {
			// Arrange
			app := tc.app

			// Act
			got := AppSupportDir(home, app)

			// Assert: result is non-empty
			if got == "" {
				t.Errorf("AppSupportDir(%q, %q) = %q; want non-empty", home, app, got)
			}
		})

		t.Run("ends_with_app_"+tc.name, func(t *testing.T) {
			// Arrange / Act
			got := AppSupportDir(home, tc.app)

			// Assert: last path segment is the app name
			if filepath.Base(got) != tc.app {
				t.Errorf("filepath.Base(%q) = %q; want %q", got, filepath.Base(got), tc.app)
			}
		})
	}

	t.Run("different_apps_yield_different_paths", func(t *testing.T) {
		// Arrange / Act
		pathClaude := AppSupportDir(home, "Claude")
		pathCursor := AppSupportDir(home, "Cursor")

		// Assert
		if pathClaude == pathCursor {
			t.Errorf("expected distinct paths for different apps, both returned %q", pathClaude)
		}
	})

	t.Run("uses_filepath_separator", func(t *testing.T) {
		// Arrange / Act
		got := AppSupportDir(home, "Claude")

		// Assert: on Windows the path separator is `\`; on all other platforms it
		// is `/`. filepath.Join normalises separators for the current OS, so the
		// universal portable check is simply that the last segment equals the app
		// name — no trailing separator, no OS-specific concatenation artefacts.
		if filepath.Base(got) != "Claude" {
			t.Errorf("filepath.Base(%q) = %q; want %q", got, filepath.Base(got), "Claude")
		}

		// On Windows, the path must use the OS separator (backslash). A path that
		// still contains only forward slashes would indicate hand-concatenation
		// instead of filepath.Join.
		if runtime.GOOS == "windows" {
			if strings.Contains(got, "/") {
				t.Errorf("windows path %q contains forward-slash; expected only filepath.Separator", got)
			}
		}
	})
}

// TestInstalledAgents_NoPanic is a smoke test that confirms InstalledAgents
// completes without panicking and returns a valid (possibly empty) slice on
// all platforms — including ones where no agents are installed.
func TestInstalledAgents_NoPanic(t *testing.T) {
	// Arrange / Act — must not panic
	agents := InstalledAgents()

	// Assert: a nil slice is valid; len >= 0 is always true but exercises
	// the return type contract explicitly.
	if len(agents) < 0 {
		t.Error("InstalledAgents returned a slice with negative length (impossible)")
	}
	// Sanity-check each returned Agent has a non-empty Name and Path.
	for i, a := range agents {
		if a.Name == "" {
			t.Errorf("agents[%d].Name is empty", i)
		}
		if a.Path == "" {
			t.Errorf("agents[%d] (%s) has empty Path", i, a.Name)
		}
	}
}
