package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
)

// TestRunExecScrubsLeakedToolCallFromOutput verifies that when the model writes
// a tool call as plain text instead of using the API tool-call channel, the
// leaked wrapper — already streamed as content deltas into exec's accumulator —
// is dropped from the final output once the recovery turn re-streams the real
// answer. Guards the downstream half of the leaked-tool-call fix (the agent
// emits a response reset; exec must honor it).
func TestRunExecScrubsLeakedToolCallFromOutput(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	var mu sync.Mutex
	reqs := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		n := reqs
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		usage := `,"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}`
		if n == 1 {
			// Tool call forged as text, with a legit prose preamble.
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"On it. <tool_calls> <read_file path=\\\"a.go\\\"> </read_file> </tool_calls>\"},\"finish_reason\":\"stop\"}]"+usage+"}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a.go has 10 lines.\"},\"finish_reason\":\"stop\"}]"+usage+"}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	res, err := RunExec(context.Background(), DefaultConfig(), StartOptions{NewSession: true}, "read a.go")
	if err != nil {
		t.Fatalf("RunExec: %v", err)
	}
	if strings.Contains(res.Output, "<tool_calls") || strings.Contains(res.Output, "read_file") {
		t.Fatalf("leaked tool-call text survived in exec output: %q", res.Output)
	}
	if !strings.Contains(res.Output, "10 lines") {
		t.Fatalf("expected recovered answer in output, got %q", res.Output)
	}
	if reqs < 2 {
		t.Fatalf("expected a recovery request after the leak, got %d requests", reqs)
	}
}

func TestRunExecReturnsFinalOutput(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	res, err := RunExec(context.Background(), DefaultConfig(), StartOptions{NewSession: true}, "hi")
	if err != nil {
		t.Fatalf("RunExec: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Output != "hello world" {
		t.Fatalf("output = %q", res.Output)
	}
	if res.SessionID == "" {
		t.Fatal("expected session id")
	}
	if res.Model == "" {
		t.Fatal("expected model")
	}
}

func TestRunExecNewSessionIDsDiffer(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecEchoServer(t, "ok")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()

	first, err := RunExec(context.Background(), cfg, StartOptions{NewSession: true}, "hi")
	if err != nil {
		t.Fatalf("first RunExec: %v", err)
	}
	second, err := RunExec(context.Background(), cfg, StartOptions{NewSession: true}, "hi")
	if err != nil {
		t.Fatalf("second RunExec: %v", err)
	}
	if first.SessionID == "" || second.SessionID == "" {
		t.Fatalf("expected non-empty session ids, got %q and %q", first.SessionID, second.SessionID)
	}
	if first.SessionID == second.SessionID {
		t.Fatalf("expected a new session id per exec call, got %q twice", first.SessionID)
	}
}

func TestRunExecNewSessionDefaultsToAgentMode(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecEchoServer(t, "ok")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()

	res, err := RunExec(context.Background(), cfg, StartOptions{NewSession: true}, "hi")
	if err != nil {
		t.Fatalf("RunExec: %v", err)
	}
	st, err := session.LoadModeState(store.DefaultSessionsDir(cfg.DataDir), res.SessionID)
	if err != nil {
		t.Fatalf("load mode state: %v", err)
	}
	if st.Mode != session.ModeAgent {
		t.Fatalf("expected new session mode to default to agent, got %q", st.Mode)
	}
}

func newExecEchoServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n", content)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestSummarizeExecTextPreservesUTF8(t *testing.T) {
	input := strings.Repeat("中文🙂", 100)
	got := summarizeExecText(input)
	if !utf8.ValidString(got) {
		t.Fatalf("summary must be valid UTF-8: %q", got)
	}
	if strings.Contains(got, "�") {
		t.Fatalf("summary must not contain replacement characters: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated marker, got %q", got)
	}
}
