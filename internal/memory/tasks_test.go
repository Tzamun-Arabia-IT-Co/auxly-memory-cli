package memory

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 6, 12, 0, 0, 0, time.Local)

func TestParseTasks_MixedAndHandTyped(t *testing.T) {
	content := `# Tasks

- [ ] fix the ollama timeout — added 2026-07-06 by claude
- [x] ship v1.3.9 — added 2026-07-05 by wael, done 2026-07-06
- [ ] bare hand-typed task
* [X] star bullet done
not a task line
`
	got := ParseTasks(content)
	if len(got) != 4 {
		t.Fatalf("want 4 tasks, got %d: %+v", len(got), got)
	}

	if got[0].Done || got[0].Text != "fix the ollama timeout" || got[0].By != "claude" || got[0].Added != "2026-07-06" {
		t.Errorf("task0 parsed wrong: %+v", got[0])
	}
	if !got[1].Done || got[1].Text != "ship v1.3.9" || got[1].Completed != "2026-07-06" || got[1].By != "wael" {
		t.Errorf("task1 parsed wrong: %+v", got[1])
	}
	if got[2].Done || got[2].Text != "bare hand-typed task" || got[2].By != "" || got[2].Added != "" {
		t.Errorf("hand-typed task should parse with empty meta: %+v", got[2])
	}
	if !got[3].Done || got[3].Text != "star bullet done" {
		t.Errorf("star bullet not parsed: %+v", got[3])
	}
}

func TestStore_AddTask_CreatesAndStamps(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	tk, err := s.AddTask("fix the ollama timeout", "claude", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Text != "fix the ollama timeout" || tk.By != "claude" || tk.Added != "2026-07-06" {
		t.Errorf("returned task wrong: %+v", tk)
	}

	content, _ := s.readTasksContent()
	if !strings.HasPrefix(content, "# Tasks\n") {
		t.Errorf("missing header: %q", content)
	}
	if !strings.Contains(content, "- [ ] fix the ollama timeout — added 2026-07-06 by claude\n") {
		t.Errorf("task line wrong: %q", content)
	}
}

func TestStore_AddTask_AppendsMultiple(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.AddTask("first", "cli", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTask("second", "cli", testNow); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.ListTasks()
	if len(tasks) != 2 || tasks[0].Text != "first" || tasks[1].Text != "second" {
		t.Errorf("append order wrong: %+v", tasks)
	}
}

func TestStore_CompleteTask_ByNumber(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.AddTask("alpha", "cli", testNow)
	s.AddTask("beta", "cli", testNow)

	done, err := s.CompleteTask("2", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if done.Text != "beta" {
		t.Errorf("completed wrong task: %+v", done)
	}

	tasks, _ := s.ListTasks()
	if tasks[0].Done || !tasks[1].Done {
		t.Errorf("wrong task marked done: %+v", tasks)
	}
	if tasks[1].Completed != "2026-07-06" {
		t.Errorf("done date not stamped: %+v", tasks[1])
	}
}

func TestStore_CompleteTask_BySubstring(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.AddTask("fix the ollama timeout", "cli", testNow)
	s.AddTask("ship the release", "cli", testNow)

	done, err := s.CompleteTask("OLLAMA", testNow) // case-insensitive
	if err != nil {
		t.Fatal(err)
	}
	if done.Text != "fix the ollama timeout" {
		t.Errorf("substring matched wrong task: %+v", done)
	}
}

func TestStore_CompleteTask_AmbiguousErrors(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.AddTask("deploy the api", "cli", testNow)
	s.AddTask("deploy the web", "cli", testNow)

	_, err := s.CompleteTask("deploy", testNow)
	if err == nil || !strings.Contains(err.Error(), "matches 2 open tasks") {
		t.Fatalf("want ambiguity error, got %v", err)
	}
}

func TestStore_CompleteTask_NumberOnlyCountsOpen(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.AddTask("one", "cli", testNow)
	s.AddTask("two", "cli", testNow)
	s.AddTask("three", "cli", testNow)
	s.CompleteTask("1", testNow) // complete "one"

	// Now open tasks are two(#1), three(#2). #2 must be "three", not "one".
	done, err := s.CompleteTask("2", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if done.Text != "three" {
		t.Errorf("open-index numbering wrong after a completion: %+v", done)
	}
}

func TestStore_CompleteTask_NoMatch(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.AddTask("only task", "cli", testNow)

	if _, err := s.CompleteTask("nonexistent", testNow); err == nil {
		t.Fatal("want no-match error")
	}
	if _, err := s.CompleteTask("5", testNow); err == nil {
		t.Fatal("want out-of-range error")
	}
}

func TestStore_ListTasks_EmptyVault(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("want no tasks, got %d", len(tasks))
	}
}

func TestTaxonomy_InboxTasksOperational(t *testing.T) {
	// inbox is swept by organize; tasks is not; both are non-fact-routing.
	if !IsOrganizableFile("inbox.md") {
		t.Error("inbox.md must be organizable (organize re-files its entries)")
	}
	if IsOrganizableFile("tasks.md") {
		t.Error("tasks.md must NOT be organizable")
	}

	// Neither appears in the unscoped fact-routing guide.
	guide := RenderForPrompt()
	if strings.Contains(guide, "inbox.md") || strings.Contains(guide, "tasks.md") {
		t.Errorf("operational files leaked into the fact-routing guide:\n%s", guide)
	}
	// But a real category still routes normally.
	if !strings.Contains(guide, "infra.md") {
		t.Error("guide lost a normal category")
	}

	// Not part of the harvest/profile order.
	for _, f := range OrderedFiles() {
		if f == "inbox.md" || f == "tasks.md" {
			t.Errorf("operational file %s must not be in OrderedFiles()", f)
		}
	}
}
