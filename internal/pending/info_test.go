package pending

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWriteFromRecordsAgentAndInfoParsesIt locks the attribution chain: the
// agent lands in frontmatter, Info surfaces it plus ±counts, and legacy
// Write() entries stay parseable with an empty Agent.
func TestWriteFromRecordsAgentAndInfoParsesIt(t *testing.T) {
	m := NewManager(t.TempDir())

	name, err := m.WriteFrom("identity.md", "+ new fact\n+ another\n- old fact\n", "claude-code")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	info, err := m.Info(name)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Agent != "claude-code" || info.Target != "identity.md" {
		t.Fatalf("bad info: %+v", info)
	}
	if info.Additions != 2 || info.Deletions != 1 {
		t.Fatalf("±counts wrong: %+v", info)
	}
	if info.Created.IsZero() {
		t.Fatalf("created not parsed")
	}

	legacy, err := m.Write("prefs.md", "+ x\n")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	linfo, err := m.Info(legacy)
	if err != nil {
		t.Fatalf("Info legacy: %v", err)
	}
	if linfo.Agent != "" {
		t.Fatalf("legacy entry should have empty agent, got %q", linfo.Agent)
	}
}

// TestWriteFromRejectsControlCharsInAgent keeps the frontmatter-injection gate:
// an agent name with a newline could smuggle extra metadata fields.
func TestWriteFromRejectsControlCharsInAgent(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.WriteFrom("identity.md", "+ x\n", "evil\nbasehash: "); err == nil {
		t.Fatalf("control character in agent accepted")
	}
}

// TestSweepExpiredArchivesOldEntries locks the TTL sweep: entries past the TTL
// move to .pending/archive/ (not deleted), fresh entries stay, and List() no
// longer shows the archived ones.
func TestSweepExpiredArchivesOldEntries(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)

	oldName, err := m.WriteFrom("identity.md", "+ stale\n", "codex")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	freshName, err := m.WriteFrom("prefs.md", "+ fresh\n", "claude")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}

	// Age the old entry by rewriting its frontmatter `created` line 40 days back.
	oldPath := filepath.Join(root, ".pending", oldName)
	data, _ := os.ReadFile(oldPath)
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, l := range lines {
		if strings.HasPrefix(l, "created: ") {
			lines[i] = "created: " + time.Now().Add(-40*24*time.Hour).UTC().Format(time.RFC3339)
			replaced = true
			break
		}
	}
	if !replaced {
		t.Fatalf("no created line found")
	}
	aged := strings.Join(lines, "\n")
	if err := os.WriteFile(oldPath, []byte(aged), 0600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	archived, err := m.SweepExpired() // explicit, vault-locked — List never sweeps
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if len(archived) != 1 || archived[0] != oldName {
		t.Fatalf("archived = %v, want [%s]", archived, oldName)
	}
	files, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, f := range files {
		if f.Name == oldName {
			t.Fatalf("expired entry still listed")
		}
	}
	if len(files) != 1 || files[0].Name != freshName {
		t.Fatalf("fresh entry missing: %+v", files)
	}
	if _, err := os.Stat(filepath.Join(root, ".pending", "archive", oldName)); err != nil {
		t.Fatalf("expired entry not archived: %v", err)
	}
}

// TestSweepDisabledByEnv locks the escape hatch: AUXLY_PENDING_TTL_DAYS=0 turns
// the sweep off entirely.
func TestSweepDisabledByEnv(t *testing.T) {
	t.Setenv("AUXLY_PENDING_TTL_DAYS", "0")
	root := t.TempDir()
	m := NewManager(root)
	name, err := m.WriteFrom("identity.md", "+ x\n", "a")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	// Age via mtime AND created — sweep must still not touch it.
	oldPath := filepath.Join(root, ".pending", name)
	past := time.Now().Add(-90 * 24 * time.Hour)
	os.Chtimes(oldPath, past, past)
	if archived, err := m.SweepExpired(); err != nil || len(archived) != 0 {
		t.Fatalf("sweep ran despite TTL=0: %v %v", archived, err)
	}
	files, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("entry missing: %+v", files)
	}
}

// TestApproveFirstProjectSubfile locks the AtomicWriteFile parent-dir fix: the
// FIRST-ever approve targeting projects/<slug>.md (fresh vault, no projects/
// dir) must create the directory and apply — this failed with ENOENT before.
func TestApproveFirstProjectSubfile(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	name, err := m.WriteFrom("projects/widget-app.md", "+ - first fact\n", "claude-code")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if err := m.Approve(name); err != nil {
		t.Fatalf("Approve of first sub-file pending: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "projects", "widget-app.md"))
	if err != nil || !strings.Contains(string(data), "first fact") {
		t.Fatalf("sub-file not written: %v %q", err, data)
	}
}
