package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/debugging-sucks/event-horizon-sdk-go/eh"
	"github.com/debugging-sucks/openid/jwt"
	"github.com/debugging-sucks/runner/internal/config"
	"github.com/debugging-sucks/runner/internal/util"
	"github.com/google/renameio/v2"
	"github.com/pelletier/go-toml/v2"
)

const (
	pastelPink              = "#FFC5D3"
	grey                    = "#969696"
	red                     = "#FF0000"
	runnerSection           = "[runner]"
	runnerTokenLabel        = "Plan42 Runner Token"
	serverURLLabel          = "Server URL"
	saveButton              = "[OK]"
	cancelButton            = "[Cancel]"
	validatingTokenSection  = "Validating Token"
	connectionsSection      = "[github connections]"
	maxConnectionFieldIndex = 1
	maxRunnerFieldIndex     = 1
)

var commentStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color(grey))

var selectedSectionStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color(pastelPink)).
	PaddingTop(1)
var sectionStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color(grey)).
	PaddingTop(1)

var selectedFieldLabelStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color(pastelPink)).
	Width(20).
	Align(lipgloss.Left)

var fieldLabelStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color(grey)).
	Width(20).
	Align(lipgloss.Left)

var selectedButtonStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color(pastelPink)).
	Width(10).
	Align(lipgloss.Left)

var buttonStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color(grey)).
	Width(10).
	Align(lipgloss.Left)

var spinnerStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("69"))

var errorStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color(red))

type model struct {
	selectedSection      string
	selectedSectionIndex int
	selectedFieldIndex   int
	runnerToken          textinput.Model
	severURL             textinput.Model
	spinner              spinner.Model
	githubConnections    []*githubConnectionModel
	cfg                  config.Config
	validateErr          error
	saveErr              error
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m *model) triggerSave(cmds []tea.Cmd) []tea.Cmd {
	m.saveErr = nil
	return append(cmds, m.save)
}

func (m *model) triggerValidate(cmds []tea.Cmd) []tea.Cmd {
	m.runnerToken.Blur()
	m.cfg.Runner.RunnerToken = m.runnerToken.Value()
	m.selectedSection = validatingTokenSection
	m.validateErr = nil
	return append(cmds, m.validateToken, m.spinner.Tick)
}

func (m *model) getSectionStyle(sectionName string, sectionIndex int) *lipgloss.Style {
	if m.selectedSection == sectionName && m.selectedSectionIndex == sectionIndex {
		return &selectedSectionStyle
	}
	return &sectionStyle
}

func (m *model) getFieldLabelStyle(sectionName string, sectionIndex int, fieldIndex int) *lipgloss.Style {
	if m.selectedSection == sectionName && m.selectedSectionIndex == sectionIndex && m.selectedFieldIndex == fieldIndex {
		return &selectedFieldLabelStyle
	}
	return &fieldLabelStyle
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width)
	case error:
		cmds = m.onError(msg, cmds)
	case tea.KeyMsg:
		cmds = m.onKey(msg, cmds)
	case model:
		m = msg
		cmds = append(cmds, m.focusSelectedInput())
	}

	var cmd tea.Cmd

	pField := m.getSelectedInput()
	if pField != nil {
		*pField, cmd = pField.Update(msg)
	}

	if m.selectedSection == validatingTokenSection {
		m.spinner, cmd = m.spinner.Update(msg)
	}

	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) onError(msg error, cmds []tea.Cmd) []tea.Cmd {
	switch m.selectedSection {
	case validatingTokenSection:
		m.selectedSection = runnerSection
		m.selectedSectionIndex = 0
		m.selectedFieldIndex = maxRunnerFieldIndex
		m.validateErr = msg
		cmds = append(cmds, m.focusSelectedInput())
	case saveButton:
		m.saveErr = msg
	}
	return cmds
}

func (m model) View() string {
	b := strings.Builder{}
	b.WriteString(commentStyle.Render("# Plan42 Runner Config"))

	b.WriteString(m.getSectionStyle(runnerSection, 0).Render(runnerSection))
	b.WriteRune('\n')
	b.WriteString(m.getFieldLabelStyle(runnerSection, 0, 0).Render(runnerTokenLabel))
	b.WriteString(m.runnerToken.View())
	b.WriteRune('\n')
	b.WriteString(m.getFieldLabelStyle(runnerSection, 0, 1).Render(serverURLLabel))
	b.WriteString(m.severURL.View())
	b.WriteRune('\n')

	if m.validateErr != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("\nERROR: %v\n", m.validateErr)))
	}

	if m.selectedSection == validatingTokenSection {
		_, _ = fmt.Fprintf(&b, "\n%s  %s\n", m.spinner.View(), validatingTokenSection)
	}

	for i := range m.githubConnections {
		b.WriteString(m.getSectionStyle(connectionsSection, i).Render(fmt.Sprintf(
			"[github.%v]",
			m.githubConnections[i].name.Value(),
		)))
		b.WriteRune('\n')

		b.WriteString(fieldLabelStyle.Render("Name"))
		b.WriteString(m.githubConnections[i].name.View())
		b.WriteRune('\n')
		b.WriteString(fieldLabelStyle.Render("Connection ID"))
		b.WriteString(m.githubConnections[i].id.View())
		b.WriteRune('\n')
		b.WriteString(m.getFieldLabelStyle(connectionsSection, i, 0).Render("Server URL"))
		b.WriteString(m.githubConnections[i].serverURL.View())
		b.WriteRune('\n')
		b.WriteString(m.getFieldLabelStyle(connectionsSection, i, 1).Render("Github Token"))
		b.WriteString(m.githubConnections[i].githubToken.View())
		b.WriteRune('\n')
	}

	b.WriteRune('\n')
	if m.selectedSection == saveButton {
		b.WriteString(selectedButtonStyle.Render(saveButton))
	} else {
		b.WriteString(buttonStyle.Render(saveButton))
	}

	if m.selectedSection == cancelButton {
		b.WriteString(selectedButtonStyle.Render(cancelButton))
	} else {
		b.WriteString(buttonStyle.Render(cancelButton))
	}
	b.WriteRune('\n')

	if m.saveErr != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("\nERROR: %v", m.saveErr)))
	}

	return b.String()
}

func (m model) validateToken() tea.Msg {
	oldCfg := m.cfg.Github
	m.githubConnections = nil
	m.cfg.Github = make(map[string]*config.GithubInfo)
	m.selectedSection = saveButton

	if m.cfg.Runner.RunnerToken == "" {
		return errors.New("missing runner token")
	}

	if m.cfg.Runner.URL == "" {
		return errors.New("missing server url")
	}

	configByID := indexByID(oldCfg)

	split := strings.SplitN(m.cfg.Runner.RunnerToken, "_", 2)
	if len(split) != 2 || split[0] != "p42r" {
		return errors.New("invalid runner token")
	}

	token, err := jwt.Parse(split[1])
	if err != nil {
		return err
	}

	options := []eh.Option{
		eh.WithAPIToken(m.cfg.Runner.RunnerToken),
	}

	parsedURL, err := url.Parse(m.cfg.Runner.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" {
		return errors.New("invalid server url")
	}

	if parsedURL.Host == "localhost:7443" {
		options = append(options, eh.WithInsecureSkipVerify())
	}

	client := eh.NewClient(m.cfg.Runner.URL, options...)

	req := &eh.ListGithubConnectionsRequest{
		TenantID: token.Payload.Subject,
		Private:  util.Pointer(true),
	}

	for {
		resp, err := client.ListGithubConnections(context.Background(), req)
		var ehErr *eh.Error
		if errors.As(err, &ehErr) {
			if ehErr.ResponseCode == http.StatusForbidden {
				return errors.New("token not authorized")
			}
		}
		if err != nil {
			return fmt.Errorf("unable to connect to server: %w", err)
		}
		for _, conn := range resp.Items {
			cfg, ui := processConnection(conn, configByID)
			m.cfg.Github[cfg.Name] = cfg
			m.githubConnections = append(m.githubConnections, &ui)
		}

		if resp.NextToken == nil {
			break
		}
		req.Token = resp.NextToken
	}
	if len(m.githubConnections) != 0 {
		m.selectedSection = connectionsSection
		m.selectedSectionIndex = 0
		m.selectedFieldIndex = 0
	}
	return m
}

func indexByID(cfg map[string]*config.GithubInfo) map[string]*config.GithubInfo {
	ret := make(map[string]*config.GithubInfo)
	for _, v := range cfg {
		ret[v.ConnectionID] = v
	}
	return ret
}

func processConnection(
	conn *eh.GithubConnection,
	idIdx map[string]*config.GithubInfo,
) (*config.GithubInfo, githubConnectionModel) {
	existing := idIdx[conn.ConnectionID]
	cfgEntry := &config.GithubInfo{
		Name:         util.Deref(conn.Name),
		ConnectionID: conn.ConnectionID,
	}
	if existing != nil {
		cfgEntry.URL = existing.URL
		cfgEntry.Token = existing.Token
	}
	if cfgEntry.URL == "" {
		cfgEntry.URL = "https://github.com"
	}
	uiEntry := newGithubConnectionModel(cfgEntry)
	return cfgEntry, uiEntry
}

func (m *model) save() tea.Msg {
	fileData, err := toml.Marshal(m.cfg)
	if err != nil {
		return fmt.Errorf("unable to serialize config file: %w", err)
	}

	fileName, err := configFileName()
	if err != nil {
		return fmt.Errorf("unable to compute config file path: %w", err)
	}

	err = renameio.WriteFile(fileName, fileData, os.FileMode(0600))
	if err != nil {
		return fmt.Errorf("unable to save config file: %w", err)
	}
	return tea.Quit()
}

func (m *model) getSelectedInput() *textinput.Model {
	switch m.selectedSection {
	case runnerSection:
		switch m.selectedFieldIndex {
		case 0:
			return &m.runnerToken
		case 1:
			return &m.severURL
		}
	case connectionsSection:
		return m.githubConnections[m.selectedSectionIndex].getInput(m.selectedFieldIndex)
	}
	return nil
}

func (m *model) getTargetField() *string {
	switch m.selectedSection {
	case runnerSection:
		switch m.selectedFieldIndex {
		case 0:
			return &m.cfg.Runner.RunnerToken
		case 1:
			return &m.cfg.Runner.URL
		}
	case connectionsSection:
		entry := m.cfg.Github[m.githubConnections[m.selectedSectionIndex].name.Value()]
		switch m.selectedFieldIndex {
		case 0:
			return &entry.URL
		case 1:
			return &entry.Token
		}
	}
	return nil
}

func NoOp() tea.Msg {
	return nil
}

func (m *model) focusSelectedInput() tea.Cmd {
	ret := m.getSelectedInput()
	if ret != nil {
		return ret.Focus()
	}
	return NoOp
}

func (m *model) blurSelectedInput() {
	ret := m.getSelectedInput()
	if ret != nil {
		ret.Blur()
	}
}

func (m *model) commitChanges() {
	input := m.getSelectedInput()
	field := m.getTargetField()

	if input != nil && field != nil {
		*field = input.Value()
	}
}

func (m *model) resize(width int) {
	inputWidth := max(width-(fieldLabelStyle.GetWidth()+3), 10)
	m.runnerToken.Width = inputWidth
	m.severURL.Width = inputWidth

	for _, conn := range m.githubConnections {
		conn.serverURL.Width = inputWidth
		conn.githubToken.Width = inputWidth
	}
}

func (m *model) onKey(msg tea.KeyMsg, cmds []tea.Cmd) []tea.Cmd {
	switch msg.String() {
	case "ctrl+c", "esc":
		cmds = append(cmds, tea.Quit)
	case "ctrl+z":
		cmds = append(cmds, tea.Suspend)
	case "ctrl+s":
		switch m.selectedSection {
		case validatingTokenSection:
			// do nothing
		default:
			cmds = m.triggerSave(cmds)
		}
	case "enter":
		switch m.selectedSection {
		case saveButton:
			cmds = m.triggerSave(cmds)
		case cancelButton:
			cmds = append(cmds, tea.Quit)
		}
	case "left":
		if m.selectedSection == cancelButton {
			m.selectedSection = saveButton
		}
	case "right":
		if m.selectedSection == saveButton {
			m.selectedSection = cancelButton
		}
	case "shift+tab":
		if m.selectedSection == cancelButton {
			m.selectedSection = saveButton
			break
		}
		fallthrough // treat shift+tab as up arrow when not on the button row
	case "up":
		cmds = m.onUp(cmds)
	case "tab":
		if m.selectedSection == saveButton {
			m.selectedSection = cancelButton
			break
		}
		fallthrough // treat tab as down arrow when not on the button row
	case "down":
		cmds = m.onDown(cmds)
	}
	return cmds
}

func (m *model) onDown(cmds []tea.Cmd) []tea.Cmd {
	m.commitChanges()
	m.blurSelectedInput()
	switch m.selectedSection {
	case runnerSection:
		if m.selectedFieldIndex < maxRunnerFieldIndex {
			m.selectedFieldIndex++
			cmds = append(cmds, m.focusSelectedInput())
		} else {
			cmds = m.triggerValidate(cmds)
		}
	case connectionsSection:
		m.blurSelectedInput()
		switch {
		case m.selectedFieldIndex < maxConnectionFieldIndex:
			m.selectedFieldIndex++
		case m.selectedSectionIndex < len(m.githubConnections)-1:
			m.selectedSectionIndex++
			m.selectedFieldIndex = 0
		default:
			m.selectedSectionIndex = 0
			m.selectedFieldIndex = 0
			m.selectedSection = saveButton
		}

		if m.selectedSection == connectionsSection {
			cmds = append(cmds, m.focusSelectedInput())
		}
	}
	return cmds
}

func (m *model) onUp(cmds []tea.Cmd) []tea.Cmd {
	m.commitChanges()
	switch m.selectedSection {
	case cancelButton, saveButton:
		if len(m.githubConnections) == 0 {
			m.selectedSection = runnerSection
			m.selectedSectionIndex = 0
			m.selectedFieldIndex = maxRunnerFieldIndex
		} else {
			m.selectedSection = connectionsSection
			m.selectedSectionIndex = len(m.githubConnections) - 1
			m.selectedFieldIndex = maxConnectionFieldIndex
		}
	case runnerSection:
		m.blurSelectedInput()
		if m.selectedFieldIndex > 0 {
			m.selectedFieldIndex--
		}
	case connectionsSection:
		m.blurSelectedInput()
		switch {
		case m.selectedFieldIndex > 0:
			m.selectedFieldIndex--
		case m.selectedSectionIndex > 0:
			m.selectedSectionIndex--
			m.selectedFieldIndex = maxConnectionFieldIndex
		default:
			m.selectedSection = runnerSection
			m.selectedSectionIndex = 0
			m.selectedFieldIndex = maxRunnerFieldIndex
		}
	}
	cmds = append(cmds, m.focusSelectedInput())
	return cmds
}

func configFileName() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return path.Join(home, ".config", "plan42-runner.toml"), nil
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	_, err := p.Run()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		os.Exit(1)
	}
}

func initialModel() tea.Model {
	ret := &model{
		selectedSection:      runnerSection,
		selectedSectionIndex: 0,
		selectedFieldIndex:   0,
		runnerToken:          textinput.New(),
		severURL:             textinput.New(),
		spinner:              spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(spinnerStyle)),
	}
	ret.runnerToken.Focus()
	ret.runnerToken.Placeholder = "p42_01234abcdef..."
	ret.cfg.Runner.URL = "https://api.dev.plan42.ai"
	ret.severURL.SetValue(ret.cfg.Runner.URL)

	fileName, err := configFileName()
	if err != nil {
		return ret
	}
	f, err := os.Open(fileName)
	if err != nil {
		return ret
	}
	defer f.Close()
	err = toml.NewDecoder(f).Decode(&ret.cfg)
	if err != nil {
		return ret
	}
	for _, entry := range ret.cfg.Github {
		uiEntry := newGithubConnectionModel(entry)
		ret.githubConnections = append(ret.githubConnections, &uiEntry)
	}
	ret.runnerToken.SetValue(ret.cfg.Runner.RunnerToken)
	ret.severURL.SetValue(ret.cfg.Runner.URL)

	return ret
}

type githubConnectionModel struct {
	name        textinput.Model
	id          textinput.Model
	serverURL   textinput.Model
	githubToken textinput.Model
}

func (g *githubConnectionModel) getInput(index int) *textinput.Model {
	switch index {
	case 0:
		return &g.serverURL
	case 1:
		return &g.githubToken
	default:
		panic("invalid field index")
	}
}

func newGithubConnectionModel(entry *config.GithubInfo) githubConnectionModel {
	ret := githubConnectionModel{
		name:        textinput.New(),
		id:          textinput.New(),
		serverURL:   textinput.New(),
		githubToken: textinput.New(),
	}
	ret.name.SetValue(entry.Name)
	ret.id.SetValue(entry.ConnectionID)
	ret.name.Blur()
	ret.id.Blur()
	ret.serverURL.SetValue(entry.URL)
	ret.githubToken.SetValue(entry.Token)
	return ret
}
