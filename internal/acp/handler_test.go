package acp

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/usewhale/whale/internal/store"
)

// factoryRecorder captures the mcpServers the handler forwards to the runtime
// factory, so tests can assert that ACP client-supplied MCP servers reach the
// per-session runtime builder.
type factoryRecorder struct {
	calls   int
	gotMcps []MCPServer
	closeFn func()
}

func newHandlerForTest(t *testing.T, rec *factoryRecorder) *Handler {
	t.Helper()
	msgStore, err := store.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h.SetRuntimeFactory(func(acpSessionID, cwd string, mcps []MCPServer) (*SessionRuntime, error) {
		rec.calls++
		rec.gotMcps = mcps
		return &SessionRuntime{Close: rec.closeFn}, nil
	})
	return h
}

func TestSessionNewForwardsMCPServers(t *testing.T) {
	rec := &factoryRecorder{}
	h := newHandlerForTest(t, rec)
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionNew,
		Params: json.RawMessage(`{
			"cwd": "/work",
			"mcpServers": [
				{"name": "fs", "command": "/bin/echo", "args": ["--stdio"], "env": [{"name": "A", "value": "1"}]}
			]
		}`),
	})
	if rec.calls != 1 {
		t.Fatalf("expected factory to be called once, got %d", rec.calls)
	}
	if len(rec.gotMcps) != 1 {
		t.Fatalf("expected 1 mcpServer forwarded, got %d", len(rec.gotMcps))
	}
	got := rec.gotMcps[0]
	if got.Name != "fs" || got.Command != "/bin/echo" || len(got.Args) != 1 || got.Args[0] != "--stdio" {
		t.Errorf("unexpected server: %+v", got)
	}
	if len(got.Env) != 1 || got.Env[0] != (EnvVariable{Name: "A", Value: "1"}) {
		t.Errorf("unexpected env: %+v", got.Env)
	}
}

func TestSessionLoadForwardsMCPServers(t *testing.T) {
	rec := &factoryRecorder{}
	h := newHandlerForTest(t, rec)
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionLoad,
		Params: json.RawMessage(`{
			"sessionId": "sess-1",
			"cwd": "/work",
			"mcpServers": [{"name": "fs", "command": "/bin/echo"}]
		}`),
	})
	if rec.calls != 1 {
		t.Fatalf("expected factory to be called once on load, got %d", rec.calls)
	}
	if len(rec.gotMcps) != 1 || rec.gotMcps[0].Name != "fs" {
		t.Fatalf("mcpServers not forwarded on load: %+v", rec.gotMcps)
	}
}

func TestCloseSessionsInvokesRuntimeClose(t *testing.T) {
	closed := 0
	rec := &factoryRecorder{closeFn: func() { closed++ }}
	h := newHandlerForTest(t, rec)
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionNew,
		Params:  json.RawMessage(`{"cwd": "/work"}`),
	})
	if rec.calls != 1 {
		t.Fatalf("expected factory called once, got %d", rec.calls)
	}
	h.CloseSessions()
	if closed != 1 {
		t.Fatalf("expected runtime Close hook to run once, got %d", closed)
	}
}

// TestBoundSessionsEvictsLRU verifies that live sessions are capped at
// maxLiveSessions and the least-recently-used session is evicted (its runtime
// Close hook released) when the cap is exceeded. ACP v1 has no session/delete,
// so this bounds runtime and MCP-server leakage on long-lived hosts.
func TestBoundSessionsEvictsLRU(t *testing.T) {
	closed := 0
	h := newHandlerForTest(t, &factoryRecorder{closeFn: func() { closed++ }})

	// Create the first session and mark it as the oldest.
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionNew,
		Params:  json.RawMessage(`{"cwd": "/work"}`),
	})
	var firstID string
	h.mu.Lock()
	for id := range h.sessions {
		firstID = id
		break
	}
	h.sessions[firstID].lastUsed = time.Now().Add(-time.Hour)
	h.mu.Unlock()

	// Fill to the cap (1 + maxLiveSessions - 1 = maxLiveSessions).
	for i := 0; i < maxLiveSessions-1; i++ {
		h.handleRequest(&RPCRequest{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: 2 + i},
			Method:  MethodSessionNew,
			Params:  json.RawMessage(`{"cwd": "/work"}`),
		})
	}
	if got := len(h.sessions); got != maxLiveSessions {
		t.Fatalf("expected %d live sessions, got %d", maxLiveSessions, got)
	}

	// One more session exceeds the cap and evicts the marked oldest.
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 100},
		Method:  MethodSessionNew,
		Params:  json.RawMessage(`{"cwd": "/work"}`),
	})
	if got := len(h.sessions); got != maxLiveSessions {
		t.Fatalf("expected %d live sessions after eviction, got %d", maxLiveSessions, got)
	}
	if closed != 1 {
		t.Fatalf("expected exactly 1 eviction Close call, got %d", closed)
	}
	if _, ok := h.sessions[firstID]; ok {
		t.Fatal("least-recently-used session was not evicted")
	}
}
