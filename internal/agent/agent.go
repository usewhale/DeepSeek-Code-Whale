package agent

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/defaults"
	"github.com/usewhale/whale/internal/llm"
	llmretry "github.com/usewhale/whale/internal/llm/retry"
	"github.com/usewhale/whale/internal/memory"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/skills"
	"github.com/usewhale/whale/internal/store"
	"github.com/usewhale/whale/internal/telemetry"
)

var ErrSessionBusy = errors.New("session is currently processing another request")
var ErrBudgetExceeded = errors.New("session budget exhausted")

type AgentEventType string

const (
	AgentEventTypeAssistantDelta         AgentEventType = "assistant_delta"
	AgentEventTypeReasoningDelta         AgentEventType = "reasoning_delta"
	AgentEventTypeToolArgsDelta          AgentEventType = "tool_args_delta"
	AgentEventTypeToolArgsRepaired       AgentEventType = "tool_args_repaired"
	AgentEventTypeToolCallBlocked        AgentEventType = "tool_call_blocked"
	AgentEventTypeToolModeBlocked        AgentEventType = "tool_mode_blocked"
	AgentEventTypeToolApprovalRequired   AgentEventType = "tool_approval_required"
	AgentEventTypeToolApprovalGranted    AgentEventType = "tool_approval_granted"
	AgentEventTypeToolCallScavenged      AgentEventType = "tool_call_scavenged"
	AgentEventTypeToolPolicyDecision     AgentEventType = "tool_policy_decision"
	AgentEventTypeToolCall               AgentEventType = "tool_call"
	AgentEventTypeToolResult             AgentEventType = "tool_result"
	AgentEventTypeUserInputRequired      AgentEventType = "user_input_required"
	AgentEventTypeUserInputSubmitted     AgentEventType = "user_input_submitted"
	AgentEventTypeUserInputCancelled     AgentEventType = "user_input_cancelled"
	AgentEventTypePlanDelta              AgentEventType = "plan_delta"
	AgentEventTypePlanCompleted          AgentEventType = "plan_completed"
	AgentEventTypePlanUpdate             AgentEventType = "plan_update"
	AgentEventTypePlanStepBlocked        AgentEventType = "plan_step_blocked"
	AgentEventTypeProviderRetryScheduled AgentEventType = "provider_retry_scheduled"
	AgentEventTypeResponseReset          AgentEventType = "response_reset"
	AgentEventTypeLeakedToolCallScrubbed AgentEventType = "leaked_tool_call_scrubbed"
	AgentEventTypePrematureEndRecovered  AgentEventType = "premature_end_recovered"
	AgentEventTypeToolRecoveryScheduled  AgentEventType = "tool_recovery_scheduled"
	AgentEventTypeToolRecoveryAttempt    AgentEventType = "tool_recovery_attempt"
	AgentEventTypeToolRecoveryExhausted  AgentEventType = "tool_recovery_exhausted"
	AgentEventTypeReplanRequiredSet      AgentEventType = "replan_required_set"
	AgentEventTypeContextCompacted       AgentEventType = "context_compacted"
	AgentEventTypePrefixDrift            AgentEventType = "prefix_drift"
	AgentEventTypePrefixCacheMetrics     AgentEventType = "prefix_cache_metrics"
	AgentEventTypeUsage                  AgentEventType = "usage"
	AgentEventTypeBudgetWarning          AgentEventType = "budget_warning"
	AgentEventTypeTurnCancelled          AgentEventType = "turn_cancelled"
	AgentEventTypeForcedSummaryStarted   AgentEventType = "forced_summary_started"
	AgentEventTypeForcedSummaryDone      AgentEventType = "forced_summary_done"
	AgentEventTypeForcedSummaryFailed    AgentEventType = "forced_summary_failed"
	AgentEventTypeHookStarted            AgentEventType = "hook_started"
	AgentEventTypeHookBlocked            AgentEventType = "hook_blocked"
	AgentEventTypeHookWarned             AgentEventType = "hook_warned"
	AgentEventTypeHookFailed             AgentEventType = "hook_failed"
	AgentEventTypeHookCompleted          AgentEventType = "hook_completed"
	AgentEventTypeParallelReasonStarted  AgentEventType = "parallel_reason_started"
	AgentEventTypeParallelReasonDone     AgentEventType = "parallel_reason_completed"
	AgentEventTypeSubagentStarted        AgentEventType = "subagent_started"
	AgentEventTypeTaskProgress           AgentEventType = "task_progress"
	AgentEventTypeSubagentDone           AgentEventType = "subagent_completed"
	AgentEventTypeClassifierReview       AgentEventType = "classifier_review"
	AgentEventTypeDone                   AgentEventType = "done"
	AgentEventTypeError                  AgentEventType = "error"
)

type ToolArgsProgress struct {
	ToolCallIndex int
	ToolName      string
	ArgsChars     int
	ReadyCount    int
}

type ToolArgsRepair struct {
	ToolCallIndex int
	ToolName      string
}

type ToolCallBlocked struct {
	ToolCallID string
	ToolName   string
	ReasonCode string
}

type ToolApprovalRequired struct {
	ToolCallID string
	ToolName   string
	Reason     string
	Code       string
	Key        string
	Keys       []string
	Summary    string
	Scope      string
	Metadata   map[string]any
}

type ToolApprovalGranted struct {
	SessionID  string
	ToolCallID string
	ToolName   string
	Key        string
	Keys       []string
}

type ToolCallScavenged struct {
	Count int
}

type ToolPolicyDecision struct {
	ToolCallID    string
	ToolName      string
	Allow         bool
	NeedsApproval bool
	Reason        string
	Code          string
	Phase         string
	MatchedRule   string
}

type AgentEvent struct {
	Type           AgentEventType
	Content        string
	ReasoningDelta string
	ToolArgs       *ToolArgsProgress
	ToolArgsRepair *ToolArgsRepair
	ToolBlocked    *ToolCallBlocked
	Approval       *ToolApprovalRequired
	ApprovalGrant  *ToolApprovalGranted
	Scavenged      *ToolCallScavenged
	Policy         *ToolPolicyDecision
	Recovery       *ToolRecoveryInfo
	ProviderRetry  *llmretry.Info
	Compact        *CompactInfo
	PrefixDrift    *PrefixDriftInfo
	CacheMetrics   *PrefixCacheMetricsInfo
	Usage          *UsageInfo
	Budget         *BudgetWarningInfo
	Hook           *HookEventInfo
	Task           *TaskActivityInfo
	PlanUpdate     *PlanUpdateInfo
	Classifier     *ClassifierReviewEvent
	ToolCall       *core.ToolCall
	UserInputReq   *core.UserInputRequest
	UserInputResp  *core.UserInputResponse
	Result         *core.ToolResult
	Message        *core.Message
	Err            error
}

type PlanUpdateStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type PlanUpdateInfo struct {
	Explanation string           `json:"explanation,omitempty"`
	Plan        []PlanUpdateStep `json:"plan"`
}

type TaskActivityInfo struct {
	ToolCallID       string
	ToolName         string
	Role             string
	Model            string
	Count            int
	Summary          string
	Status           string
	DurationMS       int64
	Metadata         map[string]any
	ProgressMessages []core.SubagentStep
}

type BudgetWarningInfo struct {
	CapUSD      float64
	SpentUSD    float64
	Percent     int
	TurnCostUSD float64
}

type UserInputRequest struct {
	SessionID string
	ToolCall  core.ToolCall
	Questions []core.UserInputQuestion
}

type UserInputFunc func(req UserInputRequest) (core.UserInputResponse, bool)

type HookEventInfo struct {
	ID         string
	Name       string
	Event      HookEvent
	Source     string
	Command    string
	Decision   HookDecision
	ExitCode   int
	Message    string
	DurationMS int64
	Truncated  bool
}

type CompactInfo struct {
	Compacted      bool
	Auto           bool
	MessagesBefore int
	MessagesAfter  int
	BeforeEstimate int
	AfterEstimate  int
}

type PrefixDriftInfo struct {
	Expected string
	Actual   string
}

type PrefixCacheMetricsInfo struct {
	Model             string
	PrefixFingerprint string
	CacheShape        *telemetry.CacheShape
	PromptTokens      int
	CachedTokens      int
	CacheHitRatio     float64
}

type UsageInfo struct {
	Model string
	Usage llm.Usage
}

type ToolRecoveryInfo struct {
	ToolCallID     string
	ToolName       string
	FailureClass   string
	Action         string
	Attempt        int
	MaxAttempts    int
	Reason         string
	Executed       bool
	ReplanInjected bool
}

type Agent struct {
	provider               llm.Provider
	store                  store.MessageStore
	tools                  *core.ToolRegistry
	toolRefresh            func(context.Context) error
	storm                  stormConfig
	repairer               *toolCallRepair
	policy                 policy.ToolPolicy
	approve                policy.ApprovalFunc
	userInput              UserInputFunc
	approvalCache          *policy.SessionApprovalCache
	mode                   session.Mode
	autoCompact            bool
	compactThresh          float64
	contextWindow          int
	recovery               RecoveryPolicy
	hooks                  *HookRunner
	classifier             *Classifier
	projectMemoryEnabled   bool
	projectMemoryMaxChars  int
	projectMemoryFileOrder []string
	workspaceRoot          string
	worktreeRoot           string
	originalWorkspace      string
	disabledSkills         []string
	extraSkills            []*skills.Skill
	extraSystemBlocks      []string
	dynamicSystemBlocks    []func(RunOptions) string
	sessionRuntime         *memory.SessionRuntime
	sessionsDir            string
	budgetWarningUSD       float64
	usageLogPath           string
	toolResultArchiveDir   string
	lifecycleCtx           context.Context
	lifecycleCancel        context.CancelFunc
	budgetWarned80         sync.Map
	maxToolIters           int
	maxToolCalls           int
	maxTurns               int
	maxParallelSubagents   int
	active                 sync.Map
}

type activeTurnState struct {
	mu      sync.Mutex
	pending []core.Message
}

func (s *activeTurnState) appendPending(messages []core.Message) {
	if s == nil || len(messages) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, messages...)
}

func (s *activeTurnState) drainPending() []core.Message {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := append([]core.Message(nil), s.pending...)
	s.pending = nil
	return out
}

func (s *activeTurnState) hasPending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) > 0
}

const defaultMaxParallelSubagentCap = 128

var runtimeNumCPU = runtime.NumCPU

func defaultMaxParallelSubagents() int {
	n := runtimeNumCPU() * 2
	if n < 2 {
		return 2
	}
	if n > defaultMaxParallelSubagentCap {
		return defaultMaxParallelSubagentCap
	}
	return n
}

func NewAgent(provider llm.Provider, store store.MessageStore, tools []core.Tool) *Agent {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	return &Agent{
		provider:               provider,
		store:                  store,
		tools:                  core.NewToolRegistry(tools),
		storm:                  defaultStormConfig(),
		repairer:               newToolCallRepair(defaultStormConfig()),
		policy:                 policy.DefaultToolPolicy{},
		approvalCache:          policy.NewSessionApprovalCache(),
		mode:                   session.ModeAgent,
		compactThresh:          defaults.DefaultAgentCompactThreshold,
		contextWindow:          defaults.DefaultContextWindow,
		recovery:               DefaultRecoveryPolicy(),
		hooks:                  NewHookRunner(nil, ""),
		classifier:             NewClassifier(DefaultClassifierConfig()),
		projectMemoryEnabled:   true,
		projectMemoryMaxChars:  defaults.DefaultMemoryMaxChars,
		projectMemoryFileOrder: defaults.DefaultMemoryFileOrder(),
		sessionRuntime:         memory.NewSessionRuntime(""),
		usageLogPath:           telemetry.DefaultUsageLogDir(),
		toolResultArchiveDir:   defaultToolResultArchiveDir(telemetry.DefaultUsageLogDir()),
		lifecycleCtx:           lifecycleCtx,
		lifecycleCancel:        lifecycleCancel,
		maxToolIters:           0, // 0 = unlimited: the interactive main agent is bounded by user cancellation, compaction, and the storm loop-guard (see maxConsecutiveStormRounds) — not by a round count. Subagents override via WithMaxToolIters.
		maxParallelSubagents:   defaultMaxParallelSubagents(),
	}
}

func NewAgentWithRegistry(provider llm.Provider, store store.MessageStore, tools *core.ToolRegistry, opts ...AgentOption) *Agent {
	if tools == nil {
		tools = core.NewToolRegistry(nil)
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	a := &Agent{
		provider:               provider,
		store:                  store,
		tools:                  tools,
		storm:                  defaultStormConfig(),
		repairer:               newToolCallRepair(defaultStormConfig()),
		policy:                 policy.DefaultToolPolicy{},
		approvalCache:          policy.NewSessionApprovalCache(),
		mode:                   session.ModeAgent,
		compactThresh:          defaults.DefaultAgentCompactThreshold,
		contextWindow:          defaults.DefaultContextWindow,
		recovery:               DefaultRecoveryPolicy(),
		hooks:                  NewHookRunner(nil, ""),
		classifier:             NewClassifier(DefaultClassifierConfig()),
		projectMemoryEnabled:   true,
		projectMemoryMaxChars:  defaults.DefaultMemoryMaxChars,
		projectMemoryFileOrder: defaults.DefaultMemoryFileOrder(),
		sessionRuntime:         memory.NewSessionRuntime(""),
		usageLogPath:           telemetry.DefaultUsageLogDir(),
		toolResultArchiveDir:   defaultToolResultArchiveDir(telemetry.DefaultUsageLogDir()),
		lifecycleCtx:           lifecycleCtx,
		lifecycleCancel:        lifecycleCancel,
		maxToolIters:           0, // 0 = unlimited: the interactive main agent is bounded by user cancellation, compaction, and the storm loop-guard (see maxConsecutiveStormRounds) — not by a round count. Subagents override via WithMaxToolIters.
		maxParallelSubagents:   defaultMaxParallelSubagents(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a
}

func (a *Agent) Close() {
	if a == nil || a.lifecycleCancel == nil {
		return
	}
	a.lifecycleCancel()
}

func (a *Agent) cleanupLoopContext() context.Context {
	if a == nil || a.lifecycleCtx == nil {
		return context.Background()
	}
	return a.lifecycleCtx
}

// isToolResultReadTool reports whether the tool name is one of the read-only
// filesystem tools that may be auto-allowed for tool-results paths.
func isToolResultReadTool(name string) bool {
	switch name {
	case "read_file", "grep", "search_files":
		return true
	default:
		return false
	}
}

// isToolResultReadPath reports whether the given tool call targets a path
// inside the current session's persisted tool-results archive directory.
func (a *Agent) isToolResultReadPath(sessionID string, call core.ToolCall) bool {
	if a == nil || strings.TrimSpace(a.toolResultArchiveDir) == "" {
		return false
	}
	dir := core.ToolResultArchiveSessionDir(a.toolResultArchiveDir, sessionID)
	return policy.ToolResultReadPath(dir, call)
}

type AgentOption func(*Agent)

func WithToolPolicy(policy policy.ToolPolicy) AgentOption {
	return func(a *Agent) {
		if policy != nil {
			a.policy = policy
		}
	}
}

func WithToolRefresh(fn func(context.Context) error) AgentOption {
	return func(a *Agent) {
		a.toolRefresh = fn
	}
}

func WithApprovalFunc(fn policy.ApprovalFunc) AgentOption {
	return func(a *Agent) {
		a.approve = fn
	}
}

func WithUserInputFunc(fn UserInputFunc) AgentOption {
	return func(a *Agent) {
		a.userInput = fn
	}
}

func WithSessionMode(mode session.Mode) AgentOption {
	return func(a *Agent) {
		a.mode = mode
	}
}

func WithSessionsDir(sessionsDir string) AgentOption {
	return func(a *Agent) {
		a.sessionsDir = strings.TrimSpace(sessionsDir)
		a.sessionRuntime = memory.NewSessionRuntime(sessionsDir)
	}
}

func WithAutoCompact(enabled bool, threshold float64, contextWindow int) AgentOption {
	return func(a *Agent) {
		a.autoCompact = enabled
		if threshold > 0 && threshold < 1 {
			a.compactThresh = threshold
		}
		if contextWindow > 0 {
			a.contextWindow = contextWindow
		}
	}
}

func WithBudgetWarningUSD(capUSD float64) AgentOption {
	return func(a *Agent) {
		if capUSD > 0 {
			a.budgetWarningUSD = capUSD
		} else {
			a.budgetWarningUSD = 0
		}
	}
}

func WithUsageLogPath(path string) AgentOption {
	return func(a *Agent) {
		a.usageLogPath = strings.TrimSpace(path)
		a.toolResultArchiveDir = defaultToolResultArchiveDir(a.usageLogPath)
	}
}

func defaultToolResultArchiveDir(usageLogPath string) string {
	usageLogPath = strings.TrimSpace(usageLogPath)
	if usageLogPath == "" {
		usageLogPath = telemetry.DefaultUsageLogDir()
	}
	return filepath.Join(filepath.Dir(filepath.Clean(usageLogPath)), "tool-results")
}

func WithRecoveryPolicy(r RecoveryPolicy) AgentOption {
	return func(a *Agent) {
		a.recovery = r
	}
}

func WithHooks(hooks []ResolvedHook, workspaceRoot string) AgentOption {
	return func(a *Agent) {
		a.hooks = NewHookRunner(hooks, workspaceRoot)
	}
}

func WithHookRunner(runner *HookRunner) AgentOption {
	return func(a *Agent) {
		if runner != nil {
			a.hooks = runner
		}
	}
}

func WithHookHandlers(handlers ...HookHandler) AgentOption {
	return func(a *Agent) {
		if a.hooks == nil {
			a.hooks = NewHookRunner(nil, "")
		}
		a.hooks.AddHandlers(handlers...)
	}
}

func WithHookExecutors(promptExecutor, agentExecutor HookExecutor) AgentOption {
	return func(a *Agent) {
		if a.hooks == nil {
			a.hooks = NewHookRunner(nil, "")
		}
		a.hooks.SetExecutors(promptExecutor, agentExecutor)
	}
}

func WithProjectMemory(enabled bool, maxChars int, fileOrder []string, workspaceRoot string) AgentOption {
	return func(a *Agent) {
		a.projectMemoryEnabled = enabled
		if maxChars > 0 {
			a.projectMemoryMaxChars = maxChars
		}
		if len(fileOrder) > 0 {
			a.projectMemoryFileOrder = fileOrder
		}
		a.workspaceRoot = strings.TrimSpace(workspaceRoot)
	}
}

func WithWorktreeContext(worktreeRoot, originalWorkspace string) AgentOption {
	return func(a *Agent) {
		a.worktreeRoot = strings.TrimSpace(worktreeRoot)
		a.originalWorkspace = strings.TrimSpace(originalWorkspace)
	}
}

func WithDisabledSkills(names []string) AgentOption {
	return func(a *Agent) {
		a.disabledSkills = append([]string(nil), names...)
	}
}

func WithExtraSkills(extra []*skills.Skill) AgentOption {
	return func(a *Agent) {
		a.extraSkills = append([]*skills.Skill(nil), extra...)
	}
}

func WithExtraSystemBlocks(blocks ...string) AgentOption {
	return func(a *Agent) {
		a.extraSystemBlocks = append([]string(nil), blocks...)
	}
}

func WithDynamicSystemBlocks(blocks ...func() string) AgentOption {
	return func(a *Agent) {
		a.dynamicSystemBlocks = make([]func(RunOptions) string, 0, len(blocks))
		for _, block := range blocks {
			render := block
			a.dynamicSystemBlocks = append(a.dynamicSystemBlocks, func(RunOptions) string {
				if render == nil {
					return ""
				}
				return render()
			})
		}
	}
}

func WithDynamicSystemBlocksForTurn(blocks ...func(RunOptions) string) AgentOption {
	return func(a *Agent) {
		a.dynamicSystemBlocks = append([]func(RunOptions) string(nil), blocks...)
	}
}

func WithMaxToolIters(maxIters int) AgentOption {
	return func(a *Agent) {
		if maxIters > 0 {
			a.maxToolIters = maxIters
		}
	}
}

func WithMaxToolCalls(maxCalls int) AgentOption {
	return func(a *Agent) {
		if maxCalls > 0 {
			a.maxToolCalls = maxCalls
		}
	}
}

func WithMaxTurns(maxTurns int) AgentOption {
	return func(a *Agent) {
		if maxTurns > 0 {
			a.maxTurns = maxTurns
		}
	}
}

func WithMaxParallelSubagents(maxParallel int) AgentOption {
	return func(a *Agent) {
		if maxParallel > 0 {
			a.maxParallelSubagents = maxParallel
		}
	}
}

// WithClassifierConfig sets the auto-review classifier configuration.
func WithClassifierConfig(cfg ClassifierConfig) AgentOption {
	return func(a *Agent) {
		a.classifier = NewClassifier(cfg)
	}
}

// Classifier returns the auto-review classifier for runtime toggle.
func (a *Agent) Classifier() *Classifier {
	if a == nil {
		return nil
	}
	return a.classifier
}

func (a *Agent) RunSession(ctx context.Context, sessionID, input string) (core.Message, error) {
	events, err := a.RunStream(ctx, sessionID, input)
	if err != nil {
		return core.Message{}, err
	}
	var final core.Message
	cancelled := false
	for ev := range events {
		if ev.Type == AgentEventTypeError && ev.Err != nil {
			return core.Message{}, ev.Err
		}
		if ev.Type == AgentEventTypeTurnCancelled {
			cancelled = true
		}
		if ev.Type == AgentEventTypeDone && ev.Message != nil {
			final = *ev.Message
		}
	}
	if final.ID == "" {
		if cancelled {
			if err := ctx.Err(); err != nil {
				return core.Message{}, err
			}
			return core.Message{}, context.Canceled
		}
		return core.Message{}, errors.New("agent finished without final message")
	}
	return final, nil
}

func (a *Agent) RunStream(ctx context.Context, sessionID, input string) (<-chan AgentEvent, error) {
	return a.RunStreamWithOptions(ctx, sessionID, input, false)
}
