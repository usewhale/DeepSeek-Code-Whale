package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm"
	"github.com/usewhale/whale/internal/memory"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/telemetry"
)

func (a *Agent) collectAssistantStream(ctx context.Context, sessionID string, rt *memory.RuntimeState, events chan<- AgentEvent, tools *core.ToolRegistry, opts RunOptions) (core.Message, llm.Usage, string, *telemetry.CacheShape, error) {
	assistant, err := a.store.Create(ctx, core.Message{SessionID: sessionID, Role: core.RoleAssistant})
	if err != nil {
		return core.Message{}, llm.Usage{}, "", nil, fmt.Errorf("create assistant message: %w", err)
	}
	lastUsage := llm.Usage{}
	lastModel := ""
	var ps planState
	assistantDeltaSeen := false
	streamPersisted := false
	emit := func(ev AgentEvent) bool {
		return sendAgentEvent(ctx, events, ev)
	}

	history := a.buildTurnProviderHistory(sessionID, rt)
	// Advertise the same tool set in every mode. Plan-mode restrictions (e.g.
	// update_plan is not a proposal channel) are enforced at execution time via
	// the plan_mode_blocked gate in stream.go, not by changing the advertised
	// tools. Keeping the tool schema identical across an agent<->plan toggle keeps
	// the provider's prefix cache warm — a per-mode tool list invalidates the
	// whole cached prefix (DeepSeek keys the cache on the tool schema), costing a
	// full prompt re-bill on every switch. This mirrors reasonix's design: "the
	// cache-friendly bits — system prompt, tools schema, message history — are
	// left untouched, so the toggle costs nothing in cache hits."
	toolList := tools.Tools()
	if opts.SuppressTools {
		toolList = nil
	}
	var ch <-chan llm.ProviderEvent
	requestedAssistantPrefix := ""
	if opts.PrefixCompletion && strings.TrimSpace(opts.AssistantPrefix) != "" && len(toolList) == 0 {
		if prefixProvider, ok := a.provider.(llm.PrefixCompletionProvider); ok {
			requestedAssistantPrefix = opts.AssistantPrefix
			ch = prefixProvider.StreamResponseWithPrefix(ctx, history, opts.AssistantPrefix, nil)
		}
	}
	if ch == nil {
		ch = a.provider.StreamResponse(ctx, history, toolList)
	}
	for ev := range ch {
		switch ev.Type {
		case llm.EventContentDelta:
			assistantDeltaSeen = true
			visible, ok := a.emitAssistantContentDelta(ctx, ev.Content, &ps, events)
			if !ok {
				return core.Message{}, llm.Usage{}, "", nil, ctx.Err()
			}
			if a.mode == session.ModePlan {
				assistant.Text = visible
			} else {
				assistant.Text += visible
			}
			// Intentionally do not persist on every delta: rewriting the full
			// session JSONL per token produced O(n·m) disk I/O and caused TUI
			// stutter in long-context sessions (issue #22). The accumulated
			// state is flushed by the EventComplete / bestEffortUpdateAssistant
			// paths below.
		case llm.EventReasoningDelta:
			assistant.Reasoning += ev.ReasoningDelta
			rt.Scratch.Reasoning += ev.ReasoningDelta
			if !emit(AgentEvent{Type: AgentEventTypeReasoningDelta, ReasoningDelta: ev.ReasoningDelta}) {
				return core.Message{}, llm.Usage{}, "", nil, ctx.Err()
			}
		case llm.EventToolArgsDelta:
			if ev.ToolArgsDelta != nil {
				rt.Scratch.UpdateToolArgs(ev.ToolArgsDelta.ToolCallIndex, ev.ToolArgsDelta.ToolName, ev.ToolArgsDelta.ArgsChars, ev.ToolArgsDelta.ReadyCount)
				if !emit(AgentEvent{
					Type: AgentEventTypeToolArgsDelta,
					ToolArgs: &ToolArgsProgress{
						ToolCallIndex: ev.ToolArgsDelta.ToolCallIndex,
						ToolName:      ev.ToolArgsDelta.ToolName,
						ArgsChars:     ev.ToolArgsDelta.ArgsChars,
						ReadyCount:    ev.ToolArgsDelta.ReadyCount,
					},
				}) {
					return core.Message{}, llm.Usage{}, "", nil, ctx.Err()
				}
			}
		case llm.EventToolUseStart:
			// Tool-use start events arrive before streamed arguments are
			// complete. Persisting them here leaves dangling or empty
			// tool_calls behind if the stream fails before EventComplete.
		case llm.EventToolUseStop:
			// no-op in this minimal version
		case llm.EventRetryScheduled:
			if ev.Retry != nil {
				info := *ev.Retry
				if info.StreamReset {
					assistant.Text = ""
					assistant.Parts = nil
					assistant.Reasoning = ""
					assistant.ToolCalls = nil
					assistant.FinishReason = ""
					rt.Scratch.ResetTurn()
					ps = planState{}
					assistantDeltaSeen = false
					streamPersisted = false
					if err := a.store.Update(ctx, assistant); err != nil {
						return core.Message{}, llm.Usage{}, "", nil, err
					}
				}
				if !emit(AgentEvent{Type: AgentEventTypeProviderRetryScheduled, ProviderRetry: &info}) {
					return core.Message{}, llm.Usage{}, "", nil, ctx.Err()
				}
			}
		case llm.EventComplete:
			if ev.Response != nil {
				lastUsage = ev.Response.Usage
				assistant.Usage = messageUsageFrom(ev.Response.Usage)
				lastModel = ev.Response.Model
				assistant.FinishReason = ev.Response.FinishReason
				if ev.Response.Reasoning != "" {
					assistant.Reasoning = ev.Response.Reasoning
				} else if strings.TrimSpace(rt.Scratch.Reasoning) != "" {
					assistant.Reasoning = rt.Scratch.Reasoning
				}
				if len(ev.Response.ToolCalls) > 0 {
					assistant.ToolCalls = ev.Response.ToolCalls
					// Emit tool call events now that Input is fully populated.
					for i := range ev.Response.ToolCalls {
						tc := ev.Response.ToolCalls[i]
						if !emit(AgentEvent{Type: AgentEventTypeToolCall, ToolCall: &tc}) {
							return core.Message{}, llm.Usage{}, "", nil, ctx.Err()
						}
						if taskEvent, ok := taskStartedEvent(tc); ok {
							if !emit(taskEvent) {
								return core.Message{}, llm.Usage{}, "", nil, ctx.Err()
							}
						}
					}
				}
				if ev.Response.Content != "" {
					if !assistantDeltaSeen {
						visible, ok := a.emitAssistantContentDelta(ctx, ev.Response.Content, &ps, events)
						if !ok {
							return core.Message{}, llm.Usage{}, "", nil, ctx.Err()
						}
						assistant.Text = visible
					} else {
						// EventComplete carries the authoritative full content:
						// prefix-completion and sparse-stream providers may stream
						// only a partial prefix and deliver the complete answer here.
						// Honor it over the accumulated deltas (same as the non-Plan
						// path). In Plan mode, also sync the accumulated plan text so
						// the persisted plan part and the plan_completed event carry
						// the full plan — plan_completed then re-renders it via SetPlan.
						assistant.Text = ev.Response.Content
						if a.mode == session.ModePlan {
							ps.text.Reset()
							ps.text.WriteString(ev.Response.Content)
						}
					}
				}
				finalizeAssistantPlanParts(&assistant, &ps)
				if err := a.store.Update(ctx, assistant); err != nil {
					return core.Message{}, llm.Usage{}, "", nil, err
				}
				streamPersisted = true
			}
		case llm.EventError:
			if ev.Err != nil {
				// A user interrupt surfaces here as context.Canceled from
				// the provider goroutine; label the turn canceled so the
				// session history doesn't record a clean interrupt as a
				// provider error (session 019ead56 had six such turns).
				if errors.Is(ev.Err, context.Canceled) {
					assistant.FinishReason = core.FinishReasonCanceled
				} else {
					assistant.FinishReason = core.FinishReasonError
				}
				assistant.ErrorDetail = cancellationErrorDetail(ctx, ev.Err)
				assistant.ToolCalls = nil
				a.bestEffortUpdateAssistant(assistant)
				return core.Message{}, llm.Usage{}, "", nil, ev.Err
			}
		}
	}
	// Provider channel can close without a terminal EventComplete/EventError
	// (e.g. SSE EOF before [DONE], or EventComplete with nil Response). Since
	// deltas no longer persist, flush any accumulated assistant state so
	// resume/history doesn't see an empty assistant message. In Plan mode the
	// accumulated plan text lives in ps; attach it as the message's plan part.
	if !streamPersisted {
		if a.mode == session.ModePlan {
			assistant.Text = strings.TrimSpace(ps.text.String())
			finalizeAssistantPlanParts(&assistant, &ps)
		}
		if assistant.Text != "" || assistant.Reasoning != "" || len(assistant.ToolCalls) > 0 || len(assistant.Parts) > 0 {
			a.bestEffortUpdateAssistant(assistant)
		}
	}
	assistantPrefix := ""
	if lastUsage.PrefixCompletionRequests > 0 {
		assistantPrefix = requestedAssistantPrefix
	}
	cacheShape := buildCacheShapeForRequestWithRuntime(cacheShapeRequestAgent, history, toolList, assistantPrefix, rt.Prefix.SystemBlocks(), rt.RuntimeBlocks())
	return assistant, lastUsage, lastModel, cacheShape, nil
}

func cancellationErrorDetail(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	detail := err.Error()
	if !errors.Is(err, context.Canceled) {
		return detail
	}
	cause := context.Cause(ctx)
	if cause == nil || cause == context.Canceled {
		return detail
	}
	return detail + " (cancel cause: " + cause.Error() + ")"
}

// messageUsageFrom converts provider usage into the persisted form,
// returning nil when the provider reported nothing.
func messageUsageFrom(u llm.Usage) *core.MessageUsage {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.PromptCacheHitTokens == 0 && u.PromptCacheMissTokens == 0 {
		return nil
	}
	return &core.MessageUsage{
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		PromptCacheHitTokens:  u.PromptCacheHitTokens,
		PromptCacheMissTokens: u.PromptCacheMissTokens,
	}
}
