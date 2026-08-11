package app

import (
	"errors"
	"fmt"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/defaults"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
	"strings"
)

func (a *App) SessionID() string {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	return a.sessionID
}
func (a *App) SessionsDir() string { return a.sessionsDir }

// setSessionID is the only mutating path to sessionID. All writers (resume,
// session-new, fork) and the async MCP restore readers serialize on sessionMu.
func (a *App) setSessionID(id string) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.sessionID = id
}

// sessionPath returns a consistent snapshot of the session location for paths
// built by goroutines outside the dispatch loop (MCP startup restore).
func (a *App) sessionPath() (sessionsDir, sessionID string) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	return a.sessionsDir, a.sessionID
}
func (a *App) CurrentMode() session.Mode                  { return a.currentMode }
func (a *App) PermissionDefault() policy.PermissionAction { return a.permissionPolicy.Default }
func (a *App) AutoAcceptPermissions() bool {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	return a.autoAcceptPermissions
}
func (a *App) SetMode(mode session.Mode) (string, error) {
	if _, err := session.ParseMode(string(mode)); err != nil {
		return "", err
	}
	previous := a.currentMode
	if err := session.SaveModeState(a.sessionsDir, a.sessionID, mode); err != nil {
		return "", err
	}
	a.currentMode = mode
	a.resetAgent()
	if previous != "" && previous != mode {
		a.RecordModeChanged(string(previous), string(mode))
	}
	return fmt.Sprintf("%s mode enabled", modeTitle(mode)), nil
}
func (a *App) ToggleMode() (string, error) {
	switch a.currentMode {
	case session.ModeAgent:
		return a.SetMode(session.ModeAsk)
	case session.ModeAsk:
		return a.SetMode(session.ModePlan)
	default:
		return a.SetMode(session.ModeAgent)
	}
}
func (a *App) SetAutoAcceptPermissions(enabled bool) {
	// The approval callback reads autoAcceptPermissions under approvalMu while
	// a turn runs, so the write must take the same lock.
	a.approvalMu.Lock()
	a.autoAcceptPermissions = enabled
	a.approvalMu.Unlock()
	a.cfg.AutoAcceptPermissions = enabled
	// Any explicit auto-accept change (including enabling) also disables
	// auto-review — the permissions menu is mutually exclusive. SetAutoReviewEnabled
	// re-enables auto-review AFTER this call so ordering is safe.
	a.cfg.AutoReviewEnabled = false
	a.resetAgent()
}

func (a *App) AutoReviewEnabled() bool {
	if a == nil {
		return false
	}
	return a.cfg.AutoReviewEnabled
}

func (a *App) SetAutoReviewEnabled(enabled bool) {
	if enabled {
		// Auto-review subsumes auto-accept. Set auto-accept first (this also
		// resets AutoReviewEnabled=false), then override back to true.
		a.SetAutoAcceptPermissions(true)
	}
	a.cfg.AutoReviewEnabled = enabled
	if a.a != nil && a.a.Classifier() != nil {
		a.a.Classifier().SetEnabled(enabled)
	}
	if !enabled {
		a.resetAgent()
	}
}
func (a *App) WorkspaceRoot() string   { return a.workspaceRoot }
func (a *App) Model() string           { return a.model }
func (a *App) ReasoningEffort() string { return a.reasoningEffort }
func (a *App) ThinkingEnabled() bool   { return a.thinkingEnabled }
func (a *App) ShowReasoning() bool {
	if a == nil {
		return false
	}
	return a.cfg.ShowReasoning
}
func (a *App) ViewMode() string {
	if a == nil {
		return ViewModeDefault
	}
	mode, err := NormalizeViewMode(a.cfg.ViewMode)
	if err != nil {
		return ViewModeDefault
	}
	return mode
}
func (a *App) ListMessages() ([]core.Message, error) {
	return a.msgStore.List(a.ctx, a.sessionID)
}
func (a *App) SupportedModels() []string { return defaults.SupportedModels() }
func (a *App) SupportedEfforts() []string {
	return SupportedReasoningEfforts()
}

func (a *App) SetModelAndEffort(modelName, effort string) error {
	m := strings.TrimSpace(strings.ToLower(modelName))
	e := normalizeEffort(effort)
	if m == "" || e == "" {
		return errors.New("model and effort are required")
	}
	if !containsString(a.SupportedModels(), m) {
		return fmt.Errorf("unsupported model: %s", modelName)
	}
	if !containsString(a.SupportedEfforts(), e) {
		return fmt.Errorf("unsupported effort: %s", effort)
	}
	a.model = m
	a.reasoningEffort = e
	a.resetAgent()
	a.savePreferences()
	return nil
}

func (a *App) SetThinkingEnabled(enabled bool) {
	a.thinkingEnabled = enabled
	a.resetAgent()
	a.savePreferences()
}

func (a *App) SetViewMode(mode string) error {
	mode, err := NormalizeViewMode(mode)
	if err != nil {
		return err
	}
	a.cfg.ViewMode = mode
	return SaveGlobalViewMode(a.cfg.DataDir, mode)
}

func (a *App) ToggleViewMode() (string, error) {
	next := ViewModeFocus
	if a.ViewMode() == ViewModeFocus {
		next = ViewModeDefault
	}
	if err := a.SetViewMode(next); err != nil {
		return "", err
	}
	return next, nil
}

func ViewModeToggleMessage(mode string) string {
	if strings.TrimSpace(mode) == ViewModeFocus {
		return "Focus view enabled"
	}
	return "Focus view disabled"
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	a.resetAgent()
	var errs []string
	if a.lspManager != nil {
		if err := a.lspManager.Close(); err != nil {
			errs = append(errs, "lsp: "+err.Error())
		}
	}
	if a.mcpManager != nil {
		if err := a.mcpManager.Close(); err != nil {
			errs = append(errs, "mcp: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (a *App) resetAgent() {
	if a == nil || a.a == nil {
		return
	}
	a.a.Close()
	a.a = nil
}

func (a *App) savePreferences() {
	enabled := a.thinkingEnabled
	_ = SaveGlobalPreferences(a.cfg.DataDir, a.model, a.reasoningEffort, enabled)
}
