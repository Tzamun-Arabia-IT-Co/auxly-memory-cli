package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"claude", "[WIRED]", "codex", "[not-installed]", "Backup (git)"} {
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

func resolveSetupMsg(t *testing.T, cmd tea.Cmd) opsBackupSetupMsg {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		if sm, ok := msg.(opsBackupSetupMsg); ok {
			return sm
		}
		t.Fatalf("expected tea.BatchMsg or opsBackupSetupMsg, got %T", msg)
	}
	for _, sub := range batch {
		if sm, ok := sub().(opsBackupSetupMsg); ok {
			return sm
		}
	}
	t.Fatal("no opsBackupSetupMsg found among the batched commands")
	return opsBackupSetupMsg{}
}

func resolveAutoBackupMsg(t *testing.T, cmd tea.Cmd) opsAutoBackupMsg {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		if am, ok := msg.(opsAutoBackupMsg); ok {
			return am
		}
		t.Fatalf("expected tea.BatchMsg or opsAutoBackupMsg, got %T", msg)
	}
	for _, sub := range batch {
		if am, ok := sub().(opsAutoBackupMsg); ok {
			return am
		}
	}
	t.Fatal("no opsAutoBackupMsg found among the batched commands")
	return opsAutoBackupMsg{}
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

// TestOpsUninstallIsConfirmGated guards the destructive-op gate: [u] (when
// backup is configured) or [x] on a WIRED row must not remove anything until a
// subsequent y/enter confirms it.
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
	m.backup = git.Status{Configured: true, RemoteURL: "/tmp/bare.git", Branch: "main"}
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

	// Re-enter via [x] (which also works when backup is not configured) and confirm.
	m.backup = git.Status{Configured: false}
	m, _ = m.handleKey(keyRunes("x"))
	if m.mode != "confirmUninstall" {
		t.Fatalf("mode after [x] = %q, want confirmUninstall", m.mode)
	}
	m, cmd = m.handleKey(keyRunes("y"))
	if !m.busy || cmd == nil {
		t.Fatal("[y] after [x] should busy the model and dispatch the uninstall")
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
	if m.settings.ops.mode != "confirmSync" {
		t.Fatalf("ops.mode = %q, want confirmSync untouched", m.settings.ops.mode)
	}
}

// TestOpsURLInputOpensOnUAndDigitsDoNotSwitchTabs verifies that pressing `u`
// when backup is not set up opens the URL text input, and typing digits or `q`
// writes into the text input rather than switching tabs or quitting.
func TestOpsURLInputOpensOnUAndDigitsDoNotSwitchTabs(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	m.screen = screenSettings
	m.settings.subTab = 4
	m.settings.ops.loaded = true
	m.settings.ops.hooksRows = []hookStatusRow{{agent: "claude", status: "WIRED"}}

	// Press 'u' to open URL input.
	res, _ := m.Update(keyRunes("u"))
	m = res.(model)
	if m.settings.ops.mode != "inputURL" {
		t.Fatalf("ops.mode after [u] = %q, want inputURL", m.settings.ops.mode)
	}
	if !m.settings.ops.capturesInput() {
		t.Fatal("inputURL mode must capture input")
	}

	// Type URL containing digits (1, 2, 7, 0) and 'q' — must land in urlInput, never switch screens!
	typed := "ssh://git@127.0.0.1:2222/owner/qvault.git"
	for _, r := range typed {
		next, cmd := m.Update(keyRunes(string(r)))
		m = next.(model)
		if m.screen != screenSettings {
			t.Fatalf("typing %q switched screen from Settings to %v", string(r), m.screen)
		}
		if m.settings.subTab != 4 {
			t.Fatalf("typing %q switched Settings subTab to %d", string(r), m.settings.subTab)
		}
		_ = cmd
	}
	if got := m.settings.ops.urlInput.Value(); got != typed {
		t.Fatalf("urlInput.Value() = %q, want %q", got, typed)
	}

	// Esc cancels cleanly.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.settings.ops.mode != "" {
		t.Fatalf("ops.mode after Esc = %q, want empty", m.settings.ops.mode)
	}
}

func TestOpsGHInputOnlyWhenGHAvailableAndDefaultsToAuxlyVault(t *testing.T) {
	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.width, m.height = 120, 40
	m.ghAvail = false

	if strings.Contains(stripANSI(m.panel()), "[g]") {
		t.Fatal("panel should hide [g] when ghAvailable() is false")
	}
	m, _ = m.handleKey(keyRunes("g"))
	if m.mode != "" {
		t.Fatalf("[g] when ghAvail=false entered mode %q, want idle", m.mode)
	}

	m.ghAvail = true
	if !strings.Contains(stripANSI(m.panel()), "[g]") {
		t.Fatal("panel should show [g] when ghAvailable() is true")
	}
	m, _ = m.handleKey(keyRunes("g"))
	if m.mode != "inputGH" {
		t.Fatalf("mode after [g] = %q, want inputGH", m.mode)
	}
	if got := m.ghInput.Value(); got != "auxly-vault" {
		t.Fatalf("default GitHub repo name = %q, want auxly-vault", got)
	}
}

func TestOpsPrePushFindingsConfirmAndCancelInPage(t *testing.T) {
	origSetup := opsBackupSetup
	origStatus := opsBackupStatus
	defer func() {
		opsBackupSetup = origSetup
		opsBackupStatus = origStatus
	}()

	var seenOpts []git.SetupOptions
	opsBackupSetup = func(memPath, remoteURL string, opts git.SetupOptions) (git.SetupResult, error) {
		seenOpts = append(seenOpts, opts)
		rep := git.Report{
			SensitiveFiles:  []git.SensitiveFile{{File: "infra.md", Count: 2}},
			ExistingCommits: 3,
			Visibility:      git.VisibilityPrivate,
		}
		if !opts.Confirmed {
			return git.SetupResult{
				RemoteURL: remoteURL,
				Branch:    "main",
				Blocked:   true,
				Report:    rep,
			}, fmt.Errorf("%w: sensitive files", git.ErrPrePushBlocked)
		}
		return git.SetupResult{
			RemoteURL: remoteURL,
			Branch:    "main",
			Pushed:    true,
			Report:    rep,
		}, nil
	}
	opsBackupStatus = func(memPath string) git.Status {
		if len(seenOpts) > 0 && seenOpts[len(seenOpts)-1].Confirmed {
			return git.Status{Configured: true, RemoteURL: "/tmp/bare.git", Branch: "main"}
		}
		return git.Status{}
	}

	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.width, m.height = 120, 40

	m, _ = m.handleKey(keyRunes("u"))
	m.urlInput.SetValue("/tmp/bare.git")
	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy || cmd == nil {
		t.Fatal("Enter in inputURL should enter busy state and dispatch setup tea.Cmd")
	}

	m, _ = m.Update(resolveSetupMsg(t, cmd))
	if m.mode != "confirmPrePush" {
		t.Fatalf("mode = %q, want confirmPrePush", m.mode)
	}
	view := stripANSI(m.panel())
	if !strings.Contains(view, "infra.md") || !strings.Contains(view, "3 earlier commits will be pushed as they are") || !strings.Contains(view, "y push anyway / n cancel") {
		t.Fatalf("confirmPrePush view missing findings or prompt:\n%s", view)
	}

	// Confirm with 'y': dispatches Setup with Confirmed=true.
	m, cmd = m.handleKey(keyRunes("y"))
	if !m.busy || cmd == nil {
		t.Fatal("[y] on confirmPrePush should dispatch confirmed setup")
	}
	m, _ = m.Update(resolveSetupMsg(t, cmd))
	if len(seenOpts) < 2 || !seenOpts[1].Confirmed || seenOpts[1].AllowPublic {
		t.Fatalf("expected Confirmed=true and AllowPublic=false when report was private, got: %+v", seenOpts[1])
	}
	if !m.backup.Configured {
		t.Fatal("expected backup to be configured after confirming pre-push findings")
	}
}

func TestOpsPrePushFindingsConfirmPublicRepo_PassesAllowPublic(t *testing.T) {
	origSetup := opsBackupSetup
	origStatus := opsBackupStatus
	defer func() {
		opsBackupSetup = origSetup
		opsBackupStatus = origStatus
	}()

	var seenOpts []git.SetupOptions
	opsBackupSetup = func(memPath, remoteURL string, opts git.SetupOptions) (git.SetupResult, error) {
		seenOpts = append(seenOpts, opts)
		rep := git.Report{
			Visibility: git.VisibilityPublic,
		}
		if !opts.AllowPublic {
			return git.SetupResult{
				RemoteURL: remoteURL,
				Branch:    "main",
				Blocked:   true,
				Report:    rep,
			}, fmt.Errorf("%w: public remote repository", git.ErrPrePushBlocked)
		}
		return git.SetupResult{
			RemoteURL: remoteURL,
			Branch:    "main",
			Pushed:    true,
			Report:    rep,
		}, nil
	}
	opsBackupStatus = func(memPath string) git.Status {
		if len(seenOpts) > 0 && seenOpts[len(seenOpts)-1].AllowPublic {
			return git.Status{Configured: true, RemoteURL: "/tmp/bare.git", Branch: "main"}
		}
		return git.Status{}
	}

	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.width, m.height = 120, 40

	m, _ = m.handleKey(keyRunes("u"))
	m.urlInput.SetValue("https://github.com/someone/public-vault.git")
	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy || cmd == nil {
		t.Fatal("Enter in inputURL should enter busy state and dispatch setup tea.Cmd")
	}

	m, _ = m.Update(resolveSetupMsg(t, cmd))
	if m.mode != "confirmPrePush" {
		t.Fatalf("mode = %q, want confirmPrePush", m.mode)
	}

	// Confirm with 'y': dispatches Setup with Confirmed=true AND AllowPublic=true because report was public.
	m, cmd = m.handleKey(keyRunes("y"))
	if !m.busy || cmd == nil {
		t.Fatal("[y] on confirmPrePush should dispatch confirmed setup")
	}
	m, _ = m.Update(resolveSetupMsg(t, cmd))
	if len(seenOpts) < 2 || !seenOpts[1].Confirmed || !seenOpts[1].AllowPublic {
		t.Fatalf("expected Confirmed=true and AllowPublic=true when report was public, got: %+v", seenOpts[1])
	}
	if !m.backup.Configured {
		t.Fatal("expected backup to be configured after confirming public repo pre-push findings")
	}
}

func TestOpsConfiguredStateStripsCredentialsAndTogglesAutoBackup(t *testing.T) {
	origSetAuto := opsSetAutoBackup
	origStatus := opsBackupStatus
	defer func() {
		opsSetAutoBackup = origSetAuto
		opsBackupStatus = origStatus
	}()

	autoOn := false
	opsSetAutoBackup = func(memPath string, enabled bool) error {
		autoOn = enabled
		return nil
	}
	opsBackupStatus = func(memPath string) git.Status {
		return git.Status{
			IsRepo:         true,
			Configured:     true,
			RemoteURL:      "https://github.com/owner/auxly-vault.git",
			Branch:         "main",
			AutoBackup:     autoOn,
			LastSuccess:    time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC),
			UnpushedCount:  2,
			DirtyFileCount: 1,
		}
	}

	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.width, m.height = 120, 40
	m.backup = git.Status{
		IsRepo:         true,
		Configured:     true,
		RemoteURL:      "https://x:ghp_secret123456789012345678901234@github.com/owner/auxly-vault.git",
		Branch:         "main",
		AutoBackup:     false,
		LastSuccess:    time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC),
		UnpushedCount:  2,
		DirtyFileCount: 1,
	}

	view := stripANSI(m.panel())
	if strings.Contains(view, "ghp_secret") {
		t.Fatalf("panel leaked remote URL credentials:\n%s", view)
	}
	for _, want := range []string{"https://github.com/owner/auxly-vault.git", "main", "OFF", "Unpushed: 2", "Dirty: 1"} {
		if !strings.Contains(view, want) {
			t.Errorf("configured Backup panel missing %q:\n%s", want, view)
		}
	}

	m, cmd := m.handleKey(keyRunes("b"))
	if !m.busy || cmd == nil {
		t.Fatal("[b] should busy model and dispatch auto-backup toggle cmd")
	}
	m, _ = m.Update(resolveAutoBackupMsg(t, cmd))
	if !m.backup.AutoBackup {
		t.Fatal("expected AutoBackup=true after toggling with [b]")
	}
}

func TestOpsGHCreatePrePushFindingsConfirmRerunsWithConfirmed(t *testing.T) {
	origCreateGH := opsBackupCreateGH
	origStatus := opsBackupStatus
	defer func() {
		opsBackupCreateGH = origCreateGH
		opsBackupStatus = origStatus
	}()

	var seenNames []string
	var seenOpts []git.SetupOptions

	opsBackupCreateGH = func(memPath, name string, opts git.SetupOptions) (git.SetupResult, error) {
		seenNames = append(seenNames, name)
		seenOpts = append(seenOpts, opts)
		rep := git.Report{
			SensitiveFiles: []git.SensitiveFile{{File: "infra.md", Count: 1}},
			Visibility:     git.VisibilityUnknown,
		}
		if !opts.Confirmed {
			return git.SetupResult{
				Branch:  "main",
				Blocked: true,
				Report:  rep,
			}, fmt.Errorf("%w: sensitive files", git.ErrPrePushBlocked)
		}
		return git.SetupResult{
			RemoteURL: "https://github.com/owner/my-vault.git",
			Branch:    "main",
			Pushed:    true,
			Report:    rep,
		}, nil
	}

	opsBackupStatus = func(memPath string) git.Status {
		if len(seenOpts) > 0 && seenOpts[len(seenOpts)-1].Confirmed {
			return git.Status{Configured: true, RemoteURL: "https://github.com/owner/my-vault.git", Branch: "main"}
		}
		return git.Status{}
	}

	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.width, m.height = 120, 40
	m.ghAvail = true

	// Open GitHub create input via 'g'
	m, _ = m.handleKey(keyRunes("g"))
	if m.mode != "inputGH" {
		t.Fatalf("mode after [g] = %q, want inputGH", m.mode)
	}
	m.ghInput.SetValue("my-vault")

	// Press Enter to submit
	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy || cmd == nil {
		t.Fatal("Enter in inputGH should enter busy state and dispatch create tea.Cmd")
	}

	// Update with setup msg (blocked by pre-push)
	m, _ = m.Update(resolveSetupMsg(t, cmd))
	if m.mode != "confirmPrePush" {
		t.Fatalf("mode = %q, want confirmPrePush", m.mode)
	}
	if m.pendingGHRepo != "my-vault" {
		t.Fatalf("pendingGHRepo = %q, want my-vault", m.pendingGHRepo)
	}

	// Confirm with 'y': MUST rerun create flow with Confirmed=true, NOT silently return
	m, cmd = m.handleKey(keyRunes("y"))
	if !m.busy || cmd == nil {
		t.Fatal("[y] on confirmPrePush after GitHub create must enter busy state and dispatch confirmed create command")
	}

	// Resolve the confirmed setup msg
	m, _ = m.Update(resolveSetupMsg(t, cmd))
	if !m.backup.Configured {
		t.Fatal("expected backup to be configured after confirming GitHub create pre-push findings")
	}

	if len(seenOpts) != 2 {
		t.Fatalf("expected 2 calls to opsBackupCreateGH, got %d", len(seenOpts))
	}
	if seenOpts[0].Confirmed != false {
		t.Errorf("call 0 Confirmed = %v, want false", seenOpts[0].Confirmed)
	}
	if seenOpts[1].Confirmed != true {
		t.Errorf("call 1 Confirmed = %v, want true", seenOpts[1].Confirmed)
	}
	if seenNames[1] != "my-vault" {
		t.Errorf("call 1 name = %q, want my-vault", seenNames[1])
	}
}

func TestOpsConfirmPrePush_NoPendingSetupDoesNotSilentlyReturn(t *testing.T) {
	m := newOpsModel(t.TempDir())
	m.loaded = true
	m.mode = "confirmPrePush"
	m.pendingRemoteURL = ""
	m.pendingGHRepo = ""

	m, cmd := m.handleKey(keyRunes("y"))
	if cmd != nil {
		t.Fatal("should not dispatch a command when nothing pending")
	}
	if m.status == "" || !m.statusErr {
		t.Fatalf("pressing 'y' with empty pending should set error status, got: status=%q statusErr=%v", m.status, m.statusErr)
	}
}
