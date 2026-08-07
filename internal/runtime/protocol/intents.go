package protocol

type IntentKind string

const (
	IntentSubmit                    IntentKind = "submit"
	IntentSubmitLocal               IntentKind = "submit_local"
	IntentAllowTool                 IntentKind = "allow_tool"
	IntentAllowToolForSession       IntentKind = "allow_tool_for_session"
	IntentDenyTool                  IntentKind = "deny_tool"
	IntentCancelToolApproval        IntentKind = "cancel_tool_approval"
	IntentSubmitUserInput           IntentKind = "submit_user_input"
	IntentCancelUserInput           IntentKind = "cancel_user_input"
	IntentSelectSession             IntentKind = "select_session"
	IntentRequestSessions           IntentKind = "request_sessions"
	IntentRequestExit               IntentKind = "request_exit"
	IntentShutdown                  IntentKind = "shutdown"
	IntentSetModelAndEffort         IntentKind = "set_model_and_effort"
	IntentSetApprovalMode           IntentKind = "set_approval_mode"
	IntentEnableAutoAccept          IntentKind = "enable_auto_accept"
	IntentSetAutoReview             IntentKind = "set_auto_review"
	IntentSetViewMode               IntentKind = "set_view_mode"
	IntentToggleMode                IntentKind = "toggle_mode"
	IntentImplementPlan             IntentKind = "implement_plan"
	IntentDeclinePlan               IntentKind = "decline_plan"
	IntentRequestSkillsManage       IntentKind = "request_skills_manage"
	IntentSetSkillEnabled           IntentKind = "set_skill_enabled"
	IntentSetPluginEnabled          IntentKind = "set_plugin_enabled"
	IntentRequestHooksManage        IntentKind = "request_hooks_manage"
	IntentRequestConfigManage       IntentKind = "request_config_manage"
	IntentApplyConfigSettings       IntentKind = "apply_config_settings"
	IntentSetHookEnabled            IntentKind = "set_hook_enabled"
	IntentTrustHook                 IntentKind = "trust_hook"
	IntentTrustHooks                IntentKind = "trust_hooks"
	IntentResolveHooksStartupReview IntentKind = "resolve_hooks_startup_review"
	IntentWorktreeExitChoice        IntentKind = "worktree_exit_choice"
	IntentRequestWorkflowPanel      IntentKind = "request_workflow_panel"
	IntentCancelWorkflowRun         IntentKind = "cancel_workflow_run"
	IntentStartWorkflow             IntentKind = "start_workflow"
)

type Intent struct {
	Kind               IntentKind            `json:"kind"`
	InterruptSource    string                `json:"interrupt_source,omitempty"`
	Input              string                `json:"input,omitempty"`
	ClientInputID      string                `json:"client_input_id,omitempty"`
	HiddenInput        bool                  `json:"hidden_input,omitempty"`
	Attachments        []AttachmentInput     `json:"attachments,omitempty"`
	ToolCallID         string                `json:"tool_call_id,omitempty"`
	UserInput          *UserInputResponse    `json:"user_input,omitempty"`
	SessionInput       string                `json:"session_input,omitempty"`
	MessageID          string                `json:"message_id,omitempty"`
	Model              string                `json:"model,omitempty"`
	Effort             string                `json:"effort,omitempty"`
	Thinking           string                `json:"thinking,omitempty"`
	ApprovalMode       string                `json:"approval_mode,omitempty"`
	AutoReview         bool                  `json:"auto_review,omitempty"`
	ViewMode           string                `json:"view_mode,omitempty"`
	SkillName          string                `json:"skill_name,omitempty"`
	SkillEnabled       bool                  `json:"skill_enabled,omitempty"`
	PluginID           string                `json:"plugin_id,omitempty"`
	PluginEnabled      bool                  `json:"plugin_enabled,omitempty"`
	HookKey            string                `json:"hook_key,omitempty"`
	HookEnabled        bool                  `json:"hook_enabled,omitempty"`
	HooksReviewAction  string                `json:"hooks_review_action,omitempty"`
	ConfigUpdates      []ConfigSettingUpdate `json:"config_updates,omitempty"`
	SkillBinding       *SkillBinding         `json:"skill_binding,omitempty"`
	WorktreeAction     string                `json:"worktree_action,omitempty"`
	WorkflowRunID      string                `json:"workflow_run_id,omitempty"`
	WorkflowName       string                `json:"workflow_name,omitempty"`
	WorkflowArgs       string                `json:"workflow_args,omitempty"`
	WorkflowResume     string                `json:"workflow_resume,omitempty"`
	WorkflowTrust      bool                  `json:"workflow_trust,omitempty"`
	WorkflowScript     string                `json:"workflow_script,omitempty"`
	WorkflowSaveAs     string                `json:"workflow_save_as,omitempty"`
	WorkflowScriptPath string                `json:"workflow_script_path,omitempty"`
	// Quiet suppresses the EventTurnDone the service emits after handling an
	// intent. The TUI sets it for intents dispatched mid-turn (e.g. the
	// Alt+P permission cycle while the agent is busy) so the running turn is
	// not ended by a spurious turn-done event.
	Quiet bool `json:"quiet,omitempty"`
}

type ConfigSettingUpdate struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

type AttachmentInput struct {
	Path        string `json:"path"`
	DisplayName string `json:"display_name,omitempty"`
}

type SkillBinding struct {
	Name          string `json:"name,omitempty"`
	SkillFilePath string `json:"skill_file_path,omitempty"`
}

type UserInputAnswer struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	IsOther bool   `json:"is_other,omitempty"`
}

type UserInputResponse struct {
	Answers []UserInputAnswer `json:"answers"`
}

type ClientMessageType string

const (
	ClientMessageIntent ClientMessageType = "intent"
)

type ClientMessage struct {
	Type   ClientMessageType `json:"type"`
	Intent *Intent           `json:"intent,omitempty"`
}

type ServerMessageType string

const (
	ServerMessageReady  ServerMessageType = "ready"
	ServerMessageEvent  ServerMessageType = "event"
	ServerMessageError  ServerMessageType = "error"
	ServerMessageClosed ServerMessageType = "closed"
)

type ServerMessage struct {
	Type          ServerMessageType `json:"type"`
	Event         *Event            `json:"event,omitempty"`
	Error         string            `json:"error,omitempty"`
	SessionID     string            `json:"session_id,omitempty"`
	WorkspaceRoot string            `json:"workspace_root,omitempty"`
}
