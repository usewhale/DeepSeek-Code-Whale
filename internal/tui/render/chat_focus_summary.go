package render

import (
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
	"strings"
)

func renderFocusSummaryCard(m UIMessage, width int) []string {
	contentWidth := width - 6
	if contentWidth < 16 {
		contentWidth = 16
	}
	body := strings.TrimRight(hardWrapRendered(renderFocusSummaryBody(m.FocusSummary), contentWidth), "\n")
	if body == "" {
		return renderNotice(m, m.Text, width)
	}
	card := spacedCardStyle(width, roleBorderColor(m)).
		Render(body)
	return strings.Split(strings.TrimRight(card, "\n"), "\n")
}

func renderFocusSummaryBody(summary *FocusSummary) string {
	if summary == nil {
		return ""
	}
	parts := make([]string, 0, len(summary.Parts)+1)
	for _, part := range summary.Parts {
		rendered := renderFocusSummaryPart(part)
		if strings.TrimSpace(xansi.Strip(rendered)) != "" {
			parts = append(parts, rendered)
		}
	}
	text := strings.Join(parts, tuitheme.MutedStyle().Render(", "))
	if hint := strings.TrimSpace(summary.Hint); hint != "" {
		if text != "" {
			text += " "
		}
		text += tuitheme.MutedStyle().Render(hint)
	}
	return text
}

func renderFocusSummaryPart(part FocusSummaryPart) string {
	action := strings.TrimSpace(part.Action)
	detail := strings.TrimSpace(part.Detail)
	status := strings.TrimSpace(part.Status)
	var out strings.Builder
	if action != "" {
		out.WriteString(focusSummaryActionStyle(part).Render(action))
	}
	if detail != "" {
		if out.Len() > 0 {
			out.WriteString(tuitheme.MutedStyle().Render(": "))
		}
		out.WriteString(focusSummaryDetail(part))
	}
	if status != "" {
		if out.Len() > 0 {
			out.WriteString(" ")
		}
		out.WriteString(focusSummaryStatusStyle(part).Render(status))
	}
	return out.String()
}

func focusSummaryActionStyle(part FocusSummaryPart) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true).Foreground(focusSummaryKindColor(part.Kind))
	switch focusSummaryState(part) {
	case "failed":
		return style.Foreground(tuitheme.Default.Error)
	case "denied":
		return style.Foreground(tuitheme.Default.ResultDenied)
	case "blocked", "mode_hint", "http_error", "usage_hint", "nonzero", "recoverable":
		return style.Foreground(tuitheme.Default.Warn)
	case "running":
		return style.Foreground(tuitheme.Default.ResultRunning)
	default:
		return style
	}
}

func focusSummaryState(part FocusSummaryPart) string {
	switch strings.TrimSpace(part.State) {
	case "done", "running", "failed", "denied", "blocked", "mode_hint", "http_error", "usage_hint", "nonzero", "recoverable":
		return strings.TrimSpace(part.State)
	}
	status := strings.TrimSpace(part.Status)
	switch {
	case strings.Contains(status, "failed"):
		return "failed"
	case strings.Contains(status, "denied"), strings.Contains(status, "canceled"):
		return "denied"
	case strings.Contains(status, "blocked"):
		return "blocked"
	case strings.Contains(status, "mode hint"):
		return "mode_hint"
	case strings.Contains(status, "HTTP error"):
		return "http_error"
	case strings.Contains(status, "exited non-zero"):
		return "nonzero"
	case strings.Contains(status, "usage hint"):
		return "usage_hint"
	case strings.Contains(status, "running"):
		return "running"
	default:
		return "done"
	}
}

func focusSummaryKindColor(kind string) lipgloss.Color {
	switch kind {
	case "shell":
		return tuitheme.Default.Tool
	case "search":
		return tuitheme.Default.Palette
	case "read", "web", "list":
		return tuitheme.Default.Info
	case "edit":
		return tuitheme.Default.Warn
	case "task":
		return tuitheme.Default.Result
	case "plan":
		return tuitheme.Default.Plan
	case "mode":
		return tuitheme.Default.Warn
	case "todo":
		return tuitheme.Default.InfoSoft
	case "mcp":
		return tuitheme.Default.Info
	default:
		return tuitheme.Default.Muted
	}
}

func focusSummaryDetail(part FocusSummaryPart) string {
	if part.Kind == "shell" {
		return RenderCommandLike(part.Detail)
	}
	return lipgloss.NewStyle().Foreground(tuitheme.Default.Text).Render(part.Detail)
}

func focusSummaryStatusStyle(part FocusSummaryPart) lipgloss.Style {
	style := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	switch focusSummaryState(part) {
	case "failed":
		return style.Foreground(tuitheme.Default.Error).Bold(true)
	case "denied":
		return style.Foreground(tuitheme.Default.ResultDenied).Bold(true)
	case "blocked", "mode_hint", "http_error", "usage_hint", "nonzero", "recoverable":
		return style.Foreground(tuitheme.Default.Warn).Bold(true)
	case "running":
		return style.Foreground(tuitheme.Default.ResultRunning).Bold(true)
	default:
		return style
	}
}
