package app

import (
	"path/filepath"
	"strings"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/defaults"
	"github.com/usewhale/whale/internal/llm"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/skills"
	"github.com/usewhale/whale/internal/tasks"
	"github.com/usewhale/whale/internal/tools"
	"github.com/usewhale/whale/internal/workflow"
)

func (a *App) rebuildTaskRuntimeLocked() error {
	if a == nil {
		return nil
	}
	effort := a.reasoningEffort
	thinking := a.thinkingEnabled
	apiKey := a.apiKey
	cfg := a.cfg
	providerFactory := func(model string, maxTokens int) (llm.Provider, error) {
		if strings.TrimSpace(model) == "" {
			model = defaults.DefaultModel
		}
		return newDeepSeekProvider(providerOptions{
			APIKey:                   apiKey,
			BaseURL:                  cfg.APIBaseURL,
			Model:                    model,
			ReasoningEffort:          effort,
			ThinkingEnabled:          thinking,
			MaxTokens:                maxTokens,
			RetryPolicy:              retryPolicyFromConfig(cfg),
			StreamMaxAttempts:        cfg.RetryStreamMaxAttempts,
			StreamIdleTimeout:        cfg.RetryStreamIdleTimeout,
			DeepSeekPrefixCompletion: cfg.DeepSeekPrefixCompletion,
			DeepSeekMultimodal:       cfg.DeepSeekMultimodal,
			DeepSeekWebSearch:        cfg.DeepSeekWebSearch,
		})
	}
	providerFactoryWithOptions := func(req tasks.ProviderRequest) (llm.Provider, error) {
		model := strings.TrimSpace(req.Model)
		if model == "" {
			model = defaults.DefaultModel
		}
		reqEffort := normalizeEffort(core.FirstNonEmpty(strings.TrimSpace(req.Effort), effort))
		return newDeepSeekProvider(providerOptions{
			APIKey:                   apiKey,
			BaseURL:                  cfg.APIBaseURL,
			Model:                    model,
			ReasoningEffort:          reqEffort,
			ThinkingEnabled:          thinking,
			MaxTokens:                req.MaxTokens,
			RetryPolicy:              retryPolicyFromConfig(cfg),
			StreamMaxAttempts:        cfg.RetryStreamMaxAttempts,
			StreamIdleTimeout:        cfg.RetryStreamIdleTimeout,
			DeepSeekPrefixCompletion: cfg.DeepSeekPrefixCompletion,
			DeepSeekMultimodal:       cfg.DeepSeekMultimodal,
			DeepSeekWebSearch:        cfg.DeepSeekWebSearch,
		})
	}
	workspaceTools := func(workspace tasks.ToolWorkspace) (*core.ToolRegistry, error) {
		toolset, err := tools.NewToolset(workspace.WorkspaceRoot)
		if err != nil {
			return nil, err
		}
		toolset.SetWorktreeContext(workspace.WorktreeRoot, workspace.OriginalWorkspace)
		toolset.SetForegroundShellWait(cfg.ShellForegroundWaitDefaultMS, cfg.ShellForegroundWaitMaxMS)
		toolset.SetExecBoundaryPolicy(policy.RulePolicy{
			Default:       a.permissionPolicy.Default,
			Rules:         append([]policy.PermissionRule(nil), a.permissionPolicy.Rules...),
			WorkspaceRoot: workspace.WorkspaceRoot,
			WorktreeRoot:  workspace.WorktreeRoot,
		})
		toolset.SetExecBoundaryApproval(func() string { return a.sessionID }, func(req policy.ApprovalRequest) policy.ApprovalDecision {
			a.approvalMu.Lock()
			defer a.approvalMu.Unlock()
			if a.autoAcceptPermissions {
				return policy.ApprovalAllow
			}
			return a.approvalFn(req)
		})
		toolset.SetSkillDisabled(cfg.SkillsDisabled)
		if a.pluginManager != nil {
			toolset.SetExtraSkills(a.pluginManager.Skills())
		}
		toolset.SetWebFetchExtractor(newDeepSeekWebFetchExtractor(webFetchExtractorOptions{
			APIKey:  apiKey,
			BaseURL: cfg.APIBaseURL,
		}))
		return core.NewToolRegistryChecked(toolset.Tools())
	}
	var extraSkills []*skills.Skill
	if a.pluginManager != nil {
		extraSkills = a.pluginManager.Skills()
	}
	taskRunner := tasks.NewRunner(tasks.RunnerConfig{
		ProviderFactory:            providerFactory,
		ProviderFactoryWithOptions: providerFactoryWithOptions,
		ParentTools:                a.subagentToolRegistry,
		WorkspaceTools:             workspaceTools,
		AgentDefinitions:           tasks.NewAgentDefinitionLibraryWithDefinitions(a.workspaceRoot, taskAgentDefinitions(a.pluginAgents)),
		ParentPolicy:               a.permissionPolicy,
		MessageStore:               a.msgStore,
		SessionsDir:                a.sessionsDir,
		ParentSessionIDFunc:        func() string { return a.sessionID },
		WorkspaceRoot:              a.workspaceRoot,
		MemoryEnabled:              cfg.MemoryEnabled,
		MemoryMaxChars:             cfg.MemoryMaxChars,
		MemoryFileOrder:            parseCSVList(cfg.MemoryFileOrder),
		SkillsDisabled:             cfg.SkillsDisabled,
		ExtraSkills:                extraSkills,
		AutoCompact:                cfg.AutoCompact,
		AutoCompactThreshold:       cfg.AutoCompactThreshold,
		DefaultModel:               defaults.DefaultModel,
		DefaultMaxTokens:           tasks.DefaultMaxTokens,
		DefaultMaxToolIters:        tasks.DefaultMaxToolIters,
		SummaryMaxChars:            tasks.DefaultSummaryMaxChar,
		UsageLogPath:               filepath.Join(cfg.DataDir, "usage"),
		ApprovalFunc: func(req policy.ApprovalRequest) policy.ApprovalDecision {
			a.approvalMu.Lock()
			defer a.approvalMu.Unlock()
			if a.autoAcceptPermissions {
				return policy.ApprovalAllow
			}
			return a.approvalFn(req)
		},
	})
	a.taskTools = tasks.NewTools(taskRunner)
	a.workflowManager = nil
	a.workflowRunner = nil
	workflowLibrary := workflow.NewLibrary(a.workspaceRoot)
	if cfg.WorkflowsEnabled {
		workflowStore, err := workflow.NewFileRunEventStore(cfg.DataDir)
		if err != nil {
			return err
		}
		workflowScheduler := workflow.NewTaskScheduler(workflowStore, taskRunner)
		a.workflowManager = workflow.NewRunManager(workflowStore, workflowScheduler)
		a.workflowRunner = workflow.NewScriptRunner(cfg.DataDir, a.workflowManager)
		a.workflowRunner.Library = workflowLibrary
	}
	a.workflowTools = nil
	if cfg.WorkflowsEnabled {
		a.workflowTools = []core.Tool{workflow.NewToolWithOptions(a.workflowRunner, workflow.ToolOptions{
			ParentSessionIDFunc: func() string { return a.sessionID },
			Enabled:             true,
			Library:             workflowLibrary,
		})}
	}
	return nil
}

func (a *App) reloadWorkflowConfigForTurn() error {
	if a == nil {
		return nil
	}
	loaded, err := LoadAndApplyConfig(a.workflowReloadConfigOverlay(), a.workspaceRoot)
	if err != nil {
		return err
	}
	changed := a.cfg.WorkflowsEnabled != loaded.WorkflowsEnabled ||
		a.cfg.WorkflowKeywordTrigger != loaded.WorkflowKeywordTrigger ||
		!sameStringSlice(a.cfg.TrustedWorkflows, loaded.TrustedWorkflows)
	if !changed {
		return nil
	}
	workflowsEnabledChanged := a.cfg.WorkflowsEnabled != loaded.WorkflowsEnabled
	a.cfg.WorkflowsEnabled = loaded.WorkflowsEnabled
	a.cfg.WorkflowKeywordTrigger = loaded.WorkflowKeywordTrigger
	a.cfg.TrustedWorkflows = append([]string(nil), loaded.TrustedWorkflows...)
	a.toolMu.Lock()
	if err := a.rebuildTaskRuntimeLocked(); err != nil {
		a.toolMu.Unlock()
		return err
	}
	a.toolMu.Unlock()
	if err := a.refreshMCPTools(); err != nil {
		return err
	}
	a.resetAgent()
	if workflowsEnabledChanged {
		a.recordWorkflowsEnabledChanged(a.cfg.WorkflowsEnabled)
	}
	return nil
}

func (a *App) workflowReloadConfigOverlay() Config {
	if a == nil {
		return Config{}
	}
	cfg := Config{DataDir: a.cfg.DataDir}
	if a.workflowConfigOverlay.workflowsEnabledSet {
		cfg.WorkflowsEnabled = a.workflowConfigOverlay.workflowsEnabled
		cfg.WorkflowsEnabledExplicit = true
	}
	if a.workflowConfigOverlay.workflowKeywordTriggerSet {
		cfg.WorkflowKeywordTrigger = a.workflowConfigOverlay.workflowKeywordTrigger
		cfg.WorkflowKeywordTriggerExplicit = true
	}
	if len(a.workflowConfigOverlay.trustedWorkflows) > 0 {
		cfg.TrustedWorkflows = append([]string(nil), a.workflowConfigOverlay.trustedWorkflows...)
	}
	return cfg
}

func workflowConfigOverlayFromInput(cfg Config) workflowConfigOverlay {
	def := DefaultConfig()
	overlay := workflowConfigOverlay{}
	if cfg.WorkflowsEnabledExplicit || (cfg.configDefaulted && cfg.WorkflowsEnabled != def.WorkflowsEnabled) {
		overlay.workflowsEnabledSet = true
		overlay.workflowsEnabled = cfg.WorkflowsEnabled
	}
	if cfg.WorkflowKeywordTriggerExplicit || (cfg.configDefaulted && cfg.WorkflowKeywordTrigger != def.WorkflowKeywordTrigger) {
		overlay.workflowKeywordTriggerSet = true
		overlay.workflowKeywordTrigger = cfg.WorkflowKeywordTrigger
	}
	if len(cfg.TrustedWorkflows) > 0 {
		overlay.trustedWorkflows = append([]string(nil), cfg.TrustedWorkflows...)
	}
	return overlay
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
