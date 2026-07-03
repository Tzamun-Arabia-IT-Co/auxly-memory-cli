package tui

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─────────────────────────────────────────────────────────────────
//  Settings → Ops sub-tab: capture-hook status, git sync, and the doctor
//  report — the three CLI-only surfaces a prior audit flagged as unreachable
//  from the TUI.
//
//  Hooks and doctor have no callable core outside cmd/ (their logic is inline
//  in unexported cmd functions), and tui/ must never import cmd/ — so both
//  shell the real `auxly` binary (runAuxlySub) and render its output, the
//  same "reuse the CLI, don't reimplement it" approach ssh.go's captured runs
//  use for connect/host. Sync is different: internal/git.Sync is already a
//  clean, directly-callable core (not cmd-only), so it's called in-process —
//  no subprocess needed, and it can't drift from what `auxly sync` does
//  because it's the exact same function.
//
//  ponytail: hooks/doctor use a single-shot exec+wait (spinner ticks locally)
//  rather than ssh.go's line-streaming captured-run — those commands finish
//  in well under a second with no incremental progress to show, unlike the
//  multi-minute SSH provisioning flows that streaming exists for. Upgrade to
//  streaming if `auxly doctor` ever grows a slow step worth showing live.
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

	// mode == "" is idle; confirmUninstall/confirmSync gate destructive/outward
	// actions behind a y/n prompt, mirroring vaultModel's confirm shape.
	mode string

	busy      bool
	busyLabel string
	spin      int

	status    string
	statusErr bool

	viewingDoctor bool
	doctorOutput  string
}

func newOpsModel(memPath string) opsModel {
	return opsModel{memoryPath: memPath}
}

// capturesInput mirrors vaultModel.capturesInput: only a confirm prompt or an
// in-flight action should block the app-wide digit/quit keys — idle
// navigation (j/k/i/u/s/d) is not in that global key set, so it always
// reaches handleKey via app.go's normal sub-model delegation regardless.
func (m opsModel) capturesInput() bool {
	return m.busy || m.mode != ""
}

type opsRefreshMsg struct {
	rows []hookStatusRow
	err  string
}

type opsActionMsg struct {
	kind  string // "install" | "uninstall" | "doctor"
	agent string
	out   string
	err   error
}

type opsSyncMsg struct {
	result git.SyncResult
	err    error
}

type opsSpinTickMsg struct{}

func opsSpinTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return opsSpinTickMsg{} })
}

func opsHooksStatusCmd(memPath string) tea.Cmd {
	return func() tea.Msg {
		out, err := runAuxlySub(memPath, "hooks", "status")
		if err != nil && strings.TrimSpace(out) == "" {
			return opsRefreshMsg{err: err.Error()}
		}
		return opsRefreshMsg{rows: parseHooksStatus(out)}
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
		res, err := git.SyncStatus(memPath)
		return opsSyncMsg{result: res, err: err}
	}
}

// refreshCmd reloads the hooks status table — called on Ops sub-tab entry and
// after any install/uninstall.
func (m opsModel) refreshCmd() tea.Cmd {
	return opsHooksStatusCmd(m.memoryPath)
}

// syncStatusText renders a SyncStatus result into the four distinct outcomes
// the audit asked for: pushed / nothing-to-push / refused (sentinel or no
// remote) / error. Pure, so it's unit-testable without a real git repo.
func syncStatusText(res git.SyncResult, err error) (string, bool) {
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "temporary decrypt"):
			return "⚠ sync skipped: a temporary decrypt is in progress", true
		case strings.Contains(msg, "not a git repository"),
			strings.Contains(msg, "does not appear to be a git repository"),
			strings.Contains(msg, "No configured push destination"):
			return "⚠ sync not configured — this vault has no git remote", true
		default:
			return "✗ sync error: " + msg, true
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
		m.status, m.statusErr = syncStatusText(msg.result, msg.err)
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

// joinLines collapses `auxly hooks install/uninstall`'s output (1-2 short
// lines — a confirmation plus, sometimes, a caveat worth keeping, see
// cmd/hooks.go / cmd/hooks_shell.go) into one status line, dropping blanks.
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

func (m opsModel) handleKey(msg tea.KeyMsg) (opsModel, tea.Cmd) {
	// Report view: any key dismisses it (mirrors sshModePrint's "any key
	// dismisses the config preview").
	if m.viewingDoctor {
		m.viewingDoctor = false
		return m, nil
	}
	if m.busy {
		return m, nil // input is frozen while the subprocess/push is in flight
	}

	switch m.mode {
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
			m.busy, m.busyLabel, m.spin = true, "Syncing to remote", 0
			return m, tea.Batch(opsSyncCmd(m.memoryPath), opsSpinTick())
		case "n", "N", "esc":
			m.mode = ""
		}
		return m, nil
	}

	// idle
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.hooksRows)-1 {
			m.cursor++
		}
	case "k", "up":
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
		if _, wired := m.cursorAgentWired(); wired {
			m.mode = "confirmUninstall"
		}
	case "s":
		m.mode = "confirmSync"
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
			warn.Render("Push memory changes to the git remote now?"),
			"",
			green.Render("[y] sync") + dim.Render("   [n]/esc cancel"),
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
	lines = append(lines, dim.Render("↑/↓ select · i install · u uninstall (confirm) · r rescan"))

	lines = append(lines, "")
	lines = append(lines, bold.Render("Sync (git push)"))
	lines = append(lines, dim.Render("[s] sync now")+dim.Render(" — commits + pushes; confirm required"))

	lines = append(lines, "")
	lines = append(lines, bold.Render("Diagnostics"))
	lines = append(lines, dim.Render("[d] run doctor report"))

	if m.status != "" {
		style := green
		if m.statusErr {
			style = danger
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
