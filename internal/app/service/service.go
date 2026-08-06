package service

import (
	"context"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/app"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/plugins"
	"github.com/usewhale/whale/internal/runtime/protocol"
	"github.com/usewhale/whale/internal/skills"
)

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
	Kind               IntentKind
	Input              string
	ClientInputID      string
	HiddenInput        bool
	Attachments        []AttachmentInput
	ToolCallID         string
	UserInput          *core.UserInputResponse
	SessionInput       string
	MessageID          string
	Model              string
	Effort             string
	Thinking           string
	ApprovalMode       string
	AutoReview         bool
	ViewMode           string
	SkillName          string
	SkillEnabled       bool
	PluginID           string
	PluginEnabled      bool
	HookKey            string
	HookEnabled        bool
	HooksReviewAction  string
	ConfigUpdates      []protocol.ConfigSettingUpdate
	SkillBinding       *app.SkillBinding
	WorktreeAction     string
	WorkflowRunID      string
	WorkflowName       string
	WorkflowArgs       string
	WorkflowResume     string
	WorkflowTrust      bool
	WorkflowScript     string
	WorkflowSaveAs     string
	WorkflowScriptPath string
}

type AttachmentInput struct {
	Path        string
	DisplayName string
}

type EventKind = protocol.EventKind
type Event = protocol.Event

const (
	EventMetadataAgentTurn   = protocol.EventMetadataAgentTurn
	EventMetadataLocalSubmit = protocol.EventMetadataLocalSubmit
)

const (
	EventInfo                          = protocol.EventInfo
	EventError                         = protocol.EventError
	EventAssistantDelta                = protocol.EventAssistantDelta
	EventReasoningDelta                = protocol.EventReasoningDelta
	EventPlanDelta                     = protocol.EventPlanDelta
	EventPlanCompleted                 = protocol.EventPlanCompleted
	EventPlanUpdate                    = protocol.EventPlanUpdate
	EventProviderRetry                 = protocol.EventProviderRetry
	EventResponseReset                 = protocol.EventResponseReset
	EventToolCall                      = protocol.EventToolCall
	EventToolResult                    = protocol.EventToolResult
	EventHookStarted                   = protocol.EventHookStarted
	EventHookCompleted                 = protocol.EventHookCompleted
	EventTaskStarted                   = protocol.EventTaskStarted
	EventTaskProgress                  = protocol.EventTaskProgress
	EventTaskCompleted                 = protocol.EventTaskCompleted
	EventMCPStatus                     = protocol.EventMCPStatus
	EventMCPComplete                   = protocol.EventMCPComplete
	EventApprovalRequired              = protocol.EventApprovalRequired
	EventApprovalDecision              = protocol.EventApprovalDecision
	EventUserInputRequired             = protocol.EventUserInputRequired
	EventUserInputDone                 = protocol.EventUserInputDone
	EventSessionsListed                = protocol.EventSessionsListed
	EventLocalSubmitResult             = protocol.EventLocalSubmitResult
	EventLocalSubmitDone               = protocol.EventLocalSubmitDone
	EventDiffResult                    = protocol.EventDiffResult
	EventBtwStarted                    = protocol.EventBtwStarted
	EventBtwDelta                      = protocol.EventBtwDelta
	EventBtwDone                       = protocol.EventBtwDone
	EventBtwError                      = protocol.EventBtwError
	EventPendingInputAccepted          = protocol.EventPendingInputAccepted
	EventPendingInputRejected          = protocol.EventPendingInputRejected
	EventTurnDone                      = protocol.EventTurnDone
	EventModelSelectionRequested       = protocol.EventModelSelectionRequested
	EventPermissionsSelectionRequested = protocol.EventPermissionsSelectionRequested
	EventSkillsSelectionRequested      = protocol.EventSkillsSelectionRequested
	EventSkillsManagerUpdated          = protocol.EventSkillsManagerUpdated
	EventPluginsManagerUpdated         = protocol.EventPluginsManagerUpdated
	EventConfigManagerUpdated          = protocol.EventConfigManagerUpdated
	EventHooksManagerUpdated           = protocol.EventHooksManagerUpdated
	EventHooksStartupReviewRequested   = protocol.EventHooksStartupReviewRequested
	EventReviewRequested               = protocol.EventReviewRequested
	EventViewModeChanged               = protocol.EventViewModeChanged
	EventSkillLoaded                   = protocol.EventSkillLoaded
	EventWorktreeExitPrompt            = protocol.EventWorktreeExitPrompt
	EventExitRequested                 = protocol.EventExitRequested
	EventScreenClearRequested          = protocol.EventScreenClearRequested
	EventSessionHydrated               = protocol.EventSessionHydrated
	EventWorkflowPanel                 = protocol.EventWorkflowPanel
	EventWorkflowSnapshot              = protocol.EventWorkflowSnapshot
	EventWorkflowResult                = protocol.EventWorkflowResult
	EventWorkflowTerminal              = protocol.EventWorkflowTerminal
)

type Service struct {
	ctx              context.Context
	serviceCtxCancel context.CancelCauseFunc
	app              *app.App
	events           chan Event
	localSubmits     chan string
	cancelMu         sync.Mutex
	cancel           context.CancelFunc
	cancelCause      context.CancelCauseFunc
	active           bool
	bgWG             sync.WaitGroup

	interactionMu     sync.Mutex
	shutdownRequested bool

	approveMu     sync.Mutex
	approvals     map[string]pendingApproval
	sessionGrants map[string]map[string]bool

	inputMu sync.Mutex
	inputs  map[string]chan userInputDecision

	btwNextID         atomic.Int64
	nextEventSequence atomic.Int64

	workflowWatchMu      sync.Mutex
	workflowWatches      map[string]struct{}
	workflowReports      map[string]struct{}
	sessionStartHooksRan atomic.Bool
}

type userInputDecision struct {
	response core.UserInputResponse
	ok       bool
}

func New(ctx context.Context, cfg app.Config, start app.StartOptions) (*Service, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	a, err := app.New(ctx, cfg, start)
	if err != nil {
		cancel(context.Canceled)
		return nil, err
	}
	s := &Service{
		ctx:              ctx,
		serviceCtxCancel: cancel,
		app:              a,
		events:           make(chan Event, 512),
		localSubmits:     make(chan string, 64),
		approvals:        map[string]pendingApproval{},
		sessionGrants:    map[string]map[string]bool{},
		inputs:           map[string]chan userInputDecision{},
		workflowWatches:  map[string]struct{}{},
		workflowReports:  map[string]struct{}{},
	}
	a.SetApprovalFunc(s.awaitApproval)
	a.SetUserInputFunc(s.awaitUserInput)
	s.goTracked(s.runLocalSubmitWorker)
	if start.ResumeMenu {
		if !s.emitSessionChoices() {
			s.emitSessionHydrated()
		}
	} else {
		for _, line := range a.StartupLines() {
			s.emit(Event{Kind: EventInfo, Text: line})
		}
		if a.HooksNeedReview() {
			s.emit(Event{Kind: EventHooksStartupReviewRequested, Hooks: protocolHooks(a.HookEntries())})
		} else {
			s.runSessionStartHooksIfNeeded()
		}
		s.emitSessionHydrated()
	}
	s.startMCPStartup()
	return s, nil
}

func (s *Service) runSessionStartHooksIfNeeded() {
	if s.sessionStartHooksRan.Swap(true) {
		return
	}
	if out := s.app.RunSessionStartHook(s.hookObserver()); out != "" {
		s.emit(Event{Kind: EventInfo, Text: out})
	}
}

func (s *Service) resolveHooksStartupReview(action string) {
	switch action {
	case "trust_all":
		if _, err := s.app.TrustHooks(nil); err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emitHooksManagerUpdated()
	case "review":
		s.emitHooksManagerUpdated()
		return
	}
	s.runSessionStartHooksIfNeeded()
}

// goTracked runs fn in a goroutine and tracks it on bgWG so Close can wait
// for in-flight work (turns, side questions, local submits) to finish before
// the service tears down. Without this, background goroutines can outlive
// Close and race with cleanup of caller-owned state (e.g. test temp dirs).
func (s *Service) goTracked(fn func()) {
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		fn()
	}()
}

func (s *Service) Events() <-chan Event { return s.events }
func (s *Service) SessionID() string    { return s.app.SessionID() }
func (s *Service) WorkspaceRoot() string {
	return s.app.WorkspaceRoot()
}
func (s *Service) PrepareOpenCommand(line string) (string, *exec.Cmd, error) {
	openCmd, err := s.app.PrepareOpenCommand(line)
	if err != nil {
		return "", nil, err
	}
	return openCmd.Path, openCmd.Cmd, nil
}
func (s *Service) Model() string           { return s.app.Model() }
func (s *Service) ReasoningEffort() string { return s.app.ReasoningEffort() }
func (s *Service) ThinkingEnabled() bool   { return s.app.ThinkingEnabled() }
func (s *Service) ViewMode() string        { return s.app.ViewMode() }
func (s *Service) ShowReasoning() bool {
	if s == nil || s.app == nil {
		return false
	}
	return s.app.ShowReasoning()
}
func (s *Service) SetViewMode(mode string) error {
	if s == nil || s.app == nil {
		return nil
	}
	return s.app.SetViewMode(mode)
}
func (s *Service) SkillSuggestions() []skills.SkillView {
	if s == nil || s.app == nil {
		return nil
	}
	return s.app.SkillSuggestions()
}

func (s *Service) RunningBackgroundShells() []protocol.BackgroundShell {
	if s == nil || s.app == nil {
		return nil
	}
	tasks := s.app.RunningBackgroundShellTasks()
	if len(tasks) == 0 {
		return nil
	}
	out := make([]protocol.BackgroundShell, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, protocol.BackgroundShell{Command: task.Command, Status: task.Status})
	}
	return out
}

func (s *Service) SkillsForManager() []skills.SkillView {
	if s == nil || s.app == nil {
		return nil
	}
	return s.app.SkillReport().All()
}

func (s *Service) PluginsForManager() []plugins.PluginStatus {
	if s == nil || s.app == nil {
		return nil
	}
	return s.app.PluginStatuses()
}

func (s *Service) Close() error {
	if s == nil || s.app == nil {
		return nil
	}
	if s.serviceCtxCancel != nil {
		s.serviceCtxCancel(agent.ErrServiceShutdown)
	}
	s.bgWG.Wait()
	return s.app.Close()
}
