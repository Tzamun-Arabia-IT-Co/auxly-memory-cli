package tui

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/assets"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/charmbracelet/lipgloss"
)

// bannerLabels builds the three styled text lines shared by the banner
// (title+version, subtitle, attribution). The product name ("Auxly-Memory CLI") and the
// company name ("Tzamun Arabia IT Co") are themselves the clickable links: each carries an
// OSC-8 hyperlink AND is underlined, so it reads as a link and clicks through to the site.
func bannerLabels() (title, version, subtitle, developedBy string) {
	// The product + company names ARE the hyperlinks. Two rules make the link actually
	// click through:
	//   1. Style the PLAIN label first, THEN wrap OSC-8 from the OUTSIDE. Passing an
	//      already-OSC-8 string back through lipgloss shreds the escape (leaks the URL).
	//   2. Do NOT use Underline — lipgloss renders an underlined string CHARACTER BY
	//      CHARACTER, emitting a `\x1b[0m` reset between every letter. Those interior
	//      resets fragment the OSC-8 hyperlink span, so terminals (Warp especially) show
	//      the styling but drop the click. Bold + accent colour stays one contiguous span
	//      inside a single clean hyperlink region — and IS the visible link cue.
	title = osc8("https://auxly.io/unified-memory",
		lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("Auxly-Memory CLI"))

	subtitle = lipgloss.NewStyle().Foreground(ColorDim).Render("Unified Memory for AI Agents")

	dimItalic := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	tzamun := osc8("https://tzamun.sa",
		lipgloss.NewStyle().Foreground(ColorPrimary).Italic(true).Render("Tzamun Arabia IT Co"))
	developedBy = dimItalic.Render("Developed by ") + tzamun + dimItalic.Render(" 🇸🇦")

	version = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("v" + update.Current)
	return title, version, subtitle, developedBy
}

// osc8 wraps already-styled text in an OSC-8 terminal hyperlink. Wrap from the OUTSIDE —
// the styled text must not be passed back through lipgloss, or the escape gets mangled.
func osc8(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
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
