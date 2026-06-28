package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/telemetry"
)

type streamDispatchContext struct {
	SessionID      string
	Assistant      core.Message
	Model          string
	Policy         policy.ToolPolicy
	Tools          *core.ToolRegistry
	Events         chan<- AgentEvent
	AutoDenyCounts map[string]int
}

type toolApprovalResult struct {
	GrantOnSuccess    bool
	GrantKey          string
	GrantKeys         []string
	ExternalReadRoots []string
	ToolMessage       *core.Message
	AbortTurn         bool
}

func (a *Agent) prepareToolDispatches(ctx context.Context, sessionID, model string, assistant core.Message, events chan<- AgentEvent, tools *core.ToolRegistry) ([]core.ToolCall, []core.ToolResult, error) {
	dispatchCalls := assistant.ToolCalls
	blocked := []core.ToolResult{}
	if a.repairer == nil {
		return dispatchCalls, blocked, nil
	}
	emit := func(ev AgentEvent) bool {
		return sendAgentEvent(ctx, events, ev)
	}
	allowed := map[string]bool{}
	for _, spec := range tools.Specs() {
		if !a.toolVisibleForRepairScavenge(spec.Name) {
			continue
		}
		allowed[spec.Name] = true
	}
	isMutating := func(c core.ToolCall) bool {
		spec, ok := tools.Spec(c.Name)
		if !ok {
			return true
		}
		return !core.IsReadOnlyToolCall(spec, c)
	}
	var report repairReport
	dispatchCalls, blocked, report = a.repairer.process(assistant.ToolCalls, assistant.Reasoning, assistant.Text, allowed, isMutating)
	if report.scavenged > 0 {
		if !emit(AgentEvent{Type: AgentEventTypeToolCallScavenged, Scavenged: &ToolCallScavenged{Count: report.scavenged}}) {
			return nil, nil, ctx.Err()
		}
	}
	if report.truncationsFixed > 0 {
		for i := range report.repairedCalls {
			a.recordToolInputRepair(sessionID, model, assistant.ID, report.repairedCalls[i], "truncated_json")
			if !emit(AgentEvent{
				Type: AgentEventTypeToolArgsRepaired,
				ToolArgsRepair: &ToolArgsRepair{
					ToolCallIndex: i,
					ToolName:      report.repairedCalls[i].Name,
				},
			}) {
				return nil, nil, ctx.Err()
			}
		}
	}
	return dispatchCalls, blocked, nil
}

func (a *Agent) toolVisibleForRepairScavenge(name string) bool {
	if a == nil || a.mode != session.ModePlan {
		return true
	}
	return name != "update_plan"
}

func (a *Agent) dispatchToolCalls(ctx context.Context, sc streamDispatchContext, dispatchCalls []core.ToolCall, blocked []core.ToolResult) (*core.Message, bool, error) {
	if sc.AutoDenyCounts == nil {
		sc.AutoDenyCounts = map[string]int{}
	}
	results := make([]core.ToolResult, 0, len(sc.Assistant.ToolCalls))
	if err := appendBlockedToolResults(ctx, sc, blocked, &results); err != nil {
		return nil, false, err
	}

	pendingParallelSubagents := []preparedToolDispatch{}
	pendingParallelReasoning := []preparedToolDispatch{}
	flushPendingParallelSubagents := func() error {
		if len(pendingParallelSubagents) == 0 {
			return nil
		}
		pending := append([]preparedToolDispatch(nil), pendingParallelSubagents...)
		pendingParallelSubagents = pendingParallelSubagents[:0]
		return a.flushPendingParallelSubagents(ctx, sc.SessionID, sc.Assistant.ID, sc.Model, pending, sc.Events, &results, sc.Tools)
	}
	flushPendingParallelReasoning := func() error {
		if len(pendingParallelReasoning) == 0 {
			return nil
		}
		pending := append([]preparedToolDispatch(nil), pendingParallelReasoning...)
		pendingParallelReasoning = pendingParallelReasoning[:0]
		return a.flushPendingParallelReasoning(ctx, sc.SessionID, sc.Assistant.ID, sc.Model, pending, sc.Events, &results, sc.Tools)
	}
	flushPendingParallelBatches := func() error {
		if err := flushPendingParallelSubagents(); err != nil {
			return err
		}
		return flushPendingParallelReasoning()
	}
	for i, call := range dispatchCalls {
		if call.Name != parallelSubagentToolName {
			if err := flushPendingParallelSubagents(); err != nil {
				return nil, false, err
			}
		}
		if call.Name != parallelReasonToolName {
			if err := flushPendingParallelReasoning(); err != nil {
				return nil, false, err
			}
		}
		var err error
		call, err = a.repairDispatchInput(ctx, sc, i, call)
		if err != nil {
			return nil, false, err
		}

		spec, skipCall, err := a.resolveDispatchSpec(ctx, sc, call, flushPendingParallelBatches, &results)
		if err != nil {
			return nil, false, err
		}
		if skipCall {
			continue
		}

		var preHookContext string
		var hookBlocked bool
		call, preHookContext, hookBlocked, err = a.runPreToolUseHook(ctx, sc, call, flushPendingParallelBatches, &results)
		if err != nil {
			return nil, false, err
		}
		if hookBlocked {
			continue
		}

		modeBlocked, err := a.appendModeBlockedResult(ctx, sc, spec, call, flushPendingParallelBatches, &results)
		if err != nil {
			return nil, false, err
		}
		if modeBlocked {
			continue
		}

		handled, err := a.dispatchPreApprovalSpecialTool(ctx, sc, call, &results)
		if err != nil {
			return nil, false, err
		}
		if handled {
			continue
		}

		decision := sc.Policy.Decide(spec, call)
		if err := emitPolicyDecision(ctx, sc, call, decision); err != nil {
			return nil, false, err
		}
		if !decision.Allow {
			if err := flushPendingParallelBatches(); err != nil {
				return nil, false, err
			}
			if err := a.appendPolicyDeniedResult(ctx, sc, call, decision, false, &results); err != nil {
				return nil, false, err
			}
			continue
		}

		approval, err := a.resolveToolApproval(ctx, sc, spec, call, decision, flushPendingParallelBatches, &results)
		if err != nil {
			return nil, false, err
		}
		if approval.AbortTurn {
			return approval.ToolMessage, true, nil
		}

		prepared := preparedToolDispatch{
			Index:             i,
			Call:              call,
			PreHookContext:    preHookContext,
			GrantOnSuccess:    approval.GrantOnSuccess,
			GrantKey:          approval.GrantKey,
			GrantKeys:         approval.GrantKeys,
			ExternalReadRoots: approval.ExternalReadRoots,
		}
		if _, ok := maybeReadyParallelSubagentCall(i, call); ok {
			pendingParallelSubagents = append(pendingParallelSubagents, prepared)
			continue
		}
		if maybeReadyParallelReasonCall(call) {
			pendingParallelReasoning = append(pendingParallelReasoning, prepared)
			continue
		}

		handled, err = a.dispatchPostApprovalSpecialTool(ctx, sc, call, &results)
		if err != nil {
			return nil, false, err
		}
		if handled {
			continue
		}

		abortTurn, err := a.dispatchStandardTool(ctx, sc, prepared, &results)
		if err != nil {
			return nil, false, err
		}
		if abortTurn {
			requireEventDelivery := ctx.Err() == nil
			if err := appendAbortSkippedToolResults(ctx, sc, &results, requireEventDelivery); err != nil {
				return nil, false, err
			}
			toolMsg, err := a.createDispatchToolMessage(persistenceContext(ctx), sc, results)
			if err != nil {
				return nil, false, err
			}
			return &toolMsg, true, nil
		}
	}
	if err := flushPendingParallelBatches(); err != nil {
		return nil, false, err
	}

	toolMsg, err := a.createDispatchToolMessage(ctx, sc, results)
	if err != nil {
		return nil, false, err
	}
	return &toolMsg, false, nil
}

func emitDispatchEvent(ctx context.Context, sc streamDispatchContext, ev AgentEvent) error {
	if !sendAgentEvent(ctx, sc.Events, ev) {
		return ctx.Err()
	}
	return nil
}

func appendToolResult(ctx context.Context, sc streamDispatchContext, results *[]core.ToolResult, tr core.ToolResult) error {
	// Results produced outside the dispatch funnel (special tools, blocked
	// markers, recovery wrappers) get their channel-separated fields here,
	// before the event is emitted and the result is persisted.
	tr = core.FinalizeToolResultChannels(tr)
	*results = append(*results, tr)
	return emitDispatchEvent(ctx, sc, AgentEvent{Type: AgentEventTypeToolResult, Result: &tr})
}

func appendAbortSkippedToolResults(ctx context.Context, sc streamDispatchContext, results *[]core.ToolResult, requireEventDelivery bool) error {
	answered := make(map[string]bool, len(*results))
	for _, result := range *results {
		if result.ToolCallID != "" {
			answered[result.ToolCallID] = true
		}
	}
	for _, call := range sc.Assistant.ToolCalls {
		if call.ID == "" || answered[call.ID] {
			continue
		}
		tr := core.FinalizeToolResultChannels(core.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			ModelText:  `{"success":false,"error":"tool skipped because another tool requested a runtime handoff","code":"turn_aborted"}`,
			Code:       "turn_aborted",
		})
		*results = append(*results, tr)
		if err := emitDispatchEvent(ctx, sc, AgentEvent{Type: AgentEventTypeToolResult, Result: &tr}); err != nil && requireEventDelivery {
			return err
		}
		answered[call.ID] = true
	}
	return nil
}

func appendBlockedToolResults(ctx context.Context, sc streamDispatchContext, blocked []core.ToolResult, results *[]core.ToolResult) error {
	for _, blockedRes := range blocked {
		br := blockedRes
		reasonCode := "storm_blocked"
		if br.Metadata != nil {
			if raw, ok := br.Metadata["blocked_reason_code"].(string); ok && strings.TrimSpace(raw) != "" {
				reasonCode = strings.TrimSpace(raw)
			}
		}
		if err := emitDispatchEvent(ctx, sc, AgentEvent{
			Type: AgentEventTypeToolCallBlocked,
			ToolBlocked: &ToolCallBlocked{
				ToolCallID: br.ToolCallID,
				ToolName:   br.Name,
				ReasonCode: reasonCode,
			},
		}); err != nil {
			return err
		}
		if err := appendToolResult(ctx, sc, results, br); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) repairDispatchInput(ctx context.Context, sc streamDispatchContext, index int, call core.ToolCall) (core.ToolCall, error) {
	spec, ok := sc.Tools.Spec(call.Name)
	if !ok {
		return call, nil
	}
	if fixed, changed := core.RenestFlatInputForSpec(spec, call.Input); changed {
		call.Input = fixed
		a.recordToolInputRepair(sc.SessionID, sc.Model, sc.Assistant.ID, call, "renest_flat_input")
		if err := emitToolArgsRepaired(ctx, sc, index, call.Name); err != nil {
			return core.ToolCall{}, err
		}
	}
	if fixed, repairs := core.RepairToolInputForSpec(spec, call.Input); len(repairs) > 0 {
		call.Input = fixed
		for _, repair := range repairs {
			a.recordToolInputRepairDetail(sc.SessionID, sc.Model, sc.Assistant.ID, call, repair)
		}
		if err := emitToolArgsRepaired(ctx, sc, index, call.Name); err != nil {
			return core.ToolCall{}, err
		}
	}
	return call, nil
}

func (a *Agent) resolveDispatchSpec(ctx context.Context, sc streamDispatchContext, call core.ToolCall, flushPendingParallelSubagents func() error, results *[]core.ToolResult) (core.ToolSpec, bool, error) {
	a.ensureApprovalCacheLoaded(ctx, sc.SessionID)
	spec, ok := sc.Tools.Spec(call.Name)
	if !ok {
		spec = core.ToolSpec{Name: call.Name}
	}
	// Evaluate policy before PreToolUse hooks so that denied calls do not
	// trigger user-defined hook side effects. The second Decide() in
	// dispatchToolCalls is intentional: hooks may rewrite call.Input, which
	// must be re-evaluated by policy.
	earlyDecision := sc.Policy.Decide(spec, call)
	if earlyDecision.Allow {
		return spec, false, nil
	}
	if err := flushPendingParallelSubagents(); err != nil {
		return core.ToolSpec{}, false, err
	}
	if err := a.appendPolicyDeniedResult(ctx, sc, call, earlyDecision, true, results); err != nil {
		return core.ToolSpec{}, false, err
	}
	return spec, true, nil
}

func emitToolArgsRepaired(ctx context.Context, sc streamDispatchContext, index int, toolName string) error {
	return emitDispatchEvent(ctx, sc, AgentEvent{
		Type: AgentEventTypeToolArgsRepaired,
		ToolArgsRepair: &ToolArgsRepair{
			ToolCallIndex: index,
			ToolName:      toolName,
		},
	})
}

func emitPolicyDecision(ctx context.Context, sc streamDispatchContext, call core.ToolCall, decision policy.PolicyDecision) error {
	return emitDispatchEvent(ctx, sc, AgentEvent{
		Type: AgentEventTypeToolPolicyDecision,
		Policy: &ToolPolicyDecision{
			ToolCallID:    call.ID,
			ToolName:      call.Name,
			Allow:         decision.Allow,
			NeedsApproval: decision.RequiresApproval,
			Reason:        decision.Reason,
			Code:          decision.Code,
			Phase:         decision.Phase,
			MatchedRule:   decision.MatchedRule,
		},
	})
}

func (a *Agent) appendPolicyDeniedResult(ctx context.Context, sc streamDispatchContext, call core.ToolCall, decision policy.PolicyDecision, emitDecision bool, results *[]core.ToolResult) error {
	a.recordApprovalForCall(sc.SessionID, sc.Model, sc.Assistant.ID, approvalEventPolicyDenied, call, decision, "", nil, "")
	if emitDecision {
		if err := emitPolicyDecision(ctx, sc, call, decision); err != nil {
			return err
		}
	}
	return appendToolResult(ctx, sc, results, core.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  policyDenialEnvelope(decision),
		Metadata:   autoDenyMetadata(sc.AutoDenyCounts, decision.Code, call.Name),
	})
}

func (a *Agent) runPreToolUseHook(ctx context.Context, sc streamDispatchContext, call core.ToolCall, flushPendingParallelSubagents func() error, results *[]core.ToolResult) (core.ToolCall, string, bool, error) {
	if a.hooks.Empty() {
		return call, "", false, nil
	}
	var toolArgs any
	_ = json.Unmarshal([]byte(call.Input), &toolArgs)
	payload := NewPreToolUsePayload(sc.SessionID, call, toolArgs)
	payload.CWD = a.workspaceRoot
	report := a.hooks.RunHookWithObserver(ctx, payload, a.hookRunObserver(ctx, sc.Events))
	if report.Blocked {
		if err := flushPendingParallelSubagents(); err != nil {
			return core.ToolCall{}, "", false, err
		}
		msg := "blocked by PreToolUse hook"
		code := "hook_blocked"
		if report.Halted {
			code = "hook_halted"
			msg = "halted by PreToolUse hook"
		}
		if len(report.Outcomes) > 0 {
			last := report.Outcomes[len(report.Outcomes)-1]
			if strings.TrimSpace(hookOutcomeMessage(last)) != "" {
				msg = strings.TrimSpace(hookOutcomeMessage(last))
			}
		}
		tr := core.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			ModelText:  fmt.Sprintf(`{"success":false,"error":%q,"code":%q}`, msg, code),
			Code:       code,
		}
		if err := appendToolResult(ctx, sc, results, tr); err != nil {
			return core.ToolCall{}, "", false, err
		}
		return call, "", true, nil
	}
	if strings.TrimSpace(report.UpdatedInput) != "" {
		call.Input = strings.TrimSpace(report.UpdatedInput)
	}
	return call, strings.TrimSpace(report.AdditionalContext), false, nil
}

func (a *Agent) appendModeBlockedResult(ctx context.Context, sc streamDispatchContext, spec core.ToolSpec, call core.ToolCall, flushPendingParallelSubagents func() error, results *[]core.ToolResult) (bool, error) {
	if (a.mode != session.ModePlan && a.mode != session.ModeAsk) || core.IsReadOnlyToolCall(spec, call) {
		return false, nil
	}
	if err := flushPendingParallelSubagents(); err != nil {
		return false, err
	}
	blockedCode, blockedMsg, blockedSummary, blockedData := modeBlockedDetailsForCall(a.mode, call)
	content, err := marshalTrustedModeBlockedEnvelope(core.ToolEnvelope{
		OK:      false,
		Success: false,
		Code:    blockedCode,
		Error:   blockedMsg,
		Message: blockedMsg,
		Summary: blockedSummary,
		Data:    blockedData,
	})
	if err != nil {
		content = fmt.Sprintf(`{"success":false,"error":%q,"code":%q}`, blockedMsg, blockedCode)
	}
	if err := emitDispatchEvent(ctx, sc, AgentEvent{
		Type: AgentEventTypeToolModeBlocked,
		ToolBlocked: &ToolCallBlocked{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			ReasonCode: blockedCode,
		},
	}); err != nil {
		return false, err
	}
	a.recordApprovalEvent(telemetry.ApprovalEvent{
		Session:            sc.SessionID,
		Model:              sc.Model,
		AssistantMessageID: sc.Assistant.ID,
		ToolCallID:         call.ID,
		Tool:               call.Name,
		Event:              approvalEventModeBlocked,
		Code:               blockedCode,
		Reason:             blockedMsg,
		Phase:              "denied",
	})
	return true, appendToolResult(ctx, sc, results, core.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  content,
		Metadata:   autoDenyMetadata(sc.AutoDenyCounts, blockedCode, call.Name),
	})
}

func marshalTrustedModeBlockedEnvelope(env core.ToolEnvelope) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func (a *Agent) dispatchPreApprovalSpecialTool(ctx context.Context, sc streamDispatchContext, call core.ToolCall, results *[]core.ToolResult) (bool, error) {
	if call.Name != "update_plan" {
		return false, nil
	}
	res, err := a.handleUpdatePlan(ctx, call, sc.Events)
	if err != nil {
		tr := core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: err.Error()}
		return true, appendToolResult(ctx, sc, results, tr)
	}
	return true, appendToolResult(ctx, sc, results, res)
}

func (a *Agent) resolveToolApproval(ctx context.Context, sc streamDispatchContext, spec core.ToolSpec, call core.ToolCall, decision policy.PolicyDecision, flushPendingParallelSubagents func() error, results *[]core.ToolResult) (toolApprovalResult, error) {
	result := toolApprovalResult{
		ExternalReadRoots: policy.ExternalReadApprovalRootsForDecision(call, decision),
	}
	if !decision.RequiresApproval {
		return result, nil
	}
	// Auto-allow read_file / grep / search_files when they operate inside the
	// current session's tool-results archive directory. The tool execution
	// layer already enforces this scope via WithToolResultReadRoots; this
	// check prevents the user-facing approval dialog from appearing for
	// paths that will be allowed anyway.
	//
	// The decision's ApprovalRequirements list carries every permission kind
	// (including "read" for audit), but the Permission field records the
	// specific kind whose "ask" action triggered RequiresApproval.  We only
	// bypass when that kind is external_directory and the target path lives
	// inside the current session's tool-results directory.
	if isToolResultReadTool(call.Name) &&
		decision.Permission == "external_directory" &&
		a.isToolResultReadPath(sc.SessionID, call) {
		return result, nil
	}
	keys := policy.ApprovalKeysForDecision(call, decision)
	key := policy.ApprovalKey(call)
	if len(keys) > 0 {
		key = keys[0]
	}
	approved := a.approvalCache.HasAll(sc.SessionID, keys)
	if approved {
		a.recordApprovalForCall(sc.SessionID, sc.Model, sc.Assistant.ID, approvalEventCachedAllowed, call, decision, key, keys, policy.ApprovalScope(call))
	}
	if !approved {
		metadata := a.previewTool(ctx, sc.Tools, call)
		metadata = addPolicyApprovalMetadata(metadata, decision)
		metadata = policy.ApprovalMetadata(call, keys, metadata)
		a.recordApprovalForCall(sc.SessionID, sc.Model, sc.Assistant.ID, approvalEventRequired, call, decision, key, keys, policy.ApprovalScope(call))
		if err := emitDispatchEvent(ctx, sc, AgentEvent{
			Type: AgentEventTypeToolApprovalRequired,
			Approval: &ToolApprovalRequired{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Reason:     decision.Reason,
				Code:       decision.Code,
				Key:        key,
				Keys:       keys,
				Summary:    policy.ApprovalSummary(call),
				Scope:      policy.ApprovalScope(call),
				Metadata:   metadata,
			},
		}); err != nil {
			return toolApprovalResult{}, err
		}
		approvalDecision := policy.ApprovalDeny
		if a.approve != nil {
			approvalDecision = a.approve(policy.ApprovalRequest{
				SessionID: sc.SessionID,
				ToolCall:  call,
				Spec:      spec,
				Reason:    decision.Reason,
				Code:      decision.Code,
				Key:       key,
				Keys:      keys,
				Metadata:  metadata,
			})
		}
		a.recordApprovalForCall(sc.SessionID, sc.Model, sc.Assistant.ID, approvalDecisionEvent(approvalDecision), call, decision, key, keys, policy.ApprovalScope(call))
		approved = approvalDecision.Approved()
		if approvalDecision.Canceled() {
			return toolApprovalResult{}, context.Canceled
		}
		if approvalDecision.ForSession() {
			if policy.ApprovalKeysFileScoped(keys) {
				result.GrantOnSuccess = true
				result.GrantKey = key
				result.GrantKeys = keys
			} else {
				if !a.grantApprovals(ctx, sc.SessionID, call, key, keys, sc.Events) {
					return toolApprovalResult{}, ctx.Err()
				}
			}
		}
	}
	if approved {
		return result, nil
	}
	if err := flushPendingParallelSubagents(); err != nil {
		return toolApprovalResult{}, err
	}
	tr := core.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  `{"success":false,"error":"tool approval denied","code":"approval_denied"}`,
		Code:       "approval_denied",
	}
	if err := appendToolResult(ctx, sc, results, tr); err != nil {
		return toolApprovalResult{}, err
	}
	if err := appendAbortSkippedToolResults(ctx, sc, results, true); err != nil {
		return toolApprovalResult{}, err
	}
	for i := range *results {
		(*results)[i] = core.FinalizeToolResultChannels((*results)[i])
	}
	toolMsg, err := a.store.Create(ctx, core.Message{SessionID: sc.SessionID, Role: core.RoleTool, ToolResults: *results})
	if err != nil {
		return toolApprovalResult{}, fmt.Errorf("create tool message: %w", err)
	}
	a.persistApprovalDeniedMarker(sc.SessionID, call.Name)
	result.ToolMessage = &toolMsg
	result.AbortTurn = true
	return result, nil
}

func (a *Agent) dispatchPostApprovalSpecialTool(ctx context.Context, sc streamDispatchContext, call core.ToolCall, results *[]core.ToolResult) (bool, error) {
	if call.Name == "request_user_input" {
		res, err := a.handleRequestUserInput(ctx, call, sc.SessionID, sc.Events)
		if err != nil {
			tr := core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: err.Error()}
			return true, appendToolResult(ctx, sc, results, tr)
		}
		return true, appendToolResult(ctx, sc, results, res)
	}
	switch call.Name {
	case "todo_add", "todo_list", "todo_update", "todo_remove", "todo_clear_done":
		res, err := a.handleTodo(call, sc.SessionID)
		if err != nil {
			tr := core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: err.Error()}
			return true, appendToolResult(ctx, sc, results, tr)
		}
		return true, appendToolResult(ctx, sc, results, res)
	default:
		return false, nil
	}
}

func (a *Agent) dispatchStandardTool(ctx context.Context, sc streamDispatchContext, prepared preparedToolDispatch, results *[]core.ToolResult) (bool, error) {
	finalRes, ok, primarySucceeded := a.dispatchWithRecovery(ctx, sc.SessionID, sc.Assistant.ID, sc.Model, prepared.Call, prepared.ExternalReadRoots, sc.Events, sc.Tools)
	canceled := ctx.Err() != nil
	if canceled && !toolResultUsableAfterCancel(finalRes) {
		return false, ctx.Err()
	}
	if !ok {
		return false, nil
	}
	if !a.appendDispatchedToolResult(ctx, sc.SessionID, prepared, finalRes, primarySucceeded, sc.Events, results, !canceled) {
		return false, ctx.Err()
	}
	return canceled || toolResultRequestsTurnAbort(finalRes), nil
}

func persistenceContext(ctx context.Context) context.Context {
	if ctx != nil && ctx.Err() != nil {
		return context.Background()
	}
	return ctx
}

func toolResultUsableAfterCancel(res core.ToolResult) bool {
	if res.ToolCallID == "" && res.Name == "" && res.ModelText == "" && res.Metadata == nil {
		return false
	}
	return !res.IsError()
}

func (a *Agent) createDispatchToolMessage(ctx context.Context, sc streamDispatchContext, results []core.ToolResult) (core.Message, error) {
	for i := range results {
		results[i] = core.FinalizeToolResultChannels(results[i])
	}
	toolMsg, err := a.store.Create(ctx, core.Message{SessionID: sc.SessionID, Role: core.RoleTool, ToolResults: results})
	if err != nil {
		return core.Message{}, fmt.Errorf("create tool message: %w", err)
	}
	return toolMsg, nil
}

func toolResultRequestsTurnAbort(res core.ToolResult) bool {
	if res.Metadata == nil {
		return false
	}
	v, ok := res.Metadata["abort_turn_after_tool_result"]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	default:
		return false
	}
}
