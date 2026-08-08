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
		finishReason   core.FinishReason
		toolCalls      []core.ToolCall
		mode           session.Mode
		opts           RunOptions
		toolsAvailable bool
		want           bool
	}{
		// Colon-terminated lead-ins: the colon is the primary trigger since the
		// guard was widened. 3 of 4 known announce-then-stop instances end in
		// ":", including the gerund forms the old verb-prefix scan missed
		// ("Fixing:", "Running tests:").
		{name: "gerund lead-in fixing colon", text: "a deliberate asymmetry the comment now contradicts. Fixing:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "running tests lead-in", text: "Running tests:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed chinese verify lead-in", text: "现在验证 workflow 语法：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed chinese retry lead-in", text: "API 没改到权限。换端点重试：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed chinese patch lead-in", text: "第一个 edit 没改到 description，我写重复了，补上：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed workflow write lead-in", text: "项目已存在（mightty.pages.dev）。先写 workflow：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed rerun lead-in", text: "原始 formula 的 URL 不需要改。重新验证只改 version + sha256：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed retrigger lead-in", text: "权限已经修正。重新触发 release-please workflow 验证：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed final picker lead-in", text: "最后是 session picker 的循环：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "observed immediate merge lead-in", text: "CI 已通过，但 merge 没执行成功。立即用正确命令合并：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "chinese next step lead-in", text: "下一步检查 CI：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "english action lead-in", text: "The config is present. Now verify the workflow:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "english final action lead-in", text: "The checks passed. Finally merge the pull request:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "then commit colon", text: "The diff is staged, then commit:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "inspect plus resolve colon", text: "Inspect + resolve keeping both:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		// Non-colon fallback: the one known "."-terminated announce-then-stop
		// instance is caught by the trailing action-clause prefix scan.
		{name: "dot-terminated action clause fallback", text: "Amend tests into commit, then append maintainer reply draft.", mode: session.ModeAgent, toolsAvailable: true, want: true},
		// Non-colon inflected lead-ins: the prefix scan also matches -ing
		// gerund forms, so "fixing"/"running"/"updating"/"writing" without a
		// trailing colon are recovered instead of slipping through.
		{name: "non-colon gerund fixing", text: "The typo is clear. Fixing the permission check", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "non-colon gerund running", text: "The suite is ready. Running the tests", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "non-colon gerund updating", text: "Rebuild needed. Updating the dependency", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "non-colon gerund writing", text: "Understood. Writing the patch", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "non-colon gerund retrying", text: "Rate limited. Retrying the request", mode: session.ModeAgent, toolsAvailable: true, want: true},
		// Accepted colon false positives: bounded by the 2-nudge cap, harmless
		// per the nudge's "If no action remains, provide the complete final
		// answer" escape hatch.
		{name: "fixture analysis colon accepted", text: "The fixture analysis:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "ordinary heading accepted", text: "Details:", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "ordinary final heading accepted", text: "最后：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "user choice prompt accepted", text: "请选择：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		// Negatives: no colon and no action-prefix clause stay final answers.
		{name: "complete answer", text: "The workflow is valid.", mode: session.ModeAgent, toolsAvailable: true},
		{name: "complete answer the fix is simple", text: "The fix is simple.", mode: session.ModeAgent, toolsAvailable: true},
		{name: "final answer with future-tense clause", text: "The user will decide the direction.", mode: session.ModeAgent, toolsAvailable: true},
		{name: "colon heading with content", text: "Details: the workflow is valid.", mode: session.ModeAgent, toolsAvailable: true},
		{name: "plan reply", text: "接下来执行：", mode: session.ModePlan, toolsAvailable: true},
		{name: "ask reply", text: "现在检查：", mode: session.ModeAsk, toolsAvailable: true},
		{name: "tools suppressed", text: "Now verify:", mode: session.ModeAgent, opts: RunOptions{SuppressTools: true}, toolsAvailable: true},
		{name: "no tools available", text: "Now verify:", mode: session.ModeAgent, toolsAvailable: false},
		// Remaining outer gates: non-end-turn finishes and turns that already
		// carried a structured tool call are never recovered.
		{name: "tool use finish reason", text: "Now verify:", finishReason: core.FinishReasonToolUse, mode: session.ModeAgent, toolsAvailable: true},
		{name: "cancelled finish reason", text: "Now verify:", finishReason: core.FinishReasonCanceled, mode: session.ModeAgent, toolsAvailable: true},
		{name: "has tool calls", text: "Now verify:", toolCalls: []core.ToolCall{{ID: "tc-1", Name: "echo", Input: "{}"}}, mode: session.ModeAgent, toolsAvailable: true},
		// Text-shape edges around the colon check.
		{name: "trailing whitespace after colon", text: "Fixing: \n", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "bare colon", text: ":", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "bare fullwidth colon", text: "：", mode: session.ModeAgent, toolsAvailable: true, want: true},
		{name: "empty text", text: "   ", mode: session.ModeAgent, toolsAvailable: true},
		{name: "no colon no prefix bullet", text: "The output is ready.", mode: session.ModeAgent, toolsAvailable: true},
		{name: "non-action gerund not matched", text: "The response was helpful. Singing the final note is next.", mode: session.ModeAgent, toolsAvailable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := base
			msg.Text = tt.text
			if tt.finishReason != "" {
				msg.FinishReason = tt.finishReason
			}
			msg.ToolCalls = tt.toolCalls
			if got := shouldRecoverPrematureEndTurn(msg, tt.mode, tt.opts, tt.toolsAvailable); got != tt.want {
				t.Fatalf("shouldRecoverPrematureEndTurn(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestTrailingActionClause(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "sentence boundary", text: "The checks passed. Then merge it", want: "Then merge it"},
		{name: "newline boundary", text: "Step one done\nNext verify", want: "Next verify"},
		{name: "comma clause", text: "commit, then append reply", want: "then append reply"},
		{name: "chinese sentence boundary", text: "第一步完成。然后验证", want: "然后验证"},
		{name: "chinese comma boundary", text: "检查完毕，补上说明", want: "补上说明"},
		{name: "no boundary keeps whole", text: "fix the permission check", want: "fix the permission check"},
		{name: "strips list markers", text: "Amend tests:\n- then run them", want: "then run them"},
		{name: "strips numbered marker", text: "Steps:\n2) then verify", want: "then verify"},
		{name: "strips heading marker", text: "Steps:\n## then verify", want: "then verify"},
		{name: "empty input", text: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trailingActionClause(tt.text); got != tt.want {
				t.Fatalf("trailingActionClause(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
