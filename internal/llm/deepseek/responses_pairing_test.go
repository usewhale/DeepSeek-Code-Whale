package deepseek

import (
	"testing"

	"github.com/usewhale/whale/internal/core"
)

func TestResponsesParallelFunctionCallsStayInReasoningAssistantGroup(t *testing.T) {
	history := []core.Message{
		{SessionID: "s", Role: core.RoleUser, Text: "2026 年最新发布的稳定版 Go 是哪个版本？"},
		{SessionID: "s", Role: core.RoleAssistant,
			Reasoning: "The user is asking about the latest stable version of Go released in 2026.",
			ToolCalls: []core.ToolCall{
				{ID: "call_01_bw1UuoLuyxMgK3sz0Nm26550", Name: "fetch", Input: `{"url": "https://go.dev/doc/devel/release"}`},
				{ID: "call_02_R9buKvzqdf3ViQwt0xOa", Name: "fetch", Input: `{"url": "https://go.dev/VERSION?m=text"}`},
			},
		},
		{SessionID: "s", Role: core.RoleTool,
			ToolResults: []core.ToolResult{
				{ToolCallID: "call_01_bw1UuoLuyxMgK3sz0Nm26550", Name: "fetch", ModelText: "go1.26.5 released"},
				{ToolCallID: "call_02_R9buKvzqdf3ViQwt0xOa", Name: "fetch", ModelText: "go1.26.5"},
			},
		},
	}
	items := toResponsesInputItems(history, nil)
	var types []string
	for _, it := range items {
		typ, _ := it["type"].(string)
		types = append(types, typ)
	}
	want := []string{
		"message",
		"reasoning",
		"function_call",
		"function_call",
		"function_call_output",
		"function_call_output",
	}
	if len(types) != len(want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("types = %v, want %v", types, want)
		}
	}
}
