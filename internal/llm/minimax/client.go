package minimax

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/defaults"
	"github.com/usewhale/whale/internal/llm"
	"github.com/usewhale/whale/internal/llm/deepseek"
	llmretry "github.com/usewhale/whale/internal/llm/retry"
)

const (
	DefaultBaseURL   = "https://api.minimax.io/v1"
	CNBaseURL        = "https://api.minimaxi.com/v1"
	DefaultAPIKeyEnv = "MINIMAX_API_KEY"
	DefaultModel     = "MiniMax-M3"
)

type Options struct {
	APIKey            string
	APIKeyEnv         string
	BaseURL           string
	Model             string
	ThinkingEnabled   bool
	MaxTokens         int
	RetryPolicy       llmretry.Policy
	StreamMaxAttempts int
	StreamIdleTimeout time.Duration
}

func New(opts Options) (llm.Provider, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	apiKeyEnv := strings.TrimSpace(opts.APIKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = DefaultAPIKeyEnv
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
	}
	if apiKey == "" {
		return nil, errors.New("MiniMax API key is not configured. Set MINIMAX_API_KEY in your environment or save one in Whale credentials")
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = DefaultModel
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	thinkingEnabled := opts.ThinkingEnabled || defaults.IsMiniMaxM27Model(model)
	thinkingConfig := deepseek.ChatCompletionsThinkingConfig{
		EnabledType:    "adaptive",
		ReasoningSplit: true,
	}
	if defaults.IsMiniMaxM27Model(model) {
		thinkingConfig.Omit = true
	}
	dsOpts := []deepseek.Option{
		deepseek.WithAPIKey(apiKey),
		deepseek.WithBaseURL(baseURL),
		deepseek.WithModel(model),
		deepseek.WithThinking(thinkingEnabled),
		deepseek.WithChatCompletionsThinking(thinkingConfig),
	}
	if defaults.IsMiniMaxM3Model(model) {
		dsOpts = append(dsOpts, deepseek.WithMultimodal(deepseek.MultimodalConfig{
			Enabled: true,
			Compat:  "openai",
			BaseURL: baseURL,
			APIKey:  apiKey,
			Model:   model,
		}))
	}
	if hasRetryPolicy(opts.RetryPolicy) {
		dsOpts = append(dsOpts, deepseek.WithRetryPolicy(opts.RetryPolicy))
	}
	if opts.StreamMaxAttempts > 0 {
		dsOpts = append(dsOpts, deepseek.WithStreamMaxAttempts(opts.StreamMaxAttempts))
	}
	if opts.StreamIdleTimeout > 0 {
		dsOpts = append(dsOpts, deepseek.WithStreamIdleTimeout(opts.StreamIdleTimeout))
	}
	if opts.MaxTokens > 0 {
		dsOpts = append(dsOpts, deepseek.WithMaxTokens(opts.MaxTokens))
	}
	return deepseek.New(dsOpts...)
}

func hasRetryPolicy(policy llmretry.Policy) bool {
	return policy.MaxAttempts != 0 ||
		policy.BaseDelay != 0 ||
		policy.MaxDelay != 0 ||
		policy.Jitter != 0 ||
		policy.RespectRetryAfter ||
		policy.RetryNetwork ||
		policy.RetryStatusCodes != nil
}
