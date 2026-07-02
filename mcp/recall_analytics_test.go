package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// analyticsServer builds a LOCAL server over a temp vault with one known fact.
func analyticsServer(t *testing.T, fact string) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "projects.md"), []byte("# Projects\n- "+fact+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewServer(dir)
	s.store.WorkspaceRoot = "" // hermetic: ignore any real .auxly workspace under cwd
	if s.logger == nil {
		t.Fatal("audit logger not built")
	}
	return s
}

// auditDBBytes returns the raw audit.db (+ any sidecars) content for privacy greps.
func auditDBBytes(t *testing.T, memPath string) []byte {
	t.Helper()
	var all []byte
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if b, err := os.ReadFile(filepath.Join(memPath, "audit.db"+suffix)); err == nil {
			all = append(all, b...)
		}
	}
	if len(all) == 0 {
		t.Fatal("audit.db missing")
	}
	return all
}

// TestRecallAnalytics_RecordsHashesNeverText is the Sprint 12 acceptance gate:
// a semantic recall lands in recall_events, but neither the query nor the fact
// text is recoverable from the database — hashes only. (Recall queries can
// contain secrets; the personal-leak regression proved it.)
func TestRecallAnalytics_RecordsHashesNeverText(t *testing.T) {
	const fact = "SECRET-FACT-zebra-9713 launches in October"
	s := analyticsServer(t, fact)
	withStubEmbedder(s, true)

	// Query == the chunk's exact text ("- <fact>") → stub cosine 1.0 → a
	// guaranteed SEMANTIC hit (hash-vectors score near-randomly otherwise and
	// would fall below the relevance floor into the substring fallback).
	out := resultText(s.toolRecall("- " + fact))
	if !strings.Contains(out, "projects.md") || !strings.Contains(out, "lines") {
		t.Fatalf("semantic recall did not hit (lines marker distinguishes it from fallback): %q", out)
	}

	stats, err := s.logger.RecallStatsByFile()
	if err != nil {
		t.Fatalf("RecallStatsByFile: %v", err)
	}
	found := false
	for _, st := range stats {
		if st.File == "projects.md" && st.Hits90 >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall event not recorded: %+v", stats)
	}

	if raw := auditDBBytes(t, s.memoryPath); strings.Contains(string(raw), "zebra-9713") {
		t.Fatal("query/fact text leaked into audit.db — hashes only, ever")
	}
}

// TestRecallAnalytics_FallbackFlagged: embeddings off → substring fallback →
// the query event is recorded with the fallback flag (the rate is the embed
// health signal stats --recall surfaces).
func TestRecallAnalytics_FallbackFlagged(t *testing.T) {
	const fact = "quarterly infra budget approved"
	s := analyticsServer(t, fact)
	withStubEmbedder(s, false) // disabled → ErrUnavailable → fallback

	out := resultText(s.toolRecall("budget"))
	if !strings.Contains(out, "projects.md") {
		t.Fatalf("fallback search did not hit: %q", out)
	}

	fb, total, err := s.logger.RecallFallbackRate(30)
	if err != nil {
		t.Fatalf("RecallFallbackRate: %v", err)
	}
	if fb != 1 || total != 1 {
		t.Fatalf("fallback rate = %d/%d, want 1/1", fb, total)
	}
	if raw := auditDBBytes(t, s.memoryPath); strings.Contains(string(raw), "budget") {
		t.Fatal("fallback query text leaked into audit.db")
	}
}
