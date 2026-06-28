package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/usewhale/whale/internal/core"
	toolctx "github.com/usewhale/whale/internal/tools"
)

func (a *Agent) dispatchWithRecovery(ctx context.Context, sessionID, assistantMessageID, model string, call core.ToolCall, externalReadRoots []string, events chan<- AgentEvent, tools *core.ToolRegistry) (core.ToolResult, bool, bool) {
	attempt := 0
	dispatchCtx := core.WithToolResultArchive(ctx, a.toolResultArchiveDir, sessionID)
	dispatchCtx = toolctx.WithApprovedExternalReadRoots(dispatchCtx, externalReadRoots)
	dispatchCtx = toolctx.WithToolResultReadRoots(dispatchCtx, []string{core.ToolResultArchiveSessionDir(a.toolResultArchiveDir, sessionID)})
	emit := func(ev AgentEvent) bool {
		return sendAgentEvent(ctx, events, ev)
	}
	for {
		attempt++
		res, err := dispatchRecoveryAttempt(dispatchCtx, call, tools, recoveryProgressEmitter(call, emit))
		if ctx.Err() != nil {
			return res, true, false
		}
		if err != nil {
			res = core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: err.Error()}
		}
		if code := toolInputInvalidCode(res); code != "" {
			a.recordToolInputInvalid(sessionID, model, assistantMessageID, call, code)
		}
		class := classifyToolFailure(res, err)
		if class == "" {
			return res, true, !res.IsError()
		}
		rule, exists := a.recovery.Rules[class]
		next, done, primarySucceeded := a.handleRecoveryRule(ctx, dispatchCtx, tools, call, res, class, rule, exists, attempt, emit)
		if done {
			return next, true, primarySucceeded
		}
		if delayed, canceled := waitRecoveryBackoff(ctx, call, rule); canceled {
			return delayed, true, false
		}
		if !emitRecoveryAttempt(call, class, rule, attempt+1, emit) {
			return res, true, false
		}
	}
}

func recoveryProgressEmitter(call core.ToolCall, emit func(AgentEvent) bool) func(core.ToolProgress) {
	return func(progress core.ToolProgress) {
		// Progress events are emitted directly from tool goroutines, so
		// different ToolCallIDs may interleave in parallel subagent batches.
		// The stable contract is attribution plus each call's own
		// progress-before-completion/result ordering.
		info := TaskActivityInfo{
			ToolCallID: core.FirstNonEmpty(progress.ToolCallID, call.ID),
			ToolName:   core.FirstNonEmpty(progress.ToolName, call.Name),
			Role:       progress.Role,
			Model:      progress.Model,
			Count:      progress.Count,
			Summary:    progress.Summary,
			Status:     progress.Status,
			DurationMS: progress.DurationMS,
			Metadata:   progress.Metadata,
		}
		if len(progress.ProgressMessages) > 0 {
			info.ProgressMessages = progress.ProgressMessages
		}
		_ = emit(AgentEvent{Type: AgentEventTypeTaskProgress, Task: &info})
	}
}

func dispatchRecoveryAttempt(ctx context.Context, call core.ToolCall, tools *core.ToolRegistry, progress func(core.ToolProgress)) (core.ToolResult, error) {
	return tools.DispatchWithProgress(ctx, call, progress)
}

func (a *Agent) handleRecoveryRule(ctx context.Context, dispatchCtx context.Context, tools *core.ToolRegistry, call core.ToolCall, res core.ToolResult, class FailureClass, rule RecoveryRule, exists bool, attempt int, emit func(AgentEvent) bool) (core.ToolResult, bool, bool) {
	if !a.recovery.Enabled || !exists {
		if !emitRecoveryExhausted(call, class, RecoveryRule{Action: RecoveryActionRequestReplan}, attempt, "no recovery rule", false, false, emit) {
			return res, true, false
		}
		return res, true, false
	}
	if rule.Action == RecoveryActionFallbackReadOnly {
		fallbackRes, ok := a.executeFallbackReadonly(dispatchCtx, tools, call, res)
		if ok {
			if !emitRecoveryExhausted(call, class, rule, attempt, core.ToolResultModelText(res), true, false, emit) {
				return res, true, false
			}
			return fallbackRes, true, false
		}
	}
	if rule.Action == RecoveryActionRequestReplan {
		replanRes := buildRequestReplanResult(call, class, attempt, core.ToolResultModelText(res))
		if !emitRecoveryReplanRequired(call, class, rule, attempt, core.ToolResultModelText(res), emit) {
			return res, true, false
		}
		if !emitRecoveryExhausted(call, class, rule, attempt, core.ToolResultModelText(res), true, true, emit) {
			return res, true, false
		}
		return replanRes, true, false
	}
	if rule.Action == RecoveryActionPassThrough {
		return res, true, false
	}
	if attempt > rule.MaxAttempts || rule.Action == RecoveryActionHardBlock {
		if !emitRecoveryExhausted(call, class, rule, attempt, core.ToolResultModelText(res), false, false, emit) {
			return res, true, false
		}
		return res, true, false
	}
	if !emitRecoveryScheduled(call, class, rule, attempt, core.ToolResultModelText(res), emit) {
		return res, true, false
	}
	return res, false, false
}

func emitRecoveryExhausted(call core.ToolCall, class FailureClass, rule RecoveryRule, attempt int, reason string, executed, replanInjected bool, emit func(AgentEvent) bool) bool {
	return emit(AgentEvent{
		Type: AgentEventTypeToolRecoveryExhausted,
		Recovery: &ToolRecoveryInfo{
			ToolCallID:     call.ID,
			ToolName:       call.Name,
			FailureClass:   string(class),
			Action:         string(rule.Action),
			Attempt:        attempt,
			MaxAttempts:    rule.MaxAttempts,
			Reason:         reason,
			Executed:       executed,
			ReplanInjected: replanInjected,
		},
	})
}

func emitRecoveryReplanRequired(call core.ToolCall, class FailureClass, rule RecoveryRule, attempt int, reason string, emit func(AgentEvent) bool) bool {
	return emit(AgentEvent{
		Type: AgentEventTypeReplanRequiredSet,
		Recovery: &ToolRecoveryInfo{
			ToolCallID:     call.ID,
			ToolName:       call.Name,
			FailureClass:   string(class),
			Action:         string(rule.Action),
			Attempt:        attempt,
			MaxAttempts:    rule.MaxAttempts,
			Reason:         reason,
			ReplanInjected: true,
		},
	})
}

func emitRecoveryScheduled(call core.ToolCall, class FailureClass, rule RecoveryRule, attempt int, reason string, emit func(AgentEvent) bool) bool {
	return emit(AgentEvent{
		Type: AgentEventTypeToolRecoveryScheduled,
		Recovery: &ToolRecoveryInfo{
			ToolCallID:   call.ID,
			ToolName:     call.Name,
			FailureClass: string(class),
			Action:       string(rule.Action),
			Attempt:      attempt,
			MaxAttempts:  rule.MaxAttempts,
			Reason:       reason,
		},
	})
}

func waitRecoveryBackoff(ctx context.Context, call core.ToolCall, rule RecoveryRule) (core.ToolResult, bool) {
	if rule.Action != RecoveryActionRetryWithBackoff || rule.BackoffMS <= 0 {
		return core.ToolResult{}, false
	}
	timer := time.NewTimer(time.Duration(rule.BackoffMS) * time.Millisecond)
	select {
	case <-ctx.Done():
		timer.Stop()
		return core.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			ModelText:  ctx.Err().Error(),
			Outcome:    core.OutcomeCancelled,
			Code:       "cancelled",
		}, true
	case <-timer.C:
		return core.ToolResult{}, false
	}
}

func emitRecoveryAttempt(call core.ToolCall, class FailureClass, rule RecoveryRule, attempt int, emit func(AgentEvent) bool) bool {
	return emit(AgentEvent{
		Type: AgentEventTypeToolRecoveryAttempt,
		Recovery: &ToolRecoveryInfo{
			ToolCallID:   call.ID,
			ToolName:     call.Name,
			FailureClass: string(class),
			Action:       string(rule.Action),
			Attempt:      attempt,
			MaxAttempts:  rule.MaxAttempts,
		},
	})
}

func (a *Agent) executeFallbackReadonly(ctx context.Context, tools *core.ToolRegistry, call core.ToolCall, cause core.ToolResult) (core.ToolResult, bool) {
	fallbackCall := core.ToolCall{ID: call.ID + "-fallback", Name: "list_dir", Input: `{"path":"."}`}
	switch call.Name {
	case "write", "edit", "multi_edit":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal([]byte(call.Input), &in) == nil && in.FilePath != "" {
			b, err := json.Marshal(map[string]any{"file_path": in.FilePath})
			if err != nil {
				return core.ToolResult{}, false
			}
			fallbackCall = core.ToolCall{ID: call.ID + "-fallback", Name: "read_file", Input: string(b)}
		}
	case "shell_run":
		fallbackCall = core.ToolCall{ID: call.ID + "-fallback", Name: "list_dir", Input: `{"path":"."}`}
	default:
		return core.ToolResult{}, false
	}
	res, err := tools.Dispatch(ctx, fallbackCall)
	if err != nil {
		return core.ToolResult{}, false
	}
	wrapped, err := core.MarshalToolJSON(map[string]any{
		"success": true,
		"data": map[string]any{
			"status":  "recovered_with_fallback",
			"summary": "primary tool failed, fallback readonly tool executed",
			"failure": map[string]any{
				"tool":  call.Name,
				"code":  classifyToolFailure(cause, nil),
				"error": core.ToolResultModelText(cause),
			},
			"fallback": map[string]any{
				"tool":   fallbackCall.Name,
				"result": core.ToolResultModelText(res),
			},
		},
	})
	if err != nil {
		return core.ToolResult{}, false
	}
	return core.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  string(wrapped),
	}, true
}

func buildRequestReplanResult(call core.ToolCall, class FailureClass, attempt int, reason string) core.ToolResult {
	b, err := core.MarshalToolJSON(map[string]any{
		"success": false,
		"error":   "recovery exhausted, replan required",
		"code":    "request_replan",
		"data": map[string]any{
			"failure_class":       class,
			"tool_name":           call.Name,
			"tool_call_id":        call.ID,
			"attempts":            attempt,
			"last_error":          reason,
			"suggested_next_step": "Explain the failure and ask the user for direction before retrying.",
		},
	})
	if err != nil {
		return core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: `{"success":false,"error":"recovery exhausted, replan required","code":"request_replan"}`, Code: "request_replan"}
	}
	return core.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  string(b),
		Code:       "request_replan",
	}
}
