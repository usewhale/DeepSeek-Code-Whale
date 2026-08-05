package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/compact"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/defaults"
	"github.com/usewhale/whale/internal/llm"
	llmretry "github.com/usewhale/whale/internal/llm/retry"
)

const (
	defaultBaseURL               = "https://api.deepseek.com"
	defaultStreamMaxAttempts     = 6
	defaultStreamIdleTimeout     = 90 * time.Second
	maxToolResultReplayTokens    = 2000
	maxToolResultReplayChars     = 12 * 1024
	compactedToolResultKeepRunes = 3000
	defaultImageDetail           = "auto"
)

var errIncompleteStream = errors.New("stream disconnected before completion")

type Client struct {
	apiKey                  string
	baseURL                 string
	httpClient              *http.Client
	model                   string
	reasoningEffort         string
	thinkingEnabled         bool
	maxTokens               int
	retryPolicy             llmretry.Policy
	retrySleeper            llmretry.Sleeper
	streamMaxAttempts       int
	streamIdleTimeout       time.Duration
	prefixCompletionEnabled bool
	multimodal              MultimodalConfig
	webSearchMode           WebSearchMode
	searchCalls             *webSearchCallRegistry
}

type Option func(*Client)

type MultimodalConfig struct {
	Enabled   bool
	Compat    string
	BaseURL   string
	APIKey    string
	APIKeyEnv string
	Model     string
}

func WithBaseURL(v string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(v, "/") }
}

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = key }
}

func WithReasoningEffort(v string) Option {
	return func(c *Client) { c.reasoningEffort = strings.ToLower(strings.TrimSpace(v)) }
}

func WithThinking(enabled bool) Option {
	return func(c *Client) { c.thinkingEnabled = enabled }
}

func WithMaxTokens(v int) Option {
	return func(c *Client) { c.maxTokens = v }
}

func WithPrefixCompletion(enabled bool) Option {
	return func(c *Client) { c.prefixCompletionEnabled = enabled }
}

func WithMultimodal(cfg MultimodalConfig) Option {
	return func(c *Client) {
		cfg.Compat = strings.ToLower(strings.TrimSpace(cfg.Compat))
		cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
		cfg.APIKey = strings.TrimSpace(cfg.APIKey)
		cfg.APIKeyEnv = strings.TrimSpace(cfg.APIKeyEnv)
		cfg.Model = strings.TrimSpace(cfg.Model)
		c.multimodal = cfg
	}
}

// WithWebSearchMode selects where web search runs: local (default, chat
// completions plus Whale's own web_search tool), server (Responses API with
// DeepSeek's built-in web_search), or auto (Responses API when the model
// supports it).
func WithWebSearchMode(mode WebSearchMode) Option {
	return func(c *Client) {
		if strings.TrimSpace(string(mode)) != "" {
			c.webSearchMode = mode
		}
	}
}

func WithRetryPolicy(policy llmretry.Policy) Option {
	return func(c *Client) { c.retryPolicy = llmretry.NormalizePolicy(policy) }
}

func WithStreamMaxAttempts(v int) Option {
	return func(c *Client) {
		if v > 0 {
			c.streamMaxAttempts = v
		}
	}
}

func WithStreamIdleTimeout(v time.Duration) Option {
	return func(c *Client) {
		if v > 0 {
			c.streamIdleTimeout = v
		}
	}
}

func withRetrySleeper(s llmretry.Sleeper) Option {
	return func(c *Client) {
		if s != nil {
			c.retrySleeper = s
		}
	}
}

func New(opts ...Option) (*Client, error) {
	c := &Client{
		baseURL: strings.TrimRight(envOr("DEEPSEEK_BASE_URL", defaultBaseURL), "/"),
		httpClient: &http.Client{
			Timeout: 11 * time.Minute,
		},
		model:             defaults.DefaultModel,
		reasoningEffort:   defaults.DefaultReasoningEffort,
		thinkingEnabled:   defaults.DefaultThinkingEnabled,
		retryPolicy:       llmretry.DefaultPolicy(),
		retrySleeper:      llmretry.Sleep,
		streamMaxAttempts: defaultStreamMaxAttempts,
		streamIdleTimeout: defaultStreamIdleTimeout,
		searchCalls:       &webSearchCallRegistry{},
	}
	for _, opt := range opts {
		opt(c)
	}
	if strings.TrimSpace(c.apiKey) == "" {
		c.apiKey = strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, errors.New("DeepSeek API key is not configured. Run `whale setup` to save one for Whale, or set `DEEPSEEK_API_KEY` in your environment")
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	c.retryPolicy = llmretry.NormalizePolicy(c.retryPolicy)
	if c.retrySleeper == nil {
		c.retrySleeper = llmretry.Sleep
	}
	if c.streamMaxAttempts <= 0 {
		c.streamMaxAttempts = defaultStreamMaxAttempts
	}
	if c.streamIdleTimeout <= 0 {
		c.streamIdleTimeout = defaultStreamIdleTimeout
	}
	return c, nil
}

func envOr(name, fallback string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	return v
}

func (c *Client) StreamResponse(ctx context.Context, history []core.Message, tools []core.Tool) <-chan llm.ProviderEvent {
	out := make(chan llm.ProviderEvent)
	go func() {
		defer close(out)
		if err := c.stream(ctx, history, tools, out); err != nil {
			out <- llm.ProviderEvent{Type: llm.EventError, Err: err}
		}
	}()
	return out
}

func (c *Client) StreamResponseWithPrefix(ctx context.Context, history []core.Message, prefix string, stop []string) <-chan llm.ProviderEvent {
	out := make(chan llm.ProviderEvent)
	go func() {
		defer close(out)
		if err := c.streamPrefix(ctx, history, prefix, stop, out); err != nil {
			out <- llm.ProviderEvent{Type: llm.EventError, Err: err}
		}
	}()
	return out
}

func (c *Client) stream(ctx context.Context, history []core.Message, tools []core.Tool, out chan<- llm.ProviderEvent) error {
	if latestUserMessageHasAttachments(history) {
		return c.streamMultimodal(ctx, history, tools, out)
	}
	if c.responsesEnabled() {
		return c.streamResponses(ctx, history, tools, out)
	}
	msgs, sanitizeDiag := sanitizeDeepSeekMessagesForRequest(toDeepSeekMessages(history), c.thinkingEnabled)
	replayDiag := toolResultReplayDiagnostics(history, msgs)
	payload := map[string]any{
		"model":          c.model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       msgs,
		"thinking":       map[string]any{"type": "disabled"},
	}
	if c.thinkingEnabled {
		payload["thinking"] = map[string]any{"type": "enabled"}
		if strings.TrimSpace(c.reasoningEffort) != "" {
			payload["reasoning_effort"] = c.reasoningEffort
		}
	}
	if len(tools) > 0 {
		payload["tools"] = toDeepSeekTools(tools)
	}
	if c.maxTokens > 0 {
		payload["max_tokens"] = c.maxTokens
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	return c.streamWithRetries(ctx, c.baseURL, body, msgs, sanitizeDiag, replayDiag, out)
}

func (c *Client) streamPrefix(ctx context.Context, history []core.Message, prefix string, stop []string, out chan<- llm.ProviderEvent) error {
	// An explicit prefix request (non-empty prefix) is honored regardless of the
	// prefixCompletionEnabled auto-flag: callers that pass a prefix are opting in
	// directly (e.g. plan-finalization recovery). The flag only governs implicit
	// use. Endpoint incompatibility still falls back via prefixCompletionBaseURL.
	if c.responsesEnabled() {
		// The Responses API has no prefix-completion endpoint; degrade to a
		// plain stream (tools are dropped the same way the attachments path
		// drops them).
		return c.stream(ctx, history, nil, out)
	}
	if latestUserMessageHasAttachments(history) || strings.TrimSpace(prefix) == "" {
		return c.stream(ctx, history, nil, out)
	}
	requestBaseURL, ok := c.prefixCompletionBaseURL()
	if !ok {
		return c.stream(ctx, history, nil, out)
	}
	msgs, sanitizeDiag := sanitizeDeepSeekMessagesForRequest(toDeepSeekMessages(history), c.thinkingEnabled)
	msgs = append(msgs, map[string]any{
		"role":    "assistant",
		"content": prefix,
		"prefix":  true,
	})
	replayDiag := toolResultReplayDiagnostics(history, msgs)
	payload := map[string]any{
		"model":          c.model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       msgs,
		"thinking":       map[string]any{"type": "disabled"},
	}
	if c.thinkingEnabled {
		payload["thinking"] = map[string]any{"type": "enabled"}
		if strings.TrimSpace(c.reasoningEffort) != "" {
			payload["reasoning_effort"] = c.reasoningEffort
		}
	}
	if len(stop) > 0 {
		payload["stop"] = append([]string(nil), stop...)
	}
	if c.maxTokens > 0 {
		payload["max_tokens"] = c.maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	inner := make(chan llm.ProviderEvent, 16)
	done := make(chan error, 1)
	go func() {
		done <- c.streamWithRetries(ctx, requestBaseURL, body, msgs, sanitizeDiag, replayDiag, inner)
		close(inner)
	}()
	prefixSent := false
	for ev := range inner {
		switch ev.Type {
		case llm.EventContentDelta:
			if !prefixSent {
				if strings.HasPrefix(ev.Content, prefix) {
					prefixSent = true
					out <- ev
					continue
				}
				out <- llm.ProviderEvent{Type: llm.EventContentDelta, Content: prefix}
				prefixSent = true
			}
			if ev.Content != "" {
				out <- ev
			}
		case llm.EventComplete:
			if ev.Response != nil {
				resp := *ev.Response
				resp.Content = joinPrefixCompletionContent(prefix, resp.Content)
				resp.Usage.PrefixCompletionRequests++
				ev.Response = &resp
				if !prefixSent && strings.TrimSpace(resp.Content) != "" {
					out <- llm.ProviderEvent{Type: llm.EventContentDelta, Content: prefix}
					prefixSent = true
				}
			}
			out <- ev
		default:
			out <- ev
		}
	}
	return <-done
}

func (c *Client) prefixCompletionBaseURL() (string, bool) {
	base := strings.TrimRight(strings.TrimSpace(c.baseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	if base == defaultBaseURL || base == defaultBaseURL+"/v1" {
		return defaultBaseURL + "/beta", true
	}
	if strings.HasSuffix(base, "/beta") {
		return base, true
	}
	return "", false
}

func joinPrefixCompletionContent(prefix, content string) string {
	if strings.HasPrefix(content, prefix) {
		return content
	}
	return prefix + content
}

func (c *Client) streamWithRetries(ctx context.Context, requestBaseURL string, body []byte, msgs []map[string]any, sanitizeDiag deepSeekMessageDiagnostics, replayDiag deepSeekReplayDiagnostics, out chan<- llm.ProviderEvent) error {
	return c.streamWithRetriesAuth(ctx, requestBaseURL, c.apiKey, c.model, body, msgs, sanitizeDiag, replayDiag, out)
}

func (c *Client) streamWithRetriesAuth(ctx context.Context, requestBaseURL, apiKey, model string, body []byte, msgs []map[string]any, sanitizeDiag deepSeekMessageDiagnostics, replayDiag deepSeekReplayDiagnostics, out chan<- llm.ProviderEvent) error {
	policy := llmretry.NormalizePolicy(c.retryPolicy)
	requestAttempt := 1
	streamAttempt := 1
	for {
		resp, err := c.sendStreamRequestWithKey(ctx, requestBaseURL, apiKey, body)
		if err != nil {
			var buildErr *requestBuildError
			if errors.As(err, &buildErr) {
				return err
			}
			if !llmretry.ShouldRetry(policy, err) || requestAttempt >= policy.MaxAttempts {
				return deepSeekRequestError(err, sanitizeDiag)
			}
			delay := llmretry.Backoff(policy, requestAttempt, err)
			out <- retryScheduledEvent(requestAttempt, policy.MaxAttempts, delay, err, "request", false)
			requestAttempt++
			if sleepErr := c.retrySleeper(ctx, delay); sleepErr != nil {
				return sleepErr
			}
			continue
		}

		parseErr := parseSSE(resp.Body, model, estimateReasoningReplayTokens(msgs), replayDiag, c.streamIdleTimeout, out)
		_ = resp.Body.Close()
		if parseErr == nil {
			return nil
		}
		if !shouldRetryStreamError(parseErr) || streamAttempt >= c.streamMaxAttempts {
			return parseErr
		}
		delay := llmretry.Backoff(policy, streamAttempt, parseErr)
		out <- retryScheduledEvent(streamAttempt, c.streamMaxAttempts, delay, parseErr, "stream", true)
		streamAttempt++
		if sleepErr := c.retrySleeper(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
}

func (c *Client) sendStreamRequest(ctx context.Context, requestBaseURL string, body []byte) (*http.Response, error) {
	return c.sendStreamRequestWithKey(ctx, requestBaseURL, c.apiKey, body)
}

func (c *Client) sendStreamRequestWithKey(ctx context.Context, requestBaseURL, apiKey string, body []byte) (*http.Response, error) {
	return c.sendStreamRequestWithKeyPath(ctx, requestBaseURL, apiKey, body, "/chat/completions")
}

func (c *Client) sendStreamRequestWithKeyPath(ctx context.Context, requestBaseURL, apiKey string, body []byte, path string) (*http.Response, error) {
	requestBaseURL = strings.TrimRight(strings.TrimSpace(requestBaseURL), "/")
	if requestBaseURL == "" {
		requestBaseURL = c.baseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, &requestBuildError{err: fmt.Errorf("new request: %w", err)}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		return nil, &llmretry.HTTPError{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       string(b),
		}
	}
	return resp, nil
}

type requestBuildError struct {
	err error
}

func (e *requestBuildError) Error() string {
	return e.err.Error()
}

func (e *requestBuildError) Unwrap() error {
	return e.err
}

func deepSeekRequestError(err error, diag deepSeekMessageDiagnostics) error {
	var httpErr *llmretry.HTTPError
	if errors.As(err, &httpErr) {
		msg := fmt.Sprintf("deepseek %d: %s", httpErr.StatusCode, strings.TrimSpace(httpErr.Body))
		if httpErr.StatusCode == http.StatusBadRequest && !diag.empty() {
			msg += " (" + diag.String() + ")"
		}
		return errors.New(msg)
	}
	return err
}

func retryStatusCode(err error) int {
	var httpErr *llmretry.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

func retryScheduledEvent(attempt, maxAttempts int, delay time.Duration, err error, stage string, streamReset bool) llm.ProviderEvent {
	return llm.ProviderEvent{
		Type: llm.EventRetryScheduled,
		Retry: &llmretry.Info{
			Attempt:     attempt,
			MaxAttempts: maxAttempts,
			Delay:       delay,
			StatusCode:  retryStatusCode(err),
			Reason:      retryReason(err, stage),
			Stage:       stage,
			StreamReset: streamReset,
		},
	}
}

func retryReason(err error, stage string) string {
	if stage == "stream" {
		if errors.Is(err, llm.ErrEmptyCompletion) {
			return "model returned no content"
		}
		return "API stream disconnected"
	}
	return llmretry.Reason(err)
}

func shouldRetryStreamError(err error) bool {
	if err == nil {
		return false
	}
	// A reasoning-only empty completion (the model thought but emitted no
	// assistant content and no tool calls) produced nothing usable and was not
	// billed for output tokens, so re-issuing the same request is safe and
	// usually succeeds — the empty body is DeepSeek-side sampling variance, not a
	// deterministic rejection. In Plan mode this is the "couldn't make plan"
	// failure: the turn dies before the model writes its plan reply. It arrives
	// wrapped as a progress/terminal error (reasoning deltas mark the stream as
	// progressed), so check it before the blanket progress/terminal short-circuit
	// below. The stream-retry path resets accumulated reasoning/text via the
	// StreamReset event, so re-streaming does not duplicate output.
	var terminalErr *streamTerminalError
	if errors.As(err, &terminalErr) && terminalErr.retryable {
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	var progressErr *streamProgressError
	if errors.As(err, &progressErr) {
		return false
	}
	var responsesErr *responsesTerminalError
	if errors.As(err, &responsesErr) {
		return false
	}
	if errors.As(err, &terminalErr) {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

type streamProgressError struct {
	err error
}

func (e *streamProgressError) Error() string {
	return e.err.Error()
}

func (e *streamProgressError) Unwrap() error {
	return e.err
}

type streamStallError struct {
	timeout time.Duration
}

func (e *streamStallError) Error() string {
	return fmt.Sprintf("DeepSeek stream stalled after %s without model output", e.timeout)
}

type streamTerminalError struct {
	msg string
	// retryable marks the reasoning-only empty completion (the model thought but
	// emitted no answer) — DeepSeek-side sampling variance that a re-issue of the
	// same request usually clears. A fully-empty completion (no reasoning either)
	// is left non-retryable: it signals a malformed/broken stream where a re-issue
	// would not help.
	retryable bool
}

func (e *streamTerminalError) Error() string {
	return e.msg
}

// Unwrap exposes ErrEmptyCompletion so callers can recognize a terminal-empty
// completion (the only condition under which this error is produced) without
// importing this package's unexported error type.
func (e *streamTerminalError) Unwrap() error {
	return llm.ErrEmptyCompletion
}

func streamError(err error, hadProgress bool) error {
	if err == nil || !hadProgress {
		return err
	}
	return &streamProgressError{err: err}
}

type sseLineResult struct {
	line string
	err  error
}

func readSSELines(r io.Reader, done <-chan struct{}) <-chan sseLineResult {
	ch := make(chan sseLineResult, 1)
	go func() {
		defer close(ch)
		reader := bufio.NewReader(r)
		for {
			line, err := reader.ReadString('\n')
			select {
			case ch <- sseLineResult{line: line, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

func parseSSE(r io.ReadCloser, model string, replayTokens int, replayDiag deepSeekReplayDiagnostics, idleTimeout time.Duration, out chan<- llm.ProviderEvent) error {
	if idleTimeout <= 0 {
		idleTimeout = defaultStreamIdleTimeout
	}
	done := make(chan struct{})
	defer close(done)
	defer r.Close()
	lines := readSSELines(r, done)
	var dataLines []string
	acc := streamAccumulator{callsByIndex: map[int]*toolCallState{}, readyIndices: map[int]bool{}, reasoningReplayTokens: replayTokens, replayDiag: replayDiag}
	hadProgress := false
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()
	resetIdleTimer := func() {
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(idleTimeout)
	}
	for {
		var res sseLineResult
		var ok bool
		select {
		case res, ok = <-lines:
			if !ok {
				return streamError(errIncompleteStream, hadProgress)
			}
		case <-idleTimer.C:
			_ = r.Close()
			return streamError(&streamStallError{timeout: idleTimeout}, hadProgress)
		}
		line, err := res.line, res.err
		if err != nil && !errors.Is(err, io.EOF) {
			return streamError(err, hadProgress)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
		if trimmed == "" && len(dataLines) > 0 {
			data := strings.Join(dataLines, "\n")
			if done, progressed, perr := parseSSEData(data, model, out, &acc); perr != nil {
				return streamError(perr, hadProgress || progressed)
			} else if done {
				return nil
			} else if progressed {
				hadProgress = true
				resetIdleTimer()
			}
			dataLines = dataLines[:0]
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				if done, progressed, perr := parseSSEData(data, model, out, &acc); perr != nil {
					return streamError(perr, hadProgress || progressed)
				} else if done {
					return nil
				} else if progressed {
					hadProgress = true
					resetIdleTimer()
				}
			}
			return streamError(errIncompleteStream, hadProgress)
		}
	}
}

type sseChoiceDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type sseFrame struct {
	Choices []struct {
		Delta        sseChoiceDelta `json:"delta"`
		FinishReason string         `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens          int `json:"prompt_tokens"`
		CompletionTokens      int `json:"completion_tokens"`
		TotalTokens           int `json:"total_tokens"`
		PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	} `json:"usage"`
}

type toolCallState struct {
	id        string
	name      string
	arguments strings.Builder
	started   bool
}

type streamAccumulator struct {
	content               strings.Builder
	reasoning             strings.Builder
	callsByIndex          map[int]*toolCallState
	readyIndices          map[int]bool
	finishReason          string
	usage                 llm.Usage
	reasoningReplayTokens int
	replayDiag            deepSeekReplayDiagnostics
}

func parseSSEData(data, model string, out chan<- llm.ProviderEvent, acc *streamAccumulator) (bool, bool, error) {
	if data == "[DONE]" {
		if err := emitComplete(out, model, acc); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	if strings.TrimSpace(data) == "" {
		return false, false, nil
	}

	var frame sseFrame
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return false, false, nil // skip malformed frame
	}
	if len(frame.Choices) == 0 {
		// some providers emit usage-only terminal frames
		if frame.Usage.TotalTokens > 0 || frame.Usage.PromptTokens > 0 || frame.Usage.CompletionTokens > 0 {
			acc.usage = llm.Usage{
				PromptTokens:          frame.Usage.PromptTokens,
				CompletionTokens:      frame.Usage.CompletionTokens,
				TotalTokens:           frame.Usage.TotalTokens,
				PromptCacheHitTokens:  frame.Usage.PromptCacheHitTokens,
				PromptCacheMissTokens: frame.Usage.PromptCacheMissTokens,
			}
		}
		return false, false, nil
	}
	if frame.Usage.TotalTokens > 0 || frame.Usage.PromptTokens > 0 || frame.Usage.CompletionTokens > 0 {
		acc.usage = llm.Usage{
			PromptTokens:          frame.Usage.PromptTokens,
			CompletionTokens:      frame.Usage.CompletionTokens,
			TotalTokens:           frame.Usage.TotalTokens,
			PromptCacheHitTokens:  frame.Usage.PromptCacheHitTokens,
			PromptCacheMissTokens: frame.Usage.PromptCacheMissTokens,
		}
	}
	ch := frame.Choices[0]
	progressed := false
	if ch.FinishReason != "" {
		acc.finishReason = ch.FinishReason
	}
	if ch.Delta.Content != "" {
		acc.content.WriteString(ch.Delta.Content)
		out <- llm.ProviderEvent{Type: llm.EventContentDelta, Content: ch.Delta.Content}
		progressed = true
	}
	if ch.Delta.ReasoningContent != "" {
		acc.reasoning.WriteString(ch.Delta.ReasoningContent)
		out <- llm.ProviderEvent{
			Type:           llm.EventReasoningDelta,
			ReasoningDelta: ch.Delta.ReasoningContent,
		}
		progressed = true
	}
	for _, tc := range ch.Delta.ToolCalls {
		progressed = true
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}
		st, ok := acc.callsByIndex[idx]
		if !ok {
			st = &toolCallState{}
			acc.callsByIndex[idx] = st
		}
		if tc.ID != "" {
			st.id = tc.ID
		}
		if tc.Function.Name != "" {
			// Normalize the model-facing name (conventional names like "Bash"/
			// "Read" plus invented CLI aliases) back to whale's internal tool
			// name before it reaches the registry, routing, policy, or storage.
			st.name = core.CanonicalToolName(tc.Function.Name)
		}
		if tc.Function.Arguments != "" {
			st.arguments.WriteString(tc.Function.Arguments)
		}
		if st.name != "" {
			if !acc.readyIndices[idx] && looksLikeCompleteJSON(st.arguments.String()) {
				acc.readyIndices[idx] = true
			}
			out <- llm.ProviderEvent{
				Type: llm.EventToolArgsDelta,
				ToolArgsDelta: &llm.ToolArgsDelta{
					ToolCallIndex: idx,
					ToolName:      st.name,
					ArgsDelta:     tc.Function.Arguments,
					ArgsChars:     st.arguments.Len(),
					ReadyCount:    len(acc.readyIndices),
				},
			}
		}
		if !st.started && (st.id != "" || st.name != "") {
			st.started = true
			out <- llm.ProviderEvent{Type: llm.EventToolUseStart, ToolCall: &core.ToolCall{ID: st.id, Name: st.name}}
		}
	}
	if ch.FinishReason == "tool_calls" {
		out <- llm.ProviderEvent{Type: llm.EventToolUseStop}
		if err := emitComplete(out, model, acc); err != nil {
			return false, progressed, err
		}
		return true, progressed, nil
	}
	return false, progressed, nil
}

func emitComplete(out chan<- llm.ProviderEvent, model string, acc *streamAccumulator) error {
	calls := make([]core.ToolCall, 0, len(acc.callsByIndex))
	for i := 0; i < len(acc.callsByIndex); i++ {
		st := acc.callsByIndex[i]
		if st == nil {
			continue
		}
		calls = append(calls, core.ToolCall{ID: st.id, Name: st.name, Input: st.arguments.String()})
	}
	content := acc.content.String()
	reasoning := acc.reasoning.String()
	if len(calls) == 0 && strings.TrimSpace(content) == "" {
		if strings.TrimSpace(reasoning) != "" {
			return &streamTerminalError{msg: "DeepSeek stream ended with reasoning but no assistant content or tool calls", retryable: true}
		}
		return &streamTerminalError{msg: "DeepSeek stream ended without assistant content or tool calls"}
	}
	out <- llm.ProviderEvent{Type: llm.EventComplete, Response: &llm.ProviderResponse{
		Content:   content,
		Reasoning: reasoning,
		ToolCalls: calls,
		Usage: llm.Usage{
			PromptTokens:           acc.usage.PromptTokens,
			CompletionTokens:       acc.usage.CompletionTokens,
			TotalTokens:            acc.usage.TotalTokens,
			PromptCacheHitTokens:   acc.usage.PromptCacheHitTokens,
			PromptCacheMissTokens:  acc.usage.PromptCacheMissTokens,
			ReasoningReplayTokens:  acc.reasoningReplayTokens,
			ToolResultRawChars:     acc.replayDiag.rawChars,
			ToolResultReplayChars:  acc.replayDiag.replayChars,
			ToolResultRawTokens:    acc.replayDiag.rawTokens,
			ToolResultReplayTokens: acc.replayDiag.replayTokens,
			ToolResultTokensSaved:  acc.replayDiag.tokensSaved(),
			ToolResultsCompacted:   acc.replayDiag.compacted,
		},
		Model:        model,
		FinishReason: mapFinishReason(acc.finishReason),
	}}
	return nil
}

func estimateReasoningReplayTokens(messages []map[string]any) int {
	if len(messages) == 0 {
		return 0
	}
	var chars int
	for _, m := range messages {
		role, _ := m["role"].(string)
		if role != "assistant" {
			continue
		}
		rsn, _ := m["reasoning_content"].(string)
		if strings.TrimSpace(rsn) == "" {
			continue
		}
		chars += len(rsn)
	}
	if chars <= 0 {
		return 0
	}
	return chars / 4
}

type deepSeekReplayDiagnostics struct {
	rawChars     int
	replayChars  int
	rawTokens    int
	replayTokens int
	compacted    int
}

func (d deepSeekReplayDiagnostics) tokensSaved() int {
	if d.rawTokens <= d.replayTokens {
		return 0
	}
	return d.rawTokens - d.replayTokens
}

func toolResultReplayDiagnostics(history []core.Message, messages []map[string]any) deepSeekReplayDiagnostics {
	diag, rawByCallID := rawToolResultReplayDiagnosticsWithContent(history)
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role != "tool" {
			continue
		}
		toolCallID, _ := msg["tool_call_id"].(string)
		content, _ := msg["content"].(string)
		diag.replayChars += len(content)
		diag.replayTokens += compact.EstimateTokens(content)
		if raw, ok := rawByCallID[toolCallID]; ok && content != raw {
			diag.compacted++
		}
	}
	return diag
}

func rawToolResultReplayDiagnostics(history []core.Message) deepSeekReplayDiagnostics {
	diag, _ := rawToolResultReplayDiagnosticsWithContent(history)
	return diag
}

func rawToolResultReplayDiagnosticsWithContent(history []core.Message) (deepSeekReplayDiagnostics, map[string]string) {
	var diag deepSeekReplayDiagnostics
	rawByCallID := map[string]string{}
	pendingToolCalls := map[string]struct{}{}
	flushPending := func() {
		for id := range pendingToolCalls {
			delete(pendingToolCalls, id)
		}
	}
	for _, msg := range history {
		switch msg.Role {
		case core.RoleSystem, core.RoleUser:
			flushPending()
		case core.RoleAssistant:
			flushPending()
			for _, tc := range msg.ToolCalls {
				if strings.TrimSpace(tc.ID) != "" {
					pendingToolCalls[tc.ID] = struct{}{}
				}
			}
		case core.RoleTool:
			for _, tr := range msg.ToolResults {
				if _, ok := pendingToolCalls[tr.ToolCallID]; !ok {
					continue
				}
				rawTokens := compact.EstimateTokens(core.ToolResultModelText(tr))
				diag.rawChars += len(core.ToolResultModelText(tr))
				diag.rawTokens += rawTokens
				rawByCallID[tr.ToolCallID] = core.ToolResultModelText(tr)
				delete(pendingToolCalls, tr.ToolCallID)
			}
		}
	}
	flushPending()
	return diag, rawByCallID
}

func mapFinishReason(v string) core.FinishReason {
	switch v {
	case "tool_calls":
		return core.FinishReasonToolUse
	case "stop":
		return core.FinishReasonEndTurn
	default:
		return core.FinishReasonEndTurn
	}
}

func looksLikeCompleteJSON(s string) bool {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return false
	}
	var v any
	return json.Unmarshal([]byte(raw), &v) == nil
}

func toDeepSeekTools(tools []core.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if payload := core.ProviderToolPayload(t); payload != nil {
			out = append(out, payload)
		}
	}
	return out
}

func toDeepSeekMessages(history []core.Message) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	pendingToolCalls := map[string]struct{}{}
	flushPending := func() {
		for id := range pendingToolCalls {
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": id,
				"content":      `{"success":false,"error":"missing tool result recovered before provider send","code":"missing_tool_result_recovered"}`,
			})
			delete(pendingToolCalls, id)
		}
	}
	for _, msg := range history {
		switch msg.Role {
		case core.RoleSystem:
			flushPending()
			out = append(out, map[string]any{"role": "system", "content": core.MessagePlainText(msg)})
		case core.RoleUser:
			flushPending()
			out = append(out, map[string]any{"role": "user", "content": core.MessagePlainText(msg)})
		case core.RoleAssistant:
			// An assistant turn with no content, tool calls, or reasoning carries
			// no information (e.g. a recovered empty completion persisted before
			// plan-finalization recovery). Encoding it as an empty assistant
			// message is useless and some providers reject it, so drop it from
			// replayed/resumed history.
			if strings.TrimSpace(core.MessagePlainText(msg)) == "" && len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Reasoning) == "" {
				continue
			}
			flushPending()
			m := map[string]any{
				"role":              "assistant",
				"content":           core.MessagePlainText(msg),
				"reasoning_content": msg.Reasoning,
			}
			if len(msg.ToolCalls) > 0 {
				tcs := make([]map[string]any, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							// Replay prior tool calls under the model-facing name
							// so history stays consistent with the tool schema.
							"name":      core.DisplayToolName(tc.Name),
							"arguments": tc.Input,
						},
					})
					if strings.TrimSpace(tc.ID) != "" {
						pendingToolCalls[tc.ID] = struct{}{}
					}
				}
				m["tool_calls"] = tcs
			}
			out = append(out, m)
		case core.RoleTool:
			for _, tr := range msg.ToolResults {
				if _, ok := pendingToolCalls[tr.ToolCallID]; !ok {
					continue
				}
				out = append(out, map[string]any{
					"role":         "tool",
					"tool_call_id": tr.ToolCallID,
					"content":      compact.ToolResultReplayContent(core.ToolResultModelText(tr)),
				})
				delete(pendingToolCalls, tr.ToolCallID)
			}
		}
	}
	flushPending()
	return out
}

type deepSeekMessageDiagnostics struct {
	strippedReasoning         int
	preservedToolReasoning    int
	syntheticToolResults      int
	droppedStrayTools         int
	repairedMissingToolCallID int
}

func (d deepSeekMessageDiagnostics) empty() bool {
	return d.strippedReasoning == 0 &&
		d.preservedToolReasoning == 0 &&
		d.syntheticToolResults == 0 &&
		d.droppedStrayTools == 0 &&
		d.repairedMissingToolCallID == 0
}

func (d deepSeekMessageDiagnostics) String() string {
	parts := []string{
		fmt.Sprintf("stripped_reasoning=%d", d.strippedReasoning),
		fmt.Sprintf("preserved_tool_reasoning=%d", d.preservedToolReasoning),
		fmt.Sprintf("synthetic_tool_results=%d", d.syntheticToolResults),
		fmt.Sprintf("dropped_stray_tools=%d", d.droppedStrayTools),
		fmt.Sprintf("repaired_missing_tool_call_ids=%d", d.repairedMissingToolCallID),
	}
	return "message diagnostics: " + strings.Join(parts, " ")
}

func sanitizeDeepSeekMessagesForRequest(messages []map[string]any, thinkingEnabled bool) ([]map[string]any, deepSeekMessageDiagnostics) {
	out := make([]map[string]any, 0, len(messages))
	var diag deepSeekMessageDiagnostics
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		role, _ := msg["role"].(string)
		switch role {
		case "tool":
			diag.droppedStrayTools++
			continue
		case "assistant":
			calls, hasCalls := deepSeekToolCalls(msg)
			if !hasCalls {
				clean := cloneDeepSeekMessage(msg)
				if reasoningContentNonEmpty(clean) {
					diag.strippedReasoning++
				}
				delete(clean, "reasoning_content")
				out = append(out, clean)
				continue
			}

			clean := cloneDeepSeekMessage(msg)
			repairedCalls := cloneDeepSeekToolCalls(calls)
			needed := make([]string, 0, len(repairedCalls))
			for callIdx, call := range repairedCalls {
				id, _ := call["id"].(string)
				if strings.TrimSpace(id) == "" {
					id = fmt.Sprintf("whale_synthetic_call_%d_%d", i, callIdx)
					call["id"] = id
					diag.repairedMissingToolCallID++
				}
				needed = append(needed, id)
			}
			clean["tool_calls"] = repairedCalls
			if reasoningContentNonEmpty(clean) {
				diag.preservedToolReasoning++
			} else if thinkingEnabled {
				clean["reasoning_content"] = ""
			} else {
				delete(clean, "reasoning_content")
			}
			out = append(out, clean)

			next := i + 1
			for _, id := range needed {
				if next < len(messages) {
					nextMsg := messages[next]
					nextRole, _ := nextMsg["role"].(string)
					if nextRole == "tool" {
						toolID, _ := nextMsg["tool_call_id"].(string)
						if strings.TrimSpace(toolID) == id {
							out = append(out, cloneDeepSeekMessage(nextMsg))
							next++
							continue
						}
						if strings.TrimSpace(toolID) == "" {
							tool := cloneDeepSeekMessage(nextMsg)
							tool["tool_call_id"] = id
							out = append(out, tool)
							next++
							continue
						}
					}
				}
				out = append(out, syntheticMissingToolResult(id))
				diag.syntheticToolResults++
			}
			for next < len(messages) {
				nextRole, _ := messages[next]["role"].(string)
				if nextRole != "tool" {
					break
				}
				diag.droppedStrayTools++
				next++
			}
			i = next - 1
		default:
			out = append(out, cloneDeepSeekMessage(msg))
		}
	}
	return out, diag
}

func deepSeekToolCalls(msg map[string]any) ([]map[string]any, bool) {
	raw, ok := msg["tool_calls"]
	if !ok {
		return nil, false
	}
	switch calls := raw.(type) {
	case []map[string]any:
		if len(calls) == 0 {
			return nil, false
		}
		return calls, true
	case []any:
		out := make([]map[string]any, 0, len(calls))
		for _, item := range calls {
			call, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, call)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

func cloneDeepSeekMessage(msg map[string]any) map[string]any {
	out := make(map[string]any, len(msg))
	for k, v := range msg {
		out[k] = v
	}
	return out
}

func cloneDeepSeekToolCalls(calls []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		cloned := cloneDeepSeekMessage(call)
		if fn, ok := call["function"].(map[string]any); ok {
			cloned["function"] = cloneDeepSeekMessage(fn)
		}
		out = append(out, cloned)
	}
	return out
}

func reasoningContentNonEmpty(msg map[string]any) bool {
	reasoning, _ := msg["reasoning_content"].(string)
	return strings.TrimSpace(reasoning) != ""
}

func syntheticMissingToolResult(id string) map[string]any {
	return map[string]any{
		"role":         "tool",
		"tool_call_id": id,
		"content":      `{"success":false,"error":"missing tool result recovered before provider send","code":"missing_tool_result_recovered"}`,
	}
}
