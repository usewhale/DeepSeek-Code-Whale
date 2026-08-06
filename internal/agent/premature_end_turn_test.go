package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
)

type prematureEndThenRecoverProvider struct {
	calls    int
	sawNudge bool
}

func (p *prematureEndThenRecoverProvider) StreamResponse(_ context.Context, msgs []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	for _, msg := range msgs {
		if strings.Contains(msg.Text, "<premature_end_turn>") {
			p.sawNudge = true
		}
	}
	switch p.calls {
	case 1:
		return eventStream(endTurnEvent("现在验证 workflow 语法："))
	case 2:
		return eventStream(toolUseEvent(toolCall("tc-1", "echo", `{"v":"ok"}`)))
	default:
		return eventStream(endTurnEvent("验证通过。"))
	}
}

func TestTurnLoopRecoversFromPrematureEndTurn(t *testing.T) {
	store := NewInMemoryStore()
	provider := &prematureEndThenRecoverProvider{}
	a := NewAgent(provider, store, []Tool{echoTool{}})

	events, err := a.RunStream(context.Background(), "s-premature-end", "verify it")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	var recovered, reset, sawToolResult bool
	var done *core.Message
	for ev := range events {
		switch ev.Type {
		case AgentEventTypePrematureEndRecovered:
			recovered = true
		case AgentEventTypeResponseReset:
			reset = true
		case AgentEventTypeToolResult:
			sawToolResult = true
		case AgentEventTypeDone:
			done = ev.Message
		case AgentEventTypeError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}

	if !recovered || !reset || !sawToolResult {
		t.Fatalf("recovered/reset/toolResult = %v/%v/%v, want true/true/true", recovered, reset, sawToolResult)
	}
	if !provider.sawNudge || provider.calls != 3 {
		t.Fatalf("provider sawNudge/calls = %v/%d, want true/3", provider.sawNudge, provider.calls)
	}
	if done == nil || done.Text != "验证通过。" {
		t.Fatalf("done = %#v, want recovered final answer", done)
	}

	msgs, err := store.List(context.Background(), "s-premature-end")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var nudges int
	for _, msg := range msgs {
		if msg.Role == core.RoleUser && msg.Hidden && strings.Contains(msg.Text, "<premature_end_turn>") {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("persisted nudges = %d, want 1", nudges)
	}
}

type repeatedPrematureEndProvider struct {
	calls int
}

func (p *repeatedPrematureEndProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	return eventStream(endTurnEvent("接下来继续检查："))
}

func TestTurnLoopBoundsPrematureEndRecovery(t *testing.T) {
	store := NewInMemoryStore()
	provider := &repeatedPrematureEndProvider{}
	a := NewAgent(provider, store, []Tool{echoTool{}})

	events, err := a.RunStream(context.Background(), "s-premature-bound", "verify it")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	var recovered int
	for ev := range events {
		if ev.Type == AgentEventTypePrematureEndRecovered {
			recovered++
		}
		if ev.Type == AgentEventTypeError {
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if recovered != maxPrematureEndTurnNudges {
		t.Fatalf("recoveries = %d, want %d", recovered, maxPrematureEndTurnNudges)
	}
	if provider.calls != maxPrematureEndTurnNudges+1 {
		t.Fatalf("provider calls = %d, want %d", provider.calls, maxPrematureEndTurnNudges+1)
	}
}

func TestShouldRecoverPrematureEndTurn(t *testing.T) {
	base := core.Message{Role: core.RoleAssistant, FinishReason: core.FinishReasonEndTurn}
	tests := []struct {
		name           string
		text           string
		mode           session.Mode
		opts           RunOptions
		toolsAvailable bool
		want           bool
	}{
		{name: "observed chinese verify lead-in", text: "现在验证 workflow 语法：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed chinese retry lead-in", text: "API 没改到权限。换端点重试：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed chinese patch lead-in", text: "第一个 edit 没改到 description，我写重复了，补上：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed workflow write lead-in", text: "项目已存在（mightty.pages.dev）。先写 workflow：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed rerun lead-in", text: "原始 formula 的 URL 不需要改。重新验证只改 version + sha256：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed retrigger lead-in", text: "权限已经修正。重新触发 release-please workflow 验证：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "english action lead-in", text: "The config is present. Now verify the workflow:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "complete answer", text: "The workflow is valid.", mode: session.ModeAgent, toolsAvailable: true},
		{name: "ordinary heading", text: "Details:", mode: session.ModeAgent, toolsAvailable: true},
		{name: "user choice prompt", text: "请选择：", mode: session.ModeAgent, toolsAvailable: true},
		{name: "plan reply", text: "接下来执行：", mode: session.ModePlan, toolsAvailable: true},
		{name: "ask reply", text: "现在检查：", mode: session.ModeAsk, toolsAvailable: true},
		{name: "tools suppressed", text: "Now verify:", mode: session.ModeAgent, opts: RunOptions{SuppressTools: true}, toolsAvailable: true},
		{name: "no tools available", text: "Now verify:", mode: session.ModeAgent, toolsAvailable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := base
			msg.Text = tt.text
			if got := shouldRecoverPrematureEndTurn(msg, tt.mode, tt.opts, tt.toolsAvailable); got != tt.want {
				t.Fatalf("shouldRecoverPrematureEndTurn(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
