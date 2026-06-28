package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/compact"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/memory"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
)

type RunOptions struct {
	HiddenInput        bool
	ReadOnly           bool
	GoalContinuation   bool
	ShellAllowPrefixes []string
	ViewMode           string
	AssistantPrefix    string
	PrefixCompletion   bool
	WorkflowAuthoring  bool
	// SuppressTools forces the turn to be sent without any tools. Prefix
	// completion requests also require no current tool schemas.
	SuppressTools bool
}

func (a *Agent) RunStreamWithOptions(ctx context.Context, sessionID, input string, hiddenInput bool) (<-chan AgentEvent, error) {
	return a.RunStreamWithTurnOptions(ctx, sessionID, input, RunOptions{HiddenInput: hiddenInput})
}

func (a *Agent) RunStreamWithTurnOptions(ctx context.Context, sessionID, input string, opts RunOptions) (<-chan AgentEvent, error) {
	return a.RunStreamWithContentOptions(ctx, sessionID, []core.MessagePart{{Type: core.MessagePartText, Text: input}}, opts)
}

func (a *Agent) RunStreamWithContentOptions(ctx context.Context, sessionID string, parts []core.MessagePart, opts RunOptions) (<-chan AgentEvent, error) {
	return a.runStreamWithNewMessages(ctx, sessionID, []core.Message{
		core.UserMessageFromParts(sessionID, parts, opts.HiddenInput),
	}, opts)
}

func (a *Agent) RunStreamWithInjectedInput(ctx context.Context, sessionID, visibleInput, hiddenInput string) (<-chan AgentEvent, error) {
	return a.RunStreamWithInjectedInputOptions(ctx, sessionID, visibleInput, hiddenInput, RunOptions{})
}

func (a *Agent) RunStreamWithInjectedInputOptions(ctx context.Context, sessionID, visibleInput, hiddenInput string, opts RunOptions) (<-chan AgentEvent, error) {
	return a.RunStreamWithInjectedContentOptions(ctx, sessionID, []core.MessagePart{{Type: core.MessagePartText, Text: visibleInput}}, hiddenInput, opts)
}

func (a *Agent) RunStreamWithInjectedContentOptions(ctx context.Context, sessionID string, visibleParts []core.MessagePart, hiddenInput string, opts RunOptions) (<-chan AgentEvent, error) {
	return a.runStreamWithNewMessages(ctx, sessionID, []core.Message{
		core.UserMessageFromParts(sessionID, visibleParts, false),
		core.TextMessage(sessionID, core.RoleUser, hiddenInput, true),
	}, opts)
}

func (a *Agent) InjectTurnInput(ctx context.Context, sessionID string, newMessages []core.Message) (bool, error) {
	state, ok := a.active.Load(sessionID)
	if !ok {
		return false, nil
	}
	turnState, ok := state.(*activeTurnState)
	if !ok {
		return false, nil
	}
	createdMessages := make([]core.Message, 0, len(newMessages))
	for _, msg := range newMessages {
		msg.SessionID = sessionID
		created, err := a.store.Create(ctx, msg)
		if err != nil {
			return true, fmt.Errorf("create injected user message: %w", err)
		}
		createdMessages = append(createdMessages, created)
	}
	turnState.appendPending(createdMessages)
	return true, nil
}

func (a *Agent) runStreamWithNewMessages(ctx context.Context, sessionID string, newMessages []core.Message, opts RunOptions) (<-chan AgentEvent, error) {
	startToolResultCleanupLoop(a.cleanupLoopContext(), a.toolResultArchiveDir)
	turnState := &activeTurnState{}
	if _, loaded := a.active.LoadOrStore(sessionID, turnState); loaded {
		return nil, ErrSessionBusy
	}
	if spent, blocked := a.budgetExceeded(sessionID); blocked {
		a.active.Delete(sessionID)
		return nil, fmt.Errorf("%w: spent $%.6f >= cap $%.6f", ErrBudgetExceeded, spent, a.budgetWarningUSD)
	}

	createdMessages := make([]core.Message, 0, len(newMessages))
	for _, msg := range newMessages {
		msg.SessionID = sessionID
		msg = core.NormalizeMessageContent(msg)
		created, err := a.store.Create(ctx, msg)
		if err != nil {
			a.active.Delete(sessionID)
			return nil, fmt.Errorf("create user message: %w", err)
		}
		createdMessages = append(createdMessages, created)
	}
	history, err := a.store.List(ctx, sessionID)
	if err != nil {
		a.active.Delete(sessionID)
		return nil, fmt.Errorf("list messages: %w", err)
	}

	out := make(chan AgentEvent, 16)
	go func() {
		defer close(out)
		defer a.active.Delete(sessionID)
		toolSnapshot, err := a.refreshToolSnapshotForTurn(ctx, opts)
		if err != nil {
			emit := func(ev AgentEvent) bool {
				return sendAgentEvent(ctx, out, ev)
			}
			emit(AgentEvent{Type: AgentEventTypeError, Err: err})
			return
		}
		rt := memory.HydrateRuntime(memory.NewImmutablePrefix(a.buildImmutableSystemBlocksWithTools(toolSnapshot, opts)), history)
		rt.SetRuntimeBlocks(a.buildRuntimeSystemBlocks(opts))
		modelTurns := 0
		toolIters := 0
		toolCalls := 0
		planLoopNudges := 0
		leakedToolCallNudges := 0
		consecutiveStormRounds := 0
		consecutiveRedundantRounds := 0
		progress := &progressTracker{}
		if a.repairer != nil {
			a.repairer.resetStorm()
		}
		emit := func(ev AgentEvent) bool {
			return sendAgentEvent(ctx, out, ev)
		}
		turnPolicy := a.policy
		if len(opts.ShellAllowPrefixes) > 0 {
			turnPolicy = policy.ScopedAllowPolicy{
				Base:               a.policy,
				ShellAllowPrefixes: append([]string(nil), opts.ShellAllowPrefixes...),
			}
		}
		if opts.ReadOnly {
			turnPolicy = policy.ReadOnlyTurnPolicy{Base: turnPolicy}
		}
		autoDenyCounts := map[string]int{}
		firstRequest := true
		for {
			if pending := turnState.drainPending(); len(pending) > 0 {
				for _, msg := range pending {
					rt.Log.Append(msg)
					history = append(history, msg)
				}
			}
			rt.Scratch.ResetTurn()
			if !firstRequest {
				var err error
				toolSnapshot, err = a.refreshToolSnapshotForTurn(ctx, opts)
				if err != nil {
					emit(AgentEvent{Type: AgentEventTypeError, Err: err})
					return
				}
			}
			firstRequest = false
			rt.SetRuntimeBlocks(a.buildRuntimeSystemBlocks(opts))
			if a.autoCompact {
				before := compact.EstimateMessagesTokens(rt.BuildProviderHistory())
				if float64(before)/float64(max(1, a.contextWindow)) > a.compactThresh {
					summaryCtx := summaryRequestContextFromPrefix(rt.Prefix, rt.RuntimeBlocks())
					replacement, info, err := a.compactHistory(ctx, sessionID, history, true, a.hookRunObserver(ctx, out), summaryCtx)
					if err != nil {
						emit(AgentEvent{Type: AgentEventTypeError, Err: err})
						return
					}
					if info.Compacted {
						_ = rt.Log.RewriteWithReason(memory.RewriteReasonCompact, replacement)
						history = replacement
						info.BeforeEstimate = before
						info.AfterEstimate = compact.EstimateMessagesTokens(rt.BuildProviderHistory())
						if !emit(AgentEvent{
							Type:    AgentEventTypeContextCompacted,
							Compact: &info,
						}) {
							return
						}
					} else {
						info.BeforeEstimate = before
						info.AfterEstimate = before
						if !emit(AgentEvent{
							Type:    AgentEventTypeContextCompacted,
							Compact: &info,
						}) {
							return
						}
					}
				}
			}
			remainingToolCalls := 0
			if a.maxToolCalls > 0 {
				remainingToolCalls = a.maxToolCalls - toolCalls
			}
			if actual, ok := rt.Prefix.VerifyFingerprint(); !ok {
				if !emit(AgentEvent{Type: AgentEventTypePrefixDrift, PrefixDrift: &PrefixDriftInfo{Expected: rt.Prefix.Fingerprint(), Actual: actual}}) {
					return
				}
			}
			assistant, toolMsg, usage, modelName, cacheShape, abortTurn, attemptedToolCalls, sErr := a.streamAndHandle(ctx, sessionID, history, rt, out, turnPolicy, toolSnapshot, remainingToolCalls, autoDenyCounts, opts)
			if sErr != nil {
				if errors.Is(sErr, context.Canceled) {
					a.persistInterruptedTurnMarker(sessionID)
					emit(AgentEvent{Type: AgentEventTypeTurnCancelled, Content: "turn cancelled"})
					return
				}
				// A deadline is a timeout, not a user act — telling the
				// model "the user interrupted on purpose" would misstate
				// intent on every slow request.
				emit(AgentEvent{Type: AgentEventTypeError, Err: sErr})
				return
			}
			modelTurns++
			turnCost := a.recordTurnCost(sessionID, usage, modelName, rt.Prefix.Fingerprint(), cacheShape)
			if !emit(AgentEvent{Type: AgentEventTypeUsage, Usage: &UsageInfo{Model: modelName, Usage: usage}}) {
				return
			}
			if m := buildPrefixCacheMetrics(modelName, usage, rt.Prefix.Fingerprint(), cacheShape); m != nil {
				if !emit(AgentEvent{Type: AgentEventTypePrefixCacheMetrics, CacheMetrics: m}) {
					return
				}
			}
			if !a.emitBudgetWarningIfNeeded(ctx, sessionID, turnCost, out) {
				return
			}
			if abortTurn {
				if toolMsg != nil {
					rt.Log.Append(assistant)
					rt.Log.Append(*toolMsg)
					history = append(history, assistant, *toolMsg)
				}
				if ctx.Err() != nil {
					if errors.Is(ctx.Err(), context.Canceled) {
						a.persistInterruptedTurnMarker(sessionID)
						return
					}
					// Deadline expiry is a timeout, not a user interrupt:
					// surface it instead of closing the stream silently.
					emit(AgentEvent{Type: AgentEventTypeError, Err: ctx.Err()})
					return
				}
				if turnState.hasPending() {
					if !emit(AgentEvent{Type: AgentEventTypeResponseReset}) {
						return
					}
					continue
				}
				done := assistant
				done.FinishReason = core.FinishReasonEndTurn
				emit(AgentEvent{Type: AgentEventTypeDone, Message: &done})
				return
			}
			if assistant.FinishReason == core.FinishReasonToolUse && toolMsg != nil {
				toolIters++
				toolCalls += attemptedToolCalls
				rt.Log.Append(assistant)
				rt.Log.Append(*toolMsg)
				history = append(history, assistant, *toolMsg)
				// Runaway-loop guards. The main agent runs without a tool-iter
				// cap, so an infinite loop can't be stopped by a round count.
				// Two repetition signals bound it instead: a round whose every
				// result was storm-blocked (the model re-issuing identical calls)
				// and a "redundant" round flagged by the progress guard (same
				// target, varying args — e.g. re-reading one file with a stepping
				// offset, which the storm breaker never sees).
				stormRound := isAllStormBlocked(*toolMsg)
				if stormRound {
					consecutiveStormRounds++
				} else {
					consecutiveStormRounds = 0
				}
				readOnly := func(c core.ToolCall) bool {
					spec, ok := toolSnapshot.Spec(c.Name)
					if !ok {
						return false
					}
					return core.IsReadOnlyToolCall(spec, c)
				}
				if progress.observe(assistant.ToolCalls, toolMsg.ToolResults, readOnly) {
					consecutiveRedundantRounds++
				} else {
					consecutiveRedundantRounds = 0
				}
				loopDetected := consecutiveStormRounds >= maxConsecutiveStormRounds ||
					consecutiveRedundantRounds >= maxConsecutiveRedundantRounds
				// In Plan mode, a spinning turn never ends on its own, so the
				// end-of-turn finalization can't reach it. Before the runaway-loop
				// guard terminates planning with a contentless force-summary, give
				// the model one chance to stop investigating and write its plan.
				if loopDetected && a.mode == session.ModePlan && planLoopNudges < maxPlanLoopNudges {
					planLoopNudges++
					consecutiveStormRounds = 0
					consecutiveRedundantRounds = 0
					progress.reset()
					if a.repairer != nil {
						a.repairer.resetStorm()
					}
					nudge, err := a.persistPlanLoopNudge(ctx, sessionID)
					if err != nil {
						emit(AgentEvent{Type: AgentEventTypeError, Err: err})
						return
					}
					rt.Log.Append(nudge)
					history = append(history, nudge)
					continue
				}
				if consecutiveStormRounds >= maxConsecutiveStormRounds {
					a.forceSummaryAndFinish(ctx, sessionID, history, "repetitive tool-call loop detected", summaryRequestContextFromPrefix(rt.Prefix, rt.RuntimeBlocks()), emit)
					return
				}
				if consecutiveRedundantRounds >= maxConsecutiveRedundantRounds {
					a.forceSummaryAndFinish(ctx, sessionID, history, "redundant tool-call loop (no progress) detected", summaryRequestContextFromPrefix(rt.Prefix, rt.RuntimeBlocks()), emit)
					return
				}
				if a.maxTurns > 0 && modelTurns >= a.maxTurns {
					a.forceSummaryAndFinish(ctx, sessionID, history, "turn cap reached", summaryRequestContextFromPrefix(rt.Prefix, rt.RuntimeBlocks()), emit)
					return
				}
				if a.maxToolCalls > 0 && toolCalls >= a.maxToolCalls {
					a.forceSummaryAndFinish(ctx, sessionID, history, "tool call cap reached", summaryRequestContextFromPrefix(rt.Prefix, rt.RuntimeBlocks()), emit)
					return
				}
				if a.maxToolIters > 0 && toolIters >= a.maxToolIters {
					a.forceSummaryAndFinish(ctx, sessionID, history, "tool iteration cap reached", summaryRequestContextFromPrefix(rt.Prefix, rt.RuntimeBlocks()), emit)
					return
				}
				// Backstop only the capless main agent (maxToolIters == 0). A
				// caller that explicitly configured a cap — even one above the
				// backstop — already opted into its own ceiling above; honor it
				// rather than truncating their run early at mainAgentToolIterBackstop.
				if a.maxToolIters == 0 && toolIters >= mainAgentToolIterBackstop {
					a.forceSummaryAndFinish(ctx, sessionID, history, "tool iteration backstop reached", summaryRequestContextFromPrefix(rt.Prefix, rt.RuntimeBlocks()), emit)
					return
				}
				if turnState.hasPending() {
					if !emit(AgentEvent{Type: AgentEventTypeResponseReset}) {
						return
					}
				}
				continue
			}
			if turnState.hasPending() {
				rt.Log.Append(assistant)
				history = append(history, assistant)
				if !emit(AgentEvent{Type: AgentEventTypeResponseReset}) {
					return
				}
				continue
			}
			// Leaked-tool-call recovery. A turn can "stop" mid-task when the model
			// writes its tool calls as plain text (e.g. <tool_calls><read_file .../>
			// </tool_calls>) instead of using the API tool-call channel: such text
			// never executes, so the turn reaches here as if the model answered when
			// it meant to act. Scrub the wrapper from the persisted assistant text —
			// otherwise it pollutes history and the model keeps copying the format —
			// and nudge once toward the structured channel rather than finishing
			// silently. Bounded by maxLeakedToolCallNudges so a real final answer
			// that merely quotes a wrapper still terminates.
			if leakedToolCallNudges < maxLeakedToolCallNudges && containsLeakedToolCall(assistant.Text) {
				leakedToolCallNudges++
				cleaned := strings.TrimSpace(stripLeakedToolCalls(assistant.Text))
				assistant.Text = cleaned
				if cleaned == "" {
					assistant.Parts = nil
				} else {
					assistant.Parts = []core.MessagePart{{Type: core.MessagePartText, Text: cleaned}}
				}
				a.bestEffortUpdateAssistant(assistant)
				nudge, err := a.persistLeakedToolCallNudge(ctx, sessionID)
				if err != nil {
					emit(AgentEvent{Type: AgentEventTypeError, Err: err})
					return
				}
				rt.Log.Append(assistant)
				rt.Log.Append(nudge)
				history = append(history, assistant, nudge)
				if !emit(AgentEvent{Type: AgentEventTypeLeakedToolCallScrubbed}) {
					return
				}
				// The leaked wrapper was streamed as assistant content deltas before
				// it was scrubbed, so downstream text accumulators (the service turn
				// loop's LastResponse, whale exec output, the TUI live attempt) still
				// hold the fake tool-call text even though the persisted message was
				// cleaned. A response reset tells them to drop it; the recovered turn
				// re-streams the real answer next iteration. Mirrors the pending-input
				// reset above.
				if !emit(AgentEvent{Type: AgentEventTypeResponseReset}) {
					return
				}
				continue
			}
			// Plan-as-reply finalization: in Plan mode the assistant's final
			// answer IS the plan. A turn that ends with text (rather than a tool
			// call such as request_user_input) is an approvable plan, so emit
			// PlanCompleted to open the implementation gate. An empty final turn
			// yields no plan — the user can simply ask again — matching reasonix.
			if a.mode == session.ModePlan && strings.TrimSpace(assistant.Text) != "" {
				emit(AgentEvent{Type: AgentEventTypePlanCompleted, Content: assistant.Text})
			}
			emit(AgentEvent{Type: AgentEventTypeDone, Message: &assistant})
			return
		}
	}()

	return out, nil
}
