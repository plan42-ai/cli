package dropdown

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	collapsed = "▶"
	expanded  = "▼"
)

type Item interface {
	list.Item
	Summary() string
}

type Styles struct {
	Summary                   lipgloss.Style
	FocusedSummary            lipgloss.Style
	ExpandedAndFocusedSummary lipgloss.Style
	Chrome                    lipgloss.Style
	FocusedChrome             lipgloss.Style
}

type Model struct {
	expanded      bool
	focused       bool
	selectedIndex int
	list          list.Model
	styles        Styles
}

func (m Model) View() string {
	var ret strings.Builder
	chromeStyle := m.chromeStyle()
	ret.WriteString(chromeStyle.Render("["))
	summaryStyle := m.summaryStyle()

	selected := m.SelectedItem()
	var summaryText string
	if selected != nil {
		summaryText = " " + m.chevron() + " " + selected.Summary()
	}
	maxSummaryWidth := summaryStyle.GetMaxWidth()
	if maxSummaryWidth == 0 {
		maxSummaryWidth = summaryStyle.GetWidth()
	}
	if maxSummaryWidth != 0 {
		summaryText = ansi.Truncate(summaryText, maxSummaryWidth, "...")
	}
	summaryText += " "
	ret.WriteString(summaryStyle.Render(summaryText))

	ret.WriteString(chromeStyle.Render("]"))
	if m.expanded {
		ret.WriteString("\n")
		ret.WriteString(m.list.View())
	}
	return ret.String()
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "enter", " ":
		if m.expanded {
			m.selectedIndex = m.list.GlobalIndex()
			m.Collapse()
			return m, nil
		}
		m.Expand()
	case "esc", "left":
		if m.expanded {
			m.Collapse()
			return m, nil
		}
	case "right":
		if !m.expanded {
			m.Expand()
		}
	}
	var cmd1 tea.Cmd
	var cmd2 tea.Cmd

	if m.expanded {
		m.list, cmd2 = m.list.Update(msg)
	}
	return m, tea.Batch(cmd1, cmd2)
}

func (m *Model) Focus() {
	m.focused = true
}

func (m *Model) Blur() {
	m.focused = false
}

func (m *Model) Expand() {
	m.expanded = true
	m.list.Select(m.selectedIndex)
}

func (m *Model) Collapse() {
	m.expanded = false
}

func (m *Model) chromeStyle() lipgloss.Style {
	if m.focused {
		return m.styles.FocusedChrome
	}
	return m.styles.Chrome
}

func (m *Model) summaryStyle() lipgloss.Style {
	if m.focused && m.expanded {
		return m.styles.ExpandedAndFocusedSummary
	}
	if m.focused {
		return m.styles.FocusedSummary
	}
	return m.styles.Summary
}

func (m *Model) SelectedItem() Item {
	if m.selectedIndex >= len(m.list.Items()) {
		return nil
	}
	ret, _ := m.list.Items()[m.selectedIndex].(Item)
	return ret
}

func (m *Model) SetItems(items []Item) tea.Cmd {
	items2 := narrow(items)
	m.selectedIndex = 0
	return m.list.SetItems(items2)
}

func narrow(items []Item) []list.Item {
	// we need to copy to narrow items from []Item to []list.Item
	items2 := make([]list.Item, len(items))
	for i := range items {
		items2[i] = items[i]
	}
	return items2
}

func (m *Model) InsertItem(index int, item Item) tea.Cmd {
	return m.list.InsertItem(index, item)
}

func (m *Model) RemoveItem(index int) {
	if index <= m.selectedIndex {
		m.selectedIndex = max(m.selectedIndex-1, 0)
	}
	m.list.RemoveItem(index)
}

func (m *Model) Select(index int) {
	if index < 0 || index >= len(m.list.Items()) {
		index = 0
	}
	m.selectedIndex = index
	m.list.Select(index)
}

func (m *Model) SetSyles(styles Styles) {
	m.styles = styles
}

func (m *Model) SetDelegate(d list.ItemDelegate) {
	m.list.SetDelegate(d)
}

func (m *Model) SetShowTitle(v bool) {
	m.list.SetShowTitle(v)
}

func (m Model) ShowTitle() bool {
	return m.list.ShowTitle()
}

func (m *Model) SetShowFilter(v bool) {
	m.list.SetShowFilter(v)
}

func (m Model) ShowFilter() bool {
	return m.list.ShowFilter()
}

func (m *Model) SetShowStatusBar(v bool) {
	m.list.SetShowStatusBar(v)
}

func (m Model) ShowStatusBar() bool {
	return m.list.ShowStatusBar()
}

func (m *Model) SetShowPagination(v bool) {
	m.list.SetShowPagination(v)
}

func (m Model) ShowPagination() bool {
	return m.list.ShowPagination()
}

func (m *Model) SetShowHelp(v bool) {
	m.list.SetShowHelp(v)
}

func (m Model) ShowHelp() bool {
	return m.list.ShowHelp()
}

func (m Model) HighlightedIndex() int {
	return m.list.GlobalIndex()
}

func (m Model) SelectedIndex() int {
	return m.selectedIndex
}

func (m Model) chevron() string {
	if m.expanded {
		return expanded
	}
	return collapsed
}

func New(items []Item, delegate list.ItemDelegate, listWidth, listHeight int) Model {
	return Model{
		expanded:      false,
		focused:       false,
		selectedIndex: 0,
		list:          list.New(narrow(items), delegate, listWidth, listHeight),
	}
}
