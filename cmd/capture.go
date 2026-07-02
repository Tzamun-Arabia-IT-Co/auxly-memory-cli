package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var (
	captureTranscript string
	captureStopHook   bool
	captureProvider   string
)

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Extract durable facts from a session transcript into the pending queue",
	Long: `capture reads a session transcript (Claude Code JSONL via --transcript or
--stop-hook, plain text via stdin), extracts durable facts with the configured
LLM, dedups them against the vault, and queues them as PENDING changes — it
never writes memory directly. Designed to run fire-and-forget from an agent
Stop hook (see 'auxly hooks install').`,
	SilenceUsage: true,
	RunE:         runCapture,
}

func init() {
	captureCmd.Flags().StringVar(&captureTranscript, "transcript", "", "path to a Claude Code transcript (.jsonl)")
	captureCmd.Flags().BoolVar(&captureStopHook, "stop-hook", false, "read the Claude Code Stop-hook JSON from stdin (extracts transcript_path itself)")
	captureCmd.Flags().StringVar(&captureProvider, "provider", "claude-code", "provider attribution for the queued facts")
	rootCmd.AddCommand(captureCmd)
}

// captureMinChars skips trivial sessions (~2K tokens) — nothing durable there.
const captureMinChars = 8_000

// captureCooldown throttles back-to-back hook fires (agents can stop often).
const captureCooldown = 10 * time.Minute

func runCapture(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()

	// Throttle FIRST — the marker costs nothing, the LLM call doesn't.
	marker := filepath.Join(memPath, ".capture-last")
	if st, err := os.Stat(marker); err == nil && time.Since(st.ModTime()) < captureCooldown {
		return nil // silent: hook-driven, cooldown active
	}

	transcript, err := captureInput()
	if err != nil {
		return err
	}
	if len(transcript) < captureMinChars {
		return nil // too small to teach anything durable — silent no-op
	}
	_ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0600)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	facts, err := memory.ExtractCaptureFacts(ctx, transcript)
	if err != nil {
		// Fire-and-forget contract: an unreachable model must never fail a
		// session teardown. Named on stderr, exit 0.
		fmt.Fprintf(os.Stderr, "auxly capture: %v\n", err)
		return nil
	}
	store := memory.NewStore(memPath)
	facts = store.DedupCaptureFacts(facts)
	if len(facts) == 0 {
		return nil
	}

	mgr := pending.NewManager(memPath)
	logger, lerr := audit.NewLogger(memPath)
	if lerr == nil {
		defer logger.Close()
	}
	date := time.Now().Format("2006-01-02")
	queued := 0
	for _, f := range facts {
		file := memory.FileForCategory(f.Category)
		if f.Category == "projects" {
			file = memory.ProjectFile(store.WorkspaceRoot)
		}
		diff := fmt.Sprintf("+ - [%s] %s\n", date, f.Fact)
		name, werr := mgr.WriteFrom(file, diff, "capture:"+captureProvider)
		if werr != nil {
			continue
		}
		queued++
		if lerr == nil {
			logger.Log("capture", captureProvider, "write", file, diff, "auto-captured from session transcript", "require_approval")
		}
		_ = name
	}
	if queued > 0 {
		fmt.Printf("🪄 %d fact(s) captured → pending review (`auxly pending`)\n", queued)
	}
	return nil
}

// captureInput assembles the transcript text from whichever source was given.
func captureInput() (string, error) {
	switch {
	case captureStopHook:
		// Claude Code Stop hook: JSON on stdin carrying transcript_path.
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return "", err
		}
		var hook struct {
			TranscriptPath string `json:"transcript_path"`
		}
		if err := json.Unmarshal(data, &hook); err != nil || hook.TranscriptPath == "" {
			return "", nil // malformed hook payload — silent no-op
		}
		return readTranscriptJSONL(hook.TranscriptPath)
	case captureTranscript != "":
		return readTranscriptJSONL(captureTranscript)
	default:
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		return string(data), err
	}
}

// readTranscriptJSONL extracts human-relevant text (user + assistant turns)
// from a Claude Code transcript, skipping tool dumps — those are code and
// command noise, not knowledge about the user.
func readTranscriptJSONL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil // transcript already cleaned up — silent no-op
	}
	var b strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}
		// Content is either a plain string or a block array; keep text blocks only.
		var asString string
		if json.Unmarshal(entry.Message.Content, &asString) == nil {
			b.WriteString(entry.Message.Role + ": " + asString + "\n")
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) == nil {
			for _, blk := range blocks {
				if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
					b.WriteString(entry.Message.Role + ": " + blk.Text + "\n")
				}
			}
		}
	}
	return b.String(), nil
}
