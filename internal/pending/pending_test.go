package pending

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Characterization: a normal write→approve lands the content in the vault file.
func TestApprove_LegitTarget(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	name, err := m.Write("identity.md", "+hello world")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := m.Approve(name); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "identity.md"))
	if err != nil {
		t.Fatalf("target not written: %v", err)
	}
	if !strings.Contains(string(data), "hello world") {
		t.Fatalf("target content = %q, want it to contain 'hello world'", string(data))
	}
}

// C3: a poisoned pending entry whose frontmatter target escapes the vault must
// be rejected, and must NOT write any file outside the vault root.
func TestApprove_RejectsTraversalTarget(t *testing.T) {
	root := t.TempDir()
	pendingDir := filepath.Join(root, ".pending")
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Craft a malicious pending file by hand (as a compromised agent could).
	escapeName := "evil.md"
	body := "---\ntarget: ../../" + escapeName + "\ncreated: 2026-01-01T00:00:00Z\n---\n\n+pwned\n"
	pendingFile := filepath.Join(pendingDir, "999_evil.md")
	if err := os.WriteFile(pendingFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(root)
	if err := m.Approve("999_evil.md"); err == nil {
		t.Fatalf("Approve accepted a traversal target")
	}
	// Nothing should have been written two levels up.
	escaped := filepath.Join(filepath.Dir(filepath.Dir(root)), escapeName)
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("traversal target escaped the vault: wrote %s", escaped)
	}
}
