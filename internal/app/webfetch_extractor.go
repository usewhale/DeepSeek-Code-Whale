package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm"
	"github.com/usewhale/whale/internal/llm/deepseek"
)

const webFetchExtractorModel = "deepseek-v4-flash"

type webFetchExtractorOptions struct {
	APIKey  string
	BaseURL string
	API     deepseek.API
}

type deepSeekWebFetchExtractor struct {
	opts webFetchExtractorOptions
}

func newDeepSeekWebFetchExtractor(opts webFetchExtractorOptions) *deepSeekWebFetchExtractor {
	return &deepSeekWebFetchExtractor{opts: opts}
}

func (e *deepSeekWebFetchExtractor) Extract(ctx context.Context, prompt, content string) (string, error) {
	dsOpts := []deepseek.Option{
		deepseek.WithAPIKey(e.opts.APIKey),
		deepseek.WithModel(webFetchExtractorModel),
		deepseek.WithThinking(false),
		deepseek.WithMaxTokens(4096),
		deepseek.WithStreamIdleTimeout(45 * time.Second),
	}
	if strings.TrimSpace(e.opts.BaseURL) != "" {
		dsOpts = append(dsOpts, deepseek.WithBaseURL(e.opts.BaseURL))
	}
	// Honor the user's explicit transport selection: the extractor pins its own
	// model/thinking/tokens, but the API (responses vs chat_completions) is a
	// config property — a chat-completions-only endpoint must not be sent
	// /responses requests behind the user's back.
	dsOpts = append(dsOpts, deepseek.WithAPI(e.opts.API))
	provider, err := deepseek.New(dsOpts...)
	if err != nil {
		return "", err
	}
	history := []core.Message{
		{
			Role: core.RoleSystem,
			Text: strings.Join([]string{
				"You extract answers from fetched web content for a coding agent.",
				"Use only the supplied fetched content.",
				"Be concise and preserve exact URLs, commands, identifiers, and version numbers when relevant.",
				"If the content does not answer the prompt, say that clearly and mention the most relevant available evidence.",
				"Do not include long verbatim copyrighted passages.",
			}, " "),
		},
		{
			Role: core.RoleUser,
			Text: fmt.Sprintf("Prompt:\n%s\n\nFetched content:\n%s", strings.TrimSpace(prompt), strings.TrimSpace(content)),
		},
	}
	var chunks []string
	for ev := range provider.StreamResponse(ctx, history, nil) {
		switch ev.Type {
		case llm.EventContentDelta:
			chunks = append(chunks, ev.Content)
		case llm.EventComplete:
			if ev.Response != nil && strings.TrimSpace(ev.Response.Content) != "" {
				return strings.TrimSpace(ev.Response.Content), nil
			}
			return strings.TrimSpace(strings.Join(chunks, "")), nil
		case llm.EventError:
			if ev.Err != nil {
				return "", ev.Err
			}
			return "", fmt.Errorf("web fetch extraction failed")
		}
	}
	out := strings.TrimSpace(strings.Join(chunks, ""))
	if out == "" {
		return "", fmt.Errorf("web fetch extraction returned empty content")
	}
	return out, nil
}
