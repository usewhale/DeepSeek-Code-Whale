package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
)

type ResumeRejectedError struct {
	Reason string
}

func (e *ResumeRejectedError) Error() string {
	if e == nil {
		return ""
	}
	return e.Reason
}

func IsResumeRejectedError(err error) bool {
	var target *ResumeRejectedError
	return errors.As(err, &target)
}

type InvalidModeError struct {
	Value string
}

func (e *InvalidModeError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("invalid mode: %q (supported: agent, ask, plan)", e.Value)
}

func IsInvalidModeError(err error) bool {
	var target *InvalidModeError
	return errors.As(err, &target)
}

func ValidateResumeTarget(cfg Config, start StartOptions, currentWorkspace string) (ResumeWorktreeDecision, error) {
	if start.NewSession || strings.TrimSpace(start.SessionID) == "" {
		return ResumeWorktreeDecision{}, nil
	}
	sessionsDir := store.DefaultSessionsDir(cfg.DataDir)
	if _, err := session.ResolveStrictSession(sessionsDir, start.SessionID); err != nil {
		return ResumeWorktreeDecision{}, &ResumeRejectedError{Reason: err.Error()}
	}
	decision, err := ResolveResumeWorktreeDecision(cfg, start, currentWorkspace)
	if err != nil {
		return ResumeWorktreeDecision{}, err
	}
	explicit := strings.TrimSpace(start.Worktree.Path)
	switch {
	case decision.Session.Path != "":
		if explicit != "" && explicit != decision.Session.Path {
			return ResumeWorktreeDecision{}, &ResumeRejectedError{Reason: fmt.Sprintf("explicit worktree %q does not match session record %q", explicit, decision.Session.Path)}
		}
		if err := checkResumeWorkspaceAt(sessionsDir, start.SessionID, decision.TargetWorkspace); err != nil {
			return ResumeWorktreeDecision{}, err
		}
		return decision, nil
	case decision.MissingWorktree:
		if explicit != "" {
			return ResumeWorktreeDecision{}, &ResumeRejectedError{Reason: fmt.Sprintf("session worktree is gone; explicit worktree %q cannot take over session ownership", explicit)}
		}
		if msg, blocked, err := checkMissingWorktreeResumeGate(sessionsDir, start.SessionID, currentWorkspace); err != nil {
			return ResumeWorktreeDecision{}, err
		} else if blocked {
			return ResumeWorktreeDecision{}, &CrossWorkspaceResumeError{Message: msg}
		}
		return decision, nil
	default:
		if explicit != "" {
			return ResumeWorktreeDecision{}, &ResumeRejectedError{Reason: fmt.Sprintf("session %q has no worktree record; explicit worktree %q rejected", start.SessionID, explicit)}
		}
		if err := checkResumeWorkspaceAt(sessionsDir, start.SessionID, currentWorkspace); err != nil {
			return ResumeWorktreeDecision{}, err
		}
		return decision, nil
	}
}

func resumeTargetWorkspace(s WorktreeSession) string {
	ws := strings.TrimSpace(s.Workspace)
	if ws == "" {
		return s.Path
	}
	if inside, err := core.PathInside(ws, s.Path); err == nil && inside {
		return ws
	}
	return s.Path
}

func checkResumeWorkspaceAt(sessionsDir, sessionID, workspace string) error {
	if msg, blocked, err := CheckResumeWorkspace(sessionsDir, sessionID, workspace); err != nil {
		return err
	} else if blocked {
		return &CrossWorkspaceResumeError{Message: msg}
	}
	return nil
}

// checkMissingWorktreeResumeGate rejects resuming a session whose recorded
// worktree is gone when CommitMissingWorktreeCleanup would rebind the session
// to a workspace different from the caller's directory. Without this gate the
// read-only validation would report success, the commit phase would rewrite
// session metadata, and only then app.New would reject the run as
// cross-workspace — mutating state despite the rejected invocation.
func checkMissingWorktreeResumeGate(sessionsDir, sessionID, currentWorkspace string) (string, bool, error) {
	meta, err := session.LoadSessionMeta(sessionsDir, sessionID)
	if err != nil {
		return "", false, err
	}
	effective := core.FirstNonEmpty(strings.TrimSpace(meta.OriginalWorkspace), currentWorkspace)
	if sameWorkspace(effective, currentWorkspace) {
		return "", false, nil
	}
	return crossWorkspaceResumeMessage(effective, sessionID), true, nil
}

func CommitStartState(cfg Config, start StartOptions, currentWorkspace string, decision ResumeWorktreeDecision) error {
	if start.NewSession || strings.TrimSpace(start.SessionID) == "" {
		return nil
	}
	sessionID := strings.TrimSpace(start.SessionID)
	if decision.MissingWorktree {
		if err := CommitMissingWorktreeCleanup(store.DefaultSessionsDir(cfg.DataDir), sessionID, currentWorkspace); err != nil {
			return err
		}
	}
	if raw := strings.TrimSpace(start.ModeOverride); raw != "" {
		mode, err := session.ParseMode(raw)
		if err != nil {
			return &InvalidModeError{Value: raw}
		}
		if err := session.SaveModeState(store.DefaultSessionsDir(cfg.DataDir), sessionID, mode); err != nil {
			return fmt.Errorf("save mode state failed: %w", err)
		}
	}
	return nil
}
