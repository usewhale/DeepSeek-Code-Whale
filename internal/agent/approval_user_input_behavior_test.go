package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
	whaletools "github.com/usewhale/whale/internal/tools"
)

type approvalProvider struct {
	calls     int
	histories [][]Message
}

func (p *approvalProvider) StreamResponse(_ context.Context, history []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	p.histories = append(p.histories, append([]Message(nil), history...))
	if p.calls == 1 {
		return eventStream(toolUseEvent(toolCall("tc-w-1", "write", `{"file_path":"a.txt","content":"x"}`)))
	}
	return eventStream(endTurnEvent("done"))
}

type repeatedApprovalProvider struct {
	calls int
}

func (p *repeatedApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	if p.calls == 1 {
		return eventStream(toolUseEvent(
			toolCall("tc-w-1", "write", `{"file_path":"a.txt","content":"x"}`),
			toolCall("tc-w-2", "write", `{"file_path":"b.txt","content":"y"}`),
		))
	}
	return eventStream(endTurnEvent("done"))
}

type retryingDeniedToolProvider struct {
	calls int
}

func (p *retryingDeniedToolProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-retry-1", "write", `{"file_path":"a.txt","content":"x"}`)))
	case 2:
		return eventStream(toolUseEvent(toolCall("tc-retry-2", "write", `{"file_path":"b.txt","content":"y"}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

type requestUserInputProvider struct {
	calls int
}

func (p *requestUserInputProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	out := make(chan ProviderEvent, 1)
	p.calls++
	if p.calls == 1 {
		out <- ProviderEvent{
			Type: EventComplete,
			Response: &ProviderResponse{
				FinishReason: FinishReasonToolUse,
				ToolCalls: []ToolCall{
					{
						ID:    "rui-1",
						Name:  "request_user_input",
						Input: `{"questions":[{"header":"Mode","id":"mode","question":"Pick mode","options":[{"label":"Agent","description":"execute"},{"label":"Plan","description":"read-only"}]}]}`,
					},
				},
			},
		}
		close(out)
		return out
	}
	return eventStream(endTurnEvent("after-answer"))
}

func TestRequestUserInputRoundTrip(t *testing.T) {
	store := NewInMemoryStore()
	dir := t.TempDir()
	a := NewAgentWithRegistry(
		&requestUserInputProvider{},
		store,
		NewToolRegistry(nil),
		WithSessionsDir(dir),
		WithUserInputFunc(func(req UserInputRequest) (core.UserInputResponse, bool) {
			if req.ToolCall.Name != "request_user_input" || len(req.Questions) != 1 {
				t.Fatalf("unexpected user input request: %+v", req)
			}
			return core.UserInputResponse{
				Answers: []core.UserInputAnswer{
					{ID: "mode", Label: "Agent", Value: "Agent"},
				},
			}, true
		}),
	)
	msg, err := a.RunSession(context.Background(), "s-rui", "start")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if msg.Text != "after-answer" {
		t.Fatalf("unexpected final message: %+v", msg)
	}
	ust, err := session.LoadUserInputState(dir, "s-rui")
	if err != nil {
		t.Fatalf("load user input state: %v", err)
	}
	if ust.Pending {
		t.Fatalf("expected pending state cleared: %+v", ust)
	}
	all, _ := store.List(context.Background(), "s-rui")
	if len(all) < 3 {
		t.Fatalf("expected tool roundtrip messages, got %d", len(all))
	}
}

func TestApprovalRequiredAndDenied(t *testing.T) {
	store := NewInMemoryStore()
	prov := &approvalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalDeny
		}),
	)

	events, err := a.RunStream(context.Background(), "s-approval-deny", "go")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	var sawApproval bool
	var sawDenied bool
	var sawDone bool
	for ev := range events {
		if ev.Type == AgentEventTypeToolApprovalRequired {
			sawApproval = true
		}
		if ev.Type == AgentEventTypeToolResult && ev.Result != nil && ev.Result.IsError() {
			if strings.Contains(ev.Result.ModelText, "approval_denied") {
				sawDenied = true
			}
		}
		if ev.Type == AgentEventTypeDone {
			sawDone = true
		}
	}
	if !sawApproval {
		t.Fatal("expected approval required event")
	}
	if !sawDenied {
		t.Fatal("expected approval denied tool result")
	}
	if asked != 1 {
		t.Fatalf("expected asked=1, got %d", asked)
	}
	if prov.calls != 1 {
		t.Fatalf("expected provider to stop after denied approval, got calls=%d", prov.calls)
	}
	if !sawDone {
		t.Fatal("expected turn to finish after denied approval")
	}
	assertApprovalDeniedMarker(t, store, "s-approval-deny", "write")
}

func TestRunOptionsScopedShellAllowPrefixesDoNotLeak(t *testing.T) {
	store := NewInMemoryStore()
	prov := &oneToolProvider{tool: "shell_run", input: `{"command":"gh pr diff 123"}`}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{namedNoopTool("shell_run")}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalDeny
		}),
	)

	events, err := a.RunStreamWithTurnOptions(context.Background(), "s-scoped-allow", "go", RunOptions{
		ShellAllowPrefixes: []string{"gh pr diff"},
	})
	if err != nil {
		t.Fatalf("run stream with scoped allow failed: %v", err)
	}
	for ev := range events {
		if ev.Type == AgentEventTypeToolApprovalRequired {
			t.Fatalf("did not expect approval with scoped shell allow")
		}
	}
	if asked != 0 {
		t.Fatalf("approval callback called with scoped shell allow: %d", asked)
	}

	prov.calls = 0
	prov.input = `{"command":"curl https://example.com"}`
	events, err = a.RunStreamWithOptions(context.Background(), "s-no-scoped-allow", "go", false)
	if err != nil {
		t.Fatalf("run stream without scoped allow failed: %v", err)
	}
	var sawApproval bool
	for ev := range events {
		if ev.Type == AgentEventTypeToolApprovalRequired {
			sawApproval = true
		}
	}
	if !sawApproval || asked != 1 {
		t.Fatalf("expected approval without scoped allow, sawApproval=%v asked=%d", sawApproval, asked)
	}
}

func TestRunOptionsReadOnlyDeniesMutatingToolWithoutApproval(t *testing.T) {
	store := NewInMemoryStore()
	prov := &approvalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalDeny
		}),
	)

	events, err := a.RunStreamWithTurnOptions(context.Background(), "s-read-only-turn", "review", RunOptions{
		ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("run stream with read-only turn failed: %v", err)
	}
	var sawPolicyDeny bool
	var sawAuditToolResult bool
	for ev := range events {
		if ev.Type == AgentEventTypeToolApprovalRequired {
			t.Fatalf("read-only turn should deny mutating tools without approval")
		}
		if ev.Type == AgentEventTypeToolPolicyDecision && ev.Policy != nil && ev.Policy.Code == "read_only_turn_denied" && !ev.Policy.Allow {
			sawPolicyDeny = true
		}
		if ev.Type == AgentEventTypeToolResult && ev.Result != nil && ev.Result.ToolCallID == "tc-w-1" {
			if ev.Result.Metadata["ui_visibility"] != "audit" || ev.Result.Metadata["auto_denied"] != true || ev.Result.Metadata["policy_code"] != "read_only_turn_denied" {
				t.Fatalf("expected audit auto-deny metadata, got %+v", ev.Result.Metadata)
			}
			if !strings.Contains(ev.Result.ModelText, "do_not_retry_same_call") {
				t.Fatalf("expected model-facing retry guard in result, got %q", ev.Result.ModelText)
			}
			sawAuditToolResult = true
		}
	}
	if !sawPolicyDeny {
		t.Fatal("expected read-only policy denial result")
	}
	if !sawAuditToolResult {
		t.Fatal("expected audit-only policy denial tool result")
	}
	if asked != 0 {
		t.Fatalf("approval callback should not be called, got %d", asked)
	}
}

func TestRunOptionsReadOnlyRepeatedDenyAddsSingleNoticeMetadata(t *testing.T) {
	store := NewInMemoryStore()
	prov := &repeatedApprovalProvider{}
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
	)

	events, err := a.RunStreamWithTurnOptions(context.Background(), "s-read-only-repeat", "review", RunOptions{
		ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("run stream with read-only turn failed: %v", err)
	}
	var first, second *ToolResult
	for ev := range events {
		if ev.Type == AgentEventTypeToolResult && ev.Result != nil {
			switch ev.Result.ToolCallID {
			case "tc-w-1":
				cp := *ev.Result
				first = &cp
			case "tc-w-2":
				cp := *ev.Result
				second = &cp
			}
		}
	}
	if first == nil || second == nil {
		t.Fatalf("expected both denied tool results, first=%+v second=%+v", first, second)
	}
	if _, ok := first.Metadata["auto_deny_notice"]; ok {
		t.Fatalf("first auto-deny should stay audit-only without notice metadata: %+v", first.Metadata)
	}
	if second.Metadata["ui_visibility"] != "audit" || second.Metadata["auto_deny_repeat_count"] != 2 {
		t.Fatalf("second auto-deny should carry repeat audit metadata: %+v", second.Metadata)
	}
	if notice, _ := second.Metadata["auto_deny_notice"].(string); !strings.Contains(notice, "read-only") {
		t.Fatalf("expected repeat notice to mention read-only block, got %q", notice)
	}
}

func TestRunOptionsReadOnlyRepeatedDenyAcrossModelRetriesKeepsCount(t *testing.T) {
	store := NewInMemoryStore()
	prov := &retryingDeniedToolProvider{}
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
	)

	events, err := a.RunStreamWithTurnOptions(context.Background(), "s-read-only-retry-repeat", "review", RunOptions{
		ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("run stream with read-only turn failed: %v", err)
	}
	var first, second *ToolResult
	for ev := range events {
		if ev.Type == AgentEventTypeToolResult && ev.Result != nil {
			switch ev.Result.ToolCallID {
			case "tc-retry-1":
				cp := *ev.Result
				first = &cp
			case "tc-retry-2":
				cp := *ev.Result
				second = &cp
			}
		}
	}
	if first == nil || second == nil {
		t.Fatalf("expected both denied retry results, first=%+v second=%+v", first, second)
	}
	if _, ok := first.Metadata["auto_deny_repeat_count"]; ok {
		t.Fatalf("first denied retry should not carry repeat metadata: %+v", first.Metadata)
	}
	if second.Metadata["ui_visibility"] != "audit" || second.Metadata["auto_deny_repeat_count"] != 2 {
		t.Fatalf("second denied retry should carry repeat metadata across dispatches: %+v", second.Metadata)
	}
	if notice, _ := second.Metadata["auto_deny_notice"].(string); !strings.Contains(notice, "read-only") {
		t.Fatalf("expected retry repeat notice to mention read-only block, got %q", notice)
	}
}

func TestApprovalCancelDoesNotPersistDeniedMarker(t *testing.T) {
	store := NewInMemoryStore()
	prov := &approvalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalCancel
		}),
	)

	events, err := a.RunStream(context.Background(), "s-approval-cancel", "go")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	var sawApproval bool
	var sawCancelled bool
	for ev := range events {
		if ev.Type == AgentEventTypeToolApprovalRequired {
			sawApproval = true
		}
		if ev.Type == AgentEventTypeTurnCancelled {
			sawCancelled = true
		}
		if ev.Type == AgentEventTypeDone {
			t.Fatalf("unexpected done event after approval cancel")
		}
		if ev.Type == AgentEventTypeToolResult && ev.Result != nil && strings.Contains(ev.Result.ModelText, "approval_denied") {
			t.Fatalf("approval cancel produced denial result: %+v", ev.Result)
		}
	}
	if !sawApproval {
		t.Fatal("expected approval required event")
	}
	if !sawCancelled {
		t.Fatal("expected turn cancelled event")
	}
	if asked != 1 {
		t.Fatalf("expected asked=1, got %d", asked)
	}
	if prov.calls != 1 {
		t.Fatalf("expected provider to stop after canceled approval, got calls=%d", prov.calls)
	}
	msgs, err := store.List(context.Background(), "s-approval-cancel")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if historyContainsApprovalDeniedMarker(msgs, "write") {
		t.Fatalf("approval cancel should not persist approval-denied marker:\n%+v", msgs)
	}
	if !historyContainsInterruptedMarker(msgs) {
		t.Fatalf("expected approval cancel to persist interrupted marker:\n%+v", msgs)
	}
}

func TestApprovalDeniedMarkerIsVisibleToNextTurn(t *testing.T) {
	store := NewInMemoryStore()
	prov := &approvalProvider{}
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			return ApprovalDeny
		}),
	)

	events, err := a.RunStream(context.Background(), "s-approval-deny-next", "do the denied task")
	if err != nil {
		t.Fatalf("first run stream failed: %v", err)
	}
	for range events {
	}
	events, err = a.RunStream(context.Background(), "s-approval-deny-next", "make build")
	if err != nil {
		t.Fatalf("second run stream failed: %v", err)
	}
	for range events {
	}

	if prov.calls != 2 {
		t.Fatalf("expected provider calls=2, got %d", prov.calls)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("expected two provider histories, got %d", len(prov.histories))
	}
	if !historyContainsApprovalDeniedMarker(prov.histories[1], "write") {
		t.Fatalf("expected second provider history to include approval-denied marker:\n%+v", prov.histories[1])
	}
}

type multiToolApprovalProvider struct{}

func (p *multiToolApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	return eventStream(toolUseEvent(
		toolCall("tc-w-1", "write", `{"file_path":"a.txt","content":"x"}`),
		toolCall("tc-count-1", "counting", `{}`),
	))
}

type multiToolSubagentApprovalProvider struct{}

func (p *multiToolSubagentApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	return eventStream(toolUseEvent(
		toolCall("tc-subagent-1", "spawn_subagent", `{"role":"explore","task":"read files"}`),
		toolCall("tc-count-1", "counting", `{}`),
	))
}

type countingTool struct {
	calls int
}

func (c *countingTool) Name() string { return "counting" }
func (c *countingTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	c.calls++
	return ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: "ok"}, nil
}

func TestApprovalDeniedSkipsRemainingToolCalls(t *testing.T) {
	store := NewInMemoryStore()
	counting := &countingTool{}
	a := NewAgentWithRegistry(
		&multiToolApprovalProvider{},
		store,
		NewToolRegistry([]Tool{writeLikeTool{}, counting}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			return ApprovalDeny
		}),
	)

	events, err := a.RunStream(context.Background(), "s-approval-deny-multi", "go")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	for range events {
	}
	if counting.calls != 0 {
		t.Fatalf("expected later tool calls to be skipped after approval deny, got %d", counting.calls)
	}
	assertApprovalDeniedMarker(t, store, "s-approval-deny-multi", "write")
}

func TestSpawnSubagentApprovalDeniedSkipsRemainingToolCalls(t *testing.T) {
	store := NewInMemoryStore()
	counting := &countingTool{}
	a := NewAgentWithRegistry(
		&multiToolSubagentApprovalProvider{},
		store,
		NewToolRegistry([]Tool{namedNoopTool("spawn_subagent"), counting}),
		WithToolPolicy(RulePolicy{Default: PermissionAsk}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			return ApprovalDeny
		}),
	)

	events, err := a.RunStream(context.Background(), "s-subagent-approval-deny-multi", "go")
	if err != nil {
		t.Fatalf("run stream failed: %v", err)
	}
	for range events {
	}
	if counting.calls != 0 {
		t.Fatalf("expected later tool calls to be skipped after spawn_subagent approval deny, got %d", counting.calls)
	}
	assertApprovalDeniedMarker(t, store, "s-subagent-approval-deny-multi", "spawn_subagent")
}

func assertApprovalDeniedMarker(t *testing.T, store interface {
	List(context.Context, string) ([]Message, error)
}, sessionID, toolName string) {
	t.Helper()
	msgs, err := store.List(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if !historyContainsApprovalDeniedMarker(msgs, toolName) {
		t.Fatalf("expected approval-denied marker for %s in history:\n%+v", toolName, msgs)
	}
}

func historyContainsApprovalDeniedMarker(msgs []Message, toolName string) bool {
	for _, msg := range msgs {
		if msg.Role != RoleUser || !msg.Hidden || msg.FinishReason != FinishReasonCanceled {
			continue
		}
		if strings.Contains(msg.Text, "<approval_denied>") &&
			strings.Contains(msg.Text, "tool: "+toolName) &&
			strings.Contains(msg.Text, "switch to another tool to bypass") {
			return true
		}
	}
	return false
}

func historyContainsInterruptedMarker(msgs []Message) bool {
	for _, msg := range msgs {
		if msg.Role != RoleUser || !msg.Hidden || msg.FinishReason != FinishReasonCanceled {
			continue
		}
		if strings.Contains(msg.Text, "<turn_aborted>") {
			return true
		}
	}
	return false
}

type approvalCacheProvider struct {
	calls int
}

func (p *approvalCacheProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	if p.calls == 1 || p.calls == 2 {
		return eventStream(toolUseEvent(toolCall("tc-c-1", "write", `{"file_path":"a.txt","content":"x"}`)))
	}
	return eventStream(endTurnEvent("done"))
}

func TestApprovalAllowOnceDoesNotCacheBySessionKey(t *testing.T) {
	store := NewInMemoryStore()
	prov := &approvalCacheProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalAllow
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-approval-cache-once", "t1"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if asked != 2 {
		t.Fatalf("expected allow-once approval to ask twice for repeated key, got %d", asked)
	}
}

func TestApprovalAllowForSessionCachesBySessionKey(t *testing.T) {
	store := NewInMemoryStore()
	prov := &approvalCacheProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{writeLikeTool{}}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalAllowForSession
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-approval-cache", "t1"); err != nil {
		t.Fatalf("run1 failed: %v", err)
	}
	if _, err := a.RunSession(context.Background(), "s-approval-cache", "t2"); err != nil {
		t.Fatalf("run2 failed: %v", err)
	}
	if asked != 1 {
		t.Fatalf("expected asked once due to approval cache, got %d", asked)
	}
}

type semanticShellApprovalProvider struct {
	calls int
}

func (p *semanticShellApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-shell-1", "shell_run", `{"command":"go test ./internal/policy"}`)))
	case 2:
		return eventStream(toolUseEvent(toolCall("tc-shell-2", "shell_run", `{"command":"go test ./internal/tools"}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

func TestApprovalAllowForSessionCachesSemanticShellFamily(t *testing.T) {
	store := NewInMemoryStore()
	prov := &semanticShellApprovalProvider{}
	asked := 0
	var keys [][]string
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{namedNoopTool("shell_run")}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			keys = append(keys, append([]string(nil), req.Keys...))
			return ApprovalAllowForSession
		}),
		WithToolPolicy(DefaultToolPolicy{Rules: []PermissionRule{
			{Permission: "shell", Pattern: "go test *", Action: PermissionAsk},
		}}),
	)

	if _, err := a.RunSession(context.Background(), "s-semantic-shell-cache", "t1"); err != nil {
		t.Fatalf("run1 failed: %v", err)
	}
	if _, err := a.RunSession(context.Background(), "s-semantic-shell-cache", "t2"); err != nil {
		t.Fatalf("run2 failed: %v", err)
	}
	if asked != 1 {
		t.Fatalf("expected asked once due to semantic shell approval cache, got %d", asked)
	}
	if !reflect.DeepEqual(keys, [][]string{{"shell:bounded:go:test"}}) {
		t.Fatalf("approval keys = %v, want shell:bounded:go:test", keys)
	}
}

type externalReadApprovalProvider struct {
	path  string
	calls int
}

func (p *externalReadApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	if p.calls <= 2 {
		return eventStream(toolUseEvent(toolCall("tc-read", "read_file", `{"file_path":`+strconv.Quote(p.path)+`}`)))
	}
	return eventStream(endTurnEvent("done"))
}

func TestApprovalAllowForSessionCachesExternalReadDirectory(t *testing.T) {
	home, err := os.MkdirTemp(".", "whale-agent-ext-read-*")
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	workspace := filepath.Join(home, "workspace")
	external := filepath.Join(home, "external")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	externalFile := filepath.Join(external, "guide.txt")
	if err := os.WriteFile(externalFile, []byte("approved external read\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	toolset, err := whaletools.NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	store := NewInMemoryStore()
	prov := &externalReadApprovalProvider{path: externalFile}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry(toolset.Tools()),
		WithProjectMemory(false, 0, nil, workspace),
		WithToolPolicy(RulePolicy{Default: PermissionAllow, Rules: DefaultRules(), WorkspaceRoot: workspace}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			if got, want := req.Keys, []string{"grant:external_directory:" + filepath.ToSlash(external)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("external read approval keys = %v, want %v", got, want)
			}
			if req.Metadata["permission_kind"] != "external_directory" {
				t.Fatalf("permission metadata = %+v, want external_directory", req.Metadata)
			}
			return ApprovalAllowForSession
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-external-read", "read"); err != nil {
		t.Fatalf("run1 failed: %v", err)
	}
	if _, err := a.RunSession(context.Background(), "s-external-read", "read again"); err != nil {
		t.Fatalf("run2 failed: %v", err)
	}
	if asked != 1 {
		t.Fatalf("expected external read approval to be cached by directory, got %d prompts", asked)
	}
}

type externalReadToolsApprovalProvider struct {
	dir   string
	file  string
	calls int
}

func (p *externalReadToolsApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-read", "read_file", `{"file_path":`+strconv.Quote(p.file)+`}`)))
	case 2:
		return eventStream(toolUseEvent(toolCall("tc-grep", "grep", `{"path":`+strconv.Quote(p.dir)+`,"pattern":"approved"}`)))
	case 3:
		return eventStream(toolUseEvent(toolCall("tc-search", "search_files", `{"path":`+strconv.Quote(p.dir)+`,"pattern":"guide"}`)))
	case 4:
		return eventStream(toolUseEvent(toolCall("tc-list", "list_dir", `{"path":`+strconv.Quote(p.dir)+`}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

func TestApprovalAllowForSessionReusesExternalDirectoryGrantAcrossReadTools(t *testing.T) {
	home, err := os.MkdirTemp(".", "whale-agent-ext-read-tools-*")
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	workspace := filepath.Join(home, "workspace")
	external := filepath.Join(home, "external")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	externalFile := filepath.Join(external, "guide.txt")
	if err := os.WriteFile(externalFile, []byte("approved external read\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	toolset, err := whaletools.NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	store := NewInMemoryStore()
	prov := &externalReadToolsApprovalProvider{dir: external, file: externalFile}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry(toolset.Tools()),
		WithProjectMemory(false, 0, nil, workspace),
		WithToolPolicy(RulePolicy{Default: PermissionAllow, Rules: DefaultRules(), WorkspaceRoot: workspace}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			if got, want := req.Keys, []string{"grant:external_directory:" + filepath.ToSlash(external)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("external directory approval keys = %v, want %v", got, want)
			}
			return ApprovalAllowForSession
		}),
	)

	for i := 0; i < 4; i++ {
		if _, err := a.RunSession(context.Background(), "s-external-read-tools", "read"); err != nil {
			t.Fatalf("run %d failed: %v", i+1, err)
		}
	}
	if asked != 1 {
		t.Fatalf("expected one external directory approval across read tools, got %d prompts", asked)
	}
}

type namedNoopTool string

func (n namedNoopTool) Name() string { return string(n) }
func (n namedNoopTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: "ok"}, nil
}

type failingNamedTool string

func (f failingNamedTool) Name() string { return string(f) }
func (f failingNamedTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: `{"success":false,"error":"bad hunk","code":"patch_apply_failed"}`, Outcome: core.OutcomeFailure}, nil
}

type notFoundEditTool struct{}

func (n notFoundEditTool) Name() string { return "edit" }
func (n notFoundEditTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: `{"success":false,"error":"file not found","code":"not_found"}`, Outcome: core.OutcomeFailure}, nil
}

type fileScopedApprovalProvider struct {
	calls int
}

func (p *fileScopedApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-edit", "edit", `{"file_path":"a.txt","search":"old","replace":"new"}`)))
	case 3:
		return eventStream(toolUseEvent(toolCall("tc-write", "write", `{"file_path":"a.txt","content":"newer"}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

func TestApprovalAllowForSessionCachesEditAndWriteByFile(t *testing.T) {
	store := NewInMemoryStore()
	prov := &fileScopedApprovalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{namedNoopTool("edit"), namedNoopTool("write")}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			if got, want := req.Keys, []string{"file:a.txt"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("approval keys = %v, want %v", got, want)
			}
			return ApprovalAllowForSession
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-file-approval", "edit"); err != nil {
		t.Fatalf("run1 failed: %v", err)
	}
	if _, err := a.RunSession(context.Background(), "s-file-approval", "write"); err != nil {
		t.Fatalf("run2 failed: %v", err)
	}
	if asked != 1 {
		t.Fatalf("expected edit approval to cover later write to same file, got %d approvals", asked)
	}
}

type failedFileApprovalProvider struct {
	calls int
}

func (p *failedFileApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-bad-multi-edit", "multi_edit", `{"file_path":"a.txt","edits":[{"search":"missing","replace":"new"}]}`)))
	case 2:
		return eventStream(toolUseEvent(toolCall("tc-write-after-fail", "write", `{"file_path":"a.txt","content":"new"}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

func TestApprovalAllowForSessionDoesNotCacheFailedFileMutation(t *testing.T) {
	store := NewInMemoryStore()
	prov := &failedFileApprovalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{failingNamedTool("multi_edit"), namedNoopTool("write")}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalAllowForSession
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-failed-file-approval", "patch then write"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if asked != 2 {
		t.Fatalf("expected failed file mutation not to cache approval for later write, got %d approvals", asked)
	}
}

type unclassifiedFailedFileApprovalProvider struct {
	calls int
}

func (p *unclassifiedFailedFileApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-missing-edit", "edit", `{"file_path":"a.txt","search":"old","replace":"new"}`)))
	case 2:
		return eventStream(toolUseEvent(toolCall("tc-write-after-missing", "write", `{"file_path":"a.txt","content":"new"}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

func TestApprovalAllowForSessionDoesNotCacheUnclassifiedFailedFileMutation(t *testing.T) {
	store := NewInMemoryStore()
	prov := &unclassifiedFailedFileApprovalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{notFoundEditTool{}, namedNoopTool("write")}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalAllowForSession
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-unclassified-failed-file-approval", "edit then write"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if asked != 2 {
		t.Fatalf("expected unclassified failed file mutation not to cache approval for later write, got %d approvals", asked)
	}
}

type fallbackWriteApprovalProvider struct {
	calls int
}

func (p *fallbackWriteApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	if p.calls == 1 || p.calls == 2 {
		return eventStream(toolUseEvent(toolCall("tc-fallback-write", "write", `{"file_path":"a.txt","content":"x"}`)))
	}
	return eventStream(endTurnEvent("done"))
}

func TestApprovalAllowForSessionDoesNotCacheRecoveredReadonlyFallback(t *testing.T) {
	store := NewInMemoryStore()
	prov := &fallbackWriteApprovalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{failWriteTool{}, readOnlyViewTool{}}),
		WithToolPolicy(editApprovalPolicy()),
		WithRecoveryPolicy(RecoveryPolicy{
			Enabled: true,
			Rules: map[FailureClass]RecoveryRule{
				FailureClassExecFailed: {Action: RecoveryActionFallbackReadOnly, MaxAttempts: 0},
			},
		}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			return ApprovalAllowForSession
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-fallback-file-approval", "fallback then write"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if asked != 2 {
		t.Fatalf("expected readonly fallback not to cache file approval for later write, got %d approvals", asked)
	}
}

type patchScopedApprovalProvider struct {
	calls int
}

func (p *patchScopedApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-multi-a", "multi_edit", `{"file_path":"a.txt","edits":[{"search":"old","replace":"new"}]}`)))
	case 3:
		return eventStream(toolUseEvent(toolCall("tc-multi-a-again", "multi_edit", `{"file_path":"a.txt","edits":[{"search":"new","replace":"newer"}]}`)))
	case 5:
		return eventStream(toolUseEvent(toolCall("tc-multi-b", "multi_edit", `{"file_path":"b.txt","edits":[{"search":"old","replace":"new"}]}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

func TestApprovalAllowForSessionCachesMultiEditByFile(t *testing.T) {
	store := NewInMemoryStore()
	prov := &patchScopedApprovalProvider{}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry([]Tool{namedNoopTool("multi_edit")}),
		WithToolPolicy(editApprovalPolicy()),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			switch req.ToolCall.ID {
			case "tc-multi-a":
				if got, want := req.Keys, []string{"file:a.txt"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("first multi_edit approval keys = %v, want %v", got, want)
				}
			case "tc-multi-b":
				if got, want := req.Keys, []string{"file:b.txt"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("third multi_edit approval keys = %v, want %v", got, want)
				}
			default:
				t.Fatalf("unexpected approval for %s", req.ToolCall.ID)
			}
			return ApprovalAllowForSession
		}),
	)

	if _, err := a.RunSession(context.Background(), "s-multi-edit-approval", "edit a"); err != nil {
		t.Fatalf("run1 failed: %v", err)
	}
	if _, err := a.RunSession(context.Background(), "s-multi-edit-approval", "edit a again"); err != nil {
		t.Fatalf("run2 failed: %v", err)
	}
	if _, err := a.RunSession(context.Background(), "s-multi-edit-approval", "edit b"); err != nil {
		t.Fatalf("run3 failed: %v", err)
	}
	if asked != 2 {
		t.Fatalf("expected approvals for first file and later new file only, got %d", asked)
	}
}

// toolResultReadAutoApprovalProvider emits a sequence of read-only filesystem
// tool calls that target the tool-results archive directory.  read_file, grep
// and search_files should be auto-allowed; list_dir should still require
// approval.
type toolResultReadAutoApprovalProvider struct {
	sessionFile string
	siblingFile string
	calls       int
}

func (p *toolResultReadAutoApprovalProvider) StreamResponse(_ context.Context, _ []Message, _ []Tool) <-chan ProviderEvent {
	p.calls++
	switch p.calls {
	case 1:
		return eventStream(toolUseEvent(toolCall("tc-read", "read_file", `{"file_path":`+strconv.Quote(p.sessionFile)+`}`)))
	case 2:
		return eventStream(toolUseEvent(toolCall("tc-grep", "grep", `{"path":`+strconv.Quote(filepath.Dir(p.sessionFile))+`,"pattern":"needle"}`)))
	case 3:
		return eventStream(toolUseEvent(toolCall("tc-search", "search_files", `{"path":`+strconv.Quote(filepath.Dir(p.sessionFile))+`,"pattern":"needle"}`)))
	case 4:
		return eventStream(toolUseEvent(toolCall("tc-list", "list_dir", `{"path":`+strconv.Quote(filepath.Dir(p.sessionFile))+`}`)))
	case 5:
		return eventStream(toolUseEvent(toolCall("tc-sibling-read", "read_file", `{"file_path":`+strconv.Quote(p.siblingFile)+`}`)))
	default:
		return eventStream(endTurnEvent("done"))
	}
}

func TestToolResultReadAutoApproval(t *testing.T) {
	home, err := os.MkdirTemp(".", "whale-agent-tr-auto-*")
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	workspace := filepath.Join(home, "workspace")
	dataDir := filepath.Join(home, ".whale")
	sessionID := "sess-1"
	siblingID := "sess-2"

	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	toolResultsDir := filepath.Join(dataDir, "tool-results")
	sessionDir := core.ToolResultArchiveSessionDir(toolResultsDir, sessionID)
	siblingDir := core.ToolResultArchiveSessionDir(toolResultsDir, siblingID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siblingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(sessionDir, "output.txt")
	siblingFile := filepath.Join(siblingDir, "output.txt")
	if err := os.WriteFile(sessionFile, []byte("current session output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingFile, []byte("other session output\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	toolset, err := whaletools.NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	store := NewInMemoryStore()
	prov := &toolResultReadAutoApprovalProvider{sessionFile: sessionFile, siblingFile: siblingFile}
	asked := 0
	a := NewAgentWithRegistry(
		prov,
		store,
		NewToolRegistry(toolset.Tools()),
		WithProjectMemory(false, 0, nil, workspace),
		WithUsageLogPath(filepath.Join(dataDir, "usage")),
		WithToolPolicy(RulePolicy{Default: PermissionAllow, Rules: DefaultRules(), WorkspaceRoot: workspace}),
		WithApprovalFunc(func(req ApprovalRequest) ApprovalDecision {
			asked++
			if req.ToolCall.ID == "tc-list" {
				return ApprovalAllowForSession
			}
			if req.ToolCall.ID == "tc-sibling-read" {
				return ApprovalAllowForSession
			}
			t.Fatalf("unexpected approval request for tool %s (%s)", req.ToolCall.ID, req.ToolCall.Name)
			return ApprovalDeny
		}),
	)

	if _, err := a.RunSession(context.Background(), sessionID, "read tool results"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	// Only list_dir (tc-list) and sibling-session read (tc-sibling-read)
	// should prompt for approval.  read_file, grep and search_files on the
	// current session's archive must be auto-allowed.
	if asked != 2 {
		t.Fatalf("expected 2 approval prompts (list_dir + sibling read), got %d", asked)
	}
}
