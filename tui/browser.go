package tui

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type browserModel struct {
	store  *memory.Store
	files  []memory.FileInfo
	cursor int
}

type browserRefreshMsg struct {
	files []memory.FileInfo
}

func newBrowserModel(store *memory.Store) browserModel {
	return browserModel{store: store}
}

func (m browserModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		files, _ := m.store.List()
		return browserRefreshMsg{files: files}
	}
}

type OpenFileMsg struct {
	Filename string
	Content  string
}

func (m browserModel) Update(msg tea.Msg) (browserModel, tea.Cmd) {
	switch msg := msg.(type) {
	case browserRefreshMsg:
		m.files = msg.files
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.files)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor < len(m.files) {
				f := m.files[m.cursor]
				return m, func() tea.Msg {
					content, _ := m.store.View(f.Name)
					return OpenFileMsg{Filename: f.Name, Content: content}
				}
			}
		}
	}
	return m, nil
}

func (m browserModel) View() string {
	title := StyleTitle.Render("📁 File Browser")

	if len(m.files) == 0 {
		return title + "\n\nNo memory files found. Run 'auxly init' first."
	}

	header := fmt.Sprintf("  %-25s %8s %16s", "FILE", "SIZE", "MODIFIED")
	sep := lipgloss.NewStyle().Foreground(ColorDim).Render("  ─────────────────────────────────────────────────────")

	var content string
	for i, f := range m.files {
		cursor := "  "
		if i == m.cursor {
			cursor = lipgloss.NewStyle().Foreground(ColorPrimary).Render("▸ ")
		}
		
		line := fmt.Sprintf("%s%-25s %6d B %16s", cursor, f.Name, f.Size, f.ModTime.Format("02/01/2006 15:04:05"))
		if i == m.cursor {
			line = StyleSelectedRow.Render(line)
		}
		content += line + "\n"
	}

	return fmt.Sprintf("%s\n\n%s\n%s\n%s", title, header, sep, content)
}
