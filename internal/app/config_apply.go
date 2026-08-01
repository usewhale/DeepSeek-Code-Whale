package app

import (
	"fmt"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/plugins"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/store"
	"github.com/usewhale/whale/internal/tools"
	"strings"
	"time"
)

func ApplyLoadedConfig(cfg *Config, loaded LoadedConfig) error {
	if err := ApplyFileConfig(cfg, loaded.Global); err != nil {
		return err
	}
	if err := ApplyFileConfig(cfg, loaded.Project); err != nil {
		return err
	}
	if err := ApplyFileConfig(cfg, loaded.ProjectLocal); err != nil {
		return err
	}
	return nil
}

func ApplyFileConfig(cfg *Config, file FileConfig) error {
	if strings.TrimSpace(file.Provider) != "" {
		provider := strings.ToLower(strings.TrimSpace(file.Provider))
		switch provider {
		case "deepseek", "minimax":
			cfg.Provider = provider
		default:
			return fmt.Errorf("invalid provider: %s", file.Provider)
		}
	}
	if strings.TrimSpace(file.Model) != "" {
		cfg.Model = strings.TrimSpace(file.Model)
	}
	if strings.TrimSpace(file.ReasoningEffort) != "" {
		cfg.ReasoningEffort = strings.TrimSpace(file.ReasoningEffort)
	}
	if file.ThinkingEnabled != nil {
		cfg.ThinkingEnabled = *file.ThinkingEnabled
	}
	if strings.TrimSpace(file.UI.ViewMode) != "" {
		mode, err := NormalizeViewMode(file.UI.ViewMode)
		if err != nil {
			return err
		}
		cfg.ViewMode = mode
	}
	if file.UI.ShowReasoning != nil {
		cfg.ShowReasoning = *file.UI.ShowReasoning
	}
	if file.UI.CheckForUpdateOnStartup != nil {
		cfg.CheckForUpdateOnStartup = *file.UI.CheckForUpdateOnStartup
	}
	if err := applyPermissionsConfig(cfg, file.Permissions); err != nil {
		return err
	}
	if file.Budget.SessionLimitUSD != nil {
		cfg.BudgetWarningUSD = *file.Budget.SessionLimitUSD
	}
	if strings.TrimSpace(file.MCP.ConfigPath) != "" {
		cfg.MCPConfigPath = expandUserPath(file.MCP.ConfigPath)
	}
	if strings.TrimSpace(file.API.BaseURL) != "" {
		cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(file.API.BaseURL), "/")
	}
	if err := applyMultimodalProviderConfig(cfg, file.Providers.DeepSeek.Multimodal); err != nil {
		return err
	}
	applyMiniMaxProviderConfig(cfg, file.Providers.MiniMax)
	if file.Retry.MaxAttempts != nil {
		if *file.Retry.MaxAttempts < 0 {
			return fmt.Errorf("invalid retry.max_attempts: must be 0 or greater")
		}
		cfg.RetryMaxAttempts = *file.Retry.MaxAttempts
		cfg.RetryMaxAttemptsExplicit = true
	}
	if file.Retry.StreamMaxAttempts != nil {
		if *file.Retry.StreamMaxAttempts <= 0 {
			return fmt.Errorf("invalid retry.stream_max_attempts: must be greater than 0")
		}
		cfg.RetryStreamMaxAttempts = *file.Retry.StreamMaxAttempts
	}
	if strings.TrimSpace(file.Retry.StreamIdleTimeout) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(file.Retry.StreamIdleTimeout))
		if err != nil {
			return fmt.Errorf("invalid retry.stream_idle_timeout: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("invalid retry.stream_idle_timeout: must be greater than 0")
		}
		cfg.RetryStreamIdleTimeout = d
	}
	if strings.TrimSpace(file.Retry.MaxDelay) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(file.Retry.MaxDelay))
		if err != nil {
			return fmt.Errorf("invalid retry.max_delay: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("invalid retry.max_delay: must be greater than 0")
		}
		cfg.RetryMaxDelay = d
	}
	if err := applyShellConfig(cfg, file.Shell); err != nil {
		return err
	}
	if file.Experimental.DeepSeekPrefixCompletion != nil {
		cfg.DeepSeekPrefixCompletion = *file.Experimental.DeepSeekPrefixCompletion
	}
	if file.Tasks.MaxParallelSubagents != nil {
		if *file.Tasks.MaxParallelSubagents <= 0 {
			return fmt.Errorf("invalid tasks.max_parallel_subagents: must be greater than 0")
		}
		cfg.MaxParallelSubagents = *file.Tasks.MaxParallelSubagents
	}
	if file.Context.AutoCompact != nil {
		cfg.AutoCompact = *file.Context.AutoCompact
	}
	if file.Context.CompactThreshold != nil {
		cfg.AutoCompactThreshold = *file.Context.CompactThreshold
	}
	if file.ProjectDoc.Enabled != nil {
		cfg.MemoryEnabled = *file.ProjectDoc.Enabled
	}
	if file.ProjectDoc.MaxBytes != nil {
		cfg.MemoryMaxChars = *file.ProjectDoc.MaxBytes
	}
	if len(file.ProjectDoc.FallbackFilenames) > 0 {
		cfg.MemoryFileOrder = strings.Join(trimList(file.ProjectDoc.FallbackFilenames), ",")
	}
	if len(file.Skills.Disabled) > 0 {
		cfg.SkillsDisabled = mergeNames(cfg.SkillsDisabled, file.Skills.Disabled)
	}
	if len(file.Skills.Enabled) > 0 {
		cfg.SkillsDisabled = removeNames(cfg.SkillsDisabled, file.Skills.Enabled)
	}
	for id, pluginConfig := range file.Plugins.RuntimeConfig() {
		if cfg.Plugins == nil {
			cfg.Plugins = plugins.ConfigMap{}
		}
		cfg.Plugins[id] = mergePluginConfig(cfg.Plugins[id], pluginConfig)
	}
	if file.Workflows.Enabled != nil {
		cfg.WorkflowsEnabled = *file.Workflows.Enabled
		cfg.WorkflowsEnabledExplicit = true
	}
	if file.Lsp.Enabled != nil {
		cfg.LSPEnabled = *file.Lsp.Enabled
		cfg.LSPEnabledExplicit = true
	}
	if file.Workflows.KeywordTriggerEnabled != nil {
		cfg.WorkflowKeywordTrigger = *file.Workflows.KeywordTriggerEnabled
		cfg.WorkflowKeywordTriggerExplicit = true
	}
	if len(file.Workflows.Trusted) > 0 {
		cfg.TrustedWorkflows = mergeNames(cfg.TrustedWorkflows, file.Workflows.Trusted)
	}
	if file.AutoReview.Enabled != nil {
		cfg.AutoReviewEnabled = *file.AutoReview.Enabled
	}
	if file.AutoReview.Model != "" {
		cfg.AutoReviewModel = file.AutoReview.Model
	}
	if file.AutoReview.TimeoutMS > 0 {
		cfg.AutoReviewTimeoutMS = file.AutoReview.TimeoutMS
	}
	if len(file.AutoReview.AllowRules) > 0 {
		cfg.AutoReviewAllowRules = append(cfg.AutoReviewAllowRules, file.AutoReview.AllowRules...)
	}
	if len(file.AutoReview.DenyRules) > 0 {
		cfg.AutoReviewDenyRules = append(cfg.AutoReviewDenyRules, file.AutoReview.DenyRules...)
	}
	if len(file.AutoReview.Environment) > 0 {
		cfg.AutoReviewEnvironment = append(cfg.AutoReviewEnvironment, file.AutoReview.Environment...)
	}
	return nil
}

func LoadAndApplyConfig(cfg Config, workspaceRoot string) (Config, error) {
	base := DefaultConfig()
	base.DataDir = core.FirstNonEmpty(strings.TrimSpace(cfg.DataDir), store.DefaultDataDir())

	loaded, err := LoadConfigFiles(base.DataDir, workspaceRoot)
	if err != nil {
		return Config{}, err
	}
	if err := ApplyLoadedConfig(&base, loaded); err != nil {
		return Config{}, err
	}
	overlayExplicitConfig(&base, cfg)
	if err := validateShellRuntimeConfig(base); err != nil {
		return Config{}, err
	}
	base.ConfigLoaded = true
	return base, nil
}

func overlayExplicitConfig(dst *Config, src Config) {
	def := DefaultConfig()
	dst.DataDir = core.FirstNonEmpty(strings.TrimSpace(src.DataDir), dst.DataDir)
	if src.ModelExplicit || (strings.TrimSpace(src.Model) != "" && src.Model != def.Model) {
		dst.Model = src.Model
		dst.ModelExplicit = src.ModelExplicit
	}
	if strings.TrimSpace(src.Provider) != "" && src.Provider != def.Provider {
		dst.Provider = strings.ToLower(strings.TrimSpace(src.Provider))
	}
	if src.PermissionDefault != "" && src.PermissionDefault != def.PermissionDefault {
		dst.PermissionDefault = src.PermissionDefault
	}
	if len(src.PermissionRules) > 0 && !permissionRulesEqual(src.PermissionRules, def.PermissionRules) {
		dst.PermissionRules = append([]policy.PermissionRule{}, src.PermissionRules...)
	}
	if src.AutoAcceptPermissions != def.AutoAcceptPermissions {
		dst.AutoAcceptPermissions = src.AutoAcceptPermissions
	}
	if src.AutoCompact != def.AutoCompact {
		dst.AutoCompact = src.AutoCompact
	}
	if src.AutoCompactThreshold != def.AutoCompactThreshold {
		dst.AutoCompactThreshold = src.AutoCompactThreshold
	}
	if src.MemoryEnabled != def.MemoryEnabled {
		dst.MemoryEnabled = src.MemoryEnabled
	}
	if src.MemoryMaxChars != 0 && src.MemoryMaxChars != def.MemoryMaxChars {
		dst.MemoryMaxChars = src.MemoryMaxChars
	}
	if strings.TrimSpace(src.MemoryFileOrder) != "" && src.MemoryFileOrder != def.MemoryFileOrder {
		dst.MemoryFileOrder = src.MemoryFileOrder
	}
	if src.BudgetWarningUSD != def.BudgetWarningUSD {
		dst.BudgetWarningUSD = src.BudgetWarningUSD
	}
	if strings.TrimSpace(src.ReasoningEffort) != "" && src.ReasoningEffort != def.ReasoningEffort {
		dst.ReasoningEffort = src.ReasoningEffort
	}
	if src.ThinkingEnabled != def.ThinkingEnabled {
		dst.ThinkingEnabled = src.ThinkingEnabled
	}
	if src.CheckForUpdateOnStartup != def.CheckForUpdateOnStartup {
		dst.CheckForUpdateOnStartup = src.CheckForUpdateOnStartup
	}
	if strings.TrimSpace(src.ViewMode) != "" && src.ViewMode != def.ViewMode {
		dst.ViewMode = src.ViewMode
	}
	if src.ShowReasoning != def.ShowReasoning {
		dst.ShowReasoning = src.ShowReasoning
	}
	if src.RetryMaxAttemptsExplicit || (src.RetryMaxAttempts != 0 && src.RetryMaxAttempts != def.RetryMaxAttempts) {
		dst.RetryMaxAttempts = src.RetryMaxAttempts
		dst.RetryMaxAttemptsExplicit = src.RetryMaxAttemptsExplicit
	}
	if src.RetryStreamMaxAttempts != 0 && src.RetryStreamMaxAttempts != def.RetryStreamMaxAttempts {
		dst.RetryStreamMaxAttempts = src.RetryStreamMaxAttempts
	}
	if src.RetryStreamIdleTimeout != 0 && src.RetryStreamIdleTimeout != def.RetryStreamIdleTimeout {
		dst.RetryStreamIdleTimeout = src.RetryStreamIdleTimeout
	}
	if src.RetryMaxDelay != 0 && src.RetryMaxDelay != def.RetryMaxDelay {
		dst.RetryMaxDelay = src.RetryMaxDelay
	}
	if src.DeepSeekPrefixCompletion != def.DeepSeekPrefixCompletion {
		dst.DeepSeekPrefixCompletion = src.DeepSeekPrefixCompletion
	}
	if src.DeepSeekMultimodal != def.DeepSeekMultimodal {
		dst.DeepSeekMultimodal = normalizeMultimodalProviderConfig(src.DeepSeekMultimodal)
	}
	if src.MiniMax != def.MiniMax {
		dst.MiniMax = normalizeMiniMaxProviderConfig(src.MiniMax)
	}
	if src.ShellForegroundWaitDefaultMS != 0 && src.ShellForegroundWaitDefaultMS != def.ShellForegroundWaitDefaultMS {
		dst.ShellForegroundWaitDefaultMS = src.ShellForegroundWaitDefaultMS
	}
	if src.ShellForegroundWaitMaxMS != 0 && src.ShellForegroundWaitMaxMS != def.ShellForegroundWaitMaxMS {
		dst.ShellForegroundWaitMaxMS = src.ShellForegroundWaitMaxMS
	}
	if src.MaxParallelSubagents != 0 && src.MaxParallelSubagents != def.MaxParallelSubagents {
		dst.MaxParallelSubagents = src.MaxParallelSubagents
	}
	if src.WorkflowsEnabledExplicit || (src.configDefaulted && src.WorkflowsEnabled != def.WorkflowsEnabled) {
		dst.WorkflowsEnabled = src.WorkflowsEnabled
		dst.WorkflowsEnabledExplicit = src.WorkflowsEnabledExplicit || (src.configDefaulted && src.WorkflowsEnabled != def.WorkflowsEnabled)
	}
	if src.LSPEnabledExplicit || (src.configDefaulted && src.LSPEnabled != def.LSPEnabled) {
		dst.LSPEnabled = src.LSPEnabled
		dst.LSPEnabledExplicit = src.LSPEnabledExplicit || (src.configDefaulted && src.LSPEnabled != def.LSPEnabled)
	}
	if src.WorkflowKeywordTriggerExplicit || (src.configDefaulted && src.WorkflowKeywordTrigger != def.WorkflowKeywordTrigger) {
		dst.WorkflowKeywordTrigger = src.WorkflowKeywordTrigger
		dst.WorkflowKeywordTriggerExplicit = src.WorkflowKeywordTriggerExplicit || (src.configDefaulted && src.WorkflowKeywordTrigger != def.WorkflowKeywordTrigger)
	}
	if strings.TrimSpace(src.MCPConfigPath) != "" {
		dst.MCPConfigPath = src.MCPConfigPath
	}
	if strings.TrimSpace(src.APIBaseURL) != "" {
		dst.APIBaseURL = strings.TrimRight(strings.TrimSpace(src.APIBaseURL), "/")
	}
	if len(src.SkillsDisabled) > 0 {
		dst.SkillsDisabled = trimList(src.SkillsDisabled)
	}
	if len(src.Plugins) > 0 {
		dst.Plugins = clonePluginConfigMap(src.Plugins)
	}
	if len(src.TrustedWorkflows) > 0 {
		dst.TrustedWorkflows = trimList(src.TrustedWorkflows)
	}
}

func validateShellRuntimeConfig(cfg Config) error {
	return validateShellForegroundWait(cfg.ShellForegroundWaitDefaultMS, cfg.ShellForegroundWaitMaxMS)
}

func applyMultimodalProviderConfig(cfg *Config, file FileMultimodalProviderConfig) error {
	next := cfg.DeepSeekMultimodal
	if file.Enabled != nil {
		next.Enabled = *file.Enabled
	}
	if strings.TrimSpace(file.Compat) != "" {
		compat := strings.ToLower(strings.TrimSpace(file.Compat))
		if compat != "openai" {
			return fmt.Errorf("invalid providers.deepseek.multimodal.compat: must be \"openai\"")
		}
		next.Compat = compat
	}
	if strings.TrimSpace(file.BaseURL) != "" {
		next.BaseURL = strings.TrimRight(strings.TrimSpace(file.BaseURL), "/")
	}
	if strings.TrimSpace(file.APIKey) != "" {
		next.APIKey = strings.TrimSpace(file.APIKey)
	}
	if strings.TrimSpace(file.APIKeyEnv) != "" {
		next.APIKeyEnv = strings.TrimSpace(file.APIKeyEnv)
	}
	if strings.TrimSpace(file.Model) != "" {
		next.Model = strings.TrimSpace(file.Model)
	}
	cfg.DeepSeekMultimodal = normalizeMultimodalProviderConfig(next)
	return nil
}

func normalizeMultimodalProviderConfig(in MultimodalProviderConfig) MultimodalProviderConfig {
	in.Compat = strings.ToLower(strings.TrimSpace(in.Compat))
	if in.Compat == "" && in.Enabled {
		in.Compat = "openai"
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.APIKey = strings.TrimSpace(in.APIKey)
	in.APIKeyEnv = strings.TrimSpace(in.APIKeyEnv)
	in.Model = strings.TrimSpace(in.Model)
	return in
}

func applyMiniMaxProviderConfig(cfg *Config, file FileMiniMaxProviderConfig) {
	next := cfg.MiniMax
	if strings.TrimSpace(file.Region) != "" {
		next.Region = strings.ToLower(strings.TrimSpace(file.Region))
	}
	if strings.TrimSpace(file.BaseURL) != "" {
		next.BaseURL = strings.TrimRight(strings.TrimSpace(file.BaseURL), "/")
	}
	if strings.TrimSpace(file.APIKey) != "" {
		next.APIKey = strings.TrimSpace(file.APIKey)
	}
	if strings.TrimSpace(file.APIKeyEnv) != "" {
		next.APIKeyEnv = strings.TrimSpace(file.APIKeyEnv)
	}
	cfg.MiniMax = normalizeMiniMaxProviderConfig(next)
}

func normalizeMiniMaxProviderConfig(in MiniMaxProviderConfig) MiniMaxProviderConfig {
	in.Region = strings.ToLower(strings.TrimSpace(in.Region))
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.APIKey = strings.TrimSpace(in.APIKey)
	in.APIKeyEnv = strings.TrimSpace(in.APIKeyEnv)
	return in
}

func applyShellConfig(cfg *Config, file FileShellConfig) error {
	hasDefault := file.ForegroundWaitDefaultMS != nil
	hasMax := file.ForegroundWaitMaxMS != nil
	if !hasDefault && !hasMax {
		return nil
	}
	nextDefault := cfg.ShellForegroundWaitDefaultMS
	nextMax := cfg.ShellForegroundWaitMaxMS
	if !hasDefault && nextDefault <= 0 {
		nextDefault = tools.DefaultForegroundShellWait()
	}
	if !hasMax && nextMax <= 0 {
		nextMax = tools.MaxForegroundShellWait()
	}
	if hasDefault {
		nextDefault = *file.ForegroundWaitDefaultMS
	}
	if hasMax {
		nextMax = *file.ForegroundWaitMaxMS
	}
	if err := validateShellForegroundWait(nextDefault, nextMax); err != nil {
		return err
	}
	cfg.ShellForegroundWaitDefaultMS = nextDefault
	cfg.ShellForegroundWaitMaxMS = nextMax
	return nil
}

func validateShellForegroundWait(defaultMS, maxMS int) error {
	if defaultMS <= 0 {
		return fmt.Errorf("invalid shell.foreground_wait_default_ms: must be greater than 0")
	}
	if maxMS <= 0 {
		return fmt.Errorf("invalid shell.foreground_wait_max_ms: must be greater than 0")
	}
	if maxMS > tools.MaxForegroundShellWaitCeilingMS() {
		return fmt.Errorf("invalid shell.foreground_wait_max_ms: must be less than or equal to %d", tools.MaxForegroundShellWaitCeilingMS())
	}
	if maxMS < defaultMS {
		return fmt.Errorf("invalid shell.foreground_wait_max_ms: effective value must be greater than or equal to shell.foreground_wait_default_ms")
	}
	return nil
}

func clonePluginConfigMap(in plugins.ConfigMap) plugins.ConfigMap {
	if len(in) == 0 {
		return nil
	}
	out := plugins.ConfigMap{}
	for id, cfg := range in {
		id = plugins.NormalizePluginID(id)
		if id != "" {
			cp := cfg
			if cfg.Enabled != nil {
				enabled := *cfg.Enabled
				cp.Enabled = &enabled
			}
			cp.MCPServers = clonePluginMCPServers(cfg.MCPServers)
			out[id] = cp
		}
	}
	return out
}

func mergePluginConfig(base, overlay plugins.Config) plugins.Config {
	out := plugins.Config{MCPServers: clonePluginMCPServers(base.MCPServers)}
	if base.Enabled != nil {
		enabled := *base.Enabled
		out.Enabled = &enabled
	}
	if overlay.Enabled != nil {
		enabled := *overlay.Enabled
		out.Enabled = &enabled
	}
	if len(overlay.MCPServers) > 0 {
		if out.MCPServers == nil {
			out.MCPServers = map[string]plugins.MCPServerConfig{}
		}
		for name, serverOverlay := range overlay.MCPServers {
			name = plugins.NormalizePluginID(name)
			if name == "" {
				continue
			}
			merged := out.MCPServers[name]
			if serverOverlay.Enabled != nil {
				enabled := *serverOverlay.Enabled
				merged.Enabled = &enabled
			}
			if len(serverOverlay.DisabledTools) > 0 {
				merged.DisabledTools = append([]string(nil), serverOverlay.DisabledTools...)
			}
			out.MCPServers[name] = merged
		}
	}
	return out
}

func permissionRulesEqual(a, b []policy.PermissionRule) bool {
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

func applyPermissionsConfig(cfg *Config, file FilePermissionsConfig) error {
	if strings.TrimSpace(file.Default) != "" {
		action, err := policy.ParsePermissionAction(file.Default)
		if err != nil {
			return fmt.Errorf("invalid permissions.default: %w", err)
		}
		cfg.PermissionDefault = action
	}
	if file.AutoAccept != nil {
		cfg.AutoAcceptPermissions = *file.AutoAccept
	}
	rules, err := policy.RulesFromConfig(policy.PermissionConfig{
		Read:              file.Read,
		Edit:              file.Edit,
		Shell:             file.Shell,
		Terminal:          file.Terminal,
		ExternalDirectory: file.ExternalDirectory,
		MCP:               file.MCP,
		Memory:            file.Memory,
		Task:              file.Task,
		WebSearch:         file.WebSearch,
		WebFetch:          file.WebFetch,
		MutatingTool:      file.MutatingTool,
	})
	if err != nil {
		return fmt.Errorf("invalid permissions: %w", err)
	}
	cfg.PermissionRules = append(cfg.PermissionRules, rules...)
	return nil
}
