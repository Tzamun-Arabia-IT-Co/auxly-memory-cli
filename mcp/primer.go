package mcp

import (
	"os"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

// primerMaxChars caps the session primer at ~800 tokens so grounding a session
// never crowds out the agent's working context.
const primerMaxChars = 3200

// sessionPrimer builds the compact auto-recall block appended to skill_init /
// skill_learn: who the user is, their top preferences, the CURRENT workspace's
// project facts, and the last week's journal — the four slices an agent needs
// in its first minute. Every file is ACL-gated (a remote peer's primer only
// carries what it may read). AUXLY_PRIMER=off disables. Empty vault → "".
func (s *Server) sessionPrimer() string {
	if os.Getenv("AUXLY_PRIMER") == "off" {
		return ""
	}

	var b strings.Builder
	slice := func(file, label string, max int, keep func(string) bool) {
		if !s.canRead(file) {
			return
		}
		content, err := s.store.View(file)
		if err != nil {
			return
		}
		var lines []string
		for _, l := range strings.Split(content, "\n") {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if keep != nil && !keep(t) {
				continue
			}
			lines = append(lines, t)
			if len(lines) >= max {
				break
			}
		}
		if len(lines) == 0 {
			return
		}
		b.WriteString("\n" + label + "\n")
		for _, l := range lines {
			b.WriteString("  " + l + "\n")
		}
	}

	slice("identity.md", "WHO:", 2, nil)
	slice("preferences.md", "TOP PREFERENCES:", 5, nil)
	// Project facts for THIS workspace first (per-project sub-file), falling
	// back to the shared monolith for older vaults.
	projFile := memory.ProjectFile(s.store.WorkspaceRoot)
	if !s.canRead(projFile) || !fileHasContent(s, projFile) {
		projFile = "projects.md"
	}
	slice(projFile, "THIS PROJECT:", 6, nil)
	slice("daily.md", "LAST 7 DAYS:", 6, recentDatedLine)

	if b.Len() == 0 {
		return ""
	}
	out := "\n\n---\n🧠 SESSION PRIMER (auto-recalled from memory — ground yourself in this):\n" + b.String()
	if len(out) > primerMaxChars {
		out = out[:primerMaxChars] + "\n  …(truncated — read the files for the rest)"
	}
	return out
}

func fileHasContent(s *Server, file string) bool {
	c, err := s.store.View(file)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(c, "\n") {
		t := strings.TrimSpace(l)
		if t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}

// recentDatedLine keeps journal bullets stamped within the last 7 days. Lines
// carry dates like "[2026-07-02]" (the sync format) — undated lines are noise
// for a recency slice and are skipped.
func recentDatedLine(line string) bool {
	i := strings.Index(line, "[")
	if i < 0 || len(line) < i+11 {
		return false
	}
	ts, err := time.Parse("2006-01-02", line[i+1:i+11])
	if err != nil {
		return false
	}
	return time.Since(ts) <= 7*24*time.Hour
}
