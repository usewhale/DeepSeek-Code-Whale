// Package acp implements the Agent Client Protocol (ACP) v1 for Whale.
// ACP is a JSON-RPC 2.0 protocol over stdio that standardizes communication
// between code editors (clients) and AI coding agents.
//
// Protocol spec: https://agentclientprotocol.com
// Schema reference: https://github.com/agentclientprotocol/agent-client-protocol
package acp

import "encoding/json"

// ProtocolVersion is the ACP protocol version we speak.
const ProtocolVersion = 1

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 envelope types
// ---------------------------------------------------------------------------

// RequestID is a JSON-RPC request id: string, number, or null.
type RequestID struct {
	Value any
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	if id.Value == nil {
		return json.Marshal(nil)
	}
	return json.Marshal(id.Value)
}

func (id *RequestID) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	id.Value = raw
	return nil
}

// RPCRequest is the JSON-RPC 2.0 request envelope.
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *RequestID      `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCResponse is the JSON-RPC 2.0 successful response envelope.
type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *RequestID  `json:"id"`
	Result  interface{} `json:"result"`
}

// RPCError is the JSON-RPC 2.0 error response envelope.
type RPCErrorResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      *RequestID `json:"id"`
	Error   *RPCErr    `json:"error"`
}

// RPCErr is the JSON-RPC error object.
type RPCErr struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// RPCNotification is the JSON-RPC 2.0 notification envelope.
type RPCNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether a parsed request is a notification (no id).
func (r *RPCRequest) IsNotification() bool {
	return r.ID == nil
}

// NewSuccessResponse creates a successful JSON-RPC response.
func NewSuccessResponse(id *RequestID, result interface{}) *RPCResponse {
	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// NewErrorResponse creates a JSON-RPC error response.
func NewErrorResponse(id *RequestID, code int, message string) *RPCErrorResponse {
	return &RPCErrorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCErr{
			Code:    code,
			Message: message,
		},
	}
}

// Standard JSON-RPC error codes.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	ErrCodeCancelled      = -32800
)

// ---------------------------------------------------------------------------
// Agent method names (methods the Agent handles, sent by the Client)
// ---------------------------------------------------------------------------

const (
	MethodInitialize          = "initialize"
	MethodAuthenticate        = "authenticate"
	MethodSessionNew          = "session/new"
	MethodSessionLoad         = "session/load"
	MethodSessionSetMode      = "session/set_mode"
	MethodSessionSetConfigOpt = "session/set_config_option"
	MethodSessionPrompt       = "session/prompt"
	MethodSessionCancel       = "session/cancel"
)

// ---------------------------------------------------------------------------
// Client method names (methods the Client handles, sent by the Agent)
// ---------------------------------------------------------------------------

const (
	MethodSessionUpdate      = "session/update"
	MethodSessionRequestPerm = "session/request_permission"
	MethodFSReadTextFile     = "fs/read_text_file"
	MethodFSWriteTextFile    = "fs/write_text_file"
)

// ---------------------------------------------------------------------------
// Initialize
// ---------------------------------------------------------------------------

// InitializeRequest is sent by the client to establish connection and
// negotiate capabilities.
type InitializeRequest struct {
	ProtocolVersion    uint16              `json:"protocolVersion"`
	ClientCapabilities *ClientCapabilities `json:"clientCapabilities,omitempty"`
	ClientInfo         *Implementation     `json:"clientInfo,omitempty"`
	Meta               map[string]any      `json:"_meta,omitempty"`
}

// InitializeResponse is returned by the agent with negotiated version and
// advertised capabilities.
type InitializeResponse struct {
	ProtocolVersion   uint16             `json:"protocolVersion"`
	AgentCapabilities *AgentCapabilities `json:"agentCapabilities,omitempty"`
	AuthMethods       []AuthMethod       `json:"authMethods,omitempty"`
	AgentInfo         *Implementation    `json:"agentInfo,omitempty"`
	Meta              map[string]any     `json:"_meta,omitempty"`
}

// ClientCapabilities describes features the client supports.
type ClientCapabilities struct {
	FS       *FileSystemCapabilities `json:"fs,omitempty"`
	Terminal bool                    `json:"terminal,omitempty"`
	Meta     map[string]any          `json:"_meta,omitempty"`
}

// FileSystemCapabilities describes file operations the client supports.
type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// AgentCapabilities describes features the agent supports.
type AgentCapabilities struct {
	LoadSession         bool                   `json:"loadSession,omitempty"`
	PromptCapabilities  *PromptCapabilities    `json:"promptCapabilities,omitempty"`
	MCPCapabilities     *MCPCapabilities       `json:"mcpCapabilities,omitempty"`
	SessionCapabilities *SessionCapabilities   `json:"sessionCapabilities,omitempty"`
	Auth                *AgentAuthCapabilities `json:"auth,omitempty"`
	Meta                map[string]any         `json:"_meta,omitempty"`
}

// PromptCapabilities indicates content types the agent can process.
type PromptCapabilities struct {
	Image           bool           `json:"image,omitempty"`
	Audio           bool           `json:"audio,omitempty"`
	EmbeddedContext bool           `json:"embeddedContext,omitempty"`
	Meta            map[string]any `json:"_meta,omitempty"`
}

// MCPCapabilities indicates MCP transports the agent supports.
type MCPCapabilities struct {
	HTTP bool           `json:"http,omitempty"`
	SSE  bool           `json:"sse,omitempty"`
	Meta map[string]any `json:"_meta,omitempty"`
}

// SessionCapabilities describes session-related agent capabilities.
type SessionCapabilities struct {
	AdditionalDirectories *struct{}      `json:"additionalDirectories,omitempty"`
	Meta                  map[string]any `json:"_meta,omitempty"`
}

// AgentAuthCapabilities describes authentication capabilities.
type AgentAuthCapabilities struct {
	Logout bool           `json:"logout,omitempty"`
	Meta   map[string]any `json:"_meta,omitempty"`
}

// Implementation carries client/agent name and version information.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// AuthMethod identifies an authentication method.
type AuthMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// Authenticate
// ---------------------------------------------------------------------------

// AuthenticateRequest initiates authentication.
type AuthenticateRequest struct {
	MethodID string         `json:"methodId"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

// AuthenticateResponse confirms authentication.
type AuthenticateResponse struct{}

// ---------------------------------------------------------------------------
// Session / new
// ---------------------------------------------------------------------------

// NewSessionRequest creates a new conversation session.
type NewSessionRequest struct {
	Cwd                   string         `json:"cwd,omitempty"`
	MCPServers            []MCPServer    `json:"mcpServers,omitempty"`
	AdditionalDirectories []string       `json:"additionalDirectories,omitempty"`
	Meta                  map[string]any `json:"_meta,omitempty"`
}

// NewSessionResponse is returned after a session is created.
type NewSessionResponse struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
	Meta      map[string]any    `json:"_meta,omitempty"`
}

// MCPServer describes an MCP server the client wants the agent to connect to.
type MCPServer struct {
	Type    string        `json:"type"` // "stdio", "http", "sse"
	Name    string        `json:"name"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
	URL     string        `json:"url,omitempty"`
	Headers []HTTPHeader  `json:"headers,omitempty"`
}

// EnvVariable is a name/value pair for environment variables in the ACP
// mcpServers protocol shape (array of {name, value} objects).
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HTTPHeader is a name/value pair for HTTP MCP transports.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ---------------------------------------------------------------------------
// Session modes
// ---------------------------------------------------------------------------

// SessionModeState describes current and available modes for a session.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SessionMode describes an operating mode of the agent.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ---------------------------------------------------------------------------
// Session / set_mode
// ---------------------------------------------------------------------------

// SetSessionModeRequest changes the session mode.
type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SetSessionModeResponse confirms mode change.
type SetSessionModeResponse struct{}

// ---------------------------------------------------------------------------
// Session / load
// ---------------------------------------------------------------------------

// LoadSessionRequest loads an existing session. Per the ACP spec the client
// MUST supply cwd and mcpServers (same as session/new); cwd is the authoritative
// working directory for the resumed session.
type LoadSessionRequest struct {
	SessionID             string         `json:"sessionId"`
	Cwd                   string         `json:"cwd,omitempty"`
	MCPServers            []MCPServer    `json:"mcpServers,omitempty"`
	AdditionalDirectories []string       `json:"additionalDirectories,omitempty"`
	Meta                  map[string]any `json:"_meta,omitempty"`
}

// LoadSessionResponse returns the messages from a loaded session.
type LoadSessionResponse struct {
	Messages []ContentBlock    `json:"messages,omitempty"`
	Modes    *SessionModeState `json:"modes,omitempty"`
	Meta     map[string]any    `json:"_meta,omitempty"`
}

// ---------------------------------------------------------------------------
// Session / prompt
// ---------------------------------------------------------------------------

// PromptRequest sends a user prompt to the agent.
type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

// PromptResponse is returned when processing completes.
type PromptResponse struct {
	StopReason StopReason     `json:"stopReason"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

// StopReason indicates why the agent stopped processing.
type StopReason string

const (
	StopReasonEndTurn     StopReason = "end_turn"
	StopReasonMaxTokens   StopReason = "max_tokens"
	StopReasonMaxTurnReqs StopReason = "max_turn_requests"
	StopReasonRefusal     StopReason = "refusal"
	StopReasonCancelled   StopReason = "cancelled"
)

// ---------------------------------------------------------------------------
// Content blocks
// ---------------------------------------------------------------------------

// ContentBlock represents displayable content — compatible with MCP's ContentBlock.
type ContentBlock struct {
	Type        string                    `json:"type"`
	Text        string                    `json:"text,omitempty"`
	Data        string                    `json:"data,omitempty"`
	MimeType    string                    `json:"mimeType,omitempty"`
	URI         string                    `json:"uri,omitempty"`
	Name        string                    `json:"name,omitempty"`
	Title       string                    `json:"title,omitempty"`
	Description string                    `json:"description,omitempty"`
	Size        *int64                    `json:"size,omitempty"`
	Resource    *EmbeddedResourceResource `json:"resource,omitempty"`
	Annotations *Annotations              `json:"annotations,omitempty"`
	Meta        map[string]any            `json:"_meta,omitempty"`
}

// EmbeddedResourceResource is an embedded resource body.
type EmbeddedResourceResource struct {
	URI      string `json:"uri"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// Annotations provides optional metadata for content blocks.
type Annotations struct {
	Audience     []Role         `json:"audience,omitempty"`
	LastModified *string        `json:"lastModified,omitempty"`
	Priority     *float64       `json:"priority,omitempty"`
	Meta         map[string]any `json:"_meta,omitempty"`
}

// Role is the content attribution role.
type Role string

const (
	RoleAssistant Role = "assistant"
	RoleUser      Role = "user"
)

// TextBlock is a convenience helper for creating text content blocks.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: text}
}

// ---------------------------------------------------------------------------
// Session update (agent → client notification)
// ---------------------------------------------------------------------------

// SessionNotification wraps an update for a session.
type SessionNotification struct {
	SessionID string         `json:"sessionId"`
	Update    SessionUpdate  `json:"update"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

// SessionUpdate is a discriminated union of possible updates.
// The SessionUpdate field acts as the discriminator.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`

	// Content is polymorphic:
	//   message chunks → *ContentBlock
	//   tool_call_update → []ToolCallContent
	Content interface{} `json:"content,omitempty"`

	MessageID *string `json:"messageId,omitempty"`

	// ToolCall fields
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       *ToolKind       `json:"kind,omitempty"`
	Status     *ToolCallStatus `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`

	// Plan fields
	Entries []PlanEntry `json:"entries,omitempty"`

	// AvailableCommandsUpdate fields
	AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`

	// CurrentModeUpdate fields
	ModeID string `json:"modeId,omitempty"`

	// ConfigOptionUpdate fields
	Category string `json:"category,omitempty"`
	Name     string `json:"name,omitempty"`
	Value    any    `json:"value,omitempty"`

	Meta map[string]any `json:"_meta,omitempty"`
}

// AgentMessageChunk creates a SessionUpdate for agent message streaming.
func AgentMessageChunk(text string) SessionUpdate {
	cb := TextBlock(text)
	return SessionUpdate{
		SessionUpdate: "agent_message_chunk",
		Content:       &cb,
	}
}

// AgentThoughtChunk creates a SessionUpdate for agent reasoning streaming.
func AgentThoughtChunk(text string) SessionUpdate {
	cb := TextBlock(text)
	return SessionUpdate{
		SessionUpdate: "agent_thought_chunk",
		Content:       &cb,
	}
}

// ToolCallUpdate creates a SessionUpdate for a new tool call.
func ToolCallNotification(toolCallID, title string, kind ToolKind, status ToolCallStatus) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: "tool_call",
		ToolCallID:    toolCallID,
		Title:         title,
		Kind:          &kind,
		Status:        &status,
	}
}

// ToolCallStatusUpdate creates a SessionUpdate for updating a tool call.
func ToolCallStatusUpdate(toolCallID string, status ToolCallStatus, content []ToolCallContent) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    toolCallID,
		Status:        &status,
		Content:       content,
	}
}

// PlanUpdate creates a SessionUpdate for an execution plan.
func PlanNotification(entries []PlanEntry) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: "plan",
		Entries:       entries,
	}
}

// ToolKind categorizes tool calls for UI treatment.
type ToolKind string

const (
	ToolKindRead       ToolKind = "read"
	ToolKindEdit       ToolKind = "edit"
	ToolKindDelete     ToolKind = "delete"
	ToolKindMove       ToolKind = "move"
	ToolKindSearch     ToolKind = "search"
	ToolKindExecute    ToolKind = "execute"
	ToolKindThink      ToolKind = "think"
	ToolKindFetch      ToolKind = "fetch"
	ToolKindSwitchMode ToolKind = "switch_mode"
	ToolKindOther      ToolKind = "other"
)

// ToolCallStatus is the execution state of a tool call.
type ToolCallStatus string

const (
	ToolCallStatusPending    ToolCallStatus = "pending"
	ToolCallStatusInProgress ToolCallStatus = "in_progress"
	ToolCallStatusCompleted  ToolCallStatus = "completed"
	ToolCallStatusFailed     ToolCallStatus = "failed"
)

// ToolCallContent represents content produced by a tool. It is a discriminated
// union keyed by Type: the "content" variant nests a ContentBlock under
// Content, while the "diff" and "terminal" variants use the flat fields below.
type ToolCallContent struct {
	Type       string         `json:"type"`
	Content    *ContentBlock  `json:"content,omitempty"`
	Path       string         `json:"path,omitempty"`
	OldText    string         `json:"oldText,omitempty"`
	NewText    string         `json:"newText,omitempty"`
	TerminalID string         `json:"terminalId,omitempty"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

// ToolContent creates a generic text tool content. Per the ACP schema the
// "content" variant wraps a full ContentBlock rather than inlining the text.
func ToolContent(text string) ToolCallContent {
	cb := TextBlock(text)
	return ToolCallContent{Type: "content", Content: &cb}
}

// PlanEntry is a single step in an agent execution plan.
type PlanEntry struct {
	Content  string            `json:"content"`
	Priority PlanEntryPriority `json:"priority"`
	Status   PlanEntryStatus   `json:"status"`
}

// PlanEntryPriority is the importance of a plan entry.
type PlanEntryPriority string

const (
	PlanPriorityHigh   PlanEntryPriority = "high"
	PlanPriorityMedium PlanEntryPriority = "medium"
	PlanPriorityLow    PlanEntryPriority = "low"
)

// PlanEntryStatus is the execution status of a plan entry.
type PlanEntryStatus string

const (
	PlanStatusPending    PlanEntryStatus = "pending"
	PlanStatusInProgress PlanEntryStatus = "in_progress"
	PlanStatusCompleted  PlanEntryStatus = "completed"
)

// AvailableCommand advertises a command the agent can execute.
type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
	Meta        map[string]any         `json:"_meta,omitempty"`
}

// AvailableCommandInput describes input for a command.
type AvailableCommandInput struct {
	Type        string `json:"type"`
	Placeholder string `json:"placeholder,omitempty"`
}

// ---------------------------------------------------------------------------
// Request permission (agent → client)
// ---------------------------------------------------------------------------

// RequestPermissionRequest asks the client to authorize a tool call.
type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  PermissionToolCall `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
	Meta      map[string]any     `json:"_meta,omitempty"`
}

// PermissionToolCall is the tool call being authorized.
type PermissionToolCall struct {
	ToolCallID string            `json:"toolCallId"`
	Title      string            `json:"title"`
	Kind       ToolKind          `json:"kind"`
	Status     ToolCallStatus    `json:"status"`
	Content    []ToolCallContent `json:"content,omitempty"`
	Meta       map[string]any    `json:"_meta,omitempty"`
}

// PermissionOption is a choice presented to the user.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // "allow_once", "allow_always", "reject_once"
}

// PermissionOutcome mirrors the ACP schema's nested outcome wrapper.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`            // "selected" or "cancelled"
	OptionID string `json:"optionId,omitempty"` // only present when outcome=="selected"
}

// RequestPermissionResponse carries the user's choice.
type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}
