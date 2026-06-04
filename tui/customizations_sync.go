package tui

import (
	"os/exec"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// remoteSyncDoneMsg carries the result of a "sync now" exec back into the model.
type remoteSyncDoneMsg struct {
	summary string
	err     error
}

// syncSpinTickMsg drives the in-progress spinner while a sync exec runs, so the
// panel repaints (and looks alive) during the multi-second SSH push.
type syncSpinTickMsg struct{}

func syncSpinTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return syncSpinTickMsg{} })
}

// openSync loads the connected boxes + persisted sync prefs and opens the sub-panel.
func (m customizationsModel) openSync() customizationsModel {
	s := config.LoadSettings()
	m.syncOpen = true
	m.syncCursor = 0
	m.syncBoxes = readClients()
	m.syncMaster = s.SyncStatuslineToRemotes
	m.syncSelected = map[string]bool{}
	for _, n := range s.StatuslineSyncBoxes {
		m.syncSelected[n] = true
	}
	m.syncStatus = ""
	return m
}

// persistSync writes the master toggle + per-box selection back to settings so the
// choice survives and the auto-sync hook can read it.
func (m customizationsModel) persistSync() {
	s := config.LoadSettings()
	s.SyncStatuslineToRemotes = m.syncMaster
	var boxes []string
	for _, b := range m.syncBoxes {
		if m.syncSelected[b.Name] {
			boxes = append(boxes, b.Name)
		}
	}
	s.StatuslineSyncBoxes = boxes
	_ = config.SaveSettings(s)
}

// allSelected reports whether every box is currently selected for auto-sync.
func (m customizationsModel) allSelected() bool {
	if len(m.syncBoxes) == 0 {
		return false
	}
	for _, b := range m.syncBoxes {
		if !m.syncSelected[b.Name] {
			return false
		}
	}
	return true
}

func (m customizationsModel) handleSyncKey(msg tea.KeyMsg) (customizationsModel, tea.Cmd) {
	if m.syncing {
		// A push is in flight — don't TRAP the user. esc/q leaves immediately (the
		// background exec finishes on its own; its result is dropped on a closed
		// panel). Every other key is ignored so a mid-sync toggle can't race.
		switch msg.String() {
		case "esc", "q":
			m.syncOpen = false
			m.syncing = false
			m.syncStatus = ""
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q", "s":
		m.syncOpen = false
		return m, nil
	case "j", "down":
		if m.syncCursor < len(m.syncBoxes) {
			m.syncCursor++
		}
	case "k", "up":
		if m.syncCursor > 0 {
			m.syncCursor--
		}
	case " ":
		// Toggle the focused row: master auto-sync (row 0) or a box's membership.
		if m.syncCursor == 0 {
			m.syncMaster = !m.syncMaster
		} else {
			b := m.syncBoxes[m.syncCursor-1]
			m.syncSelected[b.Name] = !m.syncSelected[b.Name]
		}
		m.persistSync()
	case "a", "A":
		// Select all / none for auto-sync.
		want := !m.allSelected()
		for _, b := range m.syncBoxes {
			m.syncSelected[b.Name] = want
		}
		m.persistSync()
	case "enter":
		if m.syncCursor == 0 {
			m.syncMaster = !m.syncMaster
			m.persistSync()
			return m, nil
		}
		// Sync now — just the focused box (one-by-one).
		b := m.syncBoxes[m.syncCursor-1]
		m.syncing = true
		m.syncSpin = 0
		m.syncStatus = "Syncing " + b.Name
		return m, tea.Batch(syncRemoteStatuslineCmd(true, b.Name), syncSpinTick())
	case "y", "Y":
		// Sync now — all boxes at once.
		if len(m.syncBoxes) == 0 {
			return m, nil
		}
		m.syncing = true
		m.syncSpin = 0
		m.syncStatus = "Syncing all boxes"
		return m, tea.Batch(syncRemoteStatuslineCmd(false), syncSpinTick())
	}
	return m, nil
}

// handleSyncDone folds a "sync now" result back into the model.
func (m customizationsModel) handleSyncDone(msg remoteSyncDoneMsg) customizationsModel {
	m.syncing = false
	switch {
	case msg.err != nil:
		m.syncStatus = "✗ sync failed — " + firstLineOf(msg.summary)
	case msg.summary != "":
		m.syncStatus = "✓ " + msg.summary
	default:
		m.syncStatus = "✓ synced"
	}
	return m
}

// syncRemoteStatuslineCmd execs `auxly host statusline` off the UI thread: either a
// specific box (oneBox + name) or every box (--all). Pushes the statusline + usage
// preference to the box(es) without touching their binaries.
func syncRemoteStatuslineCmd(oneBox bool, names ...string) tea.Cmd {
	return func() tea.Msg {
		args := []string{"host", "statusline"}
		if oneBox {
			args = append(args, names...)
		} else {
			args = append(args, "--all")
		}
		out, err := exec.Command(exePath(), args...).CombinedOutput()
		return remoteSyncDoneMsg{summary: lastNonEmptyLine(string(out)), err: err}
	}
}

// autoSyncStatuslineCmd is the post-apply hook: when the master toggle is on, push
// the just-applied statusline to the selected boxes. A no-op (nil) when auto-sync is
// off or no box is selected, so it never fires unexpectedly.
func autoSyncStatuslineCmd() tea.Cmd {
	s := config.LoadSettings()
	if !s.SyncStatuslineToRemotes || len(s.StatuslineSyncBoxes) == 0 {
		return nil
	}
	boxes := append([]string{}, s.StatuslineSyncBoxes...)
	return syncRemoteStatuslineCmd(true, boxes...)
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// syncPanel renders the remote statusline-sync sub-panel.
func (m customizationsModel) syncPanel() string {
	dim := StyleSubtitle
	bold := lipgloss.NewStyle().Bold(true)
	accent := lipgloss.NewStyle().Foreground(ColorPrimary)
	good := lipgloss.NewStyle().Foreground(ColorSuccess)
	warn := lipgloss.NewStyle().Foreground(ColorWarning)

	var b strings.Builder
	b.WriteString(bold.Render("Sync statusline to remote machines") + "\n")
	b.WriteString(dim.Render("Push THIS machine's statusline preference (mode + usage) to your boxes over SSH.") + "\n\n")

	if len(m.syncBoxes) == 0 {
		b.WriteString(warn.Render("No connected boxes — wire one from the Remote tab first.") + "\n")
		b.WriteString("\n" + dim.Render("[s]/esc back"))
		return b.String()
	}

	// Row 0 — master auto-sync toggle.
	masterMark := warn.Render("[ off ]")
	if m.syncMaster {
		masterMark = good.Render("[ on  ]")
	}
	cursor := "  "
	label := "Auto-sync when I change my statusline"
	if m.syncCursor == 0 {
		cursor = accent.Render("▸ ")
		label = bold.Render(label)
	}
	b.WriteString(cursor + masterMark + " " + label + "\n\n")

	// Box rows — per-box auto-sync membership + one-by-one sync.
	b.WriteString(dim.Render("Boxes (space = include in auto-sync, ⏎ = sync this one now):") + "\n")
	for i, box := range m.syncBoxes {
		mark := dim.Render("[ ]")
		if m.syncSelected[box.Name] {
			mark = good.Render("[✓]")
		}
		rc := "  "
		name := box.Name
		if m.syncCursor == i+1 {
			rc = accent.Render("▸ ")
			name = bold.Render(name)
		}
		b.WriteString(rc + mark + " " + name + dim.Render("  "+box.Target) + "\n")
	}

	if m.syncStatus != "" {
		switch {
		case m.syncing:
			b.WriteString("\n" + warn.Render(spinnerFrame(m.syncSpin)+" "+m.syncStatus+" — pushing over SSH…") + "\n")
		case strings.HasPrefix(m.syncStatus, "✗"):
			b.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorDanger).Render(m.syncStatus) + "\n")
		default:
			b.WriteString("\n" + good.Render(m.syncStatus) + "\n")
		}
	}

	if m.syncing {
		b.WriteString("\n" + dim.Render("syncing over SSH… ") + accent.Render("[esc]") + dim.Render(" leave (it finishes in the background)"))
	} else {
		b.WriteString("\n" + dim.Render("↑/↓ move · space toggle · ⏎ sync this box · ") +
			accent.Render("[y]") + dim.Render(" sync ALL now · ") +
			accent.Render("[a]") + dim.Render(" all/none · ") +
			accent.Render("[s]/esc") + dim.Render(" back"))
	}
	return b.String()
}
