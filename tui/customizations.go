package tui

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// statuslineApplyDelay holds the "Applying…" state on screen briefly before the
// confirmation lands. The write itself is near-instant; this makes the in-progress
// → done transition perceptible (and the apply feel deliberate, not a silent flip).
const statuslineApplyDelay = 450 * time.Millisecond

// customizationsModel backs the Settings → Customizations sub-tab: the opt-in
// statusline for each supported agent (Claude Code / Cursor / Antigravity), with a
// per-agent switcher, a live preview rendered from real data, and an in-process,
// reversible apply. `a` cycles the focused agent; ↑/↓ pick replace/wrap/none.
type customizationsModel struct {
	agents     []statusline.Target // every statusline-capable agent (claude/cursor/antigravity)
	agentIdx   int                 // focused agent in the switcher
	optionIdx  int                 // 0 = replace (full), 1 = wrap, 2 = none/remove
	confirming bool                // confirm dialog before applying ①/②
	applying   bool                // in-progress: write dispatched, awaiting result
	pendingIdx int                 // the option being applied (snapshot for the Applying… label)
	status     string
	state      statusline.State // focused agent's live state
	detected   bool             // focused agent is installed on this machine
	width      int
	height     int

	// Remote statusline-sync sub-panel ([s]): master auto-sync toggle + per-box
	// selection + manual "sync now" (all or one). syncOpen owns the keyboard while
	// shown. syncCursor: 0 = master row, 1..N = box rows.
	syncOpen     bool
	syncCursor   int
	syncBoxes    []clientRow
	syncSelected map[string]bool
	syncMaster   bool
	syncing      bool
	syncSpin     int // spinner frame while a sync exec is in flight
	syncStatus   string
}

// statuslineAppliedMsg carries the result of an in-process install/uninstall back
// to the Customizations sub-tab (routed through settingsModel.Update).
type statuslineAppliedMsg struct {
	ok     bool
	status string
	state  statusline.State
}

// customizationsPreviewTickMsg re-renders the preview after a background usage refresh
// lands, so the focused agent's usage line flips from "⧗ as of …" to "↻ live".
type customizationsPreviewTickMsg struct{}

// previewRefreshCmd kicks the same detached, debounced, LiveUsage-gated usage refresh
// the real statusline uses, for the focused agent's provider (claude/cursor/
// antigravity), then schedules a couple of re-renders so the preview reflects the
// fresh snapshot once it lands. The trigger runs in a command goroutine; the render
// itself stays network-free. A no-op when Live Usage is off.
func (m customizationsModel) previewRefreshCmd() tea.Cmd {
	provider := m.focusedAgent().Name
	return tea.Batch(
		func() tea.Msg { statusline.MaybeRefreshUsage(provider); return nil },
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return customizationsPreviewTickMsg{} }),
		tea.Tick(6*time.Second, func(time.Time) tea.Msg { return customizationsPreviewTickMsg{} }),
	)
}

var statuslineOptions = []struct{ title, desc string }{
	{"Use the Auxly statusline", "Replaces this agent's statusline with Auxly's full 4-line view. The old command is backed up."},
	{"Add Auxly to my current", "Keeps the agent's statusline as-is and appends the Auxly line(s). Fully reversible."},
	{"None / Remove", "Removes the Auxly statusline and restores the backup, or no-ops if not installed."},
}

// canRestore reports whether the focused agent currently runs Auxly AND has a backed-up
// original — i.e. there's something to restore. When true, the ③ option presents as an
// explicit "Restore my original statusline" instead of the generic "None / Remove".
func (m customizationsModel) canRestore() bool {
	return m.state.Backup != "" &&
		(m.state.Mode == statusline.ModeFull || m.state.Mode == statusline.ModeWrap)
}

// optionAt returns the (title, desc) for option i, swapping ③ to a Restore action when
// a backed-up original exists for the focused agent.
func (m customizationsModel) optionAt(i int) (title, desc string) {
	if i == 2 && m.canRestore() {
		return "Restore my original statusline",
			"Puts back the statusline you had before Auxly: " + shortCmd(m.state.Backup)
	}
	return statuslineOptions[i].title, statuslineOptions[i].desc
}

// shortCmd trims a command for inline display.
func shortCmd(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 48 {
		return s[:47] + "…"
	}
	return s
}

// focusedAgent returns the currently selected agent target (Claude when uninit).
func (m customizationsModel) focusedAgent() statusline.Target {
	if m.agentIdx >= 0 && m.agentIdx < len(m.agents) {
		return m.agents[m.agentIdx]
	}
	t, _ := statusline.TargetByName(statusline.ProviderClaude)
	return t
}

// refresh (re)loads the agent list and the focused agent's live statusline state.
// The option cursor always resets to ① (replace) so the selection is predictable as
// you switch agents — the per-option "✓ active" marker already shows which mode the
// focused agent is actually running, so the cursor doesn't need to chase it.
func (m *customizationsModel) refresh() {
	if len(m.agents) == 0 {
		m.agents = statusline.Targets()
	}
	t := m.focusedAgent()
	m.state = statusline.CurrentState(t.Name)
	m.detected = t.Available()
	m.optionIdx = 0
}

// switchAgent moves the focused agent by delta (wrapping), dropping any in-progress
// confirm/apply and reloading the new agent's state.
func (m customizationsModel) switchAgent(delta int) customizationsModel {
	if len(m.agents) == 0 {
		m.agents = statusline.Targets()
	}
	m.agentIdx = (m.agentIdx + delta + len(m.agents)) % len(m.agents)
	m.confirming = false
	m.applying = false
	m.status = ""
	m.refresh()
	return m
}

func (m customizationsModel) handleKey(msg tea.KeyMsg) (customizationsModel, tea.Cmd) {
	if m.syncOpen {
		return m.handleSyncKey(msg)
	}
	if m.applying {
		return m, nil // input is frozen while the write is in flight
	}
	if m.confirming {
		switch msg.String() {
		case "y", "Y", "enter":
			return m.startApply()
		case "n", "N", "esc", "q":
			m.confirming = false
		}
		return m, nil
	}
	switch msg.String() {
	case "s", "S":
		return m.openSync(), nil
	case "a":
		nm := m.switchAgent(1)
		return nm, nm.previewRefreshCmd()
	case "A":
		nm := m.switchAgent(-1)
		return nm, nm.previewRefreshCmd()
	case "j", "down":
		if m.optionIdx < len(statuslineOptions)-1 {
			m.optionIdx++
		}
	case "k", "up":
		if m.optionIdx > 0 {
			m.optionIdx--
		}
	case "enter", " ":
		if !m.detected {
			m.status = m.focusedAgent().Label + " not detected — statusline integration is unavailable here."
			return m, nil
		}
		if m.optionIdx == 2 { // None/Remove needs no confirm — it only restores/no-ops.
			return m.startApply()
		}
		m.confirming = true
	}
	return m, nil
}

// startApply enters the in-progress state and dispatches the install/uninstall as a
// command. The actual write happens off the UI thread (applyStatuslineCmd); the
// result returns as a statuslineAppliedMsg that handleApplied folds back in.
func (m customizationsModel) startApply() (customizationsModel, tea.Cmd) {
	m.confirming = false
	m.applying = true
	m.pendingIdx = m.optionIdx
	m.status = ""
	return m, applyStatuslineCmd(m.optionIdx, m.focusedAgent())
}

// applyStatuslineCmd performs the in-process install/uninstall for one agent via the
// same code path the CLI uses. It runs in a command goroutine (never in Update), so
// the brief hold that makes the "Applying…" state visible is a sleep here, not a UI
// block.
func applyStatuslineCmd(idx int, t statusline.Target) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(statuslineApplyDelay)
		var err error
		switch idx {
		case 0:
			err = statusline.Install(t.Name, false)
		case 1:
			err = statusline.Install(t.Name, true)
		case 2:
			err = statusline.Uninstall(t.Name)
		}
		msg := statuslineAppliedMsg{ok: err == nil, state: statusline.CurrentState(t.Name)}
		if err != nil {
			msg.status = "✗ " + err.Error()
			return msg
		}
		switch idx {
		case 0:
			msg.status = "✓ Auxly statusline installed for " + t.Label + " (replace) — reload " + t.Label + " to see it."
		case 1:
			msg.status = "✓ Auxly appended to " + t.Label + "'s statusline (wrap) — reload " + t.Label + "."
		case 2:
			if msg.state.Command != "" {
				msg.status = "✓ Restored " + t.Label + "'s original statusline."
			} else {
				msg.status = "✓ Removed the Auxly statusline from " + t.Label + "."
			}
		}
		return msg
	}
}

// handleApplied folds an apply result back into the model: clears the in-progress
// flag, records the confirmation/error, and refreshes the displayed current state.
// On a SUCCESSFUL apply it advances the focus to the next agent (stopping at the
// last) so the user can wire Claude → Cursor → Antigravity in one guided pass.
func (m customizationsModel) handleApplied(msg statuslineAppliedMsg) customizationsModel {
	m.applying = false
	m.confirming = false
	m.status = msg.status
	m.state = msg.state
	// Guided flow: on a successful apply, advance to the next INSTALLED agent (skip
	// not-installed ones; stay put if none remain) so the user wires Claude → Cursor →
	// Antigravity in one pass.
	if msg.ok {
		for i := m.agentIdx + 1; i < len(m.agents); i++ {
			if m.agents[i].Available() {
				m.agentIdx = i
				m.refresh() // load the next agent's state; option cursor resets to ①
				m.status = msg.status + "  → next: " + m.focusedAgent().Label
				break
			}
		}
	}
	return m
}

func (m customizationsModel) capturesInput() bool {
	return m.confirming || m.applying || m.syncOpen
}

// previewInputFor synthesizes a session for the preview from real local context (cwd
// + a representative model per agent), so lines 3–4 use the user's actual data and
// the model label on line 1 matches the focused agent.
func previewInputFor(t statusline.Target) statusline.Input {
	var in statusline.Input
	if wd, err := os.Getwd(); err == nil {
		in.Workspace.CurrentDir = wd
	}
	switch t.Name {
	case statusline.ProviderCursor:
		in.Model.DisplayName = "Composer"
	case statusline.ProviderAntigravity:
		in.Model.Name = "Gemini"
	default:
		in.Model.DisplayName = "Claude"
	}
	return in
}

// agentSwitcher renders the per-agent chip row: focused agent in ‹ › primary, other
// installed agents in secondary, and not-installed agents dimmed. The `[a]` key hint
// sits on the row itself so it's obvious how to move between agents.
func (m customizationsModel) agentSwitcher() string {
	dim := StyleSubtitle
	key := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	accent := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	other := lipgloss.NewStyle().Foreground(ColorSecondary)
	var chips []string
	for i, t := range m.agents {
		label := t.Label
		if !t.Available() {
			label += " (not installed)"
		}
		switch {
		case i == m.agentIdx:
			chips = append(chips, accent.Render("‹ "+label+" ›"))
		case !t.Available():
			chips = append(chips, dim.Render(label))
		default:
			chips = append(chips, other.Render(label))
		}
	}
	return key.Render("[a] switch agent ▸  ") + strings.Join(chips, dim.Render("   "))
}

// panel renders the Customizations sub-tab body (the caller adds title + sub-tab bar).
func (m customizationsModel) panel() string {
	if m.syncOpen {
		return m.syncPanel()
	}
	dim := StyleSubtitle
	bold := lipgloss.NewStyle().Bold(true)
	accent := lipgloss.NewStyle().Foreground(ColorPrimary)
	good := lipgloss.NewStyle().Foreground(ColorSuccess)
	t := m.focusedAgent()

	var b strings.Builder
	b.WriteString(bold.Render("Statusline") + "\n")

	// Multi-agent banner (corrected — statusline now works beyond Claude Code).
	banner := "ⓘ Available for Claude Code, Cursor CLI, and Antigravity CLI. Codex & Gemini CLI\n  have no scriptable statusline yet — your memory still works there."
	b.WriteString(lipgloss.NewStyle().Foreground(ColorSecondary).Render(banner) + "\n\n")

	// Agent switcher row.
	b.WriteString(m.agentSwitcher() + "\n")

	// Current state line for the focused agent.
	cur := map[string]string{
		statusline.ModeFull:  "Auxly (replace) ✓ active",
		statusline.ModeWrap:  "Wrapper ✓ active",
		statusline.ModeOther: "the agent's own statusline",
		statusline.ModeNone:  "none",
	}[m.state.Mode]
	b.WriteString(dim.Render(t.Label+" · Current: ") + accent.Render(cur))
	if !m.detected {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorWarning).Render("(not installed)"))
	}
	b.WriteString("\n\n")

	if m.applying {
		opt := statuslineOptions[m.pendingIdx]
		warn := lipgloss.NewStyle().Foreground(ColorWarning)
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorWarning).Padding(1, 2).Render(
			warn.Render("⏳ Applying: "+opt.title) + "\n\n" +
				dim.Render("Updating "+t.Label+"'s configuration…"))
		b.WriteString(box + "\n")
		return b.String()
	}

	if m.confirming {
		opt := statuslineOptions[m.optionIdx]
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorPrimary).Padding(1, 2).Render(
			bold.Render("Apply: "+opt.title) + "\n\n" +
				dim.Render("This backs up "+t.Label+"'s current statusline and points it at Auxly.") + "\n" +
				dim.Render("Continue?") + "\n\n" +
				good.Render("[y] Yes, apply") + "    " + dim.Render("[n] / esc  Cancel"))
		b.WriteString(box + "\n")
		return b.String()
	}

	// Option list (③ presents as "Restore my original…" when a backup exists).
	for i := range statuslineOptions {
		cursor := "  "
		title, _ := m.optionAt(i)
		if i == m.optionIdx {
			cursor = accent.Render("▸ ")
			title = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(title)
		}
		active := ""
		if (i == 0 && m.state.Mode == statusline.ModeFull) ||
			(i == 1 && m.state.Mode == statusline.ModeWrap) ||
			(i == 2 && (m.state.Mode == statusline.ModeNone || m.state.Mode == statusline.ModeOther)) {
			active = good.Render("  ✓ active")
		}
		num := []string{"①", "②", "③"}[i]
		b.WriteString(cursor + num + " " + title + active + "\n")
	}
	_, desc := m.optionAt(m.optionIdx)
	b.WriteString("\n" + dim.Render(desc) + "\n")

	// Live preview (real data) for ①/②, rendered with the focused agent's provider so
	// the model + usage line match that agent; ③ has nothing to preview.
	if m.optionIdx != 2 {
		full := m.optionIdx == 0
		preview := statusline.Render(previewInputFor(t), full, t.Name)
		title := "PREVIEW (replaces the current):"
		if !full {
			title = "PREVIEW (the agent's statusline + Auxly appended):"
		}
		b.WriteString("\n" + dim.Render(title) + "\n")
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorDim).Padding(0, 1).Render(preview)
		b.WriteString(box + "\n")
	}

	if m.status != "" {
		style := good
		if strings.HasPrefix(m.status, "✗") {
			style = lipgloss.NewStyle().Foreground(ColorDanger)
		}
		b.WriteString("\n" + style.Render(m.status) + "\n")
	}

	if n := len(readClients()); n > 0 {
		syncState := lipgloss.NewStyle().Foreground(ColorWarning).Render("off")
		if config.LoadSettings().SyncStatuslineToRemotes {
			syncState = good.Render("on")
		}
		b.WriteString("\n" + accent.Render("[s]") + dim.Render(" sync this statusline to your "+strconv.Itoa(n)+" remote box(es) ▸  auto-sync: ") + syncState)
	}

	b.WriteString("\n" + dim.Render("a switch agent · ↑/↓ choose · ⏎ apply (with confirm) · or run: "+agentCLICommand(t, m.optionIdx)))
	if !m.detected {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorWarning).Render(t.Label+" not detected — options disabled."))
	}
	return b.String()
}

// agentCLICommand returns the equivalent CLI command for the focused agent + option.
// Claude omits --agent (it's the default); other agents include it.
func agentCLICommand(t statusline.Target, optionIdx int) string {
	flag := ""
	if t.Name != statusline.ProviderClaude {
		flag = " --agent " + t.Name
	}
	switch optionIdx {
	case 0:
		return "auxly statusline install" + flag
	case 1:
		return "auxly statusline install" + flag + " --wrap"
	default:
		return "auxly statusline uninstall" + flag
	}
}
