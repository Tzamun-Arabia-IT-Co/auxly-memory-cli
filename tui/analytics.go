package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

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

func (m analyticsModel) View(width int) string {
	if width <= 0 {
		width = 80
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary).Render("📈 Agent Analytics")

	if m.stats == nil {
		return title + "\n\n" + lipgloss.NewStyle().Foreground(ColorDim).Render("Loading…")
	}
	s := m.stats

	sections := []string{
		title,
		renderKPIRow(s),
		renderBarSection("📡 Writes per Provider", sortedCounts(s.ByProvider), width, s.TotalEntries, 0),
		renderBarSection("📊 Activity by Action", sortedCounts(s.ByAction), width, s.TotalActivity, 8),
		renderInsights(s),
	}
	return strings.Join(sections, "\n\n")
}

// ── KPI cards ───────────────────────────────────────────────────────────────

func renderKPIRow(s *audit.Stats) string {
	cards := []string{
		kpiCard("WRITES", fmt.Sprintf("%d", s.TotalEntries), ColorSecondary),
		kpiCard("TODAY", fmt.Sprintf("%d", s.WritesToday), ColorSuccess),
		kpiCard("ACTIVITY", fmt.Sprintf("%d", s.TotalActivity), ColorPrimary),
		kpiCard("READS", fmt.Sprintf("%d", s.ReadCount), ColorAccent),
		kpiCard("PROVIDERS", fmt.Sprintf("%d", len(s.ByProvider)), ColorWarning),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cards...)
}

func kpiCard(label, value string, color lipgloss.Color) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorDim).
		Padding(0, 1).
		Width(12).
		MarginRight(1).
		Align(lipgloss.Center)
	lbl := lipgloss.NewStyle().Foreground(ColorDim).Render(label)
	val := lipgloss.NewStyle().Bold(true).Foreground(color).Render(value)
	return box.Render(lbl + "\n" + val)
}

// ── Bar charts ────────────────────────────────────────────────────────────────

// renderBarSection draws a proportional, multi-colour bar chart. total is used
// for the percentage column; topN > 0 caps the rows (with a "+N more" footer).
func renderBarSection(heading string, items []kvCount, width, total, topN int) string {
	head := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(heading)
	if len(items) == 0 {
		return head + "\n   " + lipgloss.NewStyle().Foreground(ColorDim).Render("(no data yet)")
	}

	const labelW, countW = 16, 4
	maxBar := width - labelW - countW - 14
	if maxBar < 10 {
		maxBar = 10
	}
	if maxBar > 48 {
		maxBar = 48
	}
	max := items[0].count // items are sorted by count desc

	hidden := 0
	if topN > 0 && len(items) > topN {
		hidden = len(items) - topN
		items = items[:topN]
	}

	palette := []lipgloss.Color{ColorSecondary, ColorPrimary, ColorAccent, ColorSuccess, ColorWarning, ColorDanger}
	dim := lipgloss.NewStyle().Foreground(ColorDim)

	var b strings.Builder
	b.WriteString(head + "\n")
	for i, it := range items {
		name := it.key
		if name == "" {
			name = "(unknown)"
		}
		name = truncate(name, labelW)

		barLen := 0
		if max > 0 {
			barLen = it.count * maxBar / max
		}
		if barLen < 1 && it.count > 0 {
			barLen = 1
		}
		bar := lipgloss.NewStyle().Foreground(palette[i%len(palette)]).Render(strings.Repeat("█", barLen))

		pct := ""
		if total > 0 {
			pct = dim.Render(fmt.Sprintf(" %3d%%", it.count*100/total))
		}
		b.WriteString(fmt.Sprintf("   %-*s %*d  %s%s\n", labelW, name, countW, it.count, bar, pct))
	}
	if hidden > 0 {
		b.WriteString("   " + dim.Render(fmt.Sprintf("… +%d more", hidden)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── Insights ──────────────────────────────────────────────────────────────────

func renderInsights(s *audit.Stats) string {
	head := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("🔎 Insights")

	topProvider, topCount := "—", 0
	for p, c := range s.ByProvider {
		if c > topCount || (c == topCount && p < topProvider) {
			topProvider, topCount = p, c
		}
	}
	if topProvider == "" {
		topProvider = "(unknown)"
	}

	rows := [][2]string{
		{"Most active provider", fmt.Sprintf("%s  (%d writes)", topProvider, topCount)},
		{"Read / Write ratio", fmt.Sprintf("%d reads · %d writes", s.ReadCount, s.TotalEntries)},
		{"Write source split", fmt.Sprintf("%d local · %d ssh-remote", s.LocalWrites, s.RemoteWrites)},
		{"Last write", relativeTime(s.LastWriteTime)},
	}

	label := lipgloss.NewStyle().Foreground(ColorDim)
	value := lipgloss.NewStyle().Foreground(ColorPrimary)
	var b strings.Builder
	b.WriteString(head + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("   %s  %s\n", label.Render(fmt.Sprintf("%-22s", r[0])), value.Render(r[1])))
	}
	b.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorDim).Render(fmt.Sprintf("Total entries: %d   •   Writes today: %d", s.TotalActivity, s.WritesToday)))
	return strings.TrimRight(b.String(), "\n")
}

// ── helpers ───────────────────────────────────────────────────────────────────

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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// relativeTime renders an RFC3339 timestamp as a friendly "Xh ago" string.
func relativeTime(ts string) string {
	if ts == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
