package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

const (
	ConfigSettingWorkflowsEnabled       = "workflows.enabled"
	ConfigSettingWorkflowKeywordTrigger = "workflows.keyword_trigger_enabled"
)

type ConfigSettingType string

const (
	ConfigSettingBool ConfigSettingType = "bool"
)

type ConfigSettingView struct {
	ID          string
	Label       string
	Description string
	Type        ConfigSettingType
	Value       string
	Default     string
	Scope       string
	Source      string
}

type ConfigSettingsState struct {
	Items []ConfigSettingView
}

type ConfigSettingUpdate struct {
	ID    string
	Value string
}

type ConfigSettingsApplyResult struct {
	Updated []ConfigSettingView
	Path    string
}

func (a *App) ConfigSettings() (ConfigSettingsState, error) {
	if a == nil {
		return ConfigSettingsState{}, fmt.Errorf("app unavailable")
	}
	loaded, err := LoadConfigFiles(a.cfg.DataDir, a.workspaceRoot)
	if err != nil {
		return ConfigSettingsState{}, err
	}
	return configSettingsState(a.cfg, loaded), nil
}

func (a *App) ApplyConfigSettings(updates []ConfigSettingUpdate) (ConfigSettingsApplyResult, error) {
	if a == nil {
		return ConfigSettingsApplyResult{}, fmt.Errorf("app unavailable")
	}
	updates = normalizeConfigSettingUpdates(updates)
	if len(updates) == 0 {
		return ConfigSettingsApplyResult{}, nil
	}
	path := ProjectLocalConfigPath(a.workspaceRoot)
	file, _, err := LoadConfigFile(path)
	if err != nil {
		return ConfigSettingsApplyResult{}, err
	}
	for _, update := range updates {
		value, err := parseConfigSettingBool(update.Value)
		if err != nil {
			return ConfigSettingsApplyResult{}, fmt.Errorf("%s: %w", update.ID, err)
		}
		switch update.ID {
		case ConfigSettingWorkflowsEnabled:
			file.Workflows.Enabled = &value
		case ConfigSettingWorkflowKeywordTrigger:
			file.Workflows.KeywordTriggerEnabled = &value
		default:
			return ConfigSettingsApplyResult{}, fmt.Errorf("unknown config setting: %s", update.ID)
		}
	}
	if err := SaveConfigFile(path, file); err != nil {
		return ConfigSettingsApplyResult{}, err
	}
	loaded, err := LoadAndApplyConfig(Config{DataDir: a.cfg.DataDir}, a.workspaceRoot)
	if err != nil {
		return ConfigSettingsApplyResult{}, err
	}
	workflowsEnabledChanged := a.cfg.WorkflowsEnabled != loaded.WorkflowsEnabled
	a.cfg.WorkflowsEnabled = loaded.WorkflowsEnabled
	a.cfg.WorkflowKeywordTrigger = loaded.WorkflowKeywordTrigger
	a.cfg.TrustedWorkflows = append([]string(nil), loaded.TrustedWorkflows...)
	a.toolMu.Lock()
	if err := a.rebuildTaskRuntimeLocked(); err != nil {
		a.toolMu.Unlock()
		return ConfigSettingsApplyResult{}, err
	}
	a.toolMu.Unlock()
	if err := a.refreshMCPTools(); err != nil {
		return ConfigSettingsApplyResult{}, err
	}
	a.resetAgent()
	if workflowsEnabledChanged {
		a.recordWorkflowsEnabledChanged(a.cfg.WorkflowsEnabled)
	}
	state, err := a.ConfigSettings()
	if err != nil {
		return ConfigSettingsApplyResult{}, err
	}
	updated := make([]ConfigSettingView, 0, len(updates))
	for _, update := range updates {
		for _, item := range state.Items {
			if item.ID == update.ID {
				updated = append(updated, item)
				break
			}
		}
	}
	return ConfigSettingsApplyResult{Updated: updated, Path: path}, nil
}

// recordWorkflowsEnabledChanged appends a hidden marker so the model stops
// trusting workflow tool results that predate the configuration change. The
// marker is append-only, so the cached history prefix is unaffected.
func (a *App) recordWorkflowsEnabledChanged(enabled bool) {
	if a == nil || a.msgStore == nil {
		return
	}
	state, stale := "disabled", "enabled"
	if enabled {
		state, stale = "enabled", "disabled"
	}
	text := fmt.Sprintf("<config_changed>\nDynamic workflows are now %s in Whale. Treat earlier statements or workflow tool results claiming they are %s as stale.\n</config_changed>", state, stale)
	_, _ = a.msgStore.Create(context.Background(), core.Message{
		SessionID:    a.sessionID,
		Role:         core.RoleUser,
		Text:         text,
		Hidden:       true,
		FinishReason: core.FinishReasonEndTurn,
	})
}

func configSettingsState(cfg Config, loaded LoadedConfig) ConfigSettingsState {
	def := DefaultConfig()
	return ConfigSettingsState{Items: []ConfigSettingView{
		{
			ID:          ConfigSettingWorkflowsEnabled,
			Label:       "Dynamic workflows",
			Description: "Enable the workflow runtime, tool, catalog, and run panel integration.",
			Type:        ConfigSettingBool,
			Value:       strconv.FormatBool(cfg.WorkflowsEnabled),
			Default:     strconv.FormatBool(def.WorkflowsEnabled),
			Scope:       "project local",
			Source:      workflowEnabledSource(loaded),
		},
		{
			ID:          ConfigSettingWorkflowKeywordTrigger,
			Label:       "Workflow keyword trigger",
			Description: "Let named workflow catalog hints encourage automatic workflow use.",
			Type:        ConfigSettingBool,
			Value:       strconv.FormatBool(cfg.WorkflowKeywordTrigger),
			Default:     strconv.FormatBool(def.WorkflowKeywordTrigger),
			Scope:       "project local",
			Source:      workflowKeywordTriggerSource(loaded),
		},
	}}
}

func normalizeConfigSettingUpdates(updates []ConfigSettingUpdate) []ConfigSettingUpdate {
	out := make([]ConfigSettingUpdate, 0, len(updates))
	for _, update := range updates {
		id := strings.ToLower(strings.TrimSpace(update.ID))
		if id == "" {
			continue
		}
		out = append(out, ConfigSettingUpdate{ID: id, Value: strings.TrimSpace(update.Value)})
	}
	return out
}

func parseConfigSettingBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "yes", "y", "on", "enabled":
		return true, nil
	case "0", "f", "false", "no", "n", "off", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool value %q", value)
	}
}

func workflowEnabledSource(loaded LoadedConfig) string {
	if loaded.ProjectLocal.Workflows.Enabled != nil {
		return "project local"
	}
	if loaded.Project.Workflows.Enabled != nil {
		return "project"
	}
	if loaded.Global.Workflows.Enabled != nil {
		return "global"
	}
	return "default"
}

func workflowKeywordTriggerSource(loaded LoadedConfig) string {
	if loaded.ProjectLocal.Workflows.KeywordTriggerEnabled != nil {
		return "project local"
	}
	if loaded.Project.Workflows.KeywordTriggerEnabled != nil {
		return "project"
	}
	if loaded.Global.Workflows.KeywordTriggerEnabled != nil {
		return "global"
	}
	return "default"
}
