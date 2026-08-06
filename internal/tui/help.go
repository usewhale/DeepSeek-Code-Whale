package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	appcommands "github.com/usewhale/whale/internal/runtime/commands"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

const helpDocsURL = "https://github.com/usewhale/whale"

func (m *model) openHelp() {
	m.clearEphemeralMessages()
	m.recordPromptHistory("/help")
	m.resetHistoryNavigation()
	m.input.SetValue("")
	m.skillBinding = nil
	m.resetWindowsPasteFallbackInputState()
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	m.help.selected = 0
	m.help.offset = 0
	m.mode = modeHelp
	m.status = "help"
	m.refreshViewportContent()
}

func (m *model) closeHelp() {
	m.mode = modeChat
	m.status = "ready"
	m.help.selected = 0
	m.help.offset = 0
}

func (m *model) handleHelpKey(msg tea.KeyMsg) tea.Cmd {
	commands := helpCommands()
	if len(commands) == 0 {
		m.closeHelp()
		return nil
	}
	switch msg.String() {
	case "up", "k":
		m.help.selected = wrapSelection(m.help.selected, len(commands), -1)
	case "down", "j":
		m.help.selected = wrapSelection(m.help.selected, len(commands), 1)
	case "pgup", "ctrl+u":
		m.help.selected = max(0, m.help.selected-m.helpVisibleCount())
	case "pgdown", "ctrl+d":
		m.help.selected = min(len(commands)-1, m.help.selected+m.helpVisibleCount())
	case "home":
		m.help.selected = 0
	case "end":
		m.help.selected = len(commands) - 1
	case "esc", "ctrl+c", "enter":
		m.closeHelp()
	}
	m.ensureHelpSelectionVisible()
	return nil
}

func (m *model) helpVisibleCount() int {
	mainWidth, _ := m.layoutDims()
	// Keep the help panel within the bottom area: 6 rows is the minimum usable
	// panel, -4 leaves room for prompt/footer chrome, -7 accounts for title,
	// section label, arrows, docs, and cancel hint, and each command uses 2 rows.
	maxHelpRows := max(6, m.height-countVisibleLines(m.renderBusyStatusLine(mainWidth))-4)
	return max(1, (maxHelpRows-7)/2)
}

func (m *model) ensureHelpSelectionVisible() {
	visible := m.helpVisibleCount()
	if m.help.selected < m.help.offset {
		m.help.offset = m.help.selected
	}
	if m.help.selected >= m.help.offset+visible {
		m.help.offset = m.help.selected - visible + 1
	}
	if m.help.offset < 0 {
		m.help.offset = 0
	}
}

func (m model) renderHelp() string {
	commands := helpCommands()
	visible := m.helpVisibleCount()
	start := min(max(0, m.help.offset), max(0, len(commands)-visible))
	end := min(len(commands), start+visible)

	title := lipgloss.NewStyle().Bold(true).Foreground(tuitheme.Default.Info)
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	selected := lipgloss.NewStyle().Foreground(tuitheme.Default.Palette)
	doc := lipgloss.NewStyle().Foreground(tuitheme.Default.Info)

	rows := []string{
		title.Render(fmt.Sprintf("Whale help  %d/%d", m.help.selected+1, len(commands))),
		"",
		"Browse default commands:",
	}
	if start > 0 {
		rows = append(rows, muted.Render("↑"))
	}
	for i := start; i < end; i++ {
		cmd := commands[i]
		prefix := "  "
		style := lipgloss.NewStyle()
		if i == m.help.selected {
			prefix = "> "
			style = selected
		}
		rows = append(rows, style.Render(prefix+cmd.Name))
		rows = append(rows, muted.Render("    "+cmd.Description))
	}
	if end < len(commands) {
		rows = append(rows, muted.Render("↓"))
	}
	rows = append(rows, "", doc.Render("For more help: "+helpDocsURL), muted.Render("Esc to cancel"))

	width := max(20, m.width-2)
	return lipgloss.NewStyle().
		Foreground(tuitheme.Default.Info).
		Border(lipgloss.NormalBorder()).
		BorderForeground(tuitheme.Default.Border).
		Padding(0, 1).
		Width(width).
		Render(strings.Join(rows, "\n"))
}

type helpCommand struct {
	Name        string
	Description string
}

func helpCommands() []helpCommand {
	specs := appcommands.DefaultSlashCommands()
	out := make([]helpCommand, 0, len(specs))
	for _, spec := range specs {
		name := spec.Name
		if spec.ArgumentHint != "" {
			name += " " + spec.ArgumentHint
		}
		out = append(out, helpCommand{Name: name, Description: spec.Description})
	}
	return out
}
