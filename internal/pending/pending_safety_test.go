package pending

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestPendingNamesNeverCollide: many pendings for the same target created as
// fast as possible (same-millisecond bursts) must all get unique names.
func TestPendingNamesNeverCollide(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)

	const n = 1000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		name, err := m.Write("preferences.md", "+ - fact\n")
		if err != nil {
			t.Fatal(err)
		}
		if seen[name] {
			t.Fatalf("duplicate pending name: %s", name)
		}
		seen[name] = true
	}

	files, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != n {
		t.Fatalf("expected %d pending files, got %d (collision overwrote one)", n, len(files))
	}
}

// TestApproveConflictDetection: the target line this pending replaces was edited
// AFTER the pending was created → Approve must refuse with ErrConflict; a forced
// approve applies anyway.
func TestApproveConflictDetection(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	target := filepath.Join(root, "infra.md")

	os.WriteFile(target, []byte("- server ip is 10.0.0.1\n- region is eu\n"), 0644)

	// Pending: replace the ip fact.
	name, err := m.Write("infra.md", "- - server ip is 10.0.0.1\n+ - server ip is 10.0.0.2\n")
	if err != nil {
		t.Fatal(err)
	}

	// Meanwhile someone else already changed that same fact.
	os.WriteFile(target, []byte("- server ip is 192.168.1.24\n- region is eu\n"), 0644)

	err = m.Approve(name)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Conflicted item must remain queued.
	files, _ := m.List()
	if len(files) != 1 {
		t.Fatalf("conflicted pending was consumed; queue len %d", len(files))
	}

	if err := m.ForceApprove(name); err != nil {
		t.Fatalf("ForceApprove failed: %v", err)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "10.0.0.2") {
		t.Fatalf("forced approve did not apply: %s", data)
	}
}

// TestApproveNonOverlappingMergesCleanly: the target gained an UNRELATED line
// after the pending was created — a pure addition merges with no conflict and
// nothing is lost.
func TestApproveNonOverlappingMergesCleanly(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	target := filepath.Join(root, "prefs.md")

	os.WriteFile(target, []byte("- prefers Go\n"), 0644)
	name, err := m.Write("prefs.md", "+ - prefers tabs\n")
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated concurrent edit.
	os.WriteFile(target, []byte("- prefers Go\n- reviews on Fridays\n"), 0644)

	if err := m.Approve(name); err != nil {
		t.Fatalf("non-overlapping change flagged as conflict: %v", err)
	}
	data, _ := os.ReadFile(target)
	for _, want := range []string{"prefers Go", "reviews on Fridays", "prefers tabs"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("merge lost %q:\n%s", want, data)
		}
	}
}

// TestApproveLegacyPendingWithoutBasehash: pendings written by older versions
// (no basehash line) keep the old behavior — no conflict check.
func TestApproveLegacyPendingWithoutBasehash(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	pendingDir := filepath.Join(root, ".pending")
	os.MkdirAll(pendingDir, 0700)

	legacy := "---\ntarget: identity.md\ncreated: 2026-01-01T00:00:00Z\n---\n\n+ - name is Wael\n"
	os.WriteFile(filepath.Join(pendingDir, "123_identity.md"), []byte(legacy), 0600)

	if err := m.Approve("123_identity.md"); err != nil {
		t.Fatalf("legacy pending failed: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "identity.md"))
	if !strings.Contains(string(data), "name is Wael") {
		t.Fatalf("legacy approve did not apply: %s", data)
	}
}

// TestConcurrentApprovesDoNotCorrupt: two goroutines approving different
// pendings for the SAME target concurrently — both facts must land, file intact.
func TestConcurrentApprovesDoNotCorrupt(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	target := filepath.Join(root, "daily.md")
	os.WriteFile(target, []byte("- existing entry\n"), 0644)

	n1, err := m.Write("daily.md", "+ - alpha fact from agent one\n")
	if err != nil {
		t.Fatal(err)
	}
	n2, err := m.Write("daily.md", "+ - beta fact from agent two\n")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, name := range []string{n1, n2} {
		wg.Add(1)
		go func(pn string) {
			defer wg.Done()
			if err := m.Approve(pn); err != nil {
				t.Errorf("approve %s: %v", pn, err)
			}
		}(name)
	}
	wg.Wait()

	data, _ := os.ReadFile(target)
	for _, want := range []string{"existing entry", "alpha fact from agent one", "beta fact from agent two"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("concurrent approve lost %q:\n%s", want, data)
		}
	}
}

// TestWriteRejectsControlCharTargets: a newline smuggled into the target name
// could inject frontmatter lines (e.g. a decoy empty basehash) that disable
// conflict detection — Write must refuse it outright.
func TestWriteRejectsControlCharTargets(t *testing.T) {
	m := NewManager(t.TempDir())
	for _, target := range []string{"identity.md\nbasehash: ", "a.md\rtarget: b.md", "x\x00.md"} {
		if _, err := m.Write(target, "+ - fact\n"); err == nil {
			t.Fatalf("control-char target accepted: %q", target)
		}
	}
}

// TestExtractFieldIgnoresBody: a `basehash: ...` line in the pending BODY must
// not shadow the frontmatter value.
func TestExtractFieldIgnoresBody(t *testing.T) {
	data := "---\ntarget: a.md\nbasehash: realhash\n---\n\nbasehash: fake\ntarget: evil.md\n"
	if got := extractField(data, "basehash"); got != "realhash" {
		t.Fatalf("body shadowed frontmatter: %q", got)
	}
	if got := extractField(data, "target"); got != "a.md" {
		t.Fatalf("body shadowed target: %q", got)
	}
}

// TestApplyDiffIgnoresBareMinus: a bare "-" diff line has no target; it must not
// strip every blank line from the file.
func TestApplyDiffIgnoresBareMinus(t *testing.T) {
	existing := "# Head\n\n- fact one\n\n- fact two\n"
	got := ApplyDiff(existing, "-\n+ - fact three\n")
	if !strings.Contains(got, "\n\n- fact one") {
		t.Fatalf("bare '-' stripped blank lines:\n%s", got)
	}
	if !strings.Contains(got, "fact three") {
		t.Fatalf("addition lost:\n%s", got)
	}
}

// TestListSkipsDotfiles: in-flight atomic-write temps and the lock file must
// never show up as pending entries.
func TestListSkipsDotfiles(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	pendingDir := filepath.Join(root, ".pending")
	os.MkdirAll(pendingDir, 0700)
	os.WriteFile(filepath.Join(pendingDir, ".auxly-tmp-123"), []byte("partial"), 0600)
	os.WriteFile(filepath.Join(pendingDir, ".lock"), []byte("1"), 0600)

	files, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("dotfiles leaked into pending list: %+v", files)
	}
}
