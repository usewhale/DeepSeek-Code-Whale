package service

import (
	"github.com/usewhale/whale/internal/app"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/runtime/protocol"
)

func (s *Service) DispatchProtocol(in protocol.Intent) {
	s.Dispatch(Intent{
		Kind:               serviceIntentKind(in.Kind),
		InterruptSource:    in.InterruptSource,
		Input:              in.Input,
		ClientInputID:      in.ClientInputID,
		HiddenInput:        in.HiddenInput,
		Attachments:        serviceAttachmentInputs(in.Attachments),
		ToolCallID:         in.ToolCallID,
		UserInput:          coreUserInputResponse(in.UserInput),
		SessionInput:       in.SessionInput,
		MessageID:          in.MessageID,
		Model:              in.Model,
		Effort:             in.Effort,
		Thinking:           in.Thinking,
		ApprovalMode:       in.ApprovalMode,
		ViewMode:           in.ViewMode,
		SkillName:          in.SkillName,
		SkillEnabled:       in.SkillEnabled,
		PluginID:           in.PluginID,
		PluginEnabled:      in.PluginEnabled,
		HookKey:            in.HookKey,
		HookEnabled:        in.HookEnabled,
		HooksReviewAction:  in.HooksReviewAction,
		ConfigUpdates:      in.ConfigUpdates,
		SkillBinding:       appSkillBinding(in.SkillBinding),
		WorktreeAction:     in.WorktreeAction,
		WorkflowRunID:      in.WorkflowRunID,
		WorkflowName:       in.WorkflowName,
		WorkflowArgs:       in.WorkflowArgs,
		WorkflowResume:     in.WorkflowResume,
		WorkflowTrust:      in.WorkflowTrust,
		WorkflowScript:     in.WorkflowScript,
		WorkflowSaveAs:     in.WorkflowSaveAs,
		WorkflowScriptPath: in.WorkflowScriptPath,
		AutoReview:         in.AutoReview,
	})
}

func serviceIntentKind(kind protocol.IntentKind) IntentKind {
	switch kind {
	case protocol.IntentSubmit:
		return IntentSubmit
	case protocol.IntentSubmitLocal:
		return IntentSubmitLocal
	case protocol.IntentAllowTool:
		return IntentAllowTool
	case protocol.IntentAllowToolForSession:
		return IntentAllowToolForSession
	case protocol.IntentDenyTool:
		return IntentDenyTool
	case protocol.IntentCancelToolApproval:
		return IntentCancelToolApproval
	case protocol.IntentSubmitUserInput:
		return IntentSubmitUserInput
	case protocol.IntentCancelUserInput:
		return IntentCancelUserInput
	case protocol.IntentSelectSession:
		return IntentSelectSession
	case protocol.IntentRequestSessions:
		return IntentRequestSessions
	case protocol.IntentRequestExit:
		return IntentRequestExit
	case protocol.IntentShutdown:
		return IntentShutdown
	case protocol.IntentSetModelAndEffort:
		return IntentSetModelAndEffort
	case protocol.IntentSetApprovalMode:
		return IntentSetApprovalMode
	case protocol.IntentEnableAutoAccept:
		return IntentEnableAutoAccept
	case protocol.IntentSetAutoReview:
		return IntentSetAutoReview
	case protocol.IntentSetViewMode:
		return IntentSetViewMode
	case protocol.IntentToggleMode:
		return IntentToggleMode
	case protocol.IntentImplementPlan:
		return IntentImplementPlan
	case protocol.IntentDeclinePlan:
		return IntentDeclinePlan
	case protocol.IntentRequestSkillsManage:
		return IntentRequestSkillsManage
	case protocol.IntentSetSkillEnabled:
		return IntentSetSkillEnabled
	case protocol.IntentSetPluginEnabled:
		return IntentSetPluginEnabled
	case protocol.IntentRequestHooksManage:
		return IntentRequestHooksManage
	case protocol.IntentRequestConfigManage:
		return IntentRequestConfigManage
	case protocol.IntentApplyConfigSettings:
		return IntentApplyConfigSettings
	case protocol.IntentSetHookEnabled:
		return IntentSetHookEnabled
	case protocol.IntentTrustHook:
		return IntentTrustHook
	case protocol.IntentTrustHooks:
		return IntentTrustHooks
	case protocol.IntentResolveHooksStartupReview:
		return IntentResolveHooksStartupReview
	case protocol.IntentWorktreeExitChoice:
		return IntentWorktreeExitChoice
	case protocol.IntentRequestWorkflowPanel:
		return IntentRequestWorkflowPanel
	case protocol.IntentCancelWorkflowRun:
		return IntentCancelWorkflowRun
	case protocol.IntentStartWorkflow:
		return IntentStartWorkflow
	default:
		return IntentKind(kind)
	}
}

func serviceAttachmentInputs(in []protocol.AttachmentInput) []AttachmentInput {
	if len(in) == 0 {
		return nil
	}
	out := make([]AttachmentInput, 0, len(in))
	for _, item := range in {
		out = append(out, AttachmentInput{Path: item.Path, DisplayName: item.DisplayName})
	}
	return out
}

func coreUserInputResponse(resp *protocol.UserInputResponse) *core.UserInputResponse {
	if resp == nil {
		return nil
	}
	out := &core.UserInputResponse{Answers: make([]core.UserInputAnswer, 0, len(resp.Answers))}
	for _, answer := range resp.Answers {
		out.Answers = append(out.Answers, core.UserInputAnswer{
			ID:      answer.ID,
			Label:   answer.Label,
			Value:   answer.Value,
			IsOther: answer.IsOther,
		})
	}
	return out
}

func appSkillBinding(binding *protocol.SkillBinding) *app.SkillBinding {
	if binding == nil {
		return nil
	}
	return &app.SkillBinding{Name: binding.Name, SkillFilePath: binding.SkillFilePath}
}
