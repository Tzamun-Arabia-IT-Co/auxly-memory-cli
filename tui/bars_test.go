package tui

import (
	"strings"
	"testing"
)

// barFill counts the filled cells of a rendered bar (after stripping ANSI), given the
// glyphs a particular renderer uses for "filled".
func barFill(s string, fillRunes string) int {
	n := 0
	for _, r := range stripANSI(s) {
		if strings.ContainsRune(fillRunes, r) {
			n++
		}
	}
	return n
}

// TestRenderProgressBarProportional locks the generic determinate bar: empty at 0,
// full at total, proportional in between, and a graceful dashed track when total ≤ 0.
func TestRenderProgressBarProportional(t *testing.T) {
	const w = 40
	if f := barFill(RenderProgressBar(0, 10, w, ColorPrimary), "▰"); f != 0 {
		t.Errorf("0/10 should have no filled cells, got %d", f)
	}
	if f := barFill(RenderProgressBar(10, 10, w, ColorPrimary), "▰"); f != w {
		t.Errorf("10/10 should fill the whole bar (%d), got %d", w, f)
	}
	if f := barFill(RenderProgressBar(5, 10, w, ColorPrimary), "▰"); f != w/2 {
		t.Errorf("5/10 should fill half (%d), got %d", w/2, f)
	}
	// total ≤ 0 must not divide-by-zero; it renders a dim empty (▱) track of full width.
	got := stripANSI(RenderProgressBar(3, 0, w, ColorPrimary))
	if len([]rune(got)) != w || strings.Contains(got, "▰") {
		t.Errorf("total=0 should render a %d-wide empty track with no fill, got %q", w, got)
	}
}

// TestRenderIndeterminateBarAnimates is the fix for atomic-step bars sitting at 0%: the
// marquee always shows a filled segment, keeps a constant width, and MOVES as the frame
// advances (so the step reads as "working", never frozen).
func TestRenderIndeterminateBarAnimates(t *testing.T) {
	const w = 40
	a := stripANSI(RenderIndeterminateBar(0, w, ColorSuccess))
	if len([]rune(a)) != w {
		t.Fatalf("indeterminate bar must be exactly %d wide, got %d", w, len([]rune(a)))
	}
	if barFill(a, "▰") == 0 {
		t.Error("indeterminate bar must always show a filled segment (never empty)")
	}
	// The segment position changes across frames — sample several and require movement.
	moved := false
	prev := a
	for f := 1; f < 12; f++ {
		cur := stripANSI(RenderIndeterminateBar(f, w, ColorSuccess))
		if cur != prev {
			moved = true
			break
		}
		prev = cur
	}
	if !moved {
		t.Error("indeterminate bar did not move across frames — it would look frozen")
	}
	// Negative/odd frames must not panic or break width (defensive: triangle-wave math).
	if len([]rune(stripANSI(RenderIndeterminateBar(-7, w, ColorSuccess)))) != w {
		t.Error("indeterminate bar width must hold for any frame value")
	}
}

// TestSSHProgressBarProportional locks the connect/update bar (█/░).
func TestSSHProgressBarProportional(t *testing.T) {
	const w = 30
	if f := barFill(progressBar(0, w), "▰"); f != 0 {
		t.Errorf("0%% should have no filled blocks, got %d", f)
	}
	if f := barFill(progressBar(100, w), "▰"); f != w {
		t.Errorf("100%% should fill all %d blocks, got %d", w, f)
	}
	if f := barFill(progressBar(50, w), "▰"); f != w/2 {
		t.Errorf("50%% should fill half, got %d", f)
	}
}

// TestUsageBarProportional locks the usage meter (▰/▱).
func TestUsageBarProportional(t *testing.T) {
	const w = 12
	if f := barFill(usageBar(0, w, "claude"), "▰"); f != 0 {
		t.Errorf("0%% usage should have no filled cells, got %d", f)
	}
	if f := barFill(usageBar(100, w, "claude"), "▰"); f != w {
		t.Errorf("100%% usage should fill all %d cells, got %d", w, f)
	}
	if f := barFill(usageBar(50, w, "claude"), "▰"); f < w/2-1 || f > w/2+1 {
		t.Errorf("50%% usage should fill ~half (%d), got %d", w/2, f)
	}
}

// TestAllBarsShareGlyphs locks the unified style: every bar primitive renders using
// ONLY the shared ▰/▱ glyphs — no stray █/░/=/- from a renderer that drifted.
func TestAllBarsShareGlyphs(t *testing.T) {
	onlyMeterGlyphs := func(name, s string) {
		for _, r := range stripANSI(s) {
			switch r {
			case '▰', '▱':
				// shared glyphs — OK
			default:
				t.Errorf("%s emitted a non-shared bar glyph %q in %q", name, string(r), stripANSI(s))
				return
			}
		}
	}
	onlyMeterGlyphs("renderMeter", renderMeter(4, 12, ColorPrimary))
	onlyMeterGlyphs("RenderProgressBar", RenderProgressBar(3, 10, 20, ColorSuccess))
	onlyMeterGlyphs("RenderIndeterminateBar", RenderIndeterminateBar(5, 20, ColorSuccess))
	onlyMeterGlyphs("progressBar", progressBar(55, 30))
	onlyMeterGlyphs("usageBar", usageBar(42, 12, "claude"))
}

// TestLoadingBarStaysLiveAtCeiling is the fix for "the bar reaches 90% and looks frozen":
// the glint sweeps the filled region so the bar shows live activity, while the fill amount
// and width stay constant and only the shared ▰/▱ glyphs are used. The glint is a colour
// highlight (invisible to the colour-stripped test renderer), so its MOTION is verified
// through the pure head function rather than the rendered string.
func TestLoadingBarStaysLiveAtCeiling(t *testing.T) {
	const w, pct = 30, 90 // held near the creep ceiling
	filled := pct * w / 100

	// The glint stays in range and sweeps across the filled region as the frame advances.
	seen := map[int]bool{}
	for f := 0; f < filled*2; f++ {
		h := loadingGlintHead(filled, f)
		if h < 0 || h >= filled {
			t.Fatalf("glint head %d out of range [0,%d) at frame %d", h, filled, f)
		}
		seen[h] = true
	}
	if len(seen) < filled/2 {
		t.Errorf("the glint should sweep the filled region, only hit %d of %d cells", len(seen), filled)
	}
	if loadingGlintHead(0, 5) != -1 {
		t.Error("an empty bar has no glint head")
	}

	// The rendered bar holds its fill count + width and uses only the shared glyphs.
	a := stripANSI(renderLoadingBar(pct, w, 7, ColorPrimary))
	if barFill(a, "▰") != filled {
		t.Errorf("held fill should be %d ▰ cells, got %d", filled, barFill(a, "▰"))
	}
	if len([]rune(a)) != w {
		t.Errorf("loading bar must be %d wide, got %d", w, len([]rune(a)))
	}
	for _, r := range a {
		if r != '▰' && r != '▱' {
			t.Errorf("loading bar must use only ▰/▱, found %q", string(r))
		}
	}
}
