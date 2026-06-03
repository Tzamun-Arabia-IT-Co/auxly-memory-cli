package tui

import (
	"encoding/base64"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Brand marks are tiny colored sprites drawn with half-block characters: each
// "▀" cell paints two stacked pixels (foreground = top pixel, background =
// bottom pixel), so a 4-pixel-tall mark renders in the two text lines of an
// agent card. Pure Unicode + 24-bit color — no Nerd Font and no emoji, so it
// renders identically wherever truecolor is supported.

type brandSprite struct {
	rows []string        // one string per pixel row; each byte is a palette key, space = transparent
	pal  map[rune]string // palette key -> "#RRGGBB"
}

// brandSprites are 6 rows tall by 6 columns wide → three rendered text lines.
var brandSprites = map[string]brandSprite{
	// Anthropic clay sunburst.
	"claude": {
		rows: []string{
			"  aa  ",
			"a aa a",
			" aaaa ",
			" aaaa ",
			"a aa a",
			"  aa  ",
		},
		pal: map[rune]string{'a': "#D97757"},
	},
	// Anthropic clay terminal prompt: chevron + underline (Claude Code).
	"claude-code": {
		rows: []string{
			"a     ",
			" a    ",
			"  a   ",
			" a    ",
			"a     ",
			" aaaa ",
		},
		pal: map[rune]string{'a': "#D97757"},
	},
	// Rainbow "A" (Antigravity).
	"antigravity": {
		rows: []string{
			"  rr  ",
			"  rr  ",
			" oooo ",
			" gggg ",
			"cc  cc",
			"bb  bb",
		},
		pal: map[rune]string{'r': "#FF5555", 'o': "#FFB86C", 'g': "#50FA7B", 'c': "#8BE9FD", 'b': "#BD93F9"},
	},
	// White I-beam text cursor (Cursor).
	"cursor": {
		rows: []string{
			"wwwwww",
			"  ww  ",
			"  ww  ",
			"  ww  ",
			"  ww  ",
			"wwwwww",
		},
		pal: map[rune]string{'w': "#E6E6E6"},
	},
	// OpenAI-green ring (Codex).
	"codex": {
		rows: []string{
			" gggg ",
			"g    g",
			"g    g",
			"g    g",
			"g    g",
			" gggg ",
		},
		pal: map[rune]string{'g': "#10A37F"},
	},
	// Google blue→purple sparkle diamond (Gemini).
	"gemini": {
		rows: []string{
			"  bb  ",
			" bbbb ",
			"bbbbbb",
			"pppppp",
			" pppp ",
			"  pp  ",
		},
		pal: map[rune]string{'b': "#4285F4", 'p': "#9168C0"},
	},
}

// brandAccent is each brand's signature color, used by the clean emblem. Kept
// distinct so the six cards read apart at a glance.
var brandAccent = map[string]string{
	"claude":      "#D97757", // Anthropic clay
	"claude-code": "#E0916F", // lighter clay
	"antigravity": "#C158DC", // vivid magenta
	"cursor":      "#C7CDD6", // cool grey
	"codex":       "#74E0B0", // OpenAI-ish green
	"gemini":      "#4285F4", // Google blue
}

// brandGlyph is one distinct symbol per brand, each chosen to hint at the
// product: Anthropic sunburst (claude), a CLI prompt (claude-code), a space
// star (antigravity), an I-beam text cursor (cursor), a hexagon (codex), a
// sparkle diamond (gemini). Rendered in the brand's accent color so the six
// cards read apart at a glance — no block art, no emoji width quirks.
var brandGlyph = map[string]string{
	"claude":      "✶",
	"claude-code": "❯",
	"antigravity": "✦",
	"cursor":      "▮",
	"codex":       "⬡",
	"gemini":      "◆",
}

// brandMark returns the brand's emblem: a single accent-colored glyph. It sits
// on the card's name line (see the dashboard card render), reading like a
// brand-colored bullet rather than the old generic filled tile.
func brandMark(id string) string {
	g, ok := brandGlyph[id]
	if !ok {
		g = "◆"
	}
	c, ok := brandAccent[id]
	if !ok {
		c = "#84DCFB"
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(c)).Render(g)
}

// brandMarkLogo returns the real baked logo lines for a brand (used only if a
// larger-card mode is enabled), falling back to the hand sprite.
func brandMarkLogo(id string) []string {
	if logo, ok := brandLogo(id); ok {
		return logo
	}
	if s, ok := brandSprites[id]; ok {
		return renderSprite(s)
	}
	return []string{"      ", "      ", "      "}
}

// brandLogoCache memoizes decoded logos; the TUI runs single-threaded so a plain
// map is safe.
var brandLogoCache = map[string][]string{}

// brandLogo decodes a baked, base64-encoded colored logo into its text lines.
func brandLogo(id string) ([]string, bool) {
	if v, ok := brandLogoCache[id]; ok {
		return v, v != nil
	}
	b64, ok := brandLogosB64[id]
	if !ok {
		brandLogoCache[id] = nil
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		brandLogoCache[id] = nil
		return nil, false
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	brandLogoCache[id] = lines
	return lines, true
}

// renderSprite folds the sprite's pixel rows into half-block text lines, pairing
// each two pixel rows into one line of "▀"/"▄"/" " cells.
func renderSprite(s brandSprite) []string {
	width := 0
	for _, r := range s.rows {
		if len(r) > width {
			width = len(r)
		}
	}
	var lines []string
	for r := 0; r < len(s.rows); r += 2 {
		var sb strings.Builder
		for c := 0; c < width; c++ {
			tc := s.pal[spriteKeyAt(s.rows, r, c)]
			bc := s.pal[spriteKeyAt(s.rows, r+1, c)]
			switch {
			case tc == "" && bc == "":
				sb.WriteString(" ")
			case tc != "" && bc != "":
				sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(tc)).Background(lipgloss.Color(bc)).Render("▀"))
			case tc != "":
				sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(tc)).Render("▀"))
			default:
				sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(bc)).Render("▄"))
			}
		}
		lines = append(lines, sb.String())
	}
	return lines
}

func spriteKeyAt(rows []string, r, c int) rune {
	if r < 0 || r >= len(rows) || c < 0 || c >= len(rows[r]) {
		return ' '
	}
	return rune(rows[r][c])
}
