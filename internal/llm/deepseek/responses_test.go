package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm"
)

func TestNormalizeWebSearchMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want WebSearchMode
		err  bool
	}{
		{in: "", want: WebSearchModeAuto},
		{in: "local", want: WebSearchModeLocal},
		{in: "LOCAL", want: WebSearchModeLocal},
		{in: "server", want: WebSearchModeServer},
		{in: "Server", want: WebSearchModeServer},
		{in: "auto", want: WebSearchModeAuto},
		{in: "bogus", err: true},
	} {
		got, err := NormalizeWebSearchMode(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("NormalizeWebSearchMode(%q): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeWebSearchMode(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeWebSearchMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResponsesWebSearchServerMode(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"delta\":\"根据搜索\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":5,\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"web_search_call\",\"id\":\"ws_1\"},{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"根据搜索，答案是42。\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"input_tokens_details\":{\"cached_tokens\":4},\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithThinking(false),
		WithWebSearchMode(WebSearchModeServer),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	events := c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s1", Role: core.RoleUser, Text: "今天天气怎么样"}},
		[]core.Tool{fakeTool{"web_search"}, fakeTool{"shell_run"}},
	)

	var contentDeltas []string
	var complete *llm.ProviderResponse
	for ev := range events {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
		if ev.Type == llm.EventContentDelta {
			contentDeltas = append(contentDeltas, ev.Content)
		}
		if ev.Type == llm.EventComplete {
			complete = ev.Response
		}
	}

	if gotPath != responsesEndpointPath {
		t.Fatalf("path = %q, want %q", gotPath, responsesEndpointPath)
	}
	if gotPayload == nil {
		t.Fatal("missing request payload")
	}
	if gotPayload["store"] != false {
		t.Fatalf("store = %#v, want false", gotPayload["store"])
	}
	if gotPayload["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", gotPayload["tool_choice"])
	}
	reasoning, _ := gotPayload["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "none" {
		t.Fatalf("reasoning = %#v, want effort none (thinking disabled)", gotPayload["reasoning"])
	}
	tools, _ := gotPayload["tools"].([]any)
	var sawBuiltinSearch bool
	var sawFunctionSearch bool
	var sawFunctionName bool
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch tool["type"] {
		case "web_search":
			sawBuiltinSearch = true
		case "function":
			if tool["name"] == "web_search" {
				sawFunctionSearch = true
			}
			if name, _ := tool["name"].(string); name != "" && name != "web_search" {
				sawFunctionName = true
			}
		}
	}
	if !sawBuiltinSearch {
		t.Fatal("expected built-in web_search tool declaration")
	}
	if sawFunctionSearch {
		t.Fatal("local web_search function must be translated, not sent as a function")
	}
	if !sawFunctionName {
		t.Fatalf("function tools must carry top-level name (Responses shape), tools = %#v", gotPayload["tools"])
	}

	if len(contentDeltas) == 0 || contentDeltas[0] != "根据搜索" {
		t.Fatalf("content deltas = %#v", contentDeltas)
	}
	if complete == nil {
		t.Fatal("missing complete event")
	}
	if complete.Content != "根据搜索，答案是42。" {
		t.Fatalf("content = %q", complete.Content)
	}
	if len(complete.ToolCalls) != 0 {
		t.Fatalf("tool calls = %#v, want none (server-side search)", complete.ToolCalls)
	}
	if complete.FinishReason != core.FinishReasonEndTurn {
		t.Fatalf("finish reason = %s", complete.FinishReason)
	}
	if complete.Usage.PromptTokens != 10 || complete.Usage.CompletionTokens != 5 || complete.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", complete.Usage)
	}
	if complete.Usage.PromptCacheHitTokens != 4 {
		t.Fatalf("cached tokens = %d, want 4", complete.Usage.PromptCacheHitTokens)
	}
}

// TestResponsesWebSearchLocalModeOnResponsesTransport locks the Phase 1b
// compat rule "api = responses + web_search = local → Responses API with local
// search": when the transport is explicitly the Responses API but search is
// local, the web_search tool must stay a regular function tool (executed by
// Whale's tool system) — NOT be translated to the server-side built-in, which
// would silently turn "local" search into server-side search.
func TestResponsesWebSearchLocalModeOnResponsesTransport(t *testing.T) {
	var gotPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"delta\":\"local\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":5,\"response\":{\"id\":\"resp_local\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"local\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithThinking(false),
		WithAPI(APIResponses),
		WithWebSearchMode(WebSearchModeLocal),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	events := c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s1", Role: core.RoleUser, Text: "用本地搜索查一下"}},
		[]core.Tool{fakeTool{"web_search"}, fakeTool{"shell_run"}},
	)
	for ev := range events {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}

	if gotPayload == nil {
		t.Fatal("missing request payload")
	}
	tools, _ := gotPayload["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("expected tools in payload")
	}
	var sawBuiltinSearch bool
	var sawLocalFunctionSearch bool
	var sawShellRun bool
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch tool["type"] {
		case "web_search":
			sawBuiltinSearch = true
		case "function":
			// Display names: core.DisplayToolName maps web_search → WebSearch,
			// shell_run → Bash.
			switch tool["name"] {
			case "WebSearch":
				sawLocalFunctionSearch = true
			case "Bash":
				sawShellRun = true
			}
		}
	}
	if sawBuiltinSearch {
		t.Fatal("web_search=local must NOT be translated to the server-side built-in on the Responses transport")
	}
	if !sawLocalFunctionSearch {
		t.Fatalf("web_search=local must stay a regular function tool on the Responses transport, tools = %#v", gotPayload["tools"])
	}
	if !sawShellRun {
		t.Fatalf("other function tools must still be declared, tools = %#v", gotPayload["tools"])
	}
}

func TestResponsesMixedFunctionCallAndSearch(t *testing.T) {
	var gotPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"shell_run\",\"arguments\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":4,\"output_index\":1,\"delta\":\"{\\\"cmd\\\":\\\"\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":5,\"output_index\":1,\"delta\":\"\\\"ls\\\"}\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":6,\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"output\":[{\"type\":\"web_search_call\",\"id\":\"ws_2\",\"action\":{\"type\":\"web_search\",\"query\":\"go 2026\"}},{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"shell_run\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"},{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"我用搜索结果查了一下，然后运行了命令。\"}]}],\"usage\":{\"input_tokens\":20,\"output_tokens\":8,\"total_tokens\":28}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithWebSearchMode(WebSearchModeServer),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	events := c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s2", Role: core.RoleUser, Text: "查一下最新版"}},
		[]core.Tool{fakeTool{"web_search"}, fakeTool{"shell_run"}},
	)

	var gotStart bool
	var argsReady bool
	var complete *llm.ProviderResponse
	for ev := range events {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
		if ev.Type == llm.EventToolUseStart && ev.ToolCall != nil && ev.ToolCall.Name == "shell_run" {
			gotStart = true
		}
		if ev.Type == llm.EventToolArgsDelta && ev.ToolArgsDelta != nil && ev.ToolArgsDelta.ReadyCount >= 1 {
			argsReady = true
		}
		if ev.Type == llm.EventComplete {
			complete = ev.Response
		}
	}
	if !gotStart {
		t.Fatal("missing tool use start event")
	}
	if !argsReady {
		t.Fatal("missing tool args ready event")
	}
	if complete == nil {
		t.Fatal("missing complete event")
	}
	if len(complete.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want 1", complete.ToolCalls)
	}
	if complete.ToolCalls[0].Name != "shell_run" || complete.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool call = %+v", complete.ToolCalls[0])
	}
	if complete.ToolCalls[0].Input != `{"cmd":"ls"}` {
		t.Fatalf("tool input = %q", complete.ToolCalls[0].Input)
	}
	if complete.FinishReason != core.FinishReasonToolUse {
		t.Fatalf("finish reason = %s, want tool_use", complete.FinishReason)
	}
	if !strings.Contains(complete.Content, "搜索结果") {
		t.Fatalf("content = %q", complete.Content)
	}
	// The server-side search call must be recorded for echo on later turns.
	if got := c.searchCalls.lookup("s2"); len(got) != 1 {
		t.Fatalf("recorded search calls = %#v, want 1", got)
	} else if len(got[0].items) != 1 || got[0].items[0]["id"] != "ws_2" {
		t.Fatalf("recorded search call = %#v", got[0])
	} else if got[0].items[0]["action"] == nil {
		t.Fatalf("recorded search call must keep the full action payload: %#v", got[0])
	} else if got[0].assistantKey == "" {
		t.Fatal("recorded search call must be associated with its assistant response")
	}
}

func TestResponsesEchoesSearchCallsOnNextTurn(t *testing.T) {
	var secondInput []any
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		requests++
		if requests == 2 {
			secondInput, _ = payload["input"].([]any)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.delta\",\"sequence_number\":0,\"delta\":\"need search\"}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"web_search_call\",\"id\":\"ws_9\"},{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"答案是42\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r2\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"继续\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		}
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithWebSearchMode(WebSearchModeServer),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	first := c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s9", Role: core.RoleUser, Text: "搜一下"}},
		[]core.Tool{fakeTool{"web_search"}},
	)
	for ev := range first {
		if ev.Type == llm.EventError {
			t.Fatalf("first turn error: %v", ev.Err)
		}
	}

	second := c.StreamResponse(context.Background(),
		[]core.Message{
			{SessionID: "s9", Role: core.RoleUser, Text: "搜一下"},
			{SessionID: "s9", Role: core.RoleAssistant, Text: "答案是42", Reasoning: "need search"},
			{SessionID: "s9", Role: core.RoleUser, Text: "然后呢"},
		},
		[]core.Tool{fakeTool{"web_search"}},
	)
	for ev := range second {
		if ev.Type == llm.EventError {
			t.Fatalf("second turn error: %v", ev.Err)
		}
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	var searchIndex, reasoningIndex, latestUserIndex = -1, -1, -1
	for i, raw := range secondInput {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == "web_search_call" && item["id"] == "ws_9" {
			searchIndex = i
		}
		if item["type"] == "reasoning" {
			reasoningIndex = i
		}
		if item["role"] == "user" {
			latestUserIndex = i
		}
	}
	if searchIndex < 0 {
		t.Fatalf("second turn input does not echo web_search_call: %#v", secondInput)
	}
	if reasoningIndex < 0 || searchIndex != reasoningIndex+1 {
		t.Fatalf("search call must immediately follow its reasoning: %#v", secondInput)
	}
	if latestUserIndex != len(secondInput)-1 {
		t.Fatalf("latest user must remain the final input item: %#v", secondInput)
	}
}

func TestResponsesModeRouting(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == responsesEndpointPath {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	drain := func(mode WebSearchMode, model string, explicitMode bool) {
		t.Helper()
		opts := []Option{
			WithBaseURL(srv.URL),
			WithHTTPClient(srv.Client()),
			WithModel(model),
		}
		if explicitMode {
			opts = append(opts, WithWebSearchMode(mode))
		}
		c, err := New(opts...)
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		for ev := range c.StreamResponse(context.Background(),
			[]core.Message{{SessionID: "rt", Role: core.RoleUser, Text: "hi"}},
			[]core.Tool{fakeTool{"web_search"}},
		) {
			if ev.Type == llm.EventError {
				t.Fatalf("mode %s model %s: provider error: %v", mode, model, ev.Err)
			}
		}
	}

	drain(WebSearchModeLocal, "deepseek-v4-flash", true)
	drain(WebSearchModeServer, "deepseek-v4-flash", true)
	drain(WebSearchModeServer, "deepseek-v4-pro", true)
	drain(WebSearchModeAuto, "deepseek-v4-flash", true)
	drain(WebSearchModeAuto, "deepseek-v4-pro", true)
	// Unset mode (zero value) defaults to auto: flash uses the Responses API,
	// other models stay on chat completions.
	drain(WebSearchModeAuto, "deepseek-v4-flash", false)
	drain(WebSearchModeAuto, "deepseek-v4-pro", false)

	want := []string{
		"/chat/completions",
		responsesEndpointPath,
		"/chat/completions", // server mode degrades for unsupported model
		responsesEndpointPath,
		"/chat/completions",   // auto falls back for unsupported model
		responsesEndpointPath, // default (unset) is auto: flash → Responses API
		"/chat/completions",   // default (unset) is auto: pro falls back
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q (all: %#v)", i, paths[i], want[i], paths)
		}
	}
}

func TestResponsesFailedEvent(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"status\":\"failed\"}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithWebSearchMode(WebSearchModeServer),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var gotErr error
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "f1", Role: core.RoleUser, Text: "hi"}},
		[]core.Tool{fakeTool{"web_search"}},
	) {
		if ev.Type == llm.EventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected error for response.failed")
	}
	if !strings.Contains(gotErr.Error(), "failed") {
		t.Fatalf("error = %v", gotErr)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 (response.failed is terminal, not retried)", requests)
	}
}

func TestResponsesEmptyCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithWebSearchMode(WebSearchModeServer),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var gotErr error
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "e1", Role: core.RoleUser, Text: "hi"}},
		[]core.Tool{fakeTool{"web_search"}},
	) {
		if ev.Type == llm.EventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected error for empty completion")
	}
	if !strings.Contains(gotErr.Error(), "without assistant content") {
		t.Fatalf("error = %v", gotErr)
	}
}

func TestResponsesArgumentsObjectStringified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"web_fetch\",\"arguments\":{\"url\":\"https://example.com\",\"prompt\":\"x\"}},{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithWebSearchMode(WebSearchModeServer),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var complete *llm.ProviderResponse
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "a1", Role: core.RoleUser, Text: "hi"}},
		[]core.Tool{fakeTool{"web_fetch"}},
	) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
		if ev.Type == llm.EventComplete {
			complete = ev.Response
		}
	}
	if complete == nil || len(complete.ToolCalls) != 1 {
		t.Fatalf("complete = %+v", complete)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(complete.ToolCalls[0].Input), &args); err != nil {
		t.Fatalf("input not valid JSON: %q: %v", complete.ToolCalls[0].Input, err)
	}
	if args["url"] != "https://example.com" || args["prompt"] != "x" {
		t.Fatalf("input = %q", complete.ToolCalls[0].Input)
	}
}

func TestResponsesHistoryInputItems(t *testing.T) {
	history := []core.Message{
		{SessionID: "h1", Role: core.RoleSystem, Text: "你是助手"},
		{SessionID: "h1", Role: core.RoleUser, Text: "hi"},
		{SessionID: "h1", Role: core.RoleAssistant, Text: "你好", Reasoning: "想想"},
		{SessionID: "h1", Role: core.RoleUser, Text: "查一下"},
		{SessionID: "h1", Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{ID: "call_1", Name: "web_search", Input: `{"query":"x"}`}}},
		{SessionID: "h1", Role: core.RoleTool, ToolResults: []core.ToolResult{{ToolCallID: "call_1", Name: "web_search", ModelText: "结果"}}},
		{SessionID: "h1", Role: core.RoleUser, Text: "然后呢"},
	}
	items := toResponsesInputItems(history, nil)
	if len(items) != 7 {
		t.Fatalf("items = %d, want 7: %#v", len(items), items)
	}
	types := make([]string, len(items))
	for i, item := range items {
		types[i], _ = item["type"].(string)
	}
	wantTypes := []string{"message", "message", "message", "message", "function_call", "function_call_output", "message"}
	if strings.Join(types, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("types = %#v, want %#v", types, wantTypes)
	}
	// The last user message is present.
	last := items[len(items)-1]
	if last["role"] != "user" {
		t.Fatalf("last item = %#v", last)
	}
	// function_call carries the display name and call id.
	var fc map[string]any
	for _, item := range items {
		if item["type"] == "function_call" {
			fc = item
		}
	}
	if fc == nil {
		t.Fatal("missing function_call item")
	}
	if fc["call_id"] != "call_1" || fc["name"] != core.DisplayToolName("web_search") {
		t.Fatalf("function_call = %#v", fc)
	}
	if fc["arguments"] != `{"query":"x"}` {
		t.Fatalf("arguments = %#v", fc["arguments"])
	}
	// function_call_output pairs with the call and carries the replay content.
	var fco map[string]any
	for _, item := range items {
		if item["type"] == "function_call_output" {
			fco = item
		}
	}
	if fco == nil || fco["call_id"] != "call_1" {
		t.Fatalf("function_call_output = %#v", fco)
	}
	if fco["output"] != "结果" {
		t.Fatalf("output = %#v", fco["output"])
	}
}

func TestResponsesHistoryDropsFailedReasoningOnlyAssistant(t *testing.T) {
	history := []core.Message{
		{SessionID: "stale", Role: core.RoleUser, Text: "old question"},
		{SessionID: "stale", Role: core.RoleAssistant, Reasoning: "continue the old task", FinishReason: core.FinishReasonError},
		{SessionID: "stale", Role: core.RoleUser, Text: "new unrelated question"},
	}

	items := toResponsesInputItems(history, nil)
	if len(items) != 2 {
		t.Fatalf("items = %d, want only the two user messages: %#v", len(items), items)
	}
	for _, item := range items {
		if item["type"] == "reasoning" {
			t.Fatalf("failed reasoning-only assistant leaked into provider history: %#v", item)
		}
	}
	lastContent := items[1]["content"].([]map[string]any)
	if got := lastContent[0]["text"]; got != "new unrelated question" {
		t.Fatalf("last user input = %#v, want new question", got)
	}
}

func TestResponsesHistoryKeepsToolCallReasoning(t *testing.T) {
	history := []core.Message{
		{SessionID: "tool-reasoning", Role: core.RoleAssistant, Reasoning: "need the tool", ToolCalls: []core.ToolCall{{ID: "call_1", Name: "shell_run", Input: `{}`}}},
		{SessionID: "tool-reasoning", Role: core.RoleTool, ToolResults: []core.ToolResult{{ToolCallID: "call_1", Name: "shell_run", ModelText: "ok"}}},
	}

	items := toResponsesInputItems(history, nil)
	wantTypes := []string{"reasoning", "function_call", "function_call_output"}
	if len(items) != len(wantTypes) {
		t.Fatalf("items = %d, want %d: %#v", len(items), len(wantTypes), items)
	}
	for i, want := range wantTypes {
		if got := items[i]["type"]; got != want {
			t.Fatalf("item %d type = %#v, want %q", i, got, want)
		}
	}
}

func TestResponsesInputItemsRestoresSearchCallsToOriginatingAssistant(t *testing.T) {
	assistant := core.Message{
		SessionID: "h2",
		Role:      core.RoleAssistant,
		Text:      "old answer",
		Reasoning: "searched first",
	}
	history := []core.Message{
		{SessionID: "h2", Role: core.RoleUser, Text: "old search"},
		assistant,
		{SessionID: "h2", Role: core.RoleUser, Text: "new unrelated question"},
	}
	replays := []webSearchCallReplay{{
		assistantKey: responsesAssistantReplayKey(assistant.Text, assistant.Reasoning, nil),
		items:        []map[string]any{{"type": "web_search_call", "id": "ws_1"}},
	}}
	items := toResponsesInputItems(history, replays)
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5: %#v", len(items), items)
	}
	search := items[len(items)-2]
	if search["type"] != "web_search_call" || search["id"] != "ws_1" {
		t.Fatalf("penultimate item = %#v, want echoed web_search_call", search)
	}
	last := items[len(items)-1]
	if last["role"] != "user" {
		t.Fatalf("last item = %#v, want latest user message", last)
	}
	content := last["content"].([]map[string]any)
	if got := content[0]["text"]; got != "new unrelated question" {
		t.Fatalf("last user input = %#v, want new question", got)
	}
}

func TestResponsesInputItemsKeepsSearchAndFunctionCallsInReasoningGroup(t *testing.T) {
	assistant := core.Message{
		SessionID: "mixed",
		Role:      core.RoleAssistant,
		Text:      "I searched and need to fetch details.",
		Reasoning: "search before fetching",
		ToolCalls: []core.ToolCall{{ID: "call_1", Name: "fetch", Input: `{"url":"https://example.com"}`}},
	}
	history := []core.Message{
		{SessionID: "mixed", Role: core.RoleUser, Text: "research prices"},
		assistant,
		{SessionID: "mixed", Role: core.RoleTool, ToolResults: []core.ToolResult{{ToolCallID: "call_1", Name: "fetch", ModelText: "price details"}}},
	}
	replays := []webSearchCallReplay{{
		assistantKey: responsesAssistantReplayKey(assistant.Text, assistant.Reasoning, assistant.ToolCalls),
		items:        []map[string]any{{"type": "web_search_call", "id": "ws_mixed"}},
	}}

	items := toResponsesInputItems(history, replays)
	wantTypes := []string{"message", "message", "reasoning", "web_search_call", "function_call", "function_call_output"}
	if len(items) != len(wantTypes) {
		t.Fatalf("items = %d, want %d: %#v", len(items), len(wantTypes), items)
	}
	for i, want := range wantTypes {
		if got := items[i]["type"]; got != want {
			t.Fatalf("item %d type = %#v, want %q; items=%#v", i, got, want, items)
		}
	}
}

func TestResponsesPrefixCompletionDegrades(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == responsesEndpointPath {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"计划\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}]}}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithWebSearchMode(WebSearchModeServer),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	for ev := range c.StreamResponseWithPrefix(context.Background(),
		[]core.Message{{SessionID: "p1", Role: core.RoleUser, Text: "做个计划"}},
		"计划",
		nil,
	) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
	if len(paths) != 1 || paths[0] != responsesEndpointPath {
		t.Fatalf("paths = %#v, want responses endpoint only (prefix degrades)", paths)
	}
}

func TestNormalizeAPI(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want API
		err  bool
	}{
		{in: "", want: APIAuto},
		{in: "auto", want: APIAuto},
		{in: "AUTO", want: APIAuto},
		{in: "responses", want: APIResponses},
		{in: "Responses", want: APIResponses},
		{in: "chat_completions", want: APIChatCompletions},
		{in: "CHAT_COMPLETIONS", want: APIChatCompletions},
		// Strict grammar: aliases that name no real endpoint must fail, not
		// silently fall back to the model-name heuristic.
		{in: "completions", err: true},
		{in: "chat", err: true},
		{in: "resp", err: true},
		{in: "responses_api", err: true},
		{in: "bogus", err: true},
	} {
		got, err := NormalizeAPI(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("NormalizeAPI(%q): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeAPI(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeAPI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveTransportCompatRules(t *testing.T) {
	for _, tc := range []struct {
		name      string
		api       API
		webSearch WebSearchMode
		wantAPI   API
		wantWeb   WebSearchMode
		wantWarn  bool
	}{
		{name: "auto+server implies responses", api: APIAuto, webSearch: WebSearchModeServer, wantAPI: APIResponses, wantWeb: WebSearchModeServer},
		{name: "auto+local stays auto", api: APIAuto, webSearch: WebSearchModeLocal, wantAPI: APIAuto, wantWeb: WebSearchModeLocal},
		{name: "auto+unset stays auto", api: APIAuto, webSearch: "", wantAPI: APIAuto, wantWeb: ""},
		{name: "responses+server keeps server", api: APIResponses, webSearch: WebSearchModeServer, wantAPI: APIResponses, wantWeb: WebSearchModeServer},
		{name: "responses+local keeps local", api: APIResponses, webSearch: WebSearchModeLocal, wantAPI: APIResponses, wantWeb: WebSearchModeLocal},
		{name: "responses+unset stays responses", api: APIResponses, webSearch: "", wantAPI: APIResponses, wantWeb: ""},
		{name: "chat_completions+server degrades to local with warning", api: APIChatCompletions, webSearch: WebSearchModeServer, wantAPI: APIChatCompletions, wantWeb: WebSearchModeLocal, wantWarn: true},
		{name: "chat_completions+local unchanged", api: APIChatCompletions, webSearch: WebSearchModeLocal, wantAPI: APIChatCompletions, wantWeb: WebSearchModeLocal},
		{name: "chat_completions+unset unchanged", api: APIChatCompletions, webSearch: "", wantAPI: APIChatCompletions, wantWeb: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotAPI, gotWeb, warn := ResolveTransport(tc.api, tc.webSearch)
			if gotAPI != tc.wantAPI {
				t.Fatalf("api = %q, want %q", gotAPI, tc.wantAPI)
			}
			if gotWeb != tc.wantWeb {
				t.Fatalf("web_search = %q, want %q", gotWeb, tc.wantWeb)
			}
			if (warn != "") != tc.wantWarn {
				t.Fatalf("warn = %q, want warn=%v", warn, tc.wantWarn)
			}
		})
	}
}

func newClientWithTransport(t *testing.T, model string, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{WithAPIKey("test-key"), WithModel(model)}, opts...)
	c, err := New(all...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func TestResponsesEnabledAPIEnum(t *testing.T) {
	for _, tc := range []struct {
		name      string
		model     string
		api       API
		webSearch WebSearchMode
		want      bool
	}{
		// Explicit APIResponses wins for ANY model, including the retired
		// chat alias the ACP entrypoint used to hardcode.
		{name: "responses+v4 flash", model: "deepseek-v4-flash", api: APIResponses, want: true},
		{name: "responses+retired chat alias", model: "deepseek-chat", api: APIResponses, want: true},
		{name: "responses+local search", model: "deepseek-v4-flash", api: APIResponses, webSearch: WebSearchModeLocal, want: true},
		// Explicit APIChatCompletions wins for ANY model, including capable ones.
		{name: "chat_completions+v4 flash", model: "deepseek-v4-flash", api: APIChatCompletions, want: false},
		{name: "chat_completions+chat alias", model: "deepseek-chat", api: APIChatCompletions, want: false},
		// Auto falls back to the model heuristic + web_search mode.
		{name: "auto+v4 flash", model: "deepseek-v4-flash", api: APIAuto, want: true},
		{name: "auto+chat alias", model: "deepseek-chat", api: APIAuto, want: false},
		{name: "auto+v4 flash+local search", model: "deepseek-v4-flash", api: APIAuto, webSearch: WebSearchModeLocal, want: false},
		{name: "auto+v4 flash+server search", model: "deepseek-v4-flash", api: APIAuto, webSearch: WebSearchModeServer, want: true},
		{name: "unset api+v4 flash", model: "deepseek-v4-flash", want: true},
		{name: "unset api+chat alias", model: "deepseek-chat", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClientWithTransport(t, tc.model, WithAPI(tc.api), WithWebSearchMode(tc.webSearch))
			if got := c.responsesEnabled(); got != tc.want {
				t.Fatalf("responsesEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWithAPIRejectsEmptySelection(t *testing.T) {
	// WithAPI must ignore an empty/auto value so the zero value cannot
	// override a model-name inference.
	c := newClientWithTransport(t, "deepseek-v4-flash")
	if c.api != APIAuto {
		t.Fatalf("api = %q, want APIAuto", c.api)
	}
	WithAPI(APIAuto)(c)
	if c.api != APIAuto {
		t.Fatalf("WithAPI(APIAuto) set api = %q, want APIAuto preserved", c.api)
	}
	WithAPI(APIChatCompletions)(c)
	if c.api != APIChatCompletions {
		t.Fatalf("WithAPI(APIChatCompletions) did not set api, got %q", c.api)
	}
}

func TestWithAPINormalizesWhitespaceAndCase(t *testing.T) {
	// WithAPI must normalize like WithReasoningEffort: a raw " RESPONSES "
	// (spaces, wrong case) must not silently miss responsesEnabled's exact
	// match and fall back to the model heuristic.
	c := newClientWithTransport(t, "deepseek-v4-flash", WithAPI(" RESPONSES "))
	if c.api != APIResponses {
		t.Fatalf("api = %q, want normalized %q", c.api, APIResponses)
	}
	if !c.responsesEnabled() {
		t.Fatal("responsesEnabled() = false, want true (normalized APIResponses)")
	}
	WithAPI("  Chat_Completions  ")(c)
	if c.api != APIChatCompletions {
		t.Fatalf("api = %q, want %q", c.api, APIChatCompletions)
	}
}

func TestWithAPIAutoFallsBackToHeuristic(t *testing.T) {
	// WithAPI("auto") sets a non-canonical value that must behave exactly
	// like unset: responsesEnabled falls through to the model heuristic.
	c := newClientWithTransport(t, "deepseek-v4-flash", WithAPI("auto"))
	if !c.responsesEnabled() {
		t.Fatal("auto + v4-flash: expected Responses API (heuristic)")
	}
	c2 := newClientWithTransport(t, "deepseek-chat", WithAPI("auto"))
	if c2.responsesEnabled() {
		t.Fatal("auto + chat alias: expected chat completions (heuristic)")
	}
}

// TestResponsesIncompleteIsRetryableNotEndTurn: a response.incomplete frame
// (server-side truncation) must retry like the chat-completions path, never
// emit a clean end_turn with the delta-streamed tool call dropped.
func TestResponsesIncompleteIsRetryableNotEndTurn(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"status\":\"incomplete\",\"output\":[],\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"edit\",\"arguments\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":3,\"output_index\":0,\"delta\":\"{\\\"file_path\\\":\\\"a.go\\\"}\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"r2\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"edit\",\"arguments\":\"{\\\"file_path\\\":\\\"a.go\\\"}\"},{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithThinking(false),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var complete *llm.ProviderResponse
	var gotErr error
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s1", Role: core.RoleUser, Text: "edit a.go"}},
		[]core.Tool{fakeTool{"edit"}},
	) {
		if ev.Type == llm.EventError {
			gotErr = ev.Err
		}
		if ev.Type == llm.EventComplete {
			complete = ev.Response
		}
	}
	if gotErr != nil {
		t.Fatalf("provider error: %v", gotErr)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (response.incomplete must retry, not emit end_turn)", requests)
	}
	if complete == nil || complete.FinishReason != core.FinishReasonToolUse || len(complete.ToolCalls) != 1 {
		t.Fatalf("expected ToolUse with 1 call after retry, got %+v", complete)
	}
}

// TestResponsesSparseTerminalKeepsDeltaStreamedCalls: when the terminal
// payload omits the function_call items but the deltas already streamed them,
// the accumulated calls must survive instead of being dropped into a clean
// end_turn.
func TestResponsesSparseTerminalKeepsDeltaStreamedCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"edit\",\"arguments\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"output_index\":0,\"delta\":\"{\\\"file_path\\\":\\\"a.go\\\"\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":3,\"output_index\":0,\"delta\":\",\\\"mode\\\":\\\"full\\\"}\"}\n\n")
		// Terminal payload omits the function_call item entirely.
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithThinking(false),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var complete *llm.ProviderResponse
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s1", Role: core.RoleUser, Text: "edit a.go"}},
		[]core.Tool{fakeTool{"edit"}},
	) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
		if ev.Type == llm.EventComplete {
			complete = ev.Response
		}
	}
	if complete == nil || complete.FinishReason != core.FinishReasonToolUse || len(complete.ToolCalls) != 1 {
		t.Fatalf("expected ToolUse with the delta-streamed call, got %+v", complete)
	}
	got := complete.ToolCalls[0]
	if got.ID != "call_1" || got.Name != "edit" || got.Input != `{"file_path":"a.go","mode":"full"}` {
		t.Fatalf("tool call = %+v, want call_1/edit with merged delta arguments", got)
	}
}

// TestResponsesIncompleteExhaustsRetries: when the server keeps truncating,
// the client must stop after streamMaxAttempts with an error — never an
// infinite loop and never a clean end_turn — and each retry carries
// StreamReset so the consumer drops partial deltas.
func TestResponsesIncompleteExhaustsRetries(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"status\":\"incomplete\",\"output\":[],\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithThinking(false),
		WithStreamMaxAttempts(2),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var gotErr error
	var retryResets int
	var endTurn bool
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s1", Role: core.RoleUser, Text: "hi"}},
		nil,
	) {
		switch ev.Type {
		case llm.EventError:
			gotErr = ev.Err
		case llm.EventRetryScheduled:
			if ev.Retry != nil && ev.Retry.StreamReset {
				retryResets++
			}
		case llm.EventComplete:
			if ev.Response != nil && ev.Response.FinishReason == core.FinishReasonEndTurn {
				endTurn = true
			}
		}
	}
	if gotErr == nil {
		t.Fatal("expected error after retries exhausted, not a clean end_turn")
	}
	if endTurn {
		t.Fatal("incomplete must never produce a clean end_turn")
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (streamMaxAttempts)", requests)
	}
	if retryResets != 1 {
		t.Fatalf("StreamReset retry events = %d, want 1", retryResets)
	}
}

// TestResponsesIncompleteAfterProgressNotRetried: once deltas have streamed
// (progress), an incomplete is surfaced as an error, not retried — parity with
// the chat-completions path (partial output already delivered; StreamReset
// would only be safe for pre-progress truncation).
func TestResponsesIncompleteAfterProgressNotRetried(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"partial\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"sequence_number\":2,\"response\":{\"id\":\"r1\",\"status\":\"incomplete\",\"output\":[],\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithThinking(false),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var gotErr error
	var complete *llm.ProviderResponse
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s1", Role: core.RoleUser, Text: "hi"}},
		nil,
	) {
		if ev.Type == llm.EventError {
			gotErr = ev.Err
		}
		if ev.Type == llm.EventComplete {
			complete = ev.Response
		}
	}
	if gotErr == nil {
		t.Fatal("expected error (incomplete after progress is not retried)")
	}
	if complete != nil && complete.FinishReason == core.FinishReasonEndTurn {
		t.Fatal("must not emit a clean end_turn from a post-progress incomplete")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 (no retry after progress)", requests)
	}
}

// TestResponsesSparseTerminalPreservesCallOrder: multiple delta-streamed
// calls with non-contiguous indices survive in output-index order.
func TestResponsesSparseTerminalPreservesCallOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, idx := range []int{0, 2} {
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":%d,\"output_index\":%d,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_%d\",\"name\":\"edit\",\"arguments\":\"\"}}\n\n", idx+1, idx, idx)
		}
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":4,\"output_index\":0,\"delta\":\"{\\\"a\\\":1}\"}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":5,\"output_index\":2,\"delta\":\"{\\\"b\\\":2}\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":6,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	_ = os.Setenv("DEEPSEEK_API_KEY", "test-key")
	c, err := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithModel("deepseek-v4-flash"),
		WithThinking(false),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var complete *llm.ProviderResponse
	for ev := range c.StreamResponse(context.Background(),
		[]core.Message{{SessionID: "s1", Role: core.RoleUser, Text: "edit"}},
		[]core.Tool{fakeTool{"edit"}},
	) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
		if ev.Type == llm.EventComplete {
			complete = ev.Response
		}
	}
	if complete == nil || complete.FinishReason != core.FinishReasonToolUse || len(complete.ToolCalls) != 2 {
		t.Fatalf("expected 2 ToolUse calls, got %+v", complete)
	}
	if complete.ToolCalls[0].ID != "call_0" || complete.ToolCalls[1].ID != "call_2" {
		t.Fatalf("calls out of output-index order: %+v", complete.ToolCalls)
	}
}
