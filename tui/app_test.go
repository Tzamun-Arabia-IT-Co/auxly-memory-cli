package tui

import (
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
)

// TestMouseClickOnReviewBadgeHitsReviewTab is Finding 8's regression: the tab
// bar's badged "Review (N)" label is wider than the static "Review" entry in
// screenNames. The click hit-zone must be computed from the SAME label
// renderTabs draws (via labelFor) — otherwise a click on the badge itself
// silently lands past the (too-narrow) old hit-zone and does nothing.
func TestMouseClickOnReviewBadgeHitsReviewTab(t *testing.T) {
	m := *NewApp(t.TempDir())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(model)
	m.review.facts = make([]memory.StaleFact, 12) // "Review (12)" — wider than "Review"

	banner := renderBanner(m.width)
	tabRow := strings.Count(banner, "\n")

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
