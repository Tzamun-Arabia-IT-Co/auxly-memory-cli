package cmd

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/memory"
	tea "github.com/charmbracelet/bubbletea"
	lipgloss "github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var viewCmd = &cobra.Command{
	Use:   "view <file>",
	Short: "Print the contents of a memory file",
	Args:  cobra.ExactArgs(1),
	RunE:  runView,
}

func init() {
	rootCmd.AddCommand(viewCmd)
}

type cliViewerModel struct {
	filename string
	content  string
	lines    []string
	scrollY  int
	width    int
	height   int
	quitting bool
}

func (m cliViewerModel) Init() tea.Cmd {
	return nil
}

func (m cliViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "j", "down":
			if m.scrollY < len(m.lines)-m.height {
				m.scrollY++
			}
		case "k", "up":
			if m.scrollY > 0 {
				m.scrollY--
			}
		case "pgdown", "d", " ":
			m.scrollY += m.height
			if m.scrollY > len(m.lines)-m.height {
				m.scrollY = len(m.lines) - m.height
			}
			if m.scrollY < 0 {
				m.scrollY = 0
			}
		case "pgup", "u":
			m.scrollY -= m.height
			if m.scrollY < 0 {
				m.scrollY = 0
			}
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height - 6 // Reserve 6 lines for header/footer
		if m.height < 5 {
			m.height = 5
		}
	}
	return m, nil
}

func (m cliViewerModel) View() string {
	if m.quitting {
		return ""
	}

	cyan := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("038"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("34"))

	var b strings.Builder

	title := cyan.Render(fmt.Sprintf("📖 Viewing Memory: %s", m.filename))
	b.WriteString("  " + title + "\n\n")

	if len(m.lines) == 0 {
		b.WriteString("  (Empty file)\n")
		return b.String()
	}

	visibleLines := m.lines[m.scrollY:]
	if len(visibleLines) > m.height {
		visibleLines = visibleLines[:m.height]
	}

	boxWidth := m.width - 4
	if boxWidth < 20 {
		boxWidth = 20
	}
	if boxWidth > 80 {
		boxWidth = 80
	}

	contentBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("038")).
		Padding(1, 2).
		Width(boxWidth).
		Render(strings.Join(visibleLines, "\n"))
	b.WriteString(contentBox + "\n\n")

	scrollPct := 0
	if len(m.lines) > m.height {
		scrollPct = (m.scrollY * 100) / (len(m.lines) - m.height)
	}

	footer := dim.Render(fmt.Sprintf("  Line %d-%d of %d (%d%%) • %s",
		m.scrollY+1,
		m.scrollY+len(visibleLines),
		len(m.lines),
		scrollPct,
		green.Render("↑/↓ scroll • q/esc exit"),
	))
	b.WriteString(footer + "\n")

	return b.String()
}

func runView(cmd *cobra.Command, args []string) error {
	store := memory.NewStore(getMemoryPath())
	content, err := store.View(args[0])
	if err != nil {
		return err
	}

	lines := strings.Split(content, "\n")
	m := cliViewerModel{
		filename: args[0],
		content:  content,
		lines:    lines,
		scrollY:  0,
		width:    80,
		height:   20,
	}

	p := tea.NewProgram(&m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
