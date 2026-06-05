package statusline

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// lineSeg is one display chunk of a statusline row with a drop priority. When the joined
// row is wider than the terminal, joinFit drops the LOWEST-priority segments first until it
// fits; the highest-priority segment (prioPinned, conventionally the first/brand chunk) is
// never dropped. Higher prio = kept longer.
type lineSeg struct {
	text string
	prio int
}

// prioPinned marks a segment that must never be dropped (folder, branch, brand anchors).
const prioPinned = 1 << 30

// joinFit joins segs with sep, dropping the lowest-priority segments until the visible
// width fits within maxWidth (display order is preserved among the survivors). When
// maxWidth <= 0 it joins everything unchanged. If even the survivors overflow (e.g. a
// single very long pinned segment), the result is hard-truncated with an ellipsis.
func joinFit(segs []lineSeg, sep string, maxWidth int) string {
	present := make([]bool, len(segs))
	for i := range present {
		present[i] = true
	}
	join := func() string {
		parts := make([]string, 0, len(segs))
		for i, s := range segs {
			if present[i] {
				parts = append(parts, s.text)
			}
		}
		return strings.Join(parts, sep)
	}
	if maxWidth > 0 {
		for visibleWidth(join()) > maxWidth {
			lowIdx, lowPrio := -1, prioPinned
			for i, s := range segs {
				if present[i] && s.prio < lowPrio {
					lowPrio, lowIdx = s.prio, i
				}
			}
			if lowIdx < 0 { // only pinned segments left — stop dropping
				break
			}
			present[lowIdx] = false
		}
	}
	return truncateVisible(join(), maxWidth)
}

// visibleWidth is the on-screen cell width of s: SGR escapes stripped, then measured with
// go-runewidth so wide CJK and emoji count as 2 cells.
func visibleWidth(s string) int { return runewidth.StringWidth(stripSGR(s)) }

// stripSGR removes CSI SGR escape sequences (\x1b[ … m). UTF-8 content bytes never collide
// with the 0x1b introducer, so a byte copy preserves multibyte runes intact.
func stripSGR(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j // loop's i++ steps past the terminating 'm'
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// truncateVisible cuts s to at most maxWidth visible cells, preserving SGR escapes and
// appending "…" plus a reset so a clipped style never bleeds onto the next line. Returns s
// unchanged when maxWidth <= 0 or it already fits.
func truncateVisible(s string, maxWidth int) string {
	if maxWidth <= 0 || visibleWidth(s) <= maxWidth {
		return s
	}
	limit := maxWidth - 1 // leave a cell for the ellipsis
	var b strings.Builder
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' { // copy SGR verbatim, zero width
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if w+rw > limit {
			break
		}
		b.WriteString(s[i : i+size])
		w += rw
		i += size
	}
	b.WriteString("…")
	b.WriteString(cReset)
	return b.String()
}
