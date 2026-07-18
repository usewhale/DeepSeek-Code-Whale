package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/usewhale/whale/internal/core"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/tools"
)

func (a *App) InitializeMCP(ctx context.Context, emit func(whalemcp.StartupEvent)) {
	if a == nil || a.mcpManager == nil {
		return
	}
	a.mcpInitMu.Lock()
	if a.mcpInitStarted {
		a.mcpInitMu.Unlock()
		return
	}
	a.mcpInitStarted = true
	a.mcpInitMu.Unlock()

	a.mcpManager.InitializeWithEvents(ctx, func(ev whalemcp.StartupEvent) {
		if ev.State.Connected || ev.Complete {
			_ = a.refreshMCPTools()
		}
		if ev.Complete {
			a.freezeMCPToolSignature()
			if err := a.RestorePromotedTools(); err != nil {
				// Non-fatal: restored tools couldn't be promoted, agent can still use tool_search.
				fmt.Fprintf(os.Stderr, "whale: RestorePromotedTools: %v\n", err)
			}
		}
		if emit != nil {
			emit(ev)
		}
	})
}

func (a *App) refreshMCPTools() error {
	if a == nil || a.mcpManager == nil {
		return nil
	}
	a.toolMu.Lock()
	defer a.toolMu.Unlock()

	// Build deferred catalog and wire up tool_search.
	catalog := a.mcpManager.BuildDeferredCatalog()
	a.setupDeferredToolSearchLocked(catalog)

	// Rebuild registries: base tools + tool_search (if catalog non-empty).
	if err := a.rebuildToolRegistriesLocked(); err != nil {
		return err
	}

	// Freeze signature based on the catalog, not individual tools.
	if !catalog.Empty() {
		a.guardDeferredCatalogLocked(catalog)
	}
	return nil
}

// mcpCatalogAdapter adapts mcp.DeferredToolCatalog to tools.DeferredToolCatalog.
type mcpCatalogAdapter struct {
	c *whalemcp.DeferredToolCatalog
}

func (a *mcpCatalogAdapter) Empty() bool { return a.c.Empty() }
func (a *mcpCatalogAdapter) Search(query string) []tools.DeferredToolEntry {
	results := a.c.Search(query)
	out := make([]tools.DeferredToolEntry, len(results))
	for i, r := range results {
		out[i] = tools.DeferredToolEntry{
			Name:        r.Name,
			Server:      r.Server,
			Description: r.Description,
		}
	}
	return out
}
func (a *mcpCatalogAdapter) Names() []string { return a.c.Names() }

// setupDeferredToolSearchLocked configures the toolset with deferred catalog, promoter, and renderer.
func (a *App) setupDeferredToolSearchLocked(catalog *whalemcp.DeferredToolCatalog) {
	if a.toolset == nil {
		return
	}
	var catAdapter tools.DeferredToolCatalog
	if catalog != nil && !catalog.Empty() {
		catAdapter = &mcpCatalogAdapter{c: catalog}
	}

	promoter := a.makeDeferredPromoter()
	renderer := func() string {
		return whalemcp.RenderAvailableDeferredTools(catalog)
	}

	a.toolset.SetDeferredToolSearch(catAdapter, promoter, renderer)
}

const availableDeferredToolsMaxChars = 4000

// renderDeferredToolsBlock returns the <available-deferred-tools> block for
// injection into the system prompt. Returns empty string when no MCP tools are
// registered. The output is capped at availableDeferredToolsMaxChars to avoid
// blowing up the system prompt when many MCP tools are registered.
func (a *App) renderDeferredToolsBlock() string {
	if a.mcpManager == nil {
		return ""
	}
	catalog := a.mcpManager.BuildDeferredCatalog()
	block := whalemcp.RenderAvailableDeferredTools(catalog)
	if len(block) <= availableDeferredToolsMaxChars {
		return block
	}
	// Truncate at a newline boundary near the limit.
	truncated := block[:availableDeferredToolsMaxChars]
	if idx := strings.LastIndex(truncated, "\n"); idx > 0 {
		truncated = truncated[:idx]
	}
	// Count how many tools we omitted.
	allTools := catalog.Names()
	shownCount := 0
	for _, name := range allTools {
		if strings.Contains(truncated, name) {
			shownCount++
		}
	}
	omitted := len(allTools) - shownCount
	return truncated + fmt.Sprintf("\n... %d more tool(s) omitted\n</available-deferred-tools>", omitted)
}

// makeDeferredPromoter returns a function that builds full Tool objects for given names,
// adds them to registries, and returns their specs.
func (a *App) makeDeferredPromoter() tools.DeferredToolPromoter {
	return func(names []string) ([]core.ToolSpec, error) {
		specs, state, err := a.promoteToolsLocked(names)
		if err != nil {
			return nil, err
		}
		// Persist outside the lock to avoid filesystem I/O in the critical section.
		if err := a.writePromotedToolState(state); err != nil {
			// Non-fatal: promotion succeeded, just couldn't persist.
			fmt.Fprintf(os.Stderr, "whale: writePromotedToolState: %v\n", err)
		}
		return specs, nil
	}
}

// promoteToolsLocked builds tools, registers them, and returns specs plus
// the state that should be persisted. Caller must not hold toolMu.
func (a *App) promoteToolsLocked(names []string) ([]core.ToolSpec, promotedToolState, error) {
	a.toolMu.Lock()
	defer a.toolMu.Unlock()

	built, err := a.mcpManager.BuildTools(names)
	if err != nil {
		return nil, promotedToolState{}, err
	}

	if err := a.baseToolRegistry.AddTools(built); err != nil {
		return nil, promotedToolState{}, err
	}

	// Track promoted tools BEFORE rebuild so collectPromotedToolsLocked sees them.
	if a.promotedTools == nil {
		a.promotedTools = make(map[string]bool)
	}
	for _, t := range built {
		a.promotedTools[t.Name()] = true
	}

	if err := a.rebuildToolRegistriesLocked(); err != nil {
		return nil, promotedToolState{}, err
	}
	// Store catalog hash for validation on resume.
	catalog := a.mcpManager.BuildDeferredCatalog()
	a.promotedCatalogHash = catalog.Hash()

	state := promotedToolState{
		CatalogHash: a.promotedCatalogHash,
	}
	for name := range a.promotedTools {
		state.ToolNames = append(state.ToolNames, name)
	}
	sort.Strings(state.ToolNames)

	specs := make([]core.ToolSpec, len(built))
	for i, t := range built {
		specs[i] = core.DescribeTool(t)
	}
	return specs, state, nil
}

// rebuildToolRegistriesLocked rebuilds all three registries from scratch,
// preserving any promoted MCP tools that are already in the base registry.
func (a *App) rebuildToolRegistriesLocked() error {
	// Collect promoted MCP tools from the existing base registry (those not in toolset).
	promoted := a.collectPromotedToolsLocked()

	var base []core.Tool
	if a.toolset != nil {
		base = append(base, a.toolset.Tools()...)
	}
	base = append(base, promoted...)

	if a.baseToolRegistry != nil {
		if err := a.baseToolRegistry.ReplaceTools(base); err != nil {
			return err
		}
	}
	subagent := append([]core.Tool{}, base...)
	subagent = append(subagent, a.pluginTools...)
	if a.subagentToolRegistry != nil {
		if err := a.subagentToolRegistry.ReplaceTools(subagent); err != nil {
			return err
		}
	}
	full := append([]core.Tool{}, subagent...)
	full = append(full, a.taskTools...)
	full = append(full, a.goalTools...)
	full = append(full, a.workflowTools...)
	if a.toolRegistry != nil {
		return a.toolRegistry.ReplaceTools(full)
	}
	return nil
}

// collectPromotedToolsLocked returns promoted MCP tools from the base registry
// that are NOT part of the current toolset output (to avoid duplicates).
// Also prunes promotedTools entries that are no longer in the registry.
func (a *App) collectPromotedToolsLocked() []core.Tool {
	if a.baseToolRegistry == nil || a.promotedTools == nil {
		return nil
	}
	toolsetNames := map[string]bool{}
	if a.toolset != nil {
		for _, t := range a.toolset.Tools() {
			toolsetNames[t.Name()] = true
		}
	}
	var promoted []core.Tool
	var stale []string
	for name := range a.promotedTools {
		if toolsetNames[name] {
			continue // toolset already provides it
		}
		if t := a.baseToolRegistry.Get(name); t != nil {
			promoted = append(promoted, t)
		} else {
			stale = append(stale, name)
		}
	}
	for _, name := range stale {
		delete(a.promotedTools, name)
	}
	return promoted
}

func (a *App) guardDeferredCatalogLocked(catalog *whalemcp.DeferredToolCatalog) {
	next := catalog.Hash()
	if a.mcpSig == "" {
		a.mcpSig = next
		return
	}
	if next == a.mcpSig {
		return
	}
	if !a.mcpSigFrozen {
		a.mcpSig = next
		return
	}
	// Signature changed after freeze — intentionally a no-op.
	// Catalog hash can change when servers reconnect; deferring tools on first
	// use means hash drift is harmless. The agent will discover fresh tools via tool_search.
}

func (a *App) freezeMCPToolSignature() {
	if a == nil {
		return
	}
	a.toolMu.Lock()
	defer a.toolMu.Unlock()
	a.mcpSigFrozen = true
}

// promotedToolState is persisted to the session directory for restore-on-resume.
type promotedToolState struct {
	CatalogHash string   `json:"catalog_hash"`
	ToolNames   []string `json:"tool_names"`
}

func (a *App) writePromotedToolState(state promotedToolState) error {
	if a == nil || a.sessionsDir == "" || a.sessionID == "" {
		return nil
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := filepath.Join(a.sessionsDir, a.sessionID, "promoted_tools.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func (a *App) loadPromotedToolState() ([]string, error) {
	if a == nil || a.sessionsDir == "" || a.sessionID == "" {
		return nil, nil
	}
	path := filepath.Join(a.sessionsDir, a.sessionID, "promoted_tools.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state promotedToolState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	// If catalog hash doesn't match, promoted tools are stale.
	catalog := a.mcpManager.BuildDeferredCatalog()
	if catalog.Hash() != state.CatalogHash {
		return nil, nil // stale — start fresh
	}
	a.promotedCatalogHash = state.CatalogHash
	return state.ToolNames, nil
}

// RestorePromotedTools re-promotes previously promoted tools on session resume.
func (a *App) RestorePromotedTools() error {
	if a == nil || a.mcpManager == nil {
		return nil
	}
	toolNames, err := a.loadPromotedToolState()
	if err != nil || len(toolNames) == 0 {
		return err
	}
	a.toolMu.Lock()
	defer a.toolMu.Unlock()

	built, err := a.mcpManager.BuildTools(toolNames)
	if err != nil {
		return err
	}
	if err := a.baseToolRegistry.AddTools(built); err != nil {
		return err
	}
	if err := a.rebuildToolRegistriesLocked(); err != nil {
		return err
	}
	if a.promotedTools == nil {
		a.promotedTools = make(map[string]bool)
	}
	for _, t := range built {
		a.promotedTools[t.Name()] = true
	}
	return nil
}

func (a *App) MCPStates() []whalemcp.ServerState {
	if a == nil || a.mcpManager == nil {
		return nil
	}
	return a.mcpManager.States()
}
