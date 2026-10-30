package tui

import (
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TestMouseClickOnReviewBadgeHitsReviewTab is Finding 8's regression: the tab
// bar's badged "Review (N)" label is wider than the static "Review" entry in
// screenNames. The click hit-zone must be computed from the SAME label
// renderTabs draws (via labelFor) — otherwise a click on the badge itself
// silently lands past the (too-narrow) old hit-zone and does nothing.
func TestMouseClickOnReviewBadgeHitsReviewTab(t *testing.T) {
	m := *NewApp(t.TempDir())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 40})
	m = updated.(model)
	m.review.facts = make([]memory.StaleFact, 12) // "Review (12)" — wider than "Review"

	banner := renderBanner(m.width)
	tabRow := lipgloss.Height(banner)

	reviewIdx := int(screenReview)
	startX := 0
	for i := 0; i < reviewIdx; i++ {
		startX += 4 + len(m.labelFor(i)) + 2
	}
	staticWidth := 4 + len("Review") + 2
	badgeWidth := 4 + len(m.labelFor(reviewIdx)) + 2
	if badgeWidth <= staticWidth {
		t.Fatalf("test setup: badge should be wider than the static label (badge=%d static=%d)", badgeWidth, staticWidth)
	}

	// Click inside the extended badge zone — past where the OLD static-width
	// math would have placed the tab's right edge.
	clickX := startX + staticWidth + 1

	result, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: clickX, Y: tabRow})
	got, ok := result.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", result)
	}
	if got.screen != screenReview {
		t.Fatalf("click on the Review badge should select screenReview, got screen %d", got.screen)
	}
}

func TestMouseClickOnReviewBadgeHitsReviewTabWrapped(t *testing.T) {
	m := *NewApp(t.TempDir())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(model)
	m.review.facts = make([]memory.StaleFact, 12) // "Review (12)"

	out := m.View()
	lines := strings.Split(out, "\n")

	badgeLabel := m.labelFor(int(screenReview)) // "Review (12)"
	tabY := -1
	tabX := -1
	for y, line := range lines {
		clean := stripANSI(line)
		if idx := strings.Index(clean, badgeLabel); idx != -1 {
			tabY = y
			// Click near the end of the badge (past "Review") to verify the badge zone
			tabX = idx + len(badgeLabel) - 2
			break
		}
	}
	if tabY == -1 {
		t.Fatalf("could not find %q in View() lines at 100 cols:\n%s", badgeLabel, out)
	}

	result, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		X:      tabX,
		Y:      tabY,
	})
	got, ok := result.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", result)
	}
	if got.screen != screenReview {
		t.Fatalf("click on wrapped Review badge at (%d, %d) should select screenReview, got screen %d", tabX, tabY, got.screen)
	}
}

// TestLeavingSSHTabClearsInviteToken guards the "don't hold a secret around
// forever" requirement: a minted invite token must not survive switching
// away from the Remote tab, and must not reappear on switching back.
func TestLeavingSSHTabClearsInviteToken(t *testing.T) {
	m := NewApp(t.TempDir())
	m.gotoScreen(screenSSH)
	m.ssh.inviteToken = "auxly1-abc123"

	m.gotoScreen(screenActivity)
	if m.ssh.inviteToken != "" {
		t.Fatalf("inviteToken survived leaving the Remote tab: %q", m.ssh.inviteToken)
	}

	m.gotoScreen(screenSSH)
	if m.ssh.inviteToken != "" {
		t.Fatalf("inviteToken reappeared on returning to the Remote tab: %q", m.ssh.inviteToken)
	}
}

// TestLabelForBadgesReviewOnlyWhenNonEmpty locks labelFor's contract directly:
// every other tab is its static screenNames entry, and Review only gains the
// "(N)" badge while its queue is non-empty.
func TestLabelForBadgesReviewOnlyWhenNonEmpty(t *testing.T) {
	m := *NewApp(t.TempDir())
	if got := m.labelFor(int(screenDashboard)); got != "Dashboard" {
		t.Fatalf("labelFor(Dashboard) = %q, want %q", got, "Dashboard")
	}
	reviewIdx := int(screenReview)
	if got := m.labelFor(reviewIdx); got != "Review" {
		t.Fatalf("labelFor(Review) with empty queue = %q, want %q", got, "Review")
	}
	m.review.facts = make([]memory.StaleFact, 3)
	if got := m.labelFor(reviewIdx); got != "Review (3)" {
		t.Fatalf("labelFor(Review) with 3 facts = %q, want %q", got, "Review (3)")
	}
}

// TestOpsActionResultRepaintsContentViewport reproduces the "Ops panel not
// functional" report through the FULL app model (not opsModel in isolation).
// app.go routes opsRefreshMsg/opsActionMsg/opsSyncMsg/opsSpinTickMsg to
// settingsModel regardless of the active screen (so a busy op finishing
// after the user tabs away still lands) — but that early-return case skips
// m.syncViewport(), unlike every other early-return guard in the KeyMsg path.
// Settings renders through the shared content viewport, so its cached
// content (m.contentVP) goes stale: the model's busy/status fields update
// correctly, but the ON-SCREEN frame keeps showing the last-synced "busy"
// spinner forever — indistinguishable, to a user watching the real TUI, from
// the key doing nothing at all.
func TestOpsActionResultRepaintsContentViewport(t *testing.T) {
	orig := runAuxlySub
	defer func() { runAuxlySub = orig }()
	runAuxlySub = func(memPath string, args ...string) (string, error) {
		return "✅ claude notify hook installed.\n", nil
	}

	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	m.screen = screenSettings
	m.settings.subTab = 4
	m.settings.ops.loaded = true
	m.settings.ops.hooksRows = []hookStatusRow{{agent: "claude", status: "not-installed"}}
	m.settings.ops.cursor = 0
	m.syncViewport()

	result, cmd := m.Update(keyRunes("i"))
	m = result.(model)
	if !m.settings.ops.busy {
		t.Fatal("expected ops.busy after pressing [i] on a not-installed row")
	}
	if cmd == nil {
		t.Fatal("[i] should dispatch the install command batched with the spin tick")
	}
	if !strings.Contains(m.contentVP.View(), "Installing") {
		t.Fatalf("viewport should show the busy spinner right after the keypress:\n%s", m.contentVP.View())
	}

	// Feed the (stubbed) action result back through the FULL model, exactly
	// as bubbletea's runtime would when the tea.Cmd resolves.
	am := resolveActionMsg(t, cmd)
	result, _ = m.Update(am)
	m = result.(model)

	if m.settings.ops.busy {
		t.Fatal("ops.busy should have cleared once the action result landed")
	}
	view := stripANSI(m.contentVP.View())
	if strings.Contains(view, "Installing") {
		t.Fatalf("BUG: viewport still shows the stale busy frame after the action completed — the Ops panel looks frozen even though the model updated:\n%s", view)
	}
	if !strings.Contains(view, "installed") {
		t.Fatalf("viewport should show the completed status line once repainted, got:\n%s", view)
	}
}

func TestDashboardChromeTopRefreshesOnReviewTabWrap(t *testing.T) {
	// At w=140, without review facts, compact tabs fit on 1 row (compactTotal-1 == 140).
	// Adding review facts adds the "(1)" badge to the Review tab, increasing compact
	// tab width by 4 chars so it overflows 140 and wraps to 2 rows.
	w, h := 140, 40
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = u.(model)
	m.gotoScreen(screenDashboard)

	// Verify baseline: tab bar has 1 row
	baselineRects := m.tabLayout()
	if len(baselineRects) == 0 || baselineRects[0].totalRows != 1 {
		t.Fatalf("expected baseline tabs to fit on 1 row at w=%d, got %d", w, baselineRects[0].totalRows)
	}

	// Add review facts to make the Review tab badge appear: "Review (1)"
	m.review.facts = append(m.review.facts, memory.StaleFact{
		File: "identity.md",
		Line: "- test fact",
	})

	// Run an Update (harmless keypress on dashboard)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = u.(model)

	// Verify tab bar wrapped to 2 rows
	wrappedRects := m.tabLayout()
	if len(wrappedRects) == 0 || wrappedRects[0].totalRows != 2 {
		t.Fatalf("expected tabs to wrap to 2 rows at w=%d with review facts, got %d", w, wrappedRects[0].totalRows)
	}

	// Locate the row where the dashboard body actually starts in View() using per-line ansi.Strip
	out := m.View()
	lines := strings.Split(out, "\n")
	firstLineIdx := -1
	for y, line := range lines {
		clean := ansi.Strip(line)
		if strings.Contains(clean, "Auxly-Memory CLI Dashboard") {
			firstLineIdx = y
			break
		}
	}
	if firstLineIdx == -1 {
		t.Fatalf("could not find dashboard body in View():\n%s", out)
	}

	expectedTop := m.dashboard.chromeTop
	if m.dashboard.bodyCompact() {
		expectedTop = m.dashboard.chromeTopTight
	}
	if firstLineIdx != expectedTop {
		t.Errorf("Dashboard at %dx%d (compact=%v): first content row = %d, want expectedTop = %d (chromeTop=%d, chromeTopTight=%d)",
			w, h, m.dashboard.bodyCompact(), firstLineIdx, expectedTop, m.dashboard.chromeTop, m.dashboard.chromeTopTight)
	}
}
