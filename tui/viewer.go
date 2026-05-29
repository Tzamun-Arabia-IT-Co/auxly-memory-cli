package tui

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type viewerModel struct {
	store    *memory.Store
	filename string
	content  string
	lines    []string
	scrollY  int
	height   int
	width    int
}

func newViewerModel(store *memory.Store) viewerModel {
	return viewerModel{store: store, height: 18, width: 80}
}

func (m viewerModel) Update(msg tea.Msg) (viewerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.scrollY < len(m.lines)-m.height {
				m.scrollY++
			}
		case "k", "up":
			if m.scrollY > 0 {
				m.scrollY--
			}
		case "pgdown":
			m.scrollY += m.height
			if m.scrollY > len(m.lines)-m.height {
				m.scrollY = len(m.lines) - m.height
			}
			if m.scrollY < 0 {
				m.scrollY = 0
			}
		case "pgup":
			m.scrollY -= m.height
			if m.scrollY < 0 {
				m.scrollY = 0
			}
		}
	case tea.WindowSizeMsg:
		chrome := 24
		if msg.Width < 80 {
			chrome = 19
		}
		m.height = msg.Height - chrome
		if m.height < 3 {
			m.height = 3
		}
		m.width = msg.Width
	}
	return m, nil
}

func (m viewerModel) View() string {
	title := StyleTitle.Render(fmt.Sprintf("📖 Viewing: %s", m.filename))

	if len(m.lines) == 0 {
		return title + "\n\n(Empty file)"
	}

	visibleLines := m.lines[m.scrollY:]
	if len(visibleLines) > m.height {
		visibleLines = visibleLines[:m.height]
	}

	boxWidth := m.width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}
	contentWidth := boxWidth - 6

	processedLines := make([]string, len(visibleLines))
	for i, line := range visibleLines {
		line = strings.ReplaceAll(line, "\t", "    ")
		if len(line) > contentWidth {
			processedLines[i] = line[:contentWidth]
		} else {
			processedLines[i] = line
		}
	}

	contentBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorDim).
		Padding(1, 2).
		Width(boxWidth).
		Render(strings.Join(processedLines, "\n"))

	scrollPct := 0
	if len(m.lines) > m.height {
		scrollPct = (m.scrollY * 100) / (len(m.lines) - m.height)
	}
	scrollIndicator := StyleFooter.Render(fmt.Sprintf("Line %d-%d of %d (%d%%) • Press Esc to go back", m.scrollY+1, m.scrollY+len(visibleLines), len(m.lines), scrollPct))

	return fmt.Sprintf("%s\n\n%s\n\n%s", title, contentBox, scrollIndicator)
}
