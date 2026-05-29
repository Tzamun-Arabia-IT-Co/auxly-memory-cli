package tui

import (
	"fmt"
	"sort"

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

// kvCount is a single (key, count) pair used for stable, sorted rendering.
type kvCount struct {
	key   string
	count int
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

	barStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("134"))
	var content string

	content += "\n📡 Writes per Provider:\n"
	providers := sortedCounts(m.stats.ByProvider)
	if len(providers) > 0 {
		for _, p := range providers {
			name := p.key
			if name == "" {
				name = "(unknown)"
			}
			content += fmt.Sprintf("   %-15s %3d %s\n", name, p.count, barStyle.Render(barOf(p.count, 40)))
		}
	} else {
		content += "   (no data yet)\n"
	}

	content += "\n📊 Actions Breakdown:\n"
	for _, a := range sortedCounts(m.stats.ByAction) {
		content += fmt.Sprintf("   %-15s %d\n", a.key, a.count)
	}

	content += fmt.Sprintf("\n📝 Total Entries: %d", m.stats.TotalEntries)
	content += fmt.Sprintf("\n✍️  Writes Today: %d", m.stats.WritesToday)

	return fmt.Sprintf("%s\n%s", title, content)
}

// sortedCounts returns map entries in a STABLE order — by count descending, then
// key ascending — so the view does not reshuffle on every render (Go randomizes
// map iteration order, which made the chart jump around).
func sortedCounts(m map[string]int) []kvCount {
	out := make([]kvCount, 0, len(m))
	for k, v := range m {
		out = append(out, kvCount{key: k, count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	return out
}

// barOf renders a bar of `count` blocks, capped at max.
func barOf(count, max int) string {
	if count > max {
		count = max
	}
	bar := make([]rune, 0, count)
	for i := 0; i < count; i++ {
		bar = append(bar, '█')
	}
	return string(bar)
}
