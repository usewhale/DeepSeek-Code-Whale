package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm"
	"github.com/usewhale/whale/internal/llm/deepseek"
)

func TestNewDeepSeekProviderAppliesBaseURL(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_BASE_URL", "https://env.example")
	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	provider, err := newDeepSeekProvider(providerOptions{
		BaseURL:         srv.URL,
		Model:           "deepseek-v4-pro",
		ReasoningEffort: "high",
		ThinkingEnabled: true,
	})
	if err != nil {
		t.Fatalf("newDeepSeekProvider: %v", err)
	}
	for ev := range provider.StreamResponse(context.Background(), []core.Message{{Role: core.RoleUser, Text: "hi"}}, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
	if !sawRequest {
		t.Fatal("expected request to configured base URL")
	}
}

func TestNewDeepSeekProviderUsesInlineMultimodalAPIKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-mm-key")
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	provider, err := newDeepSeekProvider(providerOptions{
		APIKey: "main-key",
		Model:  "deepseek-v4-flash",
		DeepSeekMultimodal: MultimodalProviderConfig{
			Enabled:   true,
			Compat:    "openai",
			BaseURL:   srv.URL,
			APIKey:    "inline-mm-key",
			APIKeyEnv: "OPENROUTER_API_KEY",
			Model:     "openai/gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("newDeepSeekProvider: %v", err)
	}
	imagePath := filepath.Join(t.TempDir(), "screen.png")
	if err := os.WriteFile(imagePath, []byte("fake-image"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	history := []core.Message{core.UserMessageFromParts("s1", []core.MessagePart{
		{Type: core.MessagePartAttachment, Attachment: &core.AttachmentRef{Kind: core.AttachmentKindImage, Path: imagePath, MIME: "image/png", Filename: "screen.png"}},
	}, false)}
	for ev := range provider.StreamResponse(context.Background(), history, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
	if auth != "Bearer inline-mm-key" {
		t.Fatalf("authorization = %q, want inline multimodal key", auth)
	}
}

func TestNewDeepSeekProviderKeepsMissingMultimodalAPIKeyEnvError(t *testing.T) {
	t.Setenv("MISSING_MM_KEY", "")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Fatalf("multimodal request should not be sent with fallback key")
	}))
	defer srv.Close()

	provider, err := newDeepSeekProvider(providerOptions{
		APIKey: "main-key",
		Model:  "deepseek-v4-flash",
		DeepSeekMultimodal: MultimodalProviderConfig{
			Enabled:   true,
			Compat:    "openai",
			BaseURL:   srv.URL,
			APIKeyEnv: "MISSING_MM_KEY",
			Model:     "openai/gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("newDeepSeekProvider: %v", err)
	}
	imagePath := filepath.Join(t.TempDir(), "screen.png")
	if err := os.WriteFile(imagePath, []byte("fake-image"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	history := []core.Message{core.UserMessageFromParts("s1", []core.MessagePart{
		{Type: core.MessagePartAttachment, Attachment: &core.AttachmentRef{Kind: core.AttachmentKindImage, Path: imagePath, MIME: "image/png", Filename: "screen.png"}},
	}, false)}
	var gotErr error
	for ev := range provider.StreamResponse(context.Background(), history, nil) {
		if ev.Type == llm.EventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "multimodal API key env MISSING_MM_KEY is not set") {
		t.Fatalf("error = %v, want missing multimodal env error", gotErr)
	}
	if requests.Load() != 0 {
		t.Fatalf("multimodal requests = %d, want 0", requests.Load())
	}
}

func TestNewMiniMaxProviderUsesConfiguredEndpoint(t *testing.T) {
	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload["model"] != "MiniMax-M3" {
			t.Fatalf("model = %v, want MiniMax-M3", payload["model"])
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["type"] != "adaptive" {
			t.Fatalf("thinking = %#v, want adaptive", payload["thinking"])
		}
		if payload["reasoning_split"] != true {
			t.Fatalf("reasoning_split = %v, want true", payload["reasoning_split"])
		}
		if _, ok := payload["reasoning_effort"]; ok {
			t.Fatalf("MiniMax request should not include reasoning_effort: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	provider, err := newProvider(providerOptions{
		Provider: "minimax",
		MiniMax: MiniMaxProviderConfig{
			APIKey:  "test-minimax-key",
			BaseURL: srv.URL,
		},
		Model:           "MiniMax-M3",
		ThinkingEnabled: true,
	})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	for ev := range provider.StreamResponse(context.Background(), []core.Message{{Role: core.RoleUser, Text: "hi"}}, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
	if !sawRequest {
		t.Fatal("expected request to configured base URL")
	}
}

func TestNewMiniMaxProviderSupportsDisabledThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["type"] != "disabled" {
			t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	provider, err := newProvider(providerOptions{
		Provider: "minimax",
		MiniMax: MiniMaxProviderConfig{
			APIKey:  "test-minimax-key",
			BaseURL: srv.URL,
		},
		Model: "MiniMax-M3",
	})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	for ev := range provider.StreamResponse(context.Background(), []core.Message{{Role: core.RoleUser, Text: "hi"}}, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
}

func TestNewMiniMaxM27KeepsThinkingAlwaysOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if _, ok := payload["thinking"]; ok {
			t.Fatalf("MiniMax-M2.7 request should omit thinking control: %#v", payload["thinking"])
		}
		if payload["reasoning_split"] != true {
			t.Fatalf("reasoning_split = %v, want true", payload["reasoning_split"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	provider, err := newProvider(providerOptions{
		Provider: "minimax",
		MiniMax: MiniMaxProviderConfig{
			APIKey:  "test-minimax-key",
			BaseURL: srv.URL,
		},
		Model: "MiniMax-M2.7",
	})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	for ev := range provider.StreamResponse(context.Background(), []core.Message{{Role: core.RoleUser, Text: "hi"}}, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
}

func TestNewMiniMaxM3SendsVideoWithAdaptiveThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["type"] != "adaptive" {
			t.Fatalf("thinking = %#v, want adaptive", payload["thinking"])
		}
		if payload["reasoning_split"] != true {
			t.Fatalf("reasoning_split = %v, want true", payload["reasoning_split"])
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %#v, want one user message", payload["messages"])
		}
		message, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("message = %#v", messages[0])
		}
		content, ok := message["content"].([]any)
		if !ok || len(content) != 2 {
			t.Fatalf("content = %#v, want text and video parts", message["content"])
		}
		video, ok := content[1].(map[string]any)
		if !ok || video["type"] != "video_url" {
			t.Fatalf("video part = %#v, want video_url", content[1])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	videoPath := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	provider, err := newProvider(providerOptions{
		Provider: "minimax",
		MiniMax: MiniMaxProviderConfig{
			APIKey:  "test-minimax-key",
			BaseURL: srv.URL,
		},
		Model:           "MiniMax-M3",
		ThinkingEnabled: true,
	})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	history := []core.Message{core.UserMessageFromParts("session", []core.MessagePart{
		{Type: core.MessagePartText, Text: "describe this clip"},
		{Type: core.MessagePartAttachment, Attachment: &core.AttachmentRef{
			Kind:     core.AttachmentKindVideo,
			Path:     videoPath,
			MIME:     "video/mp4",
			Filename: "clip.mp4",
		}},
	}, false)}
	for ev := range provider.StreamResponse(context.Background(), history, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
}

func TestNewMiniMaxProviderUsesRegionalDefaultEndpoint(t *testing.T) {
	_, err := newProvider(providerOptions{
		Provider: "minimax",
		MiniMax: MiniMaxProviderConfig{
			APIKey: "test-minimax-key",
			Region: "cn_zh",
		},
		Model: "MiniMax-M2.7",
	})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	if got := minimaxBaseURLForRegion("cn_zh"); got != "https://api.minimaxi.com/v1" {
		t.Fatalf("regional endpoint = %s", got)
	}
}

func TestTaskProviderUsesConfiguredRetryPolicy(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	var parentRequests atomic.Int32
	var childRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if _, hasTools := payload["tools"]; !hasTools {
			childRequests.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if parentRequests.Add(1) == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_parallel\",\"function\":{\"name\":\"parallel_reason\",\"arguments\":\"{\\\"prompts\\\":[\\\"x\\\"]}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	// Pin to local search mode: this test mocks the chat-completions endpoint.
	cfg.DeepSeekWebSearch = deepseek.WebSearchModeLocal
	cfg.DataDir = t.TempDir()
	cfg.APIBaseURL = srv.URL
	cfg.AutoAcceptPermissions = true
	cfg.RetryMaxAttempts = 1
	cfg.RetryMaxDelay = time.Second
	a, err := New(context.Background(), cfg, StartOptions{NewSession: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := a.RunTurn(ctx, "use parallel reasoning", false)
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	for ev := range events {
		if ev.Type == "error" && ev.Err != nil {
			t.Fatalf("agent error: %v", ev.Err)
		}
	}
	if got := childRequests.Load(); got != 1 {
		t.Fatalf("child provider requests: want configured max_attempts=1, got %d", got)
	}
}

func TestRetryPolicyFromConfigDisablesRequestRetriesWhenExplicitZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RetryMaxAttempts = 0
	cfg.RetryMaxAttemptsExplicit = true

	policy := retryPolicyFromConfig(cfg)
	if policy.MaxAttempts != 1 {
		t.Fatalf("MaxAttempts = %d, want one attempt with no retries", policy.MaxAttempts)
	}
}

// TestNewDeepSeekProviderHonorsAPI locks the Phase 1b wire-through: the CLI
// provider constructor must pass providerOptions.DeepSeekAPI into the deepseek
// client, so an explicit transport selection wins for the CLI path too (not
// just the ACP entrypoint). Responses API -> /responses; chat_completions ->
// /chat/completions, regardless of the model heuristic.
func TestNewDeepSeekProviderHonorsAPI(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	for _, tc := range []struct {
		name string
		api  deepseek.API
		path string
	}{
		{name: "responses forces responses endpoint", api: deepseek.APIResponses, path: "/responses"},
		{name: "chat_completions forces chat endpoint", api: deepseek.APIChatCompletions, path: "/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.path == "/responses" {
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"delta\":\"ok\"}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":5,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
				} else {
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer srv.Close()

			// v4-flash + web_search auto would normally pick the Responses API;
			// the explicit API option must be the authority in both directions.
			provider, err := newDeepSeekProvider(providerOptions{
				BaseURL:         srv.URL,
				Model:           "deepseek-v4-flash",
				DeepSeekAPI:     tc.api,
				ReasoningEffort: "high",
				ThinkingEnabled: true,
			})
			if err != nil {
				t.Fatalf("newDeepSeekProvider: %v", err)
			}
			var sawComplete bool
			for ev := range provider.StreamResponse(context.Background(), []core.Message{{Role: core.RoleUser, Text: "hi"}}, nil) {
				if ev.Type == llm.EventError {
					t.Fatalf("provider error: %v", ev.Err)
				}
				if ev.Type == llm.EventComplete {
					sawComplete = true
				}
			}
			if gotPath != tc.path {
				t.Fatalf("request path = %q, want %q (DeepSeekAPI must be honored)", gotPath, tc.path)
			}
			if !sawComplete {
				t.Fatal("expected a complete event")
			}
		})
	}
}
