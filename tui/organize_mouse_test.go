package tui

import (
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
)

func orgLongText(p string) string {
	var b strings.Builder
	for i := 0; i < 25; i++ {
		b.WriteString(p + " line\n")
	}
	return b.String()
}

func newReviewApp(t *testing.T) model {
	t.Helper()
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = u.(model)
	m.memoryOrg.changes = []memory.ProposedChange{
		{Name: "a.md", OldContent: orgLongText("oldA"), NewContent: orgLongText("newA"), Scope: "global"},
		{Name: "b.md", OldContent: orgLongText("oldB"), NewContent: orgLongText("newB"), Scope: "global"},
		{Name: "c.md", OldContent: orgLongText("oldC"), NewContent: orgLongText("newC"), Scope: "global"},
	}
	m.memoryOrg.decisions = make([]orgDecision, 3)
	m.memoryOrg.mode = orgReview
	m.memoryOrg.loadCurrentChange()
	return m
}

// TestReviewFitsAndShowsActionBar guards the layout fix: the review screen must fit
// the terminal (no "enlarge window" clip) and show the color-coded action bar +
// counts + scroll indicators.
func TestReviewFitsAndShowsActionBar(t *testing.T) {
	m := newReviewApp(t)
	m.memoryOrg.decisions[0] = decApproved
	m.memoryOrg.loadCurrentChange()
	full := m.View()
	if strings.Contains(full, "enlarge window") {
		t.Error("review content is clipped (enlarge window)")
	}
	for _, w := range []string{"[a] Approve", "[r] Reject", "File 1 of 3 changed", "✓ 1 approved", "▼ more below"} {
		if !strings.Contains(full, w) {
			t.Errorf("review view missing %q", w)
		}
	}
	if h := strings.Count(full, "\n") + 1; h > 40 {
		t.Errorf("review view height %d exceeds terminal 40", h)
	}
}

// TestReviewClickCoordsMatchRender keeps the click hit-test math aligned with the
// actual rendered rows of the action bar and the file strip.
func TestReviewClickCoordsMatchRender(t *testing.T) {
	m := newReviewApp(t)
	lines := strings.Split(m.View(), "\n")
	actionRow := -1
	stripRow := -1
	for i, l := range lines {
		if actionRow < 0 && strings.Contains(l, "[a] Approve") {
			actionRow = i
		}
		if stripRow < 0 && strings.Contains(l, "Files:") {
			stripRow = i
		}
	}
	top := m.memoryOrg.contentTopY()
	wantAction := top + 4 + (m.memoryOrg.beforeVP.Height + 4)
	if actionRow != wantAction {
		t.Errorf("action bar rendered at row %d, click handler expects %d", actionRow, wantAction)
	}
	if stripRow != top+3 {
		t.Errorf("file strip rendered at row %d, click handler expects %d", stripRow, top+3)
	}
}

// TestReviewMouseClickAndWheel verifies clicking a button approves/rejects and the
// wheel scrolls the panes.
func TestReviewMouseClickAndWheel(t *testing.T) {
	m := newReviewApp(t)
	top := m.memoryOrg.contentTopY()
	actionsY := top + 4 + (m.memoryOrg.beforeVP.Height + 4)
	rejectX := len("[a] Approve") + 3 + 2 // inside the "[r] Reject" chip
	u, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: rejectX, Y: actionsY})
	m = u.(model)
	if m.memoryOrg.decisions[0] != decRejected {
		t.Fatalf("click Reject did not set decRejected, got %v", m.memoryOrg.decisions[0])
	}
	before := m.memoryOrg.beforeVP.YOffset
	u, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress, X: 5, Y: top + 6})
	m = u.(model)
	if m.memoryOrg.beforeVP.YOffset <= before {
		t.Errorf("wheel down did not scroll the before pane (%d -> %d)", before, m.memoryOrg.beforeVP.YOffset)
	}
}

// TestHasRealChange ensures whitespace-only differences are not treated as changes,
// so identical files don't clutter the review list.
func TestHasRealChange(t *testing.T) {
	noop := memory.ProposedChange{OldContent: "# A\n- x\n", NewContent: "# A \n- x\n\n"} // trailing ws + blank line
	if hasRealChange(noop) {
		t.Error("whitespace/blank-line-only difference must NOT count as a real change")
	}
	real := memory.ProposedChange{OldContent: "# A\n- x\n", NewContent: "# A\n- x\n- y\n"}
	if !hasRealChange(real) {
		t.Error("an added line must count as a real change")
	}
}

// TestRunningViewShowsProgress checks the running screen surfaces provider + elapsed
// so it never looks stuck.
func TestRunningViewShowsProgress(t *testing.T) {
	m := newReviewApp(t)
	m.memoryOrg.mode = orgRunning
	m.memoryOrg.runProvider = "Claude Code (Recommended)"
	m.memoryOrg.runModel = "haiku"
	m.memoryOrg.spin = 60 // ~7s
	v := m.memoryOrg.View()
	for _, w := range []string{"Claude Code", "Gathered memory files", "Waiting for the model", "✓", "Nothing is written yet"} {
		if !strings.Contains(v, w) {
			t.Errorf("running view missing %q", w)
		}
	}
}

// TestIdleAutoFetchOnURLSave verifies the custom-endpoint UX: saving the URL starts
// a model fetch, jumps focus to the Model list, and renders the bordered panels.
func TestIdleAutoFetchOnURLSave(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = u.(model)
	for i := 0; i < 12 && m.memoryOrg.currentProvider().id != "custom"; i++ {
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = u.(model)
	}
	if m.memoryOrg.currentProvider().id != "custom" {
		t.Fatal("could not select the Custom URL provider")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = u.(model)
	if !m.memoryOrg.urlEditing {
		t.Fatal("'e' did not open the URL editor")
	}
	u, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(model)
	if !m.memoryOrg.fetching {
		t.Error("saving the URL must start a model fetch")
	}
	if m.memoryOrg.focus != focusModel {
		t.Error("saving the URL must move focus to the model list")
	}
	if cmd == nil {
		t.Error("saving the URL must return a fetch command")
	}
	u, _ = m.Update(orgModelsFetchedMsg{success: true, models: []string{"qwen2.5-coder:7b", "llama3.1:8b"}})
	m = u.(model)
	v := m.memoryOrg.View()
	for _, w := range []string{"Provider", "Model", "Endpoint URL", "llama3.1:8b"} {
		if !strings.Contains(v, w) {
			t.Errorf("idle view missing %q", w)
		}
	}
}

// TestWizardProviderModelConfirm locks the step-by-step flow: Enter on the provider
// advances to the model list; Enter on the model opens the [y]/[n] confirmation;
// 'n' cancels; 'y' starts the run. The confirm popup must capture input.
func TestWizardProviderModelConfirm(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = u.(model)
	if m.memoryOrg.focus != focusProvider {
		t.Fatal("should start focused on provider")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(model)
	if m.memoryOrg.focus != focusModel {
		t.Fatalf("Enter on provider must advance to the model list, focus=%v", m.memoryOrg.focus)
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(model)
	if !m.memoryOrg.confirming {
		t.Fatal("Enter on model must open the confirmation popup")
	}
	if !m.memoryOrg.capturesInput() {
		t.Error("confirmation must capture input so number/tab keys don't leak to the app")
	}
	if v := m.memoryOrg.View(); !strings.Contains(v, "Confirm run") || !strings.Contains(v, "[y] Yes") {
		t.Error("confirm popup must show 'Confirm run' and [y]/[n]")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = u.(model)
	if m.memoryOrg.confirming {
		t.Error("'n' must cancel the confirmation")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // model -> confirm again
	m = u.(model)
	u, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = u.(model)
	if m.memoryOrg.mode != orgRunning {
		t.Errorf("'y' must start the run, mode=%v", m.memoryOrg.mode)
	}
	if cmd == nil {
		t.Error("'y' must return the run command")
	}
}

// TestCustomEnterOpensURLEditor verifies Enter on the Custom URL provider opens the
// URL field to fill (rather than running with a default).
func TestCustomEnterOpensURLEditor(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = u.(model)
	for i := 0; i < 12 && m.memoryOrg.currentProvider().id != "custom"; i++ {
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = u.(model)
	}
	if m.memoryOrg.currentProvider().id != "custom" {
		t.Fatal("could not reach Custom URL provider")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(model)
	if !m.memoryOrg.urlEditing {
		t.Error("Enter on Custom URL must open the URL editor")
	}
}

// TestApproveAutoAdvances verifies that approving/rejecting a file moves the cursor
// to the next undecided file automatically.
func TestApproveAutoAdvances(t *testing.T) {
	m := newReviewApp(t) // 3 pending files, cursor 0
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = u.(model)
	if m.memoryOrg.decisions[0] != decApproved {
		t.Fatal("file 0 should be approved")
	}
	if m.memoryOrg.fileCursor != 1 {
		t.Errorf("approving file 0 should advance to file 1, got %d", m.memoryOrg.fileCursor)
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = u.(model)
	if m.memoryOrg.decisions[1] != decRejected || m.memoryOrg.fileCursor != 2 {
		t.Errorf("rejecting file 1 should advance to file 2, got cursor=%d dec1=%v", m.memoryOrg.fileCursor, m.memoryOrg.decisions[1])
	}
	// last file: deciding it keeps the cursor on the last (nothing left)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = u.(model)
	if m.memoryOrg.fileCursor != 2 {
		t.Errorf("all decided — cursor should stay on last file, got %d", m.memoryOrg.fileCursor)
	}
}

// TestEditViewShowsSaveDiscardAndFits guards the edit screen: pressing [e] opens the
// editor with a visible, color-coded Save/Discard action bar and the content fits the
// terminal (no app-level "enlarge window" clip from an over-tall editor).
func TestEditViewShowsSaveDiscardAndFits(t *testing.T) {
	m := newReviewApp(t) // 140x40
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = u.(model)
	if m.memoryOrg.mode != orgEditing {
		t.Fatalf("pressing e should enter editing mode, got %v", m.memoryOrg.mode)
	}
	full := m.View()
	if strings.Contains(full, "enlarge window") {
		t.Error("edit view is clipped (enlarge window) — editor sized too tall")
	}
	for _, w := range []string{"[ctrl+s] Save & approve", "[esc] Discard changes", "Editing a.md"} {
		if !strings.Contains(full, w) {
			t.Errorf("edit view missing %q", w)
		}
	}
	if h := strings.Count(full, "\n") + 1; h > 40 {
		t.Errorf("edit view height %d exceeds terminal 40", h)
	}
	// ctrl+s saves edits + approves and returns to review.
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = u.(model)
	if m.memoryOrg.mode != orgReview {
		t.Errorf("ctrl+s should return to review, got %v", m.memoryOrg.mode)
	}
	if m.memoryOrg.decisions[0] != decApproved {
		t.Errorf("ctrl+s should mark file approved, got %v", m.memoryOrg.decisions[0])
	}
}

// TestRunningEscCancelsAndDropsLateResult verifies esc on the running screen cancels
// the run (calls the cancel func, returns to idle with a status) and that a late
// orgRunMsg arriving after cancel is dropped instead of opening a review.
func TestRunningEscCancelsAndDropsLateResult(t *testing.T) {
	store := memory.NewStore(t.TempDir())
	m := newOrganizeModel(store, store.Root, nil)
	m.width, m.height = 120, 40
	m.mode = orgRunning
	cancelled := false
	m.runCancel = func() { cancelled = true }

	// esc cancels.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled {
		t.Error("esc should call the run cancel func")
	}
	if m.mode != orgIdle {
		t.Errorf("esc should return to idle, got %v", m.mode)
	}
	if !strings.Contains(m.status, "cancelled") {
		t.Errorf("status should note cancellation, got %q", m.status)
	}
	if !m.capturesInput() && m.mode == orgRunning {
		t.Error("running mode should capture input")
	}

	// A late result must be ignored (we already left running).
	m, _ = m.Update(orgRunMsg{prop: memory.OrganizeProposal{Changes: []memory.ProposedChange{
		{Name: "identity.md", OldContent: "a", NewContent: "b"},
	}}, res: memory.OrganizeResult{Success: true}})
	if m.mode == orgReview {
		t.Error("late orgRunMsg after cancel must not open the review")
	}
}

// TestSubmitShowsConfirmationAndRecordsHistory guards the post-write feedback: after
// approving all + Enter, the done screen names what was written and points to the
// Audit Trail, the stats persist a files-changed count, and the writes are recorded
// in the audit log so they show up as durable history.
func TestSubmitShowsConfirmationAndRecordsHistory(t *testing.T) {
	m := newReviewApp(t) // real logger via NewApp; 3 differing files
	m.memoryOrg.runProvider = "antigravity"
	// Approve all, then submit.
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(model)

	if m.memoryOrg.mode != orgDone {
		t.Fatalf("submit should land on the done screen, got %v", m.memoryOrg.mode)
	}
	full := m.View()
	for _, w := range []string{"Wrote 3 file(s)", "Audit Trail", "a.md", "b.md", "c.md"} {
		if !strings.Contains(full, w) {
			t.Errorf("done screen missing %q", w)
		}
	}
	if m.memoryOrg.lastFilesChanged != 3 {
		t.Errorf("stats should record 3 files changed, got %d", m.memoryOrg.lastFilesChanged)
	}
	// The writes must be recorded in the audit log (durable history the user can revisit).
	entries, err := m.memoryOrg.logger.TailWrites(10)
	if err != nil {
		t.Fatalf("audit TailWrites failed: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if e.Action == "write" && e.Provider == "antigravity" {
			got[e.File] = true
		}
	}
	for _, f := range []string{"a.md", "b.md", "c.md"} {
		if !got[f] {
			t.Errorf("audit log missing organize write for %q (entries: %+v)", f, entries)
		}
	}
}
