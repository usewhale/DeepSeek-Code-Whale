package tui

import (
	"github.com/usewhale/whale/internal/runtime/protocol"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// stuckQuitInterruptThreshold is how many Ctrl+C presses while already stopping
// must accumulate before the quit escape hatch arms. The early presses are
// absorbed so a normally-stopping turn is not accidentally quit; sustained
// presses indicate a wedged turn that must remain escapable.
const stuckQuitInterruptThreshold = 3

func (m *model) handleKeyMsg(msg tea.KeyMsg) (tea.Cmd, bool, bool) {
	if !m.quitArmedUntil.IsZero() && time.Now().After(m.quitArmedUntil) {
		m.quitArmedUntil = time.Time{}
		if m.status == "Press Ctrl+C again to quit" {
			m.status = "ready"
		}
	}
	if !m.shouldDeferSlashMatchRefreshForWindowsPaste(msg) && !m.shouldSkipFileSuggestionRefreshForKey(msg) {
		_ = m.updateSlashMatches()
	}
	if m.mode == modeChat && m.page == pageDiff {
		if msg.String() == "ctrl+c" && m.busy {
			return m.interruptBusyTurn("ctrl+c"), false, true
		}
		if msg.String() == "ctrl+c" {
			return m.handleGlobalKey(msg)
		}
		return m.handleDiffPageKey(msg), false, true
	}
	if m.mode == modeChat && isClipboardImagePasteKey(msg) {
		return m.handleClipboardImagePaste(), false, true
	}
	if m.mode == modeChat && msg.Paste {
		if cmd, handled := m.handlePastedImagePath(string(msg.Runes)); handled {
			return cmd, false, true
		}
		m.cancelWindowsDeferredEnter()
		m.input.HandlePaste(string(msg.Runes))
		m.markWindowsPastedInput()
		m.resetHistoryNavigation()
		return m.updateSlashMatches(), false, true
	}
	if m.btwPanel.visible {
		if handled := m.handleBtwPanelKey(msg); handled {
			return nil, false, true
		}
	}
	// Ctrl+C precedence while busy: in modeChat a non-empty composer means
	// the user is editing a queued draft, so the first Ctrl+C clears the
	// draft (via handleGlobalKey below). With the composer empty, Ctrl+C
	// interrupts the running turn as before. Esc remains the unconditional
	// busy interrupt (handleChatModeKey "esc" case) for users who want to
	// cancel mid-edit. In blocking modes (approval, user-input, …) Ctrl+C
	// must continue to interrupt unconditionally — otherwise it would fall
	// through to the modal's own Ctrl+C handler, which only dismisses the
	// modal without canceling the running turn. The raw Value() check (not
	// TrimSpace) keeps whitespace-only drafts on the clear path so users
	// can recover from accidental blank lines / whitespace paste. The
	// hasWindowsPasteBuffer() check covers the Windows paste quiet-delay
	// window: chunks pasted during a burst live in m.windowsPaste.buffer
	// for up to windowsPasteQuietDelay (80ms) before flushing into the
	// textarea, so a Ctrl+C arriving inside that window would otherwise
	// see an empty textarea and incorrectly interrupt the running turn.
	if msg.String() == "ctrl+c" && m.busy && (m.mode != modeChat || (m.input.Value() == "" && !m.hasWindowsPasteBuffer())) {
		return m.interruptBusyTurn("ctrl+c"), false, true
	}
	if m.mode == modeChat {
		if cmd, handled := m.handleWindowsPasteFallbackKey(msg); handled {
			return cmd, false, true
		}
	}
	if m.mode == modeChat {
		if cmd, handled := m.handleChatModeKey(msg); handled {
			return cmd, false, true
		}
	}
	switch m.mode {
	case modeApproval:
		return m.handleApprovalKey(msg), false, true
	case modeSessionPicker:
		return m.handleSessionPickerKey(msg), false, true
	case modeUserInput:
		return m.handleUserInputKey(msg), false, true
	case modeModelPicker:
		return m.handleModelPickerKey(msg), false, true
	case modePermissionsMenu:
		return m.handlePermissionsMenuKey(msg), false, true
	case modePlanImplementation:
		return m.handlePlanImplementationKey(msg), false, true
	case modeSkillsMenu:
		return m.handleSkillsMenuKey(msg), false, true
	case modeSkillsManager:
		return m.handleSkillsManagerKey(msg), false, true
	case modePluginsManager:
		return m.handlePluginsManagerKey(msg), false, true
	case modeConfigManager:
		return m.handleConfigManagerKey(msg), false, true
	case modeHooksManager:
		return m.handleHooksManagerKey(msg), false, true
	case modeHooksStartupReview:
		return m.handleHooksStartupReviewKey(msg), false, true
	case modeReviewMenu:
		return m.handleReviewMenuKey(msg), false, true
	case modeReviewBranchPicker, modeReviewCommitPicker, modeReviewPRPicker:
		return m.handleReviewTargetPickerKey(msg), false, true
	case modeHelp:
		return m.handleHelpKey(msg), false, true
	case modeWorktreeExit:
		return m.handleWorktreeExitKey(msg), false, true
	case modeWorkflowLaunch:
		return m.handleWorkflowLaunchKey(msg), false, true
	case modeWorkflowRawScript:
		return m.handleWorkflowRawScriptKey(msg), false, true
	case modeWorkflowPanel:
		return m.handleWorkflowPanelKey(msg), false, true
	}
	cmd, quit, handled := m.handleGlobalKey(msg)
	if handled {
		return cmd, quit, true
	}
	cmd, handled = m.handleComposerKey(msg)
	return cmd, false, handled
}

func (m model) shouldDeferSlashMatchRefreshForWindowsPaste(msg tea.KeyMsg) bool {
	return m.mode == modeChat &&
		m.windowsPasteFallbackEnabled() &&
		(len(msg.Runes) > 0 || m.hasWindowsPasteBuffer())
}

func (m model) shouldRouteWindowsPasteFallbackBeforeLayout(msg tea.KeyMsg) bool {
	return m.mode == modeChat &&
		m.windowsPasteFallbackEnabled() &&
		m.hasWindowsPasteBuffer() &&
		!msg.Paste &&
		len(msg.Runes) > 0
}

func (m *model) interruptBusyTurn(source string) tea.Cmd {
	alreadyStopping := m.stopping
	m.cancelBlockingModalForInterrupt(!alreadyStopping)
	if alreadyStopping {
		m.stoppingInterruptCount++
		// A genuinely-stopping turn clears via EventTurnDone shortly, so the
		// first interrupts after the stop request are absorbed (no quit arming,
		// no extra intent) — the normal interrupt debounce. But a hung provider
		// can leave the turn wedged with no EventTurnDone, so m.busy never
		// clears and the user used to be trapped ("关不掉，没法输入任何指令").
		// Once Ctrl+C persists past the threshold the turn looks wedged, so we
		// expose the same arm-then-quit escape hatch as the idle composer.
		if m.stoppingInterruptCount < stuckQuitInterruptThreshold {
			m.commitLiveTranscript(false)
			return m.flushNativeScrollbackCmd()
		}
		now := time.Now()
		if !m.quitArmedUntil.IsZero() && now.Before(m.quitArmedUntil) {
			m.quitArmedUntil = time.Time{}
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentRequestExit})
			m.status = "exiting"
			m.commitLiveTranscript(false)
			return m.flushNativeScrollbackCmd()
		}
		m.quitArmedUntil = now.Add(2 * time.Second)
		m.status = "Press Ctrl+C again to quit"
		m.commitLiveTranscript(false)
		return tea.Batch(armQuitCmd(2*time.Second), m.flushNativeScrollbackCmd())
	}
	m.stoppingInterruptCount = 0
	m.quitArmedUntil = time.Time{}
	if m.runtime != nil {
		m.dispatchIntent(protocol.Intent{Kind: protocol.IntentShutdown, InterruptSource: source})
	}
	m.status = "stopping"
	m.stopping = true
	m.markWindowsBusyInputStopped()
	m.cancelWindowsDeferredEnter()
	if m.submitQueuedPromptAfterInterrupt {
		m.appendNotice(m.turnInterruptedForQueuedPromptNoticeText())
	} else {
		m.appendNotice(m.turnInterruptedNoticeText())
	}
	m.commitLiveTranscript(false)
	return m.flushNativeScrollbackCmd()
}

func (m *model) cancelBlockingModalForInterrupt(dispatch bool) {
	switch m.mode {
	case modeApproval:
		if dispatch && m.approval.toolCallID != "" {
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelToolApproval, ToolCallID: m.approval.toolCallID})
		}
		if dispatch {
			for _, prompt := range m.approvalQueue {
				if prompt.toolCallID != "" {
					m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelToolApproval, ToolCallID: prompt.toolCallID})
				}
			}
		}
		m.approval = approvalPromptState{}
		m.approvalQueue = nil
		m.mode = modeChat
	case modeUserInput:
		if dispatch && !m.busy && m.userInput.toolCallID != "" {
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentCancelUserInput, ToolCallID: m.userInput.toolCallID})
		}
		m.mode = modeChat
	}
}

func (m model) shouldSkipFileSuggestionRefreshForKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "down", "tab", "enter", "esc":
	default:
		return false
	}
	if m.hasFilePanel() {
		return true
	}
	// The file panel can be dismissed (Esc) while the @-token stays in the
	// composer. These navigation keys never mutate the textarea, so letting
	// the pre-key updateSlashMatches run would re-activate the panel and
	// kick off a file search whose returned cmd would be discarded here —
	// the UI gets stuck "Searching..." and swallows history navigation
	// until the user edits the input.
	_, hasAtToken := m.input.CurrentPrefixedToken('@')
	return hasAtToken
}

func (m *model) applyPalette() {
	if m.palette.selected < 0 || m.palette.selected >= len(m.palette.actions) {
		return
	}
	m.palette.actions[m.palette.selected].Run(m)
}
