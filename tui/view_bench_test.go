package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func benchView(b *testing.B, w, h int) {
	m := populatedDashboard(&testing.T{})
	u, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = u.(model)
	m.screen = screenDashboard
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
		u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		m = u.(model)
	}
}
func BenchmarkDashboardFrame145(b *testing.B) { benchView(b, 145, 40) }
func BenchmarkDashboardFrame100(b *testing.B) { benchView(b, 100, 30) }
