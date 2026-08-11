package app

import (
	"fmt"
	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/lsp"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/plugins"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
	"github.com/usewhale/whale/internal/tools"
	"github.com/usewhale/whale/internal/workflow"
	"strings"
	"time"
)

type appSessionInit struct {
	sessionsDir string
	msgStore    *store.JSONLStore
	sessionID   string
	branch      string
	mode        session.Mode
}

type appToolInit struct {
	toolset              *tools.Toolset
	mcpManager           *whalemcp.Manager
	lspManager           *lsp.Manager
	pluginManager        *plugins.Manager
	pluginTools          []core.Tool
	pluginAgents         []plugins.AgentDefinition
	baseTools            []core.Tool
	baseToolRegistry     *core.ToolRegistry
	subagentToolRegistry *core.ToolRegistry
	hooks                []agent.ResolvedHook
	hookStates           agent.HookStates
	hookRunner           *agent.HookRunner
	hookSources          []string
}

type appRuntimeInit struct {
	cfg             Config
	model           string
	effort          string
	thinking        bool
	contextWindow   int
	apiKey          string
	taskTools       []core.Tool
	goalTools       []core.Tool
	workflowTools   []core.Tool
	workflowManager *workflow.RunManager
	workflowRunner  *workflow.ScriptRunner
	toolRegistry    *core.ToolRegistry
}

func loadNewConfig(cfg Config, workspaceRoot string) (Config, error) {
	if cfg.ConfigLoaded {
		return cfg, nil
	}
	return LoadAndApplyConfig(cfg, workspaceRoot)
}

func initAppSession(cfg Config, start StartOptions, workspaceRoot string) (appSessionInit, error) {
	sessionsDir := store.DefaultSessionsDir(cfg.DataDir)
	msgStore, err := store.NewJSONLStore(sessionsDir)
	if err != nil {
		return appSessionInit{}, fmt.Errorf("init session store failed: %w", err)
	}
	sessionID, err := initialAppSessionID(sessionsDir, start)
	if err != nil {
		return appSessionInit{}, err
	}
	if !start.NewSession && !start.ResumeMenu {
		if msg, blocked, err := CheckResumeWorkspace(sessionsDir, sessionID, workspaceRoot); err != nil {
			return appSessionInit{}, err
		} else if blocked {
			return appSessionInit{}, &CrossWorkspaceResumeError{Message: msg}
		}
	}
	return appSessionInit{
		sessionsDir: sessionsDir,
		msgStore:    msgStore,
		sessionID:   sessionID,
	}, nil
}

func initialAppSessionID(sessionsDir string, start StartOptions) (string, error) {
	if sid := strings.TrimSpace(start.SessionID); sid != "" {
		if _, err := session.ResolveStrictSession(sessionsDir, sid); err != nil {
			return "", err
		}
		return sid, nil
	}
	if start.NewSession || start.ResumeMenu {
		return newSessionID(time.Now()), nil
	}
	sessionID, err := resolveInitialSessionID(sessionsDir)
	if err != nil {
		return "", fmt.Errorf("resolve session failed: %w", err)
	}
	return sessionID, nil
}

func completeAppSessionState(init appSessionInit, start StartOptions, workspaceRoot string) (appSessionInit, error) {
	mode, err := initialAppMode(init.sessionsDir, init.sessionID, start)
	if err != nil {
		return appSessionInit{}, err
	}
	branch := session.DetectGitBranch(workspaceRoot)
	if err := patchNewSessionMeta(init.sessionsDir, init.sessionID, workspaceRoot, branch, start); err != nil {
		return appSessionInit{}, err
	}
	init.mode = mode
	init.branch = branch
	return init, nil
}

func initialAppMode(sessionsDir, sessionID string, start StartOptions) (session.Mode, error) {
	modeState, err := session.LoadModeState(sessionsDir, sessionID)
	if err != nil {
		return "", fmt.Errorf("load session mode failed: %w", err)
	}
	if raw := strings.TrimSpace(start.ModeOverride); raw != "" {
		mode, err := session.ParseMode(raw)
		if err != nil {
			return "", err
		}
		return mode, nil
	}
	return modeState.Mode, nil
}

func patchNewSessionMeta(sessionsDir, sessionID, workspaceRoot, branch string, start StartOptions) error {
	if !start.NewSession && !start.ResumeMenu {
		return nil
	}
	meta := session.SessionMeta{Workspace: workspaceRoot, Branch: branch}
	if strings.TrimSpace(start.Worktree.Name) != "" {
		meta.WorktreeName = start.Worktree.Name
		meta.WorktreePath = start.Worktree.Path
		meta.WorktreeBranch = start.Worktree.Branch
		meta.OriginalWorkspace = start.Worktree.OriginalWorkspace
		meta.OriginalBranch = start.Worktree.OriginalBranch
		meta.OriginalHeadCommit = start.Worktree.OriginalHeadCommit
	}
	if _, err := session.PatchSessionMeta(sessionsDir, sessionID, session.SessionMetaPatchFromMeta(meta)); err != nil {
		return fmt.Errorf("patch session meta failed: %w", err)
	}
	return nil
}
