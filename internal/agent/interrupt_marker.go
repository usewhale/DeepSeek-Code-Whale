package agent

import (
	"context"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

const interruptedTurnMarkerText = "<turn_aborted>\nThe user interrupted the previous turn on purpose. Any running tools or commands may have partially executed; verify current state before retrying.\n</turn_aborted>"

func interruptedTurnMarkerTextForContext(ctx context.Context) string {
	source := strings.TrimSpace(UserInterruptSource(context.Cause(ctx)))
	if source == "" {
		return interruptedTurnMarkerText
	}
	return "<turn_aborted>\nsource: " + source + "\nThe user interrupted the previous turn on purpose. Any running tools or commands may have partially executed; verify current state before retrying.\n</turn_aborted>"
}

func approvalDeniedMarkerText(toolName string) string {
	if toolName == "" {
		toolName = "unknown"
	}
	return "<approval_denied>\nThe user denied a requested tool/action (tool: " + toolName + "). Treat the related task path as canceled. Do not retry, continue, or switch to another tool to bypass the denied action unless the user explicitly asks. If the user asks again, use the normal approval flow for the same capability instead of probing alternative tools that are known to be out of scope.\n</approval_denied>"
}

func (a *Agent) persistInterruptedTurnMarker(ctx context.Context, sessionID string) {
	_, _ = a.store.Create(context.Background(), core.Message{
		SessionID:    sessionID,
		Role:         core.RoleUser,
		Text:         interruptedTurnMarkerTextForContext(ctx),
		Hidden:       true,
		FinishReason: core.FinishReasonCanceled,
	})
}

func (a *Agent) persistApprovalDeniedMarker(sessionID, toolName string) {
	_, _ = a.store.Create(context.Background(), core.Message{
		SessionID:    sessionID,
		Role:         core.RoleUser,
		Text:         approvalDeniedMarkerText(toolName),
		Hidden:       true,
		FinishReason: core.FinishReasonCanceled,
	})
}
