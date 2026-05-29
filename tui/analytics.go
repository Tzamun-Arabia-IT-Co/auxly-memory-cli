package tui

import (
	"fmt"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type analyticsModel struct {
	logger *audit.Logger
	stats  *audit.Stats
}

type analyticsRefreshMsg struct {
	stats *audit.Stats
}

func newAnalyticsModel(logger *audit.Logger) analyticsModel {
	return analyticsModel{logger: logger}
}

func (m analyticsModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		var stats *audit.Stats
		if m.logger != nil {
			stats, _ = m.logger.Stats()
		}
		return analyticsRefreshMsg{stats: stats}
	}
}

func (m analyticsModel) Update(msg tea.Msg) (analyticsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case analyticsRefreshMsg:
		m.stats = msg.stats
	}
	return m, nil
}

func (m analyticsModel) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("134"))
	title := titleStyle.Render("📈 Agent Analytics")

	if m.stats == nil {
		return title + "\n\nLoading..."
	}

	var content string

	content += "\n📡 Writes per Provider:\n"
	if len(m.stats.ByProvider) > 0 {
		for provider, count := range m.stats.ByProvider {
			bar := ""
			for i := 0; i < count && i < 40; i++ {
				bar += "█"
			}
			content += fmt.Sprintf("   %-15s %3d %s\n", provider, count, lipgloss.NewStyle().Foreground(lipgloss.Color("134")).Render(bar))
		}
	} else {
		content += "   (no data yet)\n"
	}

	content += "\n📊 Actions Breakdown:\n"
	if len(m.stats.ByAction) > 0 {
		for action, count := range m.stats.ByAction {
			content += fmt.Sprintf("   %-15s %d\n", action, count)
		}
	}

	content += fmt.Sprintf("\n📝 Total Entries: %d", m.stats.TotalEntries)
	content += fmt.Sprintf("\n✍️  Writes Today: %d", m.stats.WritesToday)

	return fmt.Sprintf("%s\n%s", title, content)
}
