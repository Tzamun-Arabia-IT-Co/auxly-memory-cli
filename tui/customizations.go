package tui

import (
	"os"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/statusline"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// statuslineApplyDelay holds the "Applying…" state on screen briefly before the
// confirmation lands. The write itself is near-instant; this makes the in-progress
// → done transition perceptible (and the apply feel deliberate, not a silent flip).
const statuslineApplyDelay = 450 * time.Millisecond

// customizationsModel backs the Settings → Customizations sub-tab: the opt-in
// Claude Code statusline (replace / wrap / none), with a live preview rendered from
// the user's real data and an in-process, reversible apply.
type customizationsModel struct {
	optionIdx  int  // 0 = replace (full), 1 = wrap, 2 = none/remove
	confirming bool // confirm dialog before applying ①/②
	applying   bool // in-progress: write dispatched, awaiting result
	pendingIdx int  // the option being applied (snapshot for the Applying… label)
	status     string
	state      statusline.State
	ccDetected bool
	width      int
	height     int
}

// statuslineAppliedMsg carries the result of an in-process install/uninstall back
// to the Customizations sub-tab (routed through settingsModel.Update).
type statuslineAppliedMsg struct {
	ok     bool
	status string
	state  statusline.State
}

var statuslineOptions = []struct{ title, desc string }{
	{"Use the Auxly statusline", "Replaces your statusline with Auxly's full 4-line view. Your old command is backed up."},
	{"Add Auxly to my current", "Keeps your statusline as-is and appends the Auxly line(s). Fully reversible."},
	{"None / Remove", "Removes the Auxly statusline and restores your backup, or no-ops if not installed."},
}

func claudeCodeDetected() bool {
	for _, a := range detect.InstalledAgents() {
		p := strings.ToLower(a.Provider + " " + a.Name)
		if strings.Contains(p, "claude") && (strings.Contains(p, "code") || strings.Contains(p, "cli")) {
			return true
		}
	}
	// Fallback: the Claude config dir exists.
	if home, err := os.UserHomeDir(); err == nil {
		if fi, err := os.Stat(home + "/.claude"); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// refresh re-reads the live statusline state and pre-selects the smart default:
// already-Auxly → match it; a user's own statusline → wrap; nothing → replace.
func (m *customizationsModel) refresh() {
	m.state = statusline.CurrentState()
	m.ccDetected = claudeCodeDetected()
	switch m.state.Mode {
	case statusline.ModeFull:
		m.optionIdx = 0
	case statusline.ModeWrap:
		m.optionIdx = 1
	case statusline.ModeOther:
		m.optionIdx = 1 // they already have one → default to wrapping it
	default:
		m.optionIdx = 0 // none → offer the full statusline
	}
}

func (m customizationsModel) handleKey(msg tea.KeyMsg) (customizationsModel, tea.Cmd) {
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
	case "j", "down":
		if m.optionIdx < len(statuslineOptions)-1 {
			m.optionIdx++
		}
	case "k", "up":
		if m.optionIdx > 0 {
			m.optionIdx--
		}
	case "enter", " ":
		if !m.ccDetected {
			m.status = "Claude Code not detected — statusline integration is unavailable here."
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
	return m, applyStatuslineCmd(m.optionIdx)
}

// applyStatuslineCmd performs the in-process install/uninstall via the same code
// path the CLI uses. It runs in a command goroutine (never in Update), so the brief
// hold that makes the "Applying…" state visible is a sleep here, not a UI block.
func applyStatuslineCmd(idx int) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(statuslineApplyDelay)
		var err error
		switch idx {
		case 0:
			err = statusline.Install(false)
		case 1:
			err = statusline.Install(true)
		case 2:
			err = statusline.Uninstall()
		}
		msg := statuslineAppliedMsg{ok: err == nil, state: statusline.CurrentState()}
		if err != nil {
			msg.status = "✗ " + err.Error()
			return msg
		}
		switch idx {
		case 0:
			msg.status = "✓ Auxly statusline installed (replace) — reload Claude Code to see it."
		case 1:
			msg.status = "✓ Auxly appended to your statusline (wrap) — reload Claude Code."
		case 2:
			msg.status = "✓ Removed; your previous statusline was restored."
		}
		return msg
	}
}

// handleApplied folds an apply result back into the model: clears the in-progress
// flag, records the confirmation/error, and refreshes the displayed current state.
func (m customizationsModel) handleApplied(msg statuslineAppliedMsg) customizationsModel {
	m.applying = false
	m.confirming = false
	m.status = msg.status
	m.state = msg.state
	return m
}

func (m customizationsModel) capturesInput() bool { return m.confirming || m.applying }

// previewInput synthesizes a Claude Code session for the preview from real local
// context (cwd + a representative model), so lines 3–4 use the user's actual data.
func previewInput() statusline.Input {
	var in statusline.Input
	if wd, err := os.Getwd(); err == nil {
		in.Workspace.CurrentDir = wd
	}
	in.Model.DisplayName = "Claude"
	return in
}

// panel renders the Customizations sub-tab body (the caller adds title + sub-tab bar).
func (m customizationsModel) panel() string {
	dim := StyleSubtitle
	bold := lipgloss.NewStyle().Bold(true)
	accent := lipgloss.NewStyle().Foreground(ColorPrimary)
	good := lipgloss.NewStyle().Foreground(ColorSuccess)

	var b strings.Builder
	b.WriteString(bold.Render("Claude Code statusline") + "\n")

	// Claude-Code-only banner (always shown).
	banner := "ⓘ Available for Claude Code only. Other CLIs (Codex, Gemini, Cursor…) have no\n  scriptable statusline yet — your memory still works there."
	b.WriteString(lipgloss.NewStyle().Foreground(ColorSecondary).Render(banner) + "\n")

	// Current state line.
	cur := map[string]string{
		statusline.ModeFull:  "Auxly (replace) ✓ active",
		statusline.ModeWrap:  "Wrapper ✓ active",
		statusline.ModeOther: "your own statusline",
		statusline.ModeNone:  "none",
	}[m.state.Mode]
	b.WriteString(dim.Render("Current: ") + accent.Render(cur) + "\n\n")

	if m.applying {
		opt := statuslineOptions[m.pendingIdx]
		warn := lipgloss.NewStyle().Foreground(ColorWarning)
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorWarning).Padding(1, 2).Render(
			warn.Render("⏳ Applying: "+opt.title) + "\n\n" +
				dim.Render("Updating your Claude Code configuration…"))
		b.WriteString(box + "\n")
		return b.String()
	}

	if m.confirming {
		opt := statuslineOptions[m.optionIdx]
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorPrimary).Padding(1, 2).Render(
			bold.Render("Apply: "+opt.title) + "\n\n" +
				dim.Render("This backs up your current Claude Code statusline and points it at Auxly.") + "\n" +
				dim.Render("Continue?") + "\n\n" +
				good.Render("[y] Yes, apply") + "    " + dim.Render("[n] / esc  Cancel"))
		b.WriteString(box + "\n")
		return b.String()
	}

	// Option list.
	for i, opt := range statuslineOptions {
		cursor := "  "
		title := opt.title
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
	b.WriteString("\n" + dim.Render(statuslineOptions[m.optionIdx].desc) + "\n")

	// Live preview (real data) for ①/②; ③ has nothing to preview.
	if m.optionIdx != 2 {
		full := m.optionIdx == 0
		preview := statusline.Render(previewInput(), full)
		title := "PREVIEW (replaces your current):"
		if !full {
			title = "PREVIEW (your statusline + Auxly appended):"
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

	cli := []string{"auxly statusline install", "auxly statusline install --wrap", "auxly statusline uninstall"}[m.optionIdx]
	b.WriteString("\n" + dim.Render("↑/↓ choose · ⏎ apply (with confirm) · or run: "+cli))
	if !m.ccDetected {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorWarning).Render("Claude Code not detected — options disabled."))
	}
	return b.String()
}
