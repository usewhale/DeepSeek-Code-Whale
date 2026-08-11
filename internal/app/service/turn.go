package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/app"
	"github.com/usewhale/whale/internal/core"
	llmretry "github.com/usewhale/whale/internal/llm/retry"
	"github.com/usewhale/whale/internal/runtime/protocol"
)

func (s *Service) runTurn(line string, hiddenInput bool) {
	s.runTurnWithOptions(line, agent.RunOptions{HiddenInput: hiddenInput})
}

func (s *Service) runTurnWithOptions(line string, opts agent.RunOptions) {
	opts = s.tuiRunOptions(opts)
	s.runTurnWith(func(ctx context.Context) (<-chan agent.AgentEvent, error) {
		return s.app.RunTurnWithOptions(ctx, line, opts)
	})
}

func (s *Service) runTurnWithContentOptions(parts []core.MessagePart, opts agent.RunOptions) {
	opts = s.tuiRunOptions(opts)
	s.runTurnWith(func(ctx context.Context) (<-chan agent.AgentEvent, error) {
		return s.app.RunTurnWithContentOptions(ctx, parts, opts)
	})
}

func (s *Service) runInjectedTurn(visibleInput, hiddenInput string) {
	s.runInjectedTurnWithOptions(visibleInput, hiddenInput, agent.RunOptions{})
}

func (s *Service) runInjectedTurnWithOptions(visibleInput, hiddenInput string, opts agent.RunOptions) {
	opts = s.tuiRunOptions(opts)
	s.runTurnWith(func(ctx context.Context) (<-chan agent.AgentEvent, error) {
		return s.app.RunTurnWithInjectedInputOptions(ctx, visibleInput, hiddenInput, opts)
	})
}

func (s *Service) runInjectedTurnWithContentOptions(visibleParts []core.MessagePart, hiddenInput string, opts agent.RunOptions) {
	opts = s.tuiRunOptions(opts)
	s.runTurnWith(func(ctx context.Context) (<-chan agent.AgentEvent, error) {
		return s.app.RunTurnWithInjectedContentOptions(ctx, visibleParts, hiddenInput, opts)
	})
}

func (s *Service) injectActiveTurn(line string, opts agent.RunOptions, clientInputID string) bool {
	return s.injectActiveTurnWithHidden(line, "", opts, clientInputID)
}

func (s *Service) injectActiveTurnWithHidden(visibleInput, hiddenInput string, opts agent.RunOptions, clientInputID string) bool {
	s.cancelMu.Lock()
	active := s.active
	s.cancelMu.Unlock()
	if !active {
		return false
	}
	opts = s.tuiRunOptions(opts)
	injected, err := s.app.InjectTurnInputWithHidden(s.ctx, visibleInput, hiddenInput, opts)
	if err != nil {
		if clientInputID != "" {
			s.emit(Event{Kind: EventPendingInputRejected, ClientInputID: clientInputID, Text: visibleInput})
		}
		s.emit(Event{Kind: EventError, Text: err.Error()})
		return true
	}
	if !injected {
		if clientInputID != "" {
			s.emit(Event{Kind: EventPendingInputRejected, ClientInputID: clientInputID, Text: visibleInput})
		}
		s.emit(Event{Kind: EventError, Text: agent.ErrSessionBusy.Error()})
	} else if clientInputID != "" {
		s.emit(Event{Kind: EventPendingInputAccepted, ClientInputID: clientInputID, Text: visibleInput})
	}
	return true
}

func (s *Service) tuiRunOptions(opts agent.RunOptions) agent.RunOptions {
	if strings.TrimSpace(opts.ViewMode) == "" && s != nil && s.app != nil {
		opts.ViewMode = s.app.ViewMode()
	}
	if strings.TrimSpace(opts.ViewMode) == "" {
		opts.ViewMode = app.ViewModeDefault
	}
	return opts
}

func (s *Service) runSideQuestion(question string) {
	id := int(s.btwNextID.Add(1))
	s.emit(Event{Kind: EventBtwStarted, Text: question, Count: id})
	s.goTracked(func() {
		events, err := s.app.RunSideQuestionWithOptions(s.ctx, question, s.tuiRunOptions(agent.RunOptions{}))
		if err != nil {
			s.emit(Event{Kind: EventBtwError, Text: err.Error(), Count: id})
			return
		}
		for ev := range events {
			switch ev.Type {
			case agent.SideQuestionEventDelta:
				if ev.Content != "" {
					s.emit(Event{Kind: EventBtwDelta, Text: ev.Content, Count: id})
				}
			case agent.SideQuestionEventDone:
				s.emit(Event{Kind: EventBtwDone, Text: ev.Content, Count: id})
			case agent.SideQuestionEventError:
				if ev.Content != "" {
					s.emit(Event{Kind: EventBtwError, Text: ev.Content, Count: id})
				} else if ev.Err != nil {
					s.emit(Event{Kind: EventBtwError, Text: ev.Err.Error(), Count: id})
				}
			}
		}
	})
}

func (s *Service) runTurnWith(start func(context.Context) (<-chan agent.AgentEvent, error)) {
	turnCtx, cancelCause := context.WithCancelCause(s.ctx)
	cancel := context.CancelFunc(func() { cancelCause(context.Canceled) })
	s.cancelMu.Lock()
	if s.active {
		s.cancelMu.Unlock()
		cancel()
		s.emit(Event{Kind: EventError, Text: agent.ErrSessionBusy.Error()})
		s.emit(Event{Kind: EventTurnDone})
		return
	}
	s.active = true
	s.cancel = cancel
	s.cancelCause = cancelCause
	s.resetInteractionShutdown()
	s.cancelMu.Unlock()
	defer func() {
		s.cancelMu.Lock()
		s.cancel = nil
		s.cancelCause = nil
		s.active = false
		s.cancelMu.Unlock()
		cancel()
	}()
	events, err := start(turnCtx)
	if err != nil {
		if shouldSuppressCancelledTurnError(turnCtx, err) {
			s.emit(Event{Kind: EventTurnDone})
			return
		}
		s.emit(Event{Kind: EventError, Text: err.Error()})
		s.emit(Event{Kind: EventTurnDone})
		return
	}
	last := ""
	completed := false
	deltas := newTurnDeltaCoalescers(s)
	for ev := range events {
		switch ev.Type {
		case agent.AgentEventTypeAssistantDelta:
			if ev.Content != "" {
				last += ev.Content
				deltas.add(EventAssistantDelta, ev.Content)
			}
		case agent.AgentEventTypeReasoningDelta:
			deltas.add(EventReasoningDelta, ev.ReasoningDelta)
		case agent.AgentEventTypePlanDelta:
			if ev.Content != "" {
				deltas.add(EventPlanDelta, ev.Content)
			}
		case agent.AgentEventTypePlanCompleted:
			deltas.flushReliable()
			s.emit(Event{Kind: EventPlanCompleted, Text: ev.Content})
		case agent.AgentEventTypePlanUpdate:
			if ev.PlanUpdate != nil {
				deltas.flushReliable()
				s.emit(Event{Kind: EventPlanUpdate, Text: agent.FormatPlanUpdateForDisplay(*ev.PlanUpdate)})
			}
		case agent.AgentEventTypeProviderRetryScheduled:
			if ev.ProviderRetry != nil {
				deltas.flushReliable()
				if ev.ProviderRetry.StreamReset {
					last = ""
				}
				s.emit(providerRetryEvent(ev.ProviderRetry))
			}
		case agent.AgentEventTypeResponseReset:
			deltas.flushReliable()
			last = ""
			s.emit(Event{Kind: EventResponseReset})
		case agent.AgentEventTypeToolCall:
			if ev.ToolCall != nil {
				deltas.flushReliable()
				s.emit(Event{
					Kind:       EventToolCall,
					ToolCallID: ev.ToolCall.ID,
					ToolName:   ev.ToolCall.Name,
					Text:       summarizeToolCall(*ev.ToolCall),
				})
			}
		case agent.AgentEventTypeToolResult:
			if ev.Result != nil {
				deltas.flushReliable()
				if s.maybeEmitWorkflowLaunchConfirmationToolResult(ev.Result) {
					continue
				}
				s.maybeWatchWorkflowToolResult(ev.Result)
				resPayload, _ := ev.Result.Payload.(map[string]any)
				resOutcome := core.ToolResultOutcome(*ev.Result)
				status := ""
				if resOutcome != core.OutcomeSuccess && resOutcome != core.OutcomeNoResult {
					status = "failed"
				}
				s.emit(Event{
					Kind: EventToolResult, ToolCallID: ev.Result.ToolCallID, ToolName: ev.Result.Name,
					Text: core.ToolResultModelText(*ev.Result), Metadata: ev.Result.Metadata,
					Status:      status,
					ToolOutcome: string(resOutcome), ToolCode: ev.Result.Code, ToolPayload: resPayload,
				})
			}
		case agent.AgentEventTypeHookStarted:
			if ev.Hook != nil {
				deltas.flushReliable()
				s.emit(hookLifecycleEvent(EventHookStarted, ev.Hook))
			}
		case agent.AgentEventTypeHookBlocked, agent.AgentEventTypeHookWarned, agent.AgentEventTypeHookFailed, agent.AgentEventTypeHookCompleted:
			if ev.Hook != nil {
				if ev.Type == agent.AgentEventTypeHookCompleted && ev.Hook.Decision != "" && ev.Hook.Decision != agent.HookDecisionPass {
					continue
				}
				deltas.flushReliable()
				s.emit(hookLifecycleEvent(EventHookCompleted, ev.Hook))
			}
		case agent.AgentEventTypeToolApprovalGranted:
			s.syncApprovalGrant(ev.ApprovalGrant)
		case agent.AgentEventTypeParallelReasonStarted, agent.AgentEventTypeSubagentStarted:
			if ev.Task != nil {
				deltas.flushReliable()
				if ev.Type == agent.AgentEventTypeSubagentStarted {
					if out := s.app.RunSubagentHook(agent.HookEventSubagentStart, ev.Task, s.hookObserver()); out != "" {
						s.emit(Event{Kind: EventInfo, Text: out})
					}
				}
				s.emit(taskActivityEvent(EventTaskStarted, ev.Task))
			}
		case agent.AgentEventTypeTaskProgress:
			if ev.Task != nil {
				deltas.flushReliable()
				s.emit(taskActivityEvent(EventTaskProgress, ev.Task))
			}
		case agent.AgentEventTypeParallelReasonDone, agent.AgentEventTypeSubagentDone:
			if ev.Task != nil {
				deltas.flushReliable()
				if ev.Type == agent.AgentEventTypeSubagentDone {
					if out := s.app.RunSubagentHook(agent.HookEventSubagentStop, ev.Task, s.hookObserver()); out != "" {
						s.emit(Event{Kind: EventInfo, Text: out})
					}
				}
				s.emit(taskActivityEvent(EventTaskCompleted, ev.Task))
			}
		case agent.AgentEventTypeUserInputRequired:
			if ev.ToolCall != nil && ev.UserInputReq != nil {
				deltas.flushReliable()
				s.emit(Event{Kind: EventUserInputRequired, ToolCallID: ev.ToolCall.ID, ToolName: ev.ToolCall.Name, Questions: protocolUserInputQuestions(ev.UserInputReq.Questions)})
			}
		case agent.AgentEventTypeUserInputSubmitted, agent.AgentEventTypeUserInputCancelled:
			deltas.flushReliable()
			doneEvent := Event{Kind: EventUserInputDone}
			if ev.Type == agent.AgentEventTypeUserInputCancelled {
				doneEvent.Status = "canceled"
			} else {
				doneEvent.Status = "submitted"
				if ev.UserInputReq != nil {
					doneEvent.Questions = protocolUserInputQuestions(ev.UserInputReq.Questions)
				}
				if ev.UserInputResp != nil {
					doneEvent.Answers = protocolUserInputAnswers(ev.UserInputResp.Answers)
				}
			}
			if ev.ToolCall != nil {
				doneEvent.ToolCallID = ev.ToolCall.ID
				doneEvent.ToolName = ev.ToolCall.Name
			}
			s.emit(doneEvent)
		case agent.AgentEventTypeDone:
			if ev.Message != nil {
				if last == "" {
					last = ev.Message.Text
				}
				completed = ev.Message.FinishReason == core.FinishReasonEndTurn
			}
		case agent.AgentEventTypeError:
			if ev.Err != nil {
				if shouldSuppressCancelledTurnError(turnCtx, ev.Err) {
					continue
				}
				deltas.flushReliable()
				s.emit(Event{Kind: EventError, Text: ev.Err.Error()})
			}
		}
	}
	deltas.flushReliable()
	_ = s.app.FinalizeTurn(last, completed)
	if out := s.app.RunStopHookWithObserver(last, 0, s.hookObserver()); out != "" {
		s.emit(Event{Kind: EventInfo, Text: out})
	}
	s.emit(Event{
		Kind:         EventTurnDone,
		LastResponse: last,
		Metadata:     map[string]any{EventMetadataAgentTurn: true},
	})
}

func (s *Service) hookObserver() agent.HookRunObserver {
	return func(stage agent.HookRunStage, info agent.HookEventInfo) {
		kind := EventHookCompleted
		if stage == agent.HookRunStarted {
			kind = EventHookStarted
		}
		s.emit(hookLifecycleEvent(kind, &info))
	}
}

func hookLifecycleEvent(kind EventKind, info *agent.HookEventInfo) Event {
	if info == nil {
		return Event{Kind: kind}
	}
	status := "completed"
	if kind == EventHookStarted {
		status = "running"
	} else {
		switch info.Decision {
		case agent.HookDecisionBlock, agent.HookDecisionHalt:
			status = "blocked"
		case agent.HookDecisionError, agent.HookDecisionTimeout:
			status = "failed"
		case agent.HookDecisionWarn:
			status = "warning"
		}
	}
	text := summarizeHookRun(status, info)
	return Event{
		Kind:       kind,
		Text:       text,
		Status:     status,
		DurationMS: info.DurationMS,
		Hook: &protocol.HookRun{
			ID:         info.ID,
			Event:      string(info.Event),
			Name:       info.Name,
			Source:     info.Source,
			Command:    info.Command,
			Status:     status,
			Decision:   string(info.Decision),
			ExitCode:   info.ExitCode,
			Message:    info.Message,
			DurationMS: info.DurationMS,
			Truncated:  info.Truncated,
		},
	}
}

func summarizeHookRun(status string, info *agent.HookEventInfo) string {
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = strings.TrimSpace(info.Command)
	}
	if name == "" {
		name = string(info.Event)
	}
	base := fmt.Sprintf("%s hook %s · %s", info.Event, status, name)
	if strings.TrimSpace(info.Message) != "" && status != "running" {
		base += ": " + strings.TrimSpace(info.Message)
	}
	return base
}

func providerRetryEvent(info *llmretry.Info) Event {
	if info == nil {
		return Event{Kind: EventProviderRetry}
	}
	meta := map[string]any{
		"attempt":      info.Attempt,
		"max_attempts": info.MaxAttempts,
		"delay_ms":     info.Delay.Milliseconds(),
	}
	if info.StatusCode > 0 {
		meta["status_code"] = info.StatusCode
	}
	if info.Reason != "" {
		meta["reason"] = info.Reason
	}
	if info.Stage != "" {
		meta["stage"] = info.Stage
	}
	if info.StreamReset {
		meta["stream_reset"] = true
	}
	return Event{Kind: EventProviderRetry, Text: llmretry.FormatInfo(*info), Metadata: meta}
}

func shouldSuppressCancelledTurnError(ctx context.Context, err error) bool {
	return ctx != nil && ctx.Err() != nil && errors.Is(err, context.Canceled)
}

func taskActivityEvent(kind EventKind, info *agent.TaskActivityInfo) Event {
	if info == nil {
		return Event{Kind: kind}
	}
	meta := map[string]any{}
	if info.Role != "" {
		meta["role"] = info.Role
	}
	if info.Model != "" {
		meta["model"] = info.Model
	}
	if info.Summary != "" {
		meta["summary"] = info.Summary
	}
	for k, v := range info.Metadata {
		meta[k] = v
	}
	return Event{
		Kind:             kind,
		ToolCallID:       info.ToolCallID,
		ToolName:         info.ToolName,
		Text:             summarizeTaskActivity(kind, info),
		Metadata:         meta,
		Status:           info.Status,
		Count:            info.Count,
		DurationMS:       info.DurationMS,
		ProgressMessages: protocolProgressSteps(info.ProgressMessages),
	}
}

func summarizeTaskActivity(kind EventKind, info *agent.TaskActivityInfo) string {
	if info == nil {
		return ""
	}
	status := strings.TrimSpace(info.Status)
	if status == "" {
		if kind == EventTaskStarted {
			status = "started"
		} else if kind == EventTaskProgress {
			status = "running"
		} else {
			status = "completed"
		}
	}
	switch info.ToolName {
	case "parallel_reason":
		if info.Count > 0 {
			return fmt.Sprintf("parallel_reason %s · %d prompt(s)", status, info.Count)
		}
		return "parallel_reason " + status
	case "spawn_subagent":
		role := strings.TrimSpace(info.Role)
		if role == "" {
			role = "explore"
		}
		if info.Summary != "" && (kind == EventTaskStarted || kind == EventTaskProgress) {
			return fmt.Sprintf("spawn_subagent %s · %s · %s", status, role, info.Summary)
		}
		if info.DurationMS > 0 && kind == EventTaskCompleted {
			return fmt.Sprintf("spawn_subagent %s · %s · %dms", status, role, info.DurationMS)
		}
		return fmt.Sprintf("spawn_subagent %s · %s", status, role)
	default:
		return strings.TrimSpace(info.ToolName + " " + status)
	}
}

func summarizeToolCall(call core.ToolCall) string {
	body := map[string]any{}
	_ = json.Unmarshal([]byte(call.Input), &body)
	name := strings.TrimSpace(call.Name)
	switch name {
	case "parallel_reason":
		count := len(core.AsAnySlice(body["prompts"]))
		if count > 0 {
			return fmt.Sprintf("parallel_reason: %d prompt(s)", count)
		}
	case "spawn_subagent":
		role := strings.TrimSpace(core.AsString(body["role"]))
		if role == "" {
			role = "explore"
		}
		task := core.FirstLine(core.AsString(body["task"]))
		if task != "" {
			return fmt.Sprintf("spawn_subagent: %s · %s", role, task)
		}
		return "spawn_subagent: " + role
	case "workflow":
		action := strings.TrimSpace(core.AsString(body["action"]))
		if saveAs := strings.TrimSpace(core.AsString(body["saveAs"])); saveAs != "" {
			if action == "" {
				action = "create"
			}
			return fmt.Sprintf("workflow: %s %s", action, saveAs)
		}
		if path := strings.TrimSpace(core.AsString(body["scriptPath"])); path != "" {
			return fmt.Sprintf("workflow: %s", path)
		}
		if name := strings.TrimSpace(core.AsString(body["name"])); name != "" {
			return fmt.Sprintf("workflow: %s", name)
		}
		if script := core.FirstLine(strings.TrimSpace(core.AsString(body["script"]))); script != "" {
			return fmt.Sprintf("workflow: %s", script)
		}
		return "workflow"
	case "shell_run":
		if cmd, _ := body["command"].(string); strings.TrimSpace(cmd) != "" {
			return fmt.Sprintf("shell_run: %s", strings.TrimSpace(cmd))
		}
	case "shell_wait":
		if taskID, _ := body["task_id"].(string); strings.TrimSpace(taskID) != "" {
			return fmt.Sprintf("shell_wait: %s", strings.TrimSpace(taskID))
		}
	case "write", "edit":
		if path, _ := body["file_path"].(string); strings.TrimSpace(path) != "" {
			return fmt.Sprintf("%s: %s", name, strings.TrimSpace(path))
		}
	case "list_dir":
		if path, _ := body["path"].(string); strings.TrimSpace(path) != "" {
			return fmt.Sprintf("%s: %s", name, strings.TrimSpace(path))
		}
	case "grep", "search_content":
		if detail := summarizeContentSearchCall(body); detail != "" {
			return fmt.Sprintf("%s: %s", name, detail)
		}
	case "search_files":
		if detail := summarizeFileSearchCall(body); detail != "" {
			return fmt.Sprintf("%s: %s", name, detail)
		}
	case "read_file":
		if path, _ := body["file_path"].(string); strings.TrimSpace(path) != "" {
			return fmt.Sprintf("%s: %s", name, strings.TrimSpace(path))
		}
	case "web_search":
		if query := summarizeWebSearchCall(body); query != "" {
			return fmt.Sprintf("web_search: %s", query)
		}
	case "fetch", "web_fetch":
		if u, _ := body["url"].(string); strings.TrimSpace(u) != "" {
			return fmt.Sprintf("%s: %s", name, strings.TrimSpace(u))
		}
	case "multi_edit":
		if path, _ := body["file_path"].(string); strings.TrimSpace(path) != "" {
			return fmt.Sprintf("multi_edit: %s", strings.TrimSpace(path))
		}
	case "request_user_input":
		if qs := body["questions"]; qs != nil {
			return fmt.Sprintf("request_user_input: %d question(s)", len(core.AsAnySlice(qs)))
		}
	case "update_plan":
		if plan := core.AsAnySlice(body["plan"]); len(plan) > 0 {
			return fmt.Sprintf("update_plan: %d step(s)", len(plan))
		}
		return "update_plan"
	case "todo_add":
		if text := strings.TrimSpace(core.AsString(body["text"])); text != "" {
			return "todo_add: " + text
		}
	case "todo_update":
		if text := strings.TrimSpace(core.AsString(body["text"])); text != "" {
			return "todo_update: " + text
		}
		if id := strings.TrimSpace(core.AsString(body["id"])); id != "" {
			return "todo_update: " + id
		}
	case "todo_remove":
		if id := strings.TrimSpace(core.AsString(body["id"])); id != "" {
			return "todo_remove: " + id
		}
	case "todo_list":
		return "todo_list"
	case "todo_clear_done":
		return "todo_clear_done"
	}
	if strings.TrimSpace(call.Input) != "" {
		return fmt.Sprintf("%s: %s", name, strings.TrimSpace(call.Input))
	}
	return name
}

func summarizeContentSearchCall(body map[string]any) string {
	pattern := strings.TrimSpace(core.AsString(body["pattern"]))
	path := strings.TrimSpace(core.AsString(body["path"]))
	include := strings.TrimSpace(core.AsString(body["include"]))
	if pattern == "" {
		return ""
	}
	return appendSearchScope(pattern, path, include)
}

func summarizeFileSearchCall(body map[string]any) string {
	pattern := strings.TrimSpace(core.AsString(body["pattern"]))
	path := strings.TrimSpace(core.AsString(body["path"]))
	if pattern == "" {
		return ""
	}
	return appendSearchScope(pattern, path, "")
}

func summarizeWebSearchCall(body map[string]any) string {
	if query := strings.TrimSpace(core.AsString(body["query"])); query != "" {
		return query
	}
	if query := strings.TrimSpace(core.AsString(body["q"])); query != "" {
		return query
	}
	for _, entry := range core.AsAnySlice(body["search_query"]) {
		obj, _ := entry.(map[string]any)
		if query := strings.TrimSpace(core.AsString(obj["q"])); query != "" {
			return query
		}
		if query := strings.TrimSpace(core.AsString(obj["query"])); query != "" {
			return query
		}
	}
	return ""
}

func appendSearchScope(subject, path, include string) string {
	detail := strings.TrimSpace(subject)
	if detail == "" {
		return ""
	}
	if strings.TrimSpace(path) != "" {
		detail += " in " + strings.TrimSpace(path)
	}
	if strings.TrimSpace(include) != "" {
		detail += " (" + strings.TrimSpace(include) + ")"
	}
	return detail
}
