package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Column widths for the stale-fact table. Fixed (not window-width-responsive),
// matching the Approvals tab's own fixed-width row (diff.go's "%-40s") — no
// other list tab in this package tracks terminal width either.
const (
	reviewFileW = 18
	reviewTextW = 46
	// reviewAgeW fits humanizeAgo's "NNNd ago" form (wider than the old bespoke
	// "NNNd") without truncating typical multi-year ages.
	reviewAgeW = 9
)

// reviewModel is the "Review" tab: a triage list of stale facts (old and not
// recently recalled), surfaced by internal/memory.Store.StaleFacts. Mirrors
// the Approvals tab's shape (diffModel): a flat list + cursor + one status
// line, refreshed on tab-enter rather than on the dashboard's 1s tick since
// the scan walks the whole vault plus one audit-DB query per file.
type reviewModel struct {
	store    *memory.Store
	logger   *audit.Logger
	facts    []memory.StaleFact
	cursor   int
	scanning bool
	status   string // last action outcome, or an error
	err      string // scan-level error (vault unreadable etc.)
}

func newReviewModel(store *memory.Store, logger *audit.Logger) reviewModel {
	return reviewModel{store: store, logger: logger}
}

// reviewFactsMsg carries the result of an async StaleFacts scan.
type reviewFactsMsg struct {
	facts []memory.StaleFact
	err   error
}

// reviewActionMsg carries the result of an async restamp/archive on one fact.
type reviewActionMsg struct {
	kind string // "restamp" | "archive"
	file string
	line string
	err  error
}

// scanCmd runs the (potentially slow) vault-wide stale scan off the UI thread.
func (m reviewModel) scanCmd() tea.Cmd {
	store := m.store
	logger := m.logger
	return func() tea.Msg {
		if store == nil {
			return reviewFactsMsg{}
		}
		var lastRecall func(string) (map[string]time.Time, error)
		if logger != nil {
			lastRecall = logger.LastRecallByLine
		}
		facts, err := store.StaleFacts(lastRecall, false)
		return reviewFactsMsg{facts: facts, err: err}
	}
}

// Refresh fires the scan. Called by app.go's refreshCurrentScreen on tab-enter
// only — never from a background tick — per the memory API's cost contract.
func (m reviewModel) Refresh() tea.Cmd {
	return m.scanCmd()
}

// restampCmd/archiveCmd run the file-I/O action (under the vault lock) off
// the UI thread, per the BubbleTea no-blocking-I/O-in-Update rule. On success
// they best-effort log an audit entry — review actions are human decisions
// that mutate the vault and belong in the same trail as any other write.
func restampCmd(store *memory.Store, logger *audit.Logger, file, line string) tea.Cmd {
	return func() tea.Msg {
		err := store.RestampFact(file, line)
		if err == nil && logger != nil {
			logger.Log("human", "dashboard", "review_keep", file, "", line, "auto")
		}
		return reviewActionMsg{kind: "restamp", file: file, line: line, err: err}
	}
}

func archiveCmd(store *memory.Store, logger *audit.Logger, file, line string) tea.Cmd {
	return func() tea.Msg {
		err := store.ArchiveFact(file, line)
		if err == nil && logger != nil {
			logger.Log("human", "dashboard", "review_archive", file, "", line, "auto")
		}
		return reviewActionMsg{kind: "archive", file: file, line: line, err: err}
	}
}

func (m reviewModel) Update(msg tea.Msg) (reviewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case reviewFactsMsg:
		m.scanning = false
		m.facts = msg.facts
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.err = ""
		}
		// A fresh scan replaces the whole list — any "archived"/"re-stamped"
		// banner from a previous action no longer describes what's on screen.
		m.status = ""
		m.clampCursor()
		return m, nil

	case reviewActionMsg:
		if msg.err != nil {
			m.status = msg.err.Error() + " — vault may have changed, try r to rescan"
			return m, nil
		}
		m.facts = removeStaleFact(m.facts, msg.file, msg.line)
		m.clampCursor()
		if msg.kind == "restamp" {
			m.status = "re-stamped"
		} else {
			m.status = "archived → .archive/ (never deleted)"
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		// "k"/up match the vim convention used by every other list in this app
		// (down=j, up=k) — a mis-key must never silently rewrite a fact, so the
		// destructive keep/re-stamp action lives on uppercase "K" instead.
		case "down", "j":
			if m.cursor < len(m.facts)-1 {
				m.cursor++
			}
			m.status = ""
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.status = ""
		case "K":
			if m.cursor >= 0 && m.cursor < len(m.facts) {
				f := m.facts[m.cursor]
				return m, restampCmd(m.store, m.logger, f.File, f.Line)
			}
		case "a":
			if m.cursor >= 0 && m.cursor < len(m.facts) {
				f := m.facts[m.cursor]
				return m, archiveCmd(m.store, m.logger, f.File, f.Line)
			}
		case "r":
			m.scanning = true
			m.status = ""
			m.err = ""
			return m, m.scanCmd()
		case "e":
			m.status = "per-fact editing lands with the Memory tab — for now: auxly pending flow or edit the file directly"
		}
	}
	return m, nil
}

func (m *reviewModel) clampCursor() {
	if m.cursor >= len(m.facts) {
		m.cursor = len(m.facts) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// removeStaleFact drops the first fact matching file+line — the pure
// row-removal transition used after a successful restamp/archive so the row
// disappears without a full rescan.
func removeStaleFact(facts []memory.StaleFact, file, line string) []memory.StaleFact {
	out := make([]memory.StaleFact, 0, len(facts))
	removed := false
	for _, f := range facts {
		if !removed && f.File == file && f.Line == line {
			removed = true
			continue
		}
		out = append(out, f)
	}
	return out
}

// reviewAge renders a fact's age from its dated stamp (or "undated" when the
// line carries no [YYYY-MM-DD]), reusing humanizeAgo so its vocabulary
// ("Xd ago") matches the Last-Recall column instead of a bespoke "142d".
func reviewAge(t time.Time) string {
	if t.IsZero() {
		return "undated"
	}
	return humanizeAgo(time.Since(t))
}

// reviewLastRecall renders "never" or a relative time, reusing the dashboard's
// own humanizeAgo so the vocabulary ("Xm/h/d ago") matches the rest of the app.
func reviewLastRecall(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return humanizeAgo(time.Since(t))
}

// reviewRow renders one fixed-width table row: FILE | fact text | AGE | LAST RECALL.
func reviewRow(f memory.StaleFact, textW int) string {
	file := fmt.Sprintf("%-*s", reviewFileW, truncate(f.File, reviewFileW))
	text := strings.TrimPrefix(strings.TrimPrefix(f.Line, "- "), "* ")
	text = fmt.Sprintf("%-*s", textW, truncate(text, textW))
	age := fmt.Sprintf("%-*s", reviewAgeW, reviewAge(f.FactDate))
	last := reviewLastRecall(f.LastRecall)
	return fmt.Sprintf("%s %s %s %s", file, text, age, last)
}

func reviewHeaderRow() string {
	return fmt.Sprintf("%-*s %-*s %-*s %s",
		reviewFileW, "FILE", reviewTextW, "FACT", reviewAgeW, "AGE", "LAST RECALL")
}

func (m reviewModel) View() string {
	title := StyleTitle.Render("🔍 Stale Fact Review")

	if m.scanning {
		return title + "\n\n" + lipgloss.NewStyle().Foreground(ColorDim).Render("scanning…")
	}
	if m.err != "" {
		return title + "\n\n" + lipgloss.NewStyle().Foreground(ColorDanger).Render("⚠ "+m.err)
	}
	if len(m.facts) == 0 {
		// Exact match with cmd/review.go's empty-state line — one message,
		// whichever surface the user is on.
		return title + "\n\n✅ Nothing stale — every fact is either fresh or recently recalled."
	}

	dim := lipgloss.NewStyle().Foreground(ColorDim)

	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(dim.Render(reviewHeaderRow()) + "\n")
	for i, f := range m.facts {
		row := reviewRow(f, reviewTextW)
		if i == m.cursor {
			b.WriteString(StyleSelectedRow.Render("▸ "+row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}
	if m.status != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorWarning).Render(m.status) + "\n")
	}
	return b.String()
}
