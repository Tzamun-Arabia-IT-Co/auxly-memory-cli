package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestTTLBadgeMath locks the "archives in Nd" countdown: hidden when TTL is
// disabled or Created is unknown, rounded UP, and clamped to 0 (never
// negative) once past due.
func TestTTLBadgeMath(t *testing.T) {
	if got := ttlBadge(time.Now(), 0); got != "" {
		t.Fatalf("TTL=0 should hide the badge, got %q", got)
	}
	if got := ttlBadge(time.Time{}, 30*24*time.Hour); got != "" {
		t.Fatalf("zero Created should hide the badge, got %q", got)
	}
	if got := stripANSI(ttlBadge(time.Now(), 30*24*time.Hour)); !strings.Contains(got, "archives in 30d") {
		t.Fatalf("fresh entry: got %q, want it to contain 'archives in 30d'", got)
	}
	if got := stripANSI(ttlBadge(time.Now().Add(-25*24*time.Hour), 30*24*time.Hour)); !strings.Contains(got, "archives in 5d") {
		t.Fatalf("25d elapsed of 30d TTL: got %q, want 'archives in 5d'", got)
	}
	if got := stripANSI(ttlBadge(time.Now().Add(-40*24*time.Hour), 30*24*time.Hour)); !strings.Contains(got, "archives in 0d") {
		t.Fatalf("expired entry: got %q, want 'archives in 0d' (clamped, not negative)", got)
	}
}

// TestNamesForAgentMatchesCaptureAlias locks 'A' batch-key selection: matches
// the exact agent id plus its "capture:"-prefixed alias, and legacy
// no-attribution entries ("") group together under their own batch.
func TestNamesForAgentMatchesCaptureAlias(t *testing.T) {
	infos := []pending.Info{
		{Name: "a", Agent: "claude-code"},
		{Name: "b", Agent: "capture:claude-code"},
		{Name: "c", Agent: "cursor"},
		{Name: "d", Agent: ""},
	}
	if got := namesForAgent(infos, "claude-code"); !equalStrings(got, []string{"a", "b"}) {
		t.Fatalf("claude-code selection: got %v, want [a b]", got)
	}
	if got := namesForAgent(infos, "cursor"); !equalStrings(got, []string{"c"}) {
		t.Fatalf("cursor selection: got %v, want [c]", got)
	}
	if got := namesForAgent(infos, ""); !equalStrings(got, []string{"d"}) {
		t.Fatalf("legacy no-agent selection: got %v, want [d]", got)
	}
}

// TestNamesForFileNormalizesPath locks 'F' batch-key selection: targets are
// path-normalized the same way cmd/approve.go's selectPending compares them,
// so "identity.md" and "./identity.md" are the same file.
func TestNamesForFileNormalizesPath(t *testing.T) {
	infos := []pending.Info{
		{Name: "a", Target: "identity.md"},
		{Name: "b", Target: "./identity.md"},
		{Name: "c", Target: "projects.md"},
	}
	if got := namesForFile(infos, "identity.md"); !equalStrings(got, []string{"a", "b"}) {
		t.Fatalf("got %v, want [a b]", got)
	}
	if got := namesForFile(infos, "projects.md"); !equalStrings(got, []string{"c"}) {
		t.Fatalf("got %v, want [c]", got)
	}
}

// TestFormatApprovalDiffPreservesContent locks the colorizer's content
// contract: every line's TEXT is preserved verbatim regardless of which
// color bucket ('+', '-', '#', or plain) it falls into — only styling is
// added, never content mutated. Color rendering itself is a lipgloss/TTY
// concern outside this pure function's contract.
func TestFormatApprovalDiffPreservesContent(t *testing.T) {
	diff := "+added line\n-removed line\n# organize-contradictions: note\nunchanged line"
	if got := stripANSI(formatApprovalDiff(diff)); got != diff {
		t.Fatalf("formatApprovalDiff must preserve line content/order:\ngot  %q\nwant %q", got, diff)
	}
}

// TestBatchConfirmRequiresYToApprove locks the y/n gate around 'A'/'F': only
// "y" fires the batch approve; "n"/"esc" cancel without touching the queue.
func TestBatchConfirmRequiresYToApprove(t *testing.T) {
	m := diffModel{
		batchKind:  "agent",
		batchLabel: "codex",
		batchNames: []string{"p1", "p2"},
		status:     "approve 2 pending from codex? y/n",
	}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cmd != nil {
		t.Fatalf("'n' must not return a command")
	}
	if nm.batchKind != "" || nm.batchNames != nil || nm.status != "" {
		t.Fatalf("'n' must clear the pending batch, got %+v", nm)
	}

	ym, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatalf("'y' must dispatch the batch-approve command")
	}
	if ym.batchKind != "" || ym.batchNames != nil {
		t.Fatalf("'y' must clear the pending batch state before dispatch, got %+v", ym)
	}
}

// TestDiffApproveAndRejectLogPendingAuditRows is finding 1's regression: TUI
// approvals/rejections via the single 'a'/'r' keys must write the same
// pending_approve/pending_reject audit rows cmd/approve.go and cmd/reject.go
// write, so TUI-heavy users accumulate the trust evidence ApprovalStats reads.
func TestDiffApproveAndRejectLogPendingAuditRows(t *testing.T) {
	dir := t.TempDir()
	mgr := pending.NewManager(dir)
	logger, err := audit.NewLogger(dir)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer logger.Close()

	approveName, err := mgr.WriteFrom("identity.md", "- likes tea\n", "claude-code")
	if err != nil {
		t.Fatalf("WriteFrom (approve target): %v", err)
	}
	rejectName, err := mgr.WriteFrom("projects.md", "- new project\n", "capture:cursor")
	if err != nil {
		t.Fatalf("WriteFrom (reject target): %v", err)
	}

	m := newDiffModel(mgr, logger)
	m.files = []pending.PendingFile{{Name: approveName}, {Name: rejectName}}
	m.infos = []pending.Info{
		{Name: approveName, Agent: "claude-code", Target: "identity.md"},
		{Name: rejectName, Agent: "capture:cursor", Target: "projects.md"},
	}

	m.cursor = 0
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}); cmd == nil {
		t.Fatal("'a' must return a refresh command")
	}
	m.cursor = 1
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}); cmd == nil {
		t.Fatal("'r' must return a refresh command")
	}

	stats, err := logger.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ByAction["pending_approve"] != 1 {
		t.Errorf("pending_approve rows = %d, want 1 (ByAction=%v)", stats.ByAction["pending_approve"], stats.ByAction)
	}
	if stats.ByAction["pending_reject"] != 1 {
		t.Errorf("pending_reject rows = %d, want 1 (ByAction=%v)", stats.ByAction["pending_reject"], stats.ByAction)
	}
}

// TestBatchApproveCountsFailedForConcurrentRemoval is finding 2's regression:
// an error other than ErrConflict (here, the pending file vanishing mid-batch,
// as a concurrent CLI approve/reject would cause) must be counted as failed,
// never silently dropped — the three counters always reconcile against the
// batch size.
func TestBatchApproveCountsFailedForConcurrentRemoval(t *testing.T) {
	dir := t.TempDir()
	mgr := pending.NewManager(dir)
	ok, err := mgr.WriteFrom("identity.md", "- a\n", "claude-code")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	deleted, err := mgr.WriteFrom("projects.md", "- b\n", "claude-code")
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	// Simulate the pending being removed concurrently (another approve/reject/
	// TTL sweep) before this batch reaches it.
	if err := os.Remove(filepath.Join(dir, ".pending", deleted)); err != nil {
		t.Fatalf("os.Remove: %v", err)
	}

	msg := batchApproveCmd(mgr, nil, []string{ok, deleted})()
	res, isBatch := msg.(diffBatchMsg)
	if !isBatch {
		t.Fatalf("expected diffBatchMsg, got %T", msg)
	}
	if res.applied != 1 {
		t.Errorf("applied = %d, want 1", res.applied)
	}
	if res.failed != 1 {
		t.Errorf("failed = %d, want 1 — the concurrent removal must be counted, not swallowed", res.failed)
	}
	if res.conflicted != 0 {
		t.Errorf("conflicted = %d, want 0", res.conflicted)
	}
}

// TestBatchKeysNoopWhileViewingDiff is finding 4's regression: 'A'/'F' must
// only arm a batch confirm from the list view. While an individual diff is
// open (m.viewing != ""), the confirm prompt they'd arm renders on a branch
// the viewing pane doesn't show — a blind 'y' would then batch-approve
// entries the user never reviewed.
func TestBatchKeysNoopWhileViewingDiff(t *testing.T) {
	base := diffModel{
		files:   []pending.PendingFile{{Name: "p1"}},
		infos:   []pending.Info{{Name: "p1", Agent: "claude-code", Target: "identity.md"}},
		viewing: "---\ntarget: identity.md\n---\n+ hi\n",
	}

	if nm, cmd := base.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")}); cmd != nil || nm.batchKind != "" {
		t.Errorf("'A' while viewing must no-op, got cmd=%v batchKind=%q", cmd, nm.batchKind)
	}
	if nm, cmd := base.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")}); cmd != nil || nm.batchKind != "" {
		t.Errorf("'F' while viewing must no-op, got cmd=%v batchKind=%q", cmd, nm.batchKind)
	}
}

// TestFormatApprovalDiffDimsFrontmatter is finding 7's regression: the
// frontmatter block's '---' delimiters start with '-' the same as a removed
// diff line, so without a carve-out they render red (a deletion) — they must
// render dim instead. Content after the frontmatter still colors normally.
func TestFormatApprovalDiffDimsFrontmatter(t *testing.T) {
	// Force a real color profile — tests otherwise run with no TTY, where every
	// style degrades to plain text and the color-bucket comparisons below would
	// trivially pass regardless of which bucket a line actually landed in.
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	diff := "---\ntarget: identity.md\n---\n+added\n-removed\n"
	got := formatApprovalDiff(diff)
	lines := strings.Split(got, "\n")

	del := lipgloss.NewStyle().Foreground(ColorDanger)
	if lines[0] == del.Render("---") || lines[2] == del.Render("---") {
		t.Fatalf("frontmatter '---' lines must not carry the red deletion style:\n%v", lines)
	}
	if lines[4] != del.Render("-removed") {
		t.Fatalf("content after the frontmatter must still color as a deletion:\ngot  %q\nwant %q", lines[4], del.Render("-removed"))
	}
	if stripped := stripANSI(got); stripped != diff {
		t.Fatalf("content must be preserved verbatim:\ngot  %q\nwant %q", stripped, diff)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
