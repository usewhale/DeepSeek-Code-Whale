package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
)

// fakeDeferredCatalog is a test-only implementation of DeferredToolCatalog.
type fakeDeferredCatalog struct {
	entries []DeferredToolEntry
}

func (f *fakeDeferredCatalog) Empty() bool { return f == nil || len(f.entries) == 0 }

func (f *fakeDeferredCatalog) Search(query string) []DeferredToolEntry {
	if f == nil {
		return nil
	}
	var results []DeferredToolEntry
	lower := strings.ToLower(query)
	for _, e := range f.entries {
		if strings.Contains(strings.ToLower(e.Name), lower) || strings.Contains(strings.ToLower(e.Description), lower) {
			results = append(results, e)
			if len(results) >= 5 {
				break
			}
		}
	}
	return results
}

func (f *fakeDeferredCatalog) Names() []string {
	if f == nil {
		return nil
	}
	out := make([]string, len(f.entries))
	for i, e := range f.entries {
		out[i] = e.Name
	}
	return out
}

func newFakeDeferredCatalog(entries ...DeferredToolEntry) *fakeDeferredCatalog {
	return &fakeDeferredCatalog{entries: entries}
}

// fakeTool is a minimal core.Tool for testing promotion.
type fakeTool struct {
	name        string
	description string
	parameters  map[string]any
}

func (f fakeTool) Name() string               { return f.name }
func (f fakeTool) Description() string        { return f.description }
func (f fakeTool) Parameters() map[string]any { return f.parameters }
func (f fakeTool) Run(context.Context, core.ToolCall) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

func TestMcpSearchToolsWithNilCatalog(t *testing.T) {
	ts, err := NewToolset("/tmp")
	if err != nil {
		t.Fatalf("NewToolset: %v", err)
	}
	// No deferred catalog set — mcpSearchTools should return nil.
	tools := ts.mcpSearchTools()
	if tools != nil {
		t.Fatal("mcpSearchTools should return nil when catalog is nil")
	}
}

func TestMcpSearchToolsWithEmptyCatalog(t *testing.T) {
	ts, err := NewToolset("/tmp")
	if err != nil {
		t.Fatalf("NewToolset: %v", err)
	}
	ts.SetDeferredToolSearch(newFakeDeferredCatalog(), nil, nil)
	tools := ts.mcpSearchTools()
	if tools != nil {
		t.Fatal("mcpSearchTools should return nil when catalog is empty")
	}
}

func TestMcpSearchToolsWithNonEmptyCatalog(t *testing.T) {
	ts, err := NewToolset("/tmp")
	if err != nil {
		t.Fatalf("NewToolset: %v", err)
	}
	cat := newFakeDeferredCatalog(DeferredToolEntry{Name: "mcp__s__x", Server: "s", Description: "x"})
	ts.SetDeferredToolSearch(cat, nil, func() string { return "" })

	tools := ts.mcpSearchTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "tool_search" {
		t.Fatalf("expected tool_search, got %s", tools[0].Name())
	}
}

func TestToolSearchEmptyCatalog(t *testing.T) {
	cat := newFakeDeferredCatalog()
	tool := NewToolSearchTool(cat, nil, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"github"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.ModelText, "no_deferred_tools") {
		t.Fatalf("expected no_deferred_tools, got %q", result.ModelText)
	}
}

func TestToolSearchNilCatalog(t *testing.T) {
	tool := NewToolSearchTool(nil, nil, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"github"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.ModelText, "no_deferred_tools") {
		t.Fatalf("expected no_deferred_tools, got %q", result.ModelText)
	}
}

func TestToolSearchEmptyQueryShowsAvailable(t *testing.T) {
	cat := newFakeDeferredCatalog(
		DeferredToolEntry{Name: "mcp__s__alpha", Server: "s", Description: "alpha tool"},
		DeferredToolEntry{Name: "mcp__s__beta", Server: "s", Description: "beta tool"},
	)
	renderCalled := false
	tool := NewToolSearchTool(cat, nil, func() string {
		renderCalled = true
		return "<available-deferred-tools>\n[s]\n  mcp__s__alpha — alpha tool\n  mcp__s__beta — beta tool\n</available-deferred-tools>"
	})

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":""}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !renderCalled {
		t.Fatal("renderAvailable should be called for empty query")
	}
	if !strings.Contains(result.ModelText, "<available-deferred-tools>") {
		t.Fatalf("expected render output, got %q", result.ModelText)
	}
}

func TestToolSearchEmptyQueryNilRenderer(t *testing.T) {
	cat := newFakeDeferredCatalog(
		DeferredToolEntry{Name: "mcp__s__x", Server: "s", Description: "x"},
	)
	tool := NewToolSearchTool(cat, nil, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":""}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.ModelText, "No deferred tools available") {
		t.Fatalf("expected fallback message for nil renderer, got %q", result.ModelText)
	}
}

func TestToolSearchNoMatch(t *testing.T) {
	cat := newFakeDeferredCatalog(
		DeferredToolEntry{Name: "mcp__s__alpha", Server: "s", Description: "first"},
	)
	tool := NewToolSearchTool(cat, nil, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"nonexistent"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.ModelText, "no_match") {
		t.Fatalf("expected no_match, got %q", result.ModelText)
	}
	if !strings.Contains(result.ModelText, "mcp__s__alpha") {
		t.Fatalf("expected hint with available tool names, got %q", result.ModelText)
	}
	if !strings.Contains(result.ModelText, "(1 total)") {
		t.Fatalf("expected hint with total count, got %q", result.ModelText)
	}
}

func TestToolSearchWithoutPromoterReturnsInfo(t *testing.T) {
	cat := newFakeDeferredCatalog(
		DeferredToolEntry{Name: "mcp__s__x", Server: "s", Description: "x tool"},
	)
	tool := NewToolSearchTool(cat, nil, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"x"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.ModelText, "Found tools:") {
		t.Fatalf("expected info listing, got %q", result.ModelText)
	}
	if !strings.Contains(result.ModelText, "mcp__s__x") {
		t.Fatalf("expected tool name in output, got %q", result.ModelText)
	}
}

func TestToolSearchWithPromoterActivatesTools(t *testing.T) {
	cat := newFakeDeferredCatalog(
		DeferredToolEntry{Name: "mcp__s__x", Server: "s", Description: "x tool"},
	)

	var promotedNames []string
	promote := func(names []string) ([]core.ToolSpec, error) {
		promotedNames = append(promotedNames, names...)
		return []core.ToolSpec{
			{Name: names[0], Description: "activated x tool", Parameters: map[string]any{}},
		}, nil
	}

	tool := NewToolSearchTool(cat, promote, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"x"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(promotedNames) != 1 || promotedNames[0] != "mcp__s__x" {
		t.Fatalf("expected mcp__s__x to be promoted, got %v", promotedNames)
	}
	if !strings.Contains(result.ModelText, "Activated 1 tool") {
		t.Fatalf("expected activation message, got %q", result.ModelText)
	}
	if !strings.Contains(result.ModelText, "activated x tool") {
		t.Fatalf("expected tool description in output, got %q", result.ModelText)
	}
}

func TestToolSearchPromoterError(t *testing.T) {
	cat := newFakeDeferredCatalog(
		DeferredToolEntry{Name: "mcp__s__x", Server: "s", Description: "x"},
	)
	promote := func(names []string) ([]core.ToolSpec, error) {
		return nil, &testError{"promotion failed"}
	}
	tool := NewToolSearchTool(cat, promote, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"x"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.ModelText, "promote_failed") {
		t.Fatalf("expected promote_failed, got %q", result.ModelText)
	}
	if !strings.Contains(result.ModelText, "promotion failed") {
		t.Fatalf("expected error message, got %q", result.ModelText)
	}
}

func TestToolSearchInvalidJSON(t *testing.T) {
	cat := newFakeDeferredCatalog(DeferredToolEntry{Name: "mcp__s__x", Server: "s", Description: "x"})
	tool := NewToolSearchTool(cat, nil, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `not json`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Code != "invalid_args" {
		t.Fatalf("expected invalid_args, got code=%q text=%q", result.Code, result.ModelText)
	}
}

func TestToolSearchMultipleResultsCappedAtFive(t *testing.T) {
	entries := make([]DeferredToolEntry, 0, 10)
	for i := range 10 {
		entries = append(entries, DeferredToolEntry{
			Name:        "mcp__s__tool_" + string(rune('a'+i)),
			Server:      "s",
			Description: "tool",
		})
	}
	cat := newFakeDeferredCatalog(entries...)

	var promotedNames []string
	promote := func(names []string) ([]core.ToolSpec, error) {
		promotedNames = names
		var specs []core.ToolSpec
		for _, n := range names {
			specs = append(specs, core.ToolSpec{Name: n, Description: n, Parameters: map[string]any{}})
		}
		return specs, nil
	}

	tool := NewToolSearchTool(cat, promote, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"mcp"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The fake catalog's Search returns at most 5, so promotedNames should be ≤5.
	if len(promotedNames) > 5 {
		t.Fatalf("promoted at most 5 tools, got %d", len(promotedNames))
	}
	// Activation message should say however many were promoted.
	if !strings.Contains(result.ModelText, "Activated") {
		t.Fatalf("expected activation message, got %q", result.ModelText)
	}
}

func TestToolSearchSpecIncludesParameters(t *testing.T) {
	cat := newFakeDeferredCatalog(
		DeferredToolEntry{Name: "mcp__s__x", Server: "s", Description: "param tool"},
	)
	promote := func(names []string) ([]core.ToolSpec, error) {
		return []core.ToolSpec{{
			Name:        names[0],
			Description: "param tool",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"message": map[string]any{"type": "string"}},
			},
		}}, nil
	}
	tool := NewToolSearchTool(cat, promote, nil)

	call := core.ToolCall{ID: "c1", Name: "tool_search", Input: `{"query":"x"}`}
	result, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.ModelText, "message") {
		t.Fatalf("expected parameter 'message' in output, got %q", result.ModelText)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
