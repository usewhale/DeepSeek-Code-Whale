package render

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

func renderToolEvent(m UIMessage, block string, width int) []string {
	contentWidth := width - 6
	if contentWidth < 16 {
		contentWidth = 16
	}
	rawLines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(rawLines) == 0 {
		return nil
	}
	header := strings.TrimSpace(rawLines[0])
	if header == "" {
		return nil
	}
	if m.Kind == KindSubagent {
		return renderSubagentEvent(m, header, rawLines[1:], width, contentWidth)
	}
	if isShellToolEvent(m) {
		return renderShellToolEvent(m, header, rawLines[1:], width, contentWidth)
	}
	if isEditToolEvent(m) {
		return renderEditToolEvent(m, header, rawLines[1:], width, contentWidth)
	}
	if isExplorationGroupEvent(m) {
		return renderExplorationGroupEvent(m, header, rawLines[1:], width, contentWidth)
	}
	out := make([]string, 0, len(rawLines)+2)
	out = append(out, renderToolEventHeader(m, header, width)...)
	for _, raw := range rawLines[1:] {
		line := strings.TrimRight(raw, "\n")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, renderToolEventChild(line, contentWidth)...)
	}
	return out
}

const subagentCardMaxVisibleSteps = 5

const subagentCardMaxSummaryLen = 120

func renderSubagentEvent(m UIMessage, header string, bodyLines []string, width, contentWidth int) []string {
	// Determine status glyph from accumulated step statuses
	statusGlyph := "◐"
	role := subagentRoleFromHeader(header)
	for _, line := range bodyLines {
		if strings.HasPrefix(line, "role:") {
			role = strings.TrimSpace(strings.TrimPrefix(line, "role:"))
		}
	}
	if len(m.SubagentSteps) > 0 {
		last := m.SubagentSteps[len(m.SubagentSteps)-1]
		switch last.Status {
		case "completed", "done", "success":
			statusGlyph = "✓"
		case "failed", "error", "tool_failed":
			statusGlyph = "✗"
		case "cancelled":
			statusGlyph = "⊘"
		}
	}

	// Build content
	shortHeader := statusGlyph + " Subagent " + role
	stepCount := len(m.SubagentSteps)
	if stepCount > 0 {
		shortHeader += " (" + fmt.Sprintf("%d steps)", stepCount)
		// Show at most subagentCardMaxVisibleSteps, oldest truncated
		var visible []protocol.ProgressStep
		truncated := 0
		if stepCount > subagentCardMaxVisibleSteps {
			truncated = stepCount - subagentCardMaxVisibleSteps
			visible = m.SubagentSteps[truncated:]
		} else {
			visible = m.SubagentSteps
		}
		if truncated > 0 {
			shortHeader += "\n···"
		}
		for _, step := range visible {
			label := step.ToolName
			if label == "" {
				label = "agent_event"
			}
			detail := truncateSubagentSummary(step.Summary)
			if label == "subagent" {
				// Final result row — visually separate from tool steps
				shortHeader += "\n" + tuitheme.MutedStyle().Render("── result ──")
				if detail != "" {
					shortHeader += "\n" + detail
				}
			} else if detail != "" {
				shortHeader += "\n" + label + ": " + detail
			} else {
				shortHeader += "\n" + label
			}
		}
	} else {
		// No steps yet - show original body
		for _, line := range bodyLines {
			shortHeader += "\n" + line
		}
	}

	// Render as a card with border
	card := spacedCardStyle(width, lipgloss.Color("#6c6c6c")).
		Render(strings.TrimRight(shortHeader, "\n"))
	return strings.Split(strings.TrimRight(card, "\n"), "\n")
}

func truncateSubagentSummary(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= subagentCardMaxSummaryLen {
		return s
	}
	cut := subagentCardMaxSummaryLen
	// Try to break at a word boundary (space or punctuation) for cleaner cut
	for i := cut; i > cut/2; i-- {
		r := runes[i]
		if r == ' ' || r == '\n' || r == '，' || r == '。' || r == '.' || r == ',' {
			cut = i
			break
		}
	}
	return string(runes[:cut]) + " " + tuitheme.MutedStyle().Render("... omitted")
}

func subagentRoleFromHeader(header string) string {
	fields := strings.Fields(strings.TrimSpace(header))
	if len(fields) >= 2 && fields[0] == "Subagent" {
		return fields[1]
	}
	return "explore"
}

func renderEditToolEvent(m UIMessage, header string, rawLines []string, width, contentWidth int) []string {
	status := ""
	if len(rawLines) > 0 && isToolStatusLine(rawLines[0]) {
		status = strings.TrimSpace(rawLines[0])
		rawLines = rawLines[1:]
	}
	out := make([]string, 0, len(rawLines)+1)
	out = append(out, renderToolEventHeaderWithStatus(m, header, status, width)...)
	for _, raw := range rawLines {
		line := strings.TrimRight(raw, "\n")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapWithPrefixes(line, "  ", "  ", contentWidth)...)
	}
	return out
}

func renderShellToolEvent(m UIMessage, header string, rawLines []string, width, contentWidth int) []string {
	status := ""
	if len(rawLines) > 0 && isToolStatusLine(rawLines[0]) {
		status = strings.TrimSpace(rawLines[0])
		rawLines = rawLines[1:]
	}
	out := make([]string, 0, len(rawLines)+1)
	out = append(out, renderToolEventHeaderWithStatus(m, header, status, width)...)
	for _, raw := range rawLines {
		line := strings.TrimRight(raw, "\n")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapWithPrefixes(line, "  ", "  ", contentWidth)...)
	}
	return out
}

func renderExplorationGroupEvent(m UIMessage, header string, rawLines []string, width, contentWidth int) []string {
	out := make([]string, 0, len(rawLines)+1)
	out = append(out, renderToolEventHeader(m, header, width)...)
	first := true
	for _, raw := range rawLines {
		line := strings.TrimRight(raw, "\n")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		firstPrefix := "    "
		if first {
			firstPrefix = tuitheme.MutedStyle().Render("  └ ")
			first = false
		}
		out = append(out, wrapWithPrefixes(line, firstPrefix, "    ", contentWidth)...)
	}
	return out
}

func renderToolEventHeader(m UIMessage, header string, width int) []string {
	return renderToolEventHeaderWithStatus(m, header, "", width)
}

func renderToolEventHeaderWithStatus(m UIMessage, header, status string, width int) []string {
	contentWidth := width - 2
	if contentWidth < 16 {
		contentWidth = 16
	}
	bullet := toolEventBulletStyle(m).Render("•")
	verb, rest := splitEventHeader(header)
	verbStyle := lipgloss.NewStyle().Bold(true).Foreground(toolEventVerbColor(m))
	var rendered string
	if rest == "" {
		rendered = bullet + " " + verbStyle.Render(verb)
	} else {
		rendered = bullet + " " + verbStyle.Render(verb) + " " + RenderCommandLike(rest)
	}
	if strings.TrimSpace(status) != "" {
		rendered += tuitheme.MutedStyle().Render("  ") + renderToolStatusLine(status)
	}
	return wrapWithPrefixes(rendered, "", "  ", width)
}

func renderToolEventChild(line string, width int) []string {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(xansi.Strip(line)) == "" {
		return nil
	}
	if hasLeadingCommandSpace(line) {
		return renderIndentedToolEventChild(line, width)
	}
	action, rest := splitStatusLinePreservingSpace(line)
	if normalizedStatusToken(action) == "" {
		return renderPlainToolEventChild(line, width)
	}
	actionStyle := toolEventStatusStyle(action)
	var rendered string
	if rest == "" {
		rendered = actionStyle.Render(action)
	} else {
		rendered = actionStyle.Render(action) + toolEventDetailStyle(action).Render(rest)
	}
	return wrapWithPrefixes(rendered, tuitheme.MutedStyle().Render("  └ "), "    ", width)
}

func renderIndentedToolEventChild(line string, width int) []string {
	return renderPlainToolEventChild(line, width)
}

func renderPlainToolEventChild(line string, width int) []string {
	prefix := tuitheme.MutedStyle().Render("  └ ")
	rendered := line
	if lipgloss.Width(prefix)+lipgloss.Width(rendered) <= width {
		return []string{prefix + rendered}
	}
	return wrapWithPrefixes(rendered, prefix, "    ", width)
}

func hasLeadingCommandSpace(text string) bool {
	if text == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text)
	return r == ' ' || r == '\t'
}

func isShellToolEvent(m UIMessage) bool {
	return strings.HasPrefix(m.Role, "shell_result_")
}

func isEditToolEvent(m UIMessage) bool {
	switch strings.TrimSpace(m.ToolName) {
	case "write_file", "edit_file", "write", "edit", "multi_edit":
		return true
	default:
		return false
	}
}

func isToolStatusLine(line string) bool {
	action, _ := splitStatusLinePreservingSpace(line)
	return normalizedStatusToken(action) != ""
}

func splitStatusLinePreservingSpace(text string) (string, string) {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return "", ""
	}
	if strings.HasPrefix(text, "Command failed") {
		if idx := strings.Index(text, " · "); idx >= 0 {
			return text[:idx], text[idx:]
		}
		return text, ""
	}
	for i, r := range text {
		if r == ' ' || r == '\t' {
			return text[:i], text[i:]
		}
	}
	return text, ""
}

func toolEventStatusStyle(token string) lipgloss.Style {
	style := lipgloss.NewStyle().Foreground(tuitheme.Default.InfoSoft)
	switch normalizedStatusToken(token) {
	case "success":
		return style.Foreground(tuitheme.Default.Success).Bold(true)
	case "denied":
		return style.Foreground(tuitheme.Default.ResultDenied).Bold(true)
	case "error":
		return style.Foreground(tuitheme.Default.Error).Bold(true)
	case "timeout":
		return style.Foreground(tuitheme.Default.ResultTimeout).Bold(true)
	case "warning":
		return style.Foreground(tuitheme.Default.Warn).Bold(true)
	case "canceled":
		return style.Foreground(tuitheme.Default.Muted).Bold(true)
	default:
		return style
	}
}

func toolEventDetailStyle(token string) lipgloss.Style {
	switch normalizedStatusToken(token) {
	case "canceled":
		return lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	default:
		return lipgloss.NewStyle().Foreground(tuitheme.Default.Text)
	}
}

func renderToolStatusLine(line string) string {
	action, rest := splitStatusLinePreservingSpace(line)
	if normalizedStatusToken(action) == "" {
		return tuitheme.MutedStyle().Render(line)
	}
	if rest == "" {
		return toolEventStatusStyle(action).Render(action)
	}
	return toolEventStatusStyle(action).Render(action) + toolEventDetailStyle(action).Render(rest)
}

func normalizedStatusToken(token string) string {
	normalized := strings.ToUpper(strings.TrimSpace(token))
	if strings.HasPrefix(normalized, "COMMAND FAILED") {
		return "error"
	}
	switch normalized {
	case "✓", "OK", "DONE", "SUCCESS":
		return "success"
	case "DENIED":
		return "denied"
	case "✗", "ERROR", "FAILED", "FAIL":
		return "error"
	case "TIMEOUT":
		return "timeout"
	case "WARN", "WARNING", "HTTP":
		return "warning"
	case "CANCELED", "CANCELLED":
		return "canceled"
	default:
		return ""
	}
}

func splitEventHeader(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", ""
	}
	verb := parts[0]
	rest := strings.TrimSpace(strings.TrimPrefix(text, verb))
	return verb, rest
}

func isExplorationGroupEvent(m UIMessage) bool {
	return m.Role == "exploration_group" || m.Role == "exploration_group_running"
}

func toolEventBulletStyle(m UIMessage) lipgloss.Style {
	style := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted).Bold(true)
	switch m.Role {
	case "result_ok", "shell_result_ok", "exploration_group":
		return style.Foreground(tuitheme.Default.Success)
	case "result_denied", "shell_result_denied":
		return style.Foreground(tuitheme.Default.ResultDenied)
	case "result_failed", "shell_result_failed", "result_error", "shell_result_error", "error":
		return style.Foreground(tuitheme.Default.Error)
	case "result_recoverable":
		return style.Foreground(tuitheme.Default.Warn)
	case "result_timeout", "shell_result_timeout":
		return style.Foreground(tuitheme.Default.ResultTimeout)
	case "result_canceled", "shell_result_canceled":
		return style.Foreground(tuitheme.Default.Muted)
	case "result_running", "shell_result_running", "tool", "exploration_group_running":
		return style.Foreground(tuitheme.Default.Tool)
	default:
		return style
	}
}

func toolEventVerbColor(m UIMessage) lipgloss.Color {
	switch m.Role {
	case "result_ok", "shell_result_ok", "exploration_group":
		return tuitheme.Default.Success
	case "result_denied", "shell_result_denied":
		return tuitheme.Default.ResultDenied
	case "result_failed", "shell_result_failed", "result_error", "shell_result_error", "error":
		return tuitheme.Default.Error
	case "result_recoverable":
		return tuitheme.Default.Warn
	case "result_timeout", "shell_result_timeout":
		return tuitheme.Default.ResultTimeout
	case "result_canceled", "shell_result_canceled":
		return tuitheme.Default.Muted
	case "result_running", "shell_result_running", "tool", "exploration_group_running":
		return tuitheme.Default.Tool
	default:
		return tuitheme.Default.Text
	}
}

func toolNamePrefix(text string) string {
	idx := strings.Index(text, ":")
	if idx <= 0 {
		return ""
	}
	name := strings.TrimSpace(text[:idx])
	name = strings.TrimPrefix(name, "[")
	name = strings.TrimSuffix(name, "]")
	return name
}
