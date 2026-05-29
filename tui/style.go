package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Curated Style Tokens
var (
	ColorPrimary   = lipgloss.Color("038") // sleek cyan
	ColorSecondary = lipgloss.Color("134") // royal purple
	ColorSuccess   = lipgloss.Color("34")  // vibrant green
	ColorWarning   = lipgloss.Color("220") // warm yellow
	ColorDanger    = lipgloss.Color("196") // rich red
	ColorDim       = lipgloss.Color("240") // slate grey
	ColorAccent    = lipgloss.Color("205") // modern magenta

	// Typography Styles
	StyleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorSecondary)

	StyleSubtitle = lipgloss.NewStyle().
			Foreground(ColorDim)

	StyleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary)

	// Panel & Border Styles
	StyleBorderCard = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(1, 2)

	StyleHighlightCard = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(ColorPrimary).
			Padding(1, 2)

	// List Styles
	StyleSelectedRow = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("255"))

	StyleFooter = lipgloss.NewStyle().
			Foreground(ColorDim).
			Italic(true)

	// Badge Styles
	StyleBadgeSuccess = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorSuccess)

	StyleBadgeWarning = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorWarning)

	StyleBadgeDim = lipgloss.NewStyle().
			Foreground(ColorDim)
)

// RenderMetricCard builds a beautifully designed, highly professional numeric dashboard card.
func RenderMetricCard(emoji, label string, value int, highlight bool) string {
	borderCol := ColorDim
	if highlight {
		borderCol = ColorPrimary
	}

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderCol).
		Padding(1, 3).
		Width(24).
		Align(lipgloss.Center)

	valStr := fmt.Sprintf("%d", value)
	valStyle := lipgloss.NewStyle().Bold(true).Height(1).Width(18)
	if highlight {
		valStyle = valStyle.Foreground(ColorPrimary)
	} else {
		valStyle = valStyle.Foreground(ColorSecondary)
	}

	return cardStyle.Render(fmt.Sprintf("%s %s\n\n%s", emoji, label, valStyle.Render(fmt.Sprintf("  %s", valStr))))
}

// RenderTimelineNode builds a vertical timeline node in the history view.
func RenderTimelineNode(timeStr, icon, body string, isSelected bool) string {
	cursor := "  "
	bullet := lipgloss.NewStyle().Foreground(ColorDim).Render("│")
	if isSelected {
		cursor = lipgloss.NewStyle().Foreground(ColorPrimary).Render("▸ ")
		bullet = lipgloss.NewStyle().Foreground(ColorPrimary).Render("┃")
	}

	styledLine := fmt.Sprintf("%s%s  %s  %s  %s", 
		cursor,
		lipgloss.NewStyle().Foreground(ColorDim).Render(timeStr),
		bullet,
		icon,
		body,
	)

	if isSelected {
		return StyleSelectedRow.Render(styledLine)
	}
	return styledLine
}

// RenderProgressBar builds a solid, high-fidelity loading/progress bar.
func RenderProgressBar(current, total int, width int, barColor lipgloss.Color) string {
	if total <= 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
		return dimStyle.Render(strings.Repeat("-", width))
	}
	pct := (current * width) / total
	if pct > width {
		pct = width
	}
	barStyle := lipgloss.NewStyle().Foreground(barColor)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	filled := strings.Repeat("=", pct)
	empty := strings.Repeat("-", width-pct)

	return barStyle.Render(filled) + dimStyle.Render(empty)
}

// padLine pads a string to a specific visible terminal width, accounting for ANSI escapes and emojis.
func padLine(s string, width int) string {
	w := visibleWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// wrapText wraps a string to a maximum visible terminal column width, accounting for ANSI escapes and double-width characters.
func wrapText(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	var result []string
	paragraphs := strings.Split(text, "\n")
	for _, p := range paragraphs {
		words := strings.Fields(p)
		if len(words) == 0 {
			result = append(result, "")
			continue
		}
		var currentLine strings.Builder
		for _, word := range words {
			if currentLine.Len() == 0 {
				currentLine.WriteString(word)
			} else if visibleWidth(currentLine.String()+" "+word) <= limit {
				currentLine.WriteString(" " + word)
			} else {
				result = append(result, currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			result = append(result, currentLine.String())
		}
	}
	return result
}

