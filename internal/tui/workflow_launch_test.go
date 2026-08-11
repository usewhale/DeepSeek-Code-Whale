package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

func TestWorkflowLaunchLocalResultOpensInteractiveModal(t *testing.T) {
	m := model{
		assembler:           tuirender.NewAssembler(),
		mode:                modeChat,
		width:               100,
		height:              30,
		localSubmitCommands: []string{"/deep-research question"},
	}
	result := workflowLaunchTestResult()

	cmd := m.handleLocalSubmitResultEvent(protocol.Event{Status: "info", Text: result.PlainText, LocalResult: result})
	if cmd != nil {
		t.Fatal("did not expect command while opening workflow launch modal")
	}
	if m.mode != modeWorkflowLaunch {
		t.Fatalf("mode = %v, want workflow launch", m.mode)
	}
	if m.workflowLaunch.result == nil || m.workflowLaunch.result.Kind != "workflow-launch" {
		t.Fatalf("missing workflow launch result: %+v", m.workflowLaunch.result)
	}
	if got := m.renderWorkflowLaunch(); got == "" || !strings.Contains(got, "Run a dynamic workflow?") || !strings.Contains(got, "Yes, run it") || !strings.Contains(got, "Esc to cancel") {
		t.Fatalf("launch render missing expected content:\n%s", got)
	}
	if got := m.renderWorkflowLaunch(); !strings.Contains(got, "View raw script") {
		t.Fatalf("launch render missing raw script action:\n%s", got)
	}
}

func TestWorkflowLaunchLocalResultPrunesReasoningOnlyFallback(t *testing.T) {
	m := model{
		assembler:           tuirender.NewAssembler(),
		mode:                modeChat,
		width:               100,
		height:              30,
		localSubmitCommands: []string{"/deep-research question"},
	}
	m.appendStatus("The model returned reasoning only and did not produce a visible answer. Ask it to answer directly or retry the last step.")
	m.commitLiveTranscript(false)

	result := workflowLaunchTestResult()
	m.handleLocalSubmitResultEvent(protocol.Event{Status: "info", Text: result.PlainText, LocalResult: result})

	got := strings.Join(tuirender.ChatLines(m.transcript, 120), "\n")
	if strings.Contains(got, "Reasoning only") || strings.Contains(got, "did not produce a visible answer") {
		t.Fatalf("workflow launch should prune reasoning-only fallback:\n%s", got)
	}
	if m.mode != modeWorkflowLaunch {
		t.Fatalf("mode = %v, want workflow launch", m.mode)
	}
}

func TestWorkflowLaunchEnterRunsSelectedAction(t *testing.T) {
	intents := []protocol.Intent{}
	m := model{
		assembler: tuirender.NewAssembler(),
		mode:      modeWorkflowLaunch,
		width:     100,
		height:    30,
		workflowLaunch: struct {
			result    *protocol.LocalResult
			selected  int
			rawScroll int
		}{result: workflowLaunchTestResult()},
		dispatch: func(in protocol.Intent) {
			intents = append(intents, in)
		},
	}

	cmd := m.handleWorkflowLaunchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("did not expect async command")
	}
	if m.mode != modeChat {
		t.Fatalf("mode = %v, want chat", m.mode)
	}
	if len(intents) != 1 {
		t.Fatalf("intents = %+v, want one", intents)
	}
	if intents[0].Kind != protocol.IntentStartWorkflow || intents[0].WorkflowName != "deep-research" || intents[0].WorkflowArgs != "question" || intents[0].WorkflowResume != "run-source" {
		t.Fatalf("unexpected intent: %+v", intents[0])
	}
}

func TestWorkflowLaunchViewRawScriptIsReadOnlyAndReturnsToConfirmation(t *testing.T) {
	m := model{
		assembler: tuirender.NewAssembler(),
		mode:      modeWorkflowLaunch,
		width:     100,
		height:    16,
		workflowLaunch: struct {
			result    *protocol.LocalResult
			selected  int
			rawScroll int
		}{result: workflowLaunchTestResult(), selected: 1},
	}

	cmd := m.handleWorkflowLaunchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("did not expect async command")
	}
	if m.mode != modeWorkflowRawScript {
		t.Fatalf("mode = %v, want raw script", m.mode)
	}
	got := m.renderWorkflowRawScript()
	for _, want := range []string{"Workflow raw script", "Script    builtin:deep-research", "export const meta", "Esc back"} {
		if !strings.Contains(got, want) {
			t.Fatalf("raw script render missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "edit") || strings.Contains(got, "$EDITOR") {
		t.Fatalf("raw script view should be read-only, got:\n%s", got)
	}
	m.handleWorkflowRawScriptKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.workflowLaunch.rawScroll == 0 {
		t.Fatalf("expected raw script view to scroll")
	}
	m.handleWorkflowRawScriptKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeWorkflowLaunch {
		t.Fatalf("mode = %v, want workflow launch", m.mode)
	}
}

func TestWorkflowRunLocalResultDoesNotRenderPlainLaunchNotice(t *testing.T) {
	m := model{
		assembler:           tuirender.NewAssembler(),
		mode:                modeChat,
		width:               100,
		height:              30,
		localSubmitCommands: []string{"/deep-research question"},
	}
	result := &protocol.LocalResult{
		Kind:      "workflow-run",
		Title:     "deep-research is running in background",
		PlainText: "Started the deep-research workflow in the background.\n\nOpen /workflows to watch progress and inspect details. I'll report back here when it completes.",
		Fields: []protocol.LocalResultField{
			{Label: "Status", Value: "async_launched"},
			{Label: "Run", Value: "run-123"},
			{Label: "Script", Value: "/tmp/run-123/script.js"},
		},
	}

	cmd := m.handleLocalSubmitResultEvent(protocol.Event{Status: "info", Text: result.PlainText, LocalResult: result})
	if cmd != nil {
		t.Fatal("did not expect command for workflow run launch notice")
	}
	if m.mode != modeChat {
		t.Fatalf("mode = %v, want chat", m.mode)
	}
	messages := m.chatMessages()
	messages = nonHeaderMessages(messages)
	if len(messages) != 1 {
		t.Fatalf("expected only command echo; workflow progress stays out of chat until workflow_result, got %+v", messages)
	}
	got := strings.Join(tuirender.ChatLines(messages, 120), "\n")
	last := messages[len(messages)-1]
	if last.Role != "you" || last.Kind != tuirender.KindText || last.Local != nil {
		t.Fatalf("workflow local result should not enter chat as assistant text, got %+v", last)
	}
	if !strings.Contains(got, "/deep-research question") {
		t.Fatalf("workflow command echo missing:\n%s", got)
	}
	if strings.Contains(got, "Started the deep-research workflow") || strings.Contains(got, "async_launched") || strings.Contains(got, "/tmp/run-123/script.js") {
		t.Fatalf("workflow run local result should not render transcript lifecycle text:\n%s", got)
	}
}

func TestWorkflowToolCallSuppressesReasoningOnlyFallback(t *testing.T) {
	m := model{
		assembler:            tuirender.NewAssembler(),
		mode:                 modeChat,
		width:                100,
		height:               30,
		busy:                 true,
		sawReasoningThisTurn: true,
	}

	m.handleToolCallEvent(protocol.Event{Kind: protocol.EventToolCall, ToolCallID: "workflow-1", ToolName: "workflow", Text: `workflow: {"name":"smoke-result"}`})
	m.handleTurnDone(protocol.Event{Kind: protocol.EventTurnDone})

	got := strings.Join(tuirender.ChatLines(m.transcript, 120), "\n")
	if strings.Contains(got, "Reasoning only") || strings.Contains(got, "did not produce a visible answer") {
		t.Fatalf("workflow launch should suppress reasoning-only fallback:\n%s", got)
	}
}

func TestWorkflowLaunchModalSuppressesReasoningOnlyFallback(t *testing.T) {
	m := model{
		assembler:            tuirender.NewAssembler(),
		mode:                 modeWorkflowLaunch,
		width:                100,
		height:               30,
		busy:                 true,
		sawReasoningThisTurn: true,
		workflowLaunch: struct {
			result    *protocol.LocalResult
			selected  int
			rawScroll int
		}{result: workflowLaunchTestResult()},
	}

	m.handleTurnDone(protocol.Event{Kind: protocol.EventTurnDone})

	if m.mode != modeWorkflowLaunch {
		t.Fatalf("workflow launch modal should remain open, got mode=%v", m.mode)
	}
	got := strings.Join(tuirender.ChatLines(m.transcript, 120), "\n")
	if strings.Contains(got, "Reasoning only") || strings.Contains(got, "did not produce a visible answer") {
		t.Fatalf("workflow launch modal should suppress reasoning-only fallback:\n%s", got)
	}
}

func nonHeaderMessages(messages []tuirender.UIMessage) []tuirender.UIMessage {
	out := messages[:0]
	for _, msg := range messages {
		if msg.Role == "header" {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func TestWorkflowEventsLocalResultDoesNotOpenPanel(t *testing.T) {
	m := model{
		assembler: tuirender.NewAssembler(),
		mode:      modeChat,
		width:     100,
		height:    30,
	}
	result := &protocol.LocalResult{
		Kind:      "workflow",
		Title:     "Workflow Events run-123",
		PlainText: "Workflow events\n\nrun: run-123\nstatus: completed\n\nevents:\n  - log: searched docs",
		Sections: []protocol.LocalResultSection{{
			Title: "Events",
			Fields: []protocol.LocalResultField{
				{Label: "log", Value: "searched docs"},
			},
		}},
	}

	cmd := m.handleLocalSubmitResultEvent(protocol.Event{Status: "info", Text: result.PlainText, LocalResult: result})
	if cmd != nil {
		t.Fatal("did not expect command for workflow events result")
	}
	if m.mode != modeChat {
		t.Fatalf("mode = %v, want chat", m.mode)
	}
	got := strings.Join(tuirender.ChatLines(m.transcript, 120), "\n")
	if !strings.Contains(got, "Workflow Events") || !strings.Contains(got, "searched docs") {
		t.Fatalf("workflow events should render in chat, got:\n%s", got)
	}
}

func TestWorkflowPanelModeRendersPanel(t *testing.T) {
	m := model{
		mode:   modeWorkflowPanel,
		width:  100,
		height: 30,
		workflowPanel: workflowPanelState{
			result: &protocol.LocalResult{
				Kind:      "workflows",
				Title:     "Dynamic workflows",
				PlainText: "Dynamic workflows",
				Sections: []protocol.LocalResultSection{{
					Title: "run-123",
					Fields: []protocol.LocalResultField{
						{Label: "Status", Value: "running"},
					},
				}},
			},
		},
	}

	got := m.renderBody(100, 24)
	if !strings.Contains(got, "Dynamic workflows") || !strings.Contains(got, "run-123") || !strings.Contains(got, "Live refreshes every second") {
		t.Fatalf("workflow panel mode did not render panel:\n%s", got)
	}
	if bottom := strings.Join(m.bottomPartsBeforeInput(100), "\n"); strings.Contains(bottom, "Dynamic workflows") {
		t.Fatalf("workflow panel should render in the main body only, got bottom panel:\n%s", bottom)
	}
}

func TestWorkflowPanelViewRendersWithoutChatContent(t *testing.T) {
	m := model{
		mode:   modeWorkflowPanel,
		page:   pageChat,
		width:  100,
		height: 30,
		workflowPanel: workflowPanelState{
			result: &protocol.LocalResult{
				Kind:      "workflows",
				Title:     "Dynamic workflows",
				PlainText: "Dynamic workflows",
				Sections: []protocol.LocalResultSection{{
					Title: "run-123",
					Fields: []protocol.LocalResultField{
						{Label: "Status", Value: "running"},
					},
				}},
			},
		},
	}

	got := m.View()
	if !strings.Contains(got, "Dynamic workflows") || !strings.Contains(got, "run-123") {
		t.Fatalf("workflow panel view should render even with an empty chat transcript:\n%s", got)
	}
}

func TestWorkflowPanelSnapshotRefreshDoesNotRenderChatLifecycle(t *testing.T) {
	m := model{
		assembler: tuirender.NewAssembler(),
		mode:      modeWorkflowPanel,
		page:      pageChat,
		width:     100,
		height:    30,
		workflowPanel: workflowPanelState{
			runID: "run-1",
			result: &protocol.LocalResult{
				Kind: "workflow",
				WorkflowPanelSnapshot: &protocol.WorkflowPanelSnapshot{
					RunID:   "run-1",
					Status:  "running",
					Summary: "old summary",
				},
			},
		},
	}
	m.append("think", "The user wants me to run the workflow.")

	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:          protocol.EventWorkflowSnapshot,
		WorkflowRunID: "run-1",
		Status:        "completed",
		LocalResult: &protocol.LocalResult{
			Kind: "workflow",
			WorkflowPanelSnapshot: &protocol.WorkflowPanelSnapshot{
				RunID:   "run-1",
				Status:  "completed",
				Summary: "workflow completed",
			},
		},
	}))
	m = next.(model)

	chat := strings.Join(tuirender.ChatLines(m.chatMessages(), 100), "\n")
	for _, notWant := range []string{"Reasoning only", "Workflow · run-1", "workflow completed"} {
		if strings.Contains(chat, notWant) {
			t.Fatalf("workflow panel snapshot refresh should not render chat lifecycle %q:\n%s", notWant, chat)
		}
	}
	snapshot := workflowPanelSnapshot(m.workflowPanel.result)
	if snapshot == nil || snapshot.Status != "completed" || snapshot.Summary != "workflow completed" {
		t.Fatalf("workflow panel snapshot should update panel state, got %+v", snapshot)
	}
}

func TestWorkflowPanelUsesSemanticColorSegments(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	if got := workflowPanelStatusStyle("completed").GetForeground(); got != tuitheme.Default.Success {
		t.Fatalf("completed status color: want %s, got %s", tuitheme.Default.Success, got)
	}
	if got := workflowPanelStatusStyle("running").GetForeground(); got != tuitheme.Default.InfoSoft {
		t.Fatalf("running status color: want %s, got %s", tuitheme.Default.InfoSoft, got)
	}
	if got := workflowPanelStatusStyle("failed").GetForeground(); got != tuitheme.Default.Error {
		t.Fatalf("failed status color: want %s, got %s", tuitheme.Default.Error, got)
	}

	m := model{width: 120, height: 32}
	snapshot := &protocol.WorkflowPanelSnapshot{
		RunID:     "run-themed",
		Status:    "running",
		Summary:   "short summary",
		ElapsedMS: 42_000,
		Phases: []protocol.WorkflowPanelPhase{{
			Name:   "Search",
			Status: "running",
			Done:   1,
			Total:  2,
			Tasks: []protocol.WorkflowPanelTask{{
				ID:               "task-1",
				Label:            "search:docs",
				Status:           "completed",
				Model:            "deepseek",
				CompletionTokens: 3800,
				ToolCalls:        4,
				DurationMS:       11_000,
				Outcome:          "found official docs",
			}},
		}},
	}

	rendered := strings.Join(m.renderWorkflowPanelSnapshot(snapshot), "\n")
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected workflow panel to include ANSI color segments:\n%s", rendered)
	}
	plain := xansi.Strip(rendered)
	for _, want := range []string{"run-themed", "running", "Search", "search:docs", "3.8k out", "4 tools", "11s"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected stripped workflow panel to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestWorkflowPanelLongRunningSnapshotStaysWithinTerminalWidth(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := model{width: 100, height: 32}
	snapshot := issue251WorkflowPanelSnapshot()

	assertWorkflowPanelLinesWithinWidth(t, strings.Join(m.renderWorkflowPanelSnapshot(snapshot), "\n"), 100)

	m.workflowPanel.detail = true
	m.workflowPanel.focus = workflowPanelFocusTask
	m.workflowPanel.detailRight = true
	m.workflowPanel.detailSection = workflowPanelDetailOutcome
	assertWorkflowPanelLinesWithinWidth(t, strings.Join(m.renderWorkflowPanelTaskDetail(snapshot), "\n"), 100)

	m.workflowPanel.detailExpanded = true
	m.workflowPanel.expandedSection = workflowPanelDetailOutcome
	assertWorkflowPanelLinesWithinWidth(t, strings.Join(m.renderWorkflowPanelTaskDetail(snapshot), "\n"), 100)
}

func assertWorkflowPanelLinesWithinWidth(t *testing.T, rendered string, maxWidth int) {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		if width := xansi.StringWidth(line); width > maxWidth {
			t.Fatalf("rendered line width = %d, want <= %d:\n%s\n\nfull render:\n%s", width, maxWidth, line, rendered)
		}
	}
}

func issue251WorkflowPanelSnapshot() *protocol.WorkflowPanelSnapshot {
	return &protocol.WorkflowPanelSnapshot{
		RunID:     "run-4d98df11-5167-443f-b17a-7d8bbf5642d5",
		Status:    "running",
		Summary:   "Batch enrichment of Base from Trash/notes. 1 file = 1 sub-agent, rg-dedup, markup validation, INDEX.md.",
		ElapsedMS: 55_000,
		Phases: []protocol.WorkflowPanelPhase{
			{
				Name:    "Preparation",
				Status:  "running",
				Done:    2,
				Running: 1,
				Total:   3,
				Tasks: []protocol.WorkflowPanelTask{
					{
						ID:               "preparer",
						Label:            "preparer",
						Status:           "completed",
						CompletionTokens: 4700,
						ToolCalls:        4,
						DurationMS:       47_000,
						Outcome:          "## Preparer result\n\n**Structured output successfully sent.**\n\n### What was done\nCollected Trash/notes candidates, normalized Base references, and prepared a long validation checklist that should not break table borders.",
					},
					{
						ID:               "ensure-pending-review-dir",
						Label:            "ensure-pending-review-dir",
						Status:           "completed",
						CompletionTokens: 210,
						ToolCalls:        1,
						DurationMS:       4_000,
						Outcome:          "**Result:** Directory `Trash/pending-review/` successfully created with validation notes and follow-up instructions.",
					},
					{
						ID:      "get-today-date",
						Label:   "get-today-date",
						Status:  "running",
						Message: "Used shell_run",
					},
				},
			},
			{Name: "Enrichment", Status: "queued"},
			{Name: "Report", Status: "queued"},
		},
	}
}

func workflowLaunchTestResult() *protocol.LocalResult {
	return &protocol.LocalResult{
		Kind:      "workflow-launch",
		Title:     "Run a dynamic workflow?",
		PlainText: "Workflow(dynamic workflow: deep-research)\n\nRun a dynamic workflow?\n\nDeep research harness",
		Fields: []protocol.LocalResultField{
			{Label: "Workflow", Value: "deep-research"},
			{Label: "Args", Value: "question"},
		},
		Sections: []protocol.LocalResultSection{{
			Title: "Phases",
			Fields: []protocol.LocalResultField{
				{Label: "1. Scope", Value: "Decompose question"},
			},
		}, {
			Title: "Raw script",
			Fields: []protocol.LocalResultField{
				{Label: "Source", Value: "builtin"},
				{Label: "Path", Value: "builtin:deep-research"},
				{Label: "Script", Value: strings.Join([]string{
					"export const meta = {",
					"  name: 'deep-research',",
					"}",
					"phase('Scope')",
					"await agent('scope')",
					"phase('Search')",
					"await agent('search')",
					"phase('Synthesize')",
					"return {}",
				}, "\n")},
			},
		}},
		Actions: []protocol.LocalResultAction{
			{Label: "Yes, run it", WorkflowName: "deep-research", WorkflowArgs: "question", WorkflowResume: "run-source"},
			{Label: "View raw script"},
			{Label: "No"},
		},
	}
}
func TestWorkflowLaunchNavigationWrapsAround(t *testing.T) {
	m := model{
		assembler: tuirender.NewAssembler(),
		mode:      modeWorkflowLaunch,
		width:     100,
		height:    30,
		workflowLaunch: struct {
			result    *protocol.LocalResult
			selected  int
			rawScroll int
		}{result: workflowLaunchTestResult()},
	}
	actions := workflowLaunchActions(m.workflowLaunch.result)
	if len(actions) < 2 {
		t.Fatalf("expected at least two launch actions, got %d", len(actions))
	}

	// Up from the first action wraps to the last.
	m.handleWorkflowLaunchKey(tea.KeyMsg{Type: tea.KeyUp})
	if m.workflowLaunch.selected != len(actions)-1 {
		t.Fatalf("expected up at first to wrap to last, got %d", m.workflowLaunch.selected)
	}

	// Down from the last action wraps back to the first.
	m.handleWorkflowLaunchKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.workflowLaunch.selected != 0 {
		t.Fatalf("expected down at last to wrap to first, got %d", m.workflowLaunch.selected)
	}
}
