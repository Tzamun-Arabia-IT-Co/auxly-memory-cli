package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// populatedDashboard builds a model whose dashboard carries realistic data: a full
// agent grid plus several live connections — the content that was overflowing.
func populatedDashboard(t *testing.T) model {
	t.Helper()
	m := *NewApp(t.TempDir())
	cards := make([]agentCard, 12)
	for i := range cards {
		cards[i] = agentCard{id: fmt.Sprintf("a%d", i), name: fmt.Sprintf("Agent %d", i)}
	}
	m.dashboard.stats = &audit.Stats{WritesToday: 2, TotalEntries: 143}
	m.dashboard.sessions = []agentSession{
		{Provider: "claude-code", Remote: true, Host: "open.claw", IP: "192.168.1.147", OS: "linux"},
		{Provider: "claude-code", Remote: true, Host: "tzamun.ai", IP: "192.168.1.141", OS: "linux"},
		{Provider: "claude-code", Remote: true, Host: "erp.tzamun.ai", IP: "192.168.1.168", OS: "linux"},
		{Provider: "claude", Remote: false, PID: 1},
		{Provider: "warp", Remote: false, PID: 2},
	}
	m.dashboard.cards = cards
	m.screen = screenDashboard
	return m
}

// TestDashboardFitsShortTerminals guards the goal: the full 12-agent dashboard must
// fit the wide-but-short terminals the user runs, WITHOUT scrolling — it uses more
// grid columns and tightens spacing instead.
func TestDashboardFitsShortTerminals(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{131, 32}, {175, 38}, {140, 30}, {126, 34}} {
		m := populatedDashboard(t)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = updated.(model)
		m.screen = screenDashboard
		out := m.View()
		if h := lipgloss.Height(out); h > sz.h {
			t.Errorf("%dx%d: dashboard height %d exceeds terminal %d", sz.w, sz.h, h, sz.h)
		}
		if strings.Contains(out, "scroll") {
			t.Errorf("%dx%d: must FIT, not scroll:\n%s", sz.w, sz.h, out)
		}
		// The bordered card design must be preserved (rounded corners present).
		if !strings.Contains(out, "╭") {
			t.Errorf("%dx%d: bordered cards must be kept", sz.w, sz.h)
		}
	}
}

// TestDashboardRichLookOnTallTerminal confirms the body compaction is conditional:
// a tall terminal keeps the full diagnostics (no body compaction).
func TestDashboardRichLookOnTallTerminal(t *testing.T) {
	m := populatedDashboard(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 131, Height: 50})
	m = updated.(model)

	full := m.dashboard.renderConnectionsSummary(false)
	compact := m.dashboard.renderConnectionsSummary(true)
	if lipgloss.Height(compact) >= lipgloss.Height(full) {
		t.Errorf("compact connections (%d) must be shorter than rich (%d)",
			lipgloss.Height(compact), lipgloss.Height(full))
	}
	// A tall terminal (height 50) with this content must NOT compact the body.
	if m.dashboard.bodyCompact() {
		t.Error("height 50 must keep the full body (content fits with room)")
	}
}

// TestLogoAlwaysFull proves the brand logo is never shrunk to a compact tier: on
// any wide-enough terminal renderBanner is the multi-row ASCII art, and the View
// always contains it — the compact/mini logo was removed.
func TestLogoAlwaysFull(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{160, 50}, {160, 38}, {140, 30}, {131, 24}} {
		banner := renderBanner(sz.w)
		if h := lipgloss.Height(banner); h < 6 {
			t.Errorf("%dx%d: banner is only %d rows — the full ASCII logo must always show", sz.w, sz.h, h)
		}
		m := populatedDashboard(t)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = updated.(model)
		m.screen = screenSettings // a content page — logo must still be full
		if !strings.Contains(m.View(), strings.Split(banner, "\n")[0]) {
			t.Errorf("%dx%d: View dropped the full logo", sz.w, sz.h)
		}
	}
}

// TestContentPagesScrollWhenTall is the core fix: a tall content page on a short
// terminal scrolls inside the viewport (full logo + tabs stay fixed) instead of
// being truncated or pushing the chrome off the top.
func TestContentPagesScrollWhenTall(t *testing.T) {
	m := populatedDashboard(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	m = updated.(model)
	// Switch to Settings (a tall form) via the real key path so the viewport syncs.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
	m = updated.(model)
	if !m.usesViewport() || !m.vpReady {
		t.Fatal("Settings must render through the content viewport")
	}
	if m.contentVP.TotalLineCount() <= m.contentVP.Height {
		t.Skip("settings content fit this size; nothing to scroll")
	}
	// The whole view fits the terminal (chrome never scrolls off).
	if h := lipgloss.Height(m.View()); h > 24 {
		t.Errorf("view height %d exceeds terminal 24 — chrome would scroll off", h)
	}
	// Page down actually scrolls.
	before := m.contentVP.YOffset
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(model)
	if m.contentVP.YOffset <= before {
		t.Errorf("PgDn did not scroll the viewport (offset %d → %d)", before, m.contentVP.YOffset)
	}
}

// TestChromeNeverClippedOnTallPages is the screenshot bug: switching to a tall
// page (Analytics, etc.) must not scroll the banner + tab menu off the top. The
// invariant that guarantees this in alt-screen mode is simply that the whole View
// never exceeds the terminal height — so the top chrome always stays on screen.
func TestChromeNeverClippedOnTallPages(t *testing.T) {
	screens := []screen{screenAnalytics, screenActivity, screenAuditTrail, screenBrowser, screenSkills, screenSettings}
	for _, sz := range []struct{ w, h int }{{120, 24}, {100, 20}, {140, 30}} {
		for _, sc := range screens {
			m := populatedDashboard(t)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
			m = updated.(model)
			m.screen = sc
			out := m.View()
			if h := lipgloss.Height(out); h > sz.h {
				t.Errorf("%s at %dx%d: view height %d exceeds terminal %d — chrome would scroll off",
					screenNames[sc], sz.w, sz.h, h, sz.h)
			}
			// The tab menu must be present in the output regardless.
			if !strings.Contains(stripANSI(out), "Analytics") {
				t.Errorf("%s at %dx%d: tab menu missing from view", screenNames[sc], sz.w, sz.h)
			}
		}
	}
}

// TestSettingsFitsWithoutScroll locks the good→perfect win: with a full agent
// roster, the Settings page fits the user's real terminals WITHOUT scrolling — the
// compact two-column override layout keeps it within both width and height.
func TestSettingsFitsWithoutScroll(t *testing.T) {
	roster := []detect.Agent{}
	for _, p := range []string{"claude", "claude-code", "antigravity", "cursor", "codex", "gemini", "copilot", "perplexity", "warp", "void", "android-studio"} {
		roster = append(roster, detect.Agent{Name: p, Provider: p, Command: "x"})
	}
	for _, sz := range []struct{ w, h int }{{175, 38}, {140, 32}} {
		m := *NewApp(t.TempDir())
		m.settings.agents = roster
		updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = updated.(model)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
		m = updated.(model)
		m.settings.agents = roster
		m.syncViewport()
		if over := m.contentVP.TotalLineCount() - m.contentVP.Height; over > 0 {
			t.Errorf("%dx%d: Settings overflows by %d rows — should fit without scroll", sz.w, sz.h, over)
		}
		// Width must not exceed the terminal (two columns must not push past the edge).
		for _, ln := range strings.Split(m.settings.View(), "\n") {
			if w := lipgloss.Width(ln); w > sz.w {
				t.Errorf("%dx%d: a Settings line is %d wide — exceeds terminal", sz.w, sz.h, w)
				break
			}
		}
	}
}

// TestAgentGridColumnsScaleWithWidth locks the dynamic grid: wider terminals get
// more columns (fewer rows), and compact mode packs one extra.
func TestAgentGridColumnsScaleWithWidth(t *testing.T) {
	if c, _ := agentGridLayout(200, 12, false); c < 3 {
		t.Errorf("200-wide should yield >= 3 columns, got %d", c)
	}
	// Compact packs at least as many columns as non-compact at the same width.
	cNormal, _ := agentGridLayout(131, 12, false)
	cCompact, _ := agentGridLayout(131, 12, true)
	if cCompact < cNormal {
		t.Errorf("compact should not have fewer columns (normal=%d compact=%d)", cNormal, cCompact)
	}
	// Never more columns than cards.
	if c, _ := agentGridLayout(240, 2, false); c > 2 {
		t.Errorf("2 cards must not exceed 2 columns, got %d", c)
	}
}

// TestSSHWizardRepaintsInViewport is the urgent regression: a sub-mode (the SSH
// connect wizard, editingHost=true) early-returns in the parent Update, so it must
// still refresh the content viewport — otherwise selecting a method advances the
// wizard's state but the screen keeps showing the stale frame ("nothing happens").
func TestSSHWizardRepaintsInViewport(t *testing.T) {
	m := *NewApp(t.TempDir())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}}) // Remote tab
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}) // connect wizard
	m = u.(model)
	if !m.ssh.editingHost || m.ssh.formStep != formStepMethod {
		t.Fatalf("'c' must open the method step (editingHost=%v step=%q)", m.ssh.editingHost, m.ssh.formStep)
	}
	before := stripANSI(m.contentVP.View())
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}) // pick "lan"
	m = u.(model)
	if m.ssh.formMethod != "lan" {
		t.Fatalf("digit '1' must select lan (got %q)", m.ssh.formMethod)
	}
	after := stripANSI(m.contentVP.View())
	if after == before {
		t.Error("viewport did NOT repaint after a wizard keypress — stale frame (the reported bug)")
	}
}
