package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// stripOSC8 removes OSC-8 hyperlink sequences (\x1b]8;;URL\x1b\ … \x1b]8;;\x1b\) so a
// test can inspect the visible link text without the embedded URL.
func stripOSC8(s string) string {
	for {
		i := strings.Index(s, "\x1b]8;;")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "\x1b\\")
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+2:]
	}
	return s
}

// osc8Inner returns the bytes between the OSC-8 open (for url) and the following OSC-8
// close, i.e. the styled link text region — used to count interior SGR resets.
func osc8Inner(s, url string) string {
	open := "\x1b]8;;" + url + "\x1b\\"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	if j := strings.Index(rest, "\x1b]8;;\x1b\\"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// TestBannerNamesAreClickableLinks locks the banner-link design: the product name and the
// company name are themselves the clickable links. Each must carry a SINGLE intact OSC-8
// region wrapping ONE contiguous styled span — no interior `\x1b[0m` resets, which is what
// Underline produced and what fragmented the clickable region in Warp. The visible on-screen
// text stays the human name — not a raw URL.
func TestBannerNamesAreClickableLinks(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	title, _, _, dev := bannerLabels()

	for _, c := range []struct{ name, s, url, label string }{
		{"title", title, "https://auxly.io/unified-memory", "Auxly-Memory CLI"},
		{"developedBy", dev, "https://tzamun.sa", "Tzamun Arabia IT Co"},
	} {
		// OSC-8 open sequence appears exactly once, intact (not shredded). The whole name
		// is wrapped in a single hyperlink region — that's what makes it click through.
		open := "\x1b]8;;" + c.url + "\x1b\\"
		if n := strings.Count(c.s, open); n != 1 {
			t.Errorf("%s: OSC-8 link for %s should appear once intact, found %d (shredded?)", c.name, c.url, n)
		}
		// The host must appear exactly once — inside the single intact OSC-8 open sequence.
		host := strings.TrimPrefix(c.url, "https://")
		if n := strings.Count(c.s, host); n != 1 {
			t.Errorf("%s: URL host %q should appear once (in the intact OSC-8), found %d — shredded?", c.name, host, n)
		}
		// CRITICAL: exactly ONE reset inside the link region. Underline's per-character
		// rendering emits a reset between every letter, fragmenting the clickable span.
		inner := osc8Inner(c.s, c.url)
		if n := strings.Count(inner, "\x1b[0m"); n != 1 {
			t.Errorf("%s: link region must be one contiguous span (1 reset), found %d — Underline back?", c.name, n)
		}
		// With OSC-8 + SGR stripped, the visible text is exactly the human name (no URL).
		visible := stripANSI(stripOSC8(c.s))
		if !strings.Contains(visible, c.label) {
			t.Errorf("%s: visible link text should be %q, got %q", c.name, c.label, visible)
		}
		if strings.Contains(visible, host) {
			t.Errorf("%s: URL host %q leaked into visible text", c.name, host)
		}
	}

	// The full banner (both wide and compact paths) carries the OSC-8 links intact.
	for _, w := range []int{140, 70} {
		b := renderBanner(w)
		for _, url := range []string{"https://auxly.io/unified-memory", "https://tzamun.sa"} {
			open := "\x1b]8;;" + url + "\x1b\\"
			if !strings.Contains(b, open) {
				t.Errorf("renderBanner(%d) must carry the intact OSC-8 link for %s", w, url)
			}
		}
	}

	// osc8 wraps from the outside with the correct sequence.
	if got := osc8("https://example.com", "X"); got != "\x1b]8;;https://example.com\x1b\\X\x1b]8;;\x1b\\" {
		t.Errorf("osc8 unexpected: %q", got)
	}
}
