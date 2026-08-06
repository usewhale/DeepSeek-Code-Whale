package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type cancelThenSummaryProvider struct {
	calls int
}

func (p *cancelThenSummaryProvider) StreamResponse(ctx context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	out := make(chan ProviderEvent, 1)
	p.calls++
	if p.calls == 1 {
		go func() {
			defer close(out)
			<-ctx.Done()
			out <- ProviderEvent{Type: EventError, Err: ctx.Err()}
		}()
		return out
	}
	return eventStream(endTurnEvent("forced summary"))
}

func TestRunStreamCancelCurrentTurn(t *testing.T) {
	store := NewInMemoryStore()
	prov := &cancelThenSummaryProvider{}
	a := NewAgent(prov, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := a.RunStream(ctx, "s-cancel", "hi")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	time.AfterFunc(10*time.Millisecond, cancel)

	seenCancelled := false
	for ev := range events {
		switch ev.Type {
		case AgentEventTypeTurnCancelled:
			seenCancelled = true
		case AgentEventTypeForcedSummaryStarted, AgentEventTypeForcedSummaryDone, AgentEventTypeDone:
			t.Fatalf("unexpected event after cancel: %s", ev.Type)
		}
	}

	if !seenCancelled {
		t.Fatal("expected turn_cancelled event")
	}
	if got := prov.calls; got != 1 {
		t.Fatalf("expected cancel path not to trigger extra summary request, got provider calls=%d", got)
	}
	msgs, _ := store.List(context.Background(), "s-cancel")
	if len(msgs) == 0 {
		t.Fatal("expected persisted messages")
	}
	last := msgs[len(msgs)-1]
	if last.Role != RoleUser || !last.Hidden || last.FinishReason != FinishReasonCanceled || !strings.Contains(last.Text, "<turn_aborted>") {
		t.Fatalf("expected hidden interrupt marker, got: %+v", last)
	}
}

func TestRunStreamServiceShutdownDoesNotPersistUserInterruptMarker(t *testing.T) {
	store := NewInMemoryStore()
	a := NewAgent(&cancelThenSummaryProvider{}, store, nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(10*time.Millisecond, func() { cancel(ErrServiceShutdown) })

	events, err := a.RunStream(ctx, "s-service-shutdown", "hi")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	seenCancelled := false
	for ev := range events {
		if ev.Type == AgentEventTypeTurnCancelled {
			seenCancelled = true
		}
	}
	if !seenCancelled {
		t.Fatal("expected turn_cancelled event")
	}

	msgs, _ := store.List(context.Background(), "s-service-shutdown")
	for _, msg := range msgs {
		if strings.Contains(msg.Text, "<turn_aborted>") {
			t.Fatalf("service shutdown must not persist a user interrupt marker: %+v", msg)
		}
	}
}

func TestRunCancelCurrentTurnReturnsContextCanceled(t *testing.T) {
	store := NewInMemoryStore()
	a := NewAgent(&cancelThenSummaryProvider{}, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)

	_, err := a.RunSession(ctx, "s-cancel-run", "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

type blockingCancelTool struct{}

func (b blockingCancelTool) Name() string { return "blocking_cancel" }
func (b blockingCancelTool) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	<-ctx.Done()
	return ToolResult{}, ctx.Err()
}

type cancelYieldsTaskTool struct{}

func (c cancelYieldsTaskTool) Name() string { return "cancel_yields_task" }
func (c cancelYieldsTaskTool) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	<-ctx.Done()
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  `{"success":true,"code":"ok","data":{"status":"running","diagnosis":{"reason":"yield_interrupted"},"payload":{"task_id":"task-cancel-1","done":false}}}`,
	}, nil
}

func TestRunStreamCancelDuringToolSkipsRecovery(t *testing.T) {
	store := NewInMemoryStore()
	prov := &oneToolProvider{tool: "blocking_cancel", input: `{}`}
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{blockingCancelTool{}}),
		WithToolPolicy(RulePolicy{Default: PermissionAllow}),
		WithRecoveryPolicy(DefaultRecoveryPolicy()),
	)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := a.RunStream(ctx, "s-tool-cancel", "go")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	time.AfterFunc(10*time.Millisecond, cancel)

	var seenCancelled bool
	for ev := range events {
		switch ev.Type {
		case AgentEventTypeTurnCancelled:
			seenCancelled = true
		case AgentEventTypeToolRecoveryScheduled, AgentEventTypeToolRecoveryAttempt, AgentEventTypeToolRecoveryExhausted, AgentEventTypeReplanRequiredSet:
			t.Fatalf("unexpected recovery event after cancel: %s", ev.Type)
		case AgentEventTypeToolResult:
			if ev.Result != nil && strings.Contains(ev.Result.ModelText, "request_replan") {
				t.Fatalf("unexpected replan result after cancel: %s", ev.Result.ModelText)
			}
		}
	}
	if !seenCancelled {
		t.Fatal("expected turn_cancelled event")
	}
	msgs, _ := store.List(context.Background(), "s-tool-cancel")
	last := msgs[len(msgs)-1]
	if last.Role != RoleUser || !last.Hidden || last.FinishReason != FinishReasonCanceled || !strings.Contains(last.Text, "<turn_aborted>") {
		t.Fatalf("expected hidden interrupt marker, got: %+v", last)
	}
}

func TestRunStreamCancelDuringToolPersistsReturnedTaskResult(t *testing.T) {
	store := NewInMemoryStore()
	prov := &oneToolProvider{tool: "cancel_yields_task", input: `{}`}
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{cancelYieldsTaskTool{}}),
		WithToolPolicy(RulePolicy{Default: PermissionAllow}),
		WithRecoveryPolicy(DefaultRecoveryPolicy()),
	)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := a.RunStream(ctx, "s-tool-cancel-task", "go")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	time.AfterFunc(10*time.Millisecond, cancel)

	var seenTaskResult bool
	for ev := range events {
		if ev.Type == AgentEventTypeToolResult && ev.Result != nil {
			if strings.Contains(ev.Result.ModelText, `"task_id":"task-cancel-1"`) && strings.Contains(ev.Result.ModelText, `"yield_interrupted"`) {
				seenTaskResult = true
			}
		}
		if ev.Type == AgentEventTypeToolRecoveryScheduled || ev.Type == AgentEventTypeToolRecoveryAttempt {
			t.Fatalf("unexpected recovery event after cancel: %s", ev.Type)
		}
	}
	if !seenTaskResult {
		t.Fatal("expected interrupted tool task result event")
	}
	msgs, _ := store.List(context.Background(), "s-tool-cancel-task")
	if len(msgs) < 4 {
		t.Fatalf("expected user, assistant, tool, interrupt marker messages, got %d: %+v", len(msgs), msgs)
	}
	toolMsg := msgs[len(msgs)-2]
	if toolMsg.Role != RoleTool || len(toolMsg.ToolResults) != 1 || !strings.Contains(toolMsg.ToolResults[0].ModelText, `"task_id":"task-cancel-1"`) {
		t.Fatalf("expected persisted task tool result before interrupt marker, got: %+v", toolMsg)
	}
	last := msgs[len(msgs)-1]
	if last.Role != RoleUser || !last.Hidden || last.FinishReason != FinishReasonCanceled || !strings.Contains(last.Text, "<turn_aborted>") {
		t.Fatalf("expected hidden interrupt marker, got: %+v", last)
	}
}

type manyToolsAfterCancelProvider struct{}

func (p manyToolsAfterCancelProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	calls := make([]ToolCall, 0, 25)
	calls = append(calls, toolCall("tc-cancel", "cancel_yields_task", `{}`))
	for i := 0; i < 24; i++ {
		calls = append(calls, toolCall(fmt.Sprintf("tc-skip-%02d", i), fmt.Sprintf("skip_tool_%02d", i), `{}`))
	}
	return eventStream(toolUseEvent(calls...))
}

type signalCancelTaskTool struct {
	started chan struct{}
}

func (s signalCancelTaskTool) Name() string { return "cancel_yields_task" }
func (s signalCancelTaskTool) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	close(s.started)
	<-ctx.Done()
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  `{"success":true,"code":"ok","data":{"status":"running","diagnosis":{"reason":"yield_interrupted"},"payload":{"task_id":"task-cancel-1","done":false}}}`,
	}, nil
}

func TestRunStreamCancelDuringToolDoesNotBlockOnUndrainedSkippedEvents(t *testing.T) {
	store := NewInMemoryStore()
	started := make(chan struct{})
	tools := []Tool{signalCancelTaskTool{started: started}}
	for i := 0; i < 24; i++ {
		tools = append(tools, namedTestTool{name: fmt.Sprintf("skip_tool_%02d", i)})
	}
	a := NewAgentWithRegistry(
		manyToolsAfterCancelProvider{},
		store,
		NewToolRegistry(tools),
		WithToolPolicy(RulePolicy{Default: PermissionAllow}),
		WithRecoveryPolicy(DefaultRecoveryPolicy()),
	)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := a.RunStream(ctx, "s-tool-cancel-skips-undrained", "go")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	for {
		select {
		case <-started:
			goto cancelStartedTool
		case _, ok := <-events:
			if !ok {
				t.Fatal("stream closed before cancel_yields_task started")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("expected cancel_yields_task to start")
		}
	}
cancelStartedTool:
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		if _, loaded := a.active.Load("s-tool-cancel-skips-undrained"); !loaded {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected canceled stream with undrained skipped result events to release active session")
		case <-time.After(10 * time.Millisecond):
		}
	}

	msgs, _ := store.List(context.Background(), "s-tool-cancel-skips-undrained")
	if len(msgs) < 3 {
		t.Fatalf("expected user, assistant, and tool messages, got %d: %+v", len(msgs), msgs)
	}
	toolMsg := msgs[len(msgs)-1]
	if toolMsg.Role != RoleTool && len(msgs) >= 2 {
		toolMsg = msgs[len(msgs)-2]
	}
	if toolMsg.Role != RoleTool || len(toolMsg.ToolResults) != 25 {
		t.Fatalf("expected persisted task result plus skipped results, got: %+v", toolMsg)
	}
	if !strings.Contains(toolMsg.ToolResults[0].ModelText, `"task_id":"task-cancel-1"`) {
		t.Fatalf("expected first result to be returned task, got: %+v", toolMsg.ToolResults[0])
	}
	for _, result := range toolMsg.ToolResults[1:] {
		if !result.IsError() || !strings.Contains(result.ModelText, "turn_aborted") {
			t.Fatalf("expected skipped result after cancel, got: %+v", result)
		}
	}
}

type repairAndStormProvider struct {
	calls int
}

func (p *repairAndStormProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	if p.calls == 1 {
		// Six byte-identical (truncated) calls: all are repaired, and the storm
		// breaker blocks the one that exceeds the threshold (now 5-within-10).
		return eventStream(toolUseEvent(
			toolCall("tc-1", "echo_json", `{"x":1`),
			toolCall("tc-2", "echo_json", `{"x":1`),
			toolCall("tc-3", "echo_json", `{"x":1`),
			toolCall("tc-4", "echo_json", `{"x":1`),
			toolCall("tc-5", "echo_json", `{"x":1`),
			toolCall("tc-6", "echo_json", `{"x":1`),
		))
	}
	return eventStream(endTurnEvent("ok"))
}

type echoJSONTool struct{}

func (e echoJSONTool) Name() string { return "echo_json" }
func (e echoJSONTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	var v map[string]any
	if err := json.Unmarshal([]byte(call.Input), &v); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: "ok"}, nil
}

func TestRunStreamEmitsRepairAndBlockedEvents(t *testing.T) {
	store := NewInMemoryStore()
	a := NewAgent(&repairAndStormProvider{}, store, []Tool{echoJSONTool{}})

	events, err := a.RunStream(context.Background(), "s-repair", "hi")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}

	var repaired int
	var blocked int
	for ev := range events {
		switch ev.Type {
		case AgentEventTypeToolArgsRepaired:
			repaired++
		case AgentEventTypeToolCallBlocked:
			blocked++
		case AgentEventTypeError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if repaired != 6 {
		t.Fatalf("expected 6 repaired events, got %d", repaired)
	}
	if blocked != 1 {
		t.Fatalf("expected 1 blocked event, got %d", blocked)
	}
}
