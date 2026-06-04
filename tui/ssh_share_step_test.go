package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

// TestMethodRelayIsFirst locks the reordered method picker: pressing "1" on the
// method step now selects relay (the primary flow), not lan.
func TestMethodRelayIsFirst(t *testing.T) {
	m := sshModel{mode: sshModeForm, formStep: formStepMethod}
	m, _ = m.handleFormKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.formMethod != "relay" {
		t.Errorf("method key 1 should select relay, got %q", m.formMethod)
	}
	if m.formStep != formStepHost {
		t.Errorf("choosing a method should advance to the host step, got %q", m.formStep)
	}

	// And "2" is now lan (everything shifted down by one).
	m2 := sshModel{mode: sshModeForm, formStep: formStepMethod}
	m2, _ = m2.handleFormKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m2.formMethod != "lan" {
		t.Errorf("method key 2 should select lan, got %q", m2.formMethod)
	}
}

// TestRelayWizardReachesPermissionsStep locks the requested flow: for relay, the name
// step advances to the in-wizard permissions picker (seeded with sane defaults) before
// any connect; a consumer method submits straight from the name step instead.
func TestRelayWizardReachesPermissionsStep(t *testing.T) {
	// Relay: name → permissions step, seeded read-only for non-personal, personal Off.
	m := sshModel{mode: sshModeForm, formStep: formStepName, formMethod: "relay", formHost: "relay.example:22", formName: "BoxX"}
	m, _ = m.advanceForm()
	if m.formStep != formStepShare {
		t.Fatalf("relay name step should advance to the permissions step, got %q", m.formStep)
	}
	if len(m.shareFiles) == 0 || m.shareState == nil {
		t.Fatal("the permissions step must seed the file list + tri-state")
	}
	if m.shareState["personal.md"] != shareOff {
		t.Error("personal.md must default to Off in the permissions step")
	}
	// Non-personal files default to Read+Write (a connected box is a full peer).
	first := m.shareFiles[0]
	if memory.IsPersonalFile(first) {
		first = m.shareFiles[1]
	}
	if m.shareState[first] != shareReadWrite {
		t.Errorf("non-personal files must default to Read+Write, got %d for %q", m.shareState[first], first)
	}

	// ←/→ cycles the highlighted row to a different state (here Read+Write → Off).
	m.shareCursor = 0
	before := m.shareState[m.shareFiles[0]]
	m, _ = m.handleFormShareKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.shareState[m.shareFiles[0]] == before {
		t.Error("→ should change the highlighted file's access state")
	}

	// Consumer method: the name step submits, never reaching the permissions step.
	c := sshModel{mode: sshModeForm, formStep: formStepName, formMethod: "lan", formHost: "user@10.0.0.5", formOS: "linux", formName: "Srv"}
	c, _ = c.advanceForm()
	if c.formStep == formStepShare {
		t.Error("a consumer method must not enter the permissions step")
	}
}

// TestShareSelectionSplitsTriState verifies the tri-state map collapses to the
// shared/writable lists clients.yaml stores.
func TestShareSelectionSplitsTriState(t *testing.T) {
	files := []string{"identity.md", "projects.md", "personal.md"}
	state := map[string]int{"identity.md": shareRead, "projects.md": shareReadWrite, "personal.md": shareOff}
	shared, writes := shareSelection(files, state)
	if len(shared) != 2 || shared[0] != "identity.md" || shared[1] != "projects.md" {
		t.Errorf("shared should be the two non-Off files in order, got %v", shared)
	}
	if len(writes) != 1 || writes[0] != "projects.md" {
		t.Errorf("writes should be only the Read+Write file, got %v", writes)
	}
}

// TestNewlyAddedClient covers the diff used to attach the post-connect share step to
// the freshly provisioned box: a brand-new name is found by diff; a re-add (name was
// already present) falls back to an exact name match; nothing matches → ok=false.
func TestNewlyAddedClient(t *testing.T) {
	before := clientNameSet([]clientRow{{Name: "BoxA"}})
	after := []clientRow{{Name: "BoxA"}, {Name: "BoxB", Target: "root@1.2.3.4"}}

	nc, ok := newlyAddedClient(after, before, "BoxB")
	if !ok || nc.Name != "BoxB" {
		t.Fatalf("the new client BoxB should be found by diff, got %+v ok=%v", nc, ok)
	}

	// Re-add: BoxA already existed, so the diff is empty — fall back to the typed name.
	nc, ok = newlyAddedClient([]clientRow{{Name: "BoxA"}}, before, "BoxA")
	if !ok || nc.Name != "BoxA" {
		t.Fatalf("a re-add should resolve via the name fallback, got %+v ok=%v", nc, ok)
	}

	// Nothing new and no usable name → no client to share with.
	if _, ok := newlyAddedClient([]clientRow{{Name: "BoxA"}}, before, ""); ok {
		t.Error("with no new client and no name, newlyAddedClient must report ok=false")
	}
}
