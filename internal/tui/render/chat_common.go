package render

import (
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
	"strings"
)

func spacedCardStyle(width int, borderColor lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.ThickBorder()).
		BorderLeft(true).
		BorderTop(false).
		BorderRight(false).
		BorderBottom(false).
		BorderForeground(borderColor).
		PaddingLeft(1).
		PaddingTop(1).
		PaddingBottom(1).
		Width(width - 1)
}

func renderEntryText(role, text string, width int) string {
	quiet := role == "you"
	switch role {
	case "assistant", "think", "plan", "status", "result", "result_ok", "result_neutral", "result_recoverable", "result_nonzero", "result_denied", "result_failed", "result_timeout", "result_canceled", "result_error", "result_running", "error", "info", "tool", "tool_summary":
		return Markdown(text, width, quiet)
	case "shell_result_ok", "shell_result_neutral", "shell_result_nonzero", "shell_result_denied", "shell_result_failed", "shell_result_timeout", "shell_result_canceled", "shell_result_error", "shell_result_running":
		return text
	default:
		return text
	}
}

func wrapWithPrefixes(text, firstPrefix, nextPrefix string, width int) []string {
	prefixWidth := lipgloss.Width(firstPrefix)
	if nextWidth := lipgloss.Width(nextPrefix); nextWidth > prefixWidth {
		prefixWidth = nextWidth
	}
	wrapWidth := width - prefixWidth
	if wrapWidth < 8 {
		wrapWidth = 8
	}
	wrapped := hardWrapRendered(text, wrapWidth)
	lines := strings.Split(strings.TrimRight(wrapped, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			out = append(out, firstPrefix+line)
			continue
		}
		out = append(out, nextPrefix+line)
	}
	return out
}

func joinTitleAndBody(title, body string) string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return title
	}
	return title + "\n\n" + body
}

func renderAssistantMarkdown(block string, width int) []string {
	contentWidth := assistantReadableContentWidth(width)
	rendered := strings.TrimRight(hardWrapRendered(renderEntryText("assistant", block, contentWidth), contentWidth), "\n")
	if rendered == "" {
		return nil
	}
	lines := strings.Split(rendered, "\n")
	out := make([]string, 0, len(lines))
	firstContent := true
	for _, line := range lines {
		if strings.TrimSpace(xansi.Strip(line)) == "" {
			out = append(out, "")
			continue
		}
		if firstContent {
			out = append(out, tuitheme.MutedStyle().Render("•")+" "+line)
			firstContent = false
			continue
		}
		out = append(out, "  "+line)
	}
	return out
}

func assistantReadableContentWidth(width int) int {
	contentWidth := width - 2
	if contentWidth < 16 {
		return 16
	}
	if contentWidth > 110 {
		return 110
	}
	return contentWidth
}

func userReadableContentWidth(width int) int {
	contentWidth := width - 4
	if contentWidth < 16 {
		return 16
	}
	if contentWidth > 110 {
		return 110
	}
	return contentWidth
}

func hardWrapRendered(text string, width int) string {
	if width < 1 || text == "" {
		return text
	}
	return xansi.Hardwrap(text, width, true)
}

func truncatePlain(text string, width int) string {
	if width <= 0 || lipgloss.Width(text) <= width {
		return text
	}
	if width <= 3 {
		return xansi.Truncate(text, width, "")
	}
	return xansi.Truncate(text, width-3, "") + "..."
}

func renderUserPrompt(block string, width int) []string {
	contentWidth := userReadableContentWidth(width)
	rendered := strings.TrimRight(hardWrapRendered(renderEntryText("you", block, contentWidth), contentWidth), "\n")
	lines := strings.Split(rendered, "\n")
	glyph := tuitheme.UserPromptGlyphStyle().Render("›")
	rowStyle := tuitheme.UserPromptStyle().Width(width).MaxWidth(width)
	out := make([]string, 0, len(lines)+2)
	out = append(out, rowStyle.Render(""))
	for i, line := range lines {
		if i == 0 {
			out = append(out, rowStyle.Render(glyph+" "+line))
			continue
		}
		out = append(out, rowStyle.Render("  "+line))
	}
	out = append(out, rowStyle.Render(""))
	return out
}

func WorkSeparator(width int) string {
	if width < 1 {
		width = 1
	}
	return tuitheme.MutedStyle().Render(strings.Repeat("─", width))
}

func roleBorderColor(m UIMessage) lipgloss.Color {
	return tuitheme.RoleBorder(m.Role)
}
