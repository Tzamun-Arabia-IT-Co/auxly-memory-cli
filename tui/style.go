package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	runewidth "github.com/mattn/go-runewidth"
)

// parseTS parses an RFC3339 audit timestamp (stored in UTC) and returns it in
// the LOCAL zone, so the UI shows when events happened in the user's timezone
// rather than UTC (an event at 23:00Z displays as 02:00 the next day at UTC+3).
func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t.Local()
}

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

// barGlyphFilled / barGlyphEmpty are the single shared glyphs for EVERY bar in the TUI
// (progress, usage, charts, memory-composition) so they all share one look: filled ▰
// cells in a semantic colour, the remainder ▱ dimmed. Change these in one place and
// every bar follows.
const (
	barGlyphFilled = "▰"
	barGlyphEmpty  = "▱"
)

// renderMeter is the canonical Auxly bar: `filled` ▰ cells in fillColor followed by the
// remaining ▱ cells dimmed, `width` cells total. The single source of truth for bar
// styling — every bar renderer routes through it so they are visually identical.
func renderMeter(filled, width int, fillColor lipgloss.Color) string {
	if width < 0 {
		width = 0
	}
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	fill := lipgloss.NewStyle().Foreground(fillColor)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	return fill.Render(strings.Repeat(barGlyphFilled, filled)) +
		dim.Render(strings.Repeat(barGlyphEmpty, width-filled))
}

// renderLoadingBar is the canonical IN-FLIGHT loading bar: a determinate ▰/▱ meter whose
// filled region carries a moving bright "glint" so it keeps showing LIVE activity even
// while the fill holds near the creep ceiling during a long opaque wait (the model
// round-trip in Memory Org, a box's server-side `connect auto`). frame is any
// ever-incrementing counter (a spinner tick); pct is 0–100.
func renderLoadingBar(pct, width, frame int, fillColor lipgloss.Color) string {
	if width < 1 {
		width = 1
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := pct * width / 100
	fill := lipgloss.NewStyle().Foreground(fillColor)
	glint := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(ColorDim)

	head := loadingGlintHead(filled, frame)
	var b strings.Builder
	for i := 0; i < filled; i++ {
		// A 2-cell bright "comet" (head + the cell behind it) reads as motion better than
		// a lone dot; both still render the shared ▰ glyph, only brighter.
		if i == head || i == head-1 {
			b.WriteString(glint.Render(barGlyphFilled))
		} else {
			b.WriteString(fill.Render(barGlyphFilled))
		}
	}
	b.WriteString(dim.Render(strings.Repeat(barGlyphEmpty, width-filled)))
	return b.String()
}

// loadingGlintHead is the position of the moving glint within a `filled`-cell run for a
// given frame — it sweeps the filled region and repeats, so the bar keeps showing live
// activity even while the fill amount holds. Returns -1 when there is nothing filled.
func loadingGlintHead(filled, frame int) int {
	if filled <= 0 {
		return -1
	}
	return ((frame % filled) + filled) % filled
}

// RenderProgressBar builds a determinate ▰/▱ progress bar (the shared Auxly style).
func RenderProgressBar(current, total int, width int, barColor lipgloss.Color) string {
	filled := 0
	if total > 0 {
		filled = current * width / total
	}
	return renderMeter(filled, width, barColor)
}

// RenderIndeterminateBar renders an animated marquee for work that has no measurable
// sub-progress (an atomic step). A filled ▰ segment sweeps back and forth across a dim
// ▱ track, driven by an ever-incrementing frame counter, so the step reads as "working"
// instead of a determinate bar frozen at 0%.
func RenderIndeterminateBar(frame, width int, barColor lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	seg := width / 4
	if seg < 2 {
		seg = 2
	}
	span := width - seg // travel distance of the segment across the track
	if span < 1 {
		span = 1
	}
	// Triangle wave 0→span→0 so the block sweeps right, then back left.
	p := ((frame % (2 * span)) + 2*span) % (2 * span)
	if p > span {
		p = 2*span - p
	}
	barStyle := lipgloss.NewStyle().Foreground(barColor)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	return dimStyle.Render(strings.Repeat(barGlyphEmpty, p)) +
		barStyle.Render(strings.Repeat(barGlyphFilled, seg)) +
		dimStyle.Render(strings.Repeat(barGlyphEmpty, width-seg-p))
}

// padLine pads a string to a specific visible terminal width, accounting for ANSI escapes and emojis.
func padLine(s string, width int) string {
	w := visibleWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// clampLine guarantees a line never exceeds width visible columns so a bordered
// panel built from these lines can't grow past the terminal width and wrap/
// mangle the whole layout. Lines that fit are returned untouched (color kept);
// over-long lines (e.g. a long captured command line with no break points) are
// stripped of ANSI and hard-truncated with an ellipsis.
func clampLine(s string, width int) string {
	if width <= 0 || visibleWidth(s) <= width {
		return s
	}
	return runewidth.Truncate(stripANSI(s), width, "…")
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
