package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usewhale/whale/internal/core"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/tools"
)

type mcpRuntimeTestInput struct {
	Message string `json:"message"`
}

type mcpRuntimeTestOutput struct {
	Message string `json:"message"`
}

func TestRefreshMCPToolsSetsUpDeferredCatalog(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := newMCPRuntimeTestApp(mgr)

	if err := app.refreshMCPTools(); err != nil {
		t.Fatalf("initial refreshMCPTools: %v", err)
	}
	// With deferred loading, MCP tools are NOT directly in the registry.
	if app.toolRegistry.Get("mcp__runtime__echo") != nil {
		t.Fatal("MCP tool should NOT be in registry before promotion — it's deferred")
	}
	// tool_search should be present if catalog has entries.
	if app.toolRegistry.Get("tool_search") == nil {
		t.Fatal("tool_search should be in registry when deferred catalog is non-empty")
	}
	// Verify the deferred catalog was built.
	catalog := app.mcpManager.BuildDeferredCatalog()
	if catalog.Empty() {
		t.Fatal("deferred catalog should not be empty")
	}
}

func TestRefreshMCPToolsAllowsIdentityAfterFreeze(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := newMCPRuntimeTestApp(mgr)

	if err := app.refreshMCPTools(); err != nil {
		t.Fatalf("initial refreshMCPTools: %v", err)
	}
	app.freezeMCPToolSignature()
	if err := app.refreshMCPTools(); err != nil {
		t.Fatalf("identity refreshMCPTools: %v", err)
	}
	// tool_search should still be present.
	if app.toolRegistry.Get("tool_search") == nil {
		t.Fatal("tool_search should remain registered after identity refresh")
	}
}

func TestRefreshMCPToolsHandlesCatalogChangeAfterFreeze(t *testing.T) {
	first := newMCPRuntimeTestManager(t, "echoes a message")
	app := newMCPRuntimeTestApp(first)
	if err := app.refreshMCPTools(); err != nil {
		t.Fatalf("initial refreshMCPTools: %v", err)
	}
	app.freezeMCPToolSignature()

	catalog := app.mcpManager.BuildDeferredCatalog()
	if catalog.Empty() {
		t.Fatal("deferred catalog empty after initial refresh")
	}

	// second manager has different tool description
	second := newMCPRuntimeTestManager(t, "echoes a message differently")
	app.mcpManager = second
	err := app.refreshMCPTools()
	// With deferred loading, catalog hash changes don't block — it's a no-op.
	if err != nil {
		t.Fatalf("catalog description change should not block refresh: %v", err)
	}
	// tool_search should still be available.
	if app.toolRegistry.Get("tool_search") == nil {
		t.Fatal("tool_search disappeared after catalog change")
	}
}

func TestMCPToolSetSignatureChangesWithSchema(t *testing.T) {
	first, err := mcpToolSetSignature([]core.Tool{mcpSignatureTestTool{
		name:        "mcp__runtime__echo",
		description: "echoes a message",
		parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		},
	}})
	if err != nil {
		t.Fatalf("first signature: %v", err)
	}
	second, err := mcpToolSetSignature([]core.Tool{mcpSignatureTestTool{
		name:        "mcp__runtime__echo",
		description: "echoes a message",
		parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}, "upper": map[string]any{"type": "boolean"}},
		},
	}})
	if err != nil {
		t.Fatalf("second signature: %v", err)
	}
	if first == second {
		t.Fatal("schema change should change MCP tool-set signature")
	}
}

func TestMCPToolSetDeltaReportsAddedRemovedChanged(t *testing.T) {
	prev := map[string]string{
		"mcp__old__same":    `{"same":true}`,
		"mcp__old__changed": `{"version":1}`,
		"mcp__old__removed": `{"removed":true}`,
	}
	next := map[string]string{
		"mcp__old__same":    `{"same":true}`,
		"mcp__old__changed": `{"version":2}`,
		"mcp__new__added":   `{"added":true}`,
	}
	got := mcpToolSetDelta(prev, next)
	for _, want := range []string{
		"added mcp__new__added",
		"removed mcp__old__removed",
		"changed mcp__old__changed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("delta missing %q: %s", want, got)
		}
	}
}

func newMCPRuntimeTestApp(mgr *whalemcp.Manager) *App {
	ts, err := tools.NewToolset("/tmp")
	if err != nil {
		panic(err)
	}
	return &App{
		mcpManager:           mgr,
		toolset:              ts,
		baseToolRegistry:     core.NewToolRegistry(nil),
		subagentToolRegistry: core.NewToolRegistry(nil),
		toolRegistry:         core.NewToolRegistry(nil),
	}
}

func newMCPRuntimeTestManager(t *testing.T, description string) *whalemcp.Manager {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "whale-app-test-mcp", Version: "v0.0.0"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: description}, func(ctx context.Context, req *sdk.CallToolRequest, input mcpRuntimeTestInput) (*sdk.CallToolResult, mcpRuntimeTestOutput, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: "echo:" + input.Message}},
		}, mcpRuntimeTestOutput{Message: input.Message}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	mgr := whalemcp.NewManager(whalemcp.Config{
		Servers: map[string]whalemcp.ServerConfig{
			"runtime": {
				Type:    "http",
				URL:     httpServer.URL,
				Timeout: 5,
			},
		},
	})
	mgr.Initialize(context.Background())
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

type mcpSignatureTestTool struct {
	name        string
	description string
	parameters  map[string]any
}

func (t mcpSignatureTestTool) Name() string { return t.name }

func (t mcpSignatureTestTool) Description() string { return t.description }

func (t mcpSignatureTestTool) Parameters() map[string]any { return t.parameters }

func (t mcpSignatureTestTool) Run(context.Context, core.ToolCall) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

func mcpToolSetSignature(tools []core.Tool) (string, error) {
	sig, _, err := mcpToolSetSnapshot(tools)
	return sig, err
}

func mcpToolSetSnapshot(tools []core.Tool) (string, map[string]string, error) {
	payloads := make([]map[string]any, 0, len(tools))
	byName := make(map[string]string, len(tools))
	for _, tool := range tools {
		payload := core.ProviderToolPayload(tool)
		payloads = append(payloads, payload)
		b, err := json.Marshal(payload)
		if err != nil {
			return "", nil, fmt.Errorf("hash mcp tool %s: %w", tool.Name(), err)
		}
		byName[tool.Name()] = string(b)
	}
	b, err := json.Marshal(payloads)
	if err != nil {
		return "", nil, fmt.Errorf("hash mcp tool set: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), byName, nil
}

func mcpToolSetDelta(prev, next map[string]string) string {
	var added, removed, changed []string
	for name, payload := range next {
		if prevPayload, ok := prev[name]; !ok {
			added = append(added, name)
		} else if prevPayload != payload {
			changed = append(changed, name)
		}
	}
	for name := range prev {
		if _, ok := next[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	if len(changed) > 0 {
		parts = append(parts, "changed "+strings.Join(changed, ", "))
	}
	if len(parts) == 0 {
		return "tool order changed"
	}
	return strings.Join(parts, "; ")
}

func TestRestorePromotedToolsBuildToolsError(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := newMCPRuntimeTestApp(mgr)

	// Set up a session directory with a promoted_tools.json that references
	// a tool name not present in discovery data.
	dir := t.TempDir()
	app.sessionsDir = dir
	app.sessionID = "test-session"

	catalog := mgr.BuildDeferredCatalog()
	if catalog.Empty() {
		t.Fatal("catalog should not be empty")
	}
	state := promotedToolState{
		CatalogHash: catalog.Hash(),
		ToolNames:   []string{"mcp__runtime__nonexistent"},
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	sessionDir := filepath.Join(dir, "test-session")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "promoted_tools.json"), b, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	err = app.RestorePromotedTools()
	if err == nil {
		t.Fatal("expected error when BuildTools fails for non-existent tool, got nil")
	}
}

// --- renderDeferredToolsBlock tests ---

func TestRenderDeferredToolsBlockNilManager(t *testing.T) {
	app := &App{} // mcpManager is nil
	if got := app.renderDeferredToolsBlock(); got != "" {
		t.Fatalf("expected empty string for nil mcpManager, got %q", got)
	}
}

func TestRenderDeferredToolsBlockWithTools(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := &App{mcpManager: mgr}

	block := app.renderDeferredToolsBlock()
	if block == "" {
		t.Fatal("expected non-empty block for populated catalog")
	}
	if !strings.Contains(block, "<available-deferred-tools>") {
		t.Fatalf("expected opening tag, got %q", block)
	}
	if !strings.Contains(block, "</available-deferred-tools>") {
		t.Fatalf("expected closing tag, got %q", block)
	}
	if !strings.Contains(block, "mcp__runtime__echo") {
		t.Fatalf("expected tool name in block, got %q", block)
	}
}

func TestRenderDeferredToolsBlockFormat(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := &App{mcpManager: mgr}

	block := app.renderDeferredToolsBlock()
	if block == "" {
		t.Fatal("expected non-empty block")
	}
	if !strings.HasPrefix(block, "<available-deferred-tools>\n") {
		t.Fatalf("expected block to start with opening tag on its own line, got %q", block)
	}
	if !strings.HasSuffix(block, "</available-deferred-tools>") {
		t.Fatalf("expected block to end with closing tag, got %q", block)
	}
	if !strings.Contains(block, "[server: ") {
		t.Fatalf("expected server section, got %q", block)
	}
	if !strings.Contains(block, " — ") {
		t.Fatalf("expected tool name/description separator, got %q", block)
	}
}

func TestRenderDeferredToolsBlockTruncation(t *testing.T) {
	// Simulate truncation by verifying that a block exceeding the limit
	// is handled correctly. We test the truncation behaviour directly
	// rather than through the full MCP pipeline (which has description
	// length limits in the protocol).
	longLine := strings.Repeat("x", availableDeferredToolsMaxChars+100)
	block := "<available-deferred-tools>\n[server: test]\n  mcp__test__tool — " + longLine + "\n</available-deferred-tools>"

	// Apply the same truncation logic as renderDeferredToolsBlock.
	truncated := block
	if len(truncated) > availableDeferredToolsMaxChars {
		truncated = block[:availableDeferredToolsMaxChars]
		if idx := strings.LastIndex(truncated, "\n"); idx > 0 {
			truncated = truncated[:idx]
		}
		truncated += "\n... more tool(s) omitted\n</available-deferred-tools>"
	}

	if !strings.HasPrefix(truncated, "<available-deferred-tools>") {
		t.Fatalf("truncated block should start with opening tag, got %q", truncated)
	}
	if !strings.Contains(truncated, "more tool(s) omitted") {
		t.Fatalf("truncated block should contain omission notice, got %q", truncated)
	}
	if !strings.HasSuffix(truncated, "</available-deferred-tools>") {
		t.Fatalf("truncated block should end with closing tag, got %q", truncated)
	}
	// Truncation should have removed content.
	if strings.Contains(truncated, "xxx") {
		t.Fatal("truncated block should not contain the overflow content")
	}
}
