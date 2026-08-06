package defaults

import "strings"

const (
	DefaultModel                 = "deepseek-v4-flash"
	ProModel                     = "deepseek-v4-pro"
	DefaultReasoningEffort       = "high"
	DefaultThinkingEnabled       = true
	DefaultContextWindow         = 128_000
	DeepSeekV4ContextWindow      = 1_000_000
	DefaultAutoCompactThreshold  = 0.85
	DefaultAgentCompactThreshold = 0.90
	// DefaultMaxToolIters is the ACP entrypoint's tool-iteration cap. It must
	// stay a finite backstop: the dynamic loop guards (storm rounds, progress
	// redundancy) cannot see mutating-argument-varying loops, so a cap is the
	// only guaranteed termination for that loop class. 300 sits far above any
	// recorded healthy turn (max 126) while bounding the invisible loops.
	DefaultMaxToolIters       = 300
	DefaultMemoryMaxChars     = 8000
	DefaultMemoryFileOrderCSV = "AGENTS.md,.claude/instructions.md,CLAUDE.md"
)

var supportedModels = []string{
	DefaultModel,
	ProModel,
}

var defaultMemoryFileOrder = []string{
	"AGENTS.md",
	".claude/instructions.md",
	"CLAUDE.md",
}

func SupportedModels() []string {
	return append([]string(nil), supportedModels...)
}

func IsSupportedModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, supported := range supportedModels {
		if m == supported {
			return true
		}
	}
	return false
}

func DefaultMemoryFileOrder() []string {
	return append([]string(nil), defaultMemoryFileOrder...)
}

func IsDeepSeekV4Model(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(m, DefaultModel) || strings.Contains(m, ProModel)
}

// ContextWindowForModel returns the context window size in tokens for model.
func ContextWindowForModel(model string) int {
	if strings.TrimSpace(model) == "" {
		return DefaultContextWindow
	}
	if IsDeepSeekV4Model(model) {
		return DeepSeekV4ContextWindow
	}
	return DefaultContextWindow
}
