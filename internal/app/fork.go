package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
)

type forkSessionResult struct {
	Message  string
	Local    *LocalResult
	SourceID string
	NextID   string
	Title    string
}

func (a *App) forkCurrentSession(name string) (forkSessionResult, error) {
	if a == nil || a.msgStore == nil {
		return forkSessionResult{}, fmt.Errorf("session store unavailable")
	}
	sourceID := strings.TrimSpace(a.sessionID)
	if sourceID == "" {
		return forkSessionResult{}, fmt.Errorf("no active session to fork")
	}
	msgs, err := a.msgStore.List(a.ctx, sourceID)
	if err != nil {
		return forkSessionResult{}, err
	}
	if len(msgs) == 0 {
		return forkSessionResult{}, fmt.Errorf("no conversation to fork")
	}

	nextID := newSessionID(time.Now())
	copied := make([]core.Message, len(msgs))
	for i, msg := range msgs {
		msg.SessionID = nextID
		copied[i] = msg
	}
	if err := a.msgStore.RewriteSession(a.ctx, nextID, copied); err != nil {
		return forkSessionResult{}, err
	}

	titleBase := strings.TrimSpace(name)
	if titleBase == "" {
		titleBase = deriveForkTitleBase(msgs)
	}
	title := a.uniqueForkTitle(titleBase)
	branch := a.branch
	if branch == "" {
		branch = session.DetectGitBranch(a.workspaceRoot)
	}
	if err := session.SaveSessionMeta(a.sessionsDir, nextID, session.SessionMeta{
		Workspace:       a.workspaceRoot,
		Branch:          branch,
		Kind:            "fork",
		ParentSessionID: sourceID,
		Title:           title,
	}); err != nil {
		return forkSessionResult{}, err
	}
	mode := a.currentMode
	if mode == "" {
		mode = session.ModeAgent
	}
	if err := session.SaveModeState(a.sessionsDir, nextID, mode); err != nil {
		return forkSessionResult{}, err
	}
	todo, err := session.LoadTodoState(a.sessionsDir, sourceID)
	if err != nil {
		return forkSessionResult{}, err
	}
	if len(todo.Items) > 0 {
		if err := session.SaveTodoState(a.sessionsDir, nextID, todo); err != nil {
			return forkSessionResult{}, err
		}
	}

	a.setSessionID(nextID)
	a.resetAgent()
	resume := resumeCommand(a.workspaceRoot, sourceID)
	msg := fmt.Sprintf("Forked conversation %q. You are now in the fork.\nTo resume the original: %s", title, resume)
	return forkSessionResult{
		Message:  msg,
		Local:    buildForkLocalResult(title, sourceID, nextID, resume, msg),
		SourceID: sourceID,
		NextID:   nextID,
		Title:    title,
	}, nil
}

func deriveForkTitleBase(msgs []core.Message) string {
	for _, msg := range msgs {
		if msg.Role != core.RoleUser || msg.Hidden {
			continue
		}
		if text := singleLineForkTitle(msg.Text); text != "" {
			return truncateForkTitle(text, 100)
		}
	}
	return "Branched conversation"
}

func (a *App) uniqueForkTitle(base string) string {
	base = truncateForkTitle(singleLineForkTitle(base), 100)
	if base == "" {
		base = "Branched conversation"
	}
	candidate := base + " (Branch)"
	used := map[string]bool{}
	if summaries, err := session.ListSessions(a.sessionsDir, 0); err == nil {
		for _, summary := range summaries {
			if title := strings.TrimSpace(summary.Meta.Title); title != "" {
				used[title] = true
			}
		}
	}
	if !used[candidate] {
		return candidate
	}
	for i := 2; ; i++ {
		next := fmt.Sprintf("%s (Branch %d)", base, i)
		if !used[next] {
			return next
		}
	}
}

func singleLineForkTitle(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func truncateForkTitle(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}
