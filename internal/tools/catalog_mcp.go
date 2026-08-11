package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

// DeferredToolCatalog is the interface the tools package needs from the MCP layer.
type DeferredToolCatalog interface {
	Empty() bool
	Search(query string) []DeferredToolEntry
	Names() []string
}

// DeferredToolEntry is a lightweight tool descriptor returned by Search.
//
// Keep in sync with mcp.DeferredToolMeta and the adapter in
// app.mcpCatalogAdapter.Search.
type DeferredToolEntry struct {
	Name        string
	Server      string
	Description string
}

// DeferredToolPromoter builds full Tool objects for the given qualified names,
// adds them to the tool registry, and returns their specs for the model response.
// Implementations are expected to return an error for unknown names.
type DeferredToolPromoter func(names []string) ([]core.ToolSpec, error)

// DeferredToolRenderer returns the <available-deferred-tools> block text.
type DeferredToolRenderer func() string

func (b *Toolset) mcpSearchTools() []core.Tool {
	if b.deferredCatalog == nil || b.deferredCatalog.Empty() {
		return nil
	}
	return []core.Tool{
		NewToolSearchTool(b.deferredCatalog, b.deferredPromote, b.deferredRenderer),
	}
}

// NewToolSearchTool creates the tool_search tool as a closure over a catalog and promoter.
func NewToolSearchTool(
	catalog DeferredToolCatalog,
	promote DeferredToolPromoter,
	renderAvailable DeferredToolRenderer,
) core.Tool {
	return toolFn{
		name: "tool_search",
		description: `Search for MCP tools by name or keyword and load them for use. Use this when you need an MCP tool that is listed in <available-deferred-tools> but not yet in your tool schema.

Query forms:
  select:Name1,Name2 — fetch exact tools by comma-separated qualified names
  keyword phrase     — case-insensitive regex search across tool names and descriptions
  +must_have extras  — require must_have in the name; extra terms score higher

Returns up to 5 matching tools and activates them for subsequent calls.`,
		parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query: select:Name1,Name2 | keyword regex | +must_have extras",
				},
			},
			"required": []string{"query"},
		},
		readOnly:     true,
		capabilities: []string{"workspace.read"},
		fn: func(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
			if catalog == nil || catalog.Empty() {
				return core.ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					ModelText:  `{"ok":false,"error":"no deferred MCP tools available","code":"no_deferred_tools"}`,
					Code:       "no_deferred_tools",
				}, nil
			}

			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
				return marshalToolError(call, "invalid_args", fmt.Sprintf("invalid tool_search input: %v", err)), nil
			}
			if strings.TrimSpace(args.Query) == "" {
				// Show available tools if query is empty
				text := "No deferred tools available."
				if renderAvailable != nil {
					if t := renderAvailable(); t != "" {
						text = t
					}
				}
				return core.ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					ModelText:  text,
				}, nil
			}

			matches := catalog.Search(args.Query)
			if len(matches) == 0 {
				names := catalog.Names()
				hint := ""
				if len(names) > 0 {
					hint = fmt.Sprintf(" No match. Available deferred tools (%d total): %s", len(names), strings.Join(names, ", "))
				}
				return core.ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					ModelText:  fmt.Sprintf(`{"ok":false,"error":"no matching deferred tools found","code":"no_match"}%s`, hint),
					Code:       "no_match",
				}, nil
			}

			names := make([]string, len(matches))
			for i, m := range matches {
				names[i] = m.Name
			}

			if promote == nil {
				// No promoter configured — just return matches as info
				var b strings.Builder
				b.WriteString("Found tools:\n")
				for _, m := range matches {
					fmt.Fprintf(&b, "- %s (%s): %s\n", m.Name, m.Server, m.Description)
				}
				return core.ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					ModelText:  b.String(),
				}, nil
			}

			specs, err := promote(names)
			if err != nil {
				return core.ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					ModelText:  fmt.Sprintf(`{"ok":false,"error":%q,"code":"promote_failed"}`, err.Error()),
					Code:       "promote_failed",
				}, nil
			}

			// Render activated tools for the model
			var b strings.Builder
			fmt.Fprintf(&b, "Activated %d tool(s):\n\n", len(specs))
			for _, spec := range specs {
				b.WriteString(fmt.Sprintf("## %s\n%s\n\n", spec.Name, spec.Description))
				if len(spec.Parameters) > 0 {
					if props, ok := spec.Parameters["properties"]; ok {
						pretty, _ := json.MarshalIndent(props, "", "  ")
						b.WriteString(fmt.Sprintf("Parameters: %s\n\n", string(pretty)))
					}
				}
			}
			b.WriteString("These tools are now available in your tool schema for subsequent calls.")

			return core.ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				ModelText:  b.String(),
			}, nil
		},
	}
}
