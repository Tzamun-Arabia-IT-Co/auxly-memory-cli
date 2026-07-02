package audit

import (
	"testing"
	"time"
)

func TestApprovalStatsCountsGroupsAndWindow(t *testing.T) {
	l := newTestApprovalLogger(t)

	logAuditEntry(t, l, "codex", "pending_approve", "projects.md", "require_approval")
	logAuditEntry(t, l, "codex", "pending_approve", "identity.md", "require_approval")
	logAuditEntry(t, l, "codex", "pending_reject", "scratch.md", "require_approval")
	logAuditEntry(t, l, "claude-code", "pending_reject", "private.md", "require_approval")
	old := logAuditEntry(t, l, "claude-code", "pending_approve", "old.md", "require_approval")
	backdateAuditEntry(t, l, old.RequestID, 91)
	logAuditEntry(t, l, "codex", "write", "ignored.md", "auto")

	stats, err := l.ApprovalStats(90)
	if err != nil {
		t.Fatalf("ApprovalStats failed: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d stats, want 2: %#v", len(stats), stats)
	}
	if stats[0] != (AgentApprovalStats{Provider: "codex", Approved: 2, Rejected: 1}) {
		t.Fatalf("stats[0] = %#v, want codex 2/1", stats[0])
	}
	if stats[1] != (AgentApprovalStats{Provider: "claude-code", Approved: 0, Rejected: 1}) {
		t.Fatalf("stats[1] = %#v, want claude-code 0/1", stats[1])
	}
}

func TestApprovalStatsDefaultWindowAndNilLogger(t *testing.T) {
	var nilLogger *Logger
	stats, err := nilLogger.ApprovalStats(0)
	if err != nil {
		t.Fatalf("nil ApprovalStats failed: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("nil ApprovalStats length = %d, want 0", len(stats))
	}
}

// TestApprovalStatsExcludesCaptureAndOrganizeRows is the evidence-laundering
// regression test: capture:*-sourced and organize-* pseudo-agent pendings
// queue unconditionally regardless of trust, so approving them says nothing
// about a provider's direct-write judgment. Only "claude"'s own direct-write
// pending_approve/pending_reject rows should count.
func TestApprovalStatsExcludesCaptureAndOrganizeRows(t *testing.T) {
	l := newTestApprovalLogger(t)

	logAuditEntry(t, l, "capture:claude", "pending_approve", "note1.md", "pending")
	logAuditEntry(t, l, "capture:claude", "pending_approve", "note2.md", "pending")
	logAuditEntry(t, l, "organize-split", "pending_approve", "projects.md", "pending")
	logAuditEntry(t, l, "organize-contradictions", "pending_reject", "loser.md", "pending")
	logAuditEntry(t, l, "claude", "pending_approve", "identity.md", "pending")
	logAuditEntry(t, l, "claude", "pending_reject", "scratch.md", "pending")

	stats, err := l.ApprovalStats(90)
	if err != nil {
		t.Fatalf("ApprovalStats failed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d providers, want 1 (capture:*/organize-* excluded): %#v", len(stats), stats)
	}
	if stats[0] != (AgentApprovalStats{Provider: "claude", Approved: 1, Rejected: 1}) {
		t.Fatalf("stats[0] = %#v, want claude 1/1 (only its direct-write decisions)", stats[0])
	}
}

func TestNormalizeDecisionAgent(t *testing.T) {
	tests := map[string]string{
		"capture:claude-code": "capture:claude-code",
		"Codex":               "codex",
		" capture:Gemini ":    "capture:gemini",
		"":                    "",
	}
	for in, want := range tests {
		if got := NormalizeDecisionAgent(in); got != want {
			t.Fatalf("NormalizeDecisionAgent(%q) = %q, want %q", in, got, want)
		}
	}
}

func newTestApprovalLogger(t *testing.T) *Logger {
	t.Helper()

	l, err := NewLogger(t.TempDir())
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})
	return l
}

func logAuditEntry(t *testing.T, l *Logger, provider, action, file, trustLevel string) *Entry {
	t.Helper()

	entry, err := l.Log(provider, provider, action, file, "", "test", trustLevel)
	if err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	return entry
}

func backdateAuditEntry(t *testing.T, l *Logger, requestID string, days int) {
	t.Helper()

	ts := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	if _, err := l.db.Exec("UPDATE audit_entries SET timestamp = ? WHERE request_id = ?", ts, requestID); err != nil {
		t.Fatalf("backdate audit entry failed: %v", err)
	}
}
