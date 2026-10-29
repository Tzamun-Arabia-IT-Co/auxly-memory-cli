package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─────────────────────────────────────────────────────────────────
//  Settings → Ops sub-tab: capture-hook status, git backup, and the doctor
//  report — the three CLI-only surfaces a prior audit flagged as unreachable
//  from the TUI.
//
//  Hooks and doctor have no callable core outside cmd/ (their logic is inline
//  in unexported cmd functions), and tui/ must never import cmd/ — so both
//  shell the real `auxly` binary (runAuxlySub) and render its output, the
//  same "reuse the CLI, don't reimplement it" approach ssh.go's captured runs
//  use for connect/host. Backup is different: internal/git is a clean,
//  directly-callable core (not cmd-only), so it's called in-process via
//  tea.Cmds — never on the Update/View goroutine.
// ─────────────────────────────────────────────────────────────────

// runAuxlySub is a package-level var (like ssh.go's copyInvite/copyVaultKey)
// so tests can stub it instead of spawning the real binary. Always passes
// --path so a TUI launched against a non-default memory root (`auxly tui
// --path X`) shells out against that SAME vault, not the CLI's default.
var runAuxlySub = func(memPath string, args ...string) (string, error) {
	full := append([]string{"--path", memPath}, args...)
	out, err := exec.Command(exePath(), full...).CombinedOutput()
	return string(out), err
}

// Package-level git/gh hooks so Ops TUI tests never invoke real gh or touch
// external state unless exercising a local bare repo in a temp dir.
var (
	opsGHAvailable  = git.GHAvailable
	opsBackupStatus = git.BackupStatus
	opsBackupNow    = func(memPath string) (git.SyncResult, error) { return git.BackupNow(context.Background(), memPath) }
	opsBackupSetup  = func(memPath, remoteURL string, opts git.SetupOptions) (git.SetupResult, error) {
		return git.Setup(context.Background(), memPath, remoteURL, opts)
	}
	opsBackupCreateGH = func(memPath, name string, opts git.SetupOptions) (git.SetupResult, error) {
		return git.CreateGitHubAndSetup(context.Background(), memPath, name, opts)
	}
	opsSetAutoBackup = func(memPath string, enabled bool) error {
		cfg, err := git.LoadConfig(memPath)
		if err != nil {
			return err
		}
		cfg.AutoPush = enabled
		return git.SaveConfig(memPath, cfg)
	}
)

// hookStatusRow is one parsed row of `auxly hooks status`'s table.
type hookStatusRow struct {
	agent, status, detail string
}

// hooksStatusLineRE matches a data row of `auxly hooks status`'s fixed-width
// table (see cmd/hooks.go runHooksStatus): "   agent   STATUS   detail".
// Anchored on the known status tokens so the header row ("AGENT STATUS
// DETAIL") never parses as data.
var hooksStatusLineRE = regexp.MustCompile(`^\s*(\S+)\s+(WIRED|manual|not-installed)\s+(.*)$`)

// parseHooksStatus is pure (text in, rows out) so it's unit-testable without
// a subprocess.
func parseHooksStatus(out string) []hookStatusRow {
	var rows []hookStatusRow
	for _, line := range strings.Split(out, "\n") {
		if mm := hooksStatusLineRE.FindStringSubmatch(line); mm != nil {
			rows = append(rows, hookStatusRow{agent: mm[1], status: mm[2], detail: strings.TrimSpace(mm[3])})
		}
	}
	return rows
}

// opsModel is the Settings → Ops sub-tab's state.
type opsModel struct {
	memoryPath string
	width      int
	height     int

	loaded    bool
	hooksRows []hookStatusRow
	hooksErr  string
	cursor    int

	backup  git.Status
	ghAvail bool

	// mode == "" is idle;
	//   "confirmUninstall" / "confirmSync" / "confirmPrePush" gate actions behind y/n;
	//   "inputURL" / "inputGH" capture text entry for backup setup.
	mode string

	urlInput textinput.Model
	ghInput  textinput.Model

	pendingRemoteURL string
	pendingGHRepo    string
	pendingReport    git.Report

	busy      bool
	busyLabel string
	spin      int

	status    string
	statusErr bool

	viewingDoctor bool
	doctorOutput  string
}

func newOpsModel(memPath string) opsModel {
	ui := textinput.New()
	ui.Placeholder = "git@github.com:owner/auxly-vault.git"
	ui.CharLimit = 512
	ui.Width = 48

	gi := textinput.New()
	gi.Placeholder = "auxly-vault"
	gi.CharLimit = 100
	gi.Width = 40
	gi.SetValue("auxly-vault")

	return opsModel{
		memoryPath: memPath,
		urlInput:   ui,
		ghInput:    gi,
	}
}

// capturesInput mirrors vaultModel.capturesInput: any active text input,
// confirm prompt, or in-flight action blocks the app-wide digit/quit keys so
// typing into URL/repo-name inputs never switches tabs or quits the TUI.
func (m opsModel) capturesInput() bool {
	return m.busy || m.mode != ""
}

type opsRefreshMsg struct {
	rows    []hookStatusRow
	err     string
	backup  git.Status
	ghAvail bool
}

type opsActionMsg struct {
	kind  string // "install" | "uninstall" | "doctor"
	agent string
	out   string
	err   error
}

type opsSyncMsg struct {
	result git.SyncResult
	backup git.Status
	err    error
}

type opsBackupSetupMsg struct {
	result    git.SetupResult
	remoteURL string
	repoName  string
	backup    git.Status
	err       error
}

type opsAutoBackupMsg struct {
	enabled bool
	backup  git.Status
	err     error
}

type opsSpinTickMsg struct{}

func opsSpinTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return opsSpinTickMsg{} })
}

func opsHooksStatusCmd(memPath string) tea.Cmd {
	return func() tea.Msg {
		out, err := runAuxlySub(memPath, "hooks", "status")
		backup := opsBackupStatus(memPath)
		var ghAvail bool
		if !backup.Configured {
			ghAvail = opsGHAvailable()
		}
		if err != nil && strings.TrimSpace(out) == "" {
			return opsRefreshMsg{err: err.Error(), backup: backup, ghAvail: ghAvail}
		}
		return opsRefreshMsg{rows: parseHooksStatus(out), backup: backup, ghAvail: ghAvail}
	}
}

func opsHookActionCmd(memPath, kind, agent string) tea.Cmd {
	return func() tea.Msg {
		out, err := runAuxlySub(memPath, "hooks", kind, "--agent", agent)
		return opsActionMsg{kind: kind, agent: agent, out: out, err: err}
	}
}

func opsDoctorCmd(memPath string) tea.Cmd {
	return func() tea.Msg {
		out, err := runAuxlySub(memPath, "doctor")
		return opsActionMsg{kind: "doctor", out: out, err: err}
	}
}

func opsSyncCmd(memPath string) tea.Cmd {
	return func() tea.Msg {
		res, err := opsBackupNow(memPath)
		return opsSyncMsg{result: res, backup: opsBackupStatus(memPath), err: err}
	}
}

func opsBackupSetupURLCmd(memPath, remoteURL string, opts git.SetupOptions) tea.Cmd {
	return func() tea.Msg {
		res, err := opsBackupSetup(memPath, remoteURL, opts)
		return opsBackupSetupMsg{
			result:    res,
			remoteURL: remoteURL,
			backup:    opsBackupStatus(memPath),
			err:       err,
		}
	}
}

func opsBackupCreateGHCmd(memPath, name string, opts git.SetupOptions) tea.Cmd {
	return func() tea.Msg {
		res, err := opsBackupCreateGH(memPath, name, opts)
		return opsBackupSetupMsg{
			result:    res,
			remoteURL: res.RemoteURL,
			repoName:  name,
			backup:    opsBackupStatus(memPath),
			err:       err,
		}
	}
}

func opsToggleAutoBackupCmd(memPath string, enable bool) tea.Cmd {
	return func() tea.Msg {
		err := opsSetAutoBackup(memPath, enable)
		return opsAutoBackupMsg{
			enabled: enable,
			backup:  opsBackupStatus(memPath),
			err:     err,
		}
	}
}

// refreshCmd reloads hooks status + git backup status — called on Ops sub-tab
// entry and after any install/uninstall.
func (m opsModel) refreshCmd() tea.Cmd {
	return opsHooksStatusCmd(m.memoryPath)
}

// syncStatusText renders a SyncStatus/BackupNow result into the four distinct
// outcomes: pushed / nothing-to-push / refused (sentinel or no remote) / error.
// Pure, so it's unit-testable without a real git repo.
func syncStatusText(res git.SyncResult, err error) (string, bool) {
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "temporary decrypt"):
			return "⚠ sync skipped: a temporary decrypt is in progress", true
		case strings.Contains(msg, "not a git repository"),
			strings.Contains(msg, "does not appear to be a git repository"),
			strings.Contains(msg, "No configured push destination"):
			return "⚠ sync not set up — vault isn't a git repo with a remote (optional; enables off-machine backup)", true
		default:
			return "✗ sync error: " + joinLines(msg), true
		}
	}
	if res.Pushed {
		return "✓ pushed to remote", false
	}
	return "✓ nothing to push — already up to date", false
}

func (m opsModel) Update(msg tea.Msg) (opsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case opsRefreshMsg:
		m.loaded = true
		m.hooksRows = msg.rows
		m.hooksErr = msg.err
		m.backup = msg.backup
		m.ghAvail = msg.ghAvail
		if m.cursor >= len(m.hooksRows) {
			m.cursor = 0
		}
		return m, nil

	case opsActionMsg:
		m.busy = false
		switch msg.kind {
		case "install", "uninstall":
			summary := joinLines(msg.out)
			if msg.err != nil {
				if summary == "" {
					summary = msg.err.Error()
				}
				m.status, m.statusErr = "✗ "+summary, true
			} else {
				m.status, m.statusErr = summary, false
			}
			return m, opsHooksStatusCmd(m.memoryPath)
		case "doctor":
			m.doctorOutput = msg.out
			m.viewingDoctor = true
			if msg.err != nil && strings.TrimSpace(msg.out) == "" {
				m.status, m.statusErr = "✗ doctor failed: "+msg.err.Error(), true
				m.viewingDoctor = false
			}
			return m, nil
		}
		return m, nil

	case opsSyncMsg:
		m.busy = false
		if msg.backup.Configured || msg.backup.IsRepo {
			m.backup = msg.backup
		}
		m.status, m.statusErr = syncStatusText(msg.result, msg.err)
		return m, nil

	case opsBackupSetupMsg:
		m.busy = false
		if errors.Is(msg.err, git.ErrPrePushBlocked) {
			m.pendingRemoteURL = msg.remoteURL
			m.pendingGHRepo = msg.repoName
			m.pendingReport = msg.result.Report
			m.mode = "confirmPrePush"
			return m, nil
		}
		if msg.err != nil {
			m.status, m.statusErr = "✗ "+joinLines(msg.err.Error()), true
			return m, nil
		}
		m.backup = msg.backup
		m.pendingRemoteURL = ""
		m.pendingGHRepo = ""
		m.pendingReport = git.Report{}
		status := fmt.Sprintf("✓ backup set up → %s (%s)", git.StripCredentials(msg.result.RemoteURL), msg.result.Branch)
		if msg.result.Report.Warning != "" {
			status += " · ⚠ " + msg.result.Report.Warning
		}
		m.status, m.statusErr = status, false
		return m, nil

	case opsAutoBackupMsg:
		m.busy = false
		if msg.err != nil {
			m.status, m.statusErr = "✗ "+joinLines(msg.err.Error()), true
			return m, nil
		}
		m.backup = msg.backup
		if msg.enabled {
			m.status, m.statusErr = "✓ Auto-backup ON", false
		} else {
			m.status, m.statusErr = "✓ Auto-backup OFF", false
		}
		return m, nil

	case opsSpinTickMsg:
		if m.busy {
			m.spin++
			return m, opsSpinTick()
		}
		return m, nil
	}
	return m, nil
}

// joinLines collapses multi-line command output into one status line, dropping blanks.
func joinLines(s string) string {
	var kept []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, " · ")
}

func (m opsModel) cursorAgentWired() (agent string, wired bool) {
	if m.cursor < 0 || m.cursor >= len(m.hooksRows) {
		return "", false
	}
	row := m.hooksRows[m.cursor]
	return row.agent, row.status == "WIRED"
}

// wireHintFor describes what [i] does for the given not-yet-wired agent.
func wireHintFor(agent string) string {
	switch agent {
	case "claude", "codex":
		return "[i] wires " + agent + " — writes its session-end hook; captured facts go to pending review"
	case "gemini", "kimi":
		return "[i] wires " + agent + " — adds a capture wrapper to ~/.zshrc (restart your shell after)"
	case "antigravity":
		return "[i] wires antigravity — adds a capture wrapper to ~/.zshrc for its `agy` CLI (restart your shell after)"
	default:
		return ""
	}
}

func (m opsModel) handleKey(msg tea.KeyMsg) (opsModel, tea.Cmd) {
	if m.viewingDoctor {
		m.viewingDoctor = false
		return m, nil
	}
	if m.busy {
		return m, nil // input is frozen while the subprocess/git command is in flight
	}

	switch m.mode {
	case "inputURL":
		switch msg.String() {
		case "esc", "ctrl+c":
			m.urlInput.Blur()
			m.mode = ""
			return m, nil
		case "enter":
			raw := strings.TrimSpace(m.urlInput.Value())
			if raw == "" {
				m.status, m.statusErr = "⚠ enter a repository URL or press esc to cancel", true
				return m, nil
			}
			m.urlInput.Blur()
			m.mode = ""
			m.status = ""
			m.busy, m.busyLabel, m.spin = true, "Setting up git backup", 0
			return m, tea.Batch(opsBackupSetupURLCmd(m.memoryPath, raw, git.SetupOptions{}), opsSpinTick())
		}
		var cmd tea.Cmd
		m.urlInput, cmd = m.urlInput.Update(msg)
		return m, cmd

	case "inputGH":
		switch msg.String() {
		case "esc", "ctrl+c":
			m.ghInput.Blur()
			m.mode = ""
			return m, nil
		case "enter":
			name := strings.TrimSpace(m.ghInput.Value())
			if name == "" {
				name = "auxly-vault"
			}
			m.ghInput.Blur()
			m.mode = ""
			m.status = ""
			m.busy, m.busyLabel, m.spin = true, "Creating private GitHub repo "+name, 0
			return m, tea.Batch(opsBackupCreateGHCmd(m.memoryPath, name, git.SetupOptions{}), opsSpinTick())
		}
		var cmd tea.Cmd
		m.ghInput, cmd = m.ghInput.Update(msg)
		return m, cmd

	case "confirmPrePush":
		switch msg.String() {
		case "y", "Y":
			remote := m.pendingRemoteURL
			ghRepo := m.pendingGHRepo
			m.mode = ""
			m.pendingRemoteURL = ""
			m.pendingGHRepo = ""
			m.pendingReport = git.Report{}
			if ghRepo != "" {
				m.status = ""
				m.busy, m.busyLabel, m.spin = true, "Creating private GitHub repo "+ghRepo, 0
				return m, tea.Batch(opsBackupCreateGHCmd(m.memoryPath, ghRepo, git.SetupOptions{
					Confirmed:   true,
					AllowPublic: true,
				}), opsSpinTick())
			}
			if remote != "" {
				m.status = ""
				m.busy, m.busyLabel, m.spin = true, "Pushing initial backup", 0
				return m, tea.Batch(opsBackupSetupURLCmd(m.memoryPath, remote, git.SetupOptions{
					Confirmed:   true,
					AllowPublic: true,
				}), opsSpinTick())
			}
			m.status, m.statusErr = "⚠ backup setup cancelled", true
			return m, nil
		case "n", "N", "esc", "ctrl+c":
			m.mode = ""
			m.pendingRemoteURL = ""
			m.pendingGHRepo = ""
			m.pendingReport = git.Report{}
			m.status, m.statusErr = "⚠ backup setup cancelled", true
			return m, nil
		}
		return m, nil

	case "confirmUninstall":
		switch msg.String() {
		case "y", "Y", "enter":
			agent, _ := m.cursorAgentWired()
			m.mode = ""
			if agent == "" {
				return m, nil
			}
			m.status = ""
			m.busy, m.busyLabel, m.spin = true, "Removing capture hook for "+agent, 0
			return m, tea.Batch(opsHookActionCmd(m.memoryPath, "uninstall", agent), opsSpinTick())
		case "n", "N", "esc":
			m.mode = ""
		}
		return m, nil

	case "confirmSync":
		switch msg.String() {
		case "y", "Y", "enter":
			m.mode = ""
			m.status = ""
			m.busy, m.busyLabel, m.spin = true, "Backing up to remote", 0
			return m, tea.Batch(opsSyncCmd(m.memoryPath), opsSpinTick())
		case "n", "N", "esc":
			m.mode = ""
		}
		return m, nil
	}

	// idle
	switch msg.String() {
	case "j", "down":
		m.status = ""
		if m.cursor < len(m.hooksRows)-1 {
			m.cursor++
		}
	case "k", "up":
		m.status = ""
		if m.cursor > 0 {
			m.cursor--
		}
	case "i":
		if agent, wired := m.cursorAgentWired(); agent != "" && !wired {
			m.status = ""
			m.busy, m.busyLabel, m.spin = true, "Installing capture hook for "+agent, 0
			return m, tea.Batch(opsHookActionCmd(m.memoryPath, "install", agent), opsSpinTick())
		}
	case "u":
		if !m.backup.Configured {
			m.status = ""
			m.urlInput.SetValue("")
			cmd := m.urlInput.Focus()
			m.mode = "inputURL"
			return m, cmd
		}
		if _, wired := m.cursorAgentWired(); wired {
			m.mode = "confirmUninstall"
		}
	case "x", "U":
		if _, wired := m.cursorAgentWired(); wired {
			m.mode = "confirmUninstall"
		}
	case "g":
		if !m.backup.Configured && m.ghAvail {
			m.status = ""
			m.ghInput.SetValue("auxly-vault")
			m.ghInput.CursorEnd()
			cmd := m.ghInput.Focus()
			m.mode = "inputGH"
			return m, cmd
		}
	case "s":
		m.mode = "confirmSync"
	case "b":
		if m.backup.Configured {
			m.status = ""
			next := !m.backup.AutoBackup
			m.busy, m.busyLabel, m.spin = true, "Updating auto-backup", 0
			return m, tea.Batch(opsToggleAutoBackupCmd(m.memoryPath, next), opsSpinTick())
		}
	case "d":
		m.status = ""
		m.busy, m.busyLabel, m.spin = true, "Running doctor", 0
		return m, tea.Batch(opsDoctorCmd(m.memoryPath), opsSpinTick())
	case "r":
		m.status = ""
		return m, m.refreshCmd()
	}
	return m, nil
}

// panel renders the Ops sub-tab body (the caller adds title + sub-tab bar),
// mirroring vaultModel.panel()'s shape.
func (m opsModel) panel() string {
	cyan := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dim := StyleSubtitle
	accent := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(ColorSuccess)
	warn := lipgloss.NewStyle().Foreground(ColorWarning)
	danger := lipgloss.NewStyle().Foreground(ColorDanger)

	w := m.width
	if w <= 0 {
		w = 80
	}

	if m.viewingDoctor {
		return cyan.Render("🩺 Doctor Report") + "\n\n" + m.doctorOutput + "\n" + dim.Render("any key: back")
	}

	padW := w - 10
	if padW < 44 {
		padW = 44
	}
	if padW > 70 {
		padW = 70
	}
	box := func(border lipgloss.Color, lines []string) string {
		var padded []string
		for _, l := range lines {
			padded = append(padded, padLine(l, padW))
		}
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
			Padding(1, 2).Render(strings.Join(padded, "\n"))
	}

	if !m.loaded {
		return cyan.Render("Ops & Diagnostics") + "\n\n" + dim.Render("Loading…")
	}

	if m.busy {
		spin := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render(spinnerFrame(m.spin))
		return box(ColorWarning, []string{spin + "  " + warn.Render(m.busyLabel+"…")})
	}

	switch m.mode {
	case "inputURL":
		promptLines := []string{
			bold.Render("Set up Backup (git) — Repository URL"),
			dim.Render("Accepts git@host:owner/repo.git, https://..., or a local path (no tokens in URL)."),
			"",
			m.urlInput.View(),
			"",
			green.Render("[enter] submit") + dim.Render("   [esc] cancel"),
		}
		if m.status != "" {
			promptLines = append(promptLines, "", warn.Render(m.status))
		}
		return box(ColorPrimary, promptLines)

	case "inputGH":
		return box(ColorPrimary, []string{
			bold.Render("Create Private GitHub Repository"),
			dim.Render("Creates a private GitHub repository via `gh` and sets up backup."),
			"",
			m.ghInput.View(),
			"",
			green.Render("[enter] create & push") + dim.Render("   [esc] cancel"),
		})

	case "confirmPrePush":
		var findings []string
		findings = append(findings, warn.Render("⚠ Pre-push safety findings"))
		for _, sf := range m.pendingReport.SensitiveFiles {
			findings = append(findings, fmt.Sprintf("  • %s: %d potential secret(s) (unencrypted)", sf.File, sf.Count))
		}
		if m.pendingReport.ExistingCommits > 0 {
			findings = append(findings, fmt.Sprintf("  • %d earlier commits will be pushed as they are; older commits are not checked for secrets", m.pendingReport.ExistingCommits))
		}
		if m.pendingReport.IsPublic() {
			findings = append(findings, danger.Render("  • Remote repository is PUBLIC"))
		} else if m.pendingReport.Warning != "" {
			findings = append(findings, dim.Render("  • "+m.pendingReport.Warning))
		}
		findings = append(findings,
			"",
			warn.Render("y push anyway / n cancel"),
		)
		return box(ColorWarning, findings)

	case "confirmUninstall":
		agent, _ := m.cursorAgentWired()
		return box(ColorWarning, []string{
			warn.Render(fmt.Sprintf("Remove the capture hook for %q?", agent)),
			dim.Render("Facts from its sessions will stop flowing into `auxly pending`."),
			"",
			green.Render("[y] remove") + dim.Render("   [n]/esc cancel"),
		})

	case "confirmSync":
		return box(ColorWarning, []string{
			warn.Render("Back up memory changes to the git remote now?"),
			"",
			green.Render("[y] back up") + dim.Render("   [n]/esc cancel"),
		})
	}

	var lines []string
	lines = append(lines, bold.Render("Capture Hooks"))
	lines = append(lines, dim.Render("Auto-capture wires an agent's session-end hook to `auxly capture`."))
	lines = append(lines, "")
	switch {
	case m.hooksErr != "":
		lines = append(lines, danger.Render("⚠ "+m.hooksErr))
	case len(m.hooksRows) == 0:
		lines = append(lines, dim.Render("No agents to wire yet."))
	default:
		for i, r := range m.hooksRows {
			cursor := "  "
			if i == m.cursor {
				cursor = accent.Render("▸ ")
			}
			badge := dim.Render("[not-installed]")
			switch r.status {
			case "WIRED":
				badge = green.Render("[WIRED]")
			case "manual":
				badge = warn.Render("[manual]")
			}
			row := fmt.Sprintf("%s%-10s %-16s %s", cursor, r.agent, badge, dim.Render(truncate(r.detail, 34)))
			if i == m.cursor {
				row = bold.Render(fmt.Sprintf("%s%-10s %-16s ", cursor, r.agent, badge)) + dim.Render(truncate(r.detail, 34))
			}
			lines = append(lines, row)
		}
	}
	lines = append(lines, "")
	if m.backup.Configured {
		lines = append(lines, dim.Render("↑/↓ select · i install · u/x uninstall (confirm) · r rescan"))
	} else {
		lines = append(lines, dim.Render("↑/↓ select · i install · x uninstall (confirm) · r rescan"))
	}
	if agent, wired := m.cursorAgentWired(); agent != "" && !wired {
		if hint := wireHintFor(agent); hint != "" {
			lines = append(lines, dim.Render(hint))
		}
	}

	lines = append(lines, "")
	lines = append(lines, bold.Render("Backup (git)"))
	if !m.backup.Configured {
		lines = append(lines, dim.Render("Back up your memory vault to a private git repo for off-machine recovery."))
		lines = append(lines, dim.Render("[u] set up with a repo URL"))
		if m.ghAvail {
			lines = append(lines, dim.Render("[g] create a private GitHub repo (default: auxly-vault)"))
		}
	} else {
		branch := m.backup.Branch
		if branch == "" {
			branch = "main"
		}
		lines = append(lines, fmt.Sprintf("Remote: %s · Branch: %s",
			accent.Render(git.StripCredentials(m.backup.RemoteURL)),
			bold.Render(branch),
		))
		autoBadge := dim.Render("OFF")
		if m.backup.AutoBackup {
			autoBadge = green.Render("ON")
		}
		lines = append(lines, fmt.Sprintf("Auto-backup: %s · Unpushed: %d · Dirty: %d",
			autoBadge, m.backup.UnpushedCount, m.backup.DirtyFileCount,
		))
		switch {
		case m.backup.LastError != "":
			lines = append(lines, danger.Render("Last error: "+truncate(joinLines(m.backup.LastError), padW-14)))
		case !m.backup.LastSuccess.IsZero():
			lines = append(lines, dim.Render("Last backup: "+m.backup.LastSuccess.Local().Format("2006-01-02 15:04:05")))
		default:
			lines = append(lines, dim.Render("Last backup: never"))
		}
		lines = append(lines, dim.Render("[s] back up now (confirm) · [b] toggle auto-backup"))
	}

	lines = append(lines, "")
	lines = append(lines, bold.Render("Diagnostics"))
	lines = append(lines, dim.Render("[d] run doctor report"))

	if m.status != "" {
		style := green
		switch {
		case strings.HasPrefix(m.status, "✗"):
			style = danger
		case strings.HasPrefix(m.status, "⚠"):
			style = warn
		}
		lines = append(lines, "", style.Render(m.status))
	}

	var padded []string
	for _, l := range lines {
		padded = append(padded, padLine(l, padW))
	}
	panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorPrimary).
		Padding(1, 2).Render(strings.Join(padded, "\n"))
	return cyan.Render("Ops & Diagnostics") + "\n\n" + panel
}
