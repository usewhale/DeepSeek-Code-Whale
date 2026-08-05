package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/usewhale/whale/internal/llm/deepseek"
)

// TestWebFetchExtractorHonorsAPI: the web_fetch summarizer must follow the
// user's explicit transport selection, like the main provider — a
// chat-completions-only endpoint must not receive /responses requests from
// the extractor behind the user's back (and vice versa).
func TestWebFetchExtractorHonorsAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		api  deepseek.API
		path string
	}{
		{name: "responses", api: deepseek.APIResponses, path: "/responses"},
		{name: "chat_completions", api: deepseek.APIChatCompletions, path: "/chat/completions"},
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

			e := newDeepSeekWebFetchExtractor(webFetchExtractorOptions{
				APIKey:  "test-key",
				BaseURL: srv.URL,
				API:     tc.api,
			})
			got, err := e.Extract(context.Background(), "question", "fetched content")
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if got != "ok" {
				t.Fatalf("extracted = %q, want %q", got, "ok")
			}
			if gotPath != tc.path {
				t.Fatalf("request path = %q, want %q (extractor must honor DeepSeekAPI)", gotPath, tc.path)
			}
		})
	}
}
