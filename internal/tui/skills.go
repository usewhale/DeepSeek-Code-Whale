package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

func (m *model) updateSkillMatches() {
	m.skills.matches = nil
	if m.mode != modeChat || m.busy || m.hasSlashSuggestions() || m.hasFilePanel() {
		m.skills.selected = 0
		return
	}
	raw := m.input.Value()
	if m.inHistoryNav && raw == m.lastHistoryText {
		m.skills.selected = 0
		return
	}
	query, ok := skillQuery(raw)
	if !ok {
		m.skills.selected = 0
		return
	}
	if m.runtime != nil || m.skills.all == nil {
		m.refreshSkillSuggestions()
	}
	matches := make([]skillSuggestion, 0, len(m.skills.all))
	query = strings.ToLower(query)
	for _, skill := range m.skills.all {
		if query == "" || skillMatchesQuery(skill, query) {
			matches = append(matches, skill)
		}
	}
	m.skills.matches = matches
	if m.skills.selected >= len(m.skills.matches) {
		m.skills.selected = max(0, len(m.skills.matches)-1)
	}
}

func (m *model) refreshSkillSuggestions() {
	if m.runtime == nil {
		m.skills.all = nil
		return
	}
	views := m.runtime.SkillSuggestions()
	out := make([]skillSuggestion, 0, len(views))
	for _, view := range views {
		out = append(out, skillSuggestion{
			Name:          view.Name,
			Description:   view.Description,
			When:          view.When,
			SkillFilePath: view.SkillFilePath,
			Status:        string(view.Status),
			Reason:        view.Reason,
		})
	}
	m.skills.all = out
}

func skillQuery(raw string) (string, bool) {
	if strings.Contains(raw, "\n") {
		return "", false
	}
	trimmed := strings.TrimLeft(raw, " \t")
	if !strings.HasPrefix(trimmed, "$") {
		return "", false
	}
	if strings.ContainsAny(trimmed, " \t") {
		return "", false
	}
	return strings.TrimPrefix(trimmed, "$"), true
}

func skillMatchesQuery(skill skillSuggestion, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		skill.Name,
		skill.Description,
		skill.When,
	}, " "))
	return strings.Contains(haystack, query)
}

func (m model) hasSkillSuggestions() bool {
	return len(m.skills.matches) > 0
}

func (m model) renderSkillSuggestions() string {
	rows := []string{pickerTitle("Skills")}
	const maxRows = 8
	start := 0
	if len(m.skills.matches) > maxRows {
		start = m.skills.selected - maxRows/2
		if start < 0 {
			start = 0
		}
		if start > len(m.skills.matches)-maxRows {
			start = len(m.skills.matches) - maxRows
		}
	}
	end := len(m.skills.matches)
	if end > start+maxRows {
		end = start + maxRows
	}
	labelWidth := 0
	for i := start; i < end; i++ {
		labelWidth = max(labelWidth, lipgloss.Width("$"+m.skills.matches[i].Name))
	}
	labelWidth = min(labelWidth, 24)
	for i := start; i < end; i++ {
		skill := m.skills.matches[i]
		desc := strings.TrimSpace(skill.Description)
		if skill.Status == string(protocol.SkillAvailabilityNeedsSetup) && strings.TrimSpace(skill.Reason) != "" {
			desc = skill.Reason
		}
		rows = append(rows, pickerSuggestionRow("$"+skill.Name, desc, i == m.skills.selected, labelWidth))
	}
	rows = append(rows, pickerHint("  ↑/↓ navigate · Tab/Enter insert · Esc cancel"))
	return strings.Join(rows, "\n")
}

func (m *model) insertSelectedSkill() bool {
	if !m.hasSkillSuggestions() {
		return false
	}
	if m.skills.selected < 0 || m.skills.selected >= len(m.skills.matches) {
		return false
	}
	selected := m.skills.matches[m.skills.selected]
	name := strings.TrimSpace(selected.Name)
	if name == "" {
		return false
	}
	m.input.SetValue("$" + name + " ")
	if path := strings.TrimSpace(selected.SkillFilePath); path != "" {
		m.skillBinding = &protocol.SkillBinding{Name: name, SkillFilePath: path}
	} else {
		m.skillBinding = nil
	}
	m.skills.matches = nil
	m.skills.selected = 0
	m.resetHistoryNavigation()
	m.refreshViewportContent()
	return true
}

func (m *model) currentSkillBinding(value string) *protocol.SkillBinding {
	if m.skillBinding == nil {
		return nil
	}
	binding := *m.skillBinding
	name := strings.TrimSpace(binding.Name)
	if name == "" || strings.TrimSpace(binding.SkillFilePath) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	if strings.Contains(trimmed, "\n") {
		return nil
	}
	mention := "$" + name
	if trimmed == mention || strings.HasPrefix(trimmed, mention+" ") || strings.HasPrefix(trimmed, mention+"\t") {
		return &binding
	}
	return nil
}

type skillsMenuItem struct {
	Name        string
	Description string
}

func skillsMenuItems() []skillsMenuItem {
	return []skillsMenuItem{
		{Name: "List skills", Description: "Tip: press $ to open this list directly."},
		{Name: "Enable/Disable Skills", Description: "Enable or disable skills."},
	}
}

func (m *model) handleSkillsMenuKey(msg tea.KeyMsg) tea.Cmd {
	items := skillsMenuItems()
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeChat
		m.skillsMenu.selected = 0
		m.status = "ready"
	case "up", "k":
		m.skillsMenu.selected = wrapSelection(m.skillsMenu.selected, len(items), -1)
	case "down", "j":
		m.skillsMenu.selected = wrapSelection(m.skillsMenu.selected, len(items), 1)
	case "enter":
		switch m.skillsMenu.selected {
		case 0:
			m.openSkillsListFromMenu()
		case 1:
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentRequestSkillsManage})
		}
	}
	return nil
}

func (m *model) openSkillsListFromMenu() {
	m.mode = modeChat
	m.skillsMenu.selected = 0
	m.input.SetValue("$")
	m.skillBinding = nil
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	clearFileSuggestions(m)
	m.skills.matches = nil
	m.skills.selected = 0
	m.resetHistoryNavigation()
	m.updateSkillMatches()
	m.status = "ready"
	m.refreshViewportContent()
}

func (m model) renderSkillsMenu() string {
	rows := []string{
		pickerTitle("Skills"),
		pickerHint("Choose an action"),
		"",
	}
	for i, item := range skillsMenuItems() {
		rows = append(rows, renderSkillsMenuRow(item, i == m.skillsMenu.selected))
	}
	rows = append(rows, "", pickerHint("  ↑/↓ select · Enter confirm · Esc close"))
	return strings.Join(rows, "\n")
}

func renderSkillsMenuRow(item skillsMenuItem, selected bool) string {
	return pickerSuggestionRow(item.Name, item.Description, selected, 26)
}

func (m *model) setSkillsManagerItems(views []protocol.SkillView) {
	current := ""
	if m.skillsManager.selected >= 0 && m.skillsManager.selected < len(m.skillsManager.matches) {
		idx := m.skillsManager.matches[m.skillsManager.selected]
		if idx >= 0 && idx < len(m.skillsManager.all) {
			current = m.skillsManager.all[idx].Name
		}
	}
	items := make([]skillManagerItem, 0, len(views))
	for _, view := range views {
		enabled := view.Status != string(protocol.SkillAvailabilityDisabled) && view.Status != string(protocol.SkillAvailabilityProblem)
		desc := strings.TrimSpace(view.Description)
		if view.Status == string(protocol.SkillAvailabilityNeedsSetup) || view.Status == string(protocol.SkillAvailabilityDisabled) || view.Status == string(protocol.SkillAvailabilityProblem) {
			desc = strings.TrimSpace(view.Reason)
		}
		items = append(items, skillManagerItem{
			Name:                view.Name,
			Description:         desc,
			OriginalDescription: strings.TrimSpace(view.Description),
			Status:              view.Status,
			Reason:              view.Reason,
			Source:              view.Source,
			Enabled:             enabled,
			Toggleable:          view.Status != string(protocol.SkillAvailabilityProblem),
		})
	}
	m.skillsManager.all = items
	m.applySkillsManagerFilter()
	if current != "" {
		for visible, idx := range m.skillsManager.matches {
			if idx >= 0 && idx < len(m.skillsManager.all) && m.skillsManager.all[idx].Name == current {
				m.skillsManager.selected = visible
				return
			}
		}
	}
}

func (m *model) applySkillsManagerFilter() {
	query := strings.ToLower(strings.TrimSpace(m.skillsManager.query))
	matches := make([]int, 0, len(m.skillsManager.all))
	for i, item := range m.skillsManager.all {
		if query == "" || skillManagerItemMatches(item, query) {
			matches = append(matches, i)
		}
	}
	m.skillsManager.matches = matches
	if len(matches) == 0 {
		m.skillsManager.selected = 0
		return
	}
	if m.skillsManager.selected < 0 {
		m.skillsManager.selected = 0
	}
	if m.skillsManager.selected >= len(matches) {
		m.skillsManager.selected = len(matches) - 1
	}
}

func skillManagerItemMatches(item skillManagerItem, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		item.Name,
		item.Description,
		item.Reason,
		item.Source,
		item.Status,
	}, " "))
	return strings.Contains(haystack, query)
}

func (m *model) handleSkillsManagerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeChat
		m.skillsManager.query = ""
		m.skillsManager.matches = nil
		m.skillsManager.selected = 0
		m.status = "ready"
	case "up", "k":
		if len(m.skillsManager.matches) > 0 {
			m.skillsManager.selected = wrapSelection(m.skillsManager.selected, len(m.skillsManager.matches), -1)
		}
	case "down", "j":
		if len(m.skillsManager.matches) > 0 {
			m.skillsManager.selected = wrapSelection(m.skillsManager.selected, len(m.skillsManager.matches), 1)
		}
	case "backspace":
		if m.skillsManager.query != "" {
			m.skillsManager.query = strings.TrimSuffix(m.skillsManager.query, lastRuneString(m.skillsManager.query))
			m.applySkillsManagerFilter()
		}
	case " ", "enter":
		m.toggleSelectedManagedSkill()
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 && !msg.Alt {
			r := msg.Runes[0]
			if r >= 0x20 && r != 0x7f {
				m.skillsManager.query += string(r)
				m.applySkillsManagerFilter()
			}
		}
	}
	return nil
}

func lastRuneString(s string) string {
	if s == "" {
		return ""
	}
	var last rune
	for _, r := range s {
		last = r
	}
	return string(last)
}

func (m *model) toggleSelectedManagedSkill() {
	if m.skillsManager.selected < 0 || m.skillsManager.selected >= len(m.skillsManager.matches) {
		return
	}
	idx := m.skillsManager.matches[m.skillsManager.selected]
	if idx < 0 || idx >= len(m.skillsManager.all) {
		return
	}
	item := &m.skillsManager.all[idx]
	if !item.Toggleable {
		m.status = "skill unavailable"
		return
	}
	item.Enabled = !item.Enabled
	if item.Enabled {
		item.Status = string(protocol.SkillAvailabilityReady)
		if strings.TrimSpace(item.OriginalDescription) != "" {
			item.Description = item.OriginalDescription
		}
	} else {
		item.Status = string(protocol.SkillAvailabilityDisabled)
		item.Description = "Disabled in config"
	}
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetSkillEnabled, SkillName: item.Name, SkillEnabled: item.Enabled})
}

func (m model) renderSkillsManager() string {
	rows := []string{
		pickerTitle("Enable/Disable Skills"),
		pickerHint("Turn skills on or off. Changes are saved automatically."),
		"",
		pickerHint("Type to search skills"),
		renderSkillsManagerSearchLine(m.skillsManager.query),
	}
	const maxRows = 8
	if len(m.skillsManager.all) == 0 {
		rows = append(rows, pickerHint("  no skills found"))
	} else if len(m.skillsManager.matches) == 0 {
		rows = append(rows, pickerHint("  no matches"))
	} else {
		start := 0
		if len(m.skillsManager.matches) > maxRows {
			start = m.skillsManager.selected - maxRows/2
			if start < 0 {
				start = 0
			}
			if start > len(m.skillsManager.matches)-maxRows {
				start = len(m.skillsManager.matches) - maxRows
			}
		}
		end := len(m.skillsManager.matches)
		if end > start+maxRows {
			end = start + maxRows
		}
		for visible := start; visible < end; visible++ {
			item := m.skillsManager.all[m.skillsManager.matches[visible]]
			rows = append(rows, renderSkillsManagerRow(item, visible == m.skillsManager.selected))
		}
	}
	rows = append(rows, "", pickerHint("  ↑/↓ select · Space/Enter toggle · Esc close"))
	return strings.Join(rows, "\n")
}

func renderSkillsManagerSearchLine(query string) string {
	if strings.TrimSpace(query) == "" {
		return pickerSection("> ")
	}
	return pickerSection("> ") + pickerTone(query, "text")
}

func renderSkillsManagerRow(item skillManagerItem, selected bool) string {
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	selector := muted.Render("  ")
	if selected {
		selector = lipgloss.NewStyle().Foreground(tuitheme.Default.InfoSoft).Bold(true).Render("> ")
	}

	marker := " "
	markerStyle := muted
	if item.Enabled {
		marker = "x"
		markerStyle = lipgloss.NewStyle().Foreground(tuitheme.Default.Success).Bold(true)
	}
	if !item.Toggleable {
		marker = "!"
		markerStyle = lipgloss.NewStyle().Foreground(tuitheme.Default.Warn).Bold(true)
	}

	nameStyle := lipgloss.NewStyle()
	if selected {
		nameStyle = nameStyle.Foreground(tuitheme.Default.InfoSoft).Bold(true)
	}
	if !item.Toggleable {
		nameStyle = nameStyle.Foreground(tuitheme.Default.Warn)
	}

	head := selector +
		muted.Render("[") +
		markerStyle.Render(marker) +
		muted.Render("]") +
		" " +
		nameStyle.Render(item.Name)
	desc := strings.TrimSpace(item.Description)
	if desc == "" {
		return head
	}
	const descCol = 24
	gap := descCol - lipgloss.Width(head)
	if gap < 1 {
		gap = 1
	}
	return head + strings.Repeat(" ", gap) + muted.Render(desc)
}
