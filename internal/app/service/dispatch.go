package service

import (
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/app"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
)

func (s *Service) Dispatch(in Intent) {
	switch in.Kind {
	case IntentSubmit:
		s.goTracked(func() { s.handleSubmit(in.Input, in.HiddenInput, in.SkillBinding, in.ClientInputID, in.Attachments) })
	case IntentSubmitLocal:
		s.enqueueLocalSubmit(in.Input)
	case IntentAllowTool:
		s.resolveApproval(in.ToolCallID, policy.ApprovalAllow)
	case IntentAllowToolForSession:
		s.resolveApproval(in.ToolCallID, policy.ApprovalAllowForSession)
	case IntentDenyTool:
		s.resolveApproval(in.ToolCallID, policy.ApprovalDeny)
	case IntentCancelToolApproval:
		s.resolveApproval(in.ToolCallID, policy.ApprovalCancel)
	case IntentSubmitUserInput:
		if in.UserInput != nil {
			s.resolveUserInput(in.ToolCallID, *in.UserInput, true)
		}
	case IntentCancelUserInput:
		s.resolveUserInput(in.ToolCallID, core.UserInputResponse{}, false)
	case IntentRequestSessions:
		s.emitSessionChoices()
	case IntentRequestExit:
		s.requestExit()
	case IntentSelectSession:
		res, err := s.app.ApplyResumeChoice(in.SessionInput)
		if err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emit(Event{Kind: EventInfo, Text: res.Message})
		if res.Resumed {
			s.emitSessionHydrated()
		} else {
			s.emitSessionChoices()
		}
	case IntentShutdown:
		s.cancelMu.Lock()
		if s.cancelCause != nil {
			s.cancelCause(agent.NewUserInterrupt(in.InterruptSource))
		} else if s.cancel != nil {
			s.cancel()
		}
		s.cancelMu.Unlock()
		s.cancelPendingInteractions()
	case IntentWorktreeExitChoice:
		s.handleWorktreeExitChoice(in.WorktreeAction)
	case IntentRequestWorkflowPanel:
		s.emitWorkflowPanel(in.WorkflowRunID)
	case IntentCancelWorkflowRun:
		s.cancelWorkflowRun(in.WorkflowRunID)
	case IntentStartWorkflow:
		s.startWorkflow(in.WorkflowName, in.WorkflowArgs, in.WorkflowResume, in.WorkflowTrust, in.WorkflowScript, in.WorkflowSaveAs, in.WorkflowScriptPath)
	case IntentSetModelAndEffort:
		if err := s.app.SetModelAndEffort(in.Model, in.Effort); err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		if strings.EqualFold(strings.TrimSpace(in.Thinking), "on") {
			s.app.SetThinkingEnabled(true)
		}
		if strings.EqualFold(strings.TrimSpace(in.Thinking), "off") {
			s.app.SetThinkingEnabled(false)
		}
		s.emit(Event{Kind: EventInfo, Text: fmt.Sprintf("model set: %s  effort: %s  thinking: %s", s.app.Model(), s.app.ReasoningEffort(), app.OnOff(s.app.ThinkingEnabled()))})
		s.emit(Event{Kind: EventTurnDone})
	case IntentSetApprovalMode:
		enabled := in.ApprovalMode == "auto_accept"
		s.app.SetAutoAcceptPermissions(enabled)
		msg := "Auto-accept edits enabled"
		if !enabled {
			msg = "Ask for approval"
		}
		s.emit(Event{Kind: EventInfo, Text: msg, AutoAccept: enabled, AutoAcceptKnown: true})
		s.emit(Event{Kind: EventTurnDone, LastResponse: msg})
	case IntentSetAutoReview:
		s.app.SetAutoReviewEnabled(in.AutoReview)
		msg := "Auto-review enabled"
		if !in.AutoReview {
			msg = "Auto-review disabled"
		}
		enabled := s.app.AutoAcceptPermissions()
		s.emit(Event{Kind: EventInfo, Text: msg, AutoAccept: enabled, AutoAcceptKnown: true, AutoReview: in.AutoReview})
		s.emit(Event{Kind: EventTurnDone, LastResponse: msg})
	case IntentEnableAutoAccept:
		s.app.SetAutoAcceptPermissions(true)
		s.emit(Event{Kind: EventInfo, Text: autoAcceptMessage(true), AutoAccept: true, AutoAcceptKnown: true})
	case IntentSetViewMode:
		if err := s.app.SetViewMode(in.ViewMode); err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emit(Event{Kind: EventViewModeChanged, ViewMode: s.app.ViewMode(), Text: app.ViewModeToggleMessage(s.app.ViewMode())})
		s.emit(Event{Kind: EventTurnDone})
	case IntentToggleMode:
		msg, err := s.app.ToggleMode()
		if err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emit(Event{Kind: EventInfo, Text: msg})
		s.emit(Event{Kind: EventTurnDone, LastResponse: msg})
	case IntentImplementPlan:
		out, err := s.app.SetMode(session.ModeAgent)
		if err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			s.emit(Event{Kind: EventTurnDone})
			return
		}
		s.emit(Event{Kind: EventInfo, Text: out})
		s.goTracked(func() { s.runInjectedTurn("Implement the plan.", buildImplementPlanPrompt(in.Input)) })
	case IntentDeclinePlan:
		s.app.RecordPlanNotApproved()
		const msg = "Plan not approved; staying in Plan mode"
		s.emit(Event{Kind: EventInfo, Text: msg})
		s.emit(Event{Kind: EventTurnDone, LastResponse: msg})
	case IntentRequestSkillsManage:
		s.emit(Event{Kind: EventSkillsManagerUpdated, Skills: protocolSkills(s.SkillsForManager())})
	case IntentSetSkillEnabled:
		if _, err := s.app.SetSkillEnabled(in.SkillName, in.SkillEnabled); err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emit(Event{Kind: EventSkillsManagerUpdated, Skills: protocolSkills(s.SkillsForManager())})
	case IntentSetPluginEnabled:
		s.goTracked(func() {
			if _, err := s.app.SetPluginEnabled(in.PluginID, in.PluginEnabled); err != nil {
				s.emit(Event{Kind: EventError, Text: err.Error()})
				return
			}
			s.emit(Event{Kind: EventPluginsManagerUpdated, Plugins: protocolPlugins(s.PluginsForManager())})
		})
	case IntentRequestHooksManage:
		s.emitHooksManagerUpdated()
	case IntentRequestConfigManage:
		s.emitConfigManagerUpdated(true)
	case IntentApplyConfigSettings:
		s.goTracked(func() {
			updates := make([]app.ConfigSettingUpdate, 0, len(in.ConfigUpdates))
			for _, update := range in.ConfigUpdates {
				updates = append(updates, app.ConfigSettingUpdate{ID: update.ID, Value: update.Value})
			}
			res, err := s.app.ApplyConfigSettings(updates)
			if err != nil {
				s.emit(Event{Kind: EventError, Text: err.Error()})
				s.emitConfigManagerUpdated(false)
				return
			}
			msg := "config unchanged"
			if len(res.Updated) > 0 {
				msg = fmt.Sprintf("updated %d config setting(s): %s", len(res.Updated), configSettingLabels(res.Updated))
				if res.Path != "" {
					msg += "\nconfig: " + res.Path
				}
			}
			s.emit(Event{Kind: EventLocalSubmitResult, Status: "ok", Text: msg})
			s.emitConfigManagerUpdated(false)
		})
	case IntentSetHookEnabled:
		if _, err := s.app.SetHookEnabled([]string{in.HookKey}, in.HookEnabled); err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emitHooksManagerUpdated()
	case IntentTrustHook:
		if _, err := s.app.TrustHooks([]string{in.HookKey}); err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emitHooksManagerUpdated()
	case IntentTrustHooks:
		if _, err := s.app.TrustHooks(nil); err != nil {
			s.emit(Event{Kind: EventError, Text: err.Error()})
			return
		}
		s.emitHooksManagerUpdated()
	case IntentResolveHooksStartupReview:
		s.resolveHooksStartupReview(in.HooksReviewAction)
	}
}

func (s *Service) emitHooksManagerUpdated() {
	s.emit(Event{Kind: EventHooksManagerUpdated, Hooks: protocolHooks(s.app.HookEntries())})
}

func (s *Service) emitConfigManagerUpdated(open bool) {
	state, err := s.app.ConfigSettings()
	if err != nil {
		s.emit(Event{Kind: EventError, Text: err.Error()})
		return
	}
	s.emit(Event{Kind: EventConfigManagerUpdated, Config: protocolConfigSettings(state), Open: open})
}

func configSettingLabels(items []app.ConfigSettingView) string {
	labels := make([]string, 0, len(items))
	for _, item := range items {
		if item.Label != "" {
			labels = append(labels, item.Label)
		} else {
			labels = append(labels, item.ID)
		}
	}
	return strings.Join(labels, ", ")
}
