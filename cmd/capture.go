package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/spf13/cobra"
)

var (
	captureTranscript      string
	captureStopHook        bool
	captureProvider        string
	captureCodexNotifyFlag bool
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
	captureCmd.Flags().BoolVar(&captureCodexNotifyFlag, "codex-notify", false, "read a Codex notify-hook JSON payload (arg or stdin) and capture the session (wired by `hooks install --agent codex`)")
	rootCmd.AddCommand(captureCmd)
}

// captureMinChars skips trivial sessions (~2K tokens) — nothing durable there.
const captureMinChars = 8_000

// captureCooldown throttles back-to-back hook fires (agents can stop often).
const captureCooldown = 10 * time.Minute

// captureMarkerPath is the throttle marker's path for a given vault — shared
// so every capture entry point (Stop-hook, codex notify, shell wrapper) reads
// and writes the exact same file.
func captureMarkerPath(memPath string) string {
	return filepath.Join(memPath, ".capture-last")
}

// inCaptureCooldown reports whether memPath's throttle marker is still fresh.
// Every capture entry point checks this FIRST, before doing any real work —
// walking a session tree, reading a transcript, calling an LLM.
func inCaptureCooldown(memPath string) bool {
	st, err := os.Stat(captureMarkerPath(memPath))
	return err == nil && time.Since(st.ModTime()) < captureCooldown
}

func runCapture(cmd *cobra.Command, args []string) error {
	// codex's notify hook has its own full pipeline (cooldown, transcript
	// resolution, extraction) — route to it before any other mode, and
	// before touching memPath/the marker below (runCodexNotify does its own).
	if captureCodexNotifyFlag {
		return captureCodexNotify()
	}

	memPath := getMemoryPath()

	// Throttle FIRST — the marker costs nothing, the LLM call doesn't.
	if inCaptureCooldown(memPath) {
		return nil // silent: hook-driven, cooldown active
	}

	transcript, err := captureInput()
	if err != nil {
		return err
	}
	if len(transcript) < captureMinChars {
		return nil // too small to teach anything durable — silent no-op
	}
	_ = os.WriteFile(captureMarkerPath(memPath), []byte(time.Now().Format(time.RFC3339)), 0600)

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
// command noise, not knowledge about the user. Falls back to the raw file
// content when no line parses as JSON at all — a `script`-captured shell
// session (gemini/kimi wrapper) is plain text, not JSONL, and the whole file
// IS the session text in that case.
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
	// ponytail: nothing extracted (whether the file had zero valid JSON
	// lines, or was valid JSON that just never matched a user/assistant
	// turn — e.g. one incidental `{}` in a script(1)-captured plain-text
	// session) means there's no structured transcript to trust — fall back
	// to the raw file content. A lone JSON log line must not veto an
	// otherwise plain-text transcript; captureMinChars still gates junk.
	if b.Len() == 0 {
		return ansiEscapeRE.ReplaceAllString(string(data), ""), nil
	}
	return b.String(), nil
}

// ansiEscapeRE strips terminal color/cursor-control sequences — CSI
// (`\x1b[...<letter>`) and OSC (`\x1b]...<BEL>`) — that a `script`-captured
// raw transcript carries. Only applied to the raw fallback above: noise for
// an LLM doing fact extraction, not signal.
var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07`)
