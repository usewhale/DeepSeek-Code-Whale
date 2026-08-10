package app

import (
	"context"
	"sync"
	"time"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm/deepseek"
	"github.com/usewhale/whale/internal/lsp"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/plugins"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
	"github.com/usewhale/whale/internal/tools"
	"github.com/usewhale/whale/internal/workflow"
)

const (
	ViewModeDefault = "default"
	ViewModeFocus   = "focus"
)

type Config struct {
	DataDir                        string
	ConfigLoaded                   bool
	PermissionDefault              policy.PermissionAction
	PermissionRules                []policy.PermissionRule
	AutoAcceptPermissions          bool
	AutoCompact                    bool
	AutoCompactThreshold           float64
	MemoryEnabled                  bool
	MemoryMaxChars                 int
	MemoryFileOrder                string
	BudgetWarningUSD               float64
	Model                          string
	ModelExplicit                  bool
	ReasoningEffort                string
	ThinkingEnabled                bool
	CheckForUpdateOnStartup        bool
	ViewMode                       string
	ShowReasoning                  bool
	RetryMaxAttempts               int
	RetryMaxAttemptsExplicit       bool
	RetryStreamMaxAttempts         int
	RetryStreamIdleTimeout         time.Duration
	RetryMaxDelay                  time.Duration
	DeepSeekPrefixCompletion       bool
	DeepSeekMultimodal             MultimodalProviderConfig
	DeepSeekWebSearch              deepseek.WebSearchMode
	DeepSeekAPI                    deepseek.API
	ShellForegroundWaitDefaultMS   int
	ShellForegroundWaitMaxMS       int
	MaxParallelSubagents           int
	MCPConfigPath                  string
	APIBaseURL                     string
	SkillsDisabled                 []string
	Plugins                        plugins.ConfigMap
	LSPEnabled                     bool
	LSPEnabledExplicit             bool
	WorkflowsEnabled               bool
	WorkflowsEnabledExplicit       bool
	WorkflowKeywordTrigger         bool
	WorkflowKeywordTriggerExplicit bool
	TrustedWorkflows               []string
	AutoReviewEnabled              bool
	AutoReviewModel                string
	AutoReviewTimeoutMS            int
	AutoReviewAllowRules           []string
	AutoReviewDenyRules            []string
	AutoReviewEnvironment          []string
	configDefaulted                bool
}

type MultimodalProviderConfig struct {
	Enabled   bool
	Compat    string
	BaseURL   string
	APIKey    string
	APIKeyEnv string
	Model     string
}

type StartOptions struct {
	SessionID     string
	ModeOverride  string
	ResumeMenu    bool
	NewSession    bool
	Worktree      WorktreeSession
	ApprovalFunc  policy.ApprovalFunc
	UserInputFunc agent.UserInputFunc
}

type WorktreeSession struct {
	Name               string
	Workspace          string
	Path               string
	Branch             string
	OriginalWorkspace  string
	OriginalBranch     string
	OriginalHeadCommit string
}

type WorktreeExitSummary struct {
	Session      WorktreeSession
	ChangedFiles int
	IgnoredFiles int
	Commits      int
}

type WorktreeExitResult struct {
	Action        string
	Message       string
	BranchWarning string
}

type ResumeChoice struct {
	Index int
	ID    string
}

type App struct {
	ctx                   context.Context
	sessionsDir           string
	workspaceRoot         string
	branch                string
	msgStore              *store.JSONLStore
	toolRegistry          *core.ToolRegistry
	baseToolRegistry      *core.ToolRegistry
	subagentToolRegistry  *core.ToolRegistry
	toolset               *tools.Toolset
	baseTools             []core.Tool
	taskTools             []core.Tool
	goalTools             []core.Tool
	workflowTools         []core.Tool
	hooks                 []agent.ResolvedHook
	hookStates            agent.HookStates
	hookRunner            *agent.HookRunner
	hookSources           []string
	currentMode           session.Mode
	sessionID             string
	permissionPolicy      policy.RulePolicy
	autoAcceptPermissions bool
	budgetWarningUSD      float64
	cfg                   Config
	model                 string
	reasoningEffort       string
	thinkingEnabled       bool
	contextWindow         int
	mcpManager            *whalemcp.Manager
	lspManager            *lsp.Manager
	mcpSig                string
	mcpSigFrozen          bool
	promotedTools         map[string]bool // qualified tool names that have been promoted
	promotedCatalogHash   string          // catalog hash when tools were promoted
	pluginManager         *plugins.Manager
	pluginTools           []core.Tool
	pluginAgents          []plugins.AgentDefinition
	workflowManager       *workflow.RunManager
	workflowRunner        *workflow.ScriptRunner
	workflowConfigOverlay workflowConfigOverlay
	worktree              WorktreeSession
	mcpInitMu             sync.Mutex
	mcpInitStarted        bool
	// toolMu guards mutable tool/plugin state (pluginManager, pluginTools,
	// toolset, hookRunner) that SetPluginEnabled rewrites while the MCP
	// startup goroutine concurrently reads via refreshMCPTools. Held across
	// the entire refreshMCPTools body so concurrent refreshes serialize and
	// the last one always observes the latest pluginTools.
	toolMu sync.Mutex
	// sessionMu guards sessionID against the MCP startup goroutine, which reads
	// it during RestorePromotedTools while the dispatch goroutine switches
	// sessions (resume / session-new / fork). sessionsDir is immutable after
	// construction and does not need the lock.
	sessionMu sync.Mutex

	a          *agent.Agent
	apiKey     string
	approvalMu sync.Mutex
	approvalFn policy.ApprovalFunc
	userInput  agent.UserInputFunc

	pendingGoalTurn bool
}

type workflowConfigOverlay struct {
	workflowsEnabledSet       bool
	workflowsEnabled          bool
	workflowKeywordTriggerSet bool
	workflowKeywordTrigger    bool
	trustedWorkflows          []string
}
