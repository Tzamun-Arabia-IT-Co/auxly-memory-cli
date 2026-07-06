package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

func TestFormatInboxEntry_SingleLine(t *testing.T) {
	ts := time.Date(2026, 7, 6, 14, 32, 0, 0, time.Local)
	got := formatInboxEntry("fix ollama timeout tomorrow", ts)
	want := "- [2026-07-06 14:32] fix ollama timeout tomorrow\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatInboxEntry_MultiLine(t *testing.T) {
	ts := time.Date(2026, 7, 6, 9, 5, 0, 0, time.Local)
	got := formatInboxEntry("first line\nsecond line\nthird", ts)
	want := "- [2026-07-06 09:05] first line\n  second line\n  third\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppendInboxEntry_CreatesWithHeader(t *testing.T) {
	dir := t.TempDir()
	store := memory.NewStore(dir)

	if err := appendInboxEntry(store, dir, "- [2026-07-06 14:32] hello\n"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, inboxFile))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Inbox\n- [2026-07-06 14:32] hello\n"
	if string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}
}

func TestAppendInboxEntry_AppendsPreservingContent(t *testing.T) {
	dir := t.TempDir()
	store := memory.NewStore(dir)
	existing := "# Inbox\n- [2026-07-05 10:00] old note\n"
	if err := os.WriteFile(filepath.Join(dir, inboxFile), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := appendInboxEntry(store, dir, "- [2026-07-06 14:32] new note\n"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, inboxFile))
	want := existing + "- [2026-07-06 14:32] new note\n"
	if string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}
}

func TestAppendInboxEntry_AddsNewlineBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	store := memory.NewStore(dir)
	// File missing trailing newline — append must not glue onto the last line.
	if err := os.WriteFile(filepath.Join(dir, inboxFile), []byte("# Inbox\n- old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := appendInboxEntry(store, dir, "- new\n"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, inboxFile))
	if string(data) != "# Inbox\n- old\n- new\n" {
		t.Errorf("got %q", string(data))
	}
}

func TestRunNote_ArgsMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUXLY_MEMORY_PATH", dir)

	if err := runNote(nil, []string{"remember", "the", "milk"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, inboxFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "] remember the milk\n") {
		t.Errorf("inbox missing joined-args note: %q", string(data))
	}
	if !strings.HasPrefix(string(data), "# Inbox\n") {
		t.Errorf("inbox missing header: %q", string(data))
	}
}

func TestRunNote_EmptyErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUXLY_MEMORY_PATH", dir)

	// stdin is not a pipe under `go test`? Force one: empty pipe so the
	// piped-stdin branch also yields empty text.
	r, w, _ := os.Pipe()
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if err := runNote(nil, nil); err == nil {
		t.Fatal("want error on empty input, got nil")
	}
}

func TestRunNote_StdinMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUXLY_MEMORY_PATH", dir)

	r, w, _ := os.Pipe()
	if _, err := w.WriteString("piped thought\nwith detail\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if err := runNote(nil, nil); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, inboxFile))
	if !strings.Contains(string(data), "] piped thought\n  with detail\n") {
		t.Errorf("inbox missing multi-line stdin note: %q", string(data))
	}
}

func TestRunNote_VaultMissing(t *testing.T) {
	t.Setenv("AUXLY_MEMORY_PATH", filepath.Join(t.TempDir(), "does-not-exist"))

	err := runNote(nil, []string{"hello"})
	if err == nil || !strings.Contains(err.Error(), "vault not found") {
		t.Fatalf("want vault-not-found error, got %v", err)
	}
}
