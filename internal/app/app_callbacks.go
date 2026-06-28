package app

import (
	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy"
)

func defaultApprovalFunc(fn policy.ApprovalFunc) policy.ApprovalFunc {
	if fn != nil {
		return fn
	}
	return func(policy.ApprovalRequest) policy.ApprovalDecision { return policy.ApprovalDeny }
}

func defaultUserInputFunc(fn agent.UserInputFunc) agent.UserInputFunc {
	if fn != nil {
		return fn
	}
	return func(agent.UserInputRequest) (core.UserInputResponse, bool) {
		return core.UserInputResponse{}, false
	}
}

func (a *App) SetApprovalFunc(fn policy.ApprovalFunc) {
	if fn == nil {
		return
	}
	a.approvalFn = fn
	if a.toolset != nil {
		a.toolset.SetExecBoundaryApproval(func() string { return a.sessionID }, func(req policy.ApprovalRequest) policy.ApprovalDecision {
			a.approvalMu.Lock()
			defer a.approvalMu.Unlock()
			if a.autoAcceptPermissions {
				return policy.ApprovalAllow
			}
			return a.approvalFn(req)
		})
	}
	a.resetAgent()
}

func (a *App) SetUserInputFunc(fn agent.UserInputFunc) {
	if fn == nil {
		return
	}
	a.userInput = fn
	a.resetAgent()
}
