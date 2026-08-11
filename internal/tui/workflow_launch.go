package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	appcommands "github.com/usewhale/whale/internal/runtime/commands"
	"github.com/usewhale/whale/internal/runtime/protocol"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

const (
	workflowRawScriptActionLabel  = "View raw script"
	workflowRawScriptSectionTitle = "Raw script"
)

func (m *model) openWorkflowLaunch(result *protocol.LocalResult) tea.Cmd {
	m.workflowLaunch.result = result
	m.workflowLaunch.selected = 0
	m.workflowLaunch.rawScroll = 0
	m.mode = modeWorkflowLaunch
	m.status = "workflow"
	return nil
}

func (m *model) handleWorkflowLaunchKey(msg tea.KeyMsg) tea.Cmd {
	actions := workflowLaunchActions(m.workflowLaunch.result)
	switch msg.String() {
	case "esc", "q":
		m.mode = modeChat
		m.status = "ready"
	case "up", "k":
		if len(actions) > 0 {
			m.workflowLaunch.selected = wrapSelection(m.workflowLaunch.selected, len(actions), -1)
		}
	case "down", "j":
		if len(actions) > 0 {
			m.workflowLaunch.selected = wrapSelection(m.workflowLaunch.selected, len(actions), 1)
		}
	case "enter":
		if len(actions) == 0 {
			return nil
		}
		action := actions[m.workflowLaunch.selected]
		if workflowLaunchIsRawScriptAction(action) {
			m.workflowLaunch.rawScroll = 0
			m.mode = modeWorkflowRawScript
			m.status = "workflow"
			m.refreshViewportContent()
			return nil
		}
		if strings.TrimSpace(action.WorkflowName) != "" {
			m.mode = modeChat
			m.status = "command pending"
			m.localSubmitPending++
			m.dispatchIntent(protocol.Intent{
				Kind:               protocol.IntentStartWorkflow,
				WorkflowName:       action.WorkflowName,
				WorkflowArgs:       action.WorkflowArgs,
				WorkflowResume:     action.WorkflowResume,
				WorkflowTrust:      action.WorkflowTrust,
				WorkflowScript:     action.WorkflowScript,
				WorkflowSaveAs:     action.WorkflowSaveAs,
				WorkflowScriptPath: action.WorkflowScriptPath,
			})
			m.refreshViewportContent()
			return nil
		}
		cmd := strings.TrimSpace(action.Command)
		m.mode = modeChat
		m.status = "ready"
		if cmd == "" {
			return nil
		}
		submit := appcommands.ClassifySubmit(cmd, appcommands.CommandsHelp())
		return m.submitLocalNoTurn(submit)
	}
	return nil
}

func (m *model) handleWorkflowRawScriptKey(msg tea.KeyMsg) tea.Cmd {
	rows := workflowRawScriptRows(m.workflowLaunch.result, max(20, min(m.width-8, 116)))
	page := workflowRawScriptViewportRows(m.height)
	maxScroll := max(0, len(rows)-page)
	switch msg.String() {
	case "esc", "q", "left", "backspace":
		m.mode = modeWorkflowLaunch
		m.status = "workflow"
	case "up", "k":
		m.workflowLaunch.rawScroll = max(0, m.workflowLaunch.rawScroll-1)
	case "down", "j":
		m.workflowLaunch.rawScroll = min(maxScroll, m.workflowLaunch.rawScroll+1)
	case "pgup", "ctrl+b":
		m.workflowLaunch.rawScroll = max(0, m.workflowLaunch.rawScroll-page)
	case "pgdown", "ctrl+f":
		m.workflowLaunch.rawScroll = min(maxScroll, m.workflowLaunch.rawScroll+page)
	case "home", "g":
		m.workflowLaunch.rawScroll = 0
	case "end", "G":
		m.workflowLaunch.rawScroll = maxScroll
	}
	return nil
}

func (m model) renderWorkflowLaunch() string {
	result := m.workflowLaunch.result
	if result == nil {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 100
	}
	contentWidth := max(40, min(width-4, 120))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tuitheme.Default.Palette)
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	lines := []string{titleStyle.Render("Workflow(dynamic workflow: " + workflowLaunchField(result, "Workflow") + ")"), "", result.Title}
	if desc := workflowLaunchDescription(result); desc != "" {
		lines = append(lines, "", desc)
	}
	if phaseSection := workflowPanelSection(result, "Phases"); phaseSection != nil && len(phaseSection.Fields) > 0 {
		lines = append(lines, "", "Phases")
		for _, field := range phaseSection.Fields {
			lines = append(lines, workflowLaunchWrapLine(field.Label+"  "+field.Value, contentWidth-4)...)
		}
	}
	if args := workflowLaunchField(result, "Args"); args != "" {
		lines = append(lines, "", "args: "+args)
	}
	if risk := workflowLaunchField(result, "Risk"); risk != "" {
		lines = append(lines, "", risk)
	}
	actions := workflowLaunchActions(result)
	if len(actions) > 0 {
		lines = append(lines, "", "Actions")
		for i, action := range actions {
			prefix := "  "
			if i == m.workflowLaunch.selected {
				prefix = "❯ "
			}
			label := strings.TrimSpace(action.Label)
			if label == "" {
				label = "Action"
			}
			lines = append(lines, workflowLaunchWrapLine(prefix+label, contentWidth-4)...)
		}
	}
	lines = append(lines, "", muted.Render("Esc to cancel · ↑↓ select · Enter"))
	return workflowPanelSingleColumnBox(lines, contentWidth)
}

func (m model) renderWorkflowRawScript() string {
	result := m.workflowLaunch.result
	if result == nil {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 100
	}
	contentWidth := max(40, min(width-4, 120))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tuitheme.Default.Palette)
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	lines := []string{titleStyle.Render("Workflow raw script")}
	if workflowName := workflowLaunchField(result, "Workflow"); workflowName != "" {
		lines = append(lines, "", "Workflow  "+workflowName)
	}
	if source := workflowRawScriptField(result, "Source"); source != "" {
		lines = append(lines, "Source    "+source)
	}
	if path := workflowRawScriptField(result, "Path"); path != "" {
		lines = append(lines, "Script    "+path)
	}
	codeRows := workflowRawScriptRows(result, contentWidth-4)
	page := workflowRawScriptViewportRows(m.height)
	maxScroll := max(0, len(codeRows)-page)
	scroll := min(max(0, m.workflowLaunch.rawScroll), maxScroll)
	if len(codeRows) == 0 {
		codeRows = []string{"No raw script is available for this workflow."}
		page = 1
		scroll = 0
		maxScroll = 0
	}
	end := min(len(codeRows), scroll+page)
	lines = append(lines, "", muted.Render(fmt.Sprintf("Lines %d-%d of %d", scroll+1, end, len(codeRows))))
	lines = append(lines, codeRows[scroll:end]...)
	footer := "Esc back · ↑↓ scroll"
	if maxScroll > 0 {
		footer += " · PgUp/PgDn page"
	}
	lines = append(lines, "", muted.Render(footer))
	return workflowPanelSingleColumnBox(lines, contentWidth)
}

func workflowLaunchActions(result *protocol.LocalResult) []protocol.LocalResultAction {
	if result == nil {
		return nil
	}
	return result.Actions
}

func workflowLaunchField(result *protocol.LocalResult, label string) string {
	if result == nil {
		return ""
	}
	return localResultFieldValue(result.Fields, label)
}

func workflowLaunchIsRawScriptAction(action protocol.LocalResultAction) bool {
	return strings.EqualFold(strings.TrimSpace(action.Label), workflowRawScriptActionLabel)
}

func workflowRawScriptField(result *protocol.LocalResult, label string) string {
	section := workflowPanelSection(result, workflowRawScriptSectionTitle)
	if section == nil {
		return ""
	}
	return localResultFieldValue(section.Fields, label)
}

func workflowRawScriptRows(result *protocol.LocalResult, width int) []string {
	script := workflowRawScriptField(result, "Script")
	if strings.TrimSpace(script) == "" {
		return nil
	}
	sourceLines := strings.Split(strings.ReplaceAll(script, "\r\n", "\n"), "\n")
	rows := make([]string, 0, len(sourceLines))
	for _, line := range sourceLines {
		wrapped := workflowPanelWrapLine(line, max(1, width))
		if len(wrapped) == 0 {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, wrapped...)
	}
	return rows
}

func workflowRawScriptViewportRows(height int) int {
	if height <= 0 {
		return 12
	}
	return max(6, min(24, height-12))
}

func workflowLaunchDescription(result *protocol.LocalResult) string {
	if result == nil {
		return ""
	}
	text := strings.TrimSpace(result.PlainText)
	if text == "" {
		return ""
	}
	lines := workflowPanelTextLines(text)
	for i, line := range lines {
		if strings.TrimSpace(line) == "Run a dynamic workflow?" {
			for j := i + 1; j < len(lines); j++ {
				if candidate := strings.TrimSpace(lines[j]); candidate != "" {
					return candidate
				}
			}
		}
	}
	return ""
}

func workflowLaunchWrapLine(line string, width int) []string {
	wrapped := workflowPanelWrapLine(strings.TrimSpace(line), max(1, width))
	if len(wrapped) == 0 {
		return []string{""}
	}
	return wrapped
}
