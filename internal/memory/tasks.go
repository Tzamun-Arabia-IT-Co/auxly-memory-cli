package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// TasksFile is the shared todo list — a plain checkbox list any agent and the
// user read/write. It is a taxonomy category (ACL + dashboard) but Operational,
// so the organize pass never reshuffles it.
const TasksFile = "tasks.md"

const tasksDateFmt = "2006-01-02"

// Task is one checkbox line. Meta fields are best-effort: a hand-typed
// `- [ ] thing` parses fine with By/Added empty.
type Task struct {
	Text      string // task text, metadata suffix stripped
	Done      bool
	By        string // author, "" if unknown
	Added     string // YYYY-MM-DD, "" if unknown
	Completed string // YYYY-MM-DD, "" if not done/unknown
	raw       string // original line, verbatim
}

// taskLineRe matches a markdown checkbox item; group 2 is the box, group 3 the body.
var taskLineRe = regexp.MustCompile(`^\s*[-*] \[([ xX])\] (.*)$`)

// metaRe pulls the trailing "— added <date> by <who>" / ", done <date>" that
// AddTask/CompleteTask stamp. Absent on hand-typed lines (they keep full Text).
var (
	addedRe = regexp.MustCompile(`\s+[—-]\s+added (\d{4}-\d{2}-\d{2})(?:\s+by\s+(\S+))?`)
	doneRe  = regexp.MustCompile(`,\s+done (\d{4}-\d{2}-\d{2})`)
)

// ParseTasks extracts every checkbox line from tasks.md content, in file order.
func ParseTasks(content string) []Task {
	var out []Task
	for _, ln := range strings.Split(content, "\n") {
		m := taskLineRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		t := Task{Done: m[1] != " ", raw: ln}
		body := m[2]
		if dm := doneRe.FindStringSubmatch(body); dm != nil {
			t.Completed = dm[1]
			body = doneRe.ReplaceAllString(body, "")
		}
		if am := addedRe.FindStringSubmatch(body); am != nil {
			t.Added, t.By = am[1], am[2]
			body = addedRe.ReplaceAllString(body, "")
		}
		t.Text = strings.TrimSpace(body)
		out = append(out, t)
	}
	return out
}

// ListTasks returns every task in tasks.md (open and done), file order.
func (s *Store) ListTasks() ([]Task, error) {
	content, err := s.readTasksContent()
	if err != nil {
		return nil, err
	}
	return ParseTasks(content), nil
}

// AddTask appends one open task and returns it. Creates tasks.md (with header)
// when missing; preserves the file's encryption state.
func (s *Store) AddTask(text, by string, now time.Time) (Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Task{}, fmt.Errorf("empty task text")
	}
	if strings.ContainsAny(text, "\r\n") {
		text = strings.Join(strings.Fields(text), " ") // tasks are single-line
	}

	line := fmt.Sprintf("- [ ] %s — added %s", text, now.Format(tasksDateFmt))
	if by = strings.TrimSpace(by); by != "" {
		line += " by " + by
	}

	content, encrypted, err := s.readTasksRaw()
	if err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(content) == "" {
		content = "# Tasks\n"
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += line + "\n"

	if err := s.writeTasksContent(content, encrypted); err != nil {
		return Task{}, err
	}
	return Task{Text: text, By: by, Added: now.Format(tasksDateFmt)}, nil
}

// CompleteTask marks the open task identified by match as done. match is either
// a 1-based index into the OPEN tasks (as ListOpenTasks orders them) or a
// case-insensitive substring that uniquely identifies one open task.
func (s *Store) CompleteTask(match string, now time.Time) (Task, error) {
	content, encrypted, err := s.readTasksRaw()
	if err != nil {
		return Task{}, err
	}
	newContent, done, err := completeTaskInContent(content, match, now)
	if err != nil {
		return Task{}, err
	}
	if err := s.writeTasksContent(newContent, encrypted); err != nil {
		return Task{}, err
	}
	return done, nil
}

// completeTaskInContent is the pure core of CompleteTask: given file content and
// a match, it flips the resolved open task to [x] and stamps the done date.
func completeTaskInContent(content, match string, now time.Time) (string, Task, error) {
	lines := strings.Split(content, "\n")

	// Collect line indices of OPEN tasks, in order.
	var openIdx []int
	var openTasks []Task
	for i, ln := range lines {
		m := taskLineRe.FindStringSubmatch(ln)
		if m == nil || m[1] != " " {
			continue
		}
		openIdx = append(openIdx, i)
		openTasks = append(openTasks, ParseTasks(ln)[0])
	}
	if len(openIdx) == 0 {
		return "", Task{}, fmt.Errorf("no open tasks to complete")
	}

	target, err := resolveTaskMatch(match, openIdx, openTasks)
	if err != nil {
		return "", Task{}, err
	}

	lines[target] = markLineDone(lines[target], now)
	return strings.Join(lines, "\n"), ParseTasks(lines[target])[0], nil
}

// resolveTaskMatch turns match (index or substring) into the target line index.
func resolveTaskMatch(match string, openIdx []int, openTasks []Task) (int, error) {
	match = strings.TrimSpace(match)
	if match == "" {
		return 0, fmt.Errorf("specify a task number or text to complete")
	}

	// Numeric match: 1-based index into open tasks.
	if n, perr := parsePositiveInt(match); perr == nil {
		if n < 1 || n > len(openIdx) {
			return 0, fmt.Errorf("no open task #%d (there are %d)", n, len(openIdx))
		}
		return openIdx[n-1], nil
	}

	// Substring match: must hit exactly one open task.
	var hits []int
	q := strings.ToLower(match)
	for i, t := range openTasks {
		if strings.Contains(strings.ToLower(t.Text), q) {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 0:
		return 0, fmt.Errorf("no open task matches %q", match)
	case 1:
		return openIdx[hits[0]], nil
	default:
		var names []string
		for _, h := range hits {
			names = append(names, fmt.Sprintf("%q", openTasks[h].Text))
		}
		return 0, fmt.Errorf("%q matches %d open tasks: %s — be more specific", match, len(hits), strings.Join(names, ", "))
	}
}

// markLineDone flips a checkbox line to [x] and appends ", done <date>".
func markLineDone(line string, now time.Time) string {
	line = strings.Replace(line, "[ ]", "[x]", 1)
	return strings.TrimRight(line, " \t") + fmt.Sprintf(", done %s", now.Format(tasksDateFmt))
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// --- storage plumbing (encryption-aware, mirrors the inbox path) ---

func (s *Store) tasksPath() string { return filepath.Join(s.Root, TasksFile) }

// readTasksRaw returns tasks.md content (decrypted) plus whether it is
// encrypted on disk; a missing file is ("", false, nil).
func (s *Store) readTasksRaw() (content string, encrypted bool, err error) {
	data, enc, rerr := s.readVaultFile(s.tasksPath())
	if os.IsNotExist(rerr) {
		return "", false, nil
	}
	if rerr != nil {
		return "", false, rerr
	}
	return string(data), enc, nil
}

// readTasksContent is readTasksRaw without the encryption flag.
func (s *Store) readTasksContent() (string, error) {
	c, _, err := s.readTasksRaw()
	return c, err
}

func (s *Store) writeTasksContent(content string, encrypted bool) error {
	return s.writeVaultFile(s.tasksPath(), []byte(content), 0o600, encrypted)
}
