package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
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
		{Provider: "claude-code", Remote: true, Host: "node-a", IP: "10.0.0.147", OS: "linux"},
		{Provider: "claude-code", Remote: true, Host: "host.example.net", IP: "10.0.0.141", OS: "linux"},
		{Provider: "claude-code", Remote: true, Host: "erp.host.example.net", IP: "10.0.0.8", OS: "linux"},
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
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
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
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
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

// TestAgentGridColumnsScaleWithWidth locks the dynamic grid AND the hard 3-column
// cap: however wide the terminal, the grid never exceeds 3 columns (wider cards
// instead), so the status line has room and never wraps to a third row.
func TestAgentGridColumnsScaleWithWidth(t *testing.T) {
	// The cap holds at any width — even an enormous terminal stays at 3 columns.
	for _, w := range []int{200, 240, 400} {
		if c, _ := agentGridLayout(w, 12, false); c != 3 {
			t.Errorf("%d-wide should be capped at exactly 3 columns, got %d", w, c)
		}
		if c, _ := agentGridLayout(w, 12, true); c != 3 {
			t.Errorf("%d-wide compact should be capped at exactly 3 columns, got %d", w, c)
		}
	}
	// Compact packs at least as many columns as non-compact at the same width.
	cNormal, _ := agentGridLayout(131, 12, false)
	cCompact, _ := agentGridLayout(131, 12, true)
	if cCompact < cNormal {
		t.Errorf("compact should not have fewer columns (normal=%d compact=%d)", cNormal, cCompact)
	}
	// Never more columns than cards (the cap must not invent columns).
	if c, _ := agentGridLayout(240, 2, false); c > 2 {
		t.Errorf("2 cards must not exceed 2 columns, got %d", c)
	}
}

// TestAgentCardNeverWrapsToThreeLines is the reported bug: an active card with the
// widest trust badge ("read-only") must still render as exactly two content lines
// (4 rows incl. border) at every terminal size — the status line degrades (drops
// ⇄N, then the badge) rather than wrapping a trust badge onto a third row.
func TestAgentCardNeverWrapsToThreeLines(t *testing.T) {
	m := populatedDashboard(t)
	// Real brand names (the longest, e.g. "Android Studio") + the widest trust badge
	// + every card active = the worst case for the status line.
	var cards []agentCard
	for _, id := range brandOrder {
		cards = append(cards, brandMeta[id])
	}
	m.dashboard.cards = cards
	m.dashboard.trustCfg = &trust.Config{Default: trust.LevelReadOnly, Providers: map[string]trust.ProviderConfig{}}
	m.dashboard.sessions = nil
	for _, c := range cards {
		m.dashboard.sessions = append(m.dashboard.sessions, agentSession{Provider: c.id})
	}

	for _, sz := range []struct{ w, h int }{{175, 38}, {140, 32}, {124, 24}, {90, 40}} {
		u, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		mm := u.(model)
		mm.dashboard.cards = cards
		_, cardW := agentGridLayout(sz.w, len(cards), mm.dashboard.bodyCompact())
		for i, c := range cards {
			card := mm.dashboard.renderAgentCard(c, i, cardW)
			if h := lipgloss.Height(card); h != 4 {
				t.Errorf("%dx%d card %q (w=%d): height %d, want 4 — it wrapped to a 3rd line:\n%s",
					sz.w, sz.h, c.name, cardW, h, card)
			}
		}
	}
}

// TestConnectionsSummaryDedupsSameServer locks the dedup: three live sessions from
// the same remote box collapse to one row with ×3, while a distinct host shows once
// with no count.
func TestConnectionsSummaryDedupsSameServer(t *testing.T) {
	m := *NewApp(t.TempDir())
	m.dashboard.sessions = []agentSession{
		{Provider: "claude-code", Remote: true, Host: "testhost.local", IP: "10.0.0.6", OS: "linux", PID: 1},
		{Provider: "claude-code", Remote: true, Host: "testhost.local", IP: "10.0.0.6", OS: "linux", PID: 2},
		{Provider: "claude-code", Remote: true, Host: "testhost.local", IP: "10.0.0.6", OS: "linux", PID: 3},
		{Provider: "claude-code", Remote: true, Host: "erp.host.example.net", IP: "10.0.0.8", OS: "linux", PID: 4},
	}
	out := stripANSI(m.dashboard.renderConnectionsSummary(false))
	if c := strings.Count(out, "testhost.local"); c != 1 {
		t.Errorf("testhost.local should appear once (deduped), got %d:\n%s", c, out)
	}
	if c := strings.Count(out, "erp.host.example.net"); c != 1 {
		t.Errorf("erp.host.example.net should appear once, got %d:\n%s", c, out)
	}
	if !strings.Contains(out, "×3") {
		t.Errorf("the tripled host must show a ×3 count:\n%s", out)
	}
	// Only the tripled host carries a count; the singleton has none.
	if c := strings.Count(out, "×"); c != 1 {
		t.Errorf("exactly one ×N count expected, got %d:\n%s", c, out)
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
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}}) // Remote tab
	m = u.(model)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}) // connect wizard
	m = u.(model)
	if !m.ssh.editingHost || m.ssh.formStep != formStepMethod {
		t.Fatalf("'c' must open the method step (editingHost=%v step=%q)", m.ssh.editingHost, m.ssh.formStep)
	}
	before := stripANSI(m.contentVP.View())
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}) // pick "relay" (now first)
	m = u.(model)
	if m.ssh.formMethod != "relay" {
		t.Fatalf("digit '1' must select relay (got %q)", m.ssh.formMethod)
	}
	after := stripANSI(m.contentVP.View())
	if after == before {
		t.Error("viewport did NOT repaint after a wizard keypress — stale frame (the reported bug)")
	}
}

// TestDashboardRichSectionsFullMode verifies the new informative sections (memory
// composition, recent-changes feed, last-write freshness) render on a tall terminal
// and that the whole thing still fits.
func TestDashboardRichSectionsFullMode(t *testing.T) {
	m := populatedDashboard(t)
	m.dashboard.stats.LastWriteTime = time.Now().Add(-4 * time.Minute).UTC().Format(time.RFC3339)
	m.dashboard.composition = []categoryStat{
		{label: "identity", items: 3, size: 200},
		{label: "infra", items: 12, size: 900},
		{label: "projects", items: 8, size: 600},
	}
	m.dashboard.recentWrites = []audit.Entry{
		{Timestamp: m.dashboard.stats.LastWriteTime, Provider: "codex", AgentID: "auxly-organize", Action: "write", File: "business.md", Diff: "+ a\n+ b\n- c\n"},
		{Timestamp: m.dashboard.stats.LastWriteTime, Provider: "claude", Action: "write", File: "infra.md", Diff: "+ x\n"},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 60})
	m = updated.(model)
	m.screen = screenDashboard
	out := m.View()

	for _, w := range []string{"Memory by category", "infra", "Recent Memory Changes", "business.md", "Last write:"} {
		if !strings.Contains(out, w) {
			t.Errorf("full-mode dashboard missing %q", w)
		}
	}
	if h := lipgloss.Height(out); h > 60 {
		t.Errorf("rich dashboard height %d exceeds terminal 60", h)
	}
}

// TestDashboardRichSectionsSuppressedWhenCompact locks the responsive contract: on a
// short terminal the rich sections are dropped so the dashboard still fits.
func TestDashboardRichSectionsSuppressedWhenCompact(t *testing.T) {
	m := populatedDashboard(t)
	m.dashboard.stats.LastWriteTime = time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	m.dashboard.composition = []categoryStat{{label: "infra", items: 12, size: 900}}
	m.dashboard.recentWrites = []audit.Entry{
		{Timestamp: m.dashboard.stats.LastWriteTime, Provider: "claude", Action: "write", File: "infra.md", Diff: "+ x\n"},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = updated.(model)
	m.screen = screenDashboard
	out := m.View()

	if lipgloss.Height(out) > 30 {
		t.Errorf("compact dashboard height %d exceeds terminal 30", lipgloss.Height(out))
	}
	if strings.Contains(out, "Recent Memory Changes") || strings.Contains(out, "Memory by category") {
		t.Error("rich sections must be suppressed in compact mode to preserve the fit")
	}
}

// TestDashboardPendingInline verifies queued approvals surface on the dashboard in
// full mode and are suppressed (with the rest) on a short terminal.
func TestDashboardPendingInline(t *testing.T) {
	m := populatedDashboard(t)
	m.dashboard.pendingFiles = []pending.PendingFile{
		{Name: "identity-20260603.md", ModTime: time.Now().Add(-3 * time.Minute)},
		{Name: "infra-20260603.md", ModTime: time.Now().Add(-9 * time.Minute)},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 60})
	m = updated.(model)
	m.screen = screenDashboard
	out := m.View()
	for _, w := range []string{"Pending Approval", "identity-20260603", "review in Approvals"} {
		if !strings.Contains(out, w) {
			t.Errorf("full-mode dashboard missing pending item %q", w)
		}
	}

	short, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = short.(model)
	if strings.Contains(m.View(), "Pending Approval") {
		t.Error("pending block must be suppressed in compact mode")
	}
}

// TestDashboardFeedShowsWhoAndComposition checks the recent feed shows the writing
// agent and that a personal-tier category is flagged private.
func TestDashboardFeedShowsWhoAndComposition(t *testing.T) {
	m := populatedDashboard(t)
	m.dashboard.stats.LastWriteTime = time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
	m.dashboard.composition = []categoryStat{
		{label: "infra", items: 9, size: 700},
		{label: "personal", items: 4, size: 300, private: true},
	}
	m.dashboard.recentWrites = []audit.Entry{
		{Timestamp: m.dashboard.stats.LastWriteTime, Provider: "codex", Action: "write", File: "infra.md", Diff: "+ a\n"},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 60})
	m = updated.(model)
	m.screen = screenDashboard
	out := stripANSI(m.View())
	if !strings.Contains(out, "codex") {
		t.Error("recent feed should show the writing agent (who)")
	}
	if !strings.Contains(out, "🔒") {
		t.Error("personal category should be flagged private with a lock")
	}
}

// TestRemoteScopeShownInConnections verifies the access scope renders next to a
// connected remote box in the connections summary.
func TestRemoteScopeShownInConnections(t *testing.T) {
	m := populatedDashboard(t)
	m.dashboard.remoteScope = map[string]string{"node-a": "read · 6 file(s)"}
	out := stripANSI(m.dashboard.renderConnectionsSummary(false))
	if !strings.Contains(out, "read · 6 file(s)") {
		t.Errorf("remote scope not shown in connections:\n%s", out)
	}
}

// TestDashboardRichFitsTallWideTerminal reproduces the user's 198×53 terminal: with
// the full set of enrichments the dashboard must stay in FULL mode (rich sections
// visible), not fall back to compact. Guards against the enrichments inflating the
// body past common terminal heights.
func TestDashboardRichFitsTallWideTerminal(t *testing.T) {
	m := populatedDashboard(t)
	m.dashboard.stats.LastWriteTime = time.Now().Format(time.RFC3339)
	m.dashboard.composition = []categoryStat{
		{label: "projects", items: 60}, {label: "infra", items: 38}, {label: "daily", items: 33},
		{label: "agents", items: 29}, {label: "preferences", items: 25}, {label: "personal", items: 17, private: true},
		{label: "products", items: 9}, {label: "business", items: 9}, {label: "identity", items: 7},
	}
	m.dashboard.recentWrites = make([]audit.Entry, 8)
	for i := range m.dashboard.recentWrites {
		m.dashboard.recentWrites[i] = audit.Entry{Timestamp: m.dashboard.stats.LastWriteTime, Provider: "claude-code", Action: "write", File: "projects.md", Diff: "+a\n"}
	}
	m.dashboard.remoteScope = map[string]string{
		"node-a": "read/write · 8 file(s)", "host.example.net": "read/write · 8 file(s)", "erp.host.example.net": "read/write · 8 file(s)",
	}
	// The three chart/feed sections added after this test was first written
	// (vault-size sparkline, per-agent write bars, live activity feed) must
	// also be covered here — otherwise the zero-spare-rows contract this test
	// guards is only verified with those sections dark.
	m.dashboard.vaultSizeHistory = []audit.SizePoint{
		{Day: time.Now().AddDate(0, 0, -1).UTC().Format("2006-01-02"), Bytes: 1000},
		{Day: time.Now().UTC().Format("2006-01-02"), Bytes: 2000},
	}
	m.dashboard.agentWriteCounts = []audit.AgentWriteCount{
		{Provider: "claude-code", Count: 40}, {Provider: "codex", Count: 20},
		{Provider: "cursor", Count: 10}, {Provider: "gemini", Count: 4},
	}
	m.dashboard.activityFeed = make([]audit.ActivityEvent, 8)
	for i := range m.dashboard.activityFeed {
		m.dashboard.activityFeed[i] = audit.ActivityEvent{ID: int64(i + 1), TS: time.Now(), Provider: "claude-code", Action: "write", File: "projects.md"}
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 198, Height: 53})
	m = updated.(model)
	m.screen = screenDashboard
	if m.dashboard.bodyCompact() {
		t.Fatalf("198x53 must render the FULL rich dashboard, not compact (full body height=%d)",
			lipgloss.Height(m.dashboard.renderBody(false)))
	}
	out := m.View()
	for _, w := range []string{"Memory by category", "Recent Memory Changes", "🔑"} {
		if !strings.Contains(out, w) {
			t.Errorf("198x53 rich dashboard missing %q", w)
		}
	}
	if h := lipgloss.Height(out); h > 53 {
		t.Errorf("rich dashboard height %d exceeds terminal 53", h)
	}
}

// manyRemotesDashboard reproduces the reported case: a host with MANY connected
// remote boxes, which is what made the left column overflow and tipped the whole
// dashboard into compact (dropping the bars + recent feed).
func manyRemotesDashboard(t *testing.T, n int) model {
	t.Helper()
	m := populatedDashboard(t)
	m.dashboard.stats.LastWriteTime = time.Now().Format(time.RFC3339)
	m.dashboard.composition = []categoryStat{
		{label: "projects", items: 60}, {label: "infra", items: 38}, {label: "daily", items: 33},
		{label: "agents", items: 29}, {label: "preferences", items: 25}, {label: "products", items: 9},
	}
	m.dashboard.recentWrites = make([]audit.Entry, 8)
	for i := range m.dashboard.recentWrites {
		m.dashboard.recentWrites[i] = audit.Entry{Timestamp: m.dashboard.stats.LastWriteTime, Provider: "claude-code", Action: "write", File: "projects.md", Diff: "+a\n"}
	}
	sessions := make([]agentSession, 0, n+1)
	for i := 0; i < n; i++ {
		sessions = append(sessions, agentSession{
			Provider: "claude-code", Remote: true,
			Host: fmt.Sprintf("box-%02d.example.net", i),
			IP:   fmt.Sprintf("10.0.0.%d", i+1), OS: "linux",
		})
	}
	sessions = append(sessions, agentSession{Provider: "claude", Remote: false, PID: 1})
	m.dashboard.sessions = sessions
	return m
}

// TestConnectionsPanelScrollsWhenOverflowing is the core guarantee: a long remote
// list is height-bounded and scrolls IN PLACE — it must NOT push the dashboard into
// compact (the bars + recent feed stay visible), and scrolling reveals later hosts.
func TestConnectionsPanelScrollsWhenOverflowing(t *testing.T) {
	m := manyRemotesDashboard(t, 20)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 56})
	m = updated.(model)
	m.screen = screenDashboard
	d := m.dashboard

	if d.remoteGroupCount() != 20 {
		t.Fatalf("expected 20 remote groups, got %d", d.remoteGroupCount())
	}
	if d.connMaxScroll(false) <= 0 {
		t.Fatalf("20 remotes must overflow (maxScroll>0), got %d", d.connMaxScroll(false))
	}

	// Panel windows the list: the first host shows, a far-down host is below the fold,
	// and the scroll affordance is present.
	top := stripANSI(d.renderConnectionsSummary(false))
	if !strings.Contains(top, "more below — Shift+J") {
		t.Errorf("overflowing connections must show the scroll affordance with its key:\n%s", top)
	}
	if !strings.Contains(top, "box-00.example.net") {
		t.Errorf("first remote should be visible at scroll 0:\n%s", top)
	}
	if strings.Contains(top, "box-19.example.net") {
		t.Errorf("last remote must be BELOW the fold at scroll 0 (windowed):\n%s", top)
	}

	// The panel height is BOUNDED: 20 remotes render no taller than the visible cap
	// allows (2 lines/remote + the ▲/▼ + local line), NOT 20 rows. This is what keeps
	// the left column from tipping the dashboard into compact.
	cap := d.connVisibleCap(false)
	if h := lipgloss.Height(d.renderConnectionsSummary(false)); h > cap*2+4 {
		t.Errorf("connections panel height %d exceeds the bound for cap %d (must scroll, not grow)", h, cap)
	}

	// Bounding the panel keeps the rich layout: NOT compact, bars + feed visible —
	// 20 remotes that previously would have buried them off-screen.
	if d.bodyCompact() {
		t.Fatalf("20 remotes must NOT force compact at 170x56 — the panel scrolls instead (body height=%d)",
			lipgloss.Height(d.renderBody(false)))
	}
	view := stripANSI(m.View())
	for _, w := range []string{"Memory by category", "Recent Memory Changes"} {
		if !strings.Contains(view, w) {
			t.Errorf("rich section %q must stay visible while connections scroll", w)
		}
	}

	// Scrolling to the bottom reveals the last host and hides the first.
	d.connScroll = d.connMaxScroll(false)
	bottom := stripANSI(d.renderConnectionsSummary(false))
	if !strings.Contains(bottom, "box-19.example.net") {
		t.Errorf("scrolled-to-bottom must reveal the last remote:\n%s", bottom)
	}
	if !strings.Contains(bottom, "more above") {
		t.Errorf("scrolled list must show the ▲ above affordance:\n%s", bottom)
	}
}

// TestConnectionsWheelAndKeysScroll verifies the wheel (over the left box) and the
// PgUp/PgDn fallback move connScroll, clamped at both ends.
func TestConnectionsWheelAndKeysScroll(t *testing.T) {
	m := manyRemotesDashboard(t, 9)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 44})
	m = updated.(model)
	m.screen = screenDashboard
	maxOff := m.dashboard.connMaxScroll(m.dashboard.bodyCompact())

	// Wheel down over the left box (X<=47) scrolls; wheel up clamps at 0.
	for i := 0; i < maxOff+3; i++ {
		m.dashboard, _ = m.dashboard.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, X: 10, Y: 12})
	}
	if m.dashboard.connScroll != maxOff {
		t.Errorf("wheel-down must clamp at maxScroll %d, got %d", maxOff, m.dashboard.connScroll)
	}
	// Wheel over the agent grid (right side, X>47) must NOT scroll connections.
	before := m.dashboard.connScroll
	m.dashboard, _ = m.dashboard.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, X: 120, Y: 12})
	if m.dashboard.connScroll != before {
		t.Errorf("wheel over the agent grid must not move connections (%d→%d)", before, m.dashboard.connScroll)
	}
	// PgUp keyboard fallback scrolls back up; clamps at 0.
	for i := 0; i < maxOff+3; i++ {
		m.dashboard, _ = m.dashboard.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
	if m.dashboard.connScroll != 0 {
		t.Errorf("PgUp must clamp at 0, got %d", m.dashboard.connScroll)
	}
	// Ctrl+↓ / Ctrl+↑ alias (for terminals/users who reach for those) scrolls too.
	m.dashboard, _ = m.dashboard.Update(tea.KeyMsg{Type: tea.KeyCtrlDown})
	if m.dashboard.connScroll != 1 {
		t.Errorf("Ctrl+Down must scroll connections down, got %d", m.dashboard.connScroll)
	}
	m.dashboard, _ = m.dashboard.Update(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if m.dashboard.connScroll != 0 {
		t.Errorf("Ctrl+Up must scroll connections up, got %d", m.dashboard.connScroll)
	}

	// Advertised J/K (Shift+j/k): J scrolls down, K scrolls up — universal, no Fn,
	// and distinct from lowercase j/k which drive the agent grid.
	m.dashboard, _ = m.dashboard.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}})
	if m.dashboard.connScroll != 1 {
		t.Errorf("J must scroll connections down, got %d", m.dashboard.connScroll)
	}
	m.dashboard, _ = m.dashboard.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'K'}})
	if m.dashboard.connScroll != 0 {
		t.Errorf("K must scroll connections up, got %d", m.dashboard.connScroll)
	}
	// Lowercase j must NOT scroll connections (it navigates the agent grid).
	before2 := m.dashboard.connScroll
	m.dashboard, _ = m.dashboard.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.dashboard.connScroll != before2 {
		t.Errorf("lowercase j must drive cards, not scroll connections (%d→%d)", before2, m.dashboard.connScroll)
	}
}
