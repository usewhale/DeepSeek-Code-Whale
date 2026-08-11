package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy"
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
	if sc.Delete == nil {
		t.Fatalf("expected sessionCapabilities.delete to be advertised, got %+v", sc)
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

// listSessionsFromDir builds a handler over dir and issues a session/list
// request with the given raw params, returning the parsed response.
func listSessionsRaw(t *testing.T, storeDir, sessionsDir, defaultCwd, params string) (ListSessionsResponse, error) {
	t.Helper()
	msgStore, err := store.NewJSONLStore(storeDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, defaultCwd)
	h.SetSessionsDir(sessionsDir)
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionList,
		Params:  json.RawMessage(params),
	})
	var resp struct {
		Result ListSessionsResponse `json:"result"`
		Err    map[string]any       `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		return ListSessionsResponse{}, fmt.Errorf("parse response: %w", err)
	}
	if resp.Err != nil {
		return ListSessionsResponse{}, fmt.Errorf("rpc error: %v", resp.Err)
	}
	return resp.Result, nil
}

func listSessionsFromDir(t *testing.T, dir, defaultCwd, params string) (ListSessionsResponse, error) {
	return listSessionsRaw(t, dir, dir, defaultCwd, params)
}

// listSessionsNoParams issues session/list with the params field omitted
// entirely (nil), which is legal per the ACP spec.
func listSessionsNoParams(t *testing.T, dir, defaultCwd string) (ListSessionsResponse, error) {
	t.Helper()
	msgStore, err := store.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, defaultCwd)
	h.SetSessionsDir(dir)
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionList,
	})
	var resp struct {
		Result ListSessionsResponse `json:"result"`
		Err    map[string]any       `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		return ListSessionsResponse{}, fmt.Errorf("parse response: %w", err)
	}
	if resp.Err != nil {
		return ListSessionsResponse{}, fmt.Errorf("rpc error: %v", resp.Err)
	}
	return resp.Result, nil
}

func listSessionIDs(resp ListSessionsResponse) []string {
	ids := make([]string, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		ids = append(ids, s.SessionID)
	}
	return ids
}

// TestSessionListDefaultCwdFallback: a session without a meta sidecar lists
// with the handler's default cwd; a cwd filter excludes it unless the filter
// equals the default.
func TestSessionListDefaultCwdFallback(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "no-meta", "", "untracked", base)

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("unfiltered: %v", err)
	}
	if got := listSessionIDs(resp); len(got) != 1 || got[0] != "no-meta" {
		t.Fatalf("unfiltered ids = %v, want [no-meta]", got)
	}
	if len(resp.Sessions) != 1 || resp.Sessions[0].Cwd != "/work" {
		t.Fatalf("expected default cwd /work, got %+v", resp.Sessions)
	}

	resp, err = listSessionsFromDir(t, dir, "/work", `{"cwd":"/elsewhere"}`)
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if len(resp.Sessions) != 0 {
		t.Fatalf("no-meta session must be excluded by a non-default cwd filter, got %+v", resp.Sessions)
	}

	resp, err = listSessionsFromDir(t, dir, "/work", `{"cwd":"/work"}`)
	if err != nil {
		t.Fatalf("default-filtered: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("no-meta session must match a filter equal to the default cwd, got %+v", resp.Sessions)
	}
}

// TestSessionListExcludesSubagentVariants locks both subagent id shapes
// (parent--subagent-* containment and subagent-* prefix) via the shared
// session.IsSubagentSessionID predicate.
func TestSessionListExcludesSubagentVariants(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "parent--subagent-call-1", "/work", "child", base)
	writeSessionFile(t, dir, "subagent-call-2", "/work", "child", base)
	writeSessionFile(t, dir, "real-session", "/work", "real", base)

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := listSessionIDs(resp); len(got) != 1 || got[0] != "real-session" {
		t.Fatalf("ids = %v, want [real-session] only (subagent variants excluded)", got)
	}
}

// TestSessionListTitleFallbacks: empty sessions and sessions whose first user
// message is hidden fall back to "(no message yet)"; the first *visible* user
// message becomes the title.
func TestSessionListTitleFallbacks(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	if err := os.WriteFile(dir+"/empty.jsonl", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir+"/empty.jsonl", base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/system-only.jsonl",
		[]byte(`{"Role":"system","Text":"be helpful"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir+"/system-only.jsonl", base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/hidden-first.jsonl",
		[]byte(`{"Role":"user","Text":"hidden intro","Hidden":true}`+"\n"+
			`{"Role":"user","Text":"visible ask"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir+"/hidden-first.jsonl", base, base); err != nil {
		t.Fatal(err)
	}

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]string{}
	for _, s := range resp.Sessions {
		byID[s.SessionID] = s.Title
	}
	for _, id := range []string{"empty", "system-only"} {
		if byID[id] != "(no message yet)" {
			t.Fatalf("title(%s) = %q, want %q", id, byID[id], "(no message yet)")
		}
	}
	if byID["hidden-first"] != "visible ask" {
		t.Fatalf("title(hidden-first) = %q, want %q (first visible user message)", byID["hidden-first"], "visible ask")
	}
}

// TestSessionListIgnoresEventSidecars: approval/tool-input event logs are
// .jsonl files but must never surface as sessions.
func TestSessionListIgnoresEventSidecars(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "real", "/work", "real", base)
	if err := os.WriteFile(dir+"/real.approval_events.jsonl", []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/real.tool_input_events.jsonl", []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := listSessionIDs(resp); len(got) != 1 || got[0] != "real" {
		t.Fatalf("ids = %v, want [real] only (event sidecars ignored)", got)
	}
}

// TestSessionListCursorIgnoredNoPagination pins the documented behavior: the
// list is not paginated, so a cursor request returns the full list with no
// nextCursor.
func TestSessionListCursorIgnoredNoPagination(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "s1", "/work", "one", base)
	writeSessionFile(t, dir, "s2", "/work", "two", base.Add(time.Hour))

	resp, err := listSessionsFromDir(t, dir, "/work", `{"cursor":"abc"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Fatalf("cursor request must return the full list, got %+v", resp.Sessions)
	}
	if resp.NextCursor != "" {
		t.Fatalf("nextCursor = %q, want empty (no pagination)", resp.NextCursor)
	}
}

// TestSessionListCwdNormalized: the stored cwd (client-sent verbatim) is
// cleaned before comparison, so "/work/" and "/work" agree.
func TestSessionListCwdNormalized(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "s1", "/work/", "trailing slash", base)

	resp, err := listSessionsFromDir(t, dir, "/work", `{"cwd":"/work"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("stored \"/work/\" must match filter \"/work\", got %+v", resp.Sessions)
	}
	if resp.Sessions[0].Cwd != "/work" {
		t.Fatalf("reported cwd = %q, want cleaned \"/work\"", resp.Sessions[0].Cwd)
	}
}

// TestSessionListConcurrentWithWrites exercises the file-level concurrency of
// session/list: JSONL appends and session/new meta writes happen on other
// goroutines while listing runs. Run under -race; every list response must be
// valid and the final list must contain every created session.
func TestSessionListConcurrentWithWrites(t *testing.T) {
	dir := t.TempDir()
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

	const writers = 4
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				sid := fmt.Sprintf("sess-%d-%d", n, j)
				if _, err := msgStore.Create(context.Background(), core.Message{SessionID: sid, Role: core.RoleUser, Text: "hello"}); err != nil {
					t.Errorf("create %s: %v", sid, err)
				}
				// session/new writes a meta sidecar concurrently.
				h.handleRequest(&RPCRequest{
					JSONRPC: "2.0",
					ID:      &RequestID{Value: n*100 + j},
					Method:  MethodSessionNew,
					Params:  json.RawMessage(`{"cwd":"/work"}`),
				})
			}
		}(i)
	}
	for k := 0; k < 20; k++ {
		h.handleRequest(&RPCRequest{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: 1000 + k},
			Method:  MethodSessionList,
			Params:  json.RawMessage(`{}`),
		})
	}
	wg.Wait()

	// Every in-flight session/list response (id >= 1000) must be valid JSON
	// with no error object.
	lines := strings.Split(buf.String(), "\n")
	listCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			ID  float64        `json:"id"`
			Err map[string]any `json:"error"`
			Res *struct {
				Sessions []SessionInfo `json:"sessions"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("bad response line: %v", err)
		}
		if msg.ID >= 1000 {
			listCount++
			if msg.Err != nil {
				t.Fatalf("session/list errored under concurrency: %v", msg.Err)
			}
			if msg.Res == nil {
				t.Fatalf("session/list response missing result: %s", line)
			}
		}
	}
	if listCount == 0 {
		t.Fatal("expected at least one session/list response")
	}

	// Final list after all writes: every created session is present.
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 9999},
		Method:  MethodSessionList,
		Params:  json.RawMessage(`{}`),
	})
	var final struct {
		Result ListSessionsResponse `json:"result"`
		Err    map[string]any       `json:"error"`
	}
	lastLine := ""
	// Fresh snapshot taken after the 9999 request was sent: its response is
	// the final JSON line.
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lastLine = line
		}
	}
	// 9999 was the last request, so its response is the final JSON line.
	if err := json.Unmarshal([]byte(lastLine), &final); err != nil {
		t.Fatalf("parse final response: %v", err)
	}
	if final.Err != nil {
		t.Fatalf("final list errored: %v", final.Err)
	}
	got := map[string]bool{}
	for _, s := range final.Result.Sessions {
		got[s.SessionID] = true
	}
	for i := 0; i < writers; i++ {
		for j := 0; j < 5; j++ {
			sid := fmt.Sprintf("sess-%d-%d", i, j)
			if !got[sid] {
				t.Fatalf("final list missing created session %s (got %d sessions)", sid, len(final.Result.Sessions))
			}
		}
	}
}

// writeSessionFileWithUpdatedAt writes a session whose last message carries an
// explicit UpdatedAt, then forces the file mtime separately — reproducing a
// compaction/fork rewrite that bumped the mtime without touching the content.
func writeSessionFileWithUpdatedAt(t *testing.T, dir, id string, lastMsgTime, mtime time.Time) {
	t.Helper()
	line := `{"Role":"user","Text":"activity","UpdatedAt":"` + lastMsgTime.UTC().Format(time.RFC3339) + `"}` + "\n"
	if err := os.WriteFile(dir+"/"+id+".jsonl", []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir+"/"+id+".jsonl", mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestSessionListUpdatedAtFromLastMessage: UpdatedAt reflects the last
// message's persisted timestamp, not the file mtime — a rewrite (compaction,
// fork) that bumps the mtime must not change the reported last-activity time.
func TestSessionListUpdatedAtFromLastMessage(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	lastMsg := now.Add(-72 * time.Hour)
	writeSessionFileWithUpdatedAt(t, dir, "rewritten", lastMsg, now)

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want 1", resp.Sessions)
	}
	want := lastMsg.UTC().Format(time.RFC3339)
	if resp.Sessions[0].UpdatedAt != want {
		t.Fatalf("UpdatedAt = %q, want %q (last message time, not file mtime %s)",
			resp.Sessions[0].UpdatedAt, want, now.UTC().Format(time.RFC3339))
	}
}

// TestSessionListSortsByLastActivity: recency ordering uses last message
// activity, so a rewritten session whose file mtime is newer but whose last
// message is older sorts after a genuinely more recent session.
func TestSessionListSortsByLastActivity(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// A: file rewritten (mtime now), last message 3 days ago.
	writeSessionFileWithUpdatedAt(t, dir, "a-rewritten", now.Add(-72*time.Hour), now)
	// B: file mtime and last message both 2 days ago.
	writeSessionFileWithUpdatedAt(t, dir, "b-active", now.Add(-48*time.Hour), now.Add(-48*time.Hour))

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := strings.Join(listSessionIDs(resp), ",")
	if got != "b-active,a-rewritten" {
		t.Fatalf("order = %q, want %q (by last message activity, not file mtime)", got, "b-active,a-rewritten")
	}
}

// TestSessionListEmptyParamsOK: the params object is optional per ACP; a
// client that omits it must get a successful listing, not an error.
func TestSessionListEmptyParamsOK(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "s1", "/work", "one", base)

	resp, err := listSessionsNoParams(t, dir, "/work")
	if err != nil {
		t.Fatalf("list without params: %v", err)
	}
	if got := listSessionIDs(resp); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("ids = %v, want [s1]", got)
	}
}

// TestSessionListInvalidParams: malformed params yield an invalid-params error.
func TestSessionListInvalidParams(t *testing.T) {
	dir := t.TempDir()
	if _, err := listSessionsFromDir(t, dir, "/work", `{`); err == nil {
		t.Fatal("expected invalid params error")
	}
}

// TestSessionListSessionsDirIsFile: a sessions dir that is actually a file
// (ReadDir returns a non-NotExist error) must produce an error response, not
// a hang or empty list.
func TestSessionListSessionsDirIsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listSessionsRaw(t, dir, file, "/work", `{}`); err == nil {
		t.Fatal("expected error when sessions dir is a file")
	}
}

// TestSessionListIgnoresTmpFiles: in-flight rewrite artifacts (<id>.jsonl.tmp)
// must never surface as sessions.
func TestSessionListIgnoresTmpFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "real", "/work", "real", base)
	if err := os.WriteFile(dir+"/real.jsonl.tmp", []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := listSessionIDs(resp); len(got) != 1 || got[0] != "real" {
		t.Fatalf("ids = %v, want [real] (tmp artifact ignored)", got)
	}
}

// TestSessionListDeterministicOrder: equal timestamps break ties by session id
// (sort.Slice is unstable).
func TestSessionListDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	writeSessionFile(t, dir, "z-last", "/work", "z", base)
	writeSessionFile(t, dir, "a-first", "/work", "a", base)

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := strings.Join(listSessionIDs(resp), ",")
	if got != "a-first,z-last" {
		t.Fatalf("order = %q, want %q (id tie-break)", got, "a-first,z-last")
	}
}

// TestSessionListConcurrentWithRewrite races session/list against session
// rewrites (compaction/fork tmp+rename) in addition to appends and meta
// writes; every list response must stay valid and the final list complete.
func TestSessionListConcurrentWithRewrite(t *testing.T) {
	dir := t.TempDir()
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

	var wg sync.WaitGroup
	// Appender + session/new meta writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 5; j++ {
			sid := fmt.Sprintf("sess-%d", j)
			if _, err := msgStore.Create(context.Background(), core.Message{SessionID: sid, Role: core.RoleUser, Text: "hello"}); err != nil {
				t.Errorf("create %s: %v", sid, err)
			}
			h.handleRequest(&RPCRequest{JSONRPC: "2.0", ID: &RequestID{Value: j}, Method: MethodSessionNew, Params: json.RawMessage(`{"cwd":"/work"}`)})
		}
	}()
	// Rewriter: repeatedly rewrites one session (tmp+rename) while listing runs.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := msgStore.Create(context.Background(), core.Message{SessionID: "rewrite-me", Role: core.RoleUser, Text: "r"}); err != nil {
			t.Errorf("create rewrite-me: %v", err)
			return
		}
		msgs, _ := msgStore.List(context.Background(), "rewrite-me")
		for k := 0; k < 10; k++ {
			if err := msgStore.RewriteSession(context.Background(), "rewrite-me", msgs); err != nil {
				t.Errorf("rewrite: %v", err)
			}
		}
	}()
	for k := 0; k < 20; k++ {
		h.handleRequest(&RPCRequest{JSONRPC: "2.0", ID: &RequestID{Value: 1000 + k}, Method: MethodSessionList, Params: json.RawMessage(`{}`)})
	}
	wg.Wait()

	// Every in-flight list response must be valid JSON with no error.
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			ID  float64        `json:"id"`
			Err map[string]any `json:"error"`
			Res *struct {
				Sessions []SessionInfo `json:"sessions"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("bad response line: %v", err)
		}
		if msg.ID >= 1000 {
			if msg.Err != nil {
				t.Fatalf("session/list errored during rewrite: %v", msg.Err)
			}
			if msg.Res == nil {
				t.Fatalf("session/list response missing result: %s", line)
			}
		}
	}

	// Final list contains every created session, incl. the rewritten one.
	h.handleRequest(&RPCRequest{JSONRPC: "2.0", ID: &RequestID{Value: 9999}, Method: MethodSessionList, Params: json.RawMessage(`{}`)})
	lastLine := ""
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lastLine = line
		}
	}
	var final struct {
		Result ListSessionsResponse `json:"result"`
		Err    map[string]any       `json:"error"`
	}
	if err := json.Unmarshal([]byte(lastLine), &final); err != nil {
		t.Fatalf("parse final response: %v", err)
	}
	if final.Err != nil {
		t.Fatalf("final list errored: %v", final.Err)
	}
	got := map[string]bool{}
	for _, s := range final.Result.Sessions {
		got[s.SessionID] = true
	}
	for j := 0; j < 5; j++ {
		if !got[fmt.Sprintf("sess-%d", j)] {
			t.Fatalf("final list missing sess-%d", j)
		}
	}
	if !got["rewrite-me"] {
		t.Fatalf("final list missing rewritten session rewrite-me")
	}
}

func TestTranslateEventContextCompactedDroppedWithoutPanic(t *testing.T) {
	h := newHandlerForTest(t, &factoryRecorder{})
	// Compaction is an internal history rewrite. It must be dropped (nil),
	// never surfaced as a stray agent message chunk, and must not panic even
	// with a populated CompactInfo payload.
	if u := h.translateEvent(agent.AgentEvent{Type: agent.AgentEventTypeContextCompacted}); u != nil {
		t.Fatalf("ContextCompacted translated to %+v, want nil", u)
	}
	if u := h.translateEvent(agent.AgentEvent{
		Type:    agent.AgentEventTypeContextCompacted,
		Content: "compact",
		Compact: &agent.CompactInfo{Compacted: true, BeforeEstimate: 900_000, AfterEstimate: 120_000},
	}); u != nil {
		t.Fatalf("ContextCompacted with info translated to %+v, want nil", u)
	}
}

// deleteSessionRaw issues a session/delete request over a fresh handler bound
// to the given store/sessions dirs, returning the parsed RPC error object
// (nil on success). Params is used verbatim; empty means the params field is
// omitted entirely.
func deleteSessionRaw(t *testing.T, storeDir, sessionsDir, defaultCwd, params string) (map[string]any, error) {
	t.Helper()
	msgStore, err := store.NewJSONLStore(storeDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, defaultCwd)
	h.SetSessionsDir(sessionsDir)
	req := &RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionDelete,
	}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	h.handleRequest(req)
	var resp struct {
		Result DeleteSessionResponse `json:"result"`
		Err    map[string]any        `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return resp.Err, nil
}

func assertSessionArtifactsGone(t *testing.T, dir, id string) {
	t.Helper()
	for _, suffix := range []string{".jsonl", ".meta.json", ".state.json", ".todo.json", ".user_input.json", ".goal.json", ".approval_events.jsonl", ".tool_input_events.jsonl", ".approvals.json", ".jsonl.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, core.SanitizeSessionID(id)+suffix)); !os.IsNotExist(err) {
			t.Fatalf("expected %s%s removed, stat err=%v", id, suffix, err)
		}
	}
}

// TestSessionDeleteRemovesPersistedSession: happy path — the primary .jsonl
// and every sidecar (meta, telemetry events, approvals, stale rewrite tmp)
// are removed, and session/list no longer lists the session.
func TestSessionDeleteRemovesPersistedSession(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "sess-1", "/work", "hello", time.Now())
	for _, suffix := range []string{".state.json", ".todo.json", ".user_input.json", ".goal.json", ".approval_events.jsonl", ".tool_input_events.jsonl", ".approvals.json", ".jsonl.tmp"} {
		if err := os.WriteFile(dir+"/sess-1"+suffix, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rpcErr, err := deleteSessionRaw(t, dir, dir, "/work", `{"sessionId":"sess-1"}`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rpcErr != nil {
		t.Fatalf("delete errored: %v", rpcErr)
	}
	assertSessionArtifactsGone(t, dir, "sess-1")

	resp, err := listSessionsFromDir(t, dir, "/work", `{}`)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if got := listSessionIDs(resp); len(got) != 0 {
		t.Fatalf("deleted session still listed: %v", got)
	}
}

// TestSessionDeleteUnknownIDIsIdempotentSuccess: deleting a session that has
// no files and no live context succeeds (delete is naturally idempotent; the
// client refreshes its list unconditionally) and removes nothing else.
func TestSessionDeleteUnknownIDIsIdempotentSuccess(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "keep", "/work", "keep me", time.Now())

	rpcErr, err := deleteSessionRaw(t, dir, dir, "/work", `{"sessionId":"nope"}`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rpcErr != nil {
		t.Fatalf("unknown-session delete must succeed, got %v", rpcErr)
	}
	if _, err := os.Stat(dir + "/keep.jsonl"); err != nil {
		t.Fatalf("unrelated session removed: %v", err)
	}
}

// TestSessionDeleteInvalidParams: missing/malformed params, empty ids, and
// path-unsafe ids are rejected with ErrCodeInvalidParams before any file
// access; the session file must be untouched.
func TestSessionDeleteInvalidParams(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "sess-1", "/work", "hello", time.Now())

	cases := []struct{ name, params string }{
		{"omitted params", ""},
		{"malformed params", `{`},
		{"empty id", `{"sessionId":""}`},
		{"blank id", `{"sessionId":"   "}`},
		{"dot", `{"sessionId":"."}`},
		{"dotdot", `{"sessionId":".."}`},
		{"traversal", `{"sessionId":"../x"}`},
		{"slash", `{"sessionId":"a/b"}`},
		{"backslash", `{"sessionId":"a\\b"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rpcErr, err := deleteSessionRaw(t, dir, dir, "/work", tc.params)
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if rpcErr == nil {
				t.Fatalf("expected error for %q", tc.params)
			}
			if code, _ := rpcErr["code"].(float64); int(code) != ErrCodeInvalidParams {
				t.Fatalf("code = %v, want %d", rpcErr["code"], ErrCodeInvalidParams)
			}
			if _, err := os.Stat(dir + "/sess-1.jsonl"); err != nil {
				t.Fatalf("session file touched by invalid delete: %v", err)
			}
		})
	}
}

// TestSessionDeleteMetaDirUnset: without a configured sessions directory the
// handler cannot remove artifacts and must report an internal error rather
// than a false success.
func TestSessionDeleteMetaDirUnset(t *testing.T) {
	msgStore, err := store.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionDelete,
		Params:  json.RawMessage(`{"sessionId":"acp-x"}`),
	})
	var resp struct {
		Err struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Err.Code != ErrCodeInternal {
		t.Fatalf("code = %d, want %d", resp.Err.Code, ErrCodeInternal)
	}
}

// TestSessionDeleteLiveIdleSession: a live-but-idle session is removed from
// the handler map, its runtime Close hook fires off the request path, and its
// persisted artifacts are removed.
func TestSessionDeleteLiveIdleSession(t *testing.T) {
	dir := t.TempDir()
	closed := make(chan struct{})
	var once sync.Once
	rec := &factoryRecorder{closeFn: func() { once.Do(func() { close(closed) }) }}
	msgStore, err := store.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h.SetSessionsDir(dir)
	h.SetRuntimeFactory(func(acpSessionID, cwd string, mcps []MCPServer) (*SessionRuntime, error) {
		return &SessionRuntime{Close: rec.closeFn}, nil
	})
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionNew,
		Params:  json.RawMessage(`{"cwd":"/work"}`),
	})
	var id string
	h.mu.Lock()
	for k := range h.sessions {
		id = k
	}
	h.mu.Unlock()
	if id == "" {
		t.Fatal("no session created")
	}
	if err := os.WriteFile(dir+"/"+id+".jsonl", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf.Reset()
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 2},
		Method:  MethodSessionDelete,
		Params:  json.RawMessage(`{"sessionId":"` + id + `"}`),
	})
	var resp struct {
		Result DeleteSessionResponse `json:"result"`
		Err    map[string]any        `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("parse delete response: %v", err)
	}
	if resp.Err != nil {
		t.Fatalf("delete live idle session errored: %v", resp.Err)
	}
	h.mu.Lock()
	_, stillLive := h.sessions[id]
	h.mu.Unlock()
	if stillLive {
		t.Fatal("deleted session still in handler map")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime Close not invoked for deleted idle session")
	}
	if _, err := os.Stat(dir + "/" + id + ".jsonl"); !os.IsNotExist(err) {
		t.Fatalf("live session artifact not removed: %v", err)
	}
}

// TestSessionDeleteRefusesInFlight: a session with an active prompt cannot be
// deleted — every .jsonl writer (turn append, compaction rewrite) runs during
// a turn, so deleting mid-turn would let the prompt resurrect the file. The
// session stays live and its artifacts stay intact.
func TestSessionDeleteRefusesInFlight(t *testing.T) {
	dir := t.TempDir()
	closed := 0
	rec := &factoryRecorder{closeFn: func() { closed++ }}
	msgStore, err := store.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h.SetSessionsDir(dir)
	h.SetRuntimeFactory(func(acpSessionID, cwd string, mcps []MCPServer) (*SessionRuntime, error) {
		return &SessionRuntime{Close: rec.closeFn}, nil
	})
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionNew,
		Params:  json.RawMessage(`{"cwd":"/work"}`),
	})
	var id string
	h.mu.Lock()
	for k := range h.sessions {
		id = k
	}
	h.sessions[id].runs = map[*promptRun]struct{}{{}: {}}
	h.mu.Unlock()
	if err := os.WriteFile(dir+"/"+id+".jsonl", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf.Reset()
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 2},
		Method:  MethodSessionDelete,
		Params:  json.RawMessage(`{"sessionId":"` + id + `"}`),
	})
	var resp struct {
		Err struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("parse delete response: %v", err)
	}
	if resp.Err.Code != ErrCodeInternal {
		t.Fatalf("code = %d, want %d (in-flight refusal)", resp.Err.Code, ErrCodeInternal)
	}
	h.mu.Lock()
	_, stillLive := h.sessions[id]
	h.mu.Unlock()
	if !stillLive {
		t.Fatal("in-flight session removed from map despite refusal")
	}
	if _, err := os.Stat(dir + "/" + id + ".jsonl"); err != nil {
		t.Fatalf("in-flight session artifact removed: %v", err)
	}
	if closed != 0 {
		t.Fatalf("runtime Close invoked on refused delete (%d calls)", closed)
	}
}

// TestSessionDeleteThenLoadReplaysNothing pins the post-delete contract:
// session/load finds no messages and replays zero updates (load is lenient —
// it logs and continues with an empty history rather than erroring).
func TestSessionDeleteThenLoadReplaysNothing(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "gone", "/work", "hello", time.Now())
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
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 1},
		Method:  MethodSessionDelete,
		Params:  json.RawMessage(`{"sessionId":"gone"}`),
	})

	buf.Reset()
	h.handleRequest(&RPCRequest{
		JSONRPC: "2.0",
		ID:      &RequestID{Value: 2},
		Method:  MethodSessionLoad,
		Params:  json.RawMessage(`{"sessionId":"gone","cwd":"/work"}`),
	})
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("bad response line: %v", err)
		}
		if msg.Method == MethodSessionUpdate {
			t.Fatalf("load after delete replayed a message: %s", line)
		}
	}
}

// TestSessionDeleteJSONLRemovalFailureIsFatal: when the primary .jsonl cannot
// be removed (here, it is a directory), delete must fail with an internal
// error rather than report success — the session's history must not silently
// survive a successful delete.
func TestSessionDeleteJSONLRemovalFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/blocked.jsonl", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/blocked.jsonl/child", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpcErr, err := deleteSessionRaw(t, dir, dir, "/work", `{"sessionId":"blocked"}`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rpcErr == nil {
		t.Fatal("delete succeeded despite failed .jsonl removal")
	}
	if code, _ := rpcErr["code"].(float64); int(code) != ErrCodeInternal {
		t.Fatalf("code = %v, want %d", rpcErr["code"], ErrCodeInternal)
	}
	if st, err := os.Stat(dir + "/blocked.jsonl"); err != nil || !st.IsDir() {
		t.Fatalf("blocking path not preserved: %v", err)
	}
}

// TestSessionDeleteConcurrentWithList hammers session/delete (idempotent) and
// session/list from many goroutines over one shared handler. Run under -race:
// every response must be well-formed, no session may error spuriously, and the
// victim's artifacts must be gone at the end.
func TestSessionDeleteConcurrentWithList(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "victim", "/work", "hello", time.Now())
	msgStore, err := store.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	var buf bytes.Buffer
	h := NewHandler(NewTransportWithIO(&buf, &buf, &buf), msgStore, "/work")
	h.SetSessionsDir(dir)

	const deleters = 8
	var wg sync.WaitGroup
	for i := 0; i < deleters; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				h.handleRequest(&RPCRequest{
					JSONRPC: "2.0",
					ID:      &RequestID{Value: n*1000 + j},
					Method:  MethodSessionDelete,
					Params:  json.RawMessage(`{"sessionId":"victim"}`),
				})
			}
		}(i)
	}
	for k := 0; k < 25; k++ {
		h.handleRequest(&RPCRequest{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: 10000 + k},
			Method:  MethodSessionList,
			Params:  json.RawMessage(`{}`),
		})
	}
	wg.Wait()

	deleteErrors := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			ID  float64        `json:"id"`
			Err map[string]any `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("bad response line: %v", err)
		}
		if msg.ID < 10000 && msg.Err != nil {
			deleteErrors++
		}
	}
	if deleteErrors != 0 {
		t.Fatalf("%d session/delete calls errored (delete must be idempotent)", deleteErrors)
	}
	assertSessionArtifactsGone(t, dir, "victim")
}

// TestSessionDeleteMetaDirIsFile: when the sessions directory path is a
// regular file, removing artifacts under it fails with ENOTDIR — delete must
// report an internal error instead of a false success.
func TestSessionDeleteMetaDirIsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpcErr, err := deleteSessionRaw(t, dir, file, "/work", `{"sessionId":"sess-1"}`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rpcErr == nil {
		t.Fatal("delete succeeded with a file as sessions dir")
	}
	if code, _ := rpcErr["code"].(float64); int(code) != ErrCodeInternal {
		t.Fatalf("code = %v, want %d", rpcErr["code"], ErrCodeInternal)
	}
}

// TestSessionDeleteIgnoresMetaField: the ACP schema allows an optional _meta
// object on any request; it must not break delete.
func TestSessionDeleteIgnoresMetaField(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "sess-1", "/work", "hello", time.Now())
	rpcErr, err := deleteSessionRaw(t, dir, dir, "/work", `{"sessionId":"sess-1","_meta":{"why":"cleanup"}}`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rpcErr != nil {
		t.Fatalf("delete with _meta errored: %v", rpcErr)
	}
	assertSessionArtifactsGone(t, dir, "sess-1")
}

// ---------------------------------------------------------------------------
// Permission option kinds — schema-valid regression tests
// ---------------------------------------------------------------------------
//
// The ACP schema (agent-client-protocol-schema v1/client.rs and v2/client.rs)
// defines exactly four PermissionOptionKind values:
// allow_once | allow_always | reject_once | reject_always. A client like Zed
// deserializes the request_permission payload with a strict serde enum, so any
// other string (e.g. "allow_tool"/"allow_server") fails at deserialization and
// the approval is silently denied.

// runApprovalFunc drives NewACPApprovalFunc against a pipe transport, captures the
// outbound session/request_permission payload, hands the request id + pending
// response channel to respond (which must deliver or close a response), and
// returns the payload and the resulting policy decision.
func runApprovalFunc(t *testing.T, toolName string, respond func(t *testing.T, numID int64, ch chan json.RawMessage)) (RequestPermissionRequest, policy.ApprovalDecision) {
	t.Helper()
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	transport := NewTransportWithIO(strings.NewReader(""), pw, io.Discard)
	approvalFn := NewACPApprovalFunc(transport)

	done := make(chan policy.ApprovalDecision, 1)
	go func() {
		done <- approvalFn(policy.ApprovalRequest{
			SessionID: "sess-1",
			ToolCall:  core.ToolCall{ID: "tc-1", Name: toolName, Input: `{"command":"ls"}`},
		})
	}()

	scanner := bufio.NewScanner(pr)
	if !scanner.Scan() {
		t.Fatalf("no request_permission call written: %v", scanner.Err())
	}
	var rpcReq RPCRequest
	if err := json.Unmarshal(scanner.Bytes(), &rpcReq); err != nil {
		t.Fatalf("parse outbound request: %v", err)
	}
	var permReq RequestPermissionRequest
	if err := json.Unmarshal(rpcReq.Params, &permReq); err != nil {
		t.Fatalf("parse outbound params: %v", err)
	}
	numID, ok := rpcReq.ID.Value.(float64)
	if !ok {
		t.Fatalf("request id is %T (%v), want float64", rpcReq.ID.Value, rpcReq.ID.Value)
	}
	transport.pendingMu.Lock()
	ch, ok := transport.pending[int64(numID)]
	transport.pendingMu.Unlock()
	if !ok {
		t.Fatalf("no pending channel for id %d", int64(numID))
	}

	respond(t, int64(numID), ch)

	select {
	case got := <-done:
		return permReq, got
	case <-time.After(5 * time.Second):
		t.Fatal("approval func did not return")
		return permReq, policy.ApprovalDeny
	}
}

// capturePermissionRequest runs NewACPApprovalFunc against a pipe transport and
// returns the outbound session/request_permission payload, answering the call
// with a v1-nested "selected once" result so the func completes.
func capturePermissionRequest(t *testing.T, toolName string) RequestPermissionRequest {
	t.Helper()
	permReq, _ := runApprovalFunc(t, toolName, func(t *testing.T, numID int64, ch chan json.RawMessage) {
		t.Helper()
		raw, err := json.Marshal(RPCResponse{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: numID},
			Result:  json.RawMessage(`{"outcome":{"outcome":"selected","optionId":"once"}}`),
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		ch <- raw
	})
	return permReq
}

// TestPermissionOptionKindsAreSchemaValid serializes the actual
// request_permission payload the approval func sends for every tool kind
// Whale can be asked about, and asserts every option kind is one of the four
// schema-defined values. Regression test for the live bug where MCP options
// carried "allow_tool"/"allow_server" and Zed's strict serde rejected the
// request, silently denying every MCP approval.
func TestPermissionOptionKindsAreSchemaValid(t *testing.T) {
	schemaKinds := map[PermissionOptionKind]bool{
		KindAllowOnce: true, KindAllowAlways: true, KindRejectOnce: true, KindRejectAlways: true,
	}

	toolNames := []string{
		"shell_run", "edit", "web_fetch", "update_plan",
		"mcp__codemap__find_file", "mcp__server__tool", "mcp__", "",
	}
	for _, name := range toolNames {
		t.Run(name, func(t *testing.T) {
			permReq := capturePermissionRequest(t, name)
			if len(permReq.Options) == 0 {
				t.Fatal("no options sent")
			}
			for i, opt := range permReq.Options {
				if !schemaKinds[opt.Kind] {
					t.Errorf("option %d (%q) has kind %q — not a schema-valid PermissionOptionKind", i, opt.OptionID, opt.Kind)
				}
				if !opt.Kind.Valid() {
					t.Errorf("option %d (%q): Kind.Valid()=false for %q", i, opt.OptionID, opt.Kind)
				}
			}
		})
	}

	// The wire value of every schema-valid kind must round-trip exactly.
	for _, kind := range []PermissionOptionKind{KindAllowOnce, KindAllowAlways, KindRejectOnce, KindRejectAlways} {
		if b, err := json.Marshal(kind); err != nil || string(b) != `"`+string(kind)+`"` {
			t.Errorf("kind %q marshals to %s (err %v)", kind, b, err)
		}
	}
}

// strictPermissionOptionKind mimics the ACP schema's strict serde enum
// deserialization (v1/client.rs PermissionOptionKind): any value outside
// {allow_once, allow_always, reject_once, reject_always} is rejected.
type strictPermissionOptionKind string

func decodeStrictKind(raw string) (strictPermissionOptionKind, error) {
	switch raw {
	case "allow_once", "allow_always", "reject_once", "reject_always":
		return strictPermissionOptionKind(raw), nil
	default:
		return "", fmt.Errorf("unknown variant `%s`, expected one of `allow_once`, `allow_always`, `reject_once`, `reject_always`", raw)
	}
}

// TestPermissionOptionsDeserializeOnStrictSchemaDecoder cross-checks that every
// kind the approval func sends round-trips through a strict decoder that
// rejects unknown variants exactly like the ACP schema enum used by Zed — and
// that the decoder still rejects the kinds that caused the live bug.
func TestPermissionOptionsDeserializeOnStrictSchemaDecoder(t *testing.T) {
	for _, name := range []string{"shell_run", "mcp__codemap__find_file", "mcp__a__b"} {
		permReq := capturePermissionRequest(t, name)
		for _, opt := range permReq.Options {
			if _, err := decodeStrictKind(string(opt.Kind)); err != nil {
				t.Errorf("option %q for %q: %v", opt.OptionID, name, err)
			}
		}
	}
	for _, bad := range []string{"allow_tool", "allow_server", "", "ALLOW_ONCE"} {
		if _, err := decodeStrictKind(bad); err == nil {
			t.Errorf("strict decoder accepted invalid kind %q", bad)
		}
	}
}

// TestPermissionOptionKindValid covers the Valid() guard itself.
func TestPermissionOptionKindValid(t *testing.T) {
	for _, kind := range []PermissionOptionKind{KindAllowOnce, KindAllowAlways, KindRejectOnce, KindRejectAlways} {
		if !kind.Valid() {
			t.Errorf("Valid(%q) = false, want true", kind)
		}
	}
	for _, kind := range []PermissionOptionKind{"allow_tool", "allow_server", "", "ALLOW_ONCE", "allow_once\n", "allow-once"} {
		if kind.Valid() {
			t.Errorf("Valid(%q) = true, want false", kind)
		}
	}
}

// TestInvalidPermissionOptionKind covers the send-path guard helper: an option
// carrying a non-schema kind is reported, so NewACPApprovalFunc can deny loudly
// instead of sending a payload the ACP client would reject at deserialization
// (the original silent-denial bug).
func TestInvalidPermissionOptionKind(t *testing.T) {
	if bad, ok := invalidPermissionOptionKind([]PermissionOption{
		{OptionID: "once", Kind: KindAllowOnce},
		{OptionID: "tool", Kind: "allow_tool"},
	}); !ok || bad != "allow_tool" {
		t.Errorf("invalidPermissionOptionKind = (%q, %v), want (\"allow_tool\", true)", bad, ok)
	}
	if bad, ok := invalidPermissionOptionKind([]PermissionOption{
		{OptionID: "once", Kind: KindAllowOnce},
		{OptionID: "server", Kind: "allow_server"},
	}); !ok || bad != "allow_server" {
		t.Errorf("invalidPermissionOptionKind = (%q, %v), want (\"allow_server\", true)", bad, ok)
	}
	if bad, ok := invalidPermissionOptionKind(nil); ok || bad != "" {
		t.Errorf("invalidPermissionOptionKind(nil) = (%q, %v), want (\"\", false)", bad, ok)
	}
	if bad, ok := invalidPermissionOptionKind([]PermissionOption{
		{OptionID: "once", Kind: KindAllowOnce},
		{OptionID: "always", Kind: KindAllowAlways},
		{OptionID: "reject", Kind: KindRejectOnce},
	}); ok {
		t.Errorf("invalidPermissionOptionKind(valid) = (%q, true), want false", bad)
	}
}

// ---------------------------------------------------------------------------
// Approval decision mapping — related codepath of the changed option kinds
// ---------------------------------------------------------------------------
// The options sent by NewACPApprovalFunc (typed kinds) flow into the response
// parse + decision switch. Cover every branch of that downstream codepath.

// approvalDecisionWithResult runs NewACPApprovalFunc against a pipe transport
// whose session/request_permission call is answered with the given raw result
// payload, returning the policy decision.
func approvalDecisionWithResult(t *testing.T, result string) policy.ApprovalDecision {
	t.Helper()
	_, dec := runApprovalFunc(t, "shell_run", func(t *testing.T, numID int64, ch chan json.RawMessage) {
		t.Helper()
		raw, err := json.Marshal(RPCResponse{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: numID},
			Result:  json.RawMessage(result),
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		ch <- raw
	})
	return dec
}

// TestApprovalDecisionMapping covers every optionId the approval func can
// receive from the client, plus cancelled, unknown outcome, and malformed
// result — the downstream of the typed option kinds.
func TestApprovalDecisionMapping(t *testing.T) {
	cases := []struct {
		name   string
		result string
		want   policy.ApprovalDecision
	}{
		{name: "selected once", result: `{"outcome":{"outcome":"selected","optionId":"once"}}`, want: policy.ApprovalAllow},
		{name: "selected always", result: `{"outcome":{"outcome":"selected","optionId":"always"}}`, want: policy.ApprovalAllowForSession},
		{name: "selected reject", result: `{"outcome":{"outcome":"selected","optionId":"reject"}}`, want: policy.ApprovalDeny},
		{name: "selected unknown option", result: `{"outcome":{"outcome":"selected","optionId":"bogus"}}`, want: policy.ApprovalDeny},
		{name: "selected missing option", result: `{"outcome":{"outcome":"selected"}}`, want: policy.ApprovalDeny},
		{name: "cancelled", result: `{"outcome":{"outcome":"cancelled"}}`, want: policy.ApprovalCancel},
		{name: "unknown outcome", result: `{"outcome":{"outcome":"maybe"}}`, want: policy.ApprovalDeny},
		{name: "malformed result", result: `{"outcome":42}`, want: policy.ApprovalDeny},
		{name: "non-object result", result: `"hello"`, want: policy.ApprovalDeny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := approvalDecisionWithResult(t, c.result); got != c.want {
				t.Errorf("decision = %v, want %v", got, c.want)
			}
		})
	}
}

// TestApprovalFuncErrorEnvelope verifies that a JSON-RPC error response (no
// result payload) denies rather than panics or grants.
func TestApprovalFuncErrorEnvelope(t *testing.T) {
	got := approvalDecisionWithResponder(t, func(t *testing.T, numID int64, ch chan json.RawMessage) {
		raw, err := json.Marshal(RPCErrorResponse{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: numID},
			Error:   &RPCErr{Code: -32601, Message: "method not found"},
		})
		if err != nil {
			t.Fatalf("marshal error response: %v", err)
		}
		ch <- raw
	})
	if got != policy.ApprovalDeny {
		t.Errorf("error envelope decision = %v, want %v", got, policy.ApprovalDeny)
	}
}

// TestApprovalFuncTransportClosed verifies that a transport close while
// waiting for the permission response denies the approval.
func TestApprovalFuncTransportClosed(t *testing.T) {
	got := approvalDecisionWithResponder(t, func(t *testing.T, numID int64, ch chan json.RawMessage) {
		close(ch)
	})
	if got != policy.ApprovalDeny {
		t.Errorf("closed transport decision = %v, want %v", got, policy.ApprovalDeny)
	}
}

// approvalDecisionWithResponder runs NewACPApprovalFunc against a pipe
// transport whose session/request_permission call is answered by the responder,
// which receives the outbound request id and its pending response channel and
// must deliver (or close) a response.
func approvalDecisionWithResponder(t *testing.T, respond func(t *testing.T, numID int64, ch chan json.RawMessage)) policy.ApprovalDecision {
	t.Helper()
	_, dec := runApprovalFunc(t, "shell_run", respond)
	return dec
}

// TestApprovalFuncConcurrentSessions verifies the shared approval func and
// transport handle many concurrent request_permission calls: each session gets
// a distinct request id, its own pending channel, and the correct decision —
// no cross-talk, no deadlock, no data race (run with -race).
func TestApprovalFuncConcurrentSessions(t *testing.T) {
	const n = 32
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	transport := NewTransportWithIO(strings.NewReader(""), pw, io.Discard)
	approvalFn := NewACPApprovalFunc(transport)

	type result struct {
		idx int
		dec policy.ApprovalDecision
	}
	results := make(chan result, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(idx int) {
			<-start
			req := policy.ApprovalRequest{
				SessionID: fmt.Sprintf("sess-%d", idx),
				ToolCall:  core.ToolCall{ID: fmt.Sprintf("tc-%d", idx), Name: "shell_run", Input: `{"command":"ls"}`},
			}
			results <- result{idx, approvalFn(req)}
		}(i)
	}
	close(start)

	scanner := bufio.NewScanner(pr)
	seen := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		if !scanner.Scan() {
			t.Fatalf("only %d/%d request_permission calls written: %v", i, n, scanner.Err())
		}
		var rpcReq RPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &rpcReq); err != nil {
			t.Fatalf("parse outbound request %d: %v", i, err)
		}
		var permReq RequestPermissionRequest
		if err := json.Unmarshal(rpcReq.Params, &permReq); err != nil {
			t.Fatalf("parse outbound params %d: %v", i, err)
		}
		var idx int
		if _, err := fmt.Sscanf(permReq.ToolCall.ToolCallID, "tc-%d", &idx); err != nil {
			t.Fatalf("unexpected toolCallId %q: %v", permReq.ToolCall.ToolCallID, err)
		}
		if seen[idx] {
			t.Fatalf("duplicate request for session %d", idx)
		}
		seen[idx] = true

		numID, ok := rpcReq.ID.Value.(float64)
		if !ok {
			t.Fatalf("request id is %T (%v), want float64", rpcReq.ID.Value, rpcReq.ID.Value)
		}
		id := int64(numID)
		transport.pendingMu.Lock()
		ch, ok := transport.pending[id]
		transport.pendingMu.Unlock()
		if !ok {
			t.Fatalf("no pending channel for id %d", id)
		}
		resultPayload := `{"outcome":{"outcome":"selected","optionId":"once"}}`
		if idx%2 == 1 {
			resultPayload = `{"outcome":{"outcome":"selected","optionId":"always"}}`
		}
		raw, err := json.Marshal(RPCResponse{
			JSONRPC: "2.0",
			ID:      &RequestID{Value: numID},
			Result:  json.RawMessage(resultPayload),
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		ch <- raw
	}

	for i := 0; i < n; i++ {
		r := <-results
		want := policy.ApprovalAllow
		if r.idx%2 == 1 {
			want = policy.ApprovalAllowForSession
		}
		if r.dec != want {
			t.Errorf("session %d: decision = %v, want %v", r.idx, r.dec, want)
		}
	}
}

// TestPermissionOptionKindString covers the String() rendering of the typed
// kind, used in the send-path guard's log message.
func TestPermissionOptionKindString(t *testing.T) {
	for _, kind := range []PermissionOptionKind{KindAllowOnce, KindAllowAlways, KindRejectOnce, KindRejectAlways} {
		if got := kind.String(); got != string(kind) {
			t.Errorf("String(%q) = %q, want %q", kind, got, string(kind))
		}
	}
}
