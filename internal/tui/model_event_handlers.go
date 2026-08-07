package tui

import (
	"fmt"
	"github.com/usewhale/whale/internal/runtime/protocol"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	tuirender "github.com/usewhale/whale/internal/tui/render"
)

func (m *model) handleAssistantDeltaEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.append("assistant", ev.Text)
	m.recordAssistantDelta(ev.Text)
	m.addLog(logEntry{Kind: "assistant_delta", Source: "assistant", Summary: ev.Text, Raw: ev.Text})
	if strings.TrimSpace(ev.Text) != "" {
		m.sawAssistantThisTurn = true
	}
	m.startBusy()
}

func (m *model) handleReasoningDeltaEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.append("think", ev.Text)
	m.recordModelOutputDelta(ev.Text)
	m.addLog(logEntry{Kind: "reasoning_delta", Source: "reasoning", Summary: ev.Text, Raw: ev.Text})
	if strings.TrimSpace(ev.Text) != "" {
		m.sawReasoningThisTurn = true
	}
}

func (m *model) handlePlanDeltaEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.appendPlanDelta(ev.Text)
	m.recordModelOutputDelta(ev.Text)
	m.addLog(logEntry{Kind: "plan_delta", Source: "plan", Summary: ev.Text, Raw: ev.Text})
	if strings.TrimSpace(ev.Text) != "" {
		m.sawPlanThisTurn = true
	}
}

func (m *model) handlePlanCompletedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	if strings.TrimSpace(ev.Text) != "" {
		m.lastProposedPlan = strings.TrimSpace(ev.Text)
		if m.assembler == nil {
			m.assembler = tuirender.NewAssembler()
		}
		m.sawPlanThisTurn = true
		// Only a completed plan opens the implementation gate. plan_delta alone
		// (which also fires for non-final tool-use-round preambles) must not, or a
		// turn that ends via a cap/forced summary without a real plan would still
		// prompt the user to approve investigation text. Set this BEFORE committing
		// so commitLiveTranscript's uncompleted-plan demotion does not strip the
		// real finalized plan card.
		m.sawPlanCompletedThisTurn = true
		m.assembler.SetPlan(ev.Text)
		m.commitLiveTranscript(false)
	}
	m.addLog(logEntry{Kind: "plan_completed", Source: "plan", Summary: truncateLine(ev.Text, 120), Raw: ev.Text})
}

func (m *model) handlePlanUpdateEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	if strings.TrimSpace(ev.Text) != "" {
		if m.assembler == nil {
			m.assembler = tuirender.NewAssembler()
		}
		m.assembler.AddPlanUpdate(ev.Text)
		m.refreshLiveViewportContent()
	}
	m.addLog(logEntry{Kind: "plan_update", Source: "plan", Summary: truncateLine(ev.Text, 120), Raw: ev.Text})
}

func (m *model) handleProviderRetryEvent(ev protocol.Event) {
	if ev.Metadata != nil && metadataBool(ev.Metadata["stream_reset"]) {
		m.resetLiveAttemptForProviderRetry()
	}
	m.setProviderRetryStatus(ev)
	m.addLog(logEntry{Kind: "api_retry", Source: "provider", Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleResponseResetEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.resetLiveAttemptForResponseReset()
	m.addLog(logEntry{Kind: "response_reset", Source: "assistant", Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleInfoEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	if !isEnvironmentInventoryBlock(ev.Text) {
		if ev.LocalResult != nil {
			m.appendLocalResult(ev.LocalResult)
		} else if notice := permissionNoticeFromInfo(ev.Text); notice != nil {
			m.appendSystemNotice(notice)
		} else if isSessionNotice(ev.Text) {
			m.appendTranscript("notice", tuirender.KindNotice, ev.Text)
		} else {
			m.append("info", ev.Text)
		}
	} else {
		m.addLog(logEntry{
			Kind:    "env_summary",
			Source:  "system",
			Summary: "environment summary captured",
			Raw:     ev.Text,
		})
	}
	m.addLog(logEntry{Kind: "info", Source: "system", Summary: ev.Text, Raw: ev.Text})
	m.status = "ready"
	m.syncModelEffortFromLocalResult(ev.LocalResult)
	m.syncModelEffortFromInfo(ev.Text)
	m.refreshViewportContentFollow(true)
}

func (m *model) handleErrorEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.append("error", ev.Text)
	m.addLog(logEntry{Kind: "error", Source: "system", Summary: ev.Text, Raw: ev.Text})
	m.status = "error"
}

func (m *model) handleLocalSubmitResultEvent(ev protocol.Event) tea.Cmd {
	m.clearProviderRetryStatus()
	role := ev.Status
	if role == "" {
		role = "info"
	}
	if m.handleConfigManagerSubmitResult(ev) {
		m.addLog(logEntry{Kind: role, Source: "system", Summary: ev.Text, Raw: ev.Text})
		if role == "error" {
			m.status = "error"
		}
		return nil
	}
	if !isEnvironmentInventoryBlock(ev.Text) {
		m.appendLocalCommandEcho(m.popLocalSubmitCommand())
		if ev.LocalResult != nil && ev.LocalResult.Kind == "workflow-launch" {
			m.sawTerminalToolOutcomeThisTurn = true
			m.removeNoFinalAnswerStatusMessages()
			m.addLog(logEntry{Kind: role, Source: "system", Summary: ev.Text, Raw: ev.Text})
			return m.openWorkflowLaunch(ev.LocalResult)
		}
		if ev.LocalResult != nil && ev.LocalResult.Kind == "workflow-run" {
			m.sawTerminalToolOutcomeThisTurn = true
			m.removeNoFinalAnswerStatusMessages()
			// Workflow run lifecycle is rendered from workflow_snapshot events.
		} else if shouldOpenWorkflowPanelForLocalResult(ev.LocalResult) {
			m.sawTerminalToolOutcomeThisTurn = true
			m.removeNoFinalAnswerStatusMessages()
			m.addLog(logEntry{Kind: role, Source: "system", Summary: ev.Text, Raw: ev.Text})
			return m.openWorkflowPanel(ev.LocalResult)
		} else {
			m.appendLocalSubmitResult(role, ev.Text, ev.LocalResult)
		}
	} else {
		m.addLog(logEntry{
			Kind:    "env_summary",
			Source:  "system",
			Summary: "environment summary captured",
			Raw:     ev.Text,
		})
	}
	m.addLog(logEntry{Kind: role, Source: "system", Summary: ev.Text, Raw: ev.Text})
	if role == "error" {
		m.status = "error"
	}
	if role == "info" {
		m.syncModelEffortFromLocalResult(ev.LocalResult)
		m.syncModelEffortFromInfo(ev.Text)
		m.refreshViewportContentFollow(true)
	}
	return nil
}

func (m *model) handleWorkflowTerminalEvent(ev protocol.Event) {
	m.handleWorkflowResultEvent(ev)
}

func (m *model) handleWorkflowResultEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.sawTerminalToolOutcomeThisTurn = true
	m.removeNoFinalAnswerStatusMessages()
	m.ingestTimelineEvent(ev)
	if !m.hasPendingLifecycleItems() {
		m.commitLiveTranscript(false)
	} else {
		m.refreshLiveViewportContent()
	}
	m.addLog(logEntry{Kind: "workflow_result", Source: "workflow", Summary: truncateLine(ev.Text, 120), Raw: ev.Text})
	m.status = "ready"
	m.refreshViewportContentFollow(true)
}

func (m *model) handleWorkflowSnapshotEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	if m.mode == modeWorkflowPanel {
		if ev.LocalResult != nil {
			m.workflowPanel.result = ev.LocalResult
			if runID := workflowPanelRunID(ev.LocalResult); runID != "" {
				m.workflowPanel.runID = runID
			}
			m.clampWorkflowPanelSelection()
			m.clampWorkflowPanelSnapshotSelection()
		}
		m.status = "workflows"
		return
	}
	if m.sawTerminalToolOutcomeThisTurn {
		m.removeNoFinalAnswerStatusMessages()
	}
	m.addLog(logEntry{Kind: "workflow_snapshot", Source: "workflow", Summary: truncateLine(ev.Text, 120), Raw: ev.Text})
	m.status = "workflow"
	m.refreshViewportContentFollow(false)
}

func (m *model) handleDiffResultEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.appendLocalCommandEcho(m.popLocalSubmitCommand())
	m.setDiffText(ev.Text)
	m.page = pageDiff
	m.status = "diff"
	m.refreshViewportContentFollow(true)
	m.viewport.GotoTop()
}

func (m *model) handleBtwStartedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.startBtwPanel(ev.Count, ev.Text)
	m.refreshViewportContent()
}

func (m *model) handleBtwDeltaEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.appendBtwDelta(ev.Count, ev.Text)
	m.refreshViewportContent()
}

func (m *model) handleBtwDoneEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.finishBtwPanel(ev.Count, ev.Text)
	m.refreshViewportContent()
}

func (m *model) handleBtwErrorEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.failBtwPanel(ev.Count, ev.Text)
	m.refreshViewportContent()
}

func (m *model) handleToolCallEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	if ev.ToolName == "workflow" {
		m.sawTerminalToolOutcomeThisTurn = true
	}
	if ev.ToolName != "update_plan" {
		m.ingestTimelineEvent(ev)
		m.refreshLiveViewportContent()
	}
	m.addLog(logEntry{
		Kind:    "tool_call",
		Source:  ev.ToolName,
		Summary: fmt.Sprintf("%s (id=%s)", ev.Text, ev.ToolCallID),
		Raw:     fmt.Sprintf("id=%s\ninput=%s", ev.ToolCallID, ev.Text),
	})
}

func (m *model) handleToolResultEvent(ev protocol.Event) tea.Cmd {
	m.clearProviderRetryStatus()
	if auditOnlyToolResultEvent(ev) {
		role, _ := summarizeToolResultStructured(ev.ToolName, ev.Text, ev.ToolOutcome, ev.ToolCode, ev.ToolPayload)
		if ev.ToolName == "workflow" || suppressesNoFinalAnswer(role) {
			m.sawTerminalToolOutcomeThisTurn = true
			m.removeNoFinalAnswerStatusMessages()
		}
		if ev.ToolName != "update_plan" {
			m.ingestTimelineEvent(ev)
		}
		m.addLog(logEntry{Kind: "tool_result_audit", Source: ev.ToolName, Summary: truncateLine(ev.Text, 120), Raw: ev.Text})
		m.captureDiffMetadata(ev.ToolName, ev.Metadata)
		m.refreshLiveViewportContent()
		return nil
	}
	role, _ := summarizeToolResultStructured(ev.ToolName, ev.Text, ev.ToolOutcome, ev.ToolCode, ev.ToolPayload)
	if ev.ToolName == "workflow" || suppressesNoFinalAnswer(role) {
		m.sawTerminalToolOutcomeThisTurn = true
	}
	if ev.ToolName != "update_plan" {
		m.ingestTimelineEvent(ev)
	}
	m.addLog(logEntry{Kind: "tool_result", Source: ev.ToolName, Summary: truncateLine(ev.Text, 120), Raw: ev.Text})
	m.captureDiffMetadata(ev.ToolName, ev.Metadata)
	m.captureDiff(ev.ToolName, ev.Text)
	if !m.hasPendingLifecycleItems() {
		m.commitLiveTranscript(false)
	} else {
		m.refreshLiveViewportContent()
	}
	if toolResultMayChangeGitBranch(ev.ToolName) {
		return detectGitBranchCmd(m.cwdPath)
	}
	return nil
}

func auditOnlyToolResultEvent(ev protocol.Event) bool {
	if ev.Metadata == nil {
		return false
	}
	visibility, _ := ev.Metadata["ui_visibility"].(string)
	return strings.TrimSpace(visibility) == "audit"
}

func (m *model) handleHookStartedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.ingestTimelineEvent(ev)
	m.status = ev.Text
	m.refreshLiveViewportContent()
	m.addLog(logEntry{Kind: "hook_started", Source: hookLogSource(ev), Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Hook)})
}

func (m *model) handleHookCompletedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.ingestTimelineEvent(ev)
	m.status = ev.Text
	if !m.hasPendingLifecycleItems() {
		m.commitLiveTranscript(false)
	} else {
		m.refreshLiveViewportContent()
	}
	m.addLog(logEntry{Kind: "hook_completed", Source: hookLogSource(ev), Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Hook)})
}

func hookLogSource(ev protocol.Event) string {
	if ev.Hook == nil {
		return "hook"
	}
	if strings.TrimSpace(ev.Hook.Event) != "" {
		return ev.Hook.Event
	}
	return "hook"
}

func (m *model) handleTaskStartedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.status = ev.Text
	m.addLog(logEntry{Kind: "task_started", Source: ev.ToolName, Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleTaskProgressEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.status = ev.Text
	if ev.ToolName != "update_plan" {
		m.ingestTimelineEvent(ev)
		m.refreshLiveViewportContent()
	}
	m.addLog(logEntry{Kind: "task_progress", Source: ev.ToolName, Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleTaskCompletedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.status = ev.Text
	m.addLog(logEntry{Kind: "task_completed", Source: ev.ToolName, Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleMCPStatusEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.status = ev.Text
	m.addLog(logEntry{Kind: "mcp_status", Source: "mcp", Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleMCPCompleteEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.status = ev.Text
	m.addLog(logEntry{Kind: "mcp_complete", Source: "mcp", Summary: ev.Text, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleApprovalRequiredEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.ingestTimelineEvent(ev)
	m.refreshLiveViewportContent()
	if m.stopping {
		if ev.ToolCallID != "" {
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelToolApproval, ToolCallID: ev.ToolCallID})
		}
		m.addLog(logEntry{Kind: "approval_required_stale", Source: ev.ToolName, Summary: ev.Text, Raw: ev.Text})
		return
	}
	prompt := approvalPromptState{
		toolCallID: ev.ToolCallID,
		toolName:   ev.ToolName,
		reason:     ev.Text,
		metadata:   ev.Metadata,
		selected:   0,
	}
	m.addLog(logEntry{Kind: "approval_required", Source: ev.ToolName, Summary: ev.Text, Raw: ev.Text})
	if m.mode == modeApproval && m.approval.toolCallID != "" {
		m.approvalQueue = append(m.approvalQueue, prompt)
		m.status = "approval required"
		return
	}
	m.mode = modeApproval
	m.approval = prompt
	m.status = "approval required"
	// Desktop notification for approval request (only if user is idle).
	if m.notifier != nil && time.Since(m.lastUserInput) > 6*time.Second {
		m.notifier.SendApprovalRequired(ev.ToolName, ev.Text)
	}
}

func (m *model) handleApprovalDecisionEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.ingestTimelineEvent(ev)
	if ev.Decision == "cancel" || ev.Decision == "deny" {
		m.sawTerminalToolOutcomeThisTurn = true
	}
	if !m.hasPendingLifecycleItems() {
		m.commitLiveTranscript(false)
		return
	}
	m.refreshLiveViewportContent()
}

func (m *model) handleUserInputRequiredEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.ingestTimelineEvent(ev)
	m.refreshLiveViewportContent()
	if m.stopping {
		if ev.ToolCallID != "" {
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelUserInput, ToolCallID: ev.ToolCallID})
		}
		m.addLog(logEntry{Kind: "user_input_required_stale", Source: ev.ToolName, Summary: fmt.Sprintf("%d questions", len(ev.Questions)), Raw: fmt.Sprintf("%+v", ev.Questions)})
		return
	}
	m.mode = modeUserInput
	m.userInput.toolCallID = ev.ToolCallID
	m.userInput.toolName = ev.ToolName
	m.userInput.questions = ev.Questions
	m.userInput.index = 0
	m.userInput.selectedOption = 0
	m.userInput.editingOther = false
	m.userInput.otherInput.SetValue("")
	m.userInput.answers = nil
	m.addLog(logEntry{Kind: "user_input_required", Source: ev.ToolName, Summary: fmt.Sprintf("%d questions", len(ev.Questions)), Raw: fmt.Sprintf("%+v", ev.Questions)})
	m.status = "user input required"
}

func (m *model) handleUserInputDoneEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.ingestTimelineEvent(ev)
	if !m.hasPendingLifecycleItems() {
		m.commitLiveTranscript(false)
		return
	}
	m.refreshLiveViewportContent()
	m.addLog(logEntry{Kind: "user_input_done", Source: ev.ToolName, Summary: ev.Status, Raw: fmt.Sprintf("%+v", ev.Metadata)})
}

func (m *model) handleSessionsListedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.mode = modeSessionPicker
	m.sessionChoices = ev.Choices
	m.sessionIndex = firstSessionChoiceIndex(ev.Choices)
	m.addLog(logEntry{Kind: "sessions_listed", Source: "session", Summary: fmt.Sprintf("%d sessions", len(ev.Choices)), Raw: strings.Join(ev.Choices, "\n")})
	m.status = "session picker"
}

func (m *model) handleModelPickerEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.stopBusy()
	m.stopping = false
	m.mode = modeModelPicker
	m.modelPicker.stage = 0
	m.modelPicker.models = ev.ModelChoices
	m.modelPicker.efforts = ev.EffortChoices
	m.modelPicker.thinkings = ev.ThinkingChoices
	m.modelPicker.modelIx = indexOf(ev.ModelChoices, ev.CurrentModel)
	m.modelPicker.effIx = indexOf(ev.EffortChoices, ev.CurrentEffort)
	m.modelPicker.thinkIx = indexOf(ev.ThinkingChoices, ev.CurrentThinking)
}

func (m *model) handlePermissionsMenuEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.stopBusy()
	m.stopping = false
	m.mode = modePermissionsMenu
	switch permissionsMode(ev.AutoAccept, ev.AutoReview) {
	case "auto-review":
		m.permissionsMenu.selected = 1
	case "auto-accept":
		m.permissionsMenu.selected = 2
	default:
		m.permissionsMenu.selected = 0
	}
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	m.skills.matches = nil
	m.skills.selected = 0
	m.status = "permissions"
}

func (m *model) handleSkillLoadedEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.addLog(logEntry{Kind: "skill_loaded", Source: "skills", Summary: ev.Text, Raw: ev.Text})
	m.status = ev.Text
}

func (m *model) handleSkillsMenuEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.stopBusy()
	m.stopping = false
	m.mode = modeSkillsMenu
	m.skillsMenu.selected = 0
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	m.skills.matches = nil
	m.skills.selected = 0
	m.status = "skills"
}

func (m *model) handleSkillsManagerEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.stopBusy()
	m.stopping = false
	m.mode = modeSkillsManager
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	m.skills.matches = nil
	m.skills.selected = 0
	m.setSkillsManagerItems(ev.Skills)
	m.status = "skills"
}

func (m *model) handlePluginsManagerEvent(ev protocol.Event) {
	m.setPluginsManagerItems(ev.Plugins)
	if !ev.Open && m.mode != modePluginsManager {
		return
	}
	m.clearProviderRetryStatus()
	m.stopBusy()
	m.stopping = false
	m.mode = modePluginsManager
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	m.skills.matches = nil
	m.skills.selected = 0
	m.status = "plugins"
}

func (m *model) handleReviewMenuEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	m.stopBusy()
	m.stopping = false
	m.mode = modeReviewMenu
	m.reviewMenu.selected = 0
	m.reviewTargetPicker = reviewTargetPickerState{}
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	m.skills.matches = nil
	m.skills.selected = 0
	m.status = "review"
}

func (m *model) handleViewModeChangedEvent(ev protocol.Event) tea.Cmd {
	m.clearProviderRetryStatus()
	mode := strings.TrimSpace(ev.ViewMode)
	if mode == "" {
		mode = strings.TrimSpace(ev.Text)
		switch mode {
		case protocol.ViewModeToggleMessage(protocol.ViewModeFocus):
			mode = protocol.ViewModeFocus
		case protocol.ViewModeToggleMessage(protocol.ViewModeDefault):
			mode = protocol.ViewModeDefault
		default:
			mode = strings.TrimPrefix(mode, "view:")
			mode = strings.TrimSpace(mode)
		}
	}
	if mode == "" {
		mode = protocol.ViewModeDefault
	}
	m.viewMode = mode
	if strings.TrimSpace(ev.Text) != "" {
		m.setEphemeralInfo(ev.Text)
	}
	m.status = "ready"
	return m.redrawTranscriptForFocusToggleCmd()
}

func (m *model) handleWorktreeExitPromptEvent(ev protocol.Event) {
	m.clearProviderRetryStatus()
	if ev.WorktreeExit != nil {
		m.worktreeExit.summary = *ev.WorktreeExit
		m.worktreeExit.selected = 0
		m.mode = modeWorktreeExit
		m.status = "worktree exit"
	}
}

func (m *model) handleClearScreenEvent() tea.Cmd {
	m.clearProviderRetryStatus()
	m.assembler.Reset()
	m.resetTimeline()
	m.ephemeralMessages = nil
	m.resetTranscript()
	m.resetTurnVisibility()
	m.logs = nil
	m.diffs = nil
	m.status = "terminal cleared"
	return tea.Sequence(clearScreenCmd(), m.startupHeaderPrintCmd(), waitEventCmd(m.runtime))
}

func (m *model) handleSessionHydratedEvent(ev protocol.Event) tea.Cmd {
	m.setPluginSlashCommands(ev.Plugins)
	preserveHooksStartupReview := m.mode == modeHooksStartupReview || (m.mode == modeHooksManager && m.hooksManager.startupReviewOpen)
	m.mode = modeChat
	m.resumeMenu = false
	prevSessionID := m.sessionID
	if strings.TrimSpace(ev.SessionID) != "" {
		m.sessionID = strings.TrimSpace(ev.SessionID)
	}
	sessionChanged := prevSessionID != "" && m.sessionID != "" && m.sessionID != prevSessionID
	hadStartupHeaderPrinted := m.startupHeaderPrinted || (m.startupHeaderOnce != nil && *m.startupHeaderOnce)
	m.clearProviderRetryStatus()
	m.assembler.Reset()
	m.resetTimeline()
	m.ephemeralMessages = nil
	m.resetTranscript()
	m.resetTurnVisibility()
	m.logs = nil
	m.diffs = nil
	m.hydrateSessionMessages(ev.Messages)
	m.commitLiveTranscript(true)
	m.trimHydratedTranscriptForDisplay(maxHydratedTranscriptLines)
	var eventCmd tea.Cmd
	if sessionChanged {
		hadStartupHeaderPrinted = false
		eventCmd = clearScreenCmd()
	}
	if len(m.transcript) > 0 || hadStartupHeaderPrinted {
		m.startupHeaderPrinted = true
		if m.startupHeaderOnce == nil {
			m.startupHeaderOnce = new(bool)
		}
		*m.startupHeaderOnce = true
	}
	m.status = "ready"
	if preserveHooksStartupReview {
		m.mode = modeHooksStartupReview
	}
	return eventCmd
}

func (m *model) handleExitRequestedEvent() {
	m.clearProviderRetryStatus()
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentShutdown, InterruptSource: "exit_requested"})
}
