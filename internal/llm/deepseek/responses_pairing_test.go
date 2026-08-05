package deepseek

import (
	"testing"

	"github.com/usewhale/whale/internal/core"
)

func TestResponsesFunctionCallOutputAdjacentWithReasoning(t *testing.T) {
	history := []core.Message{
		{SessionID: "s", Role: core.RoleUser, Text: "2026 年最新发布的稳定版 Go 是哪个版本？"},
		{SessionID: "s", Role: core.RoleAssistant,
			Reasoning: "The user is asking about the latest stable version of Go released in 2026.",
			ToolCalls: []core.ToolCall{{ID: "call_01_bw1UuoLuyxMgK3sz0Nm26550", Name: "fetch", Input: `{"url": "https://go.dev/doc/devel/release"}`}},
		},
		{SessionID: "s", Role: core.RoleTool,
			ToolResults: []core.ToolResult{{ToolCallID: "call_01_bw1UuoLuyxMgK3sz0Nm26550", Name: "fetch", ModelText: "go1.26.5 released"}},
		},
	}
	items := toResponsesInputItems(history, nil)
	var types []string
	for _, it := range items {
		typ, _ := it["type"].(string)
		types = append(types, typ)
	}
	// function_call must be immediately followed by its function_call_output;
	// the reasoning item must NOT sit between them.
	for i := 0; i < len(types); i++ {
		if types[i] == "function_call" {
			if i+1 >= len(types) || types[i+1] != "function_call_output" {
				t.Fatalf("function_call at %d not adjacent to output: %v", i, types)
			}
		}
	}
	// The reasoning item must come before the function_call pair.
	lastReasoning := -1
	firstCall := -1
	for i, typ := range types {
		switch typ {
		case "reasoning":
			lastReasoning = i
		case "function_call":
			if firstCall < 0 {
				firstCall = i
			}
		}
	}
	if lastReasoning > firstCall {
		t.Fatalf("reasoning (%d) after function_call (%d): %v", lastReasoning, firstCall, types)
	}
}
