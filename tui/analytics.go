package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/usage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type analyticsModel struct {
	logger *audit.Logger
	stats  *audit.Stats

	// Sub-tabs: 0 = Activity (audit metrics), 1 = Usage (live quota panel).
	activeTab    int
	usageMgr     *usage.Manager
	usageReports map[string]usage.Report
	liveUsage    bool
}

type analyticsRefreshMsg struct {
	stats *audit.Stats
}

// kvCount is a single (key, count) pair used for stable, sorted rendering.
type kvCount struct {
	key   string
	count int
}

func newAnalyticsModel(logger *audit.Logger, usageMgr *usage.Manager) analyticsModel {
	return analyticsModel{
		logger:       logger,
		usageMgr:     usageMgr,
		usageReports: map[string]usage.Report{},
		liveUsage:    config.LoadSettings().LiveUsage,
	}
}

func (m analyticsModel) Refresh() tea.Cmd {
	m.liveUsage = config.LoadSettings().LiveUsage
	statsCmd := func() tea.Msg {
		var stats *audit.Stats
		if m.logger != nil {
			stats, _ = m.logger.Stats()
		}
		return analyticsRefreshMsg{stats: stats}
	}
	if m.liveUsage && m.usageMgr != nil {
		return tea.Batch(statsCmd, usageFetchCmd(m.usageMgr))
	}
	return statsCmd
}

func (m analyticsModel) Update(msg tea.Msg) (analyticsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case analyticsRefreshMsg:
		m.stats = msg.stats
	case usageReportsMsg:
		for _, r := range msg.reports {
			m.usageReports[r.Provider] = r
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h", "right", "l":
			m.activeTab = 1 - m.activeTab // toggle Activity <-> Usage
		}
	}
	return m, nil
}

func (m analyticsModel) View(width int) string {
	if width <= 0 {
		width = 80
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary).Render("📈 Agent Analytics")
	tabs := m.renderSubTabs()

	if m.activeTab == 1 {
		return title + "\n" + tabs + "\n\n" + m.renderUsagePanel(width)
	}

	if m.stats == nil {
		return title + "\n" + tabs + "\n\n" + lipgloss.NewStyle().Foreground(ColorDim).Render("Loading…")
	}
	s := m.stats
	sections := []string{
		renderKPIRow(s),
		renderBarSection("📡 Writes per Provider", sortedCounts(s.ByProvider), width, s.TotalEntries, 0),
		renderBarSection("📊 Activity by Action", sortedCounts(s.ByAction), width, s.TotalActivity, 8),
		renderInsights(s),
	}
	return title + "\n" + tabs + "\n\n" + strings.Join(sections, "\n\n")
}

// renderSubTabs draws the Activity/Usage selector. Switched with ←/→ (the global
// number keys are reserved for top-level screen navigation).
func (m analyticsModel) renderSubTabs() string {
	on := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	off := lipgloss.NewStyle().Foreground(ColorDim)
	activity, usage := off.Render("  Activity  "), off.Render("  Usage  ")
	if m.activeTab == 0 {
		activity = on.Render("▸ Activity ")
	} else {
		usage = on.Render("▸ Usage ")
	}
	hint := off.Render("  (←/→ switch)")
	return activity + usage + hint
}

// usagePanelOrder fixes the row order so the panel doesn't reshuffle.
var usagePanelOrder = []struct{ id, name string }{
	{"claude", "Claude"},
	{"claude-code", "Claude Code"},
	{"codex", "Codex"},
	{"gemini", "Gemini"},
	{"antigravity", "Antigravity"},
	{"cursor", "Cursor"},
}

// renderUsagePanel draws the rich per-brand Live Usage view for the Analytics
// Usage tab: each agent's account + tier, then its quota meters with resets.
func (m analyticsModel) renderUsagePanel(width int) string {
	if !m.liveUsage {
		return lipgloss.NewStyle().Foreground(ColorDim).
			Render("Live Usage is off. Enable it in Settings (6) to see per-agent quota here.")
	}
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	return renderUsageRows(m.usageReports, 16) + "\n\n" +
		dim.Render("↻ live · reuses each agent's own login · [r] on Dashboard force-refreshes")
}

// renderUsageRows renders the per-brand Live Usage blocks (header = glyph + name
// + account/tier, body = labeled meters + resets). Shared by the Analytics Usage
// tab and the Dashboard [u] popup. barW sets the meter width.
func renderUsageRows(reports map[string]usage.Report, barW int) string {
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	bold := lipgloss.NewStyle().Bold(true)
	now := time.Now()

	var b strings.Builder
	for _, brand := range usagePanelOrder {
		report, ok := reports[brand.id]
		header := brandMark(brand.id) + " " + bold.Render(fmt.Sprintf("%-12s", brand.name))
		id := ""
		if ok {
			id = usageIdentityLine(report)
		}
		b.WriteString(header + "  " + dim.Render(id) + "\n")

		switch {
		case !ok:
			b.WriteString("    " + dim.Render("…") + "\n")
		case report.Err != "":
			b.WriteString("    " + dim.Render("— "+report.Err) + "\n")
		default:
			for _, w := range report.Windows {
				reset := ""
				if r := usage.FormatReset(w.ResetAt, now); r != "" {
					reset = dim.Render("resets " + r)
				}
				b.WriteString(fmt.Sprintf("    %s %s %s   %s\n",
					dim.Render(fmt.Sprintf("%-8s", w.Label)),
					usageBar(w.Pct, barW, brand.id),
					bold.Render(fmt.Sprintf("%3.0f%%", w.Pct)),
					reset,
				))
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// usageIdentityLine joins the account email, subscription tier, and org that a
// report exposes, into a single dim line. Returns "" when none are known.
func usageIdentityLine(r usage.Report) string {
	parts := []string{}
	if r.Account != "" {
		parts = append(parts, r.Account)
	}
	if r.Plan != "" {
		parts = append(parts, r.Plan)
	}
	if r.Org != "" {
		parts = append(parts, r.Org)
	}
	return strings.Join(parts, "  ·  ")
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
		bar := renderMeter(barLen, maxBar, palette[i%len(palette)])

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

// truncate shortens s to at most max RUNES (not bytes) — a byte-based cut can
// land mid-character on multi-byte UTF-8 (brand names, non-ASCII file names)
// and render garbage at the boundary.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	if max == 1 {
		return string(r[:1])
	}
	return string(r[:max-1]) + "…"
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
