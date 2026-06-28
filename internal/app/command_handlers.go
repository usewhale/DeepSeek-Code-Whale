package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/plugins"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/workflow"
)

type CommandExecution struct {
	Handled        bool
	Text           string
	LocalResult    *LocalResult
	Turn           *plugins.CommandTurn
	ShouldExit     bool
	ClearScreen    bool
	Mutated        bool
	HydrateSession bool
}

func (a *App) HandleSlash(line string) (handled bool, output string, synthetic string, shouldExit bool, clearScreen bool, err error) {
	res, err := a.ExecuteSlash(line)
	if res.Turn != nil {
		synthetic = res.Turn.Input
	}
	return res.Handled, res.Text, synthetic, res.ShouldExit, res.ClearScreen, err
}

func (a *App) ExecuteSlash(line string) (CommandExecution, error) {
	cmdResult, cmdErr := handleCommand(line, a.sessionID, time.Now())
	if cmdErr != nil {
		return CommandExecution{Handled: true}, cmdErr
	}
	if !cmdResult.Handled {
		return CommandExecution{}, nil
	}
	if cmdResult.ClearScreen {
		return CommandExecution{Handled: true, ClearScreen: true}, nil
	}
	if cmdResult.ShowStatus {
		status := a.buildStatusLocalResult()
		return CommandExecution{Handled: true, Text: status.PlainText, LocalResult: status}, nil
	}
	if cmdResult.InitMemory {
		line, err := a.initMemory()
		if err != nil || line != "" {
			return CommandExecution{Handled: true, Text: line}, err
		}
		return CommandExecution{Handled: true, Text: "Initializing AGENTS.md from repository context...", Turn: &plugins.CommandTurn{
			Input:               buildInitSyntheticPrompt(),
			Hidden:              true,
			SkipUserPromptHooks: true,
			SkipSkillInjection:  true,
		}}, nil
	}
	if cmdResult.ShowSkills {
		return CommandExecution{Handled: true, Text: a.buildSkillsList()}, nil
	}
	if cmdResult.BtwQuestion != "" {
		return CommandExecution{Handled: true}, errors.New("/btw is only available in the interactive TUI")
	}
	if cmdResult.ReviewPrompt != "" {
		return CommandExecution{Handled: true, Turn: &plugins.CommandTurn{
			Input:               cmdResult.ReviewPrompt,
			Hidden:              true,
			ReadOnly:            true,
			SkipUserPromptHooks: true,
			SkipSkillInjection:  true,
			ShellAllowPrefixes:  append([]string(nil), cmdResult.AllowShellPrefixes...),
		}}, nil
	}
	if strings.TrimSpace(cmdResult.ForkName) != "" || trimmedCommandHead(line) == "/fork" {
		res, err := a.forkCurrentSession(cmdResult.ForkName)
		return CommandExecution{Handled: true, Text: res.Message, LocalResult: res.Local}, err
	}
	out := CommandExecution{Handled: true, ShouldExit: cmdResult.ShouldExit}
	if cmdResult.Mode != "" {
		mode, err := session.ParseMode(cmdResult.Mode)
		if err != nil {
			return out, err
		}
		msg, err := a.SetMode(mode)
		if err != nil {
			return out, err
		}
		if cmdResult.Output == "" {
			cmdResult.Output = msg
		}
	}
	if cmdResult.Output != "" {
		out.Text = cmdResult.Output
	}
	if cmdResult.AskPrompt != "" {
		out.Turn = &plugins.CommandTurn{Input: cmdResult.AskPrompt}
	}
	if cmdResult.PlanPrompt != "" {
		out.Turn = &plugins.CommandTurn{Input: cmdResult.PlanPrompt}
	}
	// For /new: capture old session info before switching.
	oldID := a.sessionID
	oldMsgCount := 0
	trimmed := strings.TrimSpace(line)
	fields := strings.Fields(trimmed)
	isNewCommand := len(fields) > 0 && fields[0] == "/new"
	if isNewCommand {
		if msgs, err := a.msgStore.List(a.ctx, a.sessionID); err == nil {
			oldMsgCount = len(msgs)
		}
	}
	a.sessionID = cmdResult.SessionID
	if isNewCommand {
		modeState, err := session.LoadModeState(a.sessionsDir, a.sessionID)
		if err != nil {
			return out, err
		}
		a.currentMode = modeState.Mode
		a.resetAgent()
		out.Text = fmt.Sprintf("New session\n\nsession:  %s", cmdResult.SessionID)
		if oldMsgCount > 0 {
			out.Text += fmt.Sprintf("\ndropped:  %d message(s) from %s", oldMsgCount, oldID)
		} else {
			out.Text += fmt.Sprintf("\nprevious: %s", oldID)
		}
		out.Text += fmt.Sprintf("\nresume:   whale resume %s", oldID)
		out.Text += fmt.Sprintf("\nmode:     %s", a.currentMode)
		if _, err := session.PatchSessionMeta(a.sessionsDir, a.sessionID, session.SessionMetaPatchFromMeta(a.baseSessionMeta())); err != nil {
			return out, err
		}
		out.LocalResult = buildNewSessionLocalResult(cmdResult.SessionID, oldID, string(a.currentMode), oldMsgCount, out.Text)
	}
	return out, nil
}

func trimmedCommandHead(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (a *App) HandleLocalCommand(line string) (handled bool, output string, synthetic string, err error) {
	res, err := a.ExecuteLocalCommand(line)
	if res.Turn != nil {
		synthetic = res.Turn.Input
	}
	return res.Handled, res.Text, synthetic, err
}

func (a *App) ExecuteLocalCommand(line string) (CommandExecution, error) {
	trimmed := strings.TrimSpace(line)
	if res, handled, err := a.executeGoalCommand(trimmed); handled || err != nil {
		return res, err
	}
	if IsOpenCommandLine(trimmed) {
		msg, err := a.ExecuteOpenCommand(trimmed)
		return CommandExecution{Handled: true, Text: msg}, err
	}
	if trimmed == "/mcp" {
		mcp := a.buildMCPLocalResult()
		return CommandExecution{Handled: true, Text: mcp.PlainText, LocalResult: mcp}, nil
	}
	if trimmed == "/workflows" || strings.HasPrefix(trimmed, "/workflows ") {
		fields := strings.Fields(trimmed)
		if len(fields) != 1 {
			return CommandExecution{Handled: true}, errors.New(workflowsUsage)
		}
		res := a.buildWorkflowsLocalResult("")
		return CommandExecution{Handled: true, Text: res.PlainText, LocalResult: res}, nil
	}
	if trimmed == "/deep-research" || strings.HasPrefix(trimmed, "/deep-research ") {
		opts, err := parseDeepResearchOptions(strings.TrimSpace(strings.TrimPrefix(trimmed, "/deep-research")))
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		trusted, err := a.workflowTrusted(workflow.BuiltinDeepResearchName)
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		if !opts.Confirmed && !trusted {
			res, err := a.buildWorkflowLaunchConfirmation(workflow.BuiltinDeepResearchName, opts.Question, opts.ResumeFromRunID)
			if err != nil {
				return CommandExecution{Handled: true}, err
			}
			return CommandExecution{Handled: true, Text: res.PlainText, LocalResult: res}, nil
		}
		res, err := a.startDeepResearchWorkflow(opts)
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		return CommandExecution{Handled: true, Text: res.PlainText, LocalResult: res, Mutated: true}, nil
	}
	if trimmed == "/help" {
		help := buildHelpLocalResult()
		return CommandExecution{Handled: true, Text: help.PlainText, LocalResult: help}, nil
	}
	if strings.HasPrefix(trimmed, "/help ") {
		return CommandExecution{Handled: true}, errors.New("usage: /help")
	}
	if trimmed == "/feedback" {
		return CommandExecution{Handled: true, Text: openFeedbackIssues()}, nil
	}
	if trimmed == "/diff" {
		return CommandExecution{Handled: true, Text: a.BuildDiffText(a.ctx)}, nil
	}
	if strings.HasPrefix(trimmed, "/diff ") {
		return CommandExecution{Handled: true}, errors.New("usage: /diff")
	}
	if trimmed == "/ps" {
		return a.executePSCommand(), nil
	}
	if strings.HasPrefix(trimmed, "/ps ") {
		return CommandExecution{Handled: true}, errors.New("usage: /ps")
	}
	if trimmed == "/stop" || strings.HasPrefix(trimmed, "/stop ") {
		return a.executeStopCommand(trimmed)
	}
	if trimmed == "/copy" || strings.HasPrefix(trimmed, "/copy ") {
		return a.executeCopyCommand(trimmed)
	}
	if trimmed == "/focus" {
		mode, err := a.ToggleViewMode()
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		return CommandExecution{Handled: true, Text: ViewModeToggleMessage(mode)}, nil
	}
	if strings.HasPrefix(trimmed, "/plugins ") {
		return CommandExecution{Handled: true}, errors.New("usage: /plugins")
	}
	if trimmed == "/hooks" {
		hooks := a.buildHooksLocalResult()
		return CommandExecution{Handled: true, Text: hooks.PlainText, LocalResult: hooks}, nil
	}
	if strings.HasPrefix(trimmed, "/hooks ") {
		fields := strings.Fields(trimmed)
		if len(fields) >= 3 && fields[1] == "trust" {
			keys := fields[2:]
			if len(keys) == 1 && keys[0] == "all" {
				keys = nil
			}
			text, err := a.TrustHooks(keys)
			return CommandExecution{Handled: true, Text: text, Mutated: err == nil}, err
		}
		if len(fields) >= 3 && (fields[1] == "enable" || fields[1] == "disable") {
			text, err := a.SetHookEnabled(fields[2:], fields[1] == "enable")
			return CommandExecution{Handled: true, Text: text, Mutated: err == nil}, err
		}
		return CommandExecution{Handled: true}, errors.New("usage: /hooks [trust all|trust <hook-key>...|enable <hook-key>...|disable <hook-key>...]")
	}
	if trimmed == "/stats" {
		stats := a.buildStatsLocalResult("overview")
		return CommandExecution{Handled: true, Text: stats.PlainText, LocalResult: stats}, nil
	}
	if trimmed == "/doctor" {
		result := buildDoctorLocalResult(a)
		return CommandExecution{Handled: true, Text: result.PlainText, LocalResult: result}, nil
	}
	if a.pluginManager != nil {
		res, handled, err := a.pluginManager.HandleCommand(a.ctx, trimmed)
		if handled || err != nil {
			if res.Mutated {
				a.resetAgent()
			}
			out := CommandExecution{Handled: handled, Text: res.Text, Turn: res.Turn, Mutated: res.Mutated}
			if handled && err == nil && pluginCommandID(trimmed) == "memory" && strings.TrimSpace(res.Text) != "" {
				out.LocalResult = buildMemoryLocalResult(trimmed, res.Text, res.Mutated)
			}
			return out, err
		}
		if pluginID := pluginCommandID(trimmed); pluginID != "" {
			if st, ok := a.pluginManager.Status(pluginID); ok && !st.Enabled {
				return CommandExecution{Handled: true, Text: pluginID + " plugin is disabled"}, nil
			}
		}
	}
	if strings.HasPrefix(trimmed, "/stats ") {
		fields := strings.Fields(trimmed)
		if len(fields) == 2 {
			switch fields[1] {
			case "usage", "cache", "tools", "repair", "recent", "profile", "all":
				stats := a.buildStatsLocalResult(fields[1])
				return CommandExecution{Handled: true, Text: stats.PlainText, LocalResult: stats}, nil
			}
		}
		return CommandExecution{Handled: true}, errors.New("usage: /stats [usage|cache|tools|repair|recent|profile|all]")
	}
	if strings.HasPrefix(line, "/compact") {
		fields := strings.Fields(line)
		if len(fields) != 1 || fields[0] != "/compact" {
			return CommandExecution{Handled: true}, errors.New("usage: /compact")
		}
		ag, err := a.ensureAgent()
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		info, err := ag.CompactSession(a.ctx, a.sessionID)
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		a.resetAgent()
		if !info.Compacted {
			text := "nothing to compact"
			return CommandExecution{Handled: true, Text: text, LocalResult: buildCompactLocalResult(info, text)}, nil
		}
		text := fmt.Sprintf("compacted conversation: %d -> %d messages; ~%d -> ~%d tokens", info.MessagesBefore, info.MessagesAfter, info.BeforeEstimate, info.AfterEstimate)
		return CommandExecution{Handled: true, Text: text, LocalResult: buildCompactLocalResult(info, text)}, nil
	}
	return CommandExecution{}, nil
}

func pluginCommandID(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "/memory":
		return "memory"
	default:
		return ""
	}
}
