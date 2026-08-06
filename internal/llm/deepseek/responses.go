package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/usewhale/whale/internal/compact"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm"
	llmretry "github.com/usewhale/whale/internal/llm/retry"
)

// WebSearchMode controls where web search runs: locally via Whale's own
// web_search tool (chat completions), or server-side via DeepSeek's Responses
// API built-in web_search tool.
type WebSearchMode string

const (
	// WebSearchModeLocal keeps the current behavior: chat completions plus
	// Whale's local web_search tool (DuckDuckGo/Bing scraping).
	WebSearchModeLocal WebSearchMode = "local"
	// WebSearchModeServer forces the Responses API backend with the built-in
	// web_search tool. Only deepseek-v4-flash supports the Responses API.
	WebSearchModeServer WebSearchMode = "server"
	// WebSearchModeAuto uses the Responses API backend when the model supports
	// it (deepseek-v4-flash) and falls back to chat completions otherwise.
	WebSearchModeAuto WebSearchMode = "auto"
)

const (
	responsesEndpointPath = "/responses"
	// responsesCapableModelPrefix is the model family that currently supports
	// DeepSeek's Responses API (per official docs, only deepseek-v4-flash).
	responsesCapableModelPrefix = "deepseek-v4-flash"
)

// API selects which DeepSeek endpoint the client speaks: the Responses API
// (POST /responses) or the chat completions API (POST /chat/completions).
// The API is a transport property, decoupled from the model: the model still
// picks the payload shape, but the API picks where it is sent.
type API string

const (
	// APIAuto (the zero value) infers the transport from the model name (and
	// web_search mode): deepseek-v4-flash uses the Responses API, any other
	// model uses chat completions.
	APIAuto API = ""
	// APIResponses forces the Responses API (POST /responses) for any model.
	APIResponses API = "responses"
	// APIChatCompletions forces the chat completions API
	// (POST /chat/completions) for any model.
	APIChatCompletions API = "chat_completions"
)

// NormalizeAPI parses and validates an API selection string. Exactly three
// values are accepted — "responses", "chat_completions", "auto" — plus the
// empty string, which normalizes to APIAuto. Anything else is an error: there
// is no bare /completions endpoint (only /responses and /chat/completions),
// so aliases like "completions" or "chat" would name nothing and re-introduce
// the silent model-name fallback this selection deletes. One canonical
// spelling per value, no synonyms.
func NormalizeAPI(v string) (API, error) {
	switch API(strings.ToLower(strings.TrimSpace(v))) {
	case "", "auto":
		return APIAuto, nil
	case APIResponses:
		return APIResponses, nil
	case APIChatCompletions:
		return APIChatCompletions, nil
	default:
		return APIAuto, fmt.Errorf("invalid api %q: must be \"responses\", \"chat_completions\", or \"auto\"", v)
	}
}

// ResolveTransport reconciles an explicit API selection with the web_search
// mode. The api knob is the transport authority: web_search = "server" is at
// best an inference that the Responses API is in use, so it can imply the
// Responses API but never overrides an explicit chat_completions choice. The
// one conflict — explicit chat completions plus server-side search — degrades
// web_search to local with a warning rather than refusing to start (matching
// the codebase's existing soft-degrade precedent). The returned warning is
// empty when no adjustment was needed.
func ResolveTransport(api API, webSearch WebSearchMode) (API, WebSearchMode, string) {
	switch api {
	case APIChatCompletions:
		if webSearch == WebSearchModeServer {
			return api, WebSearchModeLocal, "web_search=\"server\" requires the Responses API; degrading to web_search=\"local\" because the API is \"chat_completions\""
		}
		return api, webSearch, ""
	case APIAuto:
		if webSearch == WebSearchModeServer {
			return APIResponses, webSearch, ""
		}
		return api, webSearch, ""
	default:
		return api, webSearch, ""
	}
}

// NormalizeWebSearchMode parses and validates a web search mode string. An
// empty value normalizes to WebSearchModeAuto: deepseek-v4-flash uses
// DeepSeek's server-side search out of the box, other models keep the local
// web_search tool.
func NormalizeWebSearchMode(v string) (WebSearchMode, error) {
	switch WebSearchMode(strings.ToLower(strings.TrimSpace(v))) {
	case "", WebSearchModeAuto:
		return WebSearchModeAuto, nil
	case WebSearchModeLocal:
		return WebSearchModeLocal, nil
	case WebSearchModeServer:
		return WebSearchModeServer, nil
	default:
		return WebSearchModeLocal, fmt.Errorf("invalid web_search mode %q: must be \"local\", \"server\", or \"auto\"", v)
	}
}

// responsesEnabled reports whether the client should use the Responses API
// backend for the current turn. An explicit api selection is the transport
// authority and wins unconditionally; the zero value (auto) falls back to the
// model-name heuristic, with web_search mode still governing: local search
// forces chat completions, server/auto use the Responses API when the model
// supports it.
func (c *Client) responsesEnabled() bool {
	switch c.api {
	case APIResponses:
		return true
	case APIChatCompletions:
		return false
	}
	switch c.webSearchMode {
	case WebSearchModeServer, WebSearchModeAuto, "":
		return isResponsesCapableModel(c.model)
	default:
		return false
	}
}

func isResponsesCapableModel(model string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), responsesCapableModelPrefix)
}

// webSearchCallRegistry remembers web_search_call items per session together
// with the assistant response that produced them. DeepSeek requires search
// calls to remain in the same thinking/tool group as their reasoning_text;
// replaying them elsewhere either revives a stale task or produces a 400.
//
// The registry is in-memory only: after a restart the items are missing and
// the server simply re-searches, which stays correct.
type webSearchCallReplay struct {
	assistantKey string
	items        []map[string]any
}

type webSearchCallRegistry struct {
	mu        sync.Mutex
	bySession map[string][]webSearchCallReplay
}

func (r *webSearchCallRegistry) record(sessionID, assistantKey string, items []map[string]any) {
	if r == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(assistantKey) == "" || len(items) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bySession == nil {
		r.bySession = map[string][]webSearchCallReplay{}
	}
	r.bySession[sessionID] = append(r.bySession[sessionID], webSearchCallReplay{
		assistantKey: assistantKey,
		items:        items,
	})
}

func (r *webSearchCallRegistry) lookup(sessionID string) []webSearchCallReplay {
	if r == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	replays := r.bySession[sessionID]
	out := make([]webSearchCallReplay, len(replays))
	copy(out, replays)
	return out
}

func sessionIDForHistory(history []core.Message) string {
	for _, msg := range history {
		if strings.TrimSpace(msg.SessionID) != "" {
			return msg.SessionID
		}
	}
	return ""
}

// streamResponses drives a turn through DeepSeek's Responses API
// (POST /responses) with the built-in web_search tool. The local web_search
// function is translated to the server-side tool, so the model never issues a
// local search call and the agent loop never dispatches one.
func (c *Client) streamResponses(ctx context.Context, history []core.Message, tools []core.Tool, out chan<- llm.ProviderEvent) error {
	sessionID := sessionIDForHistory(history)
	payload := map[string]any{
		"model":  c.model,
		"input":  toResponsesInputItems(history, c.searchCalls.lookup(sessionID)),
		"stream": true,
		"store":  false,
		"reasoning": map[string]any{
			"effort": c.responsesReasoningEffort(),
		},
	}
	if len(tools) > 0 {
		payload["tools"] = toResponsesTools(tools, c.webSearchMode == WebSearchModeLocal)
		payload["tool_choice"] = "auto"
	}
	if c.maxTokens > 0 {
		payload["max_output_tokens"] = c.maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal responses payload: %w", err)
	}
	return c.streamResponsesWithRetries(ctx, c.baseURL, body, sessionID, out)
}

// responsesReasoningEffort maps Whale's thinking toggle to the Responses API
// reasoning.effort value. The Responses API enables thinking by default, so an
// explicit "none" is required when thinking is off.
func (c *Client) responsesReasoningEffort() string {
	if !c.thinkingEnabled {
		return "none"
	}
	if strings.TrimSpace(c.reasoningEffort) != "" {
		return c.reasoningEffort
	}
	return "high"
}

// toResponsesTools translates Whale's tool list to Responses API tools. The
// Responses API uses the OpenAI Responses function shape (name at the top
// level) rather than the chat completions shape (name nested under function).
//
// The web_search tool is mode-dependent: in server/auto mode it becomes the
// server-side built-in tool; in local mode it stays a regular function tool so
// Whale's tool system executes it (DuckDuckGo/Bing) and feeds the result back
// — local search on the Responses transport, matching the chat-completions
// path. Previously toResponsesTools always translated web_search to the
// built-in, which silently turned "local" search into server-side search as
// soon as the transport was explicitly set to responses.
func toResponsesTools(tools []core.Tool, localWebSearch bool) []map[string]any {
	out := make([]map[string]any, 0, len(tools)+1)
	hasWebSearch := false
	for _, t := range tools {
		if strings.TrimSpace(t.Name()) == "web_search" {
			if localWebSearch {
				// Keep it as a regular function tool: the tool registry runs
				// the local search and the result returns as
				// function_call_output, exactly like the chat-completions path.
				spec := core.DescribeTool(t)
				if strings.TrimSpace(spec.Name) != "" {
					out = append(out, map[string]any{
						"type":        "function",
						"name":        core.DisplayToolName(spec.Name),
						"description": core.ApplyDisplayToolNames(spec.Description),
						"parameters":  core.FlattenSchemaForModel(spec.Parameters),
					})
				}
			} else {
				hasWebSearch = true
			}
			continue
		}
		spec := core.DescribeTool(t)
		if strings.TrimSpace(spec.Name) == "" {
			continue
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        core.DisplayToolName(spec.Name),
			"description": core.ApplyDisplayToolNames(spec.Description),
			"parameters":  core.FlattenSchemaForModel(spec.Parameters),
		})
	}
	if hasWebSearch {
		out = append(out, map[string]any{"type": "web_search"})
	}
	return out
}

// toResponsesInputItems converts Whale's message history into Responses API
// input items. system/user/assistant messages map to message items, tool calls
// map to function_call items, tool results map to function_call_output items,
// and previously recorded web_search_call items are restored inside the
// assistant thinking/tool group that originally produced them.
func toResponsesInputItems(history []core.Message, searchReplays []webSearchCallReplay) []map[string]any {
	out := make([]map[string]any, 0, len(history)*2)
	replayUsed := make([]bool, len(searchReplays))
	searchCallsForAssistant := func(msg core.Message) []map[string]any {
		key := responsesAssistantReplayKey(core.MessagePlainText(msg), msg.Reasoning, msg.ToolCalls)
		for i, replay := range searchReplays {
			if replayUsed[i] || replay.assistantKey != key {
				continue
			}
			replayUsed[i] = true
			return replay.items
		}
		return nil
	}
	syntheticCallCount := 0
	// Keep every function_call from one assistant turn adjacent. DeepSeek
	// merges adjacent reasoning/function_call items into a single assistant
	// message. Interleaving a function_call_output between parallel calls
	// splits that assistant message, so the later call loses the turn's
	// reasoning_text and thinking mode rejects the request.
	type pendingCall struct {
		id      string
		removed bool
	}
	var pending []*pendingCall
	pendingByID := map[string]*pendingCall{}
	flushMissing := func() {
		for _, pc := range pending {
			if pc.removed {
				continue
			}
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": pc.id,
				"output":  `{"success":false,"error":"missing tool result recovered before provider send","code":"missing_tool_result_recovered"}`,
			})
		}
		pending = nil
		pendingByID = map[string]*pendingCall{}
	}
	for _, msg := range history {
		switch msg.Role {
		case core.RoleSystem:
			flushMissing()
			out = append(out, responsesMessageItem("system", core.MessagePlainText(msg)))
		case core.RoleUser:
			flushMissing()
			out = append(out, responsesMessageItem("user", core.MessagePlainText(msg)))
		case core.RoleAssistant:
			searchCalls := searchCallsForAssistant(msg)
			// Reasoning is replayable only when it belongs to a tool-call turn.
			// Plain assistant reasoning is private scratch state, and a failed
			// reasoning-only stream is especially dangerous: replaying it makes
			// the next request continue the stale task even after a new user
			// message. Server-side search is also a tool call even though it is
			// not represented in msg.ToolCalls.
			if strings.TrimSpace(core.MessagePlainText(msg)) == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			flushMissing()
			if text := strings.TrimSpace(core.MessagePlainText(msg)); text != "" {
				out = append(out, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": []map[string]any{{"type": "output_text", "text": text}},
				})
			}
			if (len(msg.ToolCalls) > 0 || len(searchCalls) > 0) && strings.TrimSpace(msg.Reasoning) != "" {
				out = append(out, map[string]any{
					"type":    "reasoning",
					"content": []map[string]any{{"type": "reasoning_text", "text": msg.Reasoning}},
				})
			}
			out = append(out, searchCalls...)
			for _, tc := range msg.ToolCalls {
				id := strings.TrimSpace(tc.ID)
				if id == "" {
					syntheticCallCount++
					id = fmt.Sprintf("whale_synthetic_call_%d", syntheticCallCount)
				}
				pc := &pendingCall{
					id: id,
				}
				pending = append(pending, pc)
				pendingByID[id] = pc
				out = append(out, map[string]any{
					"type":      "function_call",
					"call_id":   id,
					"name":      core.DisplayToolName(tc.Name),
					"arguments": tc.Input,
				})
			}
		case core.RoleTool:
			for _, tr := range msg.ToolResults {
				pc := pendingByID[tr.ToolCallID]
				if pc == nil || pc.removed {
					continue
				}
				out = append(out, map[string]any{
					"type":    "function_call_output",
					"call_id": pc.id,
					"output":  compact.ToolResultReplayContent(core.ToolResultModelText(tr)),
				})
				pc.removed = true
				delete(pendingByID, pc.id)
			}
		}
	}
	flushMissing()
	return out
}

func responsesAssistantReplayKey(content, reasoning string, calls []core.ToolCall) string {
	type replayCall struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Input string `json:"input"`
	}
	type replayIdentity struct {
		Content   string       `json:"content"`
		Reasoning string       `json:"reasoning"`
		Calls     []replayCall `json:"calls,omitempty"`
	}
	identity := replayIdentity{
		Content:   strings.TrimSpace(content),
		Reasoning: strings.TrimSpace(reasoning),
		Calls:     make([]replayCall, 0, len(calls)),
	}
	for _, call := range calls {
		identity.Calls = append(identity.Calls, replayCall{
			ID:    strings.TrimSpace(call.ID),
			Name:  core.CanonicalToolName(call.Name),
			Input: strings.TrimSpace(call.Input),
		})
	}
	encoded, _ := json.Marshal(identity)
	return string(encoded)
}

func responsesMessageItem(role, text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": role,
		"content": []map[string]any{{
			"type": "input_text",
			"text": text,
		}},
	}
}

// streamResponsesWithRetries is the Responses API counterpart of
// streamWithRetriesAuth: same retry policy, Responses endpoint, and a
// Responses-specific SSE parser.
func (c *Client) streamResponsesWithRetries(ctx context.Context, requestBaseURL string, body []byte, sessionID string, out chan<- llm.ProviderEvent) error {
	policy := llmretry.NormalizePolicy(c.retryPolicy)
	requestAttempt := 1
	streamAttempt := 1
	for {
		resp, err := c.sendStreamRequestWithKeyPath(ctx, requestBaseURL, c.apiKey, body, responsesEndpointPath)
		if err != nil {
			var buildErr *requestBuildError
			if errors.As(err, &buildErr) {
				return err
			}
			if !llmretry.ShouldRetry(policy, err) || requestAttempt >= policy.MaxAttempts {
				return deepSeekRequestError(err, deepSeekMessageDiagnostics{})
			}
			delay := llmretry.Backoff(policy, requestAttempt, err)
			out <- retryScheduledEvent(requestAttempt, policy.MaxAttempts, delay, err, "request", false)
			requestAttempt++
			if sleepErr := c.retrySleeper(ctx, delay); sleepErr != nil {
				return sleepErr
			}
			continue
		}

		parseErr := c.parseResponsesStream(resp.Body, sessionID, out)
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

func (c *Client) parseResponsesStream(r io.ReadCloser, sessionID string, out chan<- llm.ProviderEvent) error {
	idleTimeout := c.streamIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultStreamIdleTimeout
	}
	done := make(chan struct{})
	defer close(done)
	defer r.Close()
	lines := readSSELines(r, done)
	var dataLines []string
	acc := &responsesAccumulator{callsByIndex: map[int]*responsesCallState{}}
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
			done, progressed, perr := parseResponsesData(strings.Join(dataLines, "\n"), c.model, sessionID, acc, c.searchCalls, out)
			if perr != nil {
				return streamError(perr, hadProgress || progressed)
			}
			if done {
				return nil
			}
			if progressed {
				hadProgress = true
				resetIdleTimer()
			}
			dataLines = dataLines[:0]
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) > 0 {
				done, progressed, perr := parseResponsesData(strings.Join(dataLines, "\n"), c.model, sessionID, acc, c.searchCalls, out)
				if perr != nil {
					return streamError(perr, hadProgress || progressed)
				}
				if done {
					return nil
				}
				if progressed {
					hadProgress = true
					resetIdleTimer()
				}
			}
			return streamError(errIncompleteStream, hadProgress)
		}
	}
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type responsesResponseBody struct {
	ID     string           `json:"id"`
	Status string           `json:"status"`
	Output []map[string]any `json:"output"`
	Usage  *responsesUsage  `json:"usage"`
}

type responsesSSEFrame struct {
	Type        string                 `json:"type"`
	Delta       string                 `json:"delta"`
	OutputIndex *int                   `json:"output_index"`
	Item        map[string]any         `json:"item"`
	Response    *responsesResponseBody `json:"response"`
}

type responsesCallState struct {
	id        string
	name      string
	arguments strings.Builder
}

type responsesAccumulator struct {
	content      strings.Builder
	reasoning    strings.Builder
	callsByIndex map[int]*responsesCallState
	usage        llm.Usage
}

func parseResponsesData(data, model, sessionID string, acc *responsesAccumulator, registry *webSearchCallRegistry, out chan<- llm.ProviderEvent) (bool, bool, error) {
	if strings.TrimSpace(data) == "" {
		return false, false, nil
	}
	var frame responsesSSEFrame
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return false, false, nil // skip malformed frame
	}
	progressed := false
	switch frame.Type {
	case "response.output_text.delta":
		if frame.Delta != "" {
			acc.content.WriteString(frame.Delta)
			out <- llm.ProviderEvent{Type: llm.EventContentDelta, Content: frame.Delta}
			progressed = true
		}
	case "response.reasoning_text.delta":
		if frame.Delta != "" {
			acc.reasoning.WriteString(frame.Delta)
			out <- llm.ProviderEvent{Type: llm.EventReasoningDelta, ReasoningDelta: frame.Delta}
			progressed = true
		}
	case "response.function_call_arguments.delta":
		idx := 0
		if frame.OutputIndex != nil {
			idx = *frame.OutputIndex
		}
		st := acc.callsByIndex[idx]
		if st == nil {
			st = &responsesCallState{}
			acc.callsByIndex[idx] = st
		}
		st.arguments.WriteString(frame.Delta)
		if st.name != "" && frame.Delta != "" {
			out <- llm.ProviderEvent{
				Type: llm.EventToolArgsDelta,
				ToolArgsDelta: &llm.ToolArgsDelta{
					ToolCallIndex: idx,
					ToolName:      st.name,
					ArgsDelta:     frame.Delta,
					ArgsChars:     st.arguments.Len(),
					ReadyCount:    len(acc.callsByIndex),
				},
			}
			progressed = true
		}
	case "response.output_item.added":
		// The item skeleton arrives here; function_call names are extracted so
		// later arguments deltas carry the tool name for progress rendering.
		item := frame.Item
		if item != nil && item["type"] == "function_call" {
			idx := 0
			if frame.OutputIndex != nil {
				idx = *frame.OutputIndex
			}
			st := acc.callsByIndex[idx]
			if st == nil {
				st = &responsesCallState{}
				acc.callsByIndex[idx] = st
			}
			if id, _ := item["call_id"].(string); id != "" {
				st.id = id
			}
			if name, _ := item["name"].(string); name != "" {
				st.name = core.CanonicalToolName(name)
			}
			out <- llm.ProviderEvent{Type: llm.EventToolUseStart, ToolCall: &core.ToolCall{ID: st.id, Name: st.name}}
		}
	case "response.failed":
		resp := frame.Response
		if resp == nil {
			return true, progressed, errors.New("responses stream ended without a response payload")
		}
		// Terminal and non-retryable: the server reported a definitive
		// failure; re-issuing the same request would fail again.
		return true, progressed, &responsesTerminalError{msg: "deepseek responses request failed: " + responsesStatusDetail(resp)}

	case "response.incomplete":
		// Server-side truncation. The payload is NOT authoritative: deltas may
		// have streamed tool calls the truncated output omits, and the
		// chat-completions path retries this class (errIncompleteStream).
		// Mirror it — retryable stream error, never a clean end_turn with the
		// accumulated calls dropped.
		return false, progressed, errIncompleteStream

	case "response.completed":
		resp := frame.Response
		if resp == nil {
			return true, progressed, errors.New("responses stream ended without a response payload")
		}
		// The completed payload is the authoritative output: deltas may have
		// been sparse or absent.
		content := responsesOutputText(resp.Output)
		calls := responsesFunctionCalls(resp.Output)
		if len(calls) == 0 && len(acc.callsByIndex) > 0 {
			// Sparse terminal payload: it omitted the function_call items the
			// deltas already streamed. Keep the accumulated calls instead of
			// dropping them (previously this emitted a clean end_turn with the
			// planned tool call silently discarded).
			calls = responsesAccumulatedCalls(acc.callsByIndex)
		}
		if content != "" {
			acc.content.Reset()
			acc.content.WriteString(content)
		}
		if len(calls) > 0 {
			acc.callsByIndex = map[int]*responsesCallState{}
			for i, call := range calls {
				acc.callsByIndex[i] = &responsesCallState{id: call.ID, name: call.Name}
			}
		}
		if resp.Usage != nil {
			acc.usage = llm.Usage{
				PromptTokens:         resp.Usage.InputTokens,
				CompletionTokens:     resp.Usage.OutputTokens,
				TotalTokens:          resp.Usage.TotalTokens,
				PromptCacheHitTokens: responsesCachedTokens(resp.Usage),
			}
		}
		if registry != nil && sessionID != "" {
			if items := responsesWebSearchCallItems(resp.Output); len(items) > 0 {
				registry.record(sessionID, responsesAssistantReplayKey(content, acc.reasoning.String(), calls), items)
			}
		}
		if err := emitResponsesComplete(out, model, acc, calls); err != nil {
			return true, progressed, err
		}
		return true, progressed, nil
	case "response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed":
		// Server-side search: intentionally silent in v1 — the model's answer
		// already incorporates the results and no local tool is dispatched.
		progressed = true
	case "response.created":
		// No-op; response is in progress.
	}
	return false, progressed, nil
}

// responsesTerminalError marks a definitive Responses API failure that must
// not be retried (the server already reported a terminal state).
type responsesTerminalError struct {
	msg string
}

func (e *responsesTerminalError) Error() string { return e.msg }

func responsesStatusDetail(resp *responsesResponseBody) string {
	if resp == nil {
		return "empty response"
	}
	if resp.Status == "" {
		return "unknown status"
	}
	return resp.Status
}

func responsesCachedTokens(u *responsesUsage) int {
	if u == nil || u.InputTokensDetails == nil {
		return 0
	}
	return u.InputTokensDetails.CachedTokens
}

func emitResponsesComplete(out chan<- llm.ProviderEvent, model string, acc *responsesAccumulator, calls []core.ToolCall) error {
	content := acc.content.String()
	reasoning := acc.reasoning.String()
	if len(calls) == 0 && strings.TrimSpace(content) == "" {
		return &streamTerminalError{msg: "DeepSeek responses stream ended without assistant content or tool calls"}
	}
	finishReason := core.FinishReasonEndTurn
	if len(calls) > 0 {
		finishReason = core.FinishReasonToolUse
	}
	out <- llm.ProviderEvent{Type: llm.EventComplete, Response: &llm.ProviderResponse{
		Content:      content,
		Reasoning:    reasoning,
		ToolCalls:    calls,
		Usage:        acc.usage,
		Model:        model,
		FinishReason: finishReason,
	}}
	return nil
}

// responsesOutputText extracts the concatenated output_text parts from
// assistant message items in a completed response's output list.
func responsesOutputText(output []map[string]any) string {
	var b strings.Builder
	for _, item := range output {
		typ, _ := item["type"].(string)
		if typ != "message" {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if part["type"] == "output_text" {
				if s, ok := part["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
	}
	return b.String()
}

// responsesFunctionCalls extracts function_call items from a completed
// response's output list, normalizing tool names back to Whale's internal
// names (mirrors the chat completions path).
// responsesAccumulatedCalls converts the delta-accumulated call states back
// to ToolCalls ordered by output index. Used when the terminal payload omits
// the function_call items (sparse/truncated output) so delta-streamed calls
// survive instead of being dropped.
func responsesAccumulatedCalls(byIndex map[int]*responsesCallState) []core.ToolCall {
	indices := make([]int, 0, len(byIndex))
	for idx := range byIndex {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	calls := make([]core.ToolCall, 0, len(indices))
	for _, idx := range indices {
		st := byIndex[idx]
		calls = append(calls, core.ToolCall{
			ID:    st.id,
			Name:  core.CanonicalToolName(st.name),
			Input: st.arguments.String(),
		})
	}
	return calls
}

func responsesFunctionCalls(output []map[string]any) []core.ToolCall {
	var calls []core.ToolCall
	for _, item := range output {
		typ, _ := item["type"].(string)
		if typ != "function_call" {
			continue
		}
		id, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		calls = append(calls, core.ToolCall{
			ID:    id,
			Name:  core.CanonicalToolName(name),
			Input: responsesCallArguments(item["arguments"]),
		})
	}
	return calls
}

func responsesCallArguments(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		// The API may return arguments as a parsed JSON object; stringify it.
		b, err := json.Marshal(s)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// responsesWebSearchCallItems extracts web_search_call items so they can be
// echoed back on later turns. Items are preserved as-is: the server requires
// the full item (id plus the action payload) to restore search context.
func responsesWebSearchCallItems(output []map[string]any) []map[string]any {
	var items []map[string]any
	for _, item := range output {
		typ, _ := item["type"].(string)
		if typ != "web_search_call" {
			continue
		}
		items = append(items, item)
	}
	return items
}
