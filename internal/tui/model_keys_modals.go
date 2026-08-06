package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
)

func (m *model) handleApprovalKey(msg tea.KeyMsg) tea.Cmd {
	optionCount := m.approvalOptionCount()
	switch msg.String() {
	case "left", "h":
		m.approval.selected = (m.approval.selected + optionCount - 1) % optionCount
		return nil
	case "right", "l", "tab":
		m.approval.selected = (m.approval.selected + 1) % optionCount
		return nil
	case "enter":
		return m.submitSelectedApprovalDecision()
	case "a", "1":
		return m.submitApprovalDecision(protocol.IntentAllowTool, "approval_allow", "allow", "approved", "allow")
	case "s", "2":
		return m.submitApprovalDecision(protocol.IntentAllowToolForSession, "approval_allow_session", "allow for session", "approved for session", "allow_session")
	case "3":
		if m.approvalOptionCount() == 4 {
			return m.submitApprovalAndEnableAutoAccept()
		}
	case "d", "4":
		return m.submitApprovalDecision(protocol.IntentDenyTool, "approval_deny", "deny", "rejected", "deny")
	case "esc", "ctrl+c":
		return m.submitApprovalDecision(protocol.IntentCancelToolApproval, "approval_cancel", "cancel", "canceled", "cancel")
	}
	return nil
}

func (m *model) approvalOptionCount() int {
	if approvalWorkflowName(m.approval.metadata) != "" {
		return 4
	}
	return 3
}

func (m *model) submitSelectedApprovalDecision() tea.Cmd {
	switch m.approval.selected {
	case 0:
		return m.submitApprovalDecision(protocol.IntentAllowTool, "approval_allow", "allow", "approved", "allow")
	case 1:
		return m.submitApprovalDecision(protocol.IntentAllowToolForSession, "approval_allow_session", "allow for session", "approved for session", "allow_session")
	case 2:
		if m.approvalOptionCount() == 4 {
			return m.submitApprovalAndEnableAutoAccept()
		}
	}
	return m.submitApprovalDecision(protocol.IntentDenyTool, "approval_deny", "deny", "rejected", "deny")
}

func (m *model) submitApprovalAndEnableAutoAccept() tea.Cmd {
	toolCallID := m.approval.toolCallID
	toolName := m.approval.toolName
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentAllowToolForSession, ToolCallID: toolCallID})
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentEnableAutoAccept})
	m.addLog(logEntry{Kind: "approval_allow_session_auto", Source: toolName, Summary: "allow for session and enable auto mode", Raw: "allow_session_auto"})
	m.advanceApprovalPrompt("approved for session")
	return nil
}

func (m *model) submitApprovalDecision(kind protocol.IntentKind, logKind, summary, status, notice string) tea.Cmd {
	toolCallID := m.approval.toolCallID
	toolName := m.approval.toolName
	if kind == protocol.IntentCancelToolApproval {
		m.sawTerminalToolOutcomeThisTurn = true
	}
	m.dispatchIntent(protocol.Intent{Kind: kind, ToolCallID: toolCallID})
	m.addLog(logEntry{Kind: logKind, Source: toolName, Summary: summary, Raw: notice})
	m.advanceApprovalPrompt(status)
	return nil
}

func (m *model) advanceApprovalPrompt(status string) {
	if len(m.approvalQueue) == 0 {
		m.approval = approvalPromptState{}
		m.mode = modeChat
		m.status = status
		return
	}
	m.approval = m.approvalQueue[0]
	copy(m.approvalQueue, m.approvalQueue[1:])
	m.approvalQueue = m.approvalQueue[:len(m.approvalQueue)-1]
	m.approval.selected = 0
	m.mode = modeApproval
	m.status = "approval required"
}

func (m *model) handleSessionPickerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		if m.resumeMenu {
			return tea.Quit
		}
		m.mode = modeChat
	case "up", "k":
		m.sessionIndex = prevSessionChoiceIndex(m.sessionChoices, m.sessionIndex)
	case "down", "j":
		m.sessionIndex = nextSessionChoiceIndex(m.sessionChoices, m.sessionIndex)
	case "enter":
		selected := sessionChoiceNumberAt(m.sessionChoices, m.sessionIndex)
		if selected > 0 {
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSelectSession, SessionInput: strconv.Itoa(selected)})
		}
	}
	return nil
}

func (m *model) handleUserInputKey(msg tea.KeyMsg) tea.Cmd {
	if len(m.userInput.questions) == 0 {
		m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelUserInput, ToolCallID: m.userInput.toolCallID})
		m.mode = modeChat
		return nil
	}
	q := m.userInput.questions[m.userInput.index]

	// In "Other" free-text editing mode
	if m.userInput.editingOther {
		switch msg.String() {
		case "esc":
			// Always just exit the text field and return to options;
			// never interrupt the turn from here — m.busy is true
			// while request_user_input is waiting.
			m.userInput.editingOther = false
			m.userInput.otherInput.SetValue("")
			return nil
		case "enter":
			text := strings.TrimSpace(m.userInput.otherInput.Value())
			if text == "" {
				text = "Other"
			}
			m.userInput.answers = append(m.userInput.answers, protocol.UserInputAnswer{
				ID:      q.ID,
				Label:   "Other",
				Value:   text,
				IsOther: true,
			})
			m.userInput.editingOther = false
			m.userInput.otherInput.SetValue("")
			m.userInput.index++
			m.userInput.selectedOption = 0
			if m.userInput.index >= len(m.userInput.questions) {
				resp := protocol.UserInputResponse{Answers: m.userInput.answers}
				m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSubmitUserInput, ToolCallID: m.userInput.toolCallID, UserInput: &resp})
				m.mode = modeChat
			}
			return nil
		default:
			// Forward typing to the text input
			var cmd tea.Cmd
			m.userInput.otherInput, cmd = m.userInput.otherInput.Update(msg)
			return cmd
		}
	}

	totalOptions := len(q.Options) + 1 // +1 for "Other"
	switch msg.String() {
	case "esc":
		if m.busy {
			return m.interruptBusyTurn()
		}
		m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelUserInput, ToolCallID: m.userInput.toolCallID})
		m.mode = modeChat
	case "up", "k":
		if m.userInput.selectedOption > 0 {
			m.userInput.selectedOption--
		}
	case "down", "j":
		if m.userInput.selectedOption < totalOptions-1 {
			m.userInput.selectedOption++
		}
	case "enter":
		// "Other" selected → switch to free-text input
		if m.userInput.selectedOption == len(q.Options) {
			m.userInput.editingOther = true
			m.userInput.otherInput.Focus()
			return nil
		}
		opt := q.Options[m.userInput.selectedOption]
		m.userInput.answers = append(m.userInput.answers, protocol.UserInputAnswer{ID: q.ID, Label: opt.Label, Value: opt.Label})
		m.userInput.index++
		m.userInput.selectedOption = 0
		if m.userInput.index >= len(m.userInput.questions) {
			resp := protocol.UserInputResponse{Answers: m.userInput.answers}
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSubmitUserInput, ToolCallID: m.userInput.toolCallID, UserInput: &resp})
			m.mode = modeChat
		}
	}
	return nil
}

func (m *model) handleModelPickerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		if m.modelPicker.stage > 0 {
			m.modelPicker.stage--
		} else {
			m.mode = modeChat
		}
	case "up", "k":
		if m.modelPicker.stage == 0 {
			m.modelPicker.modelIx = wrapSelection(m.modelPicker.modelIx, len(m.modelPicker.models), -1)
		}
		if m.modelPicker.stage == 1 {
			m.modelPicker.effIx = wrapSelection(m.modelPicker.effIx, len(m.modelPicker.efforts), -1)
		}
		if m.modelPicker.stage == 2 {
			m.modelPicker.thinkIx = wrapSelection(m.modelPicker.thinkIx, len(m.modelPicker.thinkings), -1)
		}
	case "down", "j":
		if m.modelPicker.stage == 0 {
			m.modelPicker.modelIx = wrapSelection(m.modelPicker.modelIx, len(m.modelPicker.models), 1)
		}
		if m.modelPicker.stage == 1 {
			m.modelPicker.effIx = wrapSelection(m.modelPicker.effIx, len(m.modelPicker.efforts), 1)
		}
		if m.modelPicker.stage == 2 {
			m.modelPicker.thinkIx = wrapSelection(m.modelPicker.thinkIx, len(m.modelPicker.thinkings), 1)
		}
	case "enter":
		if m.modelPicker.stage == 0 {
			m.modelPicker.stage = 1
		} else if m.modelPicker.stage == 1 {
			m.modelPicker.stage = 2
		} else {
			modelName := safeChoice(m.modelPicker.models, m.modelPicker.modelIx)
			effort := safeChoice(m.modelPicker.efforts, m.modelPicker.effIx)
			thinking := safeChoice(m.modelPicker.thinkings, m.modelPicker.thinkIx)
			if modelName != "" && effort != "" && thinking != "" {
				m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetModelAndEffort, Model: modelName, Effort: effort, Thinking: thinking})
				m.model = modelName
				m.effort = effort
				m.thinking = thinking
			}
			m.mode = modeChat
		}
	}
	return nil
}

func (m *model) handlePermissionsMenuKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mode = modeChat
	case "up", "k", "left", "h":
		m.permissionsMenu.selected = wrapSelection(m.permissionsMenu.selected, 4, -1)
	case "down", "j", "right", "l", "tab":
		m.permissionsMenu.selected = wrapSelection(m.permissionsMenu.selected, 4, 1)
	case "enter":
		current := permissionsMode(m.autoAccept, m.autoReviewEnabled)
		switch m.permissionsMenu.selected {
		case 0: // Ask for approval
			if current != "ask" {
				m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetApprovalMode, ApprovalMode: "ask"})
			}
		case 1: // Auto-review
			if current != "auto-review" {
				m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetAutoReview, AutoReview: true})
			}
		case 2: // Auto-accept edits
			if current != "auto-accept" {
				m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetApprovalMode, ApprovalMode: "auto_accept"})
			}
		}
		m.mode = modeChat
	}
	return nil
}

func (m *model) handlePlanImplementationKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.declinePlanImplementation()
	case "up", "k", "left", "h":
		m.planImplementation.index = wrapSelection(m.planImplementation.index, 2, -1)
	case "down", "j", "right", "l", "tab":
		m.planImplementation.index = wrapSelection(m.planImplementation.index, 2, 1)
	case "enter":
		if m.localSubmitPending > 0 {
			m.status = "wait for command to finish"
			m.refreshViewportContent()
			return m.flushNativeScrollbackCmd()
		}
		if m.planImplementation.index == 0 {
			m.appendTranscript("you", tuirender.KindText, "Implement the plan.")
			m.beginTurnTranscript()
			m.startBusy()
			m.status = "running"
			m.chatMode = "agent"
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentImplementPlan})
			m.mode = modeChat
			m.refreshViewportContentFollow(true)
			return tea.Sequence(m.flushNativeScrollbackCmd(), busyTickCmd())
		}
		m.declinePlanImplementation()
	}
	return nil
}

func (m *model) declinePlanImplementation() {
	m.mode = modeChat
	m.status = "plan not approved"
	m.lastProposedPlan = ""
	m.sawPlanThisTurn = false
	m.sawPlanCompletedThisTurn = false
	m.deferredPlanPicker = false
	m.planImplementation.index = 0
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentDeclinePlan})
	m.refreshViewportContent()
}
