package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
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
