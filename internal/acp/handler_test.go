package acp

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"sync"
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
	done := make(chan struct{})
	var once sync.Once
	h := newHandlerForTest(t, &factoryRecorder{closeFn: func() { once.Do(func() { close(done) }) }})

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
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("eviction Close not invoked")
	}
	if _, ok := h.sessions[firstID]; ok {
		t.Fatal("least-recently-used session was not evicted")
	}
}

// TestEvictionSkipsActiveSessions verifies that a session with an in-flight
// prompt is never evicted, even when it is the least recently used — closing
// its runtime mid-turn would break that turn's MCP tool calls.
func TestEvictionSkipsActiveSessions(t *testing.T) {
	done := make(chan struct{})
	var once sync.Once
	h := newHandlerForTest(t, &factoryRecorder{closeFn: func() { once.Do(func() { close(done) }) }})

	sessionNewReq := func(id any) *RPCRequest {
		return &RPCRequest{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: id},
			Method:  MethodSessionNew,
			Params:  json.RawMessage(`{"cwd": "/work"}`),
		}
	}

	// First session: oldest AND has an in-flight prompt.
	h.handleRequest(sessionNewReq(1))
	var firstID string
	h.mu.Lock()
	for id := range h.sessions {
		firstID = id
		break
	}
	h.sessions[firstID].runs = make(map[*promptRun]struct{})
	h.sessions[firstID].runs[&promptRun{}] = struct{}{}
	h.sessions[firstID].lastUsed = time.Now().Add(-time.Hour)
	h.mu.Unlock()

	// Fill past the cap: 1 (active) + maxLiveSessions = cap+1.
	for i := 0; i < maxLiveSessions; i++ {
		h.handleRequest(sessionNewReq(2 + i))
	}

	h.mu.Lock()
	_, activeSurvived := h.sessions[firstID]
	live := len(h.sessions)
	h.mu.Unlock()
	if !activeSurvived {
		t.Fatal("session with in-flight prompt was evicted")
	}
	if live != maxLiveSessions {
		t.Fatalf("expected %d live sessions (cap, active kept), got %d", maxLiveSessions, live)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("eviction Close not invoked")
	}
}

// TestEvictionAllBusyKeepsCapExceeded verifies that when every live session
// has an in-flight prompt, eviction is skipped entirely (cap exceeded, no
// close) rather than killing active work.
func TestEvictionAllBusyKeepsCapExceeded(t *testing.T) {
	done := make(chan struct{})
	var once sync.Once
	h := newHandlerForTest(t, &factoryRecorder{closeFn: func() { once.Do(func() { close(done) }) }})

	sessionNewReq := func(id any) *RPCRequest {
		return &RPCRequest{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: id},
			Method:  MethodSessionNew,
			Params:  json.RawMessage(`{"cwd": "/work"}`),
		}
	}
	for i := 0; i < maxLiveSessions; i++ {
		h.handleRequest(sessionNewReq(1 + i))
	}
	h.mu.Lock()
	for _, sctx := range h.sessions {
		sctx.runs = map[*promptRun]struct{}{}
		sctx.runs[&promptRun{}] = struct{}{}
	}
	h.mu.Unlock()
	h.handleRequest(sessionNewReq(1000)) // exceeds cap; only the new idle session is evictable

	h.mu.Lock()
	live := len(h.sessions)
	busyKept := 0
	for _, sctx := range h.sessions {
		if len(sctx.runs) > 0 {
			busyKept++
		}
	}
	h.mu.Unlock()
	if live != maxLiveSessions {
		t.Fatalf("expected cap kept (new idle session evicted), got %d", live)
	}
	if busyKept != maxLiveSessions {
		t.Fatalf("expected all %d busy sessions to survive, kept %d", maxLiveSessions, busyKept)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the idle newcomer to be evicted (Close not invoked)")
	}
}

func TestInitializeAdvertisesSessionList(t *testing.T) {
	var buf bytes.Buffer
	// Wire a handler with a captured transport so we can inspect the response.
	msgStore, err := store.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	h2 := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h2.SetRuntimeFactory(func(acpSessionID, cwd string, mcps []MCPServer) (*SessionRuntime, error) {
		return &SessionRuntime{}, nil
	})
	h2.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodInitialize,
		Params:  json.RawMessage(`{"protocolVersion": 1}`),
	})
	var resp struct {
		Result struct {
			AgentCapabilities struct {
				SessionCapabilities *SessionCapabilities `json:"sessionCapabilities"`
			} `json:"agentCapabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("parse initialize response: %v", err)
	}
	sc := resp.Result.AgentCapabilities.SessionCapabilities
	if sc == nil || sc.List == nil {
		t.Fatalf("expected sessionCapabilities.list to be advertised, got %+v", sc)
	}
	if sc.Delete != nil {
		t.Fatalf("session/delete is not implemented and must not be advertised, got %+v", sc.Delete)
	}
}

func writeSessionFile(t *testing.T, dir, id, cwd, firstUserMsg string, modTime time.Time) {
	t.Helper()
	path := dir + "/" + id + ".jsonl"
	if err := os.WriteFile(path, []byte(`{"ID":"m-1","SessionID":"`+id+`","Role":"user","Text":`+strconv.Quote(firstUserMsg)+`,"Hidden":false}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session %s: %v", id, err)
	}
	if cwd != "" {
		b, _ := json.Marshal(sessionMeta{Cwd: cwd, Mode: "agent"})
		if err := os.WriteFile(dir+"/"+id+".meta.json", b, 0o600); err != nil {
			t.Fatalf("write meta %s: %v", id, err)
		}
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set mtime %s: %v", id, err)
	}
}

func TestSessionListReturnsPersistedSessions(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	// Two sessions in /work, one in /elsewhere, one subagent id, one approval sidecar.
	writeSessionFile(t, dir, "sess-1", "/work", "first work session", base)
	writeSessionFile(t, dir, "sess-2", "/work", "second work session", base.Add(2*time.Hour))
	writeSessionFile(t, dir, "sess-3", "/elsewhere", "other project", base.Add(1*time.Hour))
	writeSessionFile(t, dir, "subagent-x", "/work", "child", base)
	// Sidecar files must be ignored by the listing.
	if err := os.WriteFile(dir+"/sess-1.approvals.json", []byte(`{"approvals":[]}`), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	msgStore, err := store.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h.SetSessionsDir(dir)
	h.SetRuntimeFactory(func(acpSessionID, cwd string, mcps []MCPServer) (*SessionRuntime, error) {
		return &SessionRuntime{}, nil
	})

	// Filter by cwd=/work: only sess-1 and sess-2, sorted by mtime desc.
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 2},
		Method:  MethodSessionList,
		Params:  json.RawMessage(`{"cwd": "/work"}`),
	})
	var resp struct {
		Result ListSessionsResponse `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("parse session/list response: %v", err)
	}
	got := resp.Result.Sessions
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions for cwd=/work, got %d: %+v", len(got), got)
	}
	if got[0].SessionID != "sess-2" || got[1].SessionID != "sess-1" {
		t.Fatalf("expected mtime-desc order sess-2, sess-1, got %s, %s", got[0].SessionID, got[1].SessionID)
	}
	if got[0].Cwd != "/work" || got[0].Title != "second work session" {
		t.Fatalf("unexpected session info: %+v", got[0])
	}
	if got[0].UpdatedAt == "" {
		t.Fatal("expected RFC3339 updatedAt")
	}

	// No cwd filter: all non-subagent sessions, again mtime desc.
	buf.Reset()
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 3},
		Method:  MethodSessionList,
		Params:  json.RawMessage(`{}`),
	})
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("parse unfiltered session/list response: %v", err)
	}
	got = resp.Result.Sessions
	if len(got) != 3 {
		t.Fatalf("expected 3 sessions unfiltered, got %d: %+v", len(got), got)
	}
	if got[0].SessionID != "sess-2" || got[2].SessionID != "sess-1" {
		t.Fatalf("unexpected unfiltered order: %s, %s, %s", got[0].SessionID, got[1].SessionID, got[2].SessionID)
	}
}

func TestSessionListMissingDirReturnsEmpty(t *testing.T) {
	dir := t.TempDir() + "/does-not-exist"
	msgStore, err := store.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h.SetSessionsDir(dir)
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionList,
		Params:  json.RawMessage(`{}`),
	})
	var resp struct {
		Result ListSessionsResponse `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Result.Sessions) != 0 {
		t.Fatalf("expected empty list, got %+v", resp.Result.Sessions)
	}
}
