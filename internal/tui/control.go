package tui

import tea "github.com/charmbracelet/bubbletea"

type Control interface {
	Update(msg tea.Msg) tea.Cmd
	View() string
	Focus() tea.Cmd
	Blur()
	Value() string
	SetValue(v string)
	CanNavigateDown() bool
	CanNavigateUp() bool
}
