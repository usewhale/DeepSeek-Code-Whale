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
	"start ",
	"retry ",
	"rerun ",
	"re-run ",
	"check ",
	"inspect ",
	"verify ",
	"run ",
	"execute ",
	"update ",
	"edit ",
	"write ",
	"fix ",
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

// shouldRecoverPrematureEndTurn recognizes the narrow failure shape observed
// in DeepSeek sessions: an Agent-mode reply stops at an action lead-in ending
// in a colon, yet contains no structured tool call. Requiring both the dangling
// colon and an immediate-action prefix avoids retrying ordinary final answers,
// questions, headings, Plan replies, and tool-suppressed internal requests.
func shouldRecoverPrematureEndTurn(msg core.Message, mode session.Mode, opts RunOptions, toolsAvailable bool) bool {
	if mode != session.ModeAgent || opts.SuppressTools || !toolsAvailable {
		return false
	}
	if msg.FinishReason != core.FinishReasonEndTurn || len(msg.ToolCalls) > 0 {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	if !strings.HasSuffix(text, ":") && !strings.HasSuffix(text, "：") {
		return false
	}

	clause := trailingActionClause(strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(text, ":"), "：")))
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
