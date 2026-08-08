package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
)

// maxPrematureEndTurnNudges bounds recovery when a provider repeatedly ends
// after announcing an action without issuing its tool call. The limit prevents
// a malformed or uncooperative provider from turning one user request into an
// unbounded model-only loop.
const maxPrematureEndTurnNudges = 2

const prematureEndTurnNudgeText = "<premature_end_turn>\nYour previous reply announced an immediate next action, but returned end_turn without issuing a tool call, so the user's task may still be incomplete. Continue the task now. If the announced action requires a tool, call it through the structured tool-call API before replying. Do not merely announce the same action again. If no action remains, provide the complete final answer.\n</premature_end_turn>"

var prematureActionPrefixes = []string{
	"i'll ",
	"i will ",
	"i am going to ",
	"i'm going to ",
	"let me ",
	"now ",
	"next ",
	"then ",
	"finally ",
	"continue ",
	"continuing ",
	"start ",
	"starting ",
	"retry ",
	"retrying ",
	"rerun ",
	"rerunning ",
	"re-run ",
	"re-running ",
	"check ",
	"checking ",
	"inspect ",
	"inspecting ",
	"verify ",
	"verifying ",
	"run ",
	"running ",
	"execute ",
	"executing ",
	"update ",
	"updating ",
	"edit ",
	"editing ",
	"write ",
	"writing ",
	"fix ",
	"fixing ",
	"我来",
	"我先",
	"我现在",
	"我会",
	"我要",
	"我用",
	"先",
	"现在",
	"接下来",
	"下一步",
	"然后",
	"再",
	"最后是",
	"最后一步",
	"立即",
	"马上",
	"随后",
	"继续",
	"开始",
	"重新",
	"换",
	"补上",
	"看下",
	"看看",
	"查看",
	"检查",
	"验证",
	"确认",
	"执行",
	"运行",
	"跑",
}

// shouldRecoverPrematureEndTurn catches an Agent-mode reply that stops at an
// action lead-in without issuing a tool call. A trailing colon alone triggers
// recovery: a colon-terminated final answer invites continuation, and the
// nudge's escape hatch plus the 2-nudge cap bound a false positive to one extra
// model call. Non-colon text falls back to the action-prefix scan, which also
// matches inflected -ing forms (fixing, running, updating), so the known
// "."-terminated instance and gerund lead-ins are still caught. Outer gates
// stay: Agent mode, tools available, SuppressTools off, end_turn, no tool calls.
func shouldRecoverPrematureEndTurn(msg core.Message, mode session.Mode, opts RunOptions, toolsAvailable bool) bool {
	if mode != session.ModeAgent || opts.SuppressTools || !toolsAvailable {
		return false
	}
	if msg.FinishReason != core.FinishReasonEndTurn || len(msg.ToolCalls) > 0 {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	if strings.HasSuffix(text, ":") || strings.HasSuffix(text, "：") {
		return true
	}

	clause := trailingActionClause(text)
	clause = strings.ToLower(strings.TrimSpace(clause))
	for _, prefix := range prematureActionPrefixes {
		if strings.HasPrefix(clause, prefix) {
			return true
		}
	}
	return false
}

func trailingActionClause(text string) string {
	const boundaries = "\n。！？!?；;，,"
	boundary := -1
	boundarySize := 0
	if i := strings.LastIndexAny(text, boundaries); i >= 0 {
		_, boundarySize = utf8.DecodeRuneInString(text[i:])
		boundary = i
	}
	if i := strings.LastIndex(text, ". "); i > boundary {
		boundary = i
		boundarySize = len(". ")
	}
	if boundary >= 0 {
		text = text[boundary+boundarySize:]
	}
	return strings.TrimLeft(strings.TrimSpace(text), "#*-0123456789.、)）]】> `\t")
}

func (a *Agent) persistPrematureEndTurnNudge(ctx context.Context, sessionID string) (core.Message, error) {
	return a.store.Create(ctx, core.Message{
		SessionID:    sessionID,
		Role:         core.RoleUser,
		Text:         prematureEndTurnNudgeText,
		Hidden:       true,
		FinishReason: core.FinishReasonEndTurn,
	})
}
