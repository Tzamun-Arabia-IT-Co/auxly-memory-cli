package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	tea "github.com/charmbracelet/bubbletea"
)

// TestParseHooksStatus is the pure-function guard for turning `auxly hooks
// status`'s fixed-width table into rows: the header line must never parse as
// data, and each real status token must be captured with its detail intact.
func TestParseHooksStatus(t *testing.T) {
	out := "🔌 Capture hook status\n" +
		"   AGENT    STATUS         DETAIL\n" +
		"   claude   WIRED          /Users/x/.claude/settings.json\n" +
		"   codex    not-installed  run `auxly hooks install --agent codex`\n" +
		"   gemini   manual         some other reason\n"

	rows := parseHooksStatus(out)
	if len(rows) != 3 {
		t.Fatalf("parseHooksStatus: got %d rows, want 3 (header must be excluded): %+v", len(rows), rows)
	}
	if rows[0].agent != "claude" || rows[0].status != "WIRED" || rows[0].detail != "/Users/x/.claude/settings.json" {
		t.Errorf("row 0 = %+v, want claude/WIRED/path", rows[0])
	}
	if rows[1].agent != "codex" || rows[1].status != "not-installed" {
		t.Errorf("row 1 = %+v, want codex/not-installed", rows[1])
	}
	if rows[2].agent != "gemini" || rows[2].status != "manual" {
		t.Errorf("row 2 = %+v, want gemini/manual", rows[2])
	}
}

// TestOpsPanelRendersHooksStatus is the "status render" half of the audit
// requirement: the loaded rows must actually show up in the panel with their
// state badge.
func TestOpsPanelRendersHooksStatus(t *testing.T) {
	m := newOpsModel(t.TempDir())
	m.width, m.height = 120, 50
	m, _ = m.Update(opsRefreshMsg{rows: []hookStatusRow{
		{agent: "claude", status: "WIRED", detail: "/x/settings.json"},
		{agent: "codex", status: "not-installed", detail: "run `auxly hooks install --agent codex`"},
	}})

	view := stripANSI(m.panel())
	for _, want := range []string{"claude", "[WIRED]", "codex", "[not-installed]"} {
		if !strings.Contains(view, want) {
			t.Errorf("ops panel missing %q:\n%s", want, view)
		}
	}
}

// resolveActionMsg runs cmd (a tea.Batch of the action call + the spinner
// tick, mirroring vaultModel's tea.Batch(actionCmd, spinTick) shape) and
// returns the opsActionMsg among its results.
func resolveActionMsg(t *testing.T, cmd tea.Cmd) opsActionMsg {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		if am, ok := msg.(opsActionMsg); ok {
			return am
		}
		t.Fatalf("expected a tea.BatchMsg or opsActionMsg, got %T", msg)
	}
	for _, sub := range batch {
		if am, ok := sub().(opsActionMsg); ok {
			return am
		}
	}
	t.Fatal("no opsActionMsg found among the batched commands")
	return opsActionMsg{}
}

// TestOpsInstallTriggersCmdAndUpdatesRows is the "install action triggers the
// right cmd/state" requirement: [i] on a not-installed row busies the model
// and dispatches a command; folding in the (stubbed) result clears busy,
// shows a status line, and re-requests the hooks table.
func TestOpsInstallTriggersCmdAndUpdatesRows(t *testing.T) {
	orig := runAuxlySub
	defer func() { runAuxlySub = orig }()
	runAuxlySub = func(memPath string, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "hooks" && args[1] == "install" {
			return "✅ codex notify hook installed.\n", nil
		}
		return "", errors.New("unexpected call: " + strings.Join(args, " "))
	}

	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.hooksRows = []hookStatusRow{{agent: "codex", status: "not-installed"}}
	m.cursor = 0

	m, cmd := m.handleKey(keyRunes("i"))
	if !m.busy {
		t.Fatal("[i] on a not-installed row should enter the busy state")
	}
	if cmd == nil {
		t.Fatal("[i] should dispatch the install command")
	}

	m, _ = m.Update(resolveActionMsg(t, cmd))
	if m.busy {
		t.Fatal("busy should clear once the install result lands")
	}
	if m.statusErr {
		t.Fatalf("status should not be an error: %q", m.status)
	}
	if !strings.Contains(m.status, "installed") {
		t.Fatalf("status = %q, want it to mention the install result", m.status)
	}
}

// TestOpsUninstallIsConfirmGated guards the destructive-op gate: [u] on a
// WIRED row must not remove anything until a subsequent y/enter confirms it.
func TestOpsUninstallIsConfirmGated(t *testing.T) {
	orig := runAuxlySub
	defer func() { runAuxlySub = orig }()
	called := false
	runAuxlySub = func(memPath string, args ...string) (string, error) {
		called = true
		return "🗑️  auxly capture hook removed.\n", nil
	}

	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.hooksRows = []hookStatusRow{{agent: "claude", status: "WIRED"}}
	m.cursor = 0

	m, cmd := m.handleKey(keyRunes("u"))
	if m.mode != "confirmUninstall" {
		t.Fatalf("mode after [u] = %q, want confirmUninstall", m.mode)
	}
	if cmd != nil || called {
		t.Fatal("[u] must not remove the hook before confirmation")
	}

	// Cancel first: must return to idle without ever calling out.
	m, _ = m.handleKey(keyRunes("n"))
	if m.mode != "" || called {
		t.Fatal("[n] should cancel without running the uninstall")
	}

	// Re-enter and confirm.
	m, _ = m.handleKey(keyRunes("u"))
	m, cmd = m.handleKey(keyRunes("y"))
	if !m.busy || cmd == nil {
		t.Fatal("[y] after [u] should busy the model and dispatch the uninstall")
	}
	resolveActionMsg(t, cmd) // drives the stub
	if !called {
		t.Fatal("confirming should have invoked the uninstall subprocess")
	}
}

// TestOpsSyncIsConfirmGated guards the outward-push confirm gate: [s] alone
// must not touch git; only y/enter dispatches the sync command.
func TestOpsSyncIsConfirmGated(t *testing.T) {
	m := newOpsModel(t.TempDir())
	m.loaded = true

	m, cmd := m.handleKey(keyRunes("s"))
	if m.mode != "confirmSync" {
		t.Fatalf("mode after [s] = %q, want confirmSync", m.mode)
	}
	if cmd != nil {
		t.Fatal("[s] alone must not dispatch the sync command")
	}
	if m.capturesInput() != true {
		t.Fatal("a confirm prompt must capture input (block global digit/quit keys)")
	}

	m, cmd = m.handleKey(keyRunes("y"))
	if !m.busy {
		t.Fatal("[y] after [s] should enter the busy state")
	}
	if cmd == nil {
		t.Fatal("[y] after [s] should dispatch a tea.Cmd running the sync")
	}
}

// TestSyncStatusText covers the four distinct outcomes the audit asked for:
// pushed / nothing-to-push / refused (sentinel + not-configured) / error.
func TestSyncStatusText(t *testing.T) {
	cases := []struct {
		name    string
		res     git.SyncResult
		err     error
		wantSub string
		wantErr bool
	}{
		{"pushed", git.SyncResult{Pushed: true}, nil, "pushed", false},
		{"nothing", git.SyncResult{Pushed: false}, nil, "nothing to push", false},
		{"sentinel", git.SyncResult{}, errors.New("skipped sync: a temporary decrypt is in progress — retry once the organize run finishes"), "temporary decrypt is in progress", true},
		{"not configured", git.SyncResult{}, errors.New("memory folder is not a git repository. Run 'git init' in /x first"), "not set up", true},
		{"other error", git.SyncResult{}, errors.New("git push failed: some other failure"), "sync error", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, isErr := syncStatusText(tc.res, tc.err)
			if isErr != tc.wantErr {
				t.Errorf("statusErr = %v, want %v", isErr, tc.wantErr)
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("text = %q, want it to contain %q", got, tc.wantSub)
			}
		})
	}
}

// TestOpsDoctorViewOpensAndDismisses covers the "doctor view opens/renders"
// requirement: [d] busies + dispatches, the captured report becomes the
// panel body, and any key returns to the normal Ops view (mirrors
// sshModePrint's "any key dismisses").
func TestOpsDoctorViewOpensAndDismisses(t *testing.T) {
	orig := runAuxlySub
	defer func() { runAuxlySub = orig }()
	runAuxlySub = func(memPath string, args ...string) (string, error) {
		return "🩺 Auxly doctor\n   ✓ memory initialized\n", nil
	}

	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.width, m.height = 120, 50

	m, cmd := m.handleKey(keyRunes("d"))
	if !m.busy || cmd == nil {
		t.Fatal("[d] should busy the model and dispatch the doctor command")
	}
	m, _ = m.Update(resolveActionMsg(t, cmd))
	if !m.viewingDoctor {
		t.Fatal("doctor result should open the report view")
	}
	if !strings.Contains(m.panel(), "memory initialized") {
		t.Fatalf("doctor panel should render the captured report:\n%s", m.panel())
	}

	m, _ = m.handleKey(keyRunes("x"))
	if m.viewingDoctor {
		t.Fatal("any key should dismiss the doctor report view")
	}
}

// TestOpsCapturesInputBlocksGlobalKeys is the app.go-routing regression this
// task explicitly warns against reintroducing (the vault/suggest bug): while
// Ops owns the keyboard (a confirm prompt or a busy action), the top-level
// digit/quit switch in app.go must not hijack those keys.
func TestOpsCapturesInputBlocksGlobalKeys(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	m.screen = screenSettings
	m.settings.subTab = 4
	m.settings.ops.loaded = true
	m.settings.ops.hooksRows = []hookStatusRow{{agent: "claude", status: "WIRED"}}
	m.settings.ops.mode = "confirmSync"

	for _, key := range []tea.KeyMsg{keyRunes("1"), keyRunes("q"), {Type: tea.KeyCtrlC}} {
		result, cmd := m.Update(key)
		next, ok := result.(model)
		if !ok {
			t.Fatalf("Update returned %T, want model", result)
		}
		if next.screen != screenSettings {
			t.Fatalf("key %q changed screen to %v, want it to stay on Settings", key.String(), next.screen)
		}
		if cmd != nil {
			t.Fatalf("key %q dispatched a command (e.g. tea.Quit) instead of being absorbed by the Ops confirm prompt", key.String())
		}
		m = next
	}
	// The confirm prompt must still be exactly where it was — none of the
	// "global" keys above should have been read as y/n/esc either.
	if m.settings.ops.mode != "confirmSync" {
		t.Fatalf("ops.mode = %q, want confirmSync untouched", m.settings.ops.mode)
	}
}
