package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usewhale/whale/internal/runtime/protocol"
)

func (m *model) handleChatModeKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+o":
		if m.toggleFocusView() {
			return m.redrawTranscriptForFocusToggleCmd(), true
		}
	case "shift+tab", "backtab":
		if m.localSubmitPending > 0 {
			m.status = "wait for command to finish"
			m.refreshViewportContent()
			return m.flushNativeScrollbackCmd(), true
		}
		if !m.busy && !m.hasSlashSuggestions() && !m.hasFilePanel() && !m.hasSkillSuggestions() {
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentToggleMode})
			return nil, true
		}
	case "alt+p", "meta+p":
		if m.localSubmitPending > 0 {
			m.status = "wait for command to finish"
			m.refreshViewportContent()
			return m.flushNativeScrollbackCmd(), true
		}
		// Blocked while busy: the permission intents emit EventTurnDone, which
		// the TUI treats as turn completion — dispatching mid-turn would commit
		// the live transcript and clear busy while the backend keeps streaming.
		// Mid-turn auto-accept is still available from the approval modal
		// (approve + enable auto-accept), which uses a quiet intent path.
		if !m.busy && !m.hasSlashSuggestions() && !m.hasFilePanel() && !m.hasSkillSuggestions() {
			m.cyclePermissions()
			return nil, true
		}
	case "up":
		if m.hasSlashSuggestions() {
			m.slash.selected = wrapSelection(m.slash.selected, len(m.slash.matches), -1)
			return nil, true
		}
		if m.hasFilePanel() {
			if m.hasFileSuggestions() {
				m.files.selected = wrapSelection(m.files.selected, len(m.files.matches), -1)
			}
			return nil, true
		}
		if m.hasSkillSuggestions() {
			m.skills.selected = wrapSelection(m.skills.selected, len(m.skills.matches), -1)
			return nil, true
		}
		if m.shouldHandleHistoryNavigation() {
			if ok, cmd := m.historyPrev(); ok {
				return cmd, true
			}
		}
	case "down":
		if m.hasSlashSuggestions() {
			m.slash.selected = wrapSelection(m.slash.selected, len(m.slash.matches), 1)
			return nil, true
		}
		if m.hasFilePanel() {
			if m.hasFileSuggestions() {
				m.files.selected = wrapSelection(m.files.selected, len(m.files.matches), 1)
			}
			return nil, true
		}
		if m.hasSkillSuggestions() {
			m.skills.selected = wrapSelection(m.skills.selected, len(m.skills.matches), 1)
			return nil, true
		}
		if m.shouldHandleHistoryNavigation() {
			if ok, cmd := m.historyNext(); ok {
				return cmd, true
			}
		}
	case "tab":
		if m.hasSlashSuggestions() {
			if suggestion, ok := m.selectedSlashSuggestion(); ok {
				m.input.SetValue(suggestion.InsertText)
				m.skillBinding = nil
				return m.updateSlashMatches(), true
			}
			return nil, true
		}
		if m.insertSelectedFileSuggestion() {
			return nil, true
		}
		if m.insertSelectedSkill() {
			return nil, true
		}
	case "esc":
		if m.busy {
			m.prepareQueuedPromptAfterInterrupt()
			return m.interruptBusyTurn("esc"), true
		}
		if m.page != pageChat {
			m.page = pageChat
			m.refreshViewportContentFollow(true)
			return nil, true
		}
		if m.hasSlashPanel() {
			m.slash.matches = nil
			m.slash.selected = 0
			m.slash.argumentHint = ""
			return nil, true
		}
		if m.hasFilePanel() {
			clearFileSuggestions(m)
			return nil, true
		}
		if m.hasSkillSuggestions() {
			m.skills.matches = nil
			m.skills.selected = 0
			return nil, true
		}
	case "pgup", "pgdown":
		return m.handleViewportScrollKey(msg.String()), true
	}
	return nil, false
}

// cyclePermissions advances the permission mode with the Alt+P shortcut:
// ask -> auto-review -> auto-accept -> ask. Local state is updated
// optimistically so the footer indicator responds instantly; the service
// confirms the same state via EventInfo (AutoAcceptKnown).
func (m *model) cyclePermissions() {
	switch nextPermissionsMode(m.autoAccept, m.autoReviewEnabled) {
	case "auto-review":
		m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetAutoReview, AutoReview: true})
		m.autoReviewEnabled = true
		m.autoAccept = false
	case "auto-accept":
		m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetApprovalMode, ApprovalMode: "auto_accept"})
		m.autoAccept = true
		m.autoReviewEnabled = false
	default: // ask
		m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetApprovalMode, ApprovalMode: "ask"})
		m.autoAccept = false
		m.autoReviewEnabled = false
	}
}

func (m *model) handleDiffPageKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "q", "esc":
		m.page = pageChat
		m.refreshViewportContentFollow(true)
	case "up", "k":
		m.refreshViewportContent()
		m.viewport.LineUp(1)
	case "down", "j":
		m.refreshViewportContent()
		m.viewport.LineDown(1)
	case "pgup":
		m.handleViewportScrollKey("pgup")
	case "pgdown":
		m.handleViewportScrollKey("pgdown")
	case "home":
		m.handleViewportScrollKey("home")
	case "end":
		m.handleViewportScrollKey("end")
	}
	return nil
}

func (m *model) handleGlobalKey(msg tea.KeyMsg) (tea.Cmd, bool, bool) {
	switch msg.String() {
	case "ctrl+c":
		// Use the raw value (not TrimSpace) so whitespace-only drafts can
		// still be cleared. Without this, a draft containing only spaces or
		// blank lines would arm quit / interrupt the busy turn instead of
		// clearing — leaving the user stuck after an accidental paste.
		// Also clear when only the Windows paste buffer has content (the
		// 80ms quiet-delay window before chunks flush into the textarea) —
		// otherwise pasting during a busy turn and immediately hitting
		// Ctrl+C would arm quit instead of dropping the pasted draft.
		if m.input.Value() != "" || m.hasWindowsPasteBuffer() || len(m.composerAttachments) > 0 {
			m.input.Reset()
			m.skillBinding = nil
			m.composerAttachments = nil
			m.resetWindowsPasteFallbackInputState()
			m.resetHistoryNavigation()
			_ = m.updateSlashMatches()
			m.skills.matches = nil
			m.skills.selected = 0
			clearFileSuggestions(m)
			m.status = "input cleared"
			return nil, false, true
		}
		now := time.Now()
		if !m.quitArmedUntil.IsZero() && now.Before(m.quitArmedUntil) {
			if m.dispatch == nil {
				return nil, true, true
			}
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentRequestExit})
			m.quitArmedUntil = time.Time{}
			m.status = "exiting"
			return nil, false, true
		}
		m.quitArmedUntil = now.Add(2 * time.Second)
		m.status = "Press Ctrl+C again to quit"
		return armQuitCmd(2 * time.Second), false, true
	case "enter":
		if m.busy {
			m.submitPromptWhileBusy(m.input.Value())
			return m.flushNativeScrollbackCmd(), false, true
		}
		if m.localSubmitPending > 0 {
			m.status = "wait for command to finish"
			m.refreshViewportContent()
			return m.flushNativeScrollbackCmd(), false, true
		}
		if m.insertSelectedFileSuggestion() {
			return nil, false, true
		}
		if m.hasSlashSuggestions() && !m.slashSelectionAlreadyTyped() {
			if suggestion, ok := m.selectedSlashSuggestion(); ok {
				m.input.SetValue(suggestion.InsertText)
				m.skillBinding = nil
				suggestionCmd := m.updateSlashMatches()
				if suggestion.AutoRun {
					return tea.Sequence(suggestionCmd, m.flushNativeScrollbackCmd(), m.submitPrompt(suggestion.InsertText)), false, true
				}
				return suggestionCmd, false, true
			}
			return nil, false, true
		}
		if m.insertSelectedSkill() {
			return nil, false, true
		}
		if m.page == pageLogs && m.logFilterInput.Focused() {
			m.logFilter = strings.TrimSpace(m.logFilterInput.Value())
			m.logFilterInput.Blur()
			return nil, false, true
		}
		if raw := m.input.Value(); strings.HasSuffix(raw, "\\") {
			m.input.SetValue(strings.TrimSuffix(raw, "\\") + "\n")
			m.skillBinding = nil
			m.resetHistoryNavigation()
			return m.updateSlashMatches(), false, true
		}
		value := strings.TrimSpace(m.input.Value())
		if value == "" {
			return nil, false, true
		}
		return tea.Sequence(m.flushNativeScrollbackCmd(), m.submitPrompt(value)), false, true
	}
	return nil, false, false
}

func (m *model) handleComposerKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "shift+enter", "ctrl+j":
		m.input.InsertNewline()
		m.resetHistoryNavigation()
		return m.updateSlashMatches(), true
	case "ctrl+p":
		_, cmd := m.historyPrev()
		return cmd, true
	case "ctrl+n":
		_, cmd := m.historyNext()
		return cmd, true
	}
	if m.input.HandleKey(msg) {
		if msg.String() == "ctrl+u" {
			m.skillBinding = nil
			m.resetWindowsPasteFallbackInputState()
		}
		m.resetWindowsPasteFallbackIfInputEmpty()
		m.resetHistoryNavigation()
		return m.updateSlashMatches(), true
	}
	return nil, false
}
