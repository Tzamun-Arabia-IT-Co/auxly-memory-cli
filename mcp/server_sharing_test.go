package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/sharing"
)

// remoteServer builds a server tagged as an SSH-remote consumer with the given
// per-peer share, plus a vault containing a private personal.md (carrying a
// secret marker) and a shared projects.md.
func remoteServer(t *testing.T, share *sharing.ClientShare) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	const secret = "SECRET_SALARY_FIGURE"
	if err := os.WriteFile(filepath.Join(dir, "personal.md"), []byte("# Personal\n"+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "projects.md"), []byte("# Projects\nauxly launch notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(dir)
	s.store.WorkspaceRoot = "" // hermetic: ignore any real .auxly workspace under cwd
	s.isRemote = true
	s.share = share
	return s, secret
}

// §10 — a remote read-only peer with personal.md excluded must not see personal
// content through search (the toolSearch ACL bypass that was the reported leak).
func TestRemoteSearch_DoesNotLeakPersonal(t *testing.T) {
	s, secret := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	out := resultText(s.toolSearch(secret))
	// A leak surfaces the file: "📄 personal.md\n   <secret line>". The query
	// itself echoes in the "No results" message, so assert on the file marker.
	if strings.Contains(out, "personal.md") {
		t.Errorf("search leaked personal.md to a remote peer: %q", out)
	}
	// Sanity: search still works for a granted file.
	if hit := resultText(s.toolSearch("launch")); !strings.Contains(hit, "projects.md") {
		t.Errorf("search should still return granted projects.md, got: %q", hit)
	}
}

// §10 — a remote read-only peer must not be able to delete lines from personal.md
// (the toolSkillForget ACL bypass).
func TestRemoteForget_CannotPrunePersonal(t *testing.T) {
	s, secret := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	_ = s.toolSkillForget(secret)
	data, err := os.ReadFile(filepath.Join(s.memoryPath, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), secret) {
		t.Error("remote forget deleted content from personal.md — must be untouched")
	}
}

// §10 — personal.md is OFF BY DEFAULT for a remote: a peer whose share omits it
// (here a default non-personal grant) can neither read nor write it.
func TestRemotePersonal_OffByDefault(t *testing.T) {
	s, _ := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	if s.canRead("personal.md") {
		t.Error("personal.md must not be readable by a remote that wasn't granted it")
	}
	if s.canWrite("personal.md") {
		t.Error("personal.md must not be writable by a remote that wasn't granted it")
	}
	if !s.canRead("projects.md") {
		t.Error("granted projects.md should be readable")
	}
}

// §10 — but the host OWNS the data: an explicit personal grant is honored, so the
// remote can read (and, with a write grant, write) personal.md. This is the
// host's deliberate, warning-gated choice in the TUI.
func TestRemotePersonal_ServedWhenExplicitlyGranted(t *testing.T) {
	s, secret := remoteServer(t, &sharing.ClientShare{
		SharedFiles: []string{"projects.md", "personal.md"},
		WriteFiles:  []string{"personal.md"},
	})
	if !s.canRead("personal.md") {
		t.Error("an explicit personal grant must be honored for reads")
	}
	if !s.canWrite("personal.md") {
		t.Error("an explicit personal write grant must be honored")
	}
	// And the content is actually reachable through search now.
	if out := resultText(s.toolSearch(secret)); !strings.Contains(out, "personal.md") {
		t.Errorf("granted personal.md should be searchable, got: %q", out)
	}
}

// §10 — write gate: a read-only peer cannot write a file it can otherwise read.
func TestRemoteWrite_ReadOnlyDenied(t *testing.T) {
	s, _ := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	if s.canWrite("projects.md") {
		t.Error("read-only peer must not have write access to projects.md")
	}
}

// Local (non-remote) sessions retain full access — the gates only bind remotes.
func TestLocalSession_FullAccess(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir) // isRemote defaults false
	if !s.canRead("personal.md") || !s.canWrite("personal.md") {
		t.Error("local session must keep full read/write access")
	}
}

// §10 — the ONBOARDING TEXT leak: /auxly-init's footer used to print the full
// category guide (including personal.md) to every caller. A remote peer must not
// even be told personal.md exists, and must be told its read-only scope.
func TestRemoteInit_FooterHidesPersonalAndStatesScope(t *testing.T) {
	s, _ := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	out := resultText(s.toolSkillInit())
	if strings.Contains(out, "personal.md") {
		t.Errorf("remote /auxly-init footer must never name personal.md, got:\n%s", out)
	}
	if !strings.Contains(out, "READ-ONLY") {
		t.Errorf("remote read-only peer must be told the connection is read-only, got:\n%s", out)
	}
	if !strings.Contains(out, "projects.md") {
		t.Errorf("remote footer should still list the granted projects.md, got:\n%s", out)
	}
}

// §10 — the same scope discipline applies to the profile read (/auxly-memory).
func TestRemoteMemory_FooterScoped(t *testing.T) {
	s, _ := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "write"})
	out := resultText(s.toolSkillMemory())
	if strings.Contains(out, "personal.md") {
		t.Errorf("remote /auxly-memory footer leaked personal.md:\n%s", out)
	}
	if !strings.Contains(out, "read & write") {
		t.Errorf("a write-granted peer should see projects.md as writable, got:\n%s", out)
	}
}

// A local session still gets the full taxonomy (personal.md included) so the
// fix did not regress the normal onboarding experience.
func TestLocalInit_ShowsFullTaxonomy(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir)
	s.store.WorkspaceRoot = ""
	out := resultText(s.toolSkillInit())
	if !strings.Contains(out, "personal.md") {
		t.Errorf("local /auxly-init should show the full guide including personal.md, got:\n%s", out)
	}
}
