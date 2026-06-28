package app

import (
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/agent"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/plugins"
)

func SetPluginEnabledConfig(cfg Config, workspaceRoot, id string, enabled bool) (Config, string, error) {
	id = plugins.NormalizePluginID(id)
	if id == "" {
		return cfg, "", fmt.Errorf("plugin id must not be empty")
	}
	pm := plugins.NewManager(plugins.Context{DataDir: cfg.DataDir, WorkspaceRoot: workspaceRoot}, cfg.Plugins)
	st, ok := pm.Status(id)
	if !ok {
		return cfg, "", fmt.Errorf("plugin not found: %s", id)
	}
	id = st.Manifest.ID
	path := ProjectLocalConfigPath(workspaceRoot)
	file, _, err := LoadConfigFile(path)
	if err != nil {
		return cfg, "", err
	}
	if file.Plugins == nil {
		file.Plugins = FilePluginsConfig{}
	}
	enabledValue := enabled
	pluginConfig := file.Plugins[id]
	pluginConfig.Enabled = &enabledValue
	file.Plugins[id] = pluginConfig
	if err := SaveConfigFile(path, file); err != nil {
		return cfg, "", err
	}
	loaded, err := LoadAndApplyConfig(Config{DataDir: cfg.DataDir}, workspaceRoot)
	if err != nil {
		return cfg, "", err
	}
	msg := fmt.Sprintf("disabled plugin: %s\nconfig: %s", id, path)
	if enabled {
		msg = fmt.Sprintf("enabled plugin: %s\nconfig: %s", id, path)
	}
	return loaded, msg, nil
}

func (a *App) SetPluginEnabled(id string, enabled bool) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("plugin id must not be empty")
	}
	if a == nil || a.pluginManager == nil {
		return "", fmt.Errorf("plugins unavailable")
	}
	cfg, msg, err := SetPluginEnabledConfig(a.cfg, a.workspaceRoot, id, enabled)
	if err != nil {
		return "", err
	}
	a.cfg.Plugins = cfg.Plugins
	pm := plugins.NewManager(plugins.Context{DataDir: a.cfg.DataDir, WorkspaceRoot: a.workspaceRoot}, a.cfg.Plugins)
	outcome := pm.Outcome()
	allHooks := append([]agent.ResolvedHook{}, a.hooks...)
	allHooks = append(allHooks, outcome.CommandHooks...)
	hookRunner := agent.NewHookRunnerWithState(allHooks, a.workspaceRoot, a.hookStates)
	hookRunner.AddHandlers(outcome.HookHandlers...)
	mcpManager, err := newMCPManagerForPlugins(a.cfg, a.workspaceRoot, outcome)
	if err != nil {
		return "", err
	}
	a.mcpInitMu.Lock()
	mcpWasStarted := a.mcpInitStarted
	a.mcpInitStarted = false
	a.mcpInitMu.Unlock()
	if a.mcpManager != nil {
		_ = a.mcpManager.Close()
	}
	a.toolMu.Lock()
	a.pluginManager = pm
	a.mcpManager = mcpManager
	a.pluginTools = outcome.Tools
	a.pluginAgents = outcome.Agents
	if a.toolset != nil {
		a.toolset.SetExtraSkills(outcome.Skills)
	}
	a.hookRunner = hookRunner
	if err := a.rebuildTaskRuntimeLocked(); err != nil {
		a.toolMu.Unlock()
		return "", err
	}
	a.toolMu.Unlock()
	if mcpWasStarted {
		a.InitializeMCP(a.ctx, nil)
	}
	if err := a.refreshMCPTools(); err != nil {
		return "", err
	}
	a.resetAgent()
	return msg, nil
}

func newMCPManagerForPlugins(cfg Config, workspaceRoot string, outcome plugins.LoadOutcome) (*whalemcp.Manager, error) {
	mcpConfigPath := strings.TrimSpace(cfg.MCPConfigPath)
	if mcpConfigPath == "" {
		mcpConfigPath = whalemcp.DefaultConfigPath(cfg.DataDir)
	}
	mcpConfig, err := whalemcp.LoadConfig(mcpConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load mcp config: %w", err)
	}
	mergePluginMCPServers(&mcpConfig, outcome.MCPServers)
	return whalemcp.NewManager(mcpConfig, workspaceRoot), nil
}
