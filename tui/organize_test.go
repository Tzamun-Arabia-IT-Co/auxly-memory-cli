package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
	tea "github.com/charmbracelet/bubbletea"
)

func TestOrganizeReview_AppliesOnlyApproved(t *testing.T) {
	store := organizeTestStore(t)
	m := organizeReviewTestModel(store)

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd != nil {
		t.Fatal("approve should not return a command")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got, _ := store.View("identity.md"); got != "# Identity\n- Name: New\n" {
		t.Fatalf("approved file was not applied; got %q", got)
	}
	if got, _ := store.View("projects.md"); strings.Contains(got, "Added") {
		t.Fatalf("rejected file changed on disk; got %q", got)
	}
}

func TestOrganizeEdit_AppliesEditedContent(t *testing.T) {
	store := organizeTestStore(t)
	m := organizeReviewTestModel(store)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m.editor.SetValue("# Identity\n- Name: Edited\n")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got, _ := store.View("identity.md"); got != "# Identity\n- Name: Edited\n" {
		t.Fatalf("edited content was not applied; got %q", got)
	}
	if got, _ := store.View("projects.md"); strings.Contains(got, "Added") {
		t.Fatalf("pending unapproved file changed on disk; got %q", got)
	}
}

func TestOrganizeReview_NoApprovalsWritesNothing(t *testing.T) {
	store := organizeTestStore(t)
	m := organizeReviewTestModel(store)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got, _ := store.View("identity.md"); got != "# Identity\n- Name: Old\n" {
		t.Fatalf("unapproved identity file changed; got %q", got)
	}
	if got, _ := store.View("projects.md"); got != "# Projects\n- Alpha\n" {
		t.Fatalf("unapproved projects file changed; got %q", got)
	}
}

func TestOrganizeProviderModelPick(t *testing.T) {
	store := organizeTestStore(t)

	m := newOrganizeModel(store, store.Root, nil)
	m.providers = []orgProvider{
		{kind: "separator", label: "── Local / API ──"},
		{kind: "api", id: "custom", label: "Custom URL..."},
	}
	m.provIdx = 1
	m.resetProviderModels()
	m.customURL = "http://custom.example.test"
	m, _ = m.Update(orgModelsFetchedMsg{success: true, models: []string{"model-a", "model-b"}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.picked != "model-b" {
		t.Fatalf("expected second discovered model to be picked, got %q", m.picked)
	}
	provider, endpoint, model := m.planTarget()
	if provider != "custom" || endpoint != "http://custom.example.test" || model != "model-b" {
		t.Fatalf("plan target = (%q, %q, %q), want custom endpoint/model-b", provider, endpoint, model)
	}
}

func TestOrganizeAgentProviderRuns(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.providers = []orgProvider{
		{kind: "agent", id: "Claude Code / CLI", label: "Claude Code (Recommended)", command: "/bin/echo"},
		{kind: "separator", label: "── Local / API ──"},
		{kind: "api", id: "ollama", label: "Ollama (local)"},
	}
	m.provIdx = 0
	m.resetProviderModels()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Tab into the model list, Down to the 2nd entry (now "haiku (fast)" after
	// sonnet became the recommended default), Enter — verifies a picked
	// non-default model flows through to planTarget.
	provider, command, model := m.planTarget()
	if provider != "Claude Code / CLI" || command != "/bin/echo" || model != "haiku" {
		t.Fatalf("agent plan target = (%q, %q, %q), want Claude /bin/echo haiku", provider, command, model)
	}
}

// organizeEncryptedTestStore is a hermetic (AUXLY_VAULT_KEY test identity,
// see internal/memory/cryptio_test.go's testVaultIdentity for the same
// pattern) store with one encrypted file, for driving the encrypted-file
// choice modal.
func organizeEncryptedTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dir := t.TempDir()
	store := memory.NewStore(dir)
	store.WorkspaceRoot = ""
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUXLY_VAULT_KEY", identity.String())
	if err := store.Write("personal.md", "# Personal\n- secret\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.EncryptFile("personal.md"); err != nil {
		t.Fatal(err)
	}
	return store
}

// organizeAgentTestModel builds an organizeModel with a single CLI-agent
// provider selected (mirrors TestOrganizeAgentProviderRuns's setup).
func organizeAgentTestModel(store *memory.Store) organizeModel {
	m := newOrganizeModel(store, store.Root, nil)
	m.providers = []orgProvider{
		{kind: "agent", id: "Claude Code / CLI", label: "Claude Code (Recommended)", command: "/bin/echo"},
		{kind: "separator", label: "── Local / API ──"},
		{kind: "api", id: "ollama", label: "Ollama (local)"},
	}
	m.provIdx = 0
	m.resetProviderModels()
	m.width = 100
	m.height = 30
	return m
}

// toConfirming drives an idle organizeModel through provider→model selection
// up to the plain [y]/[n] confirm popup, matching the key sequence
// TestOrganizeAgentProviderRuns uses (Enter switches focus provider→model,
// a second Enter locks the model and opens the confirm popup).
func toConfirming(m organizeModel) organizeModel {
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m
}

// TestOrganizeEncChoice_AppearsForAgentWithEncryptedFiles proves picking [y]
// on the plain confirm popup does NOT start the run when the provider is a
// CLI agent and the vault has encrypted files — it opens the encrypted-file
// choice modal instead (the UX fix for the old hard-refusal dead end).
func TestOrganizeEncChoice_AppearsForAgentWithEncryptedFiles(t *testing.T) {
	store := organizeEncryptedTestStore(t)
	m := organizeAgentTestModel(store)
	m = toConfirming(m)
	if !m.confirming {
		t.Fatal("test setup broken: expected the plain confirm popup to be up")
	}

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.confirming {
		t.Fatal("confirming should have cleared")
	}
	if !m.encChoice {
		t.Fatal("expected the encrypted-file choice modal to open instead of starting the run")
	}
	if m.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle (run must not start until s/y/esc)", m.mode)
	}
	if len(m.encFiles) != 1 || m.encFiles[0] != "personal.md" {
		t.Fatalf("encFiles = %v, want [personal.md]", m.encFiles)
	}
	if cmd != nil {
		t.Fatal("opening the choice modal should not dispatch a command")
	}
}

// TestOrganizeEncChoice_SkipStartsRunWithoutEncryptedFiles drives [s]: the
// run must start immediately with skipEncryptedRun set, no decrypt involved.
func TestOrganizeEncChoice_SkipStartsRunWithoutEncryptedFiles(t *testing.T) {
	store := organizeEncryptedTestStore(t)
	m := organizeAgentTestModel(store)
	m = toConfirming(m)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.encChoice {
		t.Fatal("test setup broken: expected the choice modal to be up")
	}

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if m.encChoice {
		t.Fatal("encChoice should have cleared")
	}
	if !m.skipEncryptedRun {
		t.Fatal("expected skipEncryptedRun to be set by [s]")
	}
	if m.mode != orgRunning {
		t.Fatalf("mode = %v, want orgRunning", m.mode)
	}
	if cmd == nil {
		t.Fatal("[s] should dispatch the run command")
	}
}

// TestOrganizeEncChoice_EscCancels drives [esc]: back to idle, no run, no
// pending decrypt state left behind.
func TestOrganizeEncChoice_EscCancels(t *testing.T) {
	store := organizeEncryptedTestStore(t)
	m := organizeAgentTestModel(store)
	m = toConfirming(m)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.encChoice {
		t.Fatal("test setup broken: expected the choice modal to be up")
	}

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.encChoice {
		t.Fatal("encChoice should have cleared on esc")
	}
	if m.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle", m.mode)
	}
	if m.skipEncryptedRun || m.pendingRestore != nil {
		t.Fatal("esc must not leave any run-side-effect state set")
	}
	if cmd != nil {
		t.Fatal("esc should not dispatch a command")
	}
}

// TestOrganizeEncChoice_YDecryptsThenRuns drives [y]: dispatches
// TempDecryptForOrganize as a tea.Cmd (never blocking Update), and once that
// resolves the run starts with pendingRestore set so the file is
// re-encrypted automatically when the run finishes.
func TestOrganizeEncChoice_YDecryptsThenRuns(t *testing.T) {
	store := organizeEncryptedTestStore(t)
	m := organizeAgentTestModel(store)
	m = toConfirming(m)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.encChoice {
		t.Fatal("test setup broken: expected the choice modal to be up")
	}

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.encChoice {
		t.Fatal("encChoice should have cleared")
	}
	if m.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle until the decrypt resolves", m.mode)
	}
	if cmd == nil {
		t.Fatal("[y] should dispatch the temp-decrypt command")
	}

	// Run the dispatched tea.Cmd synchronously (as bubbletea's runtime would,
	// off the Update goroutine) and feed its result back in.
	msg := cmd()
	decrypted, ok := msg.(orgTempDecryptedMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want orgTempDecryptedMsg", msg)
	}
	if decrypted.err != nil {
		t.Fatalf("TempDecryptForOrganize failed: %v", decrypted.err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if vaultcrypt.IsEncrypted(raw) {
		t.Fatal("personal.md is still encrypted after the [y] decrypt step")
	}

	m, cmd2 := m.Update(decrypted)
	if m.mode != orgRunning {
		t.Fatalf("mode = %v, want orgRunning once decrypt resolves", m.mode)
	}
	if m.pendingRestore == nil {
		t.Fatal("expected pendingRestore to be set so the run's completion re-encrypts")
	}
	if cmd2 == nil {
		t.Fatal("starting the run should dispatch a command")
	}

	// Cancelling the run must still flush the pending restore.
	m, cmd3 := m.updateRunning(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle after cancel", m.mode)
	}
	if m.pendingRestore != nil {
		t.Fatal("cancel must consume pendingRestore, not leave it dangling")
	}
	if cmd3 == nil {
		t.Fatal("cancel must dispatch the restore command")
	}
	restoredMsg, ok := cmd3().(orgRestoredMsg)
	if !ok {
		t.Fatalf("cancel's restore cmd produced %T, want orgRestoredMsg", restoredMsg)
	}
	if restoredMsg.err != nil {
		t.Fatalf("restore after cancel failed: %v", restoredMsg.err)
	}
	raw, err = os.ReadFile(filepath.Join(store.Root, "personal.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !vaultcrypt.IsEncrypted(raw) {
		t.Fatal("personal.md was not re-encrypted after the cancelled run")
	}
}

// TestOrganizeModel_QuitBlockedWhileRestoring is MAJOR 5's regression:
// app.go's global quit routes "q"/"ctrl+c" through here whenever
// capturesInput() is true, so while a temp-decrypt's async re-encrypt is
// still in flight (m.restoring), this must report captures=true AND swallow
// the quit key itself — never fall through to a mode handler that would
// treat it as an ordinary "leave this screen" key.
// TestOrgRunMode_CycleWraps proves h/l cycle the top-of-tab mode selector
// Consolidate → Split projects → Find contradictions and wrap in both
// directions, and that switching modes clears a stale status/error.
func TestOrgRunMode_CycleWraps(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	if m.runMode != orgRunModeConsolidate {
		t.Fatalf("default runMode = %v, want orgRunModeConsolidate", m.runMode)
	}
	m.status = "stale status from a previous run"

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.runMode != orgRunModeSplit {
		t.Fatalf("l once = %v, want orgRunModeSplit", m.runMode)
	}
	if m.status != "" {
		t.Errorf("switching mode must clear stale status, got %q", m.status)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.runMode != orgRunModeContradictions {
		t.Fatalf("l twice = %v, want orgRunModeContradictions", m.runMode)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.runMode != orgRunModeConsolidate {
		t.Fatalf("l three times should wrap back to Consolidate, got %v", m.runMode)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.runMode != orgRunModeContradictions {
		t.Fatalf("h from Consolidate should wrap back to Contradictions, got %v", m.runMode)
	}

	// The arrow keys are the primary (discoverable) mode switch — ← / → must
	// cycle the same way h / l do, since the header advertises "(← → switch)".
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.runMode != orgRunModeConsolidate {
		t.Fatalf("→ from Contradictions should wrap to Consolidate, got %v", m.runMode)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.runMode != orgRunModeContradictions {
		t.Fatalf("← from Consolidate should wrap to Contradictions, got %v", m.runMode)
	}
}

// TestOrgRunMode_SplitEnterStartsRunning proves Enter on Split projects mode
// skips the provider/model picker entirely and drops straight into
// orgRunning with a command dispatched (bubbletea discipline: the LLM call
// happens in that tea.Cmd, never inline in Update).
func TestOrgRunMode_SplitEnterStartsRunning(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.runMode = orgRunModeSplit

	um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if um.mode != orgRunning {
		t.Fatalf("mode = %v, want orgRunning", um.mode)
	}
	if cmd == nil {
		t.Fatal("Enter on Split projects must dispatch the run command")
	}
	if um.runProvider != "Direct LLM" {
		t.Errorf("runProvider = %q, want %q", um.runProvider, "Direct LLM")
	}
}

// TestOrgRunMode_ContradictionsEnterStartsRunning mirrors the split case for
// Find contradictions mode.
func TestOrgRunMode_ContradictionsEnterStartsRunning(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.runMode = orgRunModeContradictions

	um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if um.mode != orgRunning {
		t.Fatalf("mode = %v, want orgRunning", um.mode)
	}
	if cmd == nil {
		t.Fatal("Enter on Find contradictions must dispatch the run command")
	}
	if um.runProvider != "Embeddings + Direct LLM" {
		t.Errorf("runProvider = %q, want %q", um.runProvider, "Embeddings + Direct LLM")
	}
}

// TestOrgRunMode_NonConsolidateIgnoresProviderKeys proves up/down/tab (the
// provider/model picker's keys) are inert while a non-Consolidate mode is
// selected — nothing to pick, so they must not silently mutate m.focus/
// m.provIdx/m.modelIdx behind the (hidden) Consolidate picker.
func TestOrgRunMode_NonConsolidateIgnoresProviderKeys(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.runMode = orgRunModeSplit
	wantFocus, wantProvIdx, wantModelIdx := m.focus, m.provIdx, m.modelIdx

	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyUp}, {Type: tea.KeyDown}, {Type: tea.KeyTab},
		{Type: tea.KeyRunes, Runes: []rune("f")}, {Type: tea.KeyRunes, Runes: []rune("e")},
	} {
		m, _ = m.Update(k)
	}
	if m.focus != wantFocus || m.provIdx != wantProvIdx || m.modelIdx != wantModelIdx {
		t.Fatalf("provider/model picker state changed while in Split mode: focus=%v provIdx=%d modelIdx=%d",
			m.focus, m.provIdx, m.modelIdx)
	}
	if m.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle (none of those keys should start a run)", m.mode)
	}
}

// TestSplitRunSummary_QueuedWithSkipped locks the design-item-3 wording:
// "Queued N addition(s) across M project file(s); K bullet(s) couldn't be
// matched..." — built from a stubbed memory.SplitProjectsResult so this
// doesn't need a live LLM call.
func TestSplitRunSummary_QueuedWithSkipped(t *testing.T) {
	result := memory.SplitProjectsResult{
		Writes:       []memory.PendingWrite{{TargetFile: "projects/auxly.md", Count: 3}, {TargetFile: "projects/widget.md", Count: 2}},
		SkippedCount: 4,
	}
	got := splitRunSummary(result, 5, 2)
	for _, want := range []string{"Queued 5 addition(s) across 2 project file(s)", "4 bullet(s) couldn't be matched", "Approvals (tab 4)", "organize-split"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
}

// TestSplitRunSummary_CleanupOnly and TestSplitRunSummary_NothingToSplit lock
// the two clean (non-error) edge cases runSplitProjects also special-cases.
func TestSplitRunSummary_CleanupOnly(t *testing.T) {
	result := memory.SplitProjectsResult{
		CleanupWrite: &memory.PendingWrite{TargetFile: "projects.md", Count: 3},
		CleanupOnly:  true,
	}
	got := splitRunSummary(result, 0, 0)
	if !strings.Contains(got, "Queued removal of 3 bullet(s)") {
		t.Errorf("summary %q missing the cleanup line", got)
	}
	if strings.Contains(got, "addition(s)") {
		t.Errorf("cleanup-only summary must not mention new additions, got %q", got)
	}
}

func TestSplitRunSummary_NothingToSplit(t *testing.T) {
	got := splitRunSummary(memory.SplitProjectsResult{NothingToSplit: true}, 0, 0)
	if !strings.Contains(got, "Nothing to split") {
		t.Errorf("summary %q, want the nothing-to-split message", got)
	}
}

// TestOrgSplitRunMsg_SetsStatusAndReturnsIdle proves the TUI wiring: an
// orgSplitRunMsg arriving while running returns the model to orgIdle and
// surfaces its summary — mirroring how orgRunMsg/orgModelsFetchedMsg are
// exercised elsewhere in this file (feed the async result message straight
// into Update rather than mocking the LLM call itself).
func TestOrgSplitRunMsg_SetsStatusAndReturnsIdle(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.mode = orgRunning
	m.runCancel = func() {}

	um, _ := m.Update(orgSplitRunMsg{summary: "Queued 1 addition(s) across 1 project file(s). Review in Approvals (tab 4)."})
	if um.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle", um.mode)
	}
	if !strings.Contains(um.status, "Review in Approvals") {
		t.Errorf("status = %q, want the run summary", um.status)
	}
	if um.errMsg != "" {
		t.Errorf("errMsg = %q, want empty on success", um.errMsg)
	}

	// A late result after the user already left orgRunning must be dropped.
	um2 := um
	um2.mode = orgIdle
	dropped, _ := um2.Update(orgSplitRunMsg{summary: "should be ignored"})
	if dropped.status == "should be ignored" {
		t.Error("a late orgSplitRunMsg after mode left orgRunning must be dropped")
	}
}

// TestOrgContradictionsRunMsg_SetsStatusAndReturnsIdle mirrors the split
// case, plus the error path (errMsg, not status).
func TestOrgContradictionsRunMsg_SetsStatusAndReturnsIdle(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.mode = orgRunning
	m.runCancel = func() {}

	um, _ := m.Update(orgContradictionsRunMsg{summary: "Queued 2 contradiction/duplicate finding(s) as pending; review in Approvals (tab 4)."})
	if um.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle", um.mode)
	}
	if !strings.Contains(um.status, "Queued 2 contradiction") {
		t.Errorf("status = %q, want the run summary", um.status)
	}

	m2 := newOrganizeModel(store, store.Root, nil)
	m2.mode = orgRunning
	m2.runCancel = func() {}
	um2, _ := m2.Update(orgContradictionsRunMsg{err: "boom"})
	if um2.mode != orgIdle {
		t.Fatalf("mode = %v, want orgIdle even on error", um2.mode)
	}
	if um2.errMsg != "boom" {
		t.Errorf("errMsg = %q, want %q", um2.errMsg, "boom")
	}
	if um2.status != "" {
		t.Errorf("status = %q, want empty on error", um2.status)
	}
}

// TestContradictionsErrSummary_EmbeddingsUnavailable and
// TestContradictionsErrSummary_VaultTooLarge lock the embeddings-unavailable
// / vault-too-large messages mirroring cmd/organize.go's, deterministically
// (no live embedder/network needed — contradictionsErrSummary is a pure
// function over the sentinel errors).
func TestContradictionsErrSummary_EmbeddingsUnavailable(t *testing.T) {
	summary, ok := contradictionsErrSummary(fmt.Errorf("wrap: %w", embed.ErrUnavailable))
	if !ok {
		t.Fatal("embed.ErrUnavailable must be a clean stop (ok=true), not a hard error")
	}
	if !strings.Contains(summary, "needs embeddings") {
		t.Errorf("summary = %q, want it to mention embeddings", summary)
	}
}

func TestContradictionsErrSummary_VaultTooLarge(t *testing.T) {
	summary, ok := contradictionsErrSummary(fmt.Errorf("swept 900 facts: %w", memory.ErrVaultTooLarge))
	if !ok {
		t.Fatal("memory.ErrVaultTooLarge must be a clean stop (ok=true), not a hard error")
	}
	if !strings.Contains(summary, "900 facts") {
		t.Errorf("summary = %q, want the original error text preserved", summary)
	}
}

func TestContradictionsErrSummary_OtherErrorIsHardFailure(t *testing.T) {
	_, ok := contradictionsErrSummary(fmt.Errorf("model call failed"))
	if ok {
		t.Fatal("a non-sentinel error must be a hard failure (ok=false), routed to errMsg")
	}
}

func TestOrganizeModel_QuitBlockedWhileRestoring(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.mode = orgReview // a mode whose OWN "q" handler would normally reset the review
	m.restoring = true

	if !m.capturesInput() {
		t.Fatal("capturesInput() must be true while a restore is in flight")
	}

	um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !um.restoring {
		t.Fatal("q while restoring must not clear restoring itself — only orgRestoredMsg may")
	}
	if um.mode != orgReview {
		t.Fatalf("mode = %v, want orgReview unchanged — q must not fall through to updateReview's reset", um.mode)
	}
	if cmd != nil {
		t.Fatal("q while restoring must not dispatch a command (e.g. quit)")
	}
	if !strings.Contains(um.status, "wait") {
		t.Fatalf("status = %q, want a re-encrypting/please-wait message", um.status)
	}
}

// TestOrganizeModel_OrgRestoredMsgClearsRestoring proves the flag clears once
// the async re-encrypt actually lands, so quit/tab-switch unblock again.
func TestOrganizeModel_OrgRestoredMsgClearsRestoring(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.mode = orgIdle
	m.restoring = true

	um, _ := m.Update(orgRestoredMsg{files: []string{"personal.md"}})
	if um.restoring {
		t.Fatal("orgRestoredMsg must clear restoring")
	}
	if um.capturesInput() {
		t.Fatal("capturesInput() should release once restoring clears (mode is orgIdle)")
	}
}

func organizeTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dir := t.TempDir()
	store := memory.NewStore(dir)
	store.WorkspaceRoot = ""
	if err := store.Write("identity.md", "# Identity\n- Name: Old\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("projects.md", "# Projects\n- Alpha\n"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(dir, ".last_organize.json")); err != nil && !os.IsNotExist(err) {
			t.Fatalf("unexpected stats file state: %v", err)
		}
	})
	return store
}

func organizeReviewTestModel(store *memory.Store) organizeModel {
	m := newOrganizeModel(store, store.Root, nil)
	m.width = 100
	m.height = 30
	m.mode = orgReview
	m.proposal = memory.OrganizeProposal{ModelUsed: "test-model", TokensUsed: 42}
	m.changes = []memory.ProposedChange{
		{
			Name:       "identity.md",
			OldContent: "# Identity\n- Name: Old\n",
			NewContent: "# Identity\n- Name: New\n",
			Scope:      "global",
		},
		{
			Name:       "projects.md",
			OldContent: "# Projects\n- Alpha\n",
			NewContent: "# Projects\n- Alpha\n- Added\n",
			Scope:      "global",
		},
	}
	m.decisions = []orgDecision{decPending, decPending}
	m.loadCurrentChange()
	return m
}

// TestOrganizeForceKey: F on the Consolidate idle screen arms a forced re-run
// (bypasses the dirty-file ledger) and jumps to confirmation, so a prior run's
// "Nothing to organize" is never a dead end.
func TestOrganizeForceKey(t *testing.T) {
	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	if !m.forceRun {
		t.Fatal("F must set forceRun")
	}
	if !m.confirming {
		t.Fatal("F must jump to the confirmation step")
	}
	// startRun captures forceRun for the run, then clears it so the next run
	// isn't silently forced.
	m2, _ := m.startRun()
	if m2.forceRun {
		t.Fatal("forceRun must be cleared after the run is dispatched (one-shot)")
	}
}
