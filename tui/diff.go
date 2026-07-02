package tui

import (
	"errors"
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// diffModel is the Approvals tab: a flat pending queue + cursor + one status
// line (diffModel's original shape), extended this sprint with a colorized
// diff pane, a per-row TTL countdown, and 'A'/'F' batch-approve keys.
type diffModel struct {
	mgr     *pending.Manager
	logger  *audit.Logger // best-effort pending_approve/pending_reject audit trail — see logPendingDecision
	files   []pending.PendingFile
	infos   []pending.Info // parallel to files: Agent/Created/Additions/Deletions, for the TTL badge and batch keys
	cursor  int
	viewing string
	status  string // last approve/reject/batch outcome (e.g. a conflict) shown under the list

	// Batch-approve confirmation ('A' by agent, 'F' by file) — same y/n gate
	// shape as the Memory tab's confirmDelete. batchLabel is the agent id or
	// file path shown in the confirm prompt.
	batchKind  string // "" | "agent" | "file"
	batchLabel string
	batchNames []string
}

type diffRefreshMsg struct {
	files []pending.PendingFile
	infos []pending.Info
}

// diffBatchMsg carries the result of a batch approve, run off the UI thread
// via batchApproveCmd.
type diffBatchMsg struct {
	applied    int
	conflicted int
	failed     int // any error other than ErrConflict (e.g. concurrent removal) — counted, never swallowed
}

func newDiffModel(mgr *pending.Manager, logger *audit.Logger) diffModel {
	return diffModel{mgr: mgr, logger: logger}
}

// logPendingDecision best-effort logs a human approve/reject decision so
// TUI-driven approvals accumulate the same trust evidence as the CLI
// (Sprint 16's ApprovalStats reads exactly these pending_approve/pending_reject
// rows). Mirrors cmd/approve.go's and cmd/reject.go's call byte-for-byte: the
// raw agent id (capture:/organize- prefix intact, "unknown" only when truly
// unattributed) as both agentID and provider, target as the file, no diff, a
// fixed human-readable reason, and "pending" as the trust level (a queue
// decision, not a trust level itself).
func logPendingDecision(logger *audit.Logger, info pending.Info, action, reason string) {
	if logger == nil {
		return
	}
	agent := info.Agent
	if agent == "" {
		agent = "unknown"
	}
	logger.Log(agent, agent, action, info.Target, "", reason, "pending")
}

func (m diffModel) Refresh() tea.Cmd {
	mgr := m.mgr
	return func() tea.Msg {
		files, _ := mgr.List()
		infos := make([]pending.Info, len(files))
		for i, f := range files {
			infos[i], _ = mgr.Info(f.Name)
		}
		return diffRefreshMsg{files: files, infos: infos}
	}
}

// batchApproveCmd approves every named pending entry off the UI thread.
// Conflicted entries (ErrConflict — the target changed since the pending was
// created) are skipped and counted, never force-applied — mirrors
// cmd/approve.go's bulk approve, which always requires reviewing a conflict
// alone with --force. Any other error (e.g. the pending was removed
// concurrently) is counted as failed rather than silently dropped, so the
// three counters always reconcile against len(names). Every successful
// approve is best-effort audit-logged the same way the single 'a' key is.
func batchApproveCmd(mgr *pending.Manager, logger *audit.Logger, names []string) tea.Cmd {
	return func() tea.Msg {
		applied, conflicted, failed := 0, 0, 0
		for _, name := range names {
			info, infoErr := mgr.Info(name)
			switch err := mgr.Approve(name); {
			case err == nil:
				applied++
				if infoErr == nil {
					logPendingDecision(logger, info, "pending_approve", "human approved")
				}
			case errors.Is(err, pending.ErrConflict):
				conflicted++
			default:
				failed++
			}
		}
		return diffBatchMsg{applied: applied, conflicted: conflicted, failed: failed}
	}
}

func (m diffModel) Update(msg tea.Msg) (diffModel, tea.Cmd) {
	switch msg := msg.(type) {
	case diffRefreshMsg:
		m.files = msg.files
		m.infos = msg.infos
		m.viewing = ""
		if m.cursor >= len(m.files) {
			m.cursor = len(m.files) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
	case diffBatchMsg:
		// Three counters that always reconcile against the batch size — no error
		// class is silently dropped. Zero parts are omitted so the common case
		// ("applied N") stays terse.
		parts := []string{fmt.Sprintf("applied %d", msg.applied)}
		if msg.conflicted > 0 {
			parts = append(parts, fmt.Sprintf("%d conflicts skipped", msg.conflicted))
		}
		if msg.failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", msg.failed))
		}
		m.status = strings.Join(parts, ", ")
		return m, m.Refresh()
	case tea.KeyMsg:
		// A batch confirm owns the keyboard (y/n/esc) until answered — same
		// early-return shape as the Memory tab's confirmDelete gate.
		if m.batchKind != "" {
			switch msg.String() {
			case "y":
				names := m.batchNames
				m.batchKind = ""
				m.batchLabel = ""
				m.batchNames = nil
				return m, batchApproveCmd(m.mgr, m.logger, names)
			case "n", "esc":
				m.batchKind = ""
				m.batchLabel = ""
				m.batchNames = nil
				m.status = ""
			}
			return m, nil
		}

		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.files)-1 {
				m.cursor++
			}
			m.status = "" // status describes the item it happened on — don't let it stick to another
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			m.status = ""
		case "enter":
			if m.cursor < len(m.files) {
				content, _ := m.mgr.ViewDiff(m.files[m.cursor].Name)
				m.viewing = content
			}
		case "a":
			if m.cursor < len(m.files) {
				name := m.files[m.cursor].Name
				// Conflicts (target edited since the pending was created) must be
				// visible, not silently swallowed — the item stays queued.
				if err := m.mgr.Approve(name); err != nil {
					m.status = err.Error()
				} else {
					m.status = ""
					if m.cursor < len(m.infos) {
						logPendingDecision(m.logger, m.infos[m.cursor], "pending_approve", "human approved")
					}
				}
				return m, m.Refresh()
			}
		case "r":
			if m.cursor < len(m.files) {
				name := m.files[m.cursor].Name
				if err := m.mgr.Reject(name); err != nil {
					m.status = err.Error()
				} else {
					m.status = ""
					if m.cursor < len(m.infos) {
						logPendingDecision(m.logger, m.infos[m.cursor], "pending_reject", "human rejected")
					}
				}
				return m, m.Refresh()
			}
		case "A":
			// List view only: while an individual diff is open (m.viewing != ""),
			// the confirm prompt this arms would render on a branch the user can't
			// see — a blind 'y' would then batch-approve entries never reviewed.
			if m.viewing == "" && m.cursor < len(m.infos) {
				agent := m.infos[m.cursor].Agent
				if names := namesForAgent(m.infos, agent); len(names) > 0 {
					label := agent
					if label == "" {
						label = "unknown"
					}
					m.batchKind = "agent"
					m.batchLabel = label
					m.batchNames = names
					m.status = fmt.Sprintf("approve %d pending from %s? y/n", len(names), label)
				}
			}
		case "F":
			// Same gate as 'A' — see above.
			if m.viewing == "" && m.cursor < len(m.infos) {
				file := m.infos[m.cursor].Target
				if names := namesForFile(m.infos, file); len(names) > 0 {
					m.batchKind = "file"
					m.batchLabel = file
					m.batchNames = names
					m.status = fmt.Sprintf("approve %d pending for %s? y/n", len(names), file)
				}
			}
		case "esc":
			m.viewing = ""
		}
	}
	return m, nil
}

func (m diffModel) View() string {
	title := StyleTitle.Render("📋 Approval Queue")

	if m.viewing != "" {
		diffStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(1, 2)
		return fmt.Sprintf("%s\n\n%s",
			title,
			diffStyle.Render(formatApprovalDiff(m.viewing)),
		)
	}

	if len(m.files) == 0 {
		return title + "\n\n✅ No pending approvals."
	}

	ttl := pending.PendingTTL()
	var content string
	for i, f := range m.files {
		cursor := "  "
		if i == m.cursor {
			cursor = lipgloss.NewStyle().Foreground(ColorPrimary).Render("▸ ")
		}

		badge := ""
		if i < len(m.infos) {
			badge = ttlBadge(m.infos[i].Created, ttl)
		}

		line := fmt.Sprintf("%s%-40s %-16s %s", cursor, f.Name, f.ModTime.Format("2006-01-02 15:04"), badge)
		if i == m.cursor {
			line = StyleSelectedRow.Render(line)
		}
		content += line + "\n"
	}

	if m.status != "" {
		content += "\n" + lipgloss.NewStyle().Foreground(ColorWarning).Render("⚠ "+m.status) + "\n"
	}

	return fmt.Sprintf("%s\n\n%s", title, content)
}

// formatApprovalDiff colorizes a pending entry's raw body for the Approval
// Queue's diff pane: the frontmatter block (from the opening '---' up to and
// including the closing '---' — see pending.Manager.ViewDiff/extractField)
// dim throughout, since its own "---" delimiter lines would otherwise fall
// into the '-' deletion bucket below and paint red. After the frontmatter:
// '+' lines green, '-' lines red, '#' comment lines (the
// organize-contradictions verdict/reason — see cmd/organize.go) dim, and
// everything else (unchanged context) left plain/normal. A unified
// single-column view, not side-by-side — the smallest coloring that reads
// clearly at pending-review sizes.
func formatApprovalDiff(content string) string {
	add := lipgloss.NewStyle().Foreground(ColorSuccess)
	del := lipgloss.NewStyle().Foreground(ColorDanger)
	comment := lipgloss.NewStyle().Foreground(ColorDim)
	frontmatter := lipgloss.NewStyle().Foreground(ColorDim)

	lines := strings.Split(content, "\n")
	inFrontmatter, dashes := true, 0
	for i, line := range lines {
		if inFrontmatter {
			if strings.TrimSpace(line) == "---" {
				dashes++
				lines[i] = frontmatter.Render(line)
				if dashes >= 2 {
					inFrontmatter = false
				}
				continue
			}
			if dashes >= 1 {
				lines[i] = frontmatter.Render(line)
				continue
			}
			// No opening '---' at all (malformed/legacy content, or already past
			// it) — nothing to skip, fall through to normal coloring below.
			inFrontmatter = false
		}
		switch {
		case strings.HasPrefix(line, "+"):
			lines[i] = add.Render(line)
		case strings.HasPrefix(line, "-"):
			lines[i] = del.Render(line)
		case strings.HasPrefix(line, "#"):
			lines[i] = comment.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// ttlBadge renders "archives in Nd" from a pending entry's Created time and
// the sweep TTL window (pending.PendingTTL — the same window SweepExpired
// archives against): "" when TTL is disabled (0, AUXLY_PENDING_TTL_DAYS=0) or
// Created is unknown (legacy entry). Rounds the remaining time UP so an entry
// created minutes ago still reads the full "Nd", and clamps a past-due entry
// (not yet swept) to 0 rather than a negative count.
func ttlBadge(created time.Time, ttl time.Duration) string {
	if ttl <= 0 || created.IsZero() {
		return ""
	}
	remaining := ttl - time.Since(created)
	if remaining < 0 {
		remaining = 0
	}
	days := int(math.Ceil(remaining.Hours() / 24))
	color := ColorDim
	if days <= 3 {
		color = ColorWarning
	}
	return lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("archives in %dd", days))
}

// namesForAgent returns the pending names attributed to agent, mirroring
// cmd/approve.go's selectPending: a bare agent id also matches its
// "capture:"-prefixed auto-captured entries, so one batch key covers a
// provider's whole queue. Legacy entries with no attribution ("") group
// together under the "unknown" batch.
func namesForAgent(infos []pending.Info, agent string) []string {
	var names []string
	for _, info := range infos {
		if info.Agent == agent || (agent != "" && info.Agent == "capture:"+agent) {
			names = append(names, info.Name)
		}
	}
	return names
}

// namesForFile returns the pending names targeting file, path-normalized the
// same way cmd/approve.go's selectPending compares targets (so "a.md" and
// "./a.md" match).
func namesForFile(infos []pending.Info, file string) []string {
	target := normPendingTarget(file)
	var names []string
	for _, info := range infos {
		if normPendingTarget(info.Target) == target {
			names = append(names, info.Name)
		}
	}
	return names
}

// normPendingTarget mirrors cmd/approve.go's normTarget. Duplicated rather
// than imported: cmd depends on tui (cmd/tui.go calls tui.Run), so the
// reverse import would cycle.
func normPendingTarget(t string) string {
	return path.Clean(strings.ReplaceAll(strings.TrimSpace(t), "\\", "/"))
}
