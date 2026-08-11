package tui

import (
	"github.com/charmbracelet/lipgloss"
	"strings"
)

func (m model) pageLabel() string {
	if m.page == pageLogs {
		return "logs"
	}
	if m.page == pageDiff {
		return "diff"
	}
	return "chat"
}

func (m model) renderPalette() string {
	rows := []string{
		pickerTitle("Command Palette"),
		pickerHint("(enter to run, esc to close)"),
	}
	for i, it := range m.palette.actions {
		rows = append(rows, pickerRow(it.Label, i == m.palette.selected, false))
	}
	return strings.Join(rows, "\n")
}

func (m model) renderModelPicker() string {
	rows := []string{pickerTitle("Select Model and Effort")}
	rows = append(rows, "")
	rows = append(rows, pickerSection("Model:"))
	for i, item := range m.modelPicker.models {
		rows = append(rows, pickerRow(item, m.modelPicker.stage == 0 && i == m.modelPicker.modelIx, false))
	}
	if m.modelPicker.stage >= 1 {
		rows = append(rows, "")
		rows = append(rows, pickerSection("Effort:"))
		for i, item := range m.modelPicker.efforts {
			rows = append(rows, pickerRow(item, m.modelPicker.stage == 1 && i == m.modelPicker.effIx, false))
		}
	}
	if m.modelPicker.stage >= 2 {
		rows = append(rows, "", pickerSection("Thinking:"))
		for i, item := range m.modelPicker.thinkings {
			rows = append(rows, pickerRow(item, m.modelPicker.stage == 2 && i == m.modelPicker.thinkIx, false))
		}
	}
	rows = append(rows, "", pickerHint("(up/down choose, enter next/confirm, esc back)"))
	return strings.Join(rows, "\n")
}

func (m model) renderPermissionsMenu() string {
	rows := []string{
		pickerTitle("Permissions"),
		"",
	}
	askLabel := "Ask for approval"
	autoAcceptLabel := "Auto-accept edits"
	autoReviewLabel := "Auto-review"
	current := permissionsMode(m.autoAccept, m.autoReviewEnabled)
	items := []struct {
		label string
		mode  string
	}{
		{label: currentLabel(askLabel, current == "ask"), mode: "ask"},
		{label: currentLabel(autoReviewLabel, current == "auto-review"), mode: "auto-review"},
		{label: currentLabel(autoAcceptLabel, current == "auto-accept"), mode: "auto-accept"},
	}
	for i, item := range items {
		rows = append(rows, pickerRow(item.label, i == m.permissionsMenu.selected, false))
	}
	rows = append(rows, "", pickerRow("cancel", m.permissionsMenu.selected == 3, true), "", pickerHint("(up/down choose, enter confirm, esc cancel)"))
	return strings.Join(rows, "\n")
}

func permissionsMode(autoAccept, autoReview bool) string {
	if autoReview {
		return "auto-review"
	}
	if autoAccept {
		return "auto-accept"
	}
	return "ask"
}

// nextPermissionsMode returns the permission mode reached by cycling forward
// with the Alt+P shortcut (ask -> auto-review -> auto-accept -> ask).
func nextPermissionsMode(autoAccept, autoReview bool) string {
	switch permissionsMode(autoAccept, autoReview) {
	case "auto-review":
		return "auto-accept"
	case "auto-accept":
		return "ask"
	default:
		return "auto-review"
	}
}

func currentLabel(label string, current bool) string {
	if current {
		return label + " (current)"
	}
	return label
}

func (m model) renderSessionPicker() string {
	rows := []string{
		pickerTitle("sessions"),
		pickerHint("(up/down choose, enter confirm, esc cancel)"),
	}
	for i, row := range m.sessionChoices {
		if isSessionHeaderRow(row) {
			continue
		}
		if strings.Contains(row, "Updated") && strings.Contains(row, "Conversation") {
			rows = append(rows, pickerSection("  #   Updated   Branch                    Conversation"))
			continue
		}
		selected := i == m.sessionIndex
		if choice, ok := parseSessionChoiceDisplay(row); ok {
			rows = append(rows, pickerSessionChoiceRow(choice, selected))
			continue
		}
		rows = append(rows, pickerRow(displaySessionChoiceRow(row), selected, false))
	}
	return strings.Join(rows, "\n")
}

func (m model) renderUserInputPicker() string {
	if m.userInput.index >= len(m.userInput.questions) {
		return ""
	}
	q := m.userInput.questions[m.userInput.index]
	rows := make([]string, 0, len(q.Options)+5)
	rows = append(rows, pickerTitle(q.Question), "")

	if m.userInput.editingOther {
		rows = append(rows, "Other — type your answer:", "")
		rows = append(rows, m.userInput.otherInput.View())
		rows = append(rows, "", pickerHint("(enter submit, esc cancel)"))
		return strings.Join(rows, "\n")
	}

	labelWidth := 0
	for _, opt := range q.Options {
		labelWidth = max(labelWidth, lipgloss.Width(opt.Label))
	}
	labelWidth = min(labelWidth, 24)
	for i, opt := range q.Options {
		rows = append(rows, pickerInlineDescriptionRow(opt.Label, opt.Description, i == m.userInput.selectedOption, labelWidth))
	}
	// "None of the above" row
	otherSelected := m.userInput.selectedOption == len(q.Options)
	rows = append(rows, pickerInlineDescriptionRow("Other", "None of the above — type your own answer", otherSelected, labelWidth))
	rows = append(rows, "", pickerHint("(up/down choose, enter confirm, esc cancel)"))
	return strings.Join(rows, "\n")
}

func (m model) renderPlanImplementationPicker() string {
	rows := []string{pickerTitle("Implement this plan?"), ""}
	items := []struct {
		label string
	}{
		{"Yes, implement this plan"},
		{"No, stay in Plan mode"},
	}
	for i, item := range items {
		rows = append(rows, pickerRow(item.label, i == m.planImplementation.index, false))
	}
	rows = append(rows, "", pickerHint("(up/down choose, enter confirm, esc cancel)"))
	return strings.Join(rows, "\n")
}
