package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
	"github.com/usewhale/whale/internal/tools"
)

type Handler struct {
	transport  *Transport
	store      store.MessageStore
	defaultCwd string
	metaDir    string
	newRuntime SessionRuntimeFactory

	mu       sync.Mutex
	sessions map[string]*sessionContext
}

// SessionRuntime bundles the per-session agent and toolset. Each session gets
// its own runtime so prompts in different sessions run concurrently without
// contending on shared, mutable tool state (toolset root, policy workspace).
// A prompt waiting on a permission dialog only blocks its own session.
type SessionRuntime struct {
	Agent   *agent.Agent
	Toolset *tools.Toolset

	// Close releases per-session resources (e.g. connected MCP servers) when
	// the session ends or the process shuts down. May be nil.
	Close func()
}

// SessionRuntimeFactory builds a runtime scoped to a session's cwd. It is
// invoked once per session (at session/new or session/load). mcps are the MCP
// servers the client wants the agent to connect to; ACP carries them on both
// session/new and session/load.
type SessionRuntimeFactory func(acpSessionID, cwd string, mcps []MCPServer) (*SessionRuntime, error)

// sessionMeta is the persisted, cross-restart state for a session. Messages
// live in the message store; this sidecar captures the context that ACP's
// session/load request does not carry (cwd, mode).
type sessionMeta struct {
	Cwd  string       `json:"cwd,omitempty"`
	Mode session.Mode `json:"mode,omitempty"`
}

// promptRun tracks one in-flight prompt's cancellation. It is held by pointer
// identity so each prompt deregisters exactly its own cancel on completion —
// regardless of the order prompts acquire the session's promptMu (Go's mutex
// does not grant in FIFO order, so a shared "active cancel" slot could point at
// the wrong prompt).
type promptRun struct {
	cancel context.CancelFunc
}

type sessionContext struct {
	whaleSessionID string
	runtime        *SessionRuntime
	promptMu       sync.Mutex              // serializes prompts within this session
	runs           map[*promptRun]struct{} // in-flight prompts, guarded by Handler.mu
	cwd            string
	mode           session.Mode
	lastUsed       time.Time // guarded by Handler.mu, for LRU eviction
}

// maxLiveSessions bounds concurrently live sessions. ACP v1 has no
// session/delete, so without a cap a long-lived host that keeps creating
// sessions would leak runtimes and connected MCP servers.
const maxLiveSessions = 64

// evictIfOverLimit removes the least-recently-used session when live sessions
// exceed maxLiveSessions, returning its runtime so the caller can release it
// (e.g. close MCP servers) outside the handler lock.
func (h *Handler) evictIfOverLimit() *SessionRuntime {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) <= maxLiveSessions {
		return nil
	}
	var oldestID string
	var oldest time.Time
	for id, sctx := range h.sessions {
		if oldestID == "" || sctx.lastUsed.Before(oldest) {
			oldestID, oldest = id, sctx.lastUsed
		}
	}
	if oldestID == "" {
		return nil
	}
	rt := h.sessions[oldestID].runtime
	delete(h.sessions, oldestID)
	Logger.Printf("evicting least-recently-used session %s (live sessions exceed %d)", oldestID, maxLiveSessions)
	return rt
}

func NewHandler(transport *Transport, msgStore store.MessageStore, defaultCwd string) *Handler {
	return &Handler{
		transport:  transport,
		store:      msgStore,
		defaultCwd: defaultCwd,
		sessions:   make(map[string]*sessionContext),
	}
}

// SetRuntimeFactory sets the factory used to build a per-session agent+toolset.
func (h *Handler) SetRuntimeFactory(fn SessionRuntimeFactory) { h.newRuntime = fn }

// buildRuntime creates a session-scoped runtime via the configured factory.
func (h *Handler) buildRuntime(acpSessionID, cwd string, mcps []MCPServer) (*SessionRuntime, error) {
	if h.newRuntime == nil {
		return nil, fmt.Errorf("no session runtime factory configured")
	}
	return h.newRuntime(acpSessionID, cwd, mcps)
}

// CloseSessions tears down every live session runtime, releasing per-session
// resources such as connected MCP servers. Safe to call once at shutdown;
// sessions without a Close hook are skipped.
func (h *Handler) CloseSessions() {
	h.mu.Lock()
	sessions := make([]*SessionRuntime, 0, len(h.sessions))
	for _, sctx := range h.sessions {
		if sctx.runtime != nil {
			sessions = append(sessions, sctx.runtime)
		}
	}
	h.mu.Unlock()
	for _, rt := range sessions {
		if rt.Close != nil {
			rt.Close()
		}
	}
}

// SetSessionsDir sets the directory used to persist per-session metadata
// (cwd, mode) so it survives across process restarts and session/load.
func (h *Handler) SetSessionsDir(dir string) { h.metaDir = dir }

func (h *Handler) metaPath(sessionID string) string {
	if h.metaDir == "" || !isSafeSessionID(sessionID) {
		return ""
	}
	return filepath.Join(h.metaDir, sessionID+".meta.json")
}

// isSafeSessionID rejects ids that could escape the sessions directory when
// used as a filename component. Agent-generated ids are "acp-<hex>"; ids from
// session/load are client-supplied and must be validated before they touch the
// filesystem (metadata sidecar, message store).
func isSafeSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	return true
}

// saveSessionMeta persists a session's cwd and mode. Failures are logged and
// otherwise ignored — metadata is best-effort and never blocks a request.
func (h *Handler) saveSessionMeta(sessionID string, meta sessionMeta) {
	path := h.metaPath(sessionID)
	if path == "" {
		return
	}
	b, err := json.Marshal(meta)
	if err != nil {
		Logger.Printf("failed to marshal session meta for %s: %v", sessionID, err)
		return
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		Logger.Printf("failed to write session meta for %s: %v", sessionID, err)
	}
}

// loadSessionMeta reads persisted metadata for a session. Returns ok=false if
// no metadata exists (or it cannot be read/parsed).
func (h *Handler) loadSessionMeta(sessionID string) (sessionMeta, bool) {
	path := h.metaPath(sessionID)
	if path == "" {
		return sessionMeta{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return sessionMeta{}, false
	}
	var meta sessionMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		Logger.Printf("failed to parse session meta for %s: %v", sessionID, err)
		return sessionMeta{}, false
	}
	return meta, true
}

func (h *Handler) Run() error {
	h.transport.StartDispatcher()
	requests := h.transport.Requests()
	notifications := h.transport.Notifications()
	done := h.transport.Done()

	for {
		select {
		case item, ok := <-requests:
			if !ok {
				return nil
			}
			if item.Req.Method == MethodSessionPrompt {
				go h.handlePromptAsync(item)
			} else {
				h.handleRequest(item.Req)
			}
		case raw, ok := <-notifications:
			if !ok {
				return nil
			}
			h.handleNotificationRaw(raw)
		case <-done:
			return nil
		}
	}
}

func (h *Handler) handlePromptAsync(item *dispatchItem) {
	if errResp := h.handlePrompt(item.Req); errResp != nil {
		h.transport.SendError(errResp)
	}
}

func (h *Handler) handleNotificationRaw(raw json.RawMessage) {
	method := ExtractMethod(raw)
	params := ExtractParams(raw)
	switch method {
	case MethodSessionCancel:
		var p struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(params, &p)
		if p.SessionID == "" {
			return
		}
		cancels := h.sessionCancels(p.SessionID)
		for _, fn := range cancels {
			fn()
		}
		h.transport.CancelSession(p.SessionID)
		if len(cancels) > 0 {
			Logger.Printf("session cancelled (via notification): %s", p.SessionID)
		}
	default:
		Logger.Printf("ignoring unknown notification: %s", method)
	}
}

func (h *Handler) handleRequest(req *RPCRequest) {
	var errResp *RPCErrorResponse
	switch req.Method {
	case MethodInitialize:
		errResp = h.handleInitialize(req)
	case MethodAuthenticate:
		errResp = h.handleAuthenticate(req)
	case MethodSessionNew:
		errResp = h.handleSessionNew(req)
	case MethodSessionLoad:
		errResp = h.handleSessionLoad(req)
	case MethodSessionSetMode:
		errResp = h.handleSetMode(req)
	case MethodSessionCancel:
		errResp = h.handleCancel(req)
	default:
		errResp = NewErrorResponse(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method))
	}
	if errResp != nil {
		h.transport.SendError(errResp)
	}
}

func (h *Handler) handleInitialize(req *RPCRequest) *RPCErrorResponse {
	var params InitializeRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	if params.ClientCapabilities != nil {
		Logger.Printf("client capabilities: fs.read=%v fs.write=%v terminal=%v",
			params.ClientCapabilities.FS != nil && params.ClientCapabilities.FS.ReadTextFile,
			params.ClientCapabilities.FS != nil && params.ClientCapabilities.FS.WriteTextFile,
			params.ClientCapabilities.Terminal)
	}
	// Negotiate: respond with the highest version we support that does not
	// exceed the client's request. We only speak v1, so any request at or above
	// it settles on ProtocolVersion; a lower request means the client is too old
	// and we still answer with our version so it can decide whether to continue.
	negotiated := uint16(ProtocolVersion)
	if params.ProtocolVersion != 0 && params.ProtocolVersion < negotiated {
		Logger.Printf("client requested unsupported protocol version %d; responding with %d", params.ProtocolVersion, negotiated)
	}
	h.transport.SendResponse(NewSuccessResponse(req.ID, InitializeResponse{
		ProtocolVersion: negotiated,
		AgentCapabilities: &AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: &PromptCapabilities{
				Image: false, Audio: false, EmbeddedContext: true,
			},
			MCPCapabilities: &MCPCapabilities{HTTP: false, SSE: false},
		},
		AgentInfo: &Implementation{Name: "whale", Title: "Whale", Version: "0.1.0"},
	}))
	return nil
}

func (h *Handler) handleAuthenticate(req *RPCRequest) *RPCErrorResponse {
	h.transport.SendResponse(NewSuccessResponse(req.ID, AuthenticateResponse{}))
	return nil
}

func (h *Handler) handleSessionNew(req *RPCRequest) *RPCErrorResponse {
	var params NewSessionRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	whaleSessionID := newSessionID()
	cwd := params.Cwd
	if cwd == "" {
		cwd = h.defaultCwd
	}
	rt, err := h.buildRuntime(whaleSessionID, cwd, params.MCPServers)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternal, fmt.Sprintf("failed to initialize session: %v", err))
	}
	h.mu.Lock()
	h.sessions[whaleSessionID] = &sessionContext{whaleSessionID: whaleSessionID, runtime: rt, cwd: cwd, mode: session.ModeAgent, lastUsed: time.Now()}
	h.mu.Unlock()
	if evicted := h.evictIfOverLimit(); evicted != nil && evicted.Close != nil {
		evicted.Close()
	}
	h.saveSessionMeta(whaleSessionID, sessionMeta{Cwd: cwd, Mode: session.ModeAgent})
	Logger.Printf("new session: acp=%s cwd=%s", whaleSessionID, cwd)
	h.transport.SendResponse(NewSuccessResponse(req.ID, NewSessionResponse{
		SessionID: whaleSessionID,
		Modes:     sessionModeState("code"),
	}))
	return nil
}

func (h *Handler) handleSessionLoad(req *RPCRequest) *RPCErrorResponse {
	var params LoadSessionRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	// session/load carries a client-supplied id that is used as a filesystem
	// path component (message store, metadata sidecar); reject ids that could
	// escape their directory before any file access.
	if !isSafeSessionID(params.SessionID) {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid sessionId: %q", params.SessionID))
	}
	messages, err := h.store.List(context.Background(), params.SessionID)
	if err != nil {
		Logger.Printf("failed to load messages for session %s: %v", params.SessionID, err)
		messages = nil
	}
	// Determine the working directory and mode for the resumed session.
	// Precedence: the client-supplied cwd (which the ACP spec requires on
	// session/load) is authoritative — it reflects where the client currently
	// wants the session to run. The persisted sidecar is the fallback for
	// mode (session/load does not carry it) and for cwd when a client omits it.
	cwd := h.defaultCwd
	mode := session.ModeAgent
	meta, metaOK := h.loadSessionMeta(params.SessionID)
	if metaOK {
		if meta.Cwd != "" {
			cwd = meta.Cwd
		}
		if meta.Mode != "" {
			mode = meta.Mode
		}
	}
	if params.Cwd != "" {
		if metaOK && meta.Cwd != "" && meta.Cwd != params.Cwd {
			Logger.Printf("session/load cwd %q differs from persisted %q; honoring request", params.Cwd, meta.Cwd)
		}
		cwd = params.Cwd
	}
	h.mu.Lock()
	_, exists := h.sessions[params.SessionID]
	if exists {
		h.sessions[params.SessionID].lastUsed = time.Now()
	}
	h.mu.Unlock()
	if !exists {
		rt, err := h.buildRuntime(params.SessionID, cwd, params.MCPServers)
		if err != nil {
			return NewErrorResponse(req.ID, ErrCodeInternal, fmt.Sprintf("failed to initialize session: %v", err))
		}
		h.mu.Lock()
		// Re-check under lock in case a concurrent load created it first.
		if _, exists := h.sessions[params.SessionID]; !exists {
			h.sessions[params.SessionID] = &sessionContext{whaleSessionID: params.SessionID, runtime: rt, cwd: cwd, mode: mode, lastUsed: time.Now()}
		}
		h.mu.Unlock()
		if evicted := h.evictIfOverLimit(); evicted != nil && evicted.Close != nil {
			evicted.Close()
		}
		// Persist the resolved cwd/mode so a subsequent load (or a restart where
		// the sidecar was never written) stays consistent with the runtime we
		// just built. An already-live session keeps its established runtime, so
		// we only rewrite the sidecar when we actually adopt this cwd.
		h.saveSessionMeta(params.SessionID, sessionMeta{Cwd: cwd, Mode: mode})
	}
	for _, msg := range messages {
		if update := h.translateMessage(msg); update != nil {
			h.transport.SendNotification(MethodSessionUpdate, SessionNotification{
				SessionID: params.SessionID, Update: *update,
			})
		}
	}
	Logger.Printf("session loaded: %s (%d messages replayed)", params.SessionID, len(messages))
	currentMode := "code"
	h.mu.Lock()
	if sctx, ok := h.sessions[params.SessionID]; ok {
		switch sctx.mode {
		case session.ModeAsk:
			currentMode = "ask"
		case session.ModePlan:
			currentMode = "architect"
		}
	}
	h.mu.Unlock()
	h.transport.SendResponse(NewSuccessResponse(req.ID, LoadSessionResponse{
		Modes: sessionModeState(currentMode),
	}))
	return nil
}

var acpToWhaleMode = map[string]session.Mode{
	"code": session.ModeAgent, "ask": session.ModeAsk, "architect": session.ModePlan,
}

// sessionModeState returns the agent's advertised mode catalog with the given
// mode selected as current. Both session/new and session/load report the same
// set of modes, so the list lives in one place.
func sessionModeState(currentModeID string) *SessionModeState {
	return &SessionModeState{
		CurrentModeID: currentModeID,
		AvailableModes: []SessionMode{
			{ID: "ask", Name: "Ask", Description: "Read-only Q&A without making changes"},
			{ID: "architect", Name: "Architect", Description: "Design and plan without implementation"},
			{ID: "code", Name: "Code", Description: "Full agent with tool access"},
		},
	}
}

func (h *Handler) handleSetMode(req *RPCRequest) *RPCErrorResponse {
	var params SetSessionModeRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	h.mu.Lock()
	sctx, ok := h.sessions[params.SessionID]
	var savedCwd string
	var savedMode session.Mode
	if ok {
		wm, found := acpToWhaleMode[params.ModeID]
		if !found {
			h.mu.Unlock()
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("unknown mode: %s", params.ModeID))
		}
		sctx.mode = wm
		savedCwd = sctx.cwd
		savedMode = wm
		Logger.Printf("mode change: session=%s mode=%s", params.SessionID, wm)
	}
	h.mu.Unlock()
	if !ok {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("session not found: %s", params.SessionID))
	}
	h.saveSessionMeta(params.SessionID, sessionMeta{Cwd: savedCwd, Mode: savedMode})
	h.transport.SendResponse(NewSuccessResponse(req.ID, SetSessionModeResponse{}))
	return nil
}

func (h *Handler) handleCancel(req *RPCRequest) *RPCErrorResponse {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(req.Params, &params)
	cancels := h.sessionCancels(params.SessionID)
	for _, fn := range cancels {
		fn()
	}
	h.transport.CancelSession(params.SessionID)
	if len(cancels) > 0 {
		Logger.Printf("session cancelled: %s", params.SessionID)
	}
	if req.IsNotification() {
		return nil
	}
	h.transport.SendResponse(NewSuccessResponse(req.ID, struct{}{}))
	return nil
}

func (h *Handler) translateMessage(msg core.Message) *SessionUpdate {
	switch msg.Role {
	case core.RoleUser:
		if msg.Hidden {
			return nil
		}
		cb := TextBlock(msg.Text)
		return &SessionUpdate{SessionUpdate: "user_message_chunk", Content: &cb}
	case core.RoleAssistant:
		if msg.Text != "" {
			cb := TextBlock(msg.Text)
			return &SessionUpdate{SessionUpdate: "agent_message_chunk", Content: &cb}
		}
	}
	return nil
}

// sessionCancels returns the cancel funcs for every in-flight prompt of a
// session (active and queued). session/cancel interrupts them all.
func (h *Handler) sessionCancels(sessionID string) []context.CancelFunc {
	h.mu.Lock()
	defer h.mu.Unlock()
	sctx, ok := h.sessions[sessionID]
	if !ok {
		return nil
	}
	cancels := make([]context.CancelFunc, 0, len(sctx.runs))
	for run := range sctx.runs {
		cancels = append(cancels, run.cancel)
	}
	return cancels
}

func newSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "acp-" + hex.EncodeToString(b)
}
