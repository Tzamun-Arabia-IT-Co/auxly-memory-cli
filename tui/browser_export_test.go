package tui

import (
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
)

// TestBrowserExportKey verifies the Files tab's [e] action: it fires an export command
// when files exist (and is inert when empty), and the result message renders an honest
// status. The command itself isn't executed here (that would touch ~/Downloads — the
// export logic is covered in internal/memory/export_test.go).
func TestBrowserExportKey(t *testing.T) {
	m := newBrowserModel(memory.NewStore(t.TempDir()))

	// No files → [e] is inert.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}); cmd != nil {
		t.Error("[e] must be inert with no files")
	}

	// With files, [e] starts an export and shows progress.
	m.files = []memory.FileInfo{{Name: "projects.md"}, {Name: "identity.md"}}
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if cmd == nil {
		t.Error("[e] with files must start an export command")
	}
	if !strings.Contains(m2.status, "Exporting") {
		t.Errorf("expected an in-progress status, got %q", m2.status)
	}

	// Success message renders a tagged path; failure renders an error.
	ok, _ := m2.Update(browserExportMsg{dir: "/Users/x/Downloads/auxly-memory-export-2026-06-05_021718", count: 9})
	if !strings.Contains(ok.status, "✓") || !strings.Contains(ok.status, "9") {
		t.Errorf("success status should confirm the count, got %q", ok.status)
	}
	bad, _ := m2.Update(browserExportMsg{err: errExport})
	if !strings.HasPrefix(bad.status, "✗") {
		t.Errorf("failure status should be marked ✗, got %q", bad.status)
	}

	// An export that skipped encrypted files says so inline, not just in the
	// MANIFEST.txt Export() itself writes — the TUI must not silently hide
	// that some files were left out of the snapshot.
	skipped, _ := m2.Update(browserExportMsg{dir: "/Users/x/Downloads/auxly-memory-export-x", count: 3, skipped: 2})
	if !strings.Contains(skipped.status, "2") || !strings.Contains(skipped.status, "skipped") {
		t.Errorf("status should note the skipped encrypted file count, got %q", skipped.status)
	}

	// The View advertises the [e] action.
	if !strings.Contains(stripANSI(m2.View()), "[e]") {
		t.Error("the Files view should advertise the [e] export action")
	}
}

type exportErr struct{}

func (exportErr) Error() string { return "disk full" }

var errExport = exportErr{}
