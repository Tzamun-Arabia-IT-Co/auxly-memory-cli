package tui

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type searchModel struct {
	store   *memory.Store
	query   string
	results map[string][]string
	typing  bool
}

type searchResultMsg struct {
	results map[string][]string
}

func newSearchModel(store *memory.Store) searchModel {
	return searchModel{store: store, typing: true}
}

func (m searchModel) Refresh() tea.Cmd {
	return nil
}

func (m searchModel) Update(msg tea.Msg) (searchModel, tea.Cmd) {
	switch msg := msg.(type) {
	case searchResultMsg:
		m.results = msg.results
	case tea.KeyMsg:
		if m.typing {
			switch msg.String() {
			case "enter":
				m.typing = false
				return m, m.doSearch()
			case "backspace":
				if len(m.query) > 0 {
					m.query = m.query[:len(m.query)-1]
				}
			case "esc":
				m.typing = false
				m.query = ""
				m.results = nil
			default:
				if len(msg.String()) == 1 {
					m.query += msg.String()
				}
			}
		} else {
			switch msg.String() {
			case "s", "/":
				m.typing = true
			}
		}
	}
	return m, nil
}

func (m searchModel) doSearch() tea.Cmd {
	query := m.query
	store := m.store
	return func() tea.Msg {
		results, _ := store.Search(query)
		return searchResultMsg{results: results}
	}
}

func (m searchModel) View() string {
	title := StyleTitle.Render("🔍 Search")

	// Search input
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorDim).
		Padding(0, 1).
		Width(50)

	cursor := ""
	if m.typing {
		cursor = "█"
	}
	input := inputStyle.Render(fmt.Sprintf("Search: %s%s", m.query, cursor))

	// Results
	var resultContent string
	if m.results != nil {
		if len(m.results) == 0 {
			resultContent = "\nNo results found."
		} else {
			for file, lines := range m.results {
				resultContent += fmt.Sprintf("\n📄 %s\n", lipgloss.NewStyle().Bold(true).Render(file))
				for _, line := range lines {
					highlighted := highlightQuery(line, m.query)
					resultContent += fmt.Sprintf("   %s\n", highlighted)
				}
			}
		}
	} else if !m.typing {
		resultContent = "\nPress 's' or '/' to start searching."
	} else {
		resultContent = "\nType your query and press Enter."
	}

	return fmt.Sprintf("%s\n\n%s\n%s", title, input, resultContent)
}

func highlightQuery(line, query string) string {
	if query == "" {
		return line
	}
	lowerLine := strings.ToLower(line)
	lowerQuery := strings.ToLower(query)

	style := lipgloss.NewStyle().Background(lipgloss.Color("220")).Foreground(lipgloss.Color("0"))

	var result strings.Builder
	start := 0
	for {
		idx := strings.Index(lowerLine[start:], lowerQuery)
		if idx == -1 {
			result.WriteString(line[start:])
			break
		}
		actualIdx := start + idx
		result.WriteString(line[start:actualIdx])
		matchedText := line[actualIdx : actualIdx+len(query)]
		result.WriteString(style.Render(matchedText))
		start = actualIdx + len(query)
	}
	return result.String()
}
