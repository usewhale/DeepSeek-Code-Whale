package app

import (
	"os"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/llm"
	"github.com/usewhale/whale/internal/llm/deepseek"
	llmretry "github.com/usewhale/whale/internal/llm/retry"
)

type providerOptions struct {
	APIKey                   string
	BaseURL                  string
	Model                    string
	ReasoningEffort          string
	ThinkingEnabled          bool
	MaxTokens                int
	RetryPolicy              llmretry.Policy
	StreamMaxAttempts        int
	StreamIdleTimeout        time.Duration
	DeepSeekPrefixCompletion bool
	DeepSeekMultimodal       MultimodalProviderConfig
	DeepSeekWebSearch        deepseek.WebSearchMode
}

func newDeepSeekProvider(opts providerOptions) (llm.Provider, error) {
	dsOpts := []deepseek.Option{}
	if strings.TrimSpace(opts.APIKey) != "" {
		dsOpts = append(dsOpts, deepseek.WithAPIKey(opts.APIKey))
	}
	if strings.TrimSpace(opts.BaseURL) != "" {
		dsOpts = append(dsOpts, deepseek.WithBaseURL(opts.BaseURL))
	}
	if strings.TrimSpace(opts.Model) != "" {
		dsOpts = append(dsOpts, deepseek.WithModel(opts.Model))
	}
	dsOpts = append(dsOpts,
		deepseek.WithReasoningEffort(opts.ReasoningEffort),
		deepseek.WithThinking(opts.ThinkingEnabled),
	)
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
	if opts.DeepSeekPrefixCompletion {
		dsOpts = append(dsOpts, deepseek.WithPrefixCompletion(true))
	}
	if opts.DeepSeekMultimodal.Enabled {
		mm := opts.DeepSeekMultimodal
		apiKey := strings.TrimSpace(mm.APIKey)
		apiKeyEnv := strings.TrimSpace(mm.APIKeyEnv)
		if apiKey == "" && apiKeyEnv != "" {
			apiKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
		}
		if apiKey == "" && apiKeyEnv == "" {
			apiKey = opts.APIKey
		}
		dsOpts = append(dsOpts, deepseek.WithMultimodal(deepseek.MultimodalConfig{
			Enabled:   true,
			Compat:    mm.Compat,
			BaseURL:   mm.BaseURL,
			APIKey:    apiKey,
			APIKeyEnv: apiKeyEnv,
			Model:     mm.Model,
		}))
	}
	dsOpts = append(dsOpts, deepseek.WithWebSearchMode(opts.DeepSeekWebSearch))
	return deepseek.New(dsOpts...)
}

func retryPolicyFromConfig(cfg Config) llmretry.Policy {
	policy := llmretry.DefaultPolicy()
	if cfg.RetryMaxAttemptsExplicit && cfg.RetryMaxAttempts == 0 {
		policy.MaxAttempts = 1
	} else if cfg.RetryMaxAttempts > 0 {
		policy.MaxAttempts = cfg.RetryMaxAttempts
	}
	if cfg.RetryMaxDelay > 0 {
		policy.MaxDelay = cfg.RetryMaxDelay
	}
	return llmretry.NormalizePolicy(policy)
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
