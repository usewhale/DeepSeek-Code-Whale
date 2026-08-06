package agent

import (
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/memory"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/shell"
	"github.com/usewhale/whale/internal/skills"
)

func (a *Agent) buildTurnProviderHistory(sessionID string, rt *memory.RuntimeState) []core.Message {
	out := rt.BuildProviderHistory()
	return out
}

func (a *Agent) buildImmutableSystemBlocks(opts ...RunOptions) []string {
	return a.buildImmutableSystemBlocksWithTools(a.tools, opts...)
}

func (a *Agent) buildImmutableSystemBlocksWithTools(_ *core.ToolRegistry, opts ...RunOptions) []string {
	systemBlocks := make([]string, 0, len(a.extraSystemBlocks)+2)
	for _, block := range a.extraSystemBlocks {
		if trimmed := strings.TrimSpace(block); trimmed != "" {
			systemBlocks = append(systemBlocks, trimmed)
		}
	}
	systemBlocks = append(systemBlocks, "Mode switching commands are /agent, /ask, and /plan. Shift+Tab cycles modes in the TUI. Do not tell users to run /mode agent, /mode ask, or /mode plan; those commands do not exist.")
	systemBlocks = append(systemBlocks, renderModeContractBlock())
	systemBlocks = append(systemBlocks, renderDelegationPolicyBlock())
	systemBlocks = append(systemBlocks, "For questions about the current date or time, use an available read-only shell/time command to verify the answer instead of guessing from model memory.")
	systemBlocks = append(systemBlocks, renderToolPolicyBlock())
	systemBlocks = append(systemBlocks, "For branch decisions or key assumptions requiring user choice, call request_user_input instead of presenting long A/B/C prose menus.")
	systemBlocks = append(systemBlocks, "Execute, don't narrate: never end a turn by announcing further work — do it now, in this turn, with tool calls. A turn ends only when the user explicitly asked for it (e.g. \"stop\", \"summarize\", \"hand off\"), the task is genuinely complete, or you need user input. Otherwise keep going automatically — planned-but-unexecuted next steps are a failure, not a completion.")
	return systemBlocks
}

func (a *Agent) buildRuntimeSystemBlocks(opts ...RunOptions) []string {
	systemBlocks := make([]string, 0, len(a.dynamicSystemBlocks)+6)
	var turnOpts RunOptions
	if len(opts) > 0 {
		turnOpts = opts[0]
	}
	if strings.TrimSpace(a.workspaceRoot) != "" {
		discovered := skills.Filter(skills.Discover(skills.DefaultRoots(a.workspaceRoot)), a.disabledSkills)
		discovered = append(discovered, skills.Filter(a.extraSkills, a.disabledSkills)...)
		discovered = skills.Sort(skills.Deduplicate(discovered))
		if rendered := skills.RenderAvailableSkills(discovered); rendered != "" {
			systemBlocks = append(systemBlocks, rendered)
		}
	}
	systemBlocks = append(systemBlocks, "For branch decisions or key assumptions requiring user choice, call request_user_input instead of presenting long A/B/C prose menus.")
	if a.projectMemoryEnabled {
		if mem, ok := memory.ReadProjectMemory(a.workspaceRoot, a.projectMemoryFileOrder, a.projectMemoryMaxChars); ok {
			systemBlocks = append(systemBlocks,
				"# Project Memory\n\nThe user pinned these notes about this project. Treat them as authoritative context for this workspace:\n\n```\n"+mem.Content+"\n```",
			)
		}
	}
	for _, render := range a.dynamicSystemBlocks {
		if render == nil {
			continue
		}
		if trimmed := strings.TrimSpace(render(turnOpts)); trimmed != "" {
			systemBlocks = append(systemBlocks, trimmed)
		}
	}
	if block := renderOutputStyleBlock(turnOpts.ViewMode); block != "" {
		systemBlocks = append(systemBlocks, block)
	}
	systemBlocks = append(systemBlocks, renderRuntimeBlock(a.workspaceRoot, runtimeWorktreeContext{WorktreeRoot: a.worktreeRoot, OriginalWorkspace: a.originalWorkspace}, shell.DescribeRuntime()))
	return systemBlocks
}

func renderModeContractBlock() string {
	return strings.TrimSpace(`
Mode contract.

- Agent mode is the execution mode. You may use read-only and mutating tools, including file edits, writes, patches, shell commands, workflow launches, and writable subagents, subject to policy and user approval.
- Ask mode is read-only answer mode. Answer questions and use read-only inspection tools when helpful. Do not modify files or act as though you are implementing changes. If code changes are needed, explain or outline them instead.
- Safe read-only shell commands may run in Ask or Plan mode. If a shell command is blocked, do not say all shell commands are disabled; say that specific command is not classified as safe read-only.
- If any tool result has code ask_mode_blocked or plan_mode_blocked, do not retry the same tool call or the same shell operation with another shell command or another tool. Continue only with clearly allowed read-only alternatives, or explain the block.

` + session.PlanModeInstruction())
}

func renderWorkflowAuthoringBlock() string {
	return strings.TrimSpace(`
Workflow authoring.

- When the user asks to create, generate, or write a new workflow, use the workflow tool with both script and saveAs. Do not answer by only describing a workflow.
- Do not inspect existing workflow directories, load skills, or pre-read repository files before creating the workflow unless the user explicitly asks for a preflight. Put needed inspection work inside the generated workflow's agent prompts.
- The generated script must be a Claude Code-compatible raw JavaScript workflow: first statement export const meta as a pure literal with name, description, optional whenToUse, and optional phases.
- If meta.phases is present, it must be an array of objects such as { title: "Review", detail: "one reviewer per dimension" }, not strings and not { name, description } objects.
- Use only portable workflow globals in generated scripts: args, budget, phase(), log(), agent(), workflow(), parallel(), and pipeline(). Do not use Whale-only APIs or host APIs such as require, import, fs, fetch, process, Date.now, Math.random, or new Date.
- Workflow agent leaves are tool-scoped workers. Use agent definitions and opts.tools/opts.disallowedTools to state required tool selectors. Supported selectors include workspace.read, workspace.write, shell.read, shell.run, web.search, web.fetch, mcp.read, and exact tool names; shell.run or workspace.write require an explicit non-read-only permissionMode. If a needed selector is not exposed by the runtime, make the workflow report the missing evidence instead of assuming shell, edit, or host access.
- Call phase("Name") only as a statement. Do not write phase("Name", async () => ...); phase() is not a callback wrapper and returns nothing.
- Await async workflow primitives before reading their results: const result = await agent(...), await parallel(...), await pipeline(...), or await workflow(...). Inside parallel(), thunks may return agent(...).
- Call agent(prompt, { label, phase, schema, max_tool_calls?, agent?, tools?, disallowedTools?, effort?, permissionMode?, maxTurns? }). The first argument is the complete prompt string. Do not use opts.system, opts.prompt, opts.structured, or a first-argument label.
- Do not set opts.model in generated workflows unless the user explicitly asks for a provider-supported model. By default, omit model so Whale uses the current configured model. Never hard-code Claude model names such as sonnet, opus, or haiku for Whale-generated workflows.
- saveAs must exactly match meta.name and must be kebab-case. The tool saves the script to the project .whale/workflows directory before launching it.
- Use standard JSON Schema for structured output. If a property uses enum, include an explicit type such as type: "string" so the script can run in both Whale and Claude Code.
- End generated workflows by returning a final JSON-serializable result, usually the synthesis/report object.
- For create-workflow launches, tell the user the workflow was saved and that /workflows opens the workflow panel. Do not mention /workflows with run ids or hidden subcommands.
`)
}

func WorkflowAuthoringSystemBlock() string {
	return renderWorkflowAuthoringBlock()
}

func renderOutputStyleBlock(viewMode string) string {
	if strings.TrimSpace(viewMode) != "focus" {
		return ""
	}
	return strings.TrimSpace(`
Focus view is active in the terminal.

- Keep user-facing text brief and high-level.
- Emit text only when it changes what the user needs to know: a result, finding, blocker, risk, decision point, meaningful plan change, milestone, or final summary.
- The UI already summarizes tool calls, file reads, searches, shell commands, edits, plans, and todos. Do not write assistant text merely to announce routine tool use, file inspection, searching, reading, or continuing with the next obvious step.
- Lead with the answer, action, blocker, or decision. Skip preambles and routine narration.
- Do not list every command, file, or tool call unless those details are evidence for a finding or the user explicitly asked for them.
`)
}

type runtimeWorktreeContext struct {
	WorktreeRoot      string
	OriginalWorkspace string
}

func renderRuntimeBlock(workspaceRoot string, worktree runtimeWorktreeContext, rt shell.RuntimeDescription) string {
	var b strings.Builder
	b.WriteString("Current Whale runtime:\n")
	if strings.TrimSpace(workspaceRoot) != "" {
		b.WriteString("- Current Whale workspace root: ")
		b.WriteString(workspaceRoot)
		b.WriteString("\n")
	}
	// Only surface the worktree root as its own line when it differs from the
	// workspace root (i.e. the session was started from a subdirectory). When
	// they are equal, printing it twice is noise that dilutes the single-path
	// signal src relies on.
	worktreeRoot := strings.TrimSpace(worktree.WorktreeRoot)
	if worktreeRoot != "" && worktreeRoot != strings.TrimSpace(workspaceRoot) {
		b.WriteString("- Current worktree root: ")
		b.WriteString(worktreeRoot)
		b.WriteString("\n")
	}
	if worktreeRoot != "" && strings.TrimSpace(worktree.OriginalWorkspace) != "" {
		// Co-locate the isolation rule with the path lines and keep it a single
		// concrete instruction (mirrors src). Do NOT name the parent checkout's
		// path: the worktree root already embeds it, and repeating it only invites
		// the model to strip the suffix and operate on the parent.
		b.WriteString("- This is a git worktree, an isolated copy of the repository and the project for this session. Run all commands and resolve all paths from this directory; do not cd to, read from, or modify the parent checkout it was created from unless the user explicitly asks.\n")
	}
	if strings.TrimSpace(rt.GOOS) != "" {
		b.WriteString("- OS: ")
		b.WriteString(strings.TrimSpace(rt.GOOS))
		b.WriteString("\n")
	}
	b.WriteString("- Shell: ")
	b.WriteString(rt.ShellSummary())
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Shell commands run from the current Whale workspace by default. Do not assume a synthetic path such as /workspace; use relative paths or the %s cwd parameter for subdirectories. Filesystem tools resolve relative paths from the current workspace and can request file access approval for external read paths when the user asks to inspect files outside the workspace. If access or execution is denied, do not retry the same external operation through another tool unless the user explicitly asks again.", core.DisplayToolName("shell_run")))
	if guidance := rt.CommandGuidance(); strings.TrimSpace(guidance) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(guidance))
	}
	return strings.TrimSpace(b.String())
}

func renderDelegationPolicyBlock() string {
	return strings.TrimSpace(`
Delegation policy.

- Do not use parallel_reason or spawn_subagent just because they are available.
- Use a single agent for direct questions, known-file reads, small localized edits, tightly coupled work, or tasks where the next step depends on the current result.
- Use parallel_reason for 2-8 independent, cheap, model-only subqueries that need comparison, classification, critique, or brainstorming and do not need tools, files, shell, or web access.
- Use spawn_subagent for one bounded tool-scoped exploration, research, or review task. Select a built-in role or named agent definition; child agents receive only the selected definition tools or the call's tools allowlist. Omit tools for role defaults; pass tools: [] for model-only synthesis.
- Do not ask the user to name these tools. Infer the right path from natural language such as "parallelize this" or "send a reviewer/explorer".
- If the user explicitly asks for a subagent, delegated reviewer, or explorer, spawn the appropriate subagent directly. Do not load a skill first unless the user explicitly names one.
- The parent agent owns the final answer. Summarize and reconcile child results before responding to the user.
- Do not delegate writable or high-risk work unless the runtime explicitly provides an isolated writable worker capability.
- Weigh context overlap before spawning: a child agent starts cold and cannot see anything you have already read, searched, or concluded in this conversation. If you already hold most of the relevant context (files read, code traced, prior analysis), continue working directly instead of paying for a child to re-derive it. Only spawn when the overlap is genuinely low: an independent area you have not touched, parallel fan-out, or a fresh sub-question.
- Never delegate understanding. Do not spawn a subagent to redo analysis, verification, or investigation you have already performed yourself this turn or session just because a user pushed back or asked you to double-check — re-verify directly using what you already know plus targeted follow-up reads.
- Do not issue multiple spawn_subagent calls in a row for the same unresolved question. If a child's findings are insufficient, synthesize and continue yourself rather than spawning another child to answer the same question.
`)
}

func renderToolPolicyBlock() string {
	// Tool names are rendered through DisplayToolName so the prose always
	// matches the model-facing names in the provider tool schema. The mapping
	// is static, so this output is byte-stable and does not affect prompt cache.
	return strings.TrimSpace(fmt.Sprintf(`
Tool use policy.

- Tools are provided through the provider tool schema. Choose tools by exact name and schema; do not invent tools that are not present in the schema.
- Prefer read-only inspection tools for exploration: %s, %s, %s, %s, %s, %s, and clearly read-only MCP tools when available.
- Mutating tools such as %s, %s, %s, %s with non-read-only commands, workflow launches, and writable subagents may be blocked by mode, policy, or user approval. In read-only modes, use read-only alternatives and do not request writes.
- %s can be read-only only for safe inspection commands accepted by policy; build, test, install, start, and file-changing shell commands may require approval or be denied.
- If a tool call is blocked, denied, rejected, or returns a permission/mode error, do not retry the same action through another tool unless the user explicitly asks.
- MCP tools listed under <available-deferred-tools> are not yet in your schema. Use %s to search and load them before calling.
`,
		core.DisplayToolName("read_file"), core.DisplayToolName("list_dir"), core.DisplayToolName("grep"),
		core.DisplayToolName("search_files"), core.DisplayToolName("web_search"), core.DisplayToolName("web_fetch"),
		core.DisplayToolName("multi_edit"), core.DisplayToolName("edit"), core.DisplayToolName("write"), core.DisplayToolName("shell_run"),
		core.DisplayToolName("shell_run"),
		core.DisplayToolName("tool_search"),
	))
}
