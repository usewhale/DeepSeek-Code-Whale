package tui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
	"strings"
	"testing"
	"time"
)

func TestTurnDoneSkipsDurationNoticeWhileStopping(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 80, height: 24, busy: true, stopping: true}
	m.busySince = time.Now().Add(-2 * time.Minute)

	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:         protocol.EventTurnDone,
		LastResponse: "done",
		Metadata:     agentTurnMetadata(),
	}))
	m = next.(model)

	got := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	if strings.Contains(got, "Worked for") {
		t.Fatalf("did not expect turn duration notice for stopped turn:\n%s", got)
	}
}
func TestAppendTurnDurationNoticeThresholdAndBusyState(t *testing.T) {
	m := model{width: 80, height: 24, viewportFrozen: true}
	m.appendTurnDurationNotice(true, false, 30*time.Second)
	got := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	if !strings.Contains(got, "✻ Worked for 30s") {
		t.Fatalf("expected duration notice at threshold:\n%s", got)
	}

	m = model{width: 80, height: 24, viewportFrozen: true}
	m.appendTurnDurationNotice(true, false, 29*time.Second)
	if len(m.transcript) != 0 {
		t.Fatalf("did not expect duration notice below threshold, got %+v", m.transcript)
	}

	m = model{width: 80, height: 24, viewportFrozen: true}
	m.appendTurnDurationNotice(false, false, 2*time.Minute)
	if len(m.transcript) != 0 {
		t.Fatalf("did not expect duration notice when turn was not busy, got %+v", m.transcript)
	}
}
func TestCtrlCClearsNonEmptyInputWithoutArmingQuit(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("draft")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if cmd != nil {
		t.Fatalf("expected no command when clearing input, got %T", cmd)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared, got %q", got)
	}
	if !m.quitArmedUntil.IsZero() {
		t.Fatal("expected ctrl+c on non-empty input not to arm quit")
	}
	if m.status != "input cleared" {
		t.Fatalf("unexpected status: %q", m.status)
	}
}
func TestSecondIdleCtrlCRequestsExitFlow(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.quitArmedUntil = time.Now().Add(time.Second)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)

	if len(*intents) != 1 {
		t.Fatalf("expected one intent, got %+v", *intents)
	}
	if (*intents)[0].Kind != protocol.IntentRequestExit {
		t.Fatalf("expected exit request intent, got %+v", (*intents)[0])
	}
	if !m.quitArmedUntil.IsZero() || m.status != "exiting" {
		t.Fatalf("unexpected quit state/status: %v %q", m.quitArmedUntil, m.status)
	}
}

func TestEnterSubmitUsesComposerValueAndClearsInputState(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("  $code-review inspect this diff  ")
	m.slash.selected = 1
	m.slash.argumentHint = "stale hint"
	m.files.active = true
	m.files.selected = 1
	m.skills.selected = 1
	m.skillBinding = &protocol.SkillBinding{Name: "code-review", SkillFilePath: "/tmp/code-review/SKILL.md"}
	m.promptHistory = []string{"older prompt"}
	m.historyIndex = 0
	m.historyDraft = "draft"
	m.lastHistoryText = "older prompt"
	m.inHistoryNav = true

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	got := (*intents)[0]
	if got.Kind != protocol.IntentSubmit || got.Input != "$code-review inspect this diff" {
		t.Fatalf("expected submit payload from composer value, got %+v", got)
	}
	if got.SkillBinding == nil || got.SkillBinding.Name != "code-review" || got.SkillBinding.SkillFilePath != "/tmp/code-review/SKILL.md" {
		t.Fatalf("expected current skill binding to be submitted, got %+v", got.SkillBinding)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected composer cleared after submit, got %q", got)
	}
	if m.skillBinding != nil {
		t.Fatalf("expected model skill binding cleared after submit, got %+v", m.skillBinding)
	}
	if len(m.slash.matches) != 0 || m.slash.selected != 0 || m.slash.argumentHint != "" {
		t.Fatalf("expected slash state cleared, got matches=%+v selected=%d hint=%q", m.slash.matches, m.slash.selected, m.slash.argumentHint)
	}
	if m.files.active || len(m.files.matches) != 0 || m.files.selected != 0 {
		t.Fatalf("expected file suggestions cleared, got active=%v matches=%+v selected=%d", m.files.active, m.files.matches, m.files.selected)
	}
	if len(m.skills.matches) != 0 {
		t.Fatalf("expected skill suggestions cleared, got matches=%+v selected=%d", m.skills.matches, m.skills.selected)
	}
	if m.historyIndex != -1 || m.historyDraft != "" || m.lastHistoryText != "" || m.inHistoryNav {
		t.Fatalf("expected history navigation reset, index=%d draft=%q last=%q active=%v", m.historyIndex, m.historyDraft, m.lastHistoryText, m.inHistoryNav)
	}
	if len(m.promptHistory) != 2 || m.promptHistory[1] != "$code-review inspect this diff" {
		t.Fatalf("expected trimmed prompt recorded in history, got %+v", m.promptHistory)
	}
	if !m.busy || m.status != "running" {
		t.Fatalf("expected submit to start busy turn, busy=%v status=%q", m.busy, m.status)
	}
}

func TestEnterWhileBusyQueuesInputWithoutSubmitting(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.input.SetValue("follow up while working")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared after queueing, got %q", got)
	}
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "follow up while working" {
		t.Fatalf("expected queued prompt, got %+v", m.queuedPrompts)
	}
	if len(m.pendingSteers) != 0 {
		t.Fatalf("expected no pending steers, got %+v", m.pendingSteers)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no submitted intent while busy, got %+v", *intents)
	}
	if !m.busy {
		t.Fatal("expected turn to remain busy")
	}
	if m.status != "queued (1)" {
		t.Fatalf("unexpected status: %q", m.status)
	}
	if got := strings.Join(tuirender.ChatLines(m.assembler.Snapshot(), 80), "\n"); strings.Contains(got, "follow up while working") {
		t.Fatalf("queued prompt should not be written to live transcript:\n%s", got)
	}
}

func TestTabWhileBusyDoesNotQueueFollowUp(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.input.SetValue("follow up later")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)

	if len(*intents) != 0 {
		t.Fatalf("expected no submitted intent for busy Tab, got %+v", *intents)
	}
	if len(m.pendingSteers) != 0 {
		t.Fatalf("expected no pending steers for busy Tab, got %+v", m.pendingSteers)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected no queued follow-up for busy Tab, got %+v", m.queuedPrompts)
	}
	if got := m.input.Value(); got != "follow up later" {
		t.Fatalf("expected input preserved after busy Tab, got %q", got)
	}
}

func TestPendingInputAcceptedAndRejectedEventsUpdateSteerState(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.pendingSteers = []pendingSteer{{ID: "pending-1", Text: "accepted"}, {ID: "pending-2", Text: "rejected"}}
	m.input.SetValue("current draft")

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventPendingInputAccepted, ClientInputID: "pending-1"}))
	m = next.(model)
	if !m.pendingSteers[0].Accepted {
		t.Fatalf("expected first pending steer accepted, got %+v", m.pendingSteers)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventPendingInputRejected, ClientInputID: "pending-2"}))
	m = next.(model)
	if len(m.pendingSteers) != 1 || m.pendingSteers[0].ID != "pending-1" {
		t.Fatalf("expected rejected steer removed, got %+v", m.pendingSteers)
	}
	if got := m.input.Value(); got != "rejected\ncurrent draft" {
		t.Fatalf("expected rejected steer restored before draft, got %q", got)
	}
}
func TestDiffPageCtrlCInterruptsBusyTurn(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.runtime = &testRuntime{}
	m.width = 80
	m.height = 24
	m.busy = true
	m.page = pageDiff

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if m.page != pageDiff {
		t.Fatalf("ctrl+c interrupt should not close diff page, got %v", m.page)
	}
	if !m.busy || !m.stopping || m.status != "stopping" {
		t.Fatalf("expected busy diff page ctrl+c to interrupt, busy=%v stopping=%v status=%q", m.busy, m.stopping, m.status)
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentShutdown {
		t.Fatalf("expected shutdown intent from diff page ctrl+c, got %+v", *intents)
	}
}
func TestBusyQueuedPromptWaitsForPendingLocalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.status = "running"
	m.input.SetValue("/stats all")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != "/stats all" {
		t.Fatalf("expected busy local submit dispatch, got %+v", *intents)
	}

	m.input.SetValue("queued after stats")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "queued after stats" {
		t.Fatalf("expected prompt to queue while busy local submit is pending, got %+v", m.queuedPrompts)
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:         protocol.EventTurnDone,
		LastResponse: "done",
		Metadata:     map[string]any{protocol.EventMetadataAgentTurn: true},
	}))
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("queued prompt should not start before local submit done, got %+v", *intents)
	}
	if !strings.Contains(m.status, "command") {
		t.Fatalf("expected wait status while local submit is pending, got %q", m.status)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventLocalSubmitDone}))
	m = next.(model)
	if len(*intents) != 2 || (*intents)[1].Kind != protocol.IntentSubmit || (*intents)[1].Input != "queued after stats" {
		t.Fatalf("expected queued prompt to start after local submit done, got %+v", *intents)
	}
	if !m.busy {
		t.Fatal("expected queued prompt to start a turn")
	}
}
func TestTurnDoneShowsWaitStatusWhileLocalSubmitPendingWithoutQueue(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 80, height: 14, busy: true, status: "running", localSubmitPending: 1}

	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:         protocol.EventTurnDone,
		LastResponse: "done",
		Metadata:     map[string]any{protocol.EventMetadataAgentTurn: true},
	}))
	m = next.(model)

	if m.busy {
		t.Fatal("expected turn completion to clear busy state")
	}
	if m.status != "wait for command to finish" {
		t.Fatalf("expected wait status while local submit remains pending, got %q", m.status)
	}
}
func TestQueuedPromptWaitsForAllPendingLocalSubmits(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.localSubmitPending = 2
	m.status = "wait for command to finish"
	m.queuedPrompts = []queuedPrompt{{Text: "after two locals"}}

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventLocalSubmitDone}))
	m = next.(model)
	if m.localSubmitPending != 1 {
		t.Fatalf("expected one pending local submit left, got %d", m.localSubmitPending)
	}
	if len(*intents) != 0 {
		t.Fatalf("queued prompt should not start before all local submits finish, got %+v", *intents)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventLocalSubmitDone}))
	m = next.(model)
	if m.localSubmitPending != 0 {
		t.Fatalf("expected all local submits cleared, got %d", m.localSubmitPending)
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "after two locals" {
		t.Fatalf("expected queued prompt after final local submit done, got %+v", *intents)
	}
	if !m.busy {
		t.Fatal("expected queued prompt to start a turn")
	}
}
func TestLocalSubmitBarrierBlocksPromptUntilDone(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/new scratch")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != "/new scratch" {
		t.Fatalf("expected /new local submit intent, got %+v", *intents)
	}
	if m.localSubmitPending != 1 {
		t.Fatal("expected mutating local submit to block later prompts")
	}
	if m.busy {
		t.Fatal("local submit barrier should not start working state")
	}

	m.input.SetValue("start a turn")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected prompt not to dispatch before local submit done, got %+v", *intents)
	}
	if got := m.input.Value(); got != "start a turn" {
		t.Fatalf("expected prompt to remain editable, got %q", got)
	}
	if m.status != "wait for command to finish" {
		t.Fatalf("expected wait status while local submit is pending, got %q", m.status)
	}
	if m.busy {
		t.Fatal("prompt should not start a turn while local submit is pending")
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventLocalSubmitDone, Metadata: map[string]any{protocol.EventMetadataLocalSubmit: true}}))
	m = next.(model)
	if m.localSubmitPending != 0 {
		t.Fatal("expected local submit barrier to clear after done event")
	}
	if m.status != "ready" {
		t.Fatalf("expected wait status to clear after local submit done, got %q", m.status)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 2 || (*intents)[1].Kind != protocol.IntentSubmit || (*intents)[1].Input != "start a turn" {
		t.Fatalf("expected prompt to dispatch after local submit done, got %+v", *intents)
	}
	if !m.busy {
		t.Fatal("expected prompt to start a turn after local submit done")
	}
}
func TestReadOnlyLocalSubmitBlocksPromptUntilDone(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	m.input.SetValue("/stats all")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != "/stats all" {
		t.Fatalf("expected /stats all local submit intent, got %+v", *intents)
	}
	if m.localSubmitPending != 1 {
		t.Fatal("expected idle read-only local submit to block later prompts")
	}
	if m.busy {
		t.Fatal("read-only local submit should not start working state")
	}

	m.input.SetValue("start a turn")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected prompt not to overtake local submit, got %+v", *intents)
	}
	if got := m.input.Value(); got != "start a turn" {
		t.Fatalf("expected prompt to remain editable, got %q", got)
	}
	if m.status != "wait for command to finish" {
		t.Fatalf("expected wait status while read-only local submit is pending, got %q", m.status)
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "Stats\n\nslow usage summary",
	}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind: protocol.EventLocalSubmitDone,
		Metadata: map[string]any{
			protocol.EventMetadataLocalSubmit: true,
		},
	}))
	m = next.(model)
	if m.localSubmitPending != 0 {
		t.Fatal("expected read-only local submit to clear after done event")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 2 || (*intents)[1].Kind != protocol.IntentSubmit || (*intents)[1].Input != "start a turn" {
		t.Fatalf("expected prompt to dispatch after read-only local submit done, got %+v", *intents)
	}

	rendered := strings.Join(tuirender.ChatLines(m.chatMessages(), 80), "\n")
	statsIx := strings.Index(rendered, "slow usage summary")
	promptIx := strings.Index(rendered, "start a turn")
	if statsIx < 0 || promptIx < 0 || statsIx > promptIx {
		t.Fatalf("expected local result before later prompt:\n%s", rendered)
	}
}
func TestEnterWhileBusyWithEmptyInputDoesNotAppendPersistentNotice(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.status = "running"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected no queued prompt, got %+v", m.queuedPrompts)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no submitted intent while busy, got %+v", *intents)
	}
	if !m.busy {
		t.Fatal("expected turn to remain busy")
	}
	if m.status != "running" {
		t.Fatalf("unexpected status: %q", m.status)
	}
	if got := strings.Join(tuirender.ChatLines(m.assembler.Snapshot(), 80), "\n"); strings.Contains(got, "Agent is working") {
		t.Fatalf("empty enter while busy should not write a persistent notice:\n%s", got)
	}
}
func TestTurnDoneSubmitsOneQueuedPrompt(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.queuedPrompts = []queuedPrompt{{Text: "first queued"}, {Text: "second queued"}}

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)

	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "first queued" {
		t.Fatalf("expected first queued prompt submitted, got %+v", *intents)
	}
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "second queued" {
		t.Fatalf("expected one queued prompt left, got %+v", m.queuedPrompts)
	}
	if !m.busy || m.status != "running" {
		t.Fatalf("expected queued turn running, busy=%v status=%q", m.busy, m.status)
	}
	if got := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n"); !strings.Contains(got, "first queued") {
		t.Fatalf("expected submitted queued prompt in transcript:\n%s", got)
	}
}
func TestQueuedPromptsSubmitFIFOAcrossTurns(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.queuedPrompts = []queuedPrompt{{Text: "first queued"}, {Text: "second queued"}}

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)

	if len(*intents) != 2 {
		t.Fatalf("expected two submitted intents, got %+v", *intents)
	}
	if (*intents)[0].Input != "first queued" || (*intents)[1].Input != "second queued" {
		t.Fatalf("expected FIFO submit order, got %+v", *intents)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected queue drained, got %+v", m.queuedPrompts)
	}
	if !m.busy {
		t.Fatal("expected second queued turn to be running")
	}
}
func TestStoppingTurnDoneRestoresQueuedPromptsToComposer(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.stopping = true
	m.queuedPrompts = []queuedPrompt{{Text: "first queued"}, {Text: "second queued"}}
	m.input.SetValue("current draft")

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)

	if len(*intents) != 0 {
		t.Fatalf("expected no submitted intents after stopping, got %+v", *intents)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected queue restored and cleared, got %+v", m.queuedPrompts)
	}
	if got := m.input.Value(); got != "first queued\nsecond queued\ncurrent draft" {
		t.Fatalf("expected queued prompts restored to composer, got %q", got)
	}
	if m.busy || m.stopping {
		t.Fatalf("expected stopped turn idle, busy=%v stopping=%v", m.busy, m.stopping)
	}
}
func TestTurnInterruptedNoticeText(t *testing.T) {
	m := newModel(nil, "", "", "")
	got := m.turnInterruptedNoticeText()
	if !strings.Contains(got, "Conversation interrupted") {
		t.Fatalf("unexpected interrupt notice: %q", got)
	}
}

func TestTurnInterruptedForQueuedPromptNoticeText(t *testing.T) {
	m := newModel(nil, "", "", "")
	got := m.turnInterruptedForQueuedPromptNoticeText()
	if !strings.Contains(got, "queued follow-up") {
		t.Fatalf("unexpected queued interrupt notice: %q", got)
	}
	if strings.Contains(got, "tell the model what to do differently") {
		t.Fatalf("queued interrupt notice should not use generic guidance: %q", got)
	}
}
func TestEscWhileBusyKeepsTurnBusyUntilTurnDone(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 80, height: 24, busy: true}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !m.busy {
		t.Fatal("expected interrupted turn to remain busy until EventTurnDone")
	}
	if !m.stopping {
		t.Fatal("expected stopping state after interrupt")
	}
	if m.status != "stopping" {
		t.Fatalf("unexpected status: %q", m.status)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.busy || !m.stopping {
		t.Fatalf("enter during stopping should not start another turn, busy=%v stopping=%v", m.busy, m.stopping)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if m.busy || m.stopping {
		t.Fatalf("expected turn done to clear busy/stopping, busy=%v stopping=%v", m.busy, m.stopping)
	}
}

func TestEscWithQueuedPromptSubmitsItAfterInterrupt(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.runtime = &testRuntime{}
	m.busy = true
	m.queuedPrompts = []queuedPrompt{{Text: "queued draft"}}
	m.input.SetValue("current draft")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !m.submitQueuedPromptAfterInterrupt {
		t.Fatal("expected Esc with queued prompt to request immediate submit after interrupt")
	}
	renderedAfterEsc := strings.Join(tuirender.ChatLines(m.chatMessages(), 80), "\n")
	if strings.Contains(renderedAfterEsc, "tell the model what to do differently") {
		t.Fatalf("queued Esc interrupt should not show generic interruption guidance:\n%s", renderedAfterEsc)
	}
	if !strings.Contains(renderedAfterEsc, "Interrupted to submit queued follow-up") {
		t.Fatalf("queued Esc interrupt should show queued follow-up notice:\n%s", renderedAfterEsc)
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentShutdown || (*intents)[0].InterruptSource != "esc" {
		t.Fatalf("expected shutdown intent from Esc, got %+v", *intents)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if len(*intents) != 2 || (*intents)[1].Kind != protocol.IntentSubmit || (*intents)[1].Input != "queued draft" {
		t.Fatalf("expected queued prompt to submit after interrupt, got %+v", *intents)
	}
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "current draft" {
		t.Fatalf("expected current draft to remain queued, got %+v", m.queuedPrompts)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected current draft moved into queue, got %q", got)
	}
	if !m.busy || m.stopping {
		t.Fatalf("expected queued prompt turn running, busy=%v stopping=%v", m.busy, m.stopping)
	}
}

func TestEscWithDraftQueuesAndSubmitsItAfterInterrupt(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.runtime = &testRuntime{}
	m.busy = true
	m.input.SetValue("send after interrupt")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !m.submitQueuedPromptAfterInterrupt {
		t.Fatal("expected Esc with draft to queue and request immediate submit")
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentShutdown || (*intents)[0].InterruptSource != "esc" {
		t.Fatalf("expected shutdown intent from Esc, got %+v", *intents)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if len(*intents) != 2 || (*intents)[1].Kind != protocol.IntentSubmit || (*intents)[1].Input != "send after interrupt" {
		t.Fatalf("expected draft to submit after interrupt, got %+v", *intents)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected submitted draft to clear composer, got %q", got)
	}
}
func TestEscInterruptDuringThinkingDoesNotShowReasoningOnly(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 80, height: 24, busy: true}

	// Simulate receiving reasoning (thinking) content
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventReasoningDelta, Text: "thinking..."}))
	m = next.(model)

	// User presses Esc to interrupt
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !m.stopping {
		t.Fatal("expected stopping state after Esc interrupt")
	}

	// Stream ends
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if m.busy || m.stopping {
		t.Fatalf("expected turn done to clear state, busy=%v stopping=%v", m.busy, m.stopping)
	}

	rendered := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	if !strings.Contains(rendered, "Conversation interrupted") {
		t.Fatalf("expected interrupted notice in transcript:\n%s", rendered)
	}
	if strings.Contains(rendered, "Reasoning only") || strings.Contains(rendered, "did not produce a visible answer") {
		t.Fatalf("should not show reasoning-only message after intentional Esc interrupt:\n%s", rendered)
	}
}
func TestCtrlCWhileBusyInterruptsWithoutArmingQuit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.runtime = &testRuntime{}
	m.width = 80
	m.height = 24
	m.busy = true
	m.quitArmedUntil = time.Now().Add(time.Second)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if !m.busy {
		t.Fatal("expected interrupted turn to remain busy until EventTurnDone")
	}
	if !m.stopping {
		t.Fatal("expected stopping state after ctrl+c interrupt")
	}
	if m.status != "stopping" {
		t.Fatalf("unexpected status: %q", m.status)
	}
	if !m.quitArmedUntil.IsZero() {
		t.Fatal("expected ctrl+c while busy not to arm quit")
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentShutdown || (*intents)[0].InterruptSource != "ctrl+c" {
		t.Fatalf("expected ctrl+c while busy to dispatch shutdown intent, got %+v", *intents)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if !m.quitArmedUntil.IsZero() {
		t.Fatal("expected repeated ctrl+c while stopping not to arm quit")
	}
	if len(*intents) != 1 {
		t.Fatalf("expected repeated ctrl+c while stopping not to dispatch another intent, got %+v", *intents)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if m.busy || m.stopping {
		t.Fatalf("expected turn done to clear busy/stopping, busy=%v stopping=%v", m.busy, m.stopping)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if m.quitArmedUntil.IsZero() {
		t.Fatal("expected first idle ctrl+c after interrupt to arm quit")
	}
	if len(*intents) != 1 {
		t.Fatalf("expected idle ctrl+c to avoid shutdown after stale arm was cleared, got %+v", *intents)
	}
}
func TestCtrlCWhileBusyInterruptsBeforeUserInputMode(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.runtime = &testRuntime{}
	m.width = 80
	m.height = 24
	m.busy = true
	m.mode = modeUserInput
	m.userInput.toolCallID = "tool-1"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)

	if !m.stopping {
		t.Fatal("expected ctrl+c in busy user-input mode to interrupt the turn")
	}
	if m.mode != modeChat {
		t.Fatalf("expected interrupt to leave user-input mode, got %v", m.mode)
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentShutdown {
		t.Fatalf("expected user input interrupt to dispatch shutdown only, got %+v", *intents)
	}
}
func TestEscWhileBusyUserInputInterruptsTurn(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.runtime = &testRuntime{}
	m.width = 80
	m.height = 24
	m.busy = true
	m.mode = modeUserInput
	m.userInput.toolCallID = "tool-1"
	m.userInput.questions = []protocol.UserInputQuestion{{
		Header:   "Scope",
		ID:       "scope",
		Question: "Continue?",
		Options:  []protocol.UserInputOption{{Label: "Yes", Description: "Proceed."}},
	}}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)

	if !m.stopping {
		t.Fatal("expected esc in busy user-input mode to interrupt the turn")
	}
	if m.mode != modeChat {
		t.Fatalf("expected interrupt to leave user-input mode, got %v", m.mode)
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentShutdown {
		t.Fatalf("expected esc user input interrupt to dispatch shutdown only, got %+v", *intents)
	}
}
func TestCtrlCWhileStoppingClearsBlockingModalWithoutDuplicateShutdown(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*model)
	}{
		{
			name: "approval",
			setup: func(m *model) {
				m.mode = modeApproval
				m.approval.toolCallID = "approval-queued"
			},
		},
		{
			name: "user input",
			setup: func(m *model) {
				m.mode = modeUserInput
				m.userInput.toolCallID = "input-queued"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, intents := newModelWithDispatchSpy()
			m.runtime = &testRuntime{}
			m.width = 80
			m.height = 24
			m.busy = true
			m.stopping = true
			m.status = "stopping"
			tt.setup(&m)

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			m = next.(model)

			if m.mode != modeChat {
				t.Fatalf("expected repeated ctrl+c while stopping to clear blocking mode, got %v", m.mode)
			}
			if !m.busy || !m.stopping {
				t.Fatalf("expected turn to keep stopping, busy=%v stopping=%v", m.busy, m.stopping)
			}
			if len(*intents) != 0 {
				t.Fatalf("expected no duplicate shutdown or modal intent while already stopping, got %+v", *intents)
			}
		})
	}
}
func TestStaleBlockingEventsWhileStoppingDoNotOpenModals(t *testing.T) {
	tests := []struct {
		name       string
		ev         protocol.Event
		wantIntent protocol.IntentKind
	}{
		{
			name: "approval",
			ev: protocol.Event{
				Kind:       protocol.EventApprovalRequired,
				ToolCallID: "approval-stale",
				ToolName:   "shell_run",
				Text:       "shell_run: sleep 30",
			},
			wantIntent: protocol.IntentCancelToolApproval,
		},
		{
			name: "user input",
			ev: protocol.Event{
				Kind:       protocol.EventUserInputRequired,
				ToolCallID: "input-stale",
				ToolName:   "request_user_input",
				Questions: []protocol.UserInputQuestion{{
					Header:   "Choice",
					ID:       "choice",
					Question: "Continue?",
				}},
			},
			wantIntent: protocol.IntentCancelUserInput,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, intents := newModelWithDispatchSpy()
			m.busy = true
			m.stopping = true
			m.mode = modeChat
			m.status = "stopping"

			next, _ := m.Update(svcMsg(tt.ev))
			m = next.(model)

			if m.mode != modeChat {
				t.Fatalf("expected stale event while stopping to leave chat mode, got %v", m.mode)
			}
			if m.status != "stopping" {
				t.Fatalf("expected stale event while stopping to preserve status, got %q", m.status)
			}
			if len(*intents) != 1 {
				t.Fatalf("expected stale event while stopping to resolve interaction, got %+v", *intents)
			}
			if got := (*intents)[0]; got.Kind != tt.wantIntent || got.ToolCallID != tt.ev.ToolCallID {
				t.Fatalf("unexpected stale interaction intent: %+v", got)
			}
		})
	}
}
func TestTurnDoneClearsStaleBlockingModal(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*model)
	}{
		{
			name: "approval",
			setup: func(m *model) {
				m.mode = modeApproval
				m.approval.toolCallID = "approval-stale"
			},
		},
		{
			name: "user input",
			setup: func(m *model) {
				m.mode = modeUserInput
				m.userInput.toolCallID = "input-stale"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(nil, "", "", "")
			m.busy = true
			m.stopping = true
			m.status = "stopping"
			tt.setup(&m)

			next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
			m = next.(model)

			if m.mode != modeChat {
				t.Fatalf("expected turn done to clear stale blocking mode, got %v", m.mode)
			}
			if m.busy || m.stopping {
				t.Fatalf("expected turn done to clear busy/stopping, busy=%v stopping=%v", m.busy, m.stopping)
			}
			if m.status != "ready" {
				t.Fatalf("expected ready status after turn done, got %q", m.status)
			}
		})
	}
}
func TestBusyStatusLocalResultRendersAsStructuredLiveEntry(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	m.busy = true

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventAssistantDelta, Text: "working"}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "Status\n\n- session: test-session\n- mode: agent",
		LocalResult: &protocol.LocalResult{
			Kind:      "status",
			Title:     "Status",
			PlainText: "Status\n\n- session: test-session\n- mode: agent",
			Fields: []protocol.LocalResultField{
				{Label: "Session", Value: "test-session"},
				{Label: "Mode", Value: "agent", Tone: "info"},
			},
		},
	}))
	m = next.(model)

	snap := m.assembler.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected assistant and local status in live assembler, got %+v", snap)
	}
	if got := snap[1]; got.Kind != tuirender.KindLocalStatus || got.Role != "local_status" || got.Local == nil {
		t.Fatalf("expected structured local status live entry, got role=%q kind=%q local=%+v", got.Role, got.Kind, got.Local)
	}
	rendered := strings.Join(tuirender.ChatLines(m.chatMessages(), 80), "\n")
	workingIx := strings.Index(rendered, "working")
	statusIx := strings.Index(rendered, "test-session")
	if workingIx < 0 || statusIx < 0 || workingIx > statusIx {
		t.Fatalf("expected live assistant output before structured status card:\n%s", rendered)
	}
}
func TestBusyMCPLocalResultRendersAsStructuredLiveEntry(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	m.busy = true

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventAssistantDelta, Text: "working"}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "MCP\n\nconfig: /tmp/mcp.json\nservers: 1",
		LocalResult: &protocol.LocalResult{
			Kind:      "mcp",
			Title:     "MCP",
			PlainText: "MCP\n\nconfig: /tmp/mcp.json\nservers: 1",
			Fields: []protocol.LocalResultField{
				{Label: "Config", Value: "/tmp/mcp.json"},
				{Label: "Servers", Value: "1", Tone: "info"},
			},
		},
	}))
	m = next.(model)

	snap := m.assembler.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected assistant and local mcp in live assembler, got %+v", snap)
	}
	if got := snap[1]; got.Kind != tuirender.KindLocalMCP || got.Role != "local_mcp" || got.Local == nil {
		t.Fatalf("expected structured local mcp live entry, got role=%q kind=%q local=%+v", got.Role, got.Kind, got.Local)
	}
	rendered := strings.Join(tuirender.ChatLines(m.chatMessages(), 80), "\n")
	workingIx := strings.Index(rendered, "working")
	mcpIx := strings.Index(rendered, "/tmp/mcp.json")
	if workingIx < 0 || mcpIx < 0 || workingIx > mcpIx {
		t.Fatalf("expected live assistant output before structured mcp card:\n%s", rendered)
	}
}
func TestBusyLocalCommandResultKeepsPendingToolCallLive(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30, busy: true}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-read",
		ToolName:   "read_file",
		Text:       `read_file: {"file_path":"internal/tui/model.go"}`,
	}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "Status\n\nsession: test-session",
	}))
	m = next.(model)
	if !m.hasPendingLifecycleItems() {
		t.Fatal("local result must not clear pending tool calls")
	}

	raw := `{"success":true,"data":{"status":"ok","metrics":{"returned_lines":24,"total_lines":100},"payload":{"file_path":"internal/tui/model.go","content":"package tui"}}}`
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-read",
		ToolName:   "read_file",
		Text:       raw,
	}))
	m = next.(model)

	if got := len(m.assembler.Snapshot()); got != 0 {
		t.Fatalf("expected completed tool call and local result to commit together, got %+v", m.assembler.Snapshot())
	}
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	if !strings.Contains(rendered, "Read internal/tui/model.go") || !strings.Contains(rendered, "session: test-session") {
		t.Fatalf("expected completed tool cell and local result in transcript:\n%s", rendered)
	}
	if strings.Contains(rendered, "Running internal/tui/model.go") {
		t.Fatalf("local result should not leave stale running tool cell:\n%s", rendered)
	}
}
func TestChatViewportFreezesLiveOutputWhenScrolledDuringBusy(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 10
	m.transcript = nil
	for i := 0; i < 40; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%02d", i))
	}
	m.beginTurnTranscript()
	m.startBusy()
	m.append("assistant", "live-head\n")
	m.refreshViewportContentFollow(true)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(model)
	if !m.viewportFrozen {
		t.Fatal("expected PageUp during busy output to freeze chat viewport")
	}
	if m.followTail {
		t.Fatal("expected PageUp to disable tail following")
	}
	frozenView := m.viewport.View()

	for i := 0; i < 30; i++ {
		m.append("assistant", fmt.Sprintf("live-tail-%02d\n", i))
	}
	if got := m.viewport.View(); got != frozenView {
		t.Fatalf("expected live deltas not to redraw scrolled viewport while frozen\nbefore:\n%s\n\nafter:\n%s", frozenView, got)
	}
	if strings.Contains(m.viewport.View(), "live-tail-29") {
		t.Fatalf("expected hidden live tail not to cover frozen viewport:\n%s", m.viewport.View())
	}
}
func TestChatViewportBusyFollowTailUsesTailRenderWindow(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 10
	m.transcript = nil
	for i := 0; i < 200; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%03d", i))
	}
	m.beginTurnTranscript()
	m.startBusy()
	m.append("assistant", "live-head\n")
	m.refreshViewportContentFollow(false)

	if lines := m.viewport.TotalLineCount(); lines > chatTailRenderLineFloor {
		t.Fatalf("expected busy tail-follow render to keep a bounded line window, got %d lines", lines)
	}
	view := m.View()
	if strings.Contains(view, "entry-000") {
		t.Fatalf("expected old transcript lines to be outside the busy tail render window:\n%s", view)
	}
	if !strings.Contains(view, "live-head") {
		t.Fatalf("expected live output to remain visible in tail render window:\n%s", view)
	}
}
func TestChatViewportBusyFollowTailKeepsSingleLargeLiveMessageScrollable(t *testing.T) {
	for _, height := range []int{8, 10, 20} {
		t.Run(fmt.Sprintf("height_%d", height), func(t *testing.T) {
			m := newModel(nil, "", "", "")
			m.width = 80
			m.height = height
			m.startupHeaderPrintCmd()
			m.transcript = nil
			m.beginTurnTranscript()
			m.startBusy()
			for i := 0; i < 400; i++ {
				m.append("assistant", fmt.Sprintf("single-live-%03d\n", i))
			}
			m.refreshViewportContentFollow(false)

			if lines := m.viewport.TotalLineCount(); lines <= m.viewportBodyHeight(m.width) {
				t.Fatalf("expected single coalesced live message to exceed viewport height, got %d lines", lines)
			}
			view := m.View()
			if !strings.Contains(view, "live-399") {
				t.Fatalf("expected single-message live tail to remain visible:\n%s", view)
			}
			m.handleViewportScrollKey("home")
			view = m.View()
			if !strings.Contains(view, "live-000") {
				t.Fatalf("expected full single-message live output to be scrollable to the top:\n%s", view)
			}
		})
	}
}
func TestChatViewportPageUpRestoresFullRenderWindowDuringBusy(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 10
	m.transcript = nil
	for i := 0; i < 200; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%03d", i))
	}
	m.beginTurnTranscript()
	m.startBusy()
	m.append("assistant", "live-head\n")
	m.refreshViewportContentFollow(false)
	tailLines := m.viewport.TotalLineCount()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(model)
	if !m.viewportFrozen {
		t.Fatal("expected PageUp during busy output to freeze chat viewport")
	}
	if fullLines := m.viewport.TotalLineCount(); fullLines <= tailLines {
		t.Fatalf("expected PageUp to restore full scrollable content, tail=%d full=%d", tailLines, fullLines)
	}
	if m.followTail {
		t.Fatal("expected PageUp to disable tail following")
	}
}
func TestChatViewportFirstPageUpDuringBusyAnchorsLiveTail(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 10
	m.transcript = nil
	for i := 0; i < 40; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%02d", i))
	}
	m.beginTurnTranscript()
	m.startBusy()
	for i := 0; i < 12; i++ {
		m.append("assistant", fmt.Sprintf("live-%02d\n", i))
	}
	m.refreshViewportContentFollow(true)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(model)
	view := m.View()
	if !strings.Contains(view, "live-11") {
		t.Fatalf("expected first PageUp during busy output to anchor current live tail:\n%s", view)
	}
}
func TestCtrlCWhileBusyClearsNonEmptyComposer(t *testing.T) {
	// PR 2 promoted Ctrl+C to the canonical clear-all path. During a busy
	// turn the composer-clear path must still be reachable so users can
	// drop a queued draft mid-stream without canceling the running turn.
	// Esc remains the unconditional interrupt for those who want to cancel.
	m, _ := newModelWithDispatchSpy()
	m.busy = true
	m.input.SetValue("queued draft text")

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected Ctrl+C to clear non-empty composer during busy, got %q", got)
	}
	if m.stopping {
		t.Fatal("expected Ctrl+C with non-empty composer not to interrupt the busy turn")
	}
}
func TestCtrlCWhileBusyEmptyComposerInterruptsTurn(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.busy = true
	m.input.SetValue("")

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.stopping {
		t.Fatal("expected Ctrl+C with empty composer during busy to interrupt the turn")
	}
}
func TestCtrlCWhileBusyInBlockingModeAlwaysInterrupts(t *testing.T) {
	// The composer-clear precedence is scoped to modeChat. In blocking
	// modes (approval, user-input) Ctrl+C must interrupt the running turn
	// even with a queued draft — otherwise it would only dismiss the modal
	// via the mode-specific handler and leave the turn running.
	m, _ := newModelWithDispatchSpy()
	m.busy = true
	m.input.SetValue("queued draft kept while modal blocks")
	m.mode = modeApproval
	m.approval.toolCallID = "tool-1"
	m.approval.toolName = "shell"

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.stopping {
		t.Fatal("expected Ctrl+C in modeApproval during busy to interrupt the turn, not just dismiss the modal")
	}
	if got := m.input.Value(); got != "queued draft kept while modal blocks" {
		t.Fatalf("expected interrupt path not to touch the composer draft, got %q", got)
	}
}
func TestCtrlCClearsWhitespaceOnlyDraft(t *testing.T) {
	// After PR 2 made Ctrl+C the canonical clear-all, the path must accept
	// whitespace-only buffers too — otherwise a stray Enter / blank-line
	// paste leaves the user with no way to clear short of Ctrl+C ×2 quit.
	m, _ := newModelWithDispatchSpy()
	m.input.SetValue("   \n\n  ")

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected Ctrl+C to clear whitespace-only draft, got %q", got)
	}
	if !m.quitArmedUntil.IsZero() {
		t.Fatal("expected Ctrl+C with whitespace draft to clear (not arm quit)")
	}
}
func TestCtrlCWhileBusyClearsWhitespaceOnlyDraft(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.busy = true
	m.input.SetValue("   ")

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected Ctrl+C to clear whitespace-only draft during busy, got %q", got)
	}
	if m.stopping {
		t.Fatal("expected Ctrl+C with whitespace draft during busy to clear (not interrupt)")
	}
}
func TestCtrlCClearsMultilineComposerWithLayoutSyncOnly(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 10
	m.transcript = nil
	m.input.SetValue("alpha\nbeta")
	for i := 0; i < 60; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%02d", i))
	}
	m.refreshViewportContentFollow(true)
	mainWidth, _ := m.layoutDims()
	initialBodyHeight := m.viewportBodyHeight(mainWidth)
	initialGeneration := m.chat.generation

	// Ctrl+C is the canonical full-clear after PR 2 moved Ctrl+U to readline
	// kill-to-line-start (which would only kill the current line "beta").
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	mainWidth, _ = m.layoutDims()
	if got := m.viewportBodyHeight(mainWidth); got <= initialBodyHeight {
		t.Fatalf("expected Ctrl+C clear to free composer height, got %d want > %d", got, initialBodyHeight)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected Ctrl+C to clear multiline composer, got %q", got)
	}
	if m.chat.generation != initialGeneration {
		t.Fatalf("expected Ctrl+C clear not to rerender chat, gen=%d want=%d", m.chat.generation, initialGeneration)
	}
	if !m.followTail || !m.chat.AtBottom() {
		t.Fatalf("expected Ctrl+C clear at tail to keep latest content visible, follow=%v chatBottom=%v", m.followTail, m.chat.AtBottom())
	}
}
func TestChatBusyViewShowsWorkingAboveComposer(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	view := m.View()
	if !strings.Contains(view, "Working (12s) · Type follow-up, Enter to queue · Esc/Ctrl+C to interrupt") {
		t.Fatalf("expected working status line with elapsed time:\n%s", view)
	}
	if strings.Contains(view, "status: working") {
		t.Fatalf("busy view should not render footer status:\n%s", view)
	}
	if strings.Index(view, "Working (12s)") > strings.Index(view, "Type message or command") {
		t.Fatalf("working status line should appear above composer:\n%s", view)
	}
}

func TestBusyQueueAndFooterStayOutsideComposerBlock(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "high", "on")
	m.width = 110
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale"
	m.gitBranch = "feat/composer-redesign"
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("editable follow-up")
	m.queuedPrompts = []queuedPrompt{{Text: "first queued prompt"}, {Text: "second queued prompt"}}

	bottom := ansi.Strip(m.renderBottom(110))
	lines := strings.Split(strings.TrimRight(bottom, "\n"), "\n")
	busyIdx := firstLineContaining(lines, "Working (12s)")
	queueIdx := firstLineContaining(lines, "queued follow-up (2)")
	boundaryIdx := firstFullWidthBoundaryLine(lines, 110)
	composerIdx := firstLineContaining(lines, "editable follow-up")
	footerIdx := len(lines) - 1

	if busyIdx < 0 || queueIdx < 0 || boundaryIdx < 0 || composerIdx < 0 {
		t.Fatalf("expected busy status, queue panel, composer boundary, and editable draft in bottom layout:\n%s", bottom)
	}
	if !(busyIdx < boundaryIdx && queueIdx < boundaryIdx && boundaryIdx < composerIdx && composerIdx < footerIdx) {
		t.Fatalf("expected busy status and queued prompts above composer boundary and footer after composer, got busy=%d queue=%d boundary=%d composer=%d footer=%d:\n%s",
			busyIdx, queueIdx, boundaryIdx, composerIdx, footerIdx, bottom)
	}
	insideComposer := strings.Join(lines[boundaryIdx+1:footerIdx], "\n")
	for _, unexpected := range []string{"Working (12s)", "queued follow-up (2)", "first queued prompt", "second queued prompt"} {
		if strings.Contains(insideComposer, unexpected) {
			t.Fatalf("%q should not be wrapped inside composer boundaries:\n%s", unexpected, bottom)
		}
	}
	footer := lines[footerIdx]
	for _, want := range []string{"deepseek-v4-flash . high", "thinking: on", "whale", "feat/composer-redesign"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("expected footer last line to contain %q, got %q in:\n%s", want, footer, bottom)
		}
	}
}

func TestChatBusyViewShowsDraftSpecificBusyHint(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("follow up")

	view := m.View()
	if !strings.Contains(view, "Working (12s) · Enter to queue · Esc interrupts and sends · Ctrl+C clears draft") {
		t.Fatalf("expected draft-specific busy status line:\n%s", view)
	}
	if strings.Contains(view, "Esc/Ctrl+C to interrupt") {
		t.Fatalf("draft busy status should not claim Ctrl+C interrupts:\n%s", view)
	}
}
func TestChatBusyViewTreatsWhitespaceDraftAsEmpty(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("  \n\t  ")

	view := m.View()
	if !strings.Contains(view, "Working (12s) · Type follow-up · Esc to interrupt · Ctrl+C clears draft") {
		t.Fatalf("expected whitespace draft to use draft-clearing busy guidance:\n%s", view)
	}
	for _, unexpected := range []string{"Enter to queue", "Esc/Ctrl+C to interrupt"} {
		if strings.Contains(view, unexpected) {
			t.Fatalf("whitespace-only draft should not show %q:\n%s", unexpected, view)
		}
	}
}
func TestChatBusyViewShowsProviderRetryStatus(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 140
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.providerRetryStatus = "API rate limited, retrying in 1s (1/3)"
	m.providerRetryUntil = time.Now().Add(time.Second)

	view := m.View()
	if !strings.Contains(view, "API rate limited, retrying in 1s (1/3) (12s) · Type follow-up, Enter to queue · Esc/Ctrl+C to interrupt") {
		t.Fatalf("expected retry status in busy line:\n%s", view)
	}
}
func TestChatBusyViewIgnoresExpiredProviderRetryStatus(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.providerRetryStatus = "API rate limited, retrying in 1s (1/3)"
	m.providerRetryUntil = time.Now().Add(-time.Second)

	view := m.View()
	if strings.Contains(view, "API rate limited") {
		t.Fatalf("expired retry status should not render:\n%s", view)
	}
	if !strings.Contains(view, "Working (12s) · Type follow-up, Enter to queue · Esc/Ctrl+C to interrupt") {
		t.Fatalf("expected working status after retry expiry:\n%s", view)
	}
}
func TestChatStoppingViewShowsStoppingAboveComposer(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 24
	m.startBusy()
	m.stopping = true
	m.busySince = time.Now().Add(-(time.Minute + 5*time.Second))
	view := m.View()
	if !strings.Contains(view, "Stopping (1m 05s)") {
		t.Fatalf("expected stopping status line with continued elapsed time:\n%s", view)
	}
	if strings.Contains(view, "to interrupt") {
		t.Fatalf("stopping view should not show interrupt hint:\n%s", view)
	}
}
func TestBtwBusySubmitDispatchesLocalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.submitPromptWhileBusy("/btw what is happening?")
	if len(*intents) != 1 {
		t.Fatalf("expected one intent, got %d", len(*intents))
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/btw what is happening?" {
		t.Fatalf("unexpected intent: %+v", got)
	}
	if m.localSubmitPending != 1 {
		t.Fatalf("expected pending local submit, got %d", m.localSubmitPending)
	}
}

func TestStopTaskBusySubmitDispatchesLocalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.submitPromptWhileBusy("/stop")
	if len(*intents) != 1 {
		t.Fatalf("expected one intent, got %d", len(*intents))
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/stop" {
		t.Fatalf("unexpected intent: %+v", got)
	}
	if m.localSubmitPending != 1 {
		t.Fatalf("expected pending local submit, got %d", m.localSubmitPending)
	}
}

func TestBtwPanelKeysDismissBeforeBusyInterrupt(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.btwPanel = btwPanelState{visible: true, id: 1, question: "quick?", response: "answer"}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.btwPanel.visible {
		t.Fatal("expected ctrl+c to dismiss btw panel")
	}
	if len(*intents) != 0 {
		t.Fatalf("ctrl+c with btw panel should not interrupt busy turn, got %+v", *intents)
	}
}
