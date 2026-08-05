package app

import (
	"fmt"
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

func initAppRuntime(cfg Config, sessionInit appSessionInit, toolInit appToolInit, workspaceRoot, worktreeRoot string, parentSessionIDFunc func() string, approvalFunc policy.ApprovalFunc) (appRuntimeInit, error) {
	model := core.FirstNonEmpty(strings.TrimSpace(cfg.Model), defaults.DefaultModel)
	effort := normalizeEffort(core.FirstNonEmpty(strings.TrimSpace(cfg.ReasoningEffort), defaults.DefaultReasoningEffort))
	viewMode, err := NormalizeViewMode(cfg.ViewMode)
	if err != nil {
		return appRuntimeInit{}, err
	}
	cfg.ViewMode = viewMode
	thinking := cfg.ThinkingEnabled
	contextWindow := contextWindowForModel(model)
	apiKey, err := LoadDeepSeekAPIKey(cfg.DataDir)
	if err != nil {
		return appRuntimeInit{}, fmt.Errorf("load api key failed: %w", err)
	}
	toolInit.toolset.SetExecBoundaryPolicy(policy.RulePolicy{
		Default:       cfg.PermissionDefault,
		Rules:         append([]policy.PermissionRule(nil), cfg.PermissionRules...),
		WorkspaceRoot: workspaceRoot,
		WorktreeRoot:  worktreeRoot,
	})
	toolInit.toolset.SetExecBoundaryApproval(parentSessionIDFunc, approvalFunc)
	toolInit.toolset.SetWebFetchExtractor(newDeepSeekWebFetchExtractor(webFetchExtractorOptions{
		APIKey:      apiKey,
		BaseURL:     cfg.APIBaseURL,
		API:         cfg.DeepSeekAPI,
		RetryPolicy: retryPolicyFromConfig(cfg),
	}))
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
			DeepSeekAPI:              cfg.DeepSeekAPI,
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
			DeepSeekAPI:              cfg.DeepSeekAPI,
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
			Default:       cfg.PermissionDefault,
			Rules:         append([]policy.PermissionRule(nil), cfg.PermissionRules...),
			WorkspaceRoot: workspace.WorkspaceRoot,
			WorktreeRoot:  workspace.WorktreeRoot,
		})
		toolset.SetExecBoundaryApproval(parentSessionIDFunc, approvalFunc)
		toolset.SetSkillDisabled(cfg.SkillsDisabled)
		if toolInit.pluginManager != nil {
			toolset.SetExtraSkills(toolInit.pluginManager.Skills())
		}
		toolset.SetWebFetchExtractor(newDeepSeekWebFetchExtractor(webFetchExtractorOptions{
			APIKey:      apiKey,
			BaseURL:     cfg.APIBaseURL,
			API:         cfg.DeepSeekAPI,
			RetryPolicy: retryPolicyFromConfig(cfg),
		}))
		return core.NewToolRegistryChecked(toolset.Tools())
	}
	extraSkills := []*skills.Skill(nil)
	if toolInit.pluginManager != nil {
		extraSkills = toolInit.pluginManager.Skills()
	}
	taskRunner := tasks.NewRunner(tasks.RunnerConfig{
		ProviderFactory:            providerFactory,
		ProviderFactoryWithOptions: providerFactoryWithOptions,
		ParentTools:                toolInit.subagentToolRegistry,
		WorkspaceTools:             workspaceTools,
		AgentDefinitions:           tasks.NewAgentDefinitionLibraryWithDefinitions(workspaceRoot, taskAgentDefinitions(toolInit.pluginAgents)),
		ParentPolicy:               policy.RulePolicy{Default: cfg.PermissionDefault, Rules: append([]policy.PermissionRule{}, cfg.PermissionRules...), WorkspaceRoot: workspaceRoot, WorktreeRoot: worktreeRoot},
		MessageStore:               sessionInit.msgStore,
		SessionsDir:                sessionInit.sessionsDir,
		ParentSessionID:            sessionInit.sessionID,
		ParentSessionIDFunc:        parentSessionIDFunc,
		WorkspaceRoot:              workspaceRoot,
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
		ApprovalFunc:               approvalFunc,
	})
	taskTools := tasks.NewTools(taskRunner)
	goalTools := newGoalTools(cfg.DataDir, sessionInit.sessionsDir, parentSessionIDFunc)
	var workflowManager *workflow.RunManager
	var workflowRunner *workflow.ScriptRunner
	workflowLibrary := workflow.NewLibrary(workspaceRoot)
	if cfg.WorkflowsEnabled {
		workflowStore, err := workflow.NewFileRunEventStore(cfg.DataDir)
		if err != nil {
			return appRuntimeInit{}, fmt.Errorf("init workflow event store failed: %w", err)
		}
		workflowScheduler := workflow.NewTaskScheduler(workflowStore, taskRunner)
		workflowManager = workflow.NewRunManager(workflowStore, workflowScheduler)
		workflowRunner = workflow.NewScriptRunner(cfg.DataDir, workflowManager)
		workflowRunner.Library = workflowLibrary
	}
	// Only expose the workflow tool when dynamic workflows are enabled. When
	// disabled, the tool must not appear in the schema at all so the agent never
	// queries or mentions workflow functionality (regardless of keyword trigger).
	var workflowTools []core.Tool
	if cfg.WorkflowsEnabled {
		workflowTools = []core.Tool{workflow.NewToolWithOptions(workflowRunner, workflow.ToolOptions{
			ParentSessionIDFunc: parentSessionIDFunc,
			Enabled:             true,
			Library:             workflowLibrary,
		})}
	}
	registeredTools := append([]core.Tool{}, toolInit.baseTools...)
	registeredTools = append(registeredTools, toolInit.pluginTools...)
	registeredTools = append(registeredTools, taskTools...)
	registeredTools = append(registeredTools, goalTools...)
	registeredTools = append(registeredTools, workflowTools...)
	toolRegistry, err := core.NewToolRegistryChecked(registeredTools)
	if err != nil {
		return appRuntimeInit{}, fmt.Errorf("init tool registry failed: %w", err)
	}
	return appRuntimeInit{
		cfg:             cfg,
		model:           model,
		effort:          effort,
		thinking:        thinking,
		contextWindow:   contextWindow,
		apiKey:          apiKey,
		taskTools:       taskTools,
		goalTools:       goalTools,
		workflowTools:   workflowTools,
		workflowManager: workflowManager,
		workflowRunner:  workflowRunner,
		toolRegistry:    toolRegistry,
	}, nil
}
