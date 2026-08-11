package app

import (
	"bytes"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/plugins"
	"github.com/usewhale/whale/internal/securefs"
	"github.com/usewhale/whale/internal/store"
	"os"
	"path/filepath"
	"strings"
)

const (
	ConfigFileName      = "config.toml"
	LocalConfigFileName = "config.local.toml"
)

type FileConfig struct {
	Provider        string `toml:"provider,omitempty"`
	Model           string `toml:"model,omitempty"`
	ReasoningEffort string `toml:"reasoning_effort,omitempty"`
	ThinkingEnabled *bool  `toml:"thinking_enabled,omitempty"`

	Permissions  FilePermissionsConfig         `toml:"permissions,omitempty"`
	UI           FileUIConfig                  `toml:"ui,omitempty"`
	API          FileAPIConfig                 `toml:"api,omitempty"`
	Providers    FileProvidersConfig           `toml:"providers,omitempty"`
	Retry        FileRetryConfig               `toml:"retry,omitempty"`
	Shell        FileShellConfig               `toml:"shell,omitempty"`
	Experimental FileExperimentalConfig        `toml:"experimental,omitempty"`
	Tasks        FileTasksConfig               `toml:"tasks,omitempty"`
	Budget       FileBudgetConfig              `toml:"budget,omitempty"`
	MCP          FileMCPConfig                 `toml:"mcp,omitempty"`
	Context      FileContextConfig             `toml:"context,omitempty"`
	ProjectDoc   FileProjectDocConfig          `toml:"project_doc,omitempty"`
	Skills       FileSkillsConfig              `toml:"skills,omitempty"`
	Plugins      FilePluginsConfig             `toml:"plugins,omitempty"`
	Workflows    FileWorkflowsConfig           `toml:"workflows,omitempty"`
	Lsp          FileLSPConfig                 `toml:"lsp,omitempty"`
	AutoReview   FileAutoReviewConfig          `toml:"auto_review,omitempty"`
	Hooks        map[string][]agent.HookConfig `toml:"hooks,omitempty"`
}

type FileUIConfig struct {
	ViewMode                string `toml:"view_mode,omitempty"`
	ShowReasoning           *bool  `toml:"show_reasoning,omitempty"`
	CheckForUpdateOnStartup *bool  `toml:"check_for_update_on_startup,omitempty"`
}

type FilePermissionsConfig struct {
	Default           string            `toml:"default,omitempty"`
	AutoAccept        *bool             `toml:"auto_accept,omitempty"`
	Read              map[string]string `toml:"read,omitempty"`
	Edit              map[string]string `toml:"edit,omitempty"`
	Shell             map[string]string `toml:"shell,omitempty"`
	Terminal          map[string]string `toml:"terminal,omitempty"`
	ExternalDirectory map[string]string `toml:"external_directory,omitempty"`
	MCP               map[string]string `toml:"mcp,omitempty"`
	Memory            map[string]string `toml:"memory,omitempty"`
	Task              map[string]string `toml:"task,omitempty"`
	WebSearch         map[string]string `toml:"web_search,omitempty"`
	WebFetch          map[string]string `toml:"web_fetch,omitempty"`
	MutatingTool      map[string]string `toml:"mutating_tool,omitempty"`
}

type FileAPIConfig struct {
	BaseURL string `toml:"base_url,omitempty"`
}

type FileProvidersConfig struct {
	DeepSeek FileDeepSeekProviderConfig `toml:"deepseek,omitempty"`
	MiniMax  FileMiniMaxProviderConfig  `toml:"minimax,omitempty"`
}

type FileDeepSeekProviderConfig struct {
	Multimodal FileMultimodalProviderConfig `toml:"multimodal,omitempty"`
	WebSearch  string                       `toml:"web_search,omitempty"`
	API        string                       `toml:"api,omitempty"`
}

type FileMiniMaxProviderConfig struct {
	Region    string `toml:"region,omitempty"`
	BaseURL   string `toml:"base_url,omitempty"`
	APIKey    string `toml:"api_key,omitempty"`
	APIKeyEnv string `toml:"api_key_env,omitempty"`
}

type FileMultimodalProviderConfig struct {
	Enabled   *bool  `toml:"enabled,omitempty"`
	Compat    string `toml:"compat,omitempty"`
	BaseURL   string `toml:"base_url,omitempty"`
	APIKey    string `toml:"api_key,omitempty"`
	APIKeyEnv string `toml:"api_key_env,omitempty"`
	Model     string `toml:"model,omitempty"`
}

type FileRetryConfig struct {
	MaxAttempts       *int   `toml:"max_attempts,omitempty"`
	StreamMaxAttempts *int   `toml:"stream_max_attempts,omitempty"`
	StreamIdleTimeout string `toml:"stream_idle_timeout,omitempty"`
	MaxDelay          string `toml:"max_delay,omitempty"`
}

type FileShellConfig struct {
	ForegroundWaitDefaultMS *int `toml:"foreground_wait_default_ms,omitempty"`
	ForegroundWaitMaxMS     *int `toml:"foreground_wait_max_ms,omitempty"`
}

type FileExperimentalConfig struct {
	DeepSeekPrefixCompletion *bool `toml:"deepseek_prefix_completion,omitempty"`
}

type FileTasksConfig struct {
	MaxParallelSubagents *int `toml:"max_parallel_subagents,omitempty"`
}

type FileBudgetConfig struct {
	SessionLimitUSD *float64 `toml:"session_limit_usd,omitempty"`
}

type FileMCPConfig struct {
	ConfigPath string `toml:"config_path,omitempty"`
}

type FileContextConfig struct {
	AutoCompact      *bool    `toml:"auto_compact,omitempty"`
	CompactThreshold *float64 `toml:"compact_threshold,omitempty"`
}

type FileProjectDocConfig struct {
	Enabled           *bool    `toml:"enabled,omitempty"`
	MaxBytes          *int     `toml:"max_bytes,omitempty"`
	FallbackFilenames []string `toml:"fallback_filenames,omitempty"`
}

type FileSkillsConfig struct {
	Enabled  []string `toml:"enabled,omitempty"`
	Disabled []string `toml:"disabled,omitempty"`
}

type FilePluginsConfig map[string]FilePluginConfig

type FilePluginConfig struct {
	Enabled    *bool                              `toml:"enabled,omitempty"`
	MCPServers map[string]plugins.MCPServerConfig `toml:"mcp_servers,omitempty"`
}

func (c FilePluginsConfig) RuntimeConfig() plugins.ConfigMap {
	if len(c) == 0 {
		return nil
	}
	out := plugins.ConfigMap{}
	for id, cfg := range c {
		id = plugins.NormalizePluginID(id)
		if id == "" {
			continue
		}
		runtimeCfg := plugins.Config{MCPServers: clonePluginMCPServers(cfg.MCPServers)}
		if cfg.Enabled != nil {
			enabled := *cfg.Enabled
			runtimeCfg.Enabled = &enabled
		}
		out[id] = runtimeCfg
	}
	return out
}

func FilePluginsFromRuntime(in plugins.ConfigMap) FilePluginsConfig {
	if len(in) == 0 {
		return nil
	}
	out := FilePluginsConfig{}
	for id, cfg := range in {
		id = plugins.NormalizePluginID(id)
		if id != "" {
			fileCfg := FilePluginConfig{MCPServers: clonePluginMCPServers(cfg.MCPServers)}
			if cfg.Enabled != nil {
				enabled := *cfg.Enabled
				fileCfg.Enabled = &enabled
			}
			out[id] = fileCfg
		}
	}
	return out
}

func clonePluginMCPServers(in map[string]plugins.MCPServerConfig) map[string]plugins.MCPServerConfig {
	if len(in) == 0 {
		return nil
	}
	out := map[string]plugins.MCPServerConfig{}
	for name, cfg := range in {
		name = plugins.NormalizePluginID(name)
		if name == "" {
			continue
		}
		cp := cfg
		cp.DisabledTools = append([]string(nil), cfg.DisabledTools...)
		out[name] = cp
	}
	return out
}

type FileWorkflowsConfig struct {
	Enabled               *bool    `toml:"enabled,omitempty"`
	KeywordTriggerEnabled *bool    `toml:"keyword_trigger_enabled,omitempty"`
	Trusted               []string `toml:"trusted,omitempty"`
}

// FileLSPConfig toggles LSP code intelligence. Server definitions live in
// the separate lsp.json (see internal/lsp); this only gates the feature.
type FileLSPConfig struct {
	Enabled *bool `toml:"enabled,omitempty"`
}

type FileAutoReviewConfig struct {
	Enabled     *bool    `toml:"enabled,omitempty"`
	Model       string   `toml:"model,omitempty"`
	TimeoutMS   int      `toml:"timeout_ms,omitempty"`
	AllowRules  []string `toml:"allow_rules,omitempty"`
	DenyRules   []string `toml:"deny_rules,omitempty"`
	Environment []string `toml:"environment,omitempty"`
}

type LoadedConfig struct {
	Global             FileConfig
	GlobalLoaded       bool
	GlobalPath         string
	Project            FileConfig
	ProjectLoaded      bool
	ProjectPath        string
	ProjectLocal       FileConfig
	ProjectLocalLoaded bool
	ProjectLocalPath   string
}

func GlobalConfigPath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = store.DefaultDataDir()
	}
	return filepath.Join(dataDir, ConfigFileName)
}

func ProjectConfigPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".whale", ConfigFileName)
}

func ProjectLocalConfigPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".whale", LocalConfigFileName)
}

func LoadConfigFiles(dataDir, workspaceRoot string) (LoadedConfig, error) {
	globalPath := GlobalConfigPath(dataDir)
	global, globalLoaded, err := LoadConfigFile(globalPath)
	if err != nil {
		return LoadedConfig{}, err
	}
	projectPath := ProjectConfigPath(workspaceRoot)
	project, projectLoaded, err := LoadConfigFile(projectPath)
	if err != nil {
		return LoadedConfig{}, err
	}
	projectLocalPath := ProjectLocalConfigPath(workspaceRoot)
	projectLocal, projectLocalLoaded, err := LoadConfigFile(projectLocalPath)
	if err != nil {
		return LoadedConfig{}, err
	}
	return LoadedConfig{
		Global:             global,
		GlobalLoaded:       globalLoaded,
		GlobalPath:         globalPath,
		Project:            project,
		ProjectLoaded:      projectLoaded,
		ProjectPath:        projectPath,
		ProjectLocal:       projectLocal,
		ProjectLocalLoaded: projectLocalLoaded,
		ProjectLocalPath:   projectLocalPath,
	}, nil
}

func LoadConfigFile(path string) (FileConfig, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileConfig{}, false, nil
		}
		return FileConfig{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := checkRemovedPluginListKeys(path, b); err != nil {
		return FileConfig{}, false, err
	}
	var cfg FileConfig
	meta, err := toml.Decode(string(b), &cfg)
	if err != nil {
		return FileConfig{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := checkRemovedConfigKeys(path, meta); err != nil {
		return FileConfig{}, false, err
	}
	return cfg, true, nil
}

// removedConfigKeys maps pre-v0.1.9 permission keys, which no longer have a
// migration, to the modern setting that replaces them. Both the [permissions]
// table form and the original top-level form are listed.
var removedConfigKeys = map[string]string{
	"permissions.mode":                 "set permissions.default and the per-tool [permissions.*] tables instead",
	"permissions.allow_shell_prefixes": "add [permissions.shell] entries with an \"allow\" action instead",
	"permissions.deny_shell_prefixes":  "add [permissions.shell] entries with a \"deny\" action instead",
	"allow_shell_prefixes":             "add [permissions.shell] entries with an \"allow\" action instead",
	"deny_shell_prefixes":              "add [permissions.shell] entries with a \"deny\" action instead",
	"plugins.enabled":                  "replace it with [plugins.<id>] enabled = true",
	"plugins.disabled":                 "replace it with [plugins.<id>] enabled = false",
}

// checkRemovedConfigKeys rejects configs that still carry legacy permission
// keys. They decode without error but are silently dropped, so a user relying
// on a legacy deny would lose that protection without warning; failing loudly
// forces an explicit migration instead.
func checkRemovedConfigKeys(path string, meta toml.MetaData) error {
	for _, key := range meta.Undecoded() {
		if hint, ok := removedConfigKeys[key.String()]; ok {
			if strings.HasPrefix(key.String(), "permissions.") || key.String() == "allow_shell_prefixes" || key.String() == "deny_shell_prefixes" {
				return fmt.Errorf("config %s uses removed permission key %q; %s", path, key.String(), hint)
			}
			return fmt.Errorf("config %s uses removed config key %q; %s", path, key.String(), hint)
		}
	}
	return nil
}

func checkRemovedPluginListKeys(path string, b []byte) error {
	var raw map[string]any
	if _, err := toml.Decode(string(b), &raw); err != nil {
		return nil
	}
	table, ok := raw["plugins"].(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range []string{"enabled", "disabled"} {
		if _, ok := table[key]; ok {
			hint := removedConfigKeys["plugins."+key]
			return fmt.Errorf("config %s uses removed config key %q; %s", path, "plugins."+key, hint)
		}
	}
	return nil
}

func SaveConfigFile(path string, cfg FileConfig) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := securefs.WritePrivateFile(path, buf.Bytes()); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
