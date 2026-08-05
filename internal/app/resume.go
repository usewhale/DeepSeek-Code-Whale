package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
)

func (a *App) IsResumeMenu(line string) bool { return strings.TrimSpace(line) == "/resume" }

type ResumeApplyResult struct {
	Message string
	Resumed bool
}

type CrossWorkspaceResumeError struct {
	Message string
}

func (e *CrossWorkspaceResumeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func IsCrossWorkspaceResumeError(err error) bool {
	var target *CrossWorkspaceResumeError
	return errors.As(err, &target)
}

func CheckResumeWorkspace(sessionsDir, sessionID, currentWorkspace string) (string, bool, error) {
	meta, err := session.LoadSessionMeta(sessionsDir, sessionID)
	if err != nil {
		return "", false, err
	}
	workspace := strings.TrimSpace(meta.Workspace)
	if workspace == "" || sameWorkspace(workspace, currentWorkspace) {
		return "", false, nil
	}
	return crossWorkspaceResumeMessage(workspace, sessionID), true, nil
}

func ResolveResumeWorktree(cfg Config, start StartOptions, currentWorkspace string) (ResumeWorktreeDecision, error) {
	decision, err := ResolveResumeWorktreeDecision(cfg, start, currentWorkspace)
	if err != nil {
		return ResumeWorktreeDecision{}, err
	}
	if !decision.MissingWorktree {
		return decision, nil
	}
	sessionID := strings.TrimSpace(start.SessionID)
	if sessionID == "" {
		sessionsDir := store.DefaultSessionsDir(cfg.DataDir)
		var err error
		sessionID, err = resolveInitialSessionID(sessionsDir)
		if err != nil {
			return ResumeWorktreeDecision{}, fmt.Errorf("resolve session failed: %w", err)
		}
	}
	if err := CommitMissingWorktreeCleanup(store.DefaultSessionsDir(cfg.DataDir), sessionID, currentWorkspace); err != nil {
		return ResumeWorktreeDecision{}, err
	}
	return ResumeWorktreeDecision{}, nil
}

type ResumeWorktreeDecision struct {
	Session         WorktreeSession
	MissingWorktree bool
	TargetWorkspace string
}

func ResolveResumeWorktreeDecision(cfg Config, start StartOptions, currentWorkspace string) (ResumeWorktreeDecision, error) {
	if start.ResumeMenu {
		return ResumeWorktreeDecision{}, nil
	}
	sessionID := strings.TrimSpace(start.SessionID)
	if sessionID == "" {
		sessionsDir := store.DefaultSessionsDir(cfg.DataDir)
		var err error
		sessionID, err = resolveInitialSessionID(sessionsDir)
		if err != nil {
			return ResumeWorktreeDecision{}, fmt.Errorf("resolve session failed: %w", err)
		}
	}
	sessionsDir := store.DefaultSessionsDir(cfg.DataDir)
	meta, err := session.LoadSessionMeta(sessionsDir, sessionID)
	if err != nil {
		return ResumeWorktreeDecision{}, err
	}
	if strings.TrimSpace(meta.WorktreePath) == "" {
		return ResumeWorktreeDecision{}, nil
	}
	path := strings.TrimSpace(meta.WorktreePath)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ResumeWorktreeDecision{MissingWorktree: true}, nil
		}
		return ResumeWorktreeDecision{}, fmt.Errorf("stat worktree: %w", err)
	}
	decision := ResumeWorktreeDecision{Session: WorktreeSession{
		Name:               meta.WorktreeName,
		Workspace:          meta.Workspace,
		Path:               path,
		Branch:             meta.WorktreeBranch,
		OriginalWorkspace:  meta.OriginalWorkspace,
		OriginalBranch:     meta.OriginalBranch,
		OriginalHeadCommit: meta.OriginalHeadCommit,
	}}
	decision.TargetWorkspace = resumeTargetWorkspace(decision.Session)
	return decision, nil
}

func CommitMissingWorktreeCleanup(sessionsDir, sessionID, currentWorkspace string) error {
	meta, err := session.LoadSessionMeta(sessionsDir, sessionID)
	if err != nil {
		return err
	}
	workspace := core.FirstNonEmpty(strings.TrimSpace(meta.OriginalWorkspace), currentWorkspace)
	branch := strings.TrimSpace(meta.OriginalBranch)
	if _, err := session.UpdateSessionMeta(sessionsDir, sessionID, func(meta *session.SessionMeta) {
		if workspace != "" {
			meta.Workspace = workspace
		}
		if branch != "" {
			meta.Branch = branch
		}
		clearSessionMetaWorktree(meta)
	}); err != nil {
		return fmt.Errorf("record missing worktree exit: %w", err)
	}
	return nil
}

func crossWorkspaceResumeMessage(workspace, sessionID string) string {
	return strings.Join([]string{
		"This conversation is from a different directory.",
		"",
		"To resume, run:",
		"  " + resumeCommand(workspace, sessionID),
	}, "\n")
}

func resumeCommand(workspace, sessionID string) string {
	return resumeCommandFor(runtime.GOOS, workspace, sessionID, resumeExecutable())
}

func resumeCommandFor(goos, workspace, sessionID, bin string) string {
	if goos == "windows" {
		return cmdResumeCommand(workspace, sessionID, bin)
	}
	sessionArg := resumeSessionArg(sessionID)
	return fmt.Sprintf("cd %s && %s resume %s", shQuote(workspace), shQuote(bin), sessionArg)
}

func resumeExecutable() string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "whale"
	}
	return exe
}

func sameWorkspace(a, b string) bool {
	a = normalizedWorkspacePath(a)
	b = normalizedWorkspacePath(b)
	if a == "" || b == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func normalizedWorkspacePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}

func shQuote(v string) string {
	if v == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
}

func cmdResumeCommand(workspace, sessionID, bin string) string {
	return fmt.Sprintf(
		`cmd /v:on /c "set whale_resume_workspace=%s&&set whale_resume_bin=%s&&set whale_resume_session=%s&&cd /d "!whale_resume_workspace!"&&"!whale_resume_bin!" resume "!whale_resume_session!""`,
		cmdSetValue(workspace),
		cmdSetValue(bin),
		cmdSetValue(sessionID),
	)
}

func cmdSetValue(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range v {
		switch r {
		case '^':
			b.WriteString("^^")
		case '!':
			b.WriteString("^^!")
		case '%', '&', '|', '<', '>', '(', ')':
			b.WriteByte('^')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func resumeSessionArg(sessionID string) string {
	if isBareResumeSessionID(sessionID) {
		return sessionID
	}
	return shQuote(sessionID)
}

func isBareResumeSessionID(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func (a *App) ListResumeChoices(limit int) ([]string, error) {
	filtered, err := a.listWorkspaceResumeSummaries(limit)
	if err != nil {
		return nil, err
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(filtered)+1)
	out = append(out, "recent sessions:")
	out = append(out, "   #   Updated   Branch                    Conversation")
	for i, s := range filtered {
		marker := " "
		if s.ID == a.sessionID {
			marker = "*"
		}
		branch := strings.TrimSpace(s.Meta.Branch)
		if branch == "" {
			branch = "-"
		}
		out = append(out, fmt.Sprintf("%s %2d) %-9s %-24s %s", marker, i+1, humanAgo(s.ModTime), truncateRunes(branch, 24), truncateRunes(s.Conversation, 80)))
	}
	return out, nil
}

func (a *App) listWorkspaceResumeSummaries(limit int) ([]session.SessionSummary, error) {
	summaries, err := session.ListSessions(a.sessionsDir, 0)
	if err != nil {
		return nil, err
	}
	// Keep sessions with missing workspace metadata for backward compatibility;
	// exclude only sessions explicitly recorded for a different workspace.
	filtered := make([]session.SessionSummary, 0, len(summaries))
	for _, s := range summaries {
		ws := strings.TrimSpace(s.Meta.Workspace)
		if ws == "" || sameWorkspace(ws, a.workspaceRoot) {
			filtered = append(filtered, s)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func humanAgo(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	d := time.Since(ts)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

func (a *App) ApplyResumeChoice(choice string) (ResumeApplyResult, error) {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return ResumeApplyResult{Message: "resume canceled"}, nil
	}
	summaries, err := a.listWorkspaceResumeSummaries(20)
	if err != nil {
		return ResumeApplyResult{}, err
	}
	next := ""
	if idx, err := strconv.Atoi(choice); err == nil {
		if idx < 1 || idx > len(summaries) {
			return ResumeApplyResult{}, errors.New("invalid selection")
		}
		next = summaries[idx-1].ID
	} else {
		next = choice
	}
	if msg, blocked, err := CheckResumeWorkspace(a.sessionsDir, next, a.workspaceRoot); err != nil {
		return ResumeApplyResult{}, err
	} else if blocked {
		return ResumeApplyResult{Message: msg}, nil
	}
	a.sessionID = next
	modeState, err := session.LoadModeState(a.sessionsDir, a.sessionID)
	if err != nil {
		return ResumeApplyResult{}, err
	}
	a.currentMode = modeState.Mode
	out := fmt.Sprintf("resumed session: %s\nmode: %s", a.sessionID, a.currentMode)
	if ust, err := session.LoadUserInputState(a.sessionsDir, a.sessionID); err == nil && ust.Pending {
		out += fmt.Sprintf("\npending user input: tool_call=%s questions=%d", ust.ToolCallID, len(ust.Questions))
	}
	return ResumeApplyResult{Message: out, Resumed: true}, nil
}
