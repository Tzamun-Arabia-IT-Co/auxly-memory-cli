package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/spf13/cobra"
)

var noteCmd = &cobra.Command{
	Use:     "note [text...]",
	Aliases: []string{"q"},
	Short:   "Quick-capture a thought into the vault inbox",
	Long: `note appends a timestamped entry to inbox.md in the vault — no LLM,
no category decision, works offline. File entries later: the Consolidate
organize pass sweeps inbox.md into the proper category files.

With no arguments, reads the note from piped stdin (multi-line ok):

  auxly q fix ollama timeout tomorrow
  pbpaste | auxly note`,
	SilenceUsage: true,
	RunE:         runNote,
}

func init() {
	rootCmd.AddCommand(noteCmd)
}

const inboxFile = "inbox.md"

func runNote(cmd *cobra.Command, args []string) error {
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "" {
		// No args: read stdin only when piped — an interactive bare
		// `auxly note` should error out, never hang on a TTY.
		if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice == 0 {
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			text = strings.TrimSpace(string(raw))
		}
	}
	if text == "" {
		return errors.New("nothing to capture — usage: auxly q <text> (or pipe text in)")
	}

	memPath := getMemoryPath()
	if _, err := os.Stat(memPath); err != nil {
		return fmt.Errorf("vault not found at %s — run 'auxly setup' first", memPath)
	}

	entry := formatInboxEntry(text, time.Now())
	if err := appendInboxEntry(memory.NewStore(memPath), memPath, entry); err != nil {
		return err
	}

	if logger, err := audit.NewLogger(memPath); err == nil {
		_, _ = logger.Log("cli", "cli-user", "note", inboxFile, entry, "quick capture", "auto")
		logger.Close()
	}

	fmt.Println("📥 Captured to inbox.md — the next organize run files it.")
	return nil
}

// appendInboxEntry appends entry to inbox.md through the encryption-aware
// vault path: an encrypted inbox stays encrypted, a missing one is created
// plaintext with the header.
func appendInboxEntry(store *memory.Store, memPath, entry string) error {
	path := filepath.Join(memPath, inboxFile)
	data, encrypted, err := store.ReadVaultFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, encrypted, err = []byte("# Inbox\n"), false, nil
	}
	if err != nil {
		return err
	}

	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += entry

	return store.WriteVaultFile(path, []byte(content), 0o600, encrypted)
}

// formatInboxEntry renders one timestamped bullet; extra lines are indented
// under the bullet so a multi-line note stays a single list item.
func formatInboxEntry(text string, now time.Time) string {
	lines := strings.Split(text, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s] %s\n", now.Format("2006-01-02 15:04"), strings.TrimSpace(lines[0]))
	for _, l := range lines[1:] {
		fmt.Fprintf(&b, "  %s\n", strings.TrimRight(l, " \t"))
	}
	return b.String()
}
