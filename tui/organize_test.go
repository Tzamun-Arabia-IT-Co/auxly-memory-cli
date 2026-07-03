package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

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

	provider, command, model := m.planTarget()
	if provider != "Claude Code / CLI" || command != "/bin/echo" || model != "sonnet" {
		t.Fatalf("agent plan target = (%q, %q, %q), want Claude /bin/echo sonnet", provider, command, model)
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
