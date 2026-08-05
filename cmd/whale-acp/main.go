package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/usewhale/whale/internal/acp"
	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm/deepseek"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/store"
	"github.com/usewhale/whale/internal/tools"
)

func main() {
	acp.Logger = log.New(os.Stderr, "[whale-acp] ", log.LstdFlags)
	acp.Logger.Printf("starting whale-acp (ACP v%d)", acp.ProtocolVersion)

	dataDir := os.Getenv("WHALE_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			dataDir = filepath.Join(".whale")
		} else {
			dataDir = filepath.Join(home, ".whale")
		}
	}

	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		apiKey = loadAPIKeyFromCredentials(dataDir)
	}
	if apiKey == "" {
		acp.Logger.Fatalf("DEEPSEEK_API_KEY not set and no credentials found at %s", dataDir)
	}

	workspaceRoot := os.Getenv("WHALE_WORKSPACE")
	if workspaceRoot == "" {
		var err error
		workspaceRoot, err = os.Getwd()
		if err != nil {
			acp.Logger.Fatalf("cannot determine workspace: %v", err)
		}
	}

	provider, err := deepseek.New(
		deepseek.WithAPIKey(apiKey),
		deepseek.WithModel("deepseek-chat"),
	)
	if err != nil {
		acp.Logger.Fatalf("failed to create provider: %v", err)
	}

	sessionsDir := os.Getenv("WHALE_SESSIONS_DIR")
	if sessionsDir == "" {
		sessionsDir = filepath.Join(dataDir, "sessions-acp")
	}
	msgStore, err := store.NewJSONLStore(sessionsDir)
	if err != nil {
		acp.Logger.Fatalf("failed to create message store: %v", err)
	}

	transport := acp.NewTransport()
	approvalFn := acp.NewACPApprovalFunc(transport)

	// newRuntime builds an agent+toolset scoped to a single session's cwd. Each
	// session gets its own runtime so prompts in different sessions run
	// concurrently and a prompt waiting on a permission dialog only blocks its
	// own session, not every other one sharing the process.
	newRuntime := func(acpSessionID, cwd string, mcps []acp.MCPServer) (*acp.SessionRuntime, error) {
		ts, err := tools.NewToolset(cwd)
		if err != nil {
			return nil, fmt.Errorf("create toolset: %w", err)
		}
		ts.SetWorktreeContext(cwd, cwd)
		p := loadPermissionPolicy(dataDir, cwd)
		sessionPolicy := policy.RulePolicy{
			Default:       p.Default,
			Rules:         p.Rules,
			WorkspaceRoot: cwd,
			WorktreeRoot:  cwd,
		}
		ts.SetExecBoundaryPolicy(sessionPolicy)
		sid := acpSessionID
		ts.SetExecBoundaryApproval(func() string { return sid }, approvalFn)
		mcpManager, reg := wireMCPServers(ts, dataDir, cwd, mcps)
		whaleAgent := agent.NewAgentWithRegistry(
			provider,
			msgStore,
			reg,
			agent.WithMaxToolIters(100),
			agent.WithApprovalFunc(approvalFn),
			agent.WithToolPolicy(sessionPolicy),
		)
		acp.Logger.Printf("session runtime ready: acp=%s cwd=%s tools=%d", acpSessionID, cwd, len(reg.Tools()))
		return &acp.SessionRuntime{
			Agent:   whaleAgent,
			Toolset: ts,
			Close: func() {
				if mcpManager != nil {
					if err := mcpManager.Close(); err != nil {
						acp.Logger.Printf("mcp close: %v", err)
					}
				}
			},
		}, nil
	}

	handler := acp.NewHandler(transport, msgStore, workspaceRoot)
	handler.SetRuntimeFactory(newRuntime)
	handler.SetSessionsDir(sessionsDir)

	acp.Logger.Printf("ready, waiting for ACP messages on stdin")

	err = handler.Run()
	// Close MCP server sessions before exiting — os.Exit below skips defers.
	handler.CloseSessions()
	if err != nil {
		if err == context.Canceled || err.Error() == "EOF" || err.Error() == "stdin read: EOF" {
			acp.Logger.Printf("shutting down normally")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func loadAPIKeyFromCredentials(dataDir string) string {
	path := filepath.Join(dataDir, "credentials.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var creds struct {
		DeepSeekAPIKey string `json:"deepseek_api_key,omitempty"`
	}
	if err := json.Unmarshal(b, &creds); err != nil {
		return ""
	}
	return strings.TrimSpace(creds.DeepSeekAPIKey)
}

type permFile struct {
	Permissions struct {
		Default           string            `toml:"default,omitempty"`
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
	} `toml:"permissions"`
}

// loadPermissionPolicy merges global → local → project config layers, then
// produces a RulePolicy. Defaults are prepended so user rules have higher
// precedence in the non-shell reverse evaluator.
func loadPermissionPolicy(dataDir, workspaceRoot string) policy.RulePolicy {
	// Merge layers: global, project-local, project config.local
	configPaths := []string{
		filepath.Join(dataDir, "config.toml"),
		filepath.Join(workspaceRoot, ".whale", "config.toml"),
		filepath.Join(workspaceRoot, ".whale", "config.local.toml"),
	}
	merged := permFile{}
	for _, path := range configPaths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg permFile
		if _, err := toml.Decode(string(b), &cfg); err != nil {
			continue
		}
		mergePermissions(&merged, &cfg)
	}

	perm := merged.Permissions
	defaultPerm := policy.PermissionAllow
	switch strings.ToLower(perm.Default) {
	case "deny":
		defaultPerm = policy.PermissionDeny
	case "ask":
		defaultPerm = policy.PermissionAsk
	}

	// Build user rules per category so a single malformed value only drops the
	// rules for that one category — not every user rule. Discarding the whole
	// user policy on one typo would silently revert to the permissive defaults
	// (edit/shell "*"=allow), which is more permissive than the user intended.
	categories := []struct {
		name  string
		rules map[string]string
	}{
		{"read", perm.Read},
		{"edit", perm.Edit},
		{"shell", perm.Shell},
		{"terminal", perm.Terminal},
		{"external_directory", perm.ExternalDirectory},
		{"mcp", perm.MCP},
		{"memory", perm.Memory},
		{"task", perm.Task},
		{"web_search", perm.WebSearch},
		{"web_fetch", perm.WebFetch},
		{"mutating_tool", perm.MutatingTool},
	}
	var userRules []policy.PermissionRule
	for _, c := range categories {
		catRules, err := policy.RulesFromMap(c.name, c.rules)
		if err != nil {
			acp.Logger.Printf("invalid permission rules in %q, skipping that category: %v", c.name, err)
			continue
		}
		userRules = append(userRules, catRules...)
	}

	// Default rules first, user rules last. The non-shell evaluator iterates
	// in reverse (last-match-wins), so later rules take precedence.
	rules := append([]policy.PermissionRule{}, policy.DefaultRules()...)
	rules = append(rules, userRules...)

	acp.Logger.Printf("loaded permission policy (default=%s, defaults=%d, user=%d)",
		defaultPerm, len(policy.DefaultRules()), len(userRules))
	return policy.RulePolicy{Default: defaultPerm, Rules: rules, WorkspaceRoot: workspaceRoot}
}

func mergePermissions(dst, src *permFile) {
	if src.Permissions.Default != "" {
		dst.Permissions.Default = src.Permissions.Default
	}
	for _, m := range []struct {
		dst *map[string]string
		src map[string]string
	}{
		{&dst.Permissions.Read, src.Permissions.Read},
		{&dst.Permissions.Edit, src.Permissions.Edit},
		{&dst.Permissions.Shell, src.Permissions.Shell},
		{&dst.Permissions.Terminal, src.Permissions.Terminal},
		{&dst.Permissions.ExternalDirectory, src.Permissions.ExternalDirectory},
		{&dst.Permissions.MCP, src.Permissions.MCP},
		{&dst.Permissions.Memory, src.Permissions.Memory},
		{&dst.Permissions.Task, src.Permissions.Task},
		{&dst.Permissions.WebSearch, src.Permissions.WebSearch},
		{&dst.Permissions.WebFetch, src.Permissions.WebFetch},
		{&dst.Permissions.MutatingTool, src.Permissions.MutatingTool},
	} {
		if *m.dst == nil {
			*m.dst = map[string]string{}
		}
		for k, v := range m.src {
			(*m.dst)[k] = v
		}
	}
}

// wireMCPServers loads the session MCP config, connects the configured
// servers, and configures the toolset's deferred MCP catalog so the agent can
// discover and promote mcp__<server>__<tool> tools via tool_search. MCP tools
// are deliberately NOT registered eagerly: the session schema carries only
// tool_search (plus the <available-deferred-tools> block) until the model
// selects specific tools — matching the main app and bench/deferred_compare.
// Returns the manager (for shutdown) and the session tool registry (promoted
// tools are added to it lazily). The registry is never nil.
func wireMCPServers(ts *tools.Toolset, dataDir, cwd string, mcps []acp.MCPServer) (*whalemcp.Manager, *core.ToolRegistry) {
	mcpCfg, err := mcpConfigForSession(dataDir, mcps)
	if err != nil {
		acp.Logger.Printf("mcp config: %v", err)
		mcpCfg = whalemcp.Config{Servers: map[string]whalemcp.ServerConfig{}}
	}
	var mcpManager *whalemcp.Manager
	var reg *core.ToolRegistry
	if len(mcpCfg.Servers) > 0 {
		mcpManager = whalemcp.NewManager(mcpCfg, cwd)
		mcpManager.Initialize(context.Background())
		for _, st := range mcpManager.States() {
			switch st.Status {
			case whalemcp.StatusFailed, whalemcp.StatusCancelled:
				acp.Logger.Printf("mcp server %s: %s (%s)", st.Name, st.Status, st.Error)
			default:
				acp.Logger.Printf("mcp server %s: %s (%d tools)", st.Name, st.Status, len(st.ToolNames))
			}
		}
		if catalog := mcpManager.BuildDeferredCatalog(); catalog != nil && !catalog.Empty() {
			ts.SetDeferredToolSearch(
				&deferredCatalogAdapter{c: catalog},
				func(names []string) ([]core.ToolSpec, error) {
					built, err := mcpManager.BuildTools(names)
					if err != nil {
						return nil, err
					}
					if err := reg.AddTools(built); err != nil {
						return nil, err
					}
					specs := make([]core.ToolSpec, len(built))
					for i, t := range built {
						specs[i] = core.DescribeTool(t)
					}
					return specs, nil
				},
				func() string { return whalemcp.RenderAvailableDeferredTools(catalog) },
			)
		}
	}
	// The registry must be built after SetDeferredToolSearch so tool_search is
	// included in the schema when the deferred catalog is non-empty.
	reg = core.NewToolRegistry(ts.Tools())
	return mcpManager, reg
}

// mcpConfigForSession returns the MCP server config for a session: the local
// ~/.whale/mcp.json baseline (or $WHALE_HOME/mcp.json) merged with the
// servers the ACP client supplied on session/new / session/load.
// Client-supplied servers win on name conflicts — the client is authoritative
// for its own session.
func mcpConfigForSession(dataDir string, mcps []acp.MCPServer) (whalemcp.Config, error) {
	cfg, err := whalemcp.LoadConfig(whalemcp.DefaultConfigPath(dataDir))
	if err != nil {
		return cfg, err
	}
	for _, m := range mcps {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		if kind := clientMCPServerTransport(m); kind != "stdio" {
			// We advertise mcpCapabilities {http:false, sse:false} — stdio only.
			acp.Logger.Printf("mcp server %s: unsupported transport %q (stdio only), skipping", name, kind)
			continue
		}
		cfg.Servers[name] = whalemcp.ServerConfig{
			Name:    name,
			Type:    strings.TrimSpace(m.Type),
			Command: m.Command,
			Args:    m.Args,
			Env:     envVariableMap(m.Env),
			URL:     m.URL,
		}
	}
	return cfg, nil
}

// envVariableMap converts ACP's env array ({name,value} objects) into the map
// form the whale MCP manager expects.
func envVariableMap(envs []acp.EnvVariable) map[string]string {
	if len(envs) == 0 {
		return nil
	}
	out := make(map[string]string, len(envs))
	for _, e := range envs {
		if strings.TrimSpace(e.Name) != "" {
			out[e.Name] = e.Value
		}
	}
	return out
}

// clientMCPServerTransport returns the transport kind of a client-supplied
// MCP server. whale-acp advertises mcpCapabilities {http:false, sse:false},
// so only stdio servers (command + args + env) are accepted.
func clientMCPServerTransport(m acp.MCPServer) string {
	switch strings.ToLower(strings.TrimSpace(m.Type)) {
	case "http", "streamable-http", "streamable_http", "streamablehttp":
		return "http"
	case "sse":
		return "sse"
	}
	if strings.TrimSpace(m.URL) != "" {
		return "http"
	}
	return "stdio"
}

// deferredCatalogAdapter adapts mcp.DeferredToolCatalog to the tools package's
// DeferredToolCatalog interface consumed by tool_search.
type deferredCatalogAdapter struct {
	c *whalemcp.DeferredToolCatalog
}

func (a *deferredCatalogAdapter) Empty() bool { return a.c.Empty() }

func (a *deferredCatalogAdapter) Search(query string) []tools.DeferredToolEntry {
	results := a.c.Search(query)
	out := make([]tools.DeferredToolEntry, len(results))
	for i, r := range results {
		out[i] = tools.DeferredToolEntry{Name: r.Name, Server: r.Server, Description: r.Description}
	}
	return out
}

func (a *deferredCatalogAdapter) Names() []string { return a.c.Names() }
