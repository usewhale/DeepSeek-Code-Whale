package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	tuirender "github.com/usewhale/whale/internal/tui/render"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

func (m model) renderBody(mainWidth, bodyHeight int) string {
	if bodyHeight <= 0 {
		return ""
	}
	if m.mode == modeWorkflowPanel {
		return lipgloss.NewStyle().
			Width(mainWidth).
			Height(bodyHeight).
			Render(m.renderWorkflowPanel())
	}
	if m.page == pageDiff {
		m.ensureViewportContentForSize(mainWidth, bodyHeight)
		return lipgloss.NewStyle().
			Width(mainWidth).
			Height(bodyHeight).
			Render(m.viewport.View())
	}
	if m.page != pageChat {
		m.ensureViewportContentForSize(mainWidth, bodyHeight)
		return lipgloss.NewStyle().
			Width(mainWidth).
			Height(bodyHeight).
			Border(lipgloss.NormalBorder()).
			BorderForeground(tuitheme.Default.Border).
			Render(m.viewport.View())
	}
	m.ensureViewportContentForSize(mainWidth, bodyHeight)
	return lipgloss.NewStyle().Width(mainWidth).Render(m.chat.View())
}

func (m model) View() string {
	if view, ok := m.cachedViewDuringWindowsPaste(); ok {
		return view
	}
	start := time.Now()
	mainWidth, _ := m.layoutDims()
	bottom := m.renderBottom(mainWidth)
	bottomHeight := countVisibleLines(bottom)
	bodyHeight := m.height - bottomHeight
	if m.height <= 0 {
		bodyHeight = 0
	}
	bodyHeight = max(0, bodyHeight)
	if m.page == pageChat && m.mode == modeChat {
		messages := m.chatViewportMessages()
		leadingGap := m.chatViewportLeadingGap(nil, messages)
		bodyHeight = m.chatBodyHeightForView(mainWidth, bodyHeight, messages, leadingGap)
	}
	body := m.renderBody(mainWidth, bodyHeight)
	if m.page != pageChat {
		body = padVisibleLines(body, bodyHeight, mainWidth)
	}
	var out string
	if body == "" {
		out = m.renderChatBottomOnlyView(bottom)
	} else {
		separator := "\n"
		if m.chatViewNeedsBottomGap(body, bottom) {
			separator = "\n\n"
		}
		out = body + separator + bottom
	}
	recordFrame(start, out, m.page, m.width, m.height)
	m.rememberView(out)
	return out
}

func (m model) renderChatBottomOnlyView(bottom string) string {
	if !m.shouldRenderChatBottomOnlyGap() {
		return bottom
	}
	if !m.chatViewNeedsBottomGap(" ", bottom) {
		return bottom
	}
	mainWidth, _ := m.layoutDims()
	return lipgloss.NewStyle().
		Width(mainWidth).
		MaxWidth(mainWidth).
		Render("") + "\n" + bottom
}

func (m model) shouldRenderChatBottomOnlyGap() bool {
	if m.page != pageChat || m.mode != modeChat || !m.shouldRenderComposer() {
		return false
	}
	if m.busy || m.stopping {
		return false
	}
	printed := min(max(m.nativeScrollbackPrinted, 0), len(m.transcript))
	if printed == 0 || printed < len(m.transcript) {
		return false
	}
	visible := m.focusMessages(m.transcript[:printed])
	if len(visible) == 0 {
		return false
	}
	last := visible[len(visible)-1]
	return last.Role == "assistant" && last.Kind == tuirender.KindText
}

func (m model) renderBottom(mainWidth int) string {
	footerText := footerModelEffort(m.model, m.effort) +
		"  " + footerField("thinking:", m.thinking, thinkingFooterColor(m.thinking))
	switch permissionsMode(m.autoAccept, m.autoReviewEnabled) {
	case "auto-review":
		footerText += "  " + footerAutoAccept("auto-review") + " " + footerHint("(Alt+P to cycle)")
	case "auto-accept":
		footerText += "  " + footerAutoAccept("auto-accept on") + " " + footerHint("(Alt+P to cycle)")
	default:
		footerText += "  " + footerField("permission:", "ask", tuitheme.Default.Muted) + " " + footerHint("(Alt+P to cycle)")
	}
	if m.chatMode == "ask" || m.chatMode == "plan" {
		footerText += "  " + footerField("mode:", m.chatMode, tuitheme.Default.Plan) +
			" " + footerHint("(Shift+Tab to switch)")
	}
	viewIndicator := ""
	if m.focusEnabled() {
		viewIndicator = "focus"
	}
	viewReserve := footerViewIndicatorReserve(viewIndicator)
	branchReserve := footerBranchReserveForWidth(footerText, m.gitBranch, mainWidth, viewReserve)
	if m.cwd != "" {
		footerText = appendFooterDir(footerText, m.cwd, mainWidth, branchReserve+viewReserve)
	}
	if m.gitBranch != "" {
		footerText = appendFooterBranch(footerText, m.gitBranch, mainWidth, viewReserve)
	}
	if viewIndicator != "" {
		footerText = appendFooterViewIndicator(footerText, viewIndicator, mainWidth)
	}
	footer := lipgloss.NewStyle().Width(mainWidth).MaxWidth(mainWidth).Render(lipgloss.JoinHorizontal(lipgloss.Left, footerText))
	bottomParts := m.bottomPartsBeforeInput(mainWidth)
	if m.shouldRenderComposer() {
		bottomParts = append(bottomParts, m.renderComposerBlock(mainWidth))
	}
	bottomParts = append(bottomParts, footer)
	return strings.Join(bottomParts, "\n")
}

func (m model) renderComposerBlock(mainWidth int) string {
	return strings.Join([]string{
		m.renderComposerBoundary(mainWidth),
		m.input.View(),
		m.renderComposerBoundary(mainWidth),
	}, "\n")
}

func (m model) renderComposerBoundary(mainWidth int) string {
	width := max(0, mainWidth)
	return lipgloss.NewStyle().
		Foreground(tuitheme.Default.Border).
		Width(width).
		MaxWidth(width).
		Render(strings.Repeat("─", width))
}

func (m model) shouldRenderComposer() bool {
	return m.mode == modeChat && m.page == pageChat
}

func (m model) bottomPartsBeforeInput(mainWidth int) []string {
	bottomParts := make([]string, 0, 8)
	if statusLine := m.renderBusyStatusLine(mainWidth); statusLine != "" {
		bottomParts = append(bottomParts, statusLine)
	}
	if m.mode == modeChat {
		if hint := m.renderBackgroundShellHint(mainWidth); hint != "" {
			bottomParts = append(bottomParts, hint)
		}
	}
	if btw := m.renderBtwPanel(mainWidth); btw != "" {
		bottomParts = append(bottomParts, btw)
	}
	if m.mode == modeChat && m.hasSlashPanel() {
		bottomParts = append(bottomParts, m.renderSlashSuggestions())
	}
	if m.mode == modeChat && !m.hasSlashPanel() && m.hasFilePanel() {
		bottomParts = append(bottomParts, m.renderFileSuggestions())
	}
	if m.mode == modeChat && !m.hasSlashPanel() && !m.hasFilePanel() && m.hasSkillSuggestions() {
		bottomParts = append(bottomParts, m.renderSkillSuggestions())
	}
	if m.mode == modeApproval {
		if len(bottomParts) > 0 {
			bottomParts = append(bottomParts, "")
		}
		bottomParts = append(bottomParts, m.renderApprovalPrompt())
	}
	if m.mode == modePlanImplementation {
		bottomParts = append(bottomParts, m.renderPlanImplementationPicker())
	}
	if m.mode == modePermissionsMenu {
		bottomParts = append(bottomParts, m.renderPermissionsMenu())
	}
	if m.mode == modeSkillsMenu {
		bottomParts = append(bottomParts, m.renderSkillsMenu())
	}
	if m.mode == modeSkillsManager {
		bottomParts = append(bottomParts, m.renderSkillsManager())
	}
	if m.mode == modePluginsManager {
		bottomParts = append(bottomParts, m.renderPluginsManager())
	}
	if m.mode == modeConfigManager {
		bottomParts = append(bottomParts, m.renderConfigManager())
	}
	if m.mode == modeHooksManager {
		bottomParts = append(bottomParts, m.renderHooksManager())
	}
	if m.mode == modeHooksStartupReview {
		bottomParts = append(bottomParts, m.renderHooksStartupReview())
	}
	if m.mode == modeReviewMenu {
		bottomParts = append(bottomParts, m.renderReviewMenu())
	}
	if m.mode == modeReviewBranchPicker || m.mode == modeReviewCommitPicker || m.mode == modeReviewPRPicker {
		bottomParts = append(bottomParts, m.renderReviewTargetPicker())
	}
	if m.mode == modeHelp {
		bottomParts = append(bottomParts, m.renderHelp())
	}
	if m.mode == modeChat && m.page == pageDiff {
		bottomParts = append(bottomParts, m.renderDiffPagerHints(mainWidth))
	}
	if m.mode == modeWorktreeExit {
		bottomParts = append(bottomParts, m.renderWorktreeExit())
	}
	if m.mode == modeWorkflowLaunch {
		bottomParts = append(bottomParts, m.renderWorkflowLaunch())
	}
	if m.mode == modeWorkflowRawScript {
		bottomParts = append(bottomParts, m.renderWorkflowRawScript())
	}
	if m.mode == modeSessionPicker {
		bottomParts = append(bottomParts, m.renderSessionPicker())
	}
	if m.mode == modeUserInput {
		if m.userInput.index < len(m.userInput.questions) {
			bottomParts = append(bottomParts, m.renderUserInputPicker())
		}
	}
	if m.mode == modeModelPicker {
		bottomParts = append(bottomParts, m.renderModelPicker())
	}
	if queued := m.renderQueuedPrompts(mainWidth); queued != "" {
		bottomParts = append(bottomParts, queued)
	}
	return bottomParts
}
