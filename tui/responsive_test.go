package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestResponsiveAllScreens(t *testing.T) {
	sizes := []struct {
		w, h int
	}{
		{80, 24},
		{100, 20},
		{100, 30},
		{120, 32},
		{130, 35},
		{145, 40},
		{150, 40},
		{165, 45},
		{220, 60},
	}

	for _, sz := range sizes {
		for sc := screenDashboard; sc < screenViewer; sc++ {
			m := populatedDashboard(t)
			u, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
			m = u.(model)
			m.gotoScreen(sc)

			out := m.composeView()

			// 1. Height never exceeds terminal height
			if h := lipgloss.Height(out); h > sz.h {
				t.Errorf("%s at %dx%d: view height %d exceeds terminal %d",
					screenNames[sc], sz.w, sz.h, h, sz.h)
			}

			// 2. No rendered line wider than terminal width (on composeView without safety net)
			for lineIdx, line := range strings.Split(out, "\n") {
				if w := lipgloss.Width(line); w > sz.w {
					t.Errorf("%s at %dx%d: line %d width %d exceeds terminal %d: %q",
						screenNames[sc], sz.w, sz.h, lineIdx, w, sz.w, line)
				}
			}

			clean := stripANSI(out)

			// 3. Every tab label (labelFor) appears in the output
			for i := 0; i < int(screenViewer); i++ {
				label := m.labelFor(i)
				if !strings.Contains(clean, label) {
					t.Errorf("%s at %dx%d: tab label %q missing from view",
						screenNames[sc], sz.w, sz.h, label)
				}
			}

			// 4. All footer hints appear in the output (compare after collapsing the wrap)
			cleanCollapsed := strings.Join(strings.Fields(clean), " ")
			tokens := splitFooterHints(m.rawFooter())
			for _, tok := range tokens {
				hint := strings.TrimSpace(tok.text)
				if hint == "" {
					continue
				}
				hintCollapsed := strings.Join(strings.Fields(hint), " ")
				if !strings.Contains(cleanCollapsed, hintCollapsed) {
					t.Errorf("%s at %dx%d: footer hint %q missing from view",
						screenNames[sc], sz.w, sz.h, hint)
				}
			}

			// 5. No screen may show the "more — enlarge window" clamp
			if strings.Contains(clean, "enlarge window") {
				t.Errorf("%s at %dx%d: contains clamp text 'enlarge window'",
					screenNames[sc], sz.w, sz.h)
			}
		}
	}
}

func TestTerminalTooSmall(t *testing.T) {
	m := populatedDashboard(t)
	u, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = u.(model)
	out := m.View()

	want := "Terminal too small — enlarge to at least 80 columns"
	if !strings.Contains(out, want) {
		t.Fatalf("expected View() to contain %q, got:\n%s", want, out)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("expected View() to be a single line, got %d lines", strings.Count(out, "\n")+1)
	}
	for lineIdx, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("line %d width %d exceeds 60: %q", lineIdx, w, line)
		}
	}
}

func TestMouseClickTabsWrapped(t *testing.T) {
	m := populatedDashboard(t)
	u, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	m = u.(model)

	rects := m.tabLayout()
	if len(rects) == 0 {
		t.Fatal("no tab rects returned")
	}
	if rects[0].totalRows < 2 {
		t.Fatalf("expected tabs to be wrapped onto at least 2 rows at 100 cols, got %d rows", rects[0].totalRows)
	}

	hasSecondRow := false
	for idx := 0; idx < int(screenViewer); idx++ {
		// Ensure we start from a different screen so gotoScreen executes
		startScreen := screenDashboard
		if screen(idx) == screenDashboard {
			startScreen = screenActivity
		}
		m.screen = startScreen

		out := m.View()
		lines := strings.Split(out, "\n")
		chip := stripANSI(m.renderTabChip(idx, false))

		tabStartY := lipgloss.Height(renderBanner(m.width))
		tabY := -1
		tabX := -1
		for y := tabStartY; y < len(lines); y++ {
			clean := stripANSI(lines[y])
			if cIdx := strings.Index(clean, chip); cIdx != -1 {
				tabY = y
				tabX = cIdx + len(chip)/2
				break
			}
		}
		if tabY == -1 {
			t.Fatalf("tab %d (%s, chip %q) not found in View() lines at 100 cols:\n%s", idx, m.labelFor(idx), chip, out)
		}

		r := rects[idx]
		if r.row >= 1 {
			hasSecondRow = true
		}

		res, _ := m.Update(tea.MouseMsg{
			Button: tea.MouseButtonLeft,
			Action: tea.MouseActionPress,
			X:      tabX,
			Y:      tabY,
		})
		m2, ok := res.(model)
		if !ok {
			t.Fatalf("Update returned %T, want model", res)
		}
		if m2.screen != screen(idx) {
			t.Errorf("tab %d (%s) at (%d, %d) switched to %v, want %v",
				idx, screenNames[idx], tabX, tabY, m2.screen, screen(idx))
		}
	}

	if !hasSecondRow {
		t.Fatal("expected at least one tab on row 1 or higher at 100 cols")
	}
}

func TestFirstContentRowMatchesChromeTop(t *testing.T) {
	sizes := []struct {
		w, h int
	}{
		{150, 40},
		{100, 30},
	}

	for _, sz := range sizes {
		// 1. Settings
		{
			m := populatedDashboard(t)
			u, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
			m = u.(model)
			m.gotoScreen(screenSettings)
			out := m.View()
			lines := strings.Split(out, "\n")

			firstLineIdx := -1
			for y, line := range lines {
				if strings.Contains(stripANSI(line), "Settings & Access Configuration") {
					firstLineIdx = y
					break
				}
			}
			if firstLineIdx != m.settings.chromeTop {
				t.Errorf("Settings at %dx%d: first content row = %d, want chromeTop %d",
					sz.w, sz.h, firstLineIdx, m.settings.chromeTop)
			}
		}

		// 2. Skills
		{
			m := populatedDashboard(t)
			m.skills.stats = &audit.Stats{}
			u, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
			m = u.(model)
			m.gotoScreen(screenSkills)
			out := m.View()
			lines := strings.Split(out, "\n")

			firstLineIdx := -1
			for y, line := range lines {
				clean := stripANSI(line)
				if strings.HasPrefix(strings.TrimSpace(clean), "╭") {
					firstLineIdx = y
					break
				}
			}
			if firstLineIdx != m.skills.chromeTop {
				t.Errorf("Skills at %dx%d: first content row = %d, want chromeTop %d",
					sz.w, sz.h, firstLineIdx, m.skills.chromeTop)
			}
		}

		// 3. Dashboard
		{
			m := populatedDashboard(t)
			u, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
			m = u.(model)
			m.gotoScreen(screenDashboard)
			out := m.View()
			lines := strings.Split(out, "\n")

			firstLineIdx := -1
			for y, line := range lines {
				if strings.Contains(stripANSI(line), "Auxly-Memory CLI Dashboard") {
					firstLineIdx = y
					break
				}
			}
			expectedTop := m.dashboard.chromeTop
			if m.dashboard.bodyCompact() {
				expectedTop = m.dashboard.chromeTopTight
			}
			if firstLineIdx != expectedTop {
				t.Errorf("Dashboard at %dx%d (compact=%v): first content row = %d, want expectedTop %d",
					sz.w, sz.h, m.dashboard.bodyCompact(), firstLineIdx, expectedTop)
			}
		}
	}
}

func TestResponsivePopupAndCardMouseClick(t *testing.T) {
	sizes := [][2]int{{160, 60}, {145, 40}, {131, 32}, {120, 32}, {100, 30}}

	// (a) open the popup and click each popup tab where it is drawn; activeAgentTab must change
	// accordingly and the popup must stay open.
	t.Run("PopupTabs", func(t *testing.T) {
		for _, sz := range sizes {
			m := populatedDashboard(t)
			u, _ := m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			m = u.(model)
			m.screen = screenDashboard

			u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = u.(model)
			if m.dashboard.selectedAgent == "" {
				t.Fatalf("%dx%d: popup did not open on Enter", sz[0], sz[1])
			}

			for _, tabIdx := range []int{1, 2, 3, 0} {
				lines := strings.Split(m.View(), "\n")
				tabRow, tabCol := -1, -1
				marker := fmt.Sprintf("[%d ", tabIdx+1)
				for y, l := range lines {
					clean := stripANSI(l)
					if strings.Contains(clean, "[1 ") && strings.Contains(clean, "[2 ") {
						if cIdx := strings.Index(clean, marker); cIdx >= 0 {
							tabRow = y
							tabCol = lipgloss.Width(clean[:cIdx]) + 2
							break
						}
					}
				}
				if tabRow < 0 {
					t.Fatalf("%dx%d: popup tab %d (%q) not found in View()", sz[0], sz[1], tabIdx, marker)
				}

				u, _ = m.Update(tea.MouseMsg{
					Button: tea.MouseButtonLeft,
					Action: tea.MouseActionPress,
					X:      tabCol,
					Y:      tabRow,
				})
				m = u.(model)
				if m.dashboard.selectedAgent == "" {
					t.Fatalf("%dx%d: popup closed unexpectedly after clicking tab %d at (%d, %d)",
						sz[0], sz[1], tabIdx, tabCol, tabRow)
				}
				if m.dashboard.activeAgentTab != tabIdx {
					t.Errorf("%dx%d: clicked tab %d at (%d, %d), got activeAgentTab=%d",
						sz[0], sz[1], tabIdx, tabCol, tabRow, m.dashboard.activeAgentTab)
				}
			}
		}
	})

	// (b) with the popup closed, click the middle of an agent card's name where it is drawn and check
	// it is selected/opened as in 1.6.0.
	t.Run("CardClick", func(t *testing.T) {
		for _, sz := range sizes {
			m := populatedDashboard(t)
			u, _ := m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			m = u.(model)
			m.screen = screenDashboard
			m.dashboard.selectedAgent = ""

			lines := strings.Split(m.View(), "\n")
			var targetCard agentCard
			targetY, targetX := -1, -1
			for _, card := range m.dashboard.cards {
				for y, l := range lines {
					clean := stripANSI(l)
					if cIdx := strings.Index(clean, card.name); cIdx >= 0 {
						end := cIdx + len(card.name)
						if end < len(clean) && clean[end] != ' ' && !strings.HasPrefix(clean[end:], "│") {
							continue
						}
						targetCard = card
						targetY = y
						targetX = lipgloss.Width(clean[:cIdx]) + lipgloss.Width(card.name)/2
						break
					}
				}
				if targetY >= 0 {
					break
				}
			}
			if targetY < 0 {
				// If no card is drawn without scrolling (e.g. 100x30 stacked layout before
				// PgDn), it cannot be clicked where drawn; covered by (c) below after scrolling.
				continue
			}

			u, _ = m.Update(tea.MouseMsg{
				Button: tea.MouseButtonLeft,
				Action: tea.MouseActionPress,
				X:      targetX,
				Y:      targetY,
			})
			m = u.(model)
			if m.dashboard.selectedAgent != targetCard.id {
				t.Errorf("%dx%d: clicked card %q at (%d, %d), got selectedAgent=%q, want %q",
					sz[0], sz[1], targetCard.name, targetX, targetY, m.dashboard.selectedAgent, targetCard.id)
			}
		}
	})

	// (c) in viewport mode (131x32 and 100x30), press PgDn first, then repeat (b) on a card that is
	// visible after scrolling.
	t.Run("CardClickScrolled", func(t *testing.T) {
		for _, sz := range [][2]int{{131, 32}, {100, 30}} {
			m := populatedDashboard(t)
			// At 131x32 the default 12 cards fit without scroll (guarded by
			// TestDashboardFitsShortTerminals); add extra cards so 131x32 enters
			// viewport mode for this test.
			if sz[0] == 131 && sz[1] == 32 {
				for i := 12; i < 24; i++ {
					m.dashboard.cards = append(m.dashboard.cards, agentCard{
						id:   fmt.Sprintf("a%d", i),
						name: fmt.Sprintf("Agent %d", i),
					})
				}
			}

			u, _ := m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			m = u.(model)
			m.screen = screenDashboard
			m.dashboard.selectedAgent = ""

			if !m.usesViewport() {
				t.Fatalf("%dx%d: expected dashboard to use viewport", sz[0], sz[1])
			}

			u, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
			m = u.(model)
			if m.contentVP.YOffset == 0 {
				t.Fatalf("%dx%d: expected YOffset > 0 after PgDn", sz[0], sz[1])
			}

			lines := strings.Split(m.View(), "\n")
			var targetCard agentCard
			targetY, targetX := -1, -1
			for _, card := range m.dashboard.cards {
				for y, l := range lines {
					clean := stripANSI(l)
					if cIdx := strings.Index(clean, card.name); cIdx >= 0 {
						end := cIdx + len(card.name)
						if end < len(clean) && clean[end] != ' ' && !strings.HasPrefix(clean[end:], "│") {
							continue
						}
						targetCard = card
						targetY = y
						targetX = lipgloss.Width(clean[:cIdx]) + lipgloss.Width(card.name)/2
						break
					}
				}
				if targetY >= 0 {
					break
				}
			}
			if targetY < 0 {
				t.Fatalf("%dx%d after PgDn: no agent card name found in View()", sz[0], sz[1])
			}

			u, _ = m.Update(tea.MouseMsg{
				Button: tea.MouseButtonLeft,
				Action: tea.MouseActionPress,
				X:      targetX,
				Y:      targetY,
			})
			m = u.(model)
			if m.dashboard.selectedAgent != targetCard.id {
				t.Errorf("%dx%d after PgDn: clicked card %q at (%d, %d), got selectedAgent=%q, want %q",
					sz[0], sz[1], targetCard.name, targetX, targetY, m.dashboard.selectedAgent, targetCard.id)
			}
		}
	})
}
