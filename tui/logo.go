package tui

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/assets"
	"github.com/charmbracelet/lipgloss"
)

func renderBanner(width int) string {
	// Clickable hyperlinks in modern terminal emulators (OSC 8)
	auxlyLink := fmt.Sprintf("\x1b]8;;https://auxly.io\x1b\\Auxly-Memory CLI\x1b]8;;\x1b\\")
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary).
		Render(auxlyLink)
		
	subtitle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Render("Unified Memory for AI Agents")
		
	tzamunLink := fmt.Sprintf("\x1b]8;;https://tzamun.sa\x1b\\Tzamun Arabia IT Co\x1b]8;;\x1b\\")
	developedBy := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true).
		Render(fmt.Sprintf("Developed by %s 🇸🇦", tzamunLink))

	version := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render("v1.0.0")

	// If the terminal is narrow, don't show the massive ASCII art
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
