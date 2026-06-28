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

func (a *App) SessionID() string                          { return a.sessionID }
func (a *App) SessionsDir() string                        { return a.sessionsDir }
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
	a.resetAgent()
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
	if a.mcpManager == nil {
		return nil
	}
	return a.mcpManager.Close()
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
