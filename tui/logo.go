package tui

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/assets"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/charmbracelet/lipgloss"
)

// bannerLabels builds the three styled text lines shared by the banner
// (title+version, subtitle, attribution), with OSC-8 hyperlinks for terminals
// that support them.
func bannerLabels() (title, version, subtitle, developedBy string) {
	auxlyLink := "\x1b]8;;https://auxly.io/unified-memory\x1b\\Auxly-Memory CLI\x1b]8;;\x1b\\"
	title = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(auxlyLink)

	subtitle = lipgloss.NewStyle().Foreground(ColorDim).Render("Unified Memory for AI Agents")

	tzamunLink := "\x1b]8;;https://tzamun.sa\x1b\\Tzamun Arabia IT Co\x1b]8;;\x1b\\"
	developedBy = lipgloss.NewStyle().Foreground(ColorDim).Italic(true).
		Render(fmt.Sprintf("Developed by %s 🇸🇦", tzamunLink))

	version = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("v" + update.Current)
	return title, version, subtitle, developedBy
}

// renderBanner draws the full ASCII-art Auxly logo with the title labels beside
// it. The logo is always shown — the layout gives content room by scrolling it in
// a viewport, not by shrinking the brand. The only exception is a genuinely narrow
// terminal (<80 cols) where the 45-wide art can't fit horizontally; there it falls
// back to a compact text header (a width constraint, not a height one).
func renderBanner(width int) string {
	title, version, subtitle, developedBy := bannerLabels()

	if width > 0 && width < 80 {
		return fmt.Sprintf("  %s %s\n  %s\n  %s\n\n", title, version, subtitle, developedBy)
	}

	lines := strings.Split(strings.TrimRight(assets.LogoANS, "\n"), "\n")
	labels := []string{
		title + " " + version,
		subtitle,
		developedBy,
	}
	titleLine := 3

	var result strings.Builder
	for i, line := range lines {
		result.WriteString(line)
		if i >= titleLine && i-titleLine < len(labels) {
			result.WriteString("   " + labels[i-titleLine])
		}
		result.WriteString("\n")
	}
	result.WriteString("\033[0m\n")
	return result.String()
}
