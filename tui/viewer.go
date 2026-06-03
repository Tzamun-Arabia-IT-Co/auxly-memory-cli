package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

type viewerModel struct {
	store    *memory.Store
	filename string
	content  string   // raw file content (source of truth)
	lines    []string // soft-wrapped display lines for the read view
	scrollY  int
	height   int
	width    int

	editable bool           // is this a taxonomy memory file?
	editing  bool           // edit mode active?
	ta       textarea.Model // editor (only used while editing)
	status   string         // transient status line (Saved / cancelled / error)
}

func newViewerModel(store *memory.Store) viewerModel {
	return viewerModel{store: store, height: 18, width: 80}
}

// contentWidth is the usable text width inside the bordered box.
func (m viewerModel) contentWidth() int {
	boxWidth := m.width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}
	cw := boxWidth - 6
	if cw < 20 {
		cw = 20
	}
	return cw
}

func (m viewerModel) boxWidth() int {
	bw := m.width - 4
	if bw < 40 {
		bw = 40
	}
	return bw
}

// load resets the viewer for a freshly opened file.
func (m viewerModel) load(filename, content string, editable bool) viewerModel {
	m.filename = filename
	m.content = content
	m.editable = editable
	m.editing = false
	m.status = ""
	m.scrollY = 0
	m.rewrap()
	return m
}

// rewrap recomputes the soft-wrapped display lines (rune-aware via reflow, so
// long paragraphs and Arabic text show in full instead of being byte-truncated).
func (m *viewerModel) rewrap() {
	expanded := strings.ReplaceAll(m.content, "\t", "    ")
	wrapped := wordwrap.String(expanded, m.contentWidth())
	m.lines = strings.Split(wrapped, "\n")
}

// downloadFile copies the open file to ~/Downloads so the user can keep or share
// it. Works for any file (editable or read-only) — it's just a copy, not an edit.
func (m viewerModel) downloadFile() viewerModel {
	home, err := os.UserHomeDir()
	if err != nil {
		m.status = "✗ Download failed: " + err.Error()
		return m
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		m.status = "✗ Download failed: " + err.Error()
		return m
	}
	dest := filepath.Join(dir, filepath.Base(m.filename))
	if err := os.WriteFile(dest, []byte(m.content), 0644); err != nil {
		m.status = "✗ Download failed: " + err.Error()
		return m
	}
	m.status = "✓ Downloaded to ~/Downloads/" + filepath.Base(m.filename)
	return m
}

func (m viewerModel) enterEdit() viewerModel {
	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.CharLimit = 0 // unlimited — memory files routinely exceed the 400-char default
	ta.Prompt = "│ "
	ta.SetValue(m.content)
	ta.SetWidth(m.boxWidth())
	ta.SetHeight(m.height)
	ta.Focus()
	m.ta = ta
	m.editing = true
	m.status = ""
	return m
}

func (m viewerModel) Update(msg tea.Msg) (viewerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.editing {
			switch msg.String() {
			case "ctrl+s":
				content := m.ta.Value()
				if err := m.store.Write(m.filename, content); err != nil {
					m.status = "✗ Save failed: " + err.Error()
					return m, nil
				}
				m.content = content
				m.editing = false
				m.rewrap()
				m.status = "✓ Saved " + m.filename
				return m, nil
			case "esc":
				m.editing = false
				m.status = "Edit cancelled — no changes saved"
				return m, nil
			}
			var cmd tea.Cmd
			m.ta, cmd = m.ta.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "e":
			if m.editable {
				m = m.enterEdit()
			}
		case "d":
			m = m.downloadFile()
		case "j", "down":
			if m.scrollY < len(m.lines)-m.height {
				m.scrollY++
			}
		case "k", "up":
			if m.scrollY > 0 {
				m.scrollY--
			}
		case "pgdown", " ":
			m.scrollY += m.height
			if m.scrollY > len(m.lines)-m.height {
				m.scrollY = len(m.lines) - m.height
			}
			if m.scrollY < 0 {
				m.scrollY = 0
			}
		case "pgup":
			m.scrollY -= m.height
			if m.scrollY < 0 {
				m.scrollY = 0
			}
		case "g", "home":
			m.scrollY = 0
		case "G", "end":
			m.scrollY = len(m.lines) - m.height
			if m.scrollY < 0 {
				m.scrollY = 0
			}
		}
	case tea.WindowSizeMsg:
		chrome := 24
		if msg.Width < 80 {
			chrome = 19
		}
		m.height = msg.Height - chrome
		if m.height < 3 {
			m.height = 3
		}
		m.width = msg.Width
		m.rewrap()
		if m.editing {
			m.ta.SetWidth(m.boxWidth())
			m.ta.SetHeight(m.height)
		}
		if m.scrollY > len(m.lines)-m.height {
			m.scrollY = len(m.lines) - m.height
		}
		if m.scrollY < 0 {
			m.scrollY = 0
		}
	}
	return m, nil
}

func (m viewerModel) View() string {
	if m.editing {
		return m.editView()
	}

	title := StyleTitle.Render(fmt.Sprintf("📖 Viewing: %s", m.filename))
	if !m.editable {
		title += "  " + lipgloss.NewStyle().Foreground(ColorDim).Render("🔒 read-only")
	}

	if len(m.lines) == 0 || (len(m.lines) == 1 && m.lines[0] == "") {
		empty := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorDim).
			Padding(1, 2).
			Width(m.boxWidth()).
			Render("(Empty file)")
		return fmt.Sprintf("%s\n\n%s", title, empty)
	}

	visibleLines := m.lines[m.scrollY:]
	if len(visibleLines) > m.height {
		visibleLines = visibleLines[:m.height]
	}

	contentBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorDim).
		Padding(1, 2).
		Width(m.boxWidth()).
		Render(strings.Join(visibleLines, "\n"))

	scrollPct := 0
	if len(m.lines) > m.height {
		scrollPct = (m.scrollY * 100) / (len(m.lines) - m.height)
	}
	footer := fmt.Sprintf("Line %d-%d of %d (%d%%)", m.scrollY+1, m.scrollY+len(visibleLines), len(m.lines), scrollPct)
	footer += " • d: download"
	if m.editable {
		footer += " • e: edit"
	}
	if m.status != "" {
		footer += "  " + lipgloss.NewStyle().Foreground(ColorSuccess).Render(m.status)
	}

	return fmt.Sprintf("%s\n\n%s\n\n%s", title, contentBox, StyleFooter.Render(footer))
}

func (m viewerModel) editView() string {
	title := StyleTitle.Render(fmt.Sprintf("✏️  Editing: %s", m.filename))

	editorBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		Width(m.boxWidth()).
		Render(m.ta.View())

	hint := StyleFooter.Render("Ctrl+S: save • Esc: cancel (discard) • arrows/PgUp/PgDn: move cursor")
	return fmt.Sprintf("%s\n\n%s\n\n%s", title, editorBox, hint)
}
