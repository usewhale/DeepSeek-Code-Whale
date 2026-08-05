package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/workflow"
)

func (a *App) RunUserPromptSubmitHook(input string) (blocked bool, output string, updatedInput string) {
	return a.RunUserPromptSubmitHookWithObserver(input, nil)
}

func (a *App) RunUserPromptSubmitHookWithObserver(input string, observer agent.HookRunObserver) (blocked bool, output string, updatedInput string) {
	if a == nil || a.hookRunner == nil || a.hookRunner.Empty() {
		return false, "", input
	}
	report := a.hookRunner.RunHookWithObserver(a.ctx, agent.NewUserPromptSubmitPayload(a.sessionID, a.workspaceRoot, input), observer)
	lines := renderHookReport(report)
	if report.Blocked {
		lines = append(lines, "assistant> blocked by UserPromptSubmit hook")
	}
	updatedInput = input
	if strings.TrimSpace(report.UpdatedInput) != "" {
		updatedInput = strings.TrimSpace(report.UpdatedInput)
	}
	return report.Blocked, strings.Join(lines, "\n"), updatedInput
}

func (a *App) RunStopHook(lastAssistantText string, turn int) string {
	return a.RunStopHookWithObserver(lastAssistantText, turn, nil)
}

func (a *App) RunStopHookWithObserver(lastAssistantText string, turn int, observer agent.HookRunObserver) string {
	if a.hookRunner.Empty() {
		return ""
	}
	report := a.hookRunner.RunHookWithObserver(a.ctx, agent.NewStopPayload(a.sessionID, a.workspaceRoot, lastAssistantText, turn), observer)
	return strings.Join(renderHookReport(report), "\n")
}

func (a *App) RunSessionStartHook(observer agent.HookRunObserver) string {
	if a == nil || a.hookRunner == nil || a.hookRunner.Empty() {
		return ""
	}
	report := a.hookRunner.RunHookWithObserver(a.ctx, agent.NewSessionStartPayload(a.sessionID, a.workspaceRoot), observer)
	return strings.Join(renderHookReport(report), "\n")
}

func (a *App) RunPermissionRequestHook(req policy.ApprovalRequest, observer agent.HookRunObserver) (blocked bool, output string) {
	if a == nil || a.hookRunner == nil || a.hookRunner.Empty() {
		return false, ""
	}
	payload := agent.NewPermissionRequestPayload(req.SessionID, a.workspaceRoot, req.ToolCall, req.Reason, req.Code)
	report := a.hookRunner.RunHookWithObserver(a.ctx, payload, observer)
	lines := renderHookReport(report)
	if report.Blocked {
		lines = append(lines, "assistant> blocked by PermissionRequest hook")
	}
	return report.Blocked, strings.Join(lines, "\n")
}

func (a *App) RunSubagentHook(event agent.HookEvent, info *agent.TaskActivityInfo, observer agent.HookRunObserver) string {
	if a == nil || a.hookRunner == nil || a.hookRunner.Empty() || info == nil {
		return ""
	}
	payload := agent.NewSubagentHookPayload(event, a.sessionID, a.workspaceRoot, info.Role, info.Model, info.Summary)
	report := a.hookRunner.RunHookWithObserver(a.ctx, payload, observer)
	return strings.Join(renderHookReport(report), "\n")
}

func (a *App) ensureAgent() (*agent.Agent, error) {
	if a.a == nil {
		var pluginBlocks []string
		if a.pluginManager != nil {
			pluginBlocks = a.pluginManager.StartupBlocks(a.ctx)
		}
		provider, err := newDeepSeekProvider(providerOptions{
			APIKey:                   a.apiKey,
			BaseURL:                  a.cfg.APIBaseURL,
			Model:                    a.model,
			ReasoningEffort:          a.reasoningEffort,
			ThinkingEnabled:          a.thinkingEnabled,
			RetryPolicy:              retryPolicyFromConfig(a.cfg),
			StreamMaxAttempts:        a.cfg.RetryStreamMaxAttempts,
			StreamIdleTimeout:        a.cfg.RetryStreamIdleTimeout,
			DeepSeekPrefixCompletion: a.cfg.DeepSeekPrefixCompletion,
			DeepSeekMultimodal:       a.cfg.DeepSeekMultimodal,
			DeepSeekWebSearch:        a.cfg.DeepSeekWebSearch,
			DeepSeekAPI:              a.cfg.DeepSeekAPI,
		})
		if err != nil {
			return nil, err
		}
		// When auto-review is enabled from config, it subsumes auto-accept
		// (same as the interactive toggle). Both cfg and the live field must
		// be set: cfg for hydration events, autoAcceptPermissions for the
		// approval callback that runs during a turn.
		if a.cfg.AutoReviewEnabled {
			a.cfg.AutoAcceptPermissions = true
			a.autoAcceptPermissions = true
		}
		a.a = agent.NewAgentWithRegistry(provider, a.msgStore, a.toolRegistry,
			agent.WithSessionMode(a.currentMode),
			agent.WithSessionsDir(a.sessionsDir),
			agent.WithBudgetWarningUSD(a.budgetWarningUSD),
			agent.WithUsageLogPath(filepath.Join(a.cfg.DataDir, "usage")),
			agent.WithAutoCompact(a.cfg.AutoCompact, a.cfg.AutoCompactThreshold, a.contextWindow),
			agent.WithToolPolicy(a.permissionPolicy),
			agent.WithToolRefresh(func(context.Context) error {
				return a.refreshMCPTools()
			}),
			agent.WithHookRunner(a.hookRunner),
			agent.WithExtraSystemBlocks(pluginBlocks...),
			agent.WithDynamicSystemBlocksForTurn(a.workflowDynamicSystemBlock),
			agent.WithProjectMemory(a.cfg.MemoryEnabled, a.cfg.MemoryMaxChars, parseCSVList(a.cfg.MemoryFileOrder), a.workspaceRoot),
			agent.WithWorktreeContext(a.worktree.Path, a.worktree.OriginalWorkspace),
			agent.WithMaxParallelSubagents(a.cfg.MaxParallelSubagents),
			agent.WithClassifierConfig(agent.ClassifierConfig{
				Enabled:     a.cfg.AutoReviewEnabled,
				Model:       a.cfg.AutoReviewModel,
				APIKey:      a.apiKey,
				TimeoutMS:   a.cfg.AutoReviewTimeoutMS,
				AllowRules:  a.cfg.AutoReviewAllowRules,
				DenyRules:   a.cfg.AutoReviewDenyRules,
				Environment: a.cfg.AutoReviewEnvironment,
			}),
			agent.WithDisabledSkills(a.cfg.SkillsDisabled),
			agent.WithExtraSkills(a.pluginManager.Skills()),
			agent.WithApprovalFunc(func(req policy.ApprovalRequest) policy.ApprovalDecision {
				a.approvalMu.Lock()
				defer a.approvalMu.Unlock()
				if a.autoAcceptPermissions {
					return policy.ApprovalAllow
				}
				return a.approvalFn(req)
			}),
			agent.WithUserInputFunc(a.userInput),
		)
	}
	return a.a, nil
}

func (a *App) RunTurn(ctx context.Context, input string, hiddenInput bool) (<-chan agent.AgentEvent, error) {
	return a.RunTurnWithOptions(ctx, input, agent.RunOptions{HiddenInput: hiddenInput})
}

func (a *App) RunTurnWithOptions(ctx context.Context, input string, opts agent.RunOptions) (<-chan agent.AgentEvent, error) {
	return a.RunTurnWithContentOptions(ctx, []core.MessagePart{{Type: core.MessagePartText, Text: input}}, opts)
}

func (a *App) RunTurnWithContentOptions(ctx context.Context, parts []core.MessagePart, opts agent.RunOptions) (<-chan agent.AgentEvent, error) {
	input := core.MessagePartsPlainText(parts)
	if !opts.HiddenInput && strings.TrimSpace(input) != "" {
		_, _ = session.PatchSessionMeta(a.sessionsDir, a.sessionID, session.SessionMetaPatch{Title: input})
	}
	if err := a.reloadWorkflowConfigForTurn(); err != nil {
		a.pendingGoalTurn = false
		return nil, err
	}
	a.pendingGoalTurn = opts.GoalContinuation
	opts = a.applyRunOptionsDefaults(opts)
	if !opts.WorkflowAuthoring {
		opts.WorkflowAuthoring = workflowAuthoringRequested(input)
	}
	if err := a.ensureCurrentModeMarker(); err != nil {
		a.pendingGoalTurn = false
		return nil, err
	}
	ag, err := a.ensureAgent()
	if err != nil {
		a.pendingGoalTurn = false
		return nil, err
	}
	return ag.RunStreamWithContentOptions(ctx, a.sessionID, parts, opts)
}

func (a *App) RunTurnWithInjectedInput(ctx context.Context, visibleInput, hiddenInput string) (<-chan agent.AgentEvent, error) {
	return a.RunTurnWithInjectedInputOptions(ctx, visibleInput, hiddenInput, agent.RunOptions{})
}

func (a *App) RunTurnWithInjectedInputOptions(ctx context.Context, visibleInput, hiddenInput string, opts agent.RunOptions) (<-chan agent.AgentEvent, error) {
	return a.RunTurnWithInjectedContentOptions(ctx, []core.MessagePart{{Type: core.MessagePartText, Text: visibleInput}}, hiddenInput, opts)
}

func (a *App) RunTurnWithInjectedContentOptions(ctx context.Context, visibleParts []core.MessagePart, hiddenInput string, opts agent.RunOptions) (<-chan agent.AgentEvent, error) {
	visibleInput := core.MessagePartsPlainText(visibleParts)
	if strings.TrimSpace(visibleInput) != "" {
		_, _ = session.PatchSessionMeta(a.sessionsDir, a.sessionID, session.SessionMetaPatch{Title: visibleInput})
	}
	if err := a.reloadWorkflowConfigForTurn(); err != nil {
		return nil, err
	}
	opts = a.applyRunOptionsDefaults(opts)
	if !opts.WorkflowAuthoring {
		opts.WorkflowAuthoring = workflowAuthoringRequested(visibleInput + "\n" + hiddenInput)
	}
	if err := a.ensureCurrentModeMarker(); err != nil {
		return nil, err
	}
	ag, err := a.ensureAgent()
	if err != nil {
		return nil, err
	}
	return ag.RunStreamWithInjectedContentOptions(ctx, a.sessionID, visibleParts, hiddenInput, opts)
}

func (a *App) InjectTurnInput(ctx context.Context, input string, opts agent.RunOptions) (bool, error) {
	return a.InjectTurnInputWithHidden(ctx, input, "", opts)
}

func (a *App) InjectTurnInputWithHidden(ctx context.Context, visibleInput, hiddenInput string, opts agent.RunOptions) (bool, error) {
	opts = a.applyRunOptionsDefaults(opts)
	if !opts.HiddenInput && strings.TrimSpace(visibleInput) != "" {
		_, _ = session.PatchSessionMeta(a.sessionsDir, a.sessionID, session.SessionMetaPatch{Title: visibleInput})
	}
	ag, err := a.ensureAgent()
	if err != nil {
		return false, err
	}
	messages := []core.Message{{SessionID: a.sessionID, Role: core.RoleUser, Text: visibleInput, Hidden: opts.HiddenInput}}
	if strings.TrimSpace(hiddenInput) != "" {
		messages = append(messages, core.Message{SessionID: a.sessionID, Role: core.RoleUser, Text: hiddenInput, Hidden: true})
	}
	return ag.InjectTurnInput(ctx, a.sessionID, messages)
}

func (a *App) applyRunOptionsDefaults(opts agent.RunOptions) agent.RunOptions {
	return opts
}

func (a *App) workflowDynamicSystemBlock(opts agent.RunOptions) string {
	if !a.cfg.WorkflowsEnabled {
		// Disabled means the workflow feature is invisible to the agent: the
		// workflow tool is not registered, and we inject no guidance about it.
		// The model treats "workflow" as an ordinary word and answers normally.
		return ""
	}
	blocks := []string{strings.TrimSpace(`
Workflow runtime.

- Dynamic workflows are enabled in Whale.
- Treat any earlier workflow_disabled tool results in this conversation as stale; the configuration has changed since then.
- For workflow discovery or launch, use the workflow tool instead of inspecting workflow directories with file or shell tools.
- Use the full workflow authoring rules only when the user asks to create, generate, write, or save a new workflow.
`)}
	if opts.WorkflowAuthoring {
		blocks = append(blocks, agent.WorkflowAuthoringSystemBlock())
	}
	if a.cfg.WorkflowKeywordTrigger && a.workflowRunner != nil && a.workflowRunner.Library != nil {
		if catalog := strings.TrimSpace(workflow.RenderPromptCatalog(context.Background(), a.workflowRunner.Library, workflow.DefaultPromptCatalogLimit)); catalog != "" {
			blocks = append(blocks, catalog)
		}
	}
	return strings.Join(blocks, "\n\n")
}

func workflowAuthoringRequested(input string) bool {
	text := strings.ToLower(strings.TrimSpace(input))
	if text == "" {
		return false
	}
	if !strings.Contains(text, "workflow") && !strings.Contains(text, "工作流") {
		return false
	}
	for _, marker := range []string{
		"create",
		"generate",
		"write",
		"author",
		"save",
		"new workflow",
		"add workflow",
		"新增",
		"创建",
		"生成",
		"编写",
		"写一个",
		"保存",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (a *App) FinalizeTurn(lastAssistantText string, completed bool) error {
	if err := a.finalizeGoalTurn(lastAssistantText, completed); err != nil {
		return err
	}
	meta, err := session.LoadSessionMeta(a.sessionsDir, a.sessionID)
	if err != nil {
		return nil
	}
	nextTurn := meta.TurnCount + 1
	summary := strings.TrimSpace(lastAssistantText)
	if len(summary) > 240 {
		summary = summary[:240]
	}
	_, err = session.PatchSessionMeta(a.sessionsDir, a.sessionID, session.SessionMetaPatch{Workspace: a.workspaceRoot, Branch: a.branch, TurnCount: &nextTurn, Summary: summary})
	return err
}

func (a *App) finalizeGoalTurn(lastAssistantText string, completed bool) error {
	if a == nil || !a.pendingGoalTurn {
		return nil
	}
	a.pendingGoalTurn = false
	if !completed {
		return nil
	}
	st, ok, err := session.LoadGoalState(a.sessionsDir, a.sessionID)
	if err != nil || !ok {
		return err
	}
	if st.Status != session.GoalStatusCompleted {
		return nil
	}
	st = refreshCompletedGoalUsageWithTotal(st, a.currentSessionGoalTokens())
	return session.SaveGoalState(a.sessionsDir, a.sessionID, st)
}
