package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

type reviewMenuItem struct {
	Name        string
	Description string
	Action      string
	Prefill     string
	Picker      string
}

func reviewMenuItems() []reviewMenuItem {
	return []reviewMenuItem{
		{Name: "Local changes", Description: "Review staged, unstaged, and relevant untracked files.", Action: "/review local"},
		{Name: "Branch...", Description: "Choose a base branch; first option is vs default branch.", Picker: "branch"},
		{Name: "Pull request...", Description: "Choose an open GitHub PR or enter a number/URL.", Picker: "pr"},
		{Name: "Commit...", Description: "Choose a recent commit or enter a SHA.", Picker: "commit"},
		{Name: "Custom instructions...", Description: "Describe exactly what to review.", Prefill: "/review "},
	}
}

func (m *model) handleReviewMenuKey(msg tea.KeyMsg) tea.Cmd {
	items := reviewMenuItems()
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeReviewMenu()
	case "up", "k":
		m.reviewMenu.selected = wrapSelection(m.reviewMenu.selected, len(items), -1)
	case "down", "j":
		m.reviewMenu.selected = wrapSelection(m.reviewMenu.selected, len(items), 1)
	case "enter":
		if m.reviewMenu.selected < 0 || m.reviewMenu.selected >= len(items) {
			return nil
		}
		item := items[m.reviewMenu.selected]
		if item.Action != "" {
			m.closeReviewMenu()
			return m.submitPrompt(item.Action)
		}
		if item.Picker != "" {
			return m.openReviewTargetPicker(item.Picker)
		}
		if item.Prefill != "" {
			m.closeReviewMenu()
			m.input.SetValue(item.Prefill)
			m.skillBinding = nil
			m.resetHistoryNavigation()
			cmd := m.updateSlashMatches()
			m.refreshViewportContent()
			return cmd
		}
	}
	return nil
}

func (m *model) closeReviewMenu() {
	m.mode = modeChat
	m.reviewMenu.selected = 0
	m.status = "ready"
}
