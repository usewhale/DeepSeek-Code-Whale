package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/defaults"
)

// mutatingArgLoopProvider re-issues a mutating tool call with fresh arguments
// every round — the loop class invisible to BOTH dynamic guards: the storm
// breaker keys on identical calls (args differ here), and the progress guard
// never flags mutating calls (stalled() returns false for any non-read-only
// call, by design). Only an explicit tool-iteration cap terminates it. This is
// the regression the earlier "drop the cap entirely" design would have caused.
type mutatingArgLoopProvider struct{ calls int }

func (p *mutatingArgLoopProvider) StreamResponse(_ context.Context, _ []Message, tools []Tool) <-chan ProviderEvent {
	p.calls++
	if len(tools) == 0 {
		return eventStream(endTurnEvent("forced summary"))
	}
	input := fmt.Sprintf(`{"op":"write","n":%d,"content":%q}`, p.calls, strings.Repeat("x", p.calls))
	return eventStream(toolUseEvent(toolCall(fmt.Sprintf("tc-%d", p.calls), "echo", input)))
}

func TestAgentToolIterCapStopsMutatingArgLoop(t *testing.T) {
	store := NewInMemoryStore()
	prov := &mutatingArgLoopProvider{}
	a := NewAgentWithRegistry(prov, store, core.NewToolRegistry([]core.Tool{echoTool{}}),
		WithMaxToolIters(defaults.DefaultMaxToolIters))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events, err := a.RunStream(ctx, "s-mutloop", "go")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	var forced bool
	var done *core.Message
	for ev := range events {
		switch ev.Type {
		case AgentEventTypeForcedSummaryStarted:
			if ev.Content == "tool iteration cap reached" {
				forced = true
			}
		case AgentEventTypeDone:
			done = ev.Message
		case AgentEventTypeError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if !forced {
		t.Fatal("expected forced summary from the tool-iteration cap (mutating-arg loop must not run past the cap)")
	}
	if done == nil || !strings.Contains(done.Text, "auto-interrupted") {
		t.Fatalf("expected truncation banner in final message, got %+v", done)
	}
	// The cap must bound the loop exactly: neither the storm breaker nor the
	// progress guard fired, so 300 tool rounds is the only stop. The provider
	// is called once more for the forced summary itself, hence cap+1 total.
	if prov.calls != defaults.DefaultMaxToolIters+1 {
		t.Fatalf("provider calls = %d, want %d (300 capped loop rounds + 1 summary request)", prov.calls, defaults.DefaultMaxToolIters+1)
	}
}

// healthyLongTurnProvider issues a long sequence of distinct productive tool
// rounds and then answers normally. 160 rounds exceeds the old 100-iteration
// ACP cap — the RC2 failure mode — but must complete cleanly under the raised
// 300 cap with no forced summary.
type healthyLongTurnProvider struct {
	calls  int
	rounds int
}

func (p *healthyLongTurnProvider) StreamResponse(_ context.Context, _ []Message, tools []Tool) <-chan ProviderEvent {
	p.calls++
	if len(tools) == 0 {
		return eventStream(endTurnEvent("forced summary"))
	}
	if p.calls <= p.rounds {
		input := fmt.Sprintf(`{"op":"edit","file":"a.go","n":%d,"delta":%q}`, p.calls, fmt.Sprintf("hunk-%d", p.calls))
		return eventStream(toolUseEvent(toolCall(fmt.Sprintf("tc-%d", p.calls), "echo", input)))
	}
	return eventStream(endTurnEvent("all done"))
}

func TestAgentLongHealthyTurnCompletesUnderRaisedCap(t *testing.T) {
	const rounds = 160 // > old ACP cap (100), < DefaultMaxToolIters (300)
	store := NewInMemoryStore()
	prov := &healthyLongTurnProvider{rounds: rounds}
	a := NewAgentWithRegistry(prov, store, core.NewToolRegistry([]core.Tool{echoTool{}}),
		WithMaxToolIters(defaults.DefaultMaxToolIters))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	events, err := a.RunStream(ctx, "s-healthy", "implement")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	var forced bool
	var done *core.Message
	for ev := range events {
		switch ev.Type {
		case AgentEventTypeForcedSummaryStarted:
			forced = true
		case AgentEventTypeDone:
			done = ev.Message
		case AgentEventTypeError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if forced {
		t.Fatal("healthy long turn was truncated by a forced summary")
	}
	if done == nil {
		t.Fatal("expected Done event")
	}
	if done.FinishReason != FinishReasonEndTurn {
		t.Fatalf("finish reason = %s, want end_turn", done.FinishReason)
	}
	if strings.Contains(done.Text, "auto-interrupted") {
		t.Fatalf("final message carries truncation banner: %q", done.Text)
	}
	if prov.calls != rounds+1 {
		t.Fatalf("provider calls = %d, want %d (rounds + final answer)", prov.calls, rounds+1)
	}
}
