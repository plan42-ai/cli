package runtimeselector

import (
	"fmt"
	"io"
	"os/exec"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/plan42-ai/cli/internal/tui/dropdown"
)

const (
	grey       = "#969696"
	pastelPink = "#FFC5D3"
)

var (
	selectedLabel         = lipgloss.NewStyle().Width(21).Align(lipgloss.Left).Foreground(lipgloss.Color(pastelPink)).Reverse(true)
	unselectedLabel       = lipgloss.NewStyle().Width(21).Align(lipgloss.Left)
	selectedInstallFlag   = lipgloss.NewStyle().Width(15).Align(lipgloss.Left).Foreground(lipgloss.Color(pastelPink)).Reverse(true)
	unselectedInstallFlag = lipgloss.NewStyle().Width(15).Align(lipgloss.Left).Foreground(lipgloss.Color(grey))
	summary               = lipgloss.NewStyle().Width(20).Foreground(lipgloss.Color(grey)).Align(lipgloss.Left)
	expandedFocused       = summary.Foreground(lipgloss.Color(pastelPink))
	focused               = expandedFocused.Reverse(true)
	dropdownStyles        = dropdown.Styles{
		Summary:                   summary,
		FocusedSummary:            focused,
		ExpandedAndFocusedSummary: expandedFocused,
		Chrome:                    lipgloss.NewStyle().Foreground(lipgloss.Color(grey)),
		FocusedChrome:             lipgloss.NewStyle().Foreground(lipgloss.Color(pastelPink)),
	}
)

type Item struct {
	Name        string
	Installed   bool
	ConfigValue string
}

func (i Item) FilterValue() string {
	return i.Name
}

func (i Item) Summary() string {
	return i.Name
}

type itemDelegate struct{}

func (itemDelegate) Render(w io.Writer, m list.Model, idx int, item list.Item) {
	i, _ := item.(Item)
	var labelStyle = unselectedLabel
	var installedStyle = unselectedInstallFlag
	var installedStr = "(Not Installed)"
	if i.Installed {
		installedStr = "(Installed)"
	}
	var indicator = " "
	if m.Index() == idx {
		indicator = ">"
		labelStyle = selectedLabel
		installedStyle = selectedInstallFlag
	}
	_, _ = fmt.Fprint(w, "\t", labelStyle.Render(fmt.Sprintf("%v %v", indicator, i.Name)))
	_, _ = fmt.Fprint(w, installedStyle.Render(installedStr))
}

func (i itemDelegate) Height() int {
	return 1
}

func (i itemDelegate) Spacing() int {
	return 0
}

func (i itemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd {
	return nil
}

type Model struct {
	dropdown.Model
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd

	m.Model, cmd = m.Model.Update(msg)
	return m, cmd
}

func New() Model {
	ret := Model{
		Model: dropdown.New(
			[]dropdown.Item{
				Item{
					Name:        "Apple Container",
					Installed:   isInstalled("container"),
					ConfigValue: "apple",
				},
				Item{
					Name:        "Podman",
					Installed:   isInstalled("podman"),
					ConfigValue: "podman",
				},
			},
			itemDelegate{},
			100,
			2,
		),
	}

	ret.SetShowStatusBar(false)
	ret.SetShowFilter(false)
	ret.SetShowTitle(false)
	ret.SetShowPagination(false)
	ret.SetShowHelp(false)
	ret.SetSyles(dropdownStyles)
	return ret
}

func isInstalled(executable string) bool {
	p, err := exec.LookPath(executable)
	return p != "" && err == nil
}
