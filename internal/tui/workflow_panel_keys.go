package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usewhale/whale/internal/runtime/protocol"
)

func (m *model) handleWorkflowPanelKey(msg tea.KeyMsg) tea.Cmd {
	if m.workflowPanel.detail && workflowPanelHasSnapshot(m.workflowPanel.result) {
		return m.handleWorkflowPanelDetailKey(msg)
	}
	switch msg.String() {
	case "esc", "q":
		m.mode = modeChat
		m.status = "ready"
		m.workflowPanel.tickID++
		return m.flushNativeScrollbackCmd()
	case "up", "k":
		if workflowPanelHasSnapshot(m.workflowPanel.result) {
			m.moveWorkflowPanelSnapshotSelection(-1)
		} else if runs := workflowPanelRunSections(m.workflowPanel.result); len(runs) > 0 {
			m.workflowPanel.selected = wrapSelection(m.workflowPanel.selected, len(runs), -1)
		}
	case "down", "j":
		if workflowPanelHasSnapshot(m.workflowPanel.result) {
			m.moveWorkflowPanelSnapshotSelection(1)
		} else if runs := workflowPanelRunSections(m.workflowPanel.result); len(runs) > 0 {
			m.workflowPanel.selected = wrapSelection(m.workflowPanel.selected, len(runs), 1)
		}
	case "tab", "right", "l":
		if workflowPanelHasSnapshot(m.workflowPanel.result) {
			m.workflowPanel.focus = workflowPanelFocusTask
			m.clampWorkflowPanelSnapshotSelection()
		} else if runs := workflowPanelRunSections(m.workflowPanel.result); msg.String() == "tab" && len(runs) > 0 {
			m.workflowPanel.selected = wrapSelection(m.workflowPanel.selected, len(runs), 1)
		}
	case "enter":
		if workflowPanelHasSnapshot(m.workflowPanel.result) {
			if m.workflowPanel.focus == workflowPanelFocusTask {
				m.workflowPanel.detail = true
				m.workflowPanel.detailRight = false
				m.workflowPanel.detailSection = workflowPanelDetailPrompt
				m.workflowPanel.detailExpanded = false
				m.workflowPanel.detailScroll = 0
			} else {
				m.workflowPanel.focus = workflowPanelFocusTask
			}
		} else if runID := m.selectedWorkflowPanelRunID(); runID != "" && m.workflowPanel.result != nil && m.workflowPanel.result.Kind == "workflows" {
			m.workflowPanel.runID = runID
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentRequestWorkflowPanel, WorkflowRunID: runID})
		}
	case "backspace", "left", "h":
		if workflowPanelHasSnapshot(m.workflowPanel.result) && m.workflowPanel.focus == workflowPanelFocusTask {
			m.workflowPanel.focus = workflowPanelFocusPhase
		} else if m.workflowPanel.result != nil && m.workflowPanel.result.Kind == "workflow" {
			m.workflowPanel.runID = ""
			m.workflowPanel.selectedPhase = 0
			m.workflowPanel.selectedTask = 0
			m.workflowPanel.focus = workflowPanelFocusPhase
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentRequestWorkflowPanel})
		}
	case "x":
		if runID := m.selectedWorkflowPanelRunID(); runID != "" {
			m.status = "stopping workflow"
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelWorkflowRun, WorkflowRunID: runID})
		}
	}
	return nil
}

func (m *model) handleWorkflowPanelDetailKey(msg tea.KeyMsg) tea.Cmd {
	if m.workflowPanel.detailExpanded {
		switch msg.String() {
		case "esc", "q", "enter":
			m.workflowPanel.detailExpanded = false
			m.workflowPanel.detailScroll = 0
			return nil
		case "up", "k":
			m.workflowPanel.detailScroll--
			if m.workflowPanel.detailScroll < 0 {
				m.workflowPanel.detailScroll = 0
			}
			return nil
		case "down", "j":
			m.workflowPanel.detailScroll++
			return nil
		case "pgdown", "ctrl+n":
			m.workflowPanel.detailScroll += 10
			return nil
		case "pgup", "ctrl+p":
			m.workflowPanel.detailScroll -= 10
			if m.workflowPanel.detailScroll < 0 {
				m.workflowPanel.detailScroll = 0
			}
			return nil
		}
	}
	switch msg.String() {
	case "esc", "q":
		m.mode = modeChat
		m.status = "ready"
		m.workflowPanel.tickID++
		return m.flushNativeScrollbackCmd()
	case "backspace", "left", "h":
		m.workflowPanel.detail = false
		m.workflowPanel.detailRight = false
		m.workflowPanel.detailExpanded = false
		m.workflowPanel.detailScroll = 0
	case "tab", "right", "l":
		m.workflowPanel.detailRight = true
	case "up", "k":
		if m.workflowPanel.detailRight {
			m.moveWorkflowPanelDetailSection(-1)
		} else {
			m.workflowPanel.selectedTask--
			m.clampWorkflowPanelSnapshotSelection()
		}
	case "down", "j":
		if m.workflowPanel.detailRight {
			m.moveWorkflowPanelDetailSection(1)
		} else {
			m.workflowPanel.selectedTask++
			m.clampWorkflowPanelSnapshotSelection()
		}
	case "enter":
		if !m.workflowPanel.detailRight {
			m.workflowPanel.detailRight = true
			return nil
		}
		switch m.workflowPanel.detailSection {
		case workflowPanelDetailPrompt, workflowPanelDetailOutcome:
			if m.workflowPanel.detailExpanded && m.workflowPanel.expandedSection == m.workflowPanel.detailSection {
				m.workflowPanel.detailExpanded = false
				m.workflowPanel.detailScroll = 0
			} else {
				m.workflowPanel.detailExpanded = true
				m.workflowPanel.expandedSection = m.workflowPanel.detailSection
				m.workflowPanel.detailScroll = 0
			}
		}
	case "pgdown", "ctrl+n":
	case "pgup", "ctrl+p":
	}
	return nil
}

func (m *model) moveWorkflowPanelDetailSection(delta int) {
	count := int(workflowPanelDetailOutcome) - int(workflowPanelDetailPrompt) + 1
	next := wrapSelection(int(m.workflowPanel.detailSection), count, delta)
	if workflowPanelDetailSection(next) != m.workflowPanel.detailSection {
		m.workflowPanel.detailSection = workflowPanelDetailSection(next)
		m.workflowPanel.detailExpanded = false
		m.workflowPanel.detailScroll = 0
	}
}

func (m *model) selectedWorkflowPanelRunID() string {
	if m.workflowPanel.result == nil {
		return strings.TrimSpace(m.workflowPanel.runID)
	}
	if runID := workflowPanelRunID(m.workflowPanel.result); runID != "" {
		return runID
	}
	runs := workflowPanelRunSections(m.workflowPanel.result)
	if len(runs) == 0 {
		return strings.TrimSpace(m.workflowPanel.runID)
	}
	m.clampWorkflowPanelSelection()
	return strings.TrimSpace(runs[m.workflowPanel.selected].Title)
}

func (m *model) clampWorkflowPanelSelection() {
	runs := workflowPanelRunSections(m.workflowPanel.result)
	if len(runs) == 0 {
		m.workflowPanel.selected = 0
		return
	}
	if m.workflowPanel.selected < 0 {
		m.workflowPanel.selected = 0
	}
	if m.workflowPanel.selected >= len(runs) {
		m.workflowPanel.selected = len(runs) - 1
	}
}

func (m *model) moveWorkflowPanelSnapshotSelection(delta int) {
	snapshot := workflowPanelSnapshot(m.workflowPanel.result)
	if snapshot == nil || len(snapshot.Phases) == 0 {
		m.workflowPanel.selectedPhase = 0
		m.workflowPanel.selectedTask = 0
		m.workflowPanel.focus = workflowPanelFocusPhase
		return
	}
	if m.workflowPanel.focus == workflowPanelFocusTask {
		tasks := snapshot.Phases[m.workflowPanel.selectedPhase].Tasks
		m.workflowPanel.selectedTask = wrapSelection(m.workflowPanel.selectedTask, len(tasks), delta)
	} else {
		m.workflowPanel.selectedPhase = wrapSelection(m.workflowPanel.selectedPhase, len(snapshot.Phases), delta)
		m.workflowPanel.selectedTask = 0
	}
}

func (m *model) clampWorkflowPanelSnapshotSelection() {
	snapshot := workflowPanelSnapshot(m.workflowPanel.result)
	if snapshot == nil || len(snapshot.Phases) == 0 {
		m.workflowPanel.selectedPhase = 0
		m.workflowPanel.selectedTask = 0
		m.workflowPanel.focus = workflowPanelFocusPhase
		return
	}
	if m.workflowPanel.selectedPhase < 0 {
		m.workflowPanel.selectedPhase = 0
	}
	if m.workflowPanel.selectedPhase >= len(snapshot.Phases) {
		m.workflowPanel.selectedPhase = len(snapshot.Phases) - 1
	}
	tasks := snapshot.Phases[m.workflowPanel.selectedPhase].Tasks
	if len(tasks) == 0 {
		m.workflowPanel.selectedTask = 0
		m.workflowPanel.focus = workflowPanelFocusPhase
		return
	}
	if m.workflowPanel.selectedTask < 0 {
		m.workflowPanel.selectedTask = 0
	}
	if m.workflowPanel.selectedTask >= len(tasks) {
		m.workflowPanel.selectedTask = len(tasks) - 1
	}
}
