package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var todoCmd = &cobra.Command{
	Use:     "todo [text... | done <n|text>]",
	Aliases: []string{"task", "tasks"},
	Short:   "Shared todo list — you and your agents read/write tasks.md",
	Long: `todo is a shared task list backed by tasks.md in the vault. It git-syncs
like any memory file, and agents can add/see tasks over MCP.

  auxly todo                       list open tasks (done shown below)
  auxly todo fix the ollama timeout   add a task
  auxly todo done 2                mark open task #2 complete
  auxly todo done ollama           mark the task matching "ollama" complete

Use '--' before task text that starts with the word "done":
  auxly todo -- done reviewing the PR`,
	SilenceUsage: true,
	RunE:         runTodo,
}

func init() {
	rootCmd.AddCommand(todoCmd)
}

func runTodo(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()
	if _, err := os.Stat(memPath); err != nil {
		return fmt.Errorf("vault not found at %s — run 'auxly setup' first", memPath)
	}
	store := memory.NewStore(memPath)

	switch {
	case len(args) == 0:
		return listTodos(store)
	case args[0] == "done":
		return doneTodo(store, memPath, strings.TrimSpace(strings.Join(args[1:], " ")))
	default:
		return addTodo(store, memPath, strings.Join(args, " "))
	}
}

func listTodos(store *memory.Store) error {
	tasks, err := store.ListTasks()
	if err != nil {
		return err
	}
	var open, done []memory.Task
	for _, t := range tasks {
		if t.Done {
			done = append(done, t)
		} else {
			open = append(open, t)
		}
	}

	if len(open) == 0 && len(done) == 0 {
		fmt.Println("No tasks yet. Add one: auxly todo <text>")
		return nil
	}

	fmt.Printf("📋 Tasks — %d open, %d done\n\n", len(open), len(done))
	for i, t := range open {
		fmt.Printf("  %2d. [ ] %s%s\n", i+1, t.Text, taskMeta(t))
	}
	if len(done) > 0 {
		fmt.Println()
		for _, t := range done {
			fmt.Printf("      [x] %s%s\n", t.Text, taskMeta(t))
		}
	}
	return nil
}

func addTodo(store *memory.Store, memPath, text string) error {
	t, err := store.AddTask(text, "cli", time.Now())
	if err != nil {
		return err
	}
	logTaskEvent(memPath, "task_add", t.Text)
	fmt.Printf("✅ Added: %s\n", t.Text)
	return nil
}

func doneTodo(store *memory.Store, memPath, match string) error {
	if match == "" {
		return fmt.Errorf("which task? usage: auxly todo done <number|text>")
	}
	t, err := store.CompleteTask(match, time.Now())
	if err != nil {
		return err
	}
	logTaskEvent(memPath, "task_done", t.Text)
	fmt.Printf("✅ Done: %s\n", t.Text)
	return nil
}

// taskMeta renders the dim "(by X · added Y)" suffix when metadata is present.
func taskMeta(t memory.Task) string {
	var parts []string
	if t.By != "" {
		parts = append(parts, "by "+t.By)
	}
	if t.Completed != "" {
		parts = append(parts, "done "+t.Completed)
	} else if t.Added != "" {
		parts = append(parts, "added "+t.Added)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, " · ") + ")"
}

func logTaskEvent(memPath, action, text string) {
	if logger, err := audit.NewLogger(memPath); err == nil {
		_, _ = logger.Log("cli", "cli-user", action, memory.TasksFile, text, "task", "auto")
		logger.Close()
	}
}
