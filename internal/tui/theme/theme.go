package theme

import "github.com/charmbracelet/lipgloss"

// Palette is the single built-in whale TUI chrome palette.
// It centralizes the current color choices without introducing user-facing
// theme configuration yet.
type Palette struct {
	Text       lipgloss.Color
	Accent     lipgloss.Color
	Assistant  lipgloss.Color
	Border     lipgloss.Color
	Muted      lipgloss.Color
	Subtle     lipgloss.Color
	Info       lipgloss.Color
	InfoSoft   lipgloss.Color
	Success    lipgloss.Color
	Warn       lipgloss.Color
	Error      lipgloss.Color
	Palette    lipgloss.Color
	StatusIdle lipgloss.Color
	Selection  lipgloss.Color

	UserAccent     lipgloss.Color
	UserBackground lipgloss.Color

	Plan           lipgloss.Color
	PlanBackground lipgloss.Color
	Tool           lipgloss.Color
	Result         lipgloss.Color
	ResultDenied   lipgloss.Color
	ResultTimeout  lipgloss.Color
	ResultError    lipgloss.Color
	ResultRunning  lipgloss.Color
}

var Default = Palette{
	Text:           lipgloss.Color(""),
	Accent:         lipgloss.Color("63"),
	Assistant:      lipgloss.Color("39"),
	Border:         lipgloss.Color("240"),
	Muted:          lipgloss.Color("245"),
	Subtle:         lipgloss.Color("240"),
	Info:           lipgloss.Color("111"),
	InfoSoft:       lipgloss.Color("86"),
	Success:        lipgloss.Color("78"),
	Warn:           lipgloss.Color("220"),
	Error:          lipgloss.Color("203"),
	Palette:        lipgloss.Color("212"),
	StatusIdle:     lipgloss.Color("86"),
	Selection:      lipgloss.Color("240"),
	UserAccent:     lipgloss.Color("63"),
	UserBackground: lipgloss.Color("236"),
	Plan:           lipgloss.Color("75"),
	PlanBackground: lipgloss.Color("236"),
	Tool:           lipgloss.Color("220"),
	Result:         lipgloss.Color("81"),
	ResultDenied:   lipgloss.Color("214"),
	ResultTimeout:  lipgloss.Color("215"),
	ResultError:    lipgloss.Color("197"),
	ResultRunning:  lipgloss.Color("117"),
}

func UserPromptStyle() lipgloss.Style {
	return lipgloss.NewStyle().Background(Default.UserBackground)
}

func UserPromptGlyphStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(Default.UserAccent).Bold(true)
}

func MutedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(Default.Muted)
}

func StatusStyle(kind string) lipgloss.Style {
	switch kind {
	case "success":
		return lipgloss.NewStyle().Foreground(Default.Success)
	case "warning", "warn":
		return lipgloss.NewStyle().Foreground(Default.Warn)
	case "error":
		return lipgloss.NewStyle().Foreground(Default.Error)
	default:
		return MutedStyle()
	}
}

func RoleBorder(role string) lipgloss.Color {
	switch role {
	case "you":
		return Default.Accent
	case "assistant":
		return Default.Assistant
	case "think":
		return Default.Border
	case "notice", "info", "result_canceled", "result_neutral", "shell_result_neutral":
		return Default.Muted
	case "status":
		return Default.Info
	case "plan":
		return Default.Plan
	case "tool":
		return Default.Tool
	case "result":
		return Default.Result
	case "result_ok", "shell_result_ok":
		return Default.Success
	case "result_nonzero", "shell_result_nonzero":
		return Default.Warn
	case "result_denied", "shell_result_denied":
		return Default.ResultDenied
	case "result_failed", "shell_result_failed", "error":
		return Default.Error
	case "result_blocked", "shell_result_blocked", "result_mode_hint", "shell_result_mode_hint", "result_http_error", "shell_result_http_error", "result_usage_hint", "shell_result_usage_hint", "result_recoverable":
		return Default.Warn
	case "result_timeout", "shell_result_timeout":
		return Default.ResultTimeout
	case "result_error", "shell_result_error":
		return Default.ResultError
	case "result_running", "shell_result_running":
		return Default.ResultRunning
	case "shell_result_canceled":
		return Default.Muted
	default:
		return Default.Border
	}
}
