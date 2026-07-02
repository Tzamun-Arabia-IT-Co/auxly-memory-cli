package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectSlugAndFile locks the workspace→sub-file derivation, including
// stripping the .auxly/memory marker suffix that Store.WorkspaceRoot carries.
func TestProjectSlugAndFile(t *testing.T) {
	cases := []struct{ root, file string }{
		{"/Users/x/projects/auxly-memory/.auxly/memory", "projects/auxly-memory.md"},
		{"/Users/x/projects/My_Repo 2/.auxly/memory", "projects/my-repo-2.md"},
		{"C:\\Users\\x\\code\\WinApp\\.auxly\\memory", "projects/winapp.md"},
		{"", "projects/general.md"},
		{"/срв/😀/.auxly/memory", "projects/general.md"}, // nothing sluggable → general
	}
	for _, c := range cases {
		if got := ProjectFile(c.root); got != c.file {
			t.Errorf("ProjectFile(%q) = %q, want %q", c.root, got, c.file)
		}
	}
}

// TestCategoryForFileProjectsDir locks the directory-backed category: any
// projects/<slug>.md maps to the projects category (so tier/ACL/organize gates
// treat it like projects.md), while deeper nesting and other dirs do not.
func TestCategoryForFileProjectsDir(t *testing.T) {
	c, ok := CategoryForFile("projects/auxly.md")
	if !ok || c.Slug != "projects" {
		t.Fatalf("projects/auxly.md not recognized: %+v %v", c, ok)
	}
	if IsPersonalFile("projects/auxly.md") {
		t.Fatalf("project sub-file must be shared tier")
	}
	if !IsOrganizableFile("projects/auxly.md") {
		t.Fatalf("project sub-file must be organizable")
	}
	if _, ok := CategoryForFile("projects/deep/nest.md"); ok {
		t.Fatalf("nested paths must not be a category")
	}
	if _, ok := CategoryForFile("secrets/auxly.md"); ok {
		t.Fatalf("other directories must not be a category")
	}
}

// TestListIncludesProjectSubfiles locks the vault enumeration: projects/*.md
// appear in List under their slash names (feeding recall, unified compile,
// organize), and a workspace sub-file overrides the global one of the same name.
func TestListIncludesProjectSubfiles(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "identity.md"), []byte("# I\n"), 0644)
	os.WriteFile(filepath.Join(root, "projects", "alpha.md"), []byte("# A global\n"), 0644)
	os.MkdirAll(filepath.Join(ws, "projects"), 0755)
	os.WriteFile(filepath.Join(ws, "projects", "alpha.md"), []byte("# A ws\n"), 0644)

	s := &Store{Root: root, WorkspaceRoot: ws}
	files, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]FileInfo{}
	for _, f := range files {
		byName[f.Name] = f
	}
	sub, ok := byName["projects/alpha.md"]
	if !ok {
		t.Fatalf("projects/alpha.md missing from List: %+v", files)
	}
	if sub.Path != filepath.Join(ws, "projects", "alpha.md") {
		t.Fatalf("workspace sub-file should override global, got %s", sub.Path)
	}
	if content, err := s.View("projects/alpha.md"); err != nil || content != "# A ws\n" {
		t.Fatalf("View sub-file: %q %v", content, err)
	}

	// Writing to a fresh sub-file must create projects/ on demand (both scopes).
	if err := s.WriteScoped("projects/beta.md", "# B\n", "global"); err != nil {
		t.Fatalf("global sub-file write: %v", err)
	}
	if err := s.WriteScoped("projects/gamma.md", "# G\n", "workspace"); err != nil {
		t.Fatalf("workspace sub-file write: %v", err)
	}
}
