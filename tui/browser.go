package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type browserModel struct {
	store  *memory.Store
	files  []memory.FileInfo
	cursor int
	status string // transient feedback (e.g. the export result path)
}

// browserExportMsg carries the outcome of an "export all" run back into Update.
type browserExportMsg struct {
	dir     string
	count   int
	skipped int // encrypted files Export() left out of the snapshot
	err     error
}

// exportAllCmd writes every memory file to a timestamped folder in ~/Downloads, each
// tagged with its name + the export time. Runs off the UI thread (file I/O).
func exportAllCmd(store *memory.Store) tea.Cmd {
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return browserExportMsg{err: err}
		}
		// Export() itself skips encrypted files (and notes it in MANIFEST.txt),
		// but returns no count of them — read it separately so the TUI can
		// surface the same honesty inline instead of only inside the written
		// manifest.
		skipped := store.EncryptedFileCount()
		res, err := store.Export(filepath.Join(home, "Downloads"), time.Now())
		if err != nil {
			return browserExportMsg{err: err}
		}
		return browserExportMsg{dir: res.Dir, count: len(res.Files), skipped: skipped}
	}
}

type browserRefreshMsg struct {
	files []memory.FileInfo
}

func newBrowserModel(store *memory.Store) browserModel {
	return browserModel{store: store}
}

// shortHome rewrites a path under the home directory to a ~/… form for compact display.
func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}

func (m browserModel) Refresh() tea.Cmd {
	return func() tea.Msg {
		files, _ := m.store.List()
		return browserRefreshMsg{files: orderBrowserFiles(files)}
	}
}

// orderBrowserFiles produces a STABLE, human-sensible order: editable memory
// files first, in canonical taxonomy order (identity, personal, preferences, …),
// then every read-only file (provider/rules docs, the aggregate) alphabetically.
// store.List() is already name-sorted, so SliceStable keeps a deterministic tie
// order — the list no longer reshuffles between renders.
func orderBrowserFiles(files []memory.FileInfo) []memory.FileInfo {
	rank := map[string]int{}
	for i, name := range memory.OrderedFiles() {
		rank[name] = i
	}
	sort.SliceStable(files, func(i, j int) bool {
		ri, oki := rank[files[i].Name]
		rj, okj := rank[files[j].Name]
		if oki && okj {
			return ri < rj
		}
		if oki != okj {
			return oki // editable taxonomy files come first
		}
		return files[i].Name < files[j].Name
	})
	return files
}

type OpenFileMsg struct {
	Filename string
	Content  string
	Editable bool
}

func (m browserModel) Update(msg tea.Msg) (browserModel, tea.Cmd) {
	switch msg := msg.(type) {
	case browserRefreshMsg:
		m.files = msg.files
		if m.cursor >= len(m.files) {
			m.cursor = 0
		}
	case browserExportMsg:
		if msg.err != nil {
			m.status = "✗ Export failed: " + msg.err.Error()
		} else {
			m.status = fmt.Sprintf("✓ Exported %d file(s) → %s", msg.count, shortHome(msg.dir))
			if msg.skipped > 0 {
				m.status += fmt.Sprintf("  (%d encrypted file(s) skipped — see MANIFEST.txt)", msg.skipped)
			}
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.files)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "e", "E":
			// Export every memory file to a timestamped folder in ~/Downloads.
			if len(m.files) > 0 {
				m.status = "Exporting…"
				return m, exportAllCmd(m.store)
			}
		case "enter":
			if m.cursor < len(m.files) {
				f := m.files[m.cursor]
				return m, func() tea.Msg {
					content, _ := m.store.View(f.Name)
					return OpenFileMsg{
						Filename: f.Name,
						Content:  content,
						Editable: memory.IsEditableFile(f.Name),
					}
				}
			}
		}
	}
	return m, nil
}

func (m browserModel) View() string {
	title := StyleTitle.Render("📁 File Browser")

	if len(m.files) == 0 {
		return title + "\n\nNo memory files found. Run 'auxly init' first."
	}

	header := fmt.Sprintf("  %-24s %8s %19s   %s", "FILE", "SIZE", "MODIFIED", "ACCESS")
	sep := lipgloss.NewStyle().Foreground(ColorDim).Render("  ──────────────────────────────────────────────────────────────────")

	editTag := lipgloss.NewStyle().Foreground(ColorSuccess).Render("✎ editable")
	roTag := lipgloss.NewStyle().Foreground(ColorDim).Render("🔒 read-only")

	var content string
	for i, f := range m.files {
		cursor := "  "
		if i == m.cursor {
			cursor = lipgloss.NewStyle().Foreground(ColorPrimary).Render("▸ ")
		}

		access := roTag
		if memory.IsEditableFile(f.Name) {
			access = editTag
		}

		line := fmt.Sprintf("%s%-24s %6d B %19s   %s", cursor, f.Name, f.Size, f.ModTime.Format("02/01/2006 15:04:05"), access)
		if i == m.cursor {
			line = StyleSelectedRow.Render(line)
		}
		content += line + "\n"
	}

	accent := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dim := lipgloss.NewStyle().Foreground(ColorDim)
	hint := "  " + accent.Render("[e]") + dim.Render(" export all to ~/Downloads (tagged with name + timestamp)")
	out := fmt.Sprintf("%s\n\n%s\n%s\n%s\n%s", title, header, sep, content, hint)
	if m.status != "" {
		color := ColorSuccess
		if strings.HasPrefix(m.status, "✗") {
			color = ColorDanger
		}
		out += "\n  " + lipgloss.NewStyle().Foreground(color).Render(m.status)
	}
	return out
}
