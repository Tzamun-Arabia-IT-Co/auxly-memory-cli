package tui

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type diffModel struct {
	mgr     *pending.Manager
	files   []pending.PendingFile
	cursor  int
	viewing string
	status  string // last approve/reject outcome (e.g. a conflict) shown under the list
}

type diffRefreshMsg struct {
	files []pending.PendingFile
}

func newDiffModel(mgr *pending.Manager) diffModel {
	return diffModel{mgr: mgr}
}

func (m diffModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		files, _ := m.mgr.List()
		return diffRefreshMsg{files: files}
	}
}

func (m diffModel) Update(msg tea.Msg) (diffModel, tea.Cmd) {
	switch msg := msg.(type) {
	case diffRefreshMsg:
		m.files = msg.files
		m.viewing = ""
		if m.cursor >= len(m.files) {
			m.cursor = len(m.files) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.files)-1 {
				m.cursor++
			}
			m.status = "" // status describes the item it happened on — don't let it stick to another
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			m.status = ""
		case "enter":
			if m.cursor < len(m.files) {
				content, _ := m.mgr.ViewDiff(m.files[m.cursor].Name)
				m.viewing = content
			}
		case "a":
			if m.cursor < len(m.files) {
				// Conflicts (target edited since the pending was created) must be
				// visible, not silently swallowed — the item stays queued.
				if err := m.mgr.Approve(m.files[m.cursor].Name); err != nil {
					m.status = err.Error()
				} else {
					m.status = ""
				}
				return m, m.Refresh()
			}
		case "r":
			if m.cursor < len(m.files) {
				if err := m.mgr.Reject(m.files[m.cursor].Name); err != nil {
					m.status = err.Error()
				} else {
					m.status = ""
				}
				return m, m.Refresh()
			}
		case "esc":
			m.viewing = ""
		}
	}
	return m, nil
}

func (m diffModel) View() string {
	title := StyleTitle.Render("📋 Approval Queue")

	if m.viewing != "" {
		diffStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(1, 2)
		return fmt.Sprintf("%s\n\n%s",
			title,
			diffStyle.Render(m.viewing),
		)
	}

	if len(m.files) == 0 {
		return title + "\n\n✅ No pending approvals."
	}

	var content string
	for i, f := range m.files {
		cursor := "  "
		if i == m.cursor {
			cursor = lipgloss.NewStyle().Foreground(ColorPrimary).Render("▸ ")
		}

		line := fmt.Sprintf("%s%-40s %s", cursor, f.Name, f.ModTime.Format("2006-01-02 15:04"))
		if i == m.cursor {
			line = StyleSelectedRow.Render(line)
		}
		content += line + "\n"
	}

	if m.status != "" {
		content += "\n" + lipgloss.NewStyle().Foreground(ColorWarning).Render("⚠ "+m.status) + "\n"
	}

	return fmt.Sprintf("%s\n\n%s", title, content)
}
