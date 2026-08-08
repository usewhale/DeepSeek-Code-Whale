package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/shell"
)

func TestRuntimeEnvironmentBlockIncludesWorkspaceAndShellRunCWD(t *testing.T) {
	block := renderRuntimeBlock("/repo", runtimeWorktreeContext{}, shell.RuntimeDescription{
		GOOS: "linux",
		Spec: shell.Spec{Kind: shell.KindPOSIX, DisplayName: "/bin/sh"},
	})

	for _, want := range []string{
		"Current Whale runtime:",
		"OS: linux",
		"Current Whale workspace root: /repo",
		"Shell: /bin/sh (/bin/sh -lc)",
		"Shell commands run from the current Whale workspace by default",
		"Bash cwd parameter",
		"request file access approval for external read paths",
		"do not retry the same external operation through another tool",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("runtime block missing %q:\n%s", want, block)
		}
	}
}

func TestRuntimeEnvironmentBlockWindowsUsesCurrentShellRunName(t *testing.T) {
	block := renderRuntimeBlock(`C:\repo`, runtimeWorktreeContext{}, shell.RuntimeDescription{
		GOOS: "windows",
		Spec: shell.Spec{Kind: shell.KindPowerShell, DisplayName: "PowerShell"},
	})

	for _, want := range []string{
		"OS: windows",
		"Shell: PowerShell",
		`Current Whale workspace root: C:\repo`,
		"Use PowerShell syntax",
		"read_file",
		"Ask/Plan mode",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("windows runtime block missing %q:\n%s", want, block)
		}
	}
	for _, old := range []string{"exec" + "_shell", "always " + "PowerShell"} {
		if strings.Contains(block, old) {
			t.Fatalf("windows runtime block contains stale wording %q:\n%s", old, block)
		}
	}
}

func TestRuntimeSystemBlocksIncludeRuntimeEnvironment(t *testing.T) {
	a := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithProjectMemory(false, 0, nil, "/repo"))
	immutable := strings.Join(a.buildImmutableSystemBlocks(), "\n\n")
	if strings.Contains(immutable, "Current Whale runtime:") || strings.Contains(immutable, "Current Whale workspace root: /repo") {
		t.Fatalf("runtime environment leaked into immutable system blocks:\n%s", immutable)
	}

	joined := strings.Join(a.buildRuntimeSystemBlocks(), "\n\n")

	if !strings.Contains(joined, "Current Whale runtime:") {
		t.Fatalf("runtime system blocks missing runtime environment:\n%s", joined)
	}
	if !strings.Contains(joined, "Current Whale workspace root: /repo") {
		t.Fatalf("runtime system blocks missing workspace root:\n%s", joined)
	}
	if !strings.Contains(joined, "Bash cwd parameter") {
		t.Fatalf("runtime system blocks missing Bash cwd guidance:\n%s", joined)
	}
}

func TestRuntimeSystemBlocksIncludeDynamicSystemBlocks(t *testing.T) {
	a := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil),
		WithDynamicSystemBlocks(func() string {
			return "Available workflows.\n\n- dead-code-scan [project]: Scan for dead code."
		}),
	)
	immutable := strings.Join(a.buildImmutableSystemBlocks(), "\n\n")
	if strings.Contains(immutable, "Available workflows.") || strings.Contains(immutable, "dead-code-scan [project]") || strings.Contains(immutable, "Current session mode:") {
		t.Fatalf("dynamic system blocks leaked into immutable system blocks:\n%s", immutable)
	}

	joined := strings.Join(a.buildRuntimeSystemBlocks(), "\n\n")

	for _, want := range []string{
		"Available workflows.",
		"dead-code-scan [project]",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("runtime system blocks missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Current session mode:") {
		t.Fatalf("current mode marker leaked into runtime system blocks:\n%s", joined)
	}
}

func TestImmutableSystemBlocksDoNotIncludeWorkflowAuthoringGuidance(t *testing.T) {
	a := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithProjectMemory(false, 0, nil, "/repo"))
	joined := strings.Join(a.buildImmutableSystemBlocks(), "\n\n")

	for _, unexpected := range []string{
		"Workflow authoring.",
		"use the workflow tool with both script and saveAs",
		"Claude Code-compatible raw JavaScript workflow",
	} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("immutable system blocks should not include workflow authoring %q:\n%s", unexpected, joined)
		}
	}
}

func TestWorkflowAuthoringSystemBlockIncludesGuidance(t *testing.T) {
	joined := WorkflowAuthoringSystemBlock()

	for _, want := range []string{
		"Workflow authoring.",
		"use the workflow tool with both script and saveAs",
		"Do not inspect existing workflow directories, load skills, or pre-read repository files",
		"Claude Code-compatible raw JavaScript workflow",
		"array of objects such as { title:",
		"tool-scoped workers",
		"Call phase(\"Name\") only as a statement",
		"Await async workflow primitives before reading their results",
		"Call agent(prompt, { label, phase, schema",
		"Never hard-code Claude model names",
		"saveAs must exactly match meta.name",
		"enum, include an explicit type",
		"returning a final JSON-serializable result",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("system blocks missing %q:\n%s", want, joined)
		}
	}
}

func TestNaturalLanguageWorkflowPromptCanUseNamedWorkflowTool(t *testing.T) {
	provider := &catalogWorkflowProvider{}
	tool := &recordingWorkflowTool{}
	a := NewAgentWithRegistry(provider, NewInMemoryStore(), core.NewToolRegistry([]core.Tool{tool}),
		WithDynamicSystemBlocks(func() string {
			return "Available workflows.\n\nUse the workflow tool when the user names one of these workflows.\n\n- dead-code-scan [project]: Scan for dead code."
		}),
	)

	for range mustRunStreamWithOptions(t, a, "s-workflow-catalog", "run dead-code-scan workflow on this repo", RunOptions{}) {
	}

	if !provider.sawCatalog {
		t.Fatalf("provider did not see workflow catalog in system prompt")
	}
	if !provider.sawWorkflowTool {
		t.Fatalf("provider did not see workflow tool")
	}
	if !strings.Contains(tool.input, `"name":"dead-code-scan"`) {
		t.Fatalf("workflow tool was not called with named workflow, input=%q", tool.input)
	}
}

type catalogWorkflowProvider struct {
	calls           int
	sawCatalog      bool
	sawWorkflowTool bool
}

func (p *catalogWorkflowProvider) StreamResponse(_ context.Context, history []Message, tools []Tool) <-chan ProviderEvent {
	p.calls++
	for _, msg := range history {
		if msg.Role == RoleSystem && strings.Contains(msg.Text, "dead-code-scan [project]") {
			p.sawCatalog = true
		}
	}
	for _, tool := range tools {
		if tool.Name() == "workflow" {
			p.sawWorkflowTool = true
		}
	}
	if p.calls == 1 {
		return eventStream(toolUseEvent(toolCall("wf-1", "workflow", `{"name":"dead-code-scan","args":"run dead-code-scan workflow on this repo"}`)))
	}
	return eventStream(endTurnEvent("workflow launched"))
}

type recordingWorkflowTool struct {
	input string
}

func (t *recordingWorkflowTool) Name() string { return "workflow" }
func (t *recordingWorkflowTool) Description() string {
	return "Launch a workflow by name."
}
func (t *recordingWorkflowTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"args": map[string]any{},
		},
	}
}
func (t *recordingWorkflowTool) Run(_ context.Context, call core.ToolCall) (core.ToolResult, error) {
	t.input = call.Input
	return core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: "ok"}, nil
}

func TestRuntimeEnvironmentBlockIncludesWorktreeContext(t *testing.T) {
	block := renderRuntimeBlock("/repo/.whale/worktrees/feature", runtimeWorktreeContext{
		WorktreeRoot:      "/repo/.whale/worktrees/feature",
		OriginalWorkspace: "/repo",
	}, shell.RuntimeDescription{
		GOOS: "linux",
		Spec: shell.Spec{Kind: shell.KindPOSIX, DisplayName: "/bin/sh"},
	})

	for _, want := range []string{
		"Current Whale workspace root: /repo/.whale/worktrees/feature",
		"This is a git worktree, an isolated copy of the repository",
		"Run all commands and resolve all paths from this directory",
		"do not cd to, read from, or modify the parent checkout",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("worktree runtime block missing %q:\n%s", want, block)
		}
	}
	// The worktree root equals the workspace root here, so it must not be printed
	// a second time: the duplicate path line is noise that dilutes the single-path
	// signal src relies on.
	if strings.Contains(block, "Current worktree root:") {
		t.Fatalf("worktree runtime block duplicated the worktree path:\n%s", block)
	}
	// The original checkout's absolute path must never be surfaced to the model:
	// doing so invites it to read/grep the parent repo and trip per-directory
	// external-directory approvals (mirrors src, which drops the path entirely).
	if strings.Contains(block, "Original workspace: /repo") {
		t.Fatalf("worktree runtime block leaked original workspace path:\n%s", block)
	}
}

func TestRuntimeEnvironmentBlockShowsWorktreeRootWhenStartedFromSubdir(t *testing.T) {
	block := renderRuntimeBlock("/repo/.whale/worktrees/feature/sub", runtimeWorktreeContext{
		WorktreeRoot:      "/repo/.whale/worktrees/feature",
		OriginalWorkspace: "/repo",
	}, shell.RuntimeDescription{
		GOOS: "linux",
		Spec: shell.Spec{Kind: shell.KindPOSIX, DisplayName: "/bin/sh"},
	})

	for _, want := range []string{
		"Current Whale workspace root: /repo/.whale/worktrees/feature/sub",
		"Current worktree root: /repo/.whale/worktrees/feature",
		"This is a git worktree, an isolated copy of the repository",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("worktree runtime block missing %q:\n%s", want, block)
		}
	}
}

func TestRuntimeSystemBlocksDeclareCurrentModeAuthoritatively(t *testing.T) {
	a := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModeAsk))
	immutable := strings.Join(a.buildImmutableSystemBlocks(), "\n\n")
	if strings.Contains(immutable, "Current session mode: ask") {
		t.Fatalf("mode authority leaked into immutable system blocks:\n%s", immutable)
	}
	for _, want := range []string{
		"Mode switching commands are /agent, /ask, and /plan",
		"Do not tell users to run /mode agent",
		"Mode contract.",
		"Agent mode is the execution mode.",
		"Ask mode is read-only answer mode.",
		"Plan Mode is a collaboration mode for designing the work before implementation",
		"ask_mode_blocked or plan_mode_blocked",
	} {
		if !strings.Contains(immutable, want) {
			t.Fatalf("immutable mode guidance missing %q:\n%s", want, immutable)
		}
	}

	joined := strings.Join(a.buildRuntimeSystemBlocks(), "\n\n")
	for _, notWant := range []string{
		"Current session mode:",
		"Treat older mode markers as stale.",
		"Ask mode is active.",
		"Agent mode is active.",
		"You are in PLAN mode.",
	} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("runtime system blocks should not include long mode text %q:\n%s", notWant, joined)
		}
	}
}

func TestPlanModeInstructionsTreatExecutionRequestsAsPlanning(t *testing.T) {
	a := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModePlan))
	immutable := strings.Join(a.buildImmutableSystemBlocks(), "\n\n")

	for _, want := range []string{
		"User intent, imperative wording",
		"create a branch",
		"Treat execution requests in Plan mode as requests to plan that execution",
		"do not retry the same tool call or the same shell operation with another shell command",
		"Running side-effectful commands whose purpose is to carry out the plan",
		"Outputting slash commands such as /agent",
		"The session remains in Plan mode until the user or UI explicitly changes modes",
		"Ground the plan in the actual environment",
		"How to present the plan",
		"taken as your proposed plan",
		"update_plan is a TODO/checklist/progress tool",
		"the user is then asked to approve it",
	} {
		if !strings.Contains(immutable, want) {
			t.Fatalf("immutable plan mode contract missing %q:\n%s", want, immutable)
		}
	}

	joined := strings.Join(a.buildRuntimeSystemBlocks(), "\n\n")
	if strings.Contains(joined, "User intent, imperative wording") || strings.Contains(joined, "Running side-effectful commands whose purpose is to carry out the plan") {
		t.Fatalf("runtime should not include long plan mode instructions:\n%s", joined)
	}
	if strings.Contains(joined, "Current session mode:") {
		t.Fatalf("runtime should not include current mode marker:\n%s", joined)
	}
}

func TestRuntimeSystemPromptStableAcrossSessionModes(t *testing.T) {
	agentMode := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModeAgent), WithProjectMemory(false, 0, nil, "/repo"))
	askMode := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModeAsk), WithProjectMemory(false, 0, nil, "/repo"))
	planMode := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModePlan), WithProjectMemory(false, 0, nil, "/repo"))

	agentPrompt := strings.Join(agentMode.buildRuntimeSystemBlocks(), "\n\n")
	askPrompt := strings.Join(askMode.buildRuntimeSystemBlocks(), "\n\n")
	planPrompt := strings.Join(planMode.buildRuntimeSystemBlocks(), "\n\n")
	if agentPrompt != askPrompt || agentPrompt != planPrompt {
		t.Fatalf("runtime provider prompt should not change across modes")
	}
	if strings.Contains(agentPrompt, "Current session mode:") {
		t.Fatalf("runtime provider prompt leaked current mode marker:\n%s", agentPrompt)
	}
}

func TestProviderHistoryReplaysPersistedModeChangeMarker(t *testing.T) {
	provider := &modeTailCaptureProvider{}
	store := NewInMemoryStore()
	a := NewAgentWithRegistry(provider, store, core.NewToolRegistry(nil), WithSessionMode(session.ModePlan), WithProjectMemory(false, 0, nil, "/repo"))

	_, err := store.Create(context.Background(), core.Message{
		SessionID: "s-mode-tail",
		Role:      core.RoleUser,
		Text:      "<mode_changed>\nThe active session mode is now plan, changed from agent. When the plan is decision-complete, output exactly one <proposed_plan> block.\n</mode_changed>",
		Hidden:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for range mustRunStreamWithOptions(t, a, "s-mode-tail", "draft a plan", RunOptions{}) {
	}

	if len(provider.history) == 0 {
		t.Fatal("provider did not receive history")
	}
	var marker core.Message
	markerIndex := -1
	for i, msg := range provider.history {
		if strings.Contains(msg.Text, "<mode_changed>") {
			marker = msg
			markerIndex = i
			break
		}
	}
	if markerIndex < 0 {
		t.Fatalf("provider history missing persisted mode-change marker: %+v", provider.history)
	}
	if marker.Role != core.RoleUser || !marker.Hidden {
		t.Fatalf("provider mode marker should be hidden history message, got role=%s hidden=%v text=%q", marker.Role, marker.Hidden, marker.Text)
	}
	last := provider.history[len(provider.history)-1]
	if last.Text != "draft a plan" {
		t.Fatalf("mode-change marker should not replace the last user prompt, last=%+v", last)
	}
	for _, want := range []string{
		"<mode_changed>",
		"active session mode is now plan",
		"<proposed_plan>",
	} {
		if !strings.Contains(marker.Text, want) {
			t.Fatalf("mode marker missing %q:\n%s", want, marker.Text)
		}
	}
	for _, msg := range provider.history {
		if strings.Contains(msg.Text, "<whale_runtime_mode>") {
			t.Fatalf("provider history should not include per-turn runtime mode marker: %+v", msg)
		}
	}
	for _, msg := range provider.history {
		if msg.Role == core.RoleSystem && strings.Contains(msg.Text, "Current session mode:") {
			t.Fatalf("current mode leaked into system prompt:\n%s", msg.Text)
		}
	}
	persisted, err := store.List(context.Background(), "s-mode-tail")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range persisted {
		if strings.Contains(msg.Text, "<whale_runtime_mode>") {
			t.Fatalf("per-turn mode marker should not be persisted: %+v", msg)
		}
	}
}

type modeTailCaptureProvider struct {
	history []Message
}

func (p *modeTailCaptureProvider) StreamResponse(_ context.Context, history []Message, _ []Tool) <-chan ProviderEvent {
	p.history = append([]Message(nil), history...)
	// Return a finalized plan so plan-mode finalization recovery does not fire
	// and append a recovery turn; this test only inspects the first request's
	// mode-marker replay, not recovery behavior.
	return eventStream(endTurnEvent("<proposed_plan>\nok\n</proposed_plan>"))
}

func TestImmutableSystemPromptStableAcrossSessionModes(t *testing.T) {
	agentMode := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModeAgent))
	askMode := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModeAsk))
	planMode := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(session.ModePlan))

	agentPrompt := strings.Join(agentMode.buildImmutableSystemBlocks(), "\n\n")
	askPrompt := strings.Join(askMode.buildImmutableSystemBlocks(), "\n\n")
	planPrompt := strings.Join(planMode.buildImmutableSystemBlocks(), "\n\n")
	if agentPrompt != askPrompt || agentPrompt != planPrompt {
		t.Fatalf("immutable system prompt should be stable across modes")
	}
}

func TestRuntimeSystemBlocksIncludeFocusOutputStyleOnlyInFocusView(t *testing.T) {
	a := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil))

	immutable := strings.Join(a.buildImmutableSystemBlocks(RunOptions{ViewMode: "focus"}), "\n\n")
	if strings.Contains(immutable, "Focus view is active") {
		t.Fatalf("focus output style leaked into immutable system blocks:\n%s", immutable)
	}

	defaultPrompt := strings.Join(a.buildRuntimeSystemBlocks(RunOptions{ViewMode: "default"}), "\n\n")
	if strings.Contains(defaultPrompt, "Focus view is active") {
		t.Fatalf("default runtime should not include focus output style:\n%s", defaultPrompt)
	}

	focusPrompt := strings.Join(a.buildRuntimeSystemBlocks(RunOptions{ViewMode: "focus"}), "\n\n")
	for _, want := range []string{
		"Focus view is active in the terminal.",
		"Emit text only when it changes what the user needs to know",
		"result, finding, blocker, risk, decision point",
		"Do not write assistant text merely to announce routine tool use",
		"file inspection, searching, reading, or continuing with the next obvious step",
	} {
		if !strings.Contains(focusPrompt, want) {
			t.Fatalf("runtime focus output style missing %q:\n%s", want, focusPrompt)
		}
	}
}

func TestImmutableSystemPromptUsesStableToolPolicyWithoutToolCatalog(t *testing.T) {
	tools := core.NewToolRegistry([]core.Tool{&recordingWorkflowTool{}})
	a := NewAgentWithRegistry(nil, nil, tools)
	joined := strings.Join(a.buildImmutableSystemBlocks(), "\n\n")

	for _, want := range []string{
		"Tool use policy.",
		"provider tool schema",
		"Choose tools by exact name and schema",
		"do not invent tools",
		"Prefer read-only inspection tools",
		"Read, LS, Grep, Glob",
		"Mutating tools such as MultiEdit, Edit, Write",
		"may be blocked by mode, policy, or user approval",
		"Bash can be read-only only for safe inspection commands accepted by policy",
		"do not retry the same action through another tool",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("tool policy missing %q:\n%s", want, joined)
		}
	}
	for _, notWant := range []string{
		"Available tools",
		"No tools are available.",
		"workflow [",
		"Launch a workflow by name.",
		" args:",
		" approval:",
	} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("system prompt leaked tool catalog text %q:\n%s", notWant, joined)
		}
	}
}

func TestImmutableSystemPromptToolPolicyDoesNotDependOnToolRegistry(t *testing.T) {
	withoutTools := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil))
	withTools := NewAgentWithRegistry(nil, nil, core.NewToolRegistry([]core.Tool{&recordingWorkflowTool{}}))

	a := strings.Join(withoutTools.buildImmutableSystemBlocks(), "\n\n")
	b := strings.Join(withTools.buildImmutableSystemBlocks(), "\n\n")
	if a != b {
		t.Fatalf("immutable system prompt changed with tool registry\nwithout tools:\n%s\n\nwith tools:\n%s", a, b)
	}
}

// TestImmutableSystemBlocksIncludeAnnounceThenStop verifies the announce-
// then-stop instruction is a static immutable block, so it reaches every
// entrypoint (ACP, terminal CLI, subagents) that shares the turn loop.
func TestImmutableSystemBlocksIncludeAnnounceThenStop(t *testing.T) {
	a := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithProjectMemory(false, 0, nil, "/repo"))
	joined := strings.Join(a.buildImmutableSystemBlocks(), "\n\n")

	for _, want := range []string{
		"Execute, don't narrate",
		"never end a turn by announcing further work",
		"do it now, in this turn, with tool calls",
		`"stop"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("announce-then-stop block missing %q:\n%s", want, joined)
		}
	}
	if got := strings.Count(joined, "Execute, don't narrate"); got != 1 {
		t.Fatalf("announce-then-stop block appears %d times, want exactly once:\n%s", got, joined)
	}
	// The instruction is a contract, not runtime context: it must never depend
	// on the tool registry or session mode, and it must not be duplicated into
	// the runtime blocks (which are re-rendered per turn).
	runtime := strings.Join(a.buildRuntimeSystemBlocks(), "\n\n")
	if strings.Contains(runtime, "Execute, don't narrate") {
		t.Fatalf("announce-then-stop block leaked into runtime blocks:\n%s", runtime)
	}
	modes := []session.Mode{session.ModeAgent, session.ModeAsk, session.ModePlan}
	for _, m := range modes {
		withMode := NewAgentWithRegistry(nil, nil, core.NewToolRegistry(nil), WithSessionMode(m))
		if got := strings.Join(withMode.buildImmutableSystemBlocks(), "\n\n"); !strings.Contains(got, "Execute, don't narrate") {
			t.Fatalf("mode %v missing announce-then-stop block", m)
		}
	}
	// Immutable blocks are shared by every entrypoint via the turn loop; the
	// tool registry must not change the block (it is appended unconditionally).
	withTools := NewAgentWithRegistry(nil, nil, core.NewToolRegistry([]core.Tool{&recordingWorkflowTool{}}))
	if got := strings.Join(withTools.buildImmutableSystemBlocks(), "\n\n"); !strings.Contains(got, "Execute, don't narrate") {
		t.Fatalf("with-tools agent missing announce-then-stop block")
	}
}
