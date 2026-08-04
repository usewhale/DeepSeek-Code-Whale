package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
)

func TestCommitLiveTranscriptClearsAssembler(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), width: 80, height: 24}
	m.append("assistant", "streamed answer")
	m.commitLiveTranscript(true)
	if got := len(m.assembler.Snapshot()); got != 0 {
		t.Fatalf("expected live assembler cleared after commit, got %d entries", got)
	}
	if len(m.transcript) != 1 || m.transcript[0].Text != "streamed answer" {
		t.Fatalf("expected committed transcript entry, got %+v", m.transcript)
	}
}

func TestSubmitPromptCommitsIdleLiveNoticeBeforeUserEcho(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), width: 100, height: 30}
	m.appendSystemNotice(&tuirender.SystemNotice{Kind: "approval_denied", Tone: "error", Action: "Denied", Subject: "request", Detail: "to use", Command: "search_files"})

	_ = m.submitPrompt("run vue-newsletter workflow")

	if got := len(m.assembler.Snapshot()); got != 0 {
		t.Fatalf("expected previous live notice to be committed before new prompt, got %d live messages", got)
	}
	if len(m.transcript) < 2 {
		t.Fatalf("expected denied notice and user prompt in transcript, got %+v", m.transcript)
	}
	if m.transcript[0].Kind != tuirender.KindNotice || !strings.Contains(m.transcript[0].Text, "Denied request to use search_files") {
		t.Fatalf("first transcript entry should be previous denied notice, got %+v", m.transcript[0])
	}
	if m.transcript[1].Role != "you" || m.transcript[1].Text != "run vue-newsletter workflow" {
		t.Fatalf("second transcript entry should be new user prompt, got %+v", m.transcript[1])
	}
}

func TestAssistantDeltaKeepsMultilineBlockLiveUntilBoundary(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 24
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventAssistantDelta, Text: "stable line\nlive tail"}))
	m = next.(model)
	snap := m.assembler.Snapshot()
	if len(snap) != 1 || snap[0].Text != "stable line\nlive tail" {
		t.Fatalf("expected newline-delimited assistant content to stay in one live message, got %+v", snap)
	}
	view := m.View()
	if !strings.Contains(view, "stable line") {
		t.Fatalf("expected first line to remain in the same live block:\n%s", view)
	}
	if !strings.Contains(view, "live tail") {
		t.Fatalf("expected tail to remain in the same live block:\n%s", view)
	}
}
func TestReasoningDeltaKeepsSingleThinkingCardAcrossNewlines(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 24
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventReasoningDelta, Text: "first thought\n\nsecond thought\nthird thought"}))
	m = next.(model)
	snap := m.assembler.Snapshot()
	if len(snap) != 1 || snap[0].Role != "think" {
		t.Fatalf("expected reasoning content to stay in one live thinking message, got %+v", snap)
	}
	lines := m.renderChatLines(80)
	joined := strings.Join(lines, "\n")
	if got := strings.Count(joined, "Thinking"); got != 1 {
		t.Fatalf("expected one thinking card, got %d:\n%s", got, joined)
	}
	if strings.Contains(joined, "first thought") {
		t.Fatalf("expected streaming thinking preview to hide older line:\n%s", joined)
	}
	for _, want := range []string{"second thought", "third thought"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in thinking card:\n%s", want, joined)
		}
	}
}
func TestCommittedReasoningIsNoLongerStreaming(t *testing.T) {
	m := newModel(nil, "", "", "")
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventReasoningDelta, Text: "first\nsecond\nthird"}))
	m = next.(model)
	snap := m.assembler.Snapshot()
	if len(snap) != 1 || !snap[0].Streaming {
		t.Fatalf("expected live reasoning to be streaming, got %+v", snap)
	}
	m.commitLiveTranscript(true)
	if got := len(m.transcript); got != 1 {
		t.Fatalf("expected one transcript message, got %d", got)
	}
	if m.transcript[0].Streaming {
		t.Fatalf("committed reasoning should not stay streaming: %+v", m.transcript[0])
	}
}
func TestChatFooterShowsAutoAcceptOnlyWhenEnabled(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 100
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale"

	view := m.View()
	assertFooterLastLineNotContains(t, view, "auto-accept on")

	m.autoAccept = true
	view = m.View()
	assertFooterLastLine(t, view, "auto-accept on")
}
func TestChatFooterShowsPermissionStateAndCycleHint(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 100
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale"

	view := m.View()
	assertFooterLastLine(t, view, "permission: ask")
	assertFooterLastLine(t, view, "Alt+P to cycle")

	m.autoReviewEnabled = true
	view = m.View()
	assertFooterLastLine(t, view, "auto-review")
	assertFooterLastLine(t, view, "Alt+P to cycle")

	m.autoReviewEnabled = false
	m.autoAccept = true
	view = m.View()
	assertFooterLastLine(t, view, "auto-accept on")
	assertFooterLastLine(t, view, "Alt+P to cycle")
}

func TestChatFooterShowsGitBranchInsteadOfScrollHint(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 140
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale-footer-branch-display"
	m.gitBranch = "feat/footer-branch"

	view := m.View()
	assertFooterLastLine(t, view, "footer-branch-display")
	assertFooterLastLine(t, view, "feat/footer-branch")
	assertFooterLastLineNotContains(t, view, "PgUp/PgDn scroll")
}
func TestChatFooterOmitsEmptyGitBranch(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 120
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale-footer-branch-display"

	view := m.View()
	assertFooterLastLine(t, view, "whale-footer-branch-display")
	assertFooterLastLineNotContains(t, view, "PgUp/PgDn scroll")
}
func TestChatFooterLongGitBranchDoesNotHideDirectory(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 100
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale-footer-branch-display"
	m.gitBranch = "feat/this-is-an-extremely-long-branch-name-that-cannot-fit-in-the-footer"

	view := m.View()
	assertFooterLastLine(t, view, "footer-branch-display")
	assertFooterLastLineNotContains(t, view, m.gitBranch)
	assertFooterLastLineNotContains(t, view, "PgUp/PgDn scroll")
}
func TestChatFooterKeepsFocusIndicatorWithGitBranch(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 130
	m.height = 24
	m.viewMode = protocol.ViewModeFocus
	m.cwd = "/Users/goranka/Engineer/ai/dsk/whale-output-mouse-copy"
	m.gitBranch = "feat/footer-branch"

	view := m.View()
	assertFooterLastLine(t, view, "focus")
	assertFooterLastLine(t, view, "feat/footer-branch")
	assertFooterLastLineNotContains(t, view, "PgUp/PgDn scroll")
}
func TestGitBranchUpdatedIgnoresStaleCwd(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.cwdPath = "/tmp/current"
	next, _ := m.Update(gitBranchUpdatedMsg{cwd: "/tmp/old", branch: "old"})
	m = next.(model)
	if m.gitBranch != "" {
		t.Fatalf("expected stale branch update to be ignored, got %q", m.gitBranch)
	}

	next, _ = m.Update(gitBranchUpdatedMsg{cwd: "/tmp/current", branch: "current"})
	m = next.(model)
	if m.gitBranch != "current" {
		t.Fatalf("expected current branch update, got %q", m.gitBranch)
	}
}
func TestDetectGitBranch(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "checkout", "-b", "feat/footer-branch")

	if got := detectGitBranch(dir); got != "feat/footer-branch" {
		t.Fatalf("expected branch %q, got %q", "feat/footer-branch", got)
	}
}
func TestDetectGitBranchNonGitDirectory(t *testing.T) {
	requireGit(t)
	if got := detectGitBranch(t.TempDir()); got != "" {
		t.Fatalf("expected no branch outside git repo, got %q", got)
	}
}
func TestDetectGitBranchDetachedHead(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "checkout", "-B", "whale-test-base")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "checkout", "--detach", "HEAD")

	if got := detectGitBranch(dir); got != "" {
		t.Fatalf("expected no branch in detached HEAD, got %q", got)
	}
}
func TestChatTranscriptRetainsLocalCommandResultsAcrossSubmits(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	localInfo := func(text string) protocol.Event {
		return protocol.Event{Kind: protocol.EventLocalSubmitResult, Status: "info", Text: text}
	}
	localDone := func() protocol.Event {
		return protocol.Event{Kind: protocol.EventLocalSubmitDone, Metadata: map[string]any{protocol.EventMetadataLocalSubmit: true}}
	}
	next, _ := m.Update(svcMsg(localInfo("MCP\n\nconfig: /tmp/mcp.json servers: none")))
	m = next.(model)
	next, _ = m.Update(svcMsg(localDone()))
	m = next.(model)

	m.input.SetValue("/status")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(svcMsg(localInfo("Status\n\nsession: test-session")))
	m = next.(model)
	next, _ = m.Update(svcMsg(localDone()))
	m = next.(model)

	got := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	for _, want := range []string{"config: /tmp/mcp.json", "/status", "session: test-session"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected transcript to retain %q:\n%s", want, got)
		}
	}
}
func TestNewSessionLocalResultRendersAsNotice(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "New session\n\nsession:  fresh\nprevious: old\nresume:   whale resume old\nmode:     agent",
	}))
	m = next.(model)
	if len(m.transcript) < 1 {
		t.Fatalf("expected session notice, got %+v", m.transcript)
	}
	got := m.transcript[len(m.transcript)-1]
	if got.Role != "notice" || got.Kind != tuirender.KindNotice {
		t.Fatalf("expected session notice kind, got role=%q kind=%q text=%q", got.Role, got.Kind, got.Text)
	}
}

func TestNewSessionLocalResultSyncsFooterModeFromTrustedFields(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	m.chatMode = "plan"
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "New session\n\nsession:  fresh\nprevious: old\nresume:   whale resume old\nmode:     agent",
		LocalResult: &protocol.LocalResult{
			Kind:      "new_session",
			Title:     "New session",
			PlainText: "New session\n\nsession:  fresh\nprevious: old\nresume:   whale resume old\nmode:     agent",
			Fields: []protocol.LocalResultField{
				{Label: "Session", Value: "fresh"},
				{Label: "Previous", Value: "old"},
				{Label: "Mode", Value: "agent"},
			},
		},
	}))
	m = next.(model)
	if m.chatMode != "agent" {
		t.Fatalf("expected /new notice to sync footer mode to agent, got %q", m.chatMode)
	}
}

func TestUntrustedLocalResultFieldsDoNotSyncFooterState(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	m.model = "deepseek-v4-flash"
	m.chatMode = "plan"
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "local-indexer\n\nstatus: scaffold\nmodel: not installed\nmode: agent\njobs: none",
		LocalResult: &protocol.LocalResult{
			Kind:      "local_indexer",
			Title:     "local-indexer",
			PlainText: "local-indexer\n\nstatus: scaffold\nmodel: not installed\nmode: agent\njobs: none",
			Fields: []protocol.LocalResultField{
				{Label: "Status", Value: "scaffold"},
				{Label: "Model", Value: "not installed"},
				{Label: "Mode", Value: "agent"},
			},
		},
	}))
	m = next.(model)
	if m.model != "deepseek-v4-flash" {
		t.Fatalf("untrusted local result changed footer model to %q", m.model)
	}
	if m.chatMode != "plan" {
		t.Fatalf("untrusted local result changed footer mode to %q", m.chatMode)
	}
}
func TestStatusLocalResultRendersAsStructuredTranscriptEntry(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14

	m.input.SetValue("/status")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "Status\n\n- session: test-session",
		LocalResult: &protocol.LocalResult{
			Kind:      "status",
			Title:     "Status",
			PlainText: "Status\n\n- session: test-session",
			Fields: []protocol.LocalResultField{
				{Label: "Session", Value: "test-session"},
				{Label: "Mode", Value: "agent", Tone: "info"},
			},
		},
	}))
	m = next.(model)

	if len(m.transcript) < 2 {
		t.Fatalf("expected command echo and status result, got %+v", m.transcript)
	}
	got := m.transcript[len(m.transcript)-1]
	if got.Kind != tuirender.KindLocalStatus || got.Role != "local_status" || got.Local == nil {
		t.Fatalf("expected local status transcript entry, got role=%q kind=%q local=%+v", got.Role, got.Kind, got.Local)
	}
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	for _, want := range []string{"/status", "Status", "Session", "test-session", "Mode", "agent"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered status transcript to contain %q:\n%s", want, rendered)
		}
	}
}
func TestLocalCommandResultCommitsIdleAssemblerBeforeNextPrompt(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14

	next, _ := m.Update(svcMsg(protocol.Event{
		Kind: protocol.EventInfo,
		Text: "Startup notice",
	}))
	m = next.(model)
	if got := len(m.assembler.Snapshot()); got == 0 {
		t.Fatal("expected idle info event to leave live assembler content")
	}

	m.input.SetValue("/status")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "Status\n\nsession: test-session",
	}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind: protocol.EventLocalSubmitDone,
		Metadata: map[string]any{
			protocol.EventMetadataLocalSubmit: true,
		},
	}))
	m = next.(model)
	if got := len(m.assembler.Snapshot()); got != 0 {
		t.Fatalf("expected local result to commit idle live assembler, got %d live entries", got)
	}

	m.input.SetValue("next prompt")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	rendered := strings.Join(tuirender.ChatLines(m.chatMessages(), 80), "\n")
	noticeIx := strings.Index(rendered, "Startup notice")
	statusIx := strings.Index(rendered, "session: test-session")
	promptIx := strings.Index(rendered, "next prompt")
	if noticeIx < 0 || statusIx < 0 || promptIx < 0 || !(noticeIx < statusIx && statusIx < promptIx) {
		t.Fatalf("expected idle live content and local result before next prompt:\n%s", rendered)
	}
}
func TestLocalCommandResultPreservesLiveTurnOrder(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14
	localInfo := protocol.Event{Kind: protocol.EventLocalSubmitResult, Status: "info", Text: "Stats\n\nusage summary"}

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventAssistantDelta, Text: "streamed answer"}))
	m = next.(model)
	next, _ = m.Update(svcMsg(localInfo))
	m = next.(model)

	rendered := strings.Join(tuirender.ChatLines(m.chatMessages(), 80), "\n")
	streamedIx := strings.Index(rendered, "streamed answer")
	statsIx := strings.Index(rendered, "usage summary")
	if streamedIx < 0 || statsIx < 0 || streamedIx > statsIx {
		t.Fatalf("expected live assistant output before local result:\n%s", rendered)
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:         protocol.EventTurnDone,
		LastResponse: "streamed answer with final reconciliation",
		Metadata:     map[string]any{protocol.EventMetadataAgentTurn: true},
	}))
	m = next.(model)
	rendered = strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	streamedIx = strings.Index(rendered, "streamed answer")
	statsIx = strings.Index(rendered, "usage summary")
	tailIx := strings.Index(rendered, "with final reconciliation")
	if streamedIx < 0 || statsIx < 0 || tailIx < 0 || !(streamedIx < statsIx && statsIx < tailIx) {
		t.Fatalf("expected assistant prefix, local result, then recovered assistant tail:\n%s", rendered)
	}
	if got := len(m.assembler.Snapshot()); got != 0 {
		t.Fatalf("expected local result to leave live assembler empty after reconciliation, got %+v", m.assembler.Snapshot())
	}
}
func TestChatStartupHeaderPrintsCompactWhenShort(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = next.(model)
	if !m.startupHeaderPrinted {
		t.Fatal("expected window size update to mark startup header printed")
	}
	view := m.View()
	if strings.Contains(view, "WHALE") {
		t.Fatalf("expected printed startup header not to repeat in live viewport:\n%s", view)
	}
	header := m.startupHeaderText()
	if !strings.Contains(header, "WHALE") {
		t.Fatalf("expected compact startup header text:\n%s", header)
	}
	if strings.Contains(header, "██╗") {
		t.Fatalf("expected compact short-terminal header, got large logo:\n%s", header)
	}
	for _, want := range []string{"model: deepseek-v4-flash"} {
		if !strings.Contains(header, want) {
			t.Fatalf("expected startup header to contain %q:\n%s", want, header)
		}
	}
	if got := countVisibleLines(view); got >= m.height {
		t.Fatalf("expected compact header view to use natural height below terminal height %d, got %d:\n%s", m.height, got, view)
	}
}
func TestChatStartupHeaderLeavesGapAboveComposer(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24
	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	promptIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "Type message or command") {
			promptIdx = i
			break
		}
	}
	if promptIdx < 1 {
		t.Fatalf("expected composer after startup header:\n%s", view)
	}
	if promptIdx < 2 {
		t.Fatalf("expected composer boundary after startup header gap:\n%s", view)
	}
	if strings.TrimSpace(lines[promptIdx-2]) != "" {
		t.Fatalf("expected blank line between startup header and composer boundary, got %q in view:\n%s", lines[promptIdx-2], view)
	}
}

func TestChatTranscriptLeavesGapAboveComposer(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24
	m.startupHeaderPrintCmd()
	m.appendTranscript("assistant", tuirender.KindText, "done response")

	view := xansi.Strip(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	responseIdx := firstLineContaining(lines, "done response")
	boundaryIdx := firstFullWidthBoundaryLine(lines, 80)
	if responseIdx < 0 || boundaryIdx < 0 {
		t.Fatalf("expected assistant response and composer boundary in view:\n%s", view)
	}
	if boundaryIdx-responseIdx != 2 {
		t.Fatalf("expected one blank line between assistant response and composer boundary, got response=%d boundary=%d in:\n%s", responseIdx, boundaryIdx, view)
	}
	if strings.TrimSpace(lines[responseIdx+1]) != "" {
		t.Fatalf("expected blank line above composer boundary, got %q in:\n%s", lines[responseIdx+1], view)
	}
}

func TestChatTranscriptBottomGapDoesNotOverflowConstrainedHeight(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = countVisibleLines(m.renderBottom(80)) + 1
	m.startupHeaderPrintCmd()
	m.appendTranscript("assistant", tuirender.KindText, "tight response")

	view := xansi.Strip(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if got := len(lines); got > m.height {
		t.Fatalf("expected constrained view not to exceed height %d, got %d:\n%s", m.height, got, view)
	}
	responseIdx := firstLineContaining(lines, "tight response")
	boundaryIdx := firstFullWidthBoundaryLine(lines, 80)
	if responseIdx < 0 || boundaryIdx < 0 {
		t.Fatalf("expected assistant response and composer boundary in constrained view:\n%s", view)
	}
	if boundaryIdx-responseIdx != 1 {
		t.Fatalf("expected constrained view to omit extra bottom gap, got response=%d boundary=%d in:\n%s", responseIdx, boundaryIdx, view)
	}
}
func TestChatComposerBoundaryRendersAboveComposer(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24
	m.input.SetWidth(76)

	view := xansi.Strip(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	promptIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "Type message or command") {
			promptIdx = i
			break
		}
	}
	if promptIdx < 1 {
		t.Fatalf("expected composer after boundary:\n%s", view)
	}
	boundary := lines[promptIdx-1]
	if want := strings.Repeat("─", 80); boundary != want {
		t.Fatalf("expected full-width composer boundary before composer, got %q want %q in view:\n%s", boundary, want, view)
	}
}
func TestComposerBoundaryUsesBottomLayoutWidth(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 120
	m.height = 24
	m.sidebar = true
	m.input.SetWidth(116)

	bottom := xansi.Strip(m.renderBottom(86))
	lines := strings.Split(strings.TrimRight(bottom, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected boundary, composer, and footer in bottom layout:\n%s", bottom)
	}
	boundary := lines[0]
	composer := lines[1]
	if want := strings.Repeat("─", 86); boundary != want {
		t.Fatalf("expected boundary to use main layout width, got width %d line %q", lipgloss.Width(boundary), boundary)
	}
	if lipgloss.Width(composer) != 116 {
		t.Fatalf("expected composer view to retain its own width, got %d line %q", lipgloss.Width(composer), composer)
	}
}
func TestComposerSeparatorsWrapComposerWithBottomLayoutWidth(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 120
	m.height = 24
	m.sidebar = true
	m.input.SetWidth(116)
	m.input.SetValue("first line\nsecond line")

	bottom := xansi.Strip(m.renderBottom(86))
	lines := strings.Split(strings.TrimRight(bottom, "\n"), "\n")
	boundary := strings.Repeat("─", 86)
	boundaryIdxs := []int{}
	for i, line := range lines {
		if line == boundary {
			boundaryIdxs = append(boundaryIdxs, i)
			if got := lipgloss.Width(line); got != 86 {
				t.Fatalf("expected composer separator width 86, got %d line %q", got, line)
			}
		}
	}
	if len(boundaryIdxs) != 2 {
		t.Fatalf("expected top and bottom composer separators, got indexes %v in:\n%s", boundaryIdxs, bottom)
	}
	firstLineIdx := firstLineContaining(lines, "first line")
	secondLineIdx := firstLineContaining(lines, "second line")
	footerIdx := len(lines) - 1
	if firstLineIdx < 0 || secondLineIdx < 0 {
		t.Fatalf("expected multiline composer content in bottom:\n%s", bottom)
	}
	if !(boundaryIdxs[0] < firstLineIdx && firstLineIdx < secondLineIdx && secondLineIdx < boundaryIdxs[1] && boundaryIdxs[1] < footerIdx) {
		t.Fatalf("expected separators to wrap composer before footer, got top=%d first=%d second=%d bottom=%d footer=%d:\n%s",
			boundaryIdxs[0], firstLineIdx, secondLineIdx, boundaryIdxs[1], footerIdx, bottom)
	}
	if got := lipgloss.Width(lines[firstLineIdx]); got != 116 {
		t.Fatalf("expected composer input to retain existing width 116, got %d line %q", got, lines[firstLineIdx])
	}
}
func TestComposerSeparatorsContributeToBottomHeight(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24
	m.input.SetWidth(76)
	m.input.SetValue("alpha\nbeta")

	bottom := m.renderBottom(80)
	plain := xansi.Strip(bottom)
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	boundary := strings.Repeat("─", 80)
	boundaryCount := 0
	for _, line := range lines {
		if line == boundary {
			boundaryCount++
		}
	}
	if boundaryCount != 2 {
		t.Fatalf("expected two real separator lines in bottom, got %d:\n%s", boundaryCount, plain)
	}
	if got, want := countVisibleLines(bottom), len(lines); got != want {
		t.Fatalf("expected embedded newlines to determine bottom height, got %d want %d:\n%s", got, want, plain)
	}
}
func TestMultilineComposerDoesNotOverlapFooterAtFixedWidth(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 9
	m.input.SetWidth(76)
	m.input.SetValue("one\ntwo\nthree")

	view := xansi.Strip(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	footerIdx := len(lines) - 1
	boundary := strings.Repeat("─", 80)
	boundaryIdxs := []int{}
	for i, line := range lines {
		if line == boundary {
			boundaryIdxs = append(boundaryIdxs, i)
		}
	}
	if len(boundaryIdxs) != 2 {
		t.Fatalf("expected top and bottom composer separators in view, got %v:\n%s", boundaryIdxs, view)
	}
	for _, want := range []string{"one", "two", "three"} {
		idx := firstLineContaining(lines, want)
		if idx < 0 {
			t.Fatalf("expected composer line %q in view:\n%s", want, view)
		}
		if !(boundaryIdxs[0] < idx && idx < boundaryIdxs[1]) {
			t.Fatalf("expected composer line %q between separators, got line %d separators %v:\n%s", want, idx, boundaryIdxs, view)
		}
	}
	if !(boundaryIdxs[1] < footerIdx) {
		t.Fatalf("expected footer after bottom separator, got bottom separator %d footer %d:\n%s", boundaryIdxs[1], footerIdx, view)
	}
	if strings.Contains(lines[footerIdx], "one") || strings.Contains(lines[footerIdx], "two") || strings.Contains(lines[footerIdx], "three") {
		t.Fatalf("composer content overlapped footer line %q in:\n%s", lines[footerIdx], view)
	}
	if got := countVisibleLines(view); got > m.height {
		t.Fatalf("expected view not to exceed fixed height %d, got %d:\n%s", m.height, got, view)
	}
}
func TestSmallTerminalHeightKeepsBodyFooterAndComposerSeparated(t *testing.T) {
	for _, height := range []int{5, 6, 7} {
		m := newModel(nil, "deepseek-v4-flash", "max", "off")
		m.width = 80
		m.height = height
		m.startupHeaderPrintCmd()
		m.input.SetWidth(76)
		m.input.SetValue("alpha\nbeta")
		m.appendTranscript("info", tuirender.KindText, "body-tail")

		view := xansi.Strip(m.View())
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if got := len(lines); got > height {
			t.Fatalf("height %d: expected rendered lines not to exceed terminal height, got %d:\n%s", height, got, view)
		}
		footerIdx := len(lines) - 1
		if !strings.Contains(lines[footerIdx], "deepseek-v4-flash . max") {
			t.Fatalf("height %d: expected footer on last line, got %q in:\n%s", height, lines[footerIdx], view)
		}
		bodyIdx := firstLineContaining(lines, "body-tail")
		alphaIdx := firstLineContaining(lines, "alpha")
		betaIdx := firstLineContaining(lines, "beta")
		boundary := strings.Repeat("─", 80)
		boundaryIdxs := []int{}
		for i, line := range lines {
			if line == boundary {
				boundaryIdxs = append(boundaryIdxs, i)
			}
		}
		if len(boundaryIdxs) != 2 {
			t.Fatalf("height %d: expected top and bottom separators, got %v:\n%s", height, boundaryIdxs, view)
		}
		if alphaIdx < 0 || betaIdx < 0 {
			t.Fatalf("height %d: expected multiline composer content in view:\n%s", height, view)
		}
		if !(boundaryIdxs[0] < alphaIdx && alphaIdx < betaIdx && betaIdx < boundaryIdxs[1] && boundaryIdxs[1] < footerIdx) {
			t.Fatalf("height %d: expected composer and footer separated, got boundaries=%v alpha=%d beta=%d footer=%d:\n%s",
				height, boundaryIdxs, alphaIdx, betaIdx, footerIdx, view)
		}
		if bodyIdx >= 0 && !(bodyIdx < boundaryIdxs[0]) {
			t.Fatalf("height %d: expected body before composer, got body=%d boundaries=%v:\n%s", height, bodyIdx, boundaryIdxs, view)
		}
	}
}

func TestComposerRegressionManualAcceptanceRenderStates(t *testing.T) {
	cases := []struct {
		name    string
		height  int
		setup   func(*model)
		want    []string
		notWant []string
	}{
		{
			name:   "first screen empty state",
			height: 24,
			want:   []string{"Type message or command", "deepseek-v4-flash . max", "thinking: off"},
		},
		{
			name:   "typing",
			height: 24,
			setup: func(m *model) {
				m.startupHeaderPrintCmd()
				m.input.SetValue("draft prompt")
			},
			want: []string{"draft prompt", "deepseek-v4-flash . max"},
		},
		{
			name:   "multiline",
			height: 8,
			setup: func(m *model) {
				m.startupHeaderPrintCmd()
				m.input.SetValue("line one\nline two\nline three")
			},
			want: []string{"line one", "line two", "line three", "deepseek-v4-flash . max"},
		},
		{
			name:   "busy follow-up",
			height: 24,
			setup: func(m *model) {
				m.startupHeaderPrintCmd()
				m.startBusy()
				m.busySince = time.Now().Add(-12 * time.Second)
				m.input.SetValue("follow up")
				m.queuedPrompts = []queuedPrompt{{Text: "queued one"}}
			},
			want: []string{"Working (12s)", "Enter to queue", "queued follow-up (1)", "queued one", "follow up"},
		},
		{
			name:   "slash suggestions",
			height: 24,
			setup: func(m *model) {
				m.startupHeaderPrintCmd()
				m.input.SetValue("/")
				m.updateSlashMatches()
			},
			want: []string{"/permissions", "Tab/Enter pick", "› /"},
		},
		{
			name:   "file suggestions",
			height: 24,
			setup: func(m *model) {
				m.startupHeaderPrintCmd()
				m.input.SetValue("@mod")
				m.files.active = true
				m.files.matches = []fileSuggestion{{Path: "internal/tui/model.go"}}
			},
			want: []string{"Files", "internal/tui/model.go", "Tab/Enter insert", "@mod"},
		},
		{
			name:   "skill suggestions",
			height: 24,
			setup: func(m *model) {
				m.startupHeaderPrintCmd()
				m.input.SetValue("$co")
				m.skills.matches = []skillSuggestion{{Name: "code-review", Description: "Review local changes"}}
			},
			want: []string{"Skills", "$code-review", "Tab/Enter insert", "$co"},
		},
		{
			name:   "small terminal height",
			height: 6,
			setup: func(m *model) {
				m.startupHeaderPrintCmd()
				m.input.SetValue("small\nheight")
				m.appendTranscript("info", tuirender.KindText, "body-tail")
			},
			want: []string{"small", "height", "deepseek-v4-flash . max"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(nil, "deepseek-v4-flash", "max", "off")
			m.width = 80
			m.height = tt.height
			m.input.SetWidth(76)
			if tt.setup != nil {
				tt.setup(&m)
			}

			view := xansi.Strip(m.View())
			for _, want := range tt.want {
				if !strings.Contains(view, want) {
					t.Fatalf("expected render state to contain %q:\n%s", want, view)
				}
			}
			for _, notWant := range append(tt.notWant, "? for shortcuts · ↵ for agents") {
				if strings.Contains(view, notWant) {
					t.Fatalf("render state should not contain %q:\n%s", notWant, view)
				}
			}
			if got := countVisibleLines(view); got > tt.height {
				t.Fatalf("expected render state not to exceed height %d, got %d:\n%s", tt.height, got, view)
			}

			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			footerIdx := len(lines) - 1
			if !strings.Contains(lines[footerIdx], "deepseek-v4-flash . max") {
				t.Fatalf("expected footer on last line, got %q in:\n%s", lines[footerIdx], view)
			}
			boundary := strings.Repeat("─", 80)
			boundaryIdxs := []int{}
			for i, line := range lines {
				if line == boundary {
					boundaryIdxs = append(boundaryIdxs, i)
				}
			}
			if len(boundaryIdxs) != 2 {
				t.Fatalf("expected two composer separators, got %v:\n%s", boundaryIdxs, view)
			}
			if !(boundaryIdxs[0] < boundaryIdxs[1] && boundaryIdxs[1] < footerIdx) {
				t.Fatalf("expected composer block before footer, got boundaries=%v footer=%d:\n%s", boundaryIdxs, footerIdx, view)
			}
		})
	}
}

func TestChatStartupHeaderGapDoesNotOverflowConstrainedHeight(t *testing.T) {
	for _, height := range []int{5, 11} {
		m := newModel(nil, "deepseek-v4-flash", "max", "off")
		m.width = 80
		m.height = height
		view := m.View()
		if got := countVisibleLines(view); got > height {
			t.Fatalf("startup header view overflowed height %d with %d lines:\n%s", height, got, view)
		}
	}
}
func TestChatStartupHeaderPrintsLargeLogoWhenTall(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(model)
	if !m.startupHeaderPrinted {
		t.Fatal("expected window size update to mark startup header printed")
	}
	header := m.startupHeaderText()
	if !strings.Contains(header, "███████╗") {
		t.Fatalf("expected large startup header:\n%s", header)
	}
	for _, want := range []string{"model:     deepseek-v4-flash", "effort:    max", "thinking:  off"} {
		if !strings.Contains(header, want) {
			t.Fatalf("expected startup header to contain %q:\n%s", want, header)
		}
	}
}
func TestChatStartupHeaderPrintCommandIsOneShot(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24
	cmd := m.startupHeaderPrintCmd()
	if cmd == nil {
		t.Fatal("expected startup header to be printed to native scrollback")
	}
	printed := teaPrintLineMessageBody(cmd())
	if !strings.Contains(printed, "███████") {
		t.Fatal("expected startup header print command to emit the banner")
	}
	if !strings.HasSuffix(printed, strings.Repeat("\n", startupHeaderTrailingBlankLines)) {
		t.Fatalf("expected startup header print command to leave trailing gap, got %#v", printed)
	}
	if !m.startupHeaderPrinted {
		t.Fatal("expected startup header to be marked printed")
	}
	if cmd := m.startupHeaderPrintCmd(); cmd != nil {
		t.Fatal("expected startup header print command to be one-shot")
	}
}

func TestChatStartupHeaderReplayLeavesGapWhenEmpty(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24

	cmd := m.replayNativeScrollbackCmd()
	if cmd == nil {
		t.Fatal("expected startup header replay to print empty welcome state")
	}
	printed := teaPrintLineMessageBody(cmd())
	if !strings.Contains(printed, "███████") {
		t.Fatalf("expected replay to emit startup header, got %#v", printed)
	}
	if !strings.HasSuffix(printed, strings.Repeat("\n", startupHeaderTrailingBlankLines)) {
		t.Fatalf("expected empty replay to leave trailing gap, got %#v", printed)
	}
}

func TestChatStartupHeaderReplayKeepsTranscriptSeparator(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24
	m.appendTranscript("info", tuirender.KindText, "first content")

	cmd := m.replayNativeScrollbackCmd()
	if cmd == nil {
		t.Fatal("expected startup header replay with transcript to print")
	}
	printed := teaPrintLineMessageBody(cmd())
	if !strings.Contains(printed, "███████") || !strings.Contains(printed, "first content") {
		t.Fatalf("expected replay to emit startup header and transcript, got %#v", printed)
	}
	if !strings.Contains(printed, "╯\n\n┃") {
		t.Fatalf("expected header and transcript to keep existing separator, got %#v", printed)
	}
	if strings.Contains(printed, "╯"+strings.Repeat("\n", startupHeaderTrailingBlankLines+1)) {
		t.Fatalf("expected transcript replay not to add empty-welcome trailing gap before transcript, got %#v", printed)
	}
}

func teaPrintLineMessageBody(msg tea.Msg) string {
	v := reflect.ValueOf(msg)
	if !v.IsValid() {
		return ""
	}
	field := v.FieldByName("messageBody")
	if !field.IsValid() || field.Kind() != reflect.String {
		return fmt.Sprintf("%#v", msg)
	}
	return field.String()
}

func TestChatStartupHeaderStaysVisibleWithSmallTranscript(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(model)

	m.appendTranscript("info", tuirender.KindText, "first content")
	view := m.View()
	// Once printed to native scrollback the header must stay out of the
	// live viewport so resize ticks cannot repaint it into the conversation.
	if strings.Contains(view, "███████╗") {
		t.Fatalf("expected printed startup header not to repeat in live viewport:\n%s", view)
	}
	if !strings.Contains(view, "first content") {
		t.Fatalf("expected transcript content in view:\n%s", view)
	}
	if got := countVisibleLines(view); got >= m.height {
		t.Fatalf("expected short transcript view to use natural height below terminal height %d, got %d:\n%s", m.height, got, view)
	}
}
func TestChatStartupHeaderReturnsAfterNewSessionNotice(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 24
	m.startupHeaderPrinted = true
	m.appendTranscript("assistant", tuirender.KindText, "old content")

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventScreenClearRequested}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind: protocol.EventInfo,
		Text: "New session\n\nsession: fresh",
	}))
	m = next.(model)

	if !m.startupHeaderPrinted {
		t.Fatal("expected clear screen to schedule a fresh startup header print")
	}
	view := m.View()
	if strings.Contains(view, "old content") {
		t.Fatalf("expected old content cleared after new session:\n%s", view)
	}
	if strings.Contains(view, "session: fresh") {
		t.Fatalf("expected printed new session notice not to repeat in tail viewport:\n%s", view)
	}
}
func TestChatHeaderOmittedWhenBodyTooShort(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = countVisibleLines(m.renderBottom(80)) + 2
	view := m.View()
	if strings.Contains(view, "╭") || strings.Contains(view, "╰") || strings.Contains(view, "WHALE") {
		t.Fatalf("startup header should not render inside chat viewport:\n%s", view)
	}
	if got := countVisibleLines(view); got != countVisibleLines(m.renderBottom(80)) {
		t.Fatalf("expected body to collapse when header cannot fit, got %d lines:\n%s", got, view)
	}
}
func TestChatViewPinsBottomAfterContentExceedsScreen(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 12
	m.transcript = nil
	for i := 0; i < 40; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%02d", i))
	}

	view := m.View()
	if got := countVisibleLines(view); got != m.height {
		t.Fatalf("expected overflowing chat view to occupy terminal height %d, got %d:\n%s", m.height, got, view)
	}
	assertFooterLastLine(t, view, "deepseek-v4-flash . max")
	if !strings.Contains(view, "entry-39") {
		t.Fatalf("expected overflowing chat view to follow latest content:\n%s", view)
	}
	if strings.Contains(view, "entry-00") {
		t.Fatalf("expected overflowing chat view to scroll older content out of the visible frame:\n%s", view)
	}
}

func TestChatBodyHeightIncludesConversationGaps(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 40
	m.startupHeaderPrinted = true
	*m.startupHeaderOnce = true
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "hi"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "Hello! How can I help you today?"},
		{Role: "you", Kind: tuirender.KindText, Text: "hah"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "Hello again! What can I do for you?"},
	}
	m.nativeScrollbackPrinted = 0

	messages := m.chatViewportMessages()
	renderWidth := max(20, m.width-2)
	want := chatListRenderedLineCount(messages, renderWidth)

	if got := m.chatBodyHeightForView(m.width, 100, messages, 0); got != want {
		t.Fatalf("expected chat body height to include conversation gaps, got %d want %d", got, want)
	}
	view := xansi.Strip(m.View())
	if blanks := countBlankLinesBetweenText(t, view, "hi", "Hello! How can I help you today?"); blanks < chatListGap {
		t.Fatalf("expected visible user-to-assistant gap to survive body height calculation, got %d blank lines:\n%s", blanks, view)
	}
	if blanks := countBlankLinesBetweenText(t, view, "Hello! How can I help you today?", "hah"); blanks < chatListGap {
		t.Fatalf("expected visible assistant-to-user gap to survive body height calculation, got %d blank lines:\n%s", blanks, view)
	}
}

func TestChatBodyHeightUsesProvidedLeadingGap(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "max", "off")
	m.width = 80
	m.height = 40
	m.startupHeaderPrinted = true
	*m.startupHeaderOnce = true
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "hi"},
	}
	m.nativeScrollbackPrinted = len(m.transcript)

	messages := m.chatViewportMessages()
	renderWidth := max(20, m.width-2)
	leadingGap := 3
	want := chatListRenderedLineCountWithLeadingGap(messages, renderWidth, leadingGap)

	if got := m.chatBodyHeightForView(m.width, 100, messages, leadingGap); got != want {
		t.Fatalf("expected chat body height to use provided leading gap, got %d want %d", got, want)
	}
	if derived := m.chatViewportLeadingGap(nil, messages); derived == leadingGap {
		t.Fatalf("test setup expected provided leading gap %d to differ from derived gap", leadingGap)
	}
}

func countBlankLinesBetweenText(t *testing.T, text, before, after string) int {
	t.Helper()
	lines := strings.Split(text, "\n")
	beforeIdx, afterIdx := -1, -1
	for i, line := range lines {
		if beforeIdx < 0 && strings.Contains(line, before) {
			beforeIdx = i
			continue
		}
		if beforeIdx >= 0 && strings.Contains(line, after) {
			afterIdx = i
			break
		}
	}
	if beforeIdx < 0 || afterIdx < 0 {
		t.Fatalf("could not find %q before %q in:\n%s", before, after, text)
	}
	blanks := 0
	for _, line := range lines[beforeIdx+1 : afterIdx] {
		if strings.TrimSpace(line) == "" {
			blanks++
		}
	}
	return blanks
}

func TestComposerEditsDoNotRerenderChatWhenHeightIsStable(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 10
	m.transcript = nil
	m.input.SetValue("seed\nline")
	for i := 0; i < 60; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%02d", i))
	}
	m.refreshViewportContentFollow(true)
	initialGeneration := m.chat.generation
	initialOffset := m.viewport.YOffset

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = next.(model)
	if got := m.input.Value(); got != "seed\nlinea" {
		t.Fatalf("expected rune input to update composer, got %q", got)
	}
	if m.chat.generation != initialGeneration {
		t.Fatalf("expected rune input not to rerender chat, gen=%d want=%d", m.chat.generation, initialGeneration)
	}
	if m.viewport.YOffset != initialOffset {
		t.Fatalf("expected rune input not to move chat offset, got %d want %d", m.viewport.YOffset, initialOffset)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)
	if got := m.input.Value(); got != "seed\nline" {
		t.Fatalf("expected backspace to update composer, got %q", got)
	}
	if m.chat.generation != initialGeneration {
		t.Fatalf("expected backspace not to rerender chat, gen=%d want=%d", m.chat.generation, initialGeneration)
	}
	if m.viewport.YOffset != initialOffset {
		t.Fatalf("expected backspace not to move chat offset, got %d want %d", m.viewport.YOffset, initialOffset)
	}
}
func BenchmarkComposerEditCycleLongHistory(b *testing.B) {
	for _, historyCount := range []int{500, 1000, 2000} {
		b.Run(fmt.Sprintf("history-%d", historyCount), func(b *testing.B) {
			m := newLongHistoryComposerModel(historyCount, "seed")
			initialGeneration := m.chat.generation
			initialItems := len(m.chat.items)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
				m = next.(model)
				next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
				m = next.(model)
			}
			b.StopTimer()

			if m.chat.generation != initialGeneration {
				b.Fatalf("expected edit cycle not to rerender chat, gen=%d want=%d", m.chat.generation, initialGeneration)
			}
			if len(m.chat.items) != initialItems {
				b.Fatalf("expected edit cycle to keep chat item count stable, got %d want %d", len(m.chat.items), initialItems)
			}
		})
	}
}
func TestChatFooterShowsEffectiveThinkingAndEffort(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "high", "on")
	m.width = 80
	m.height = 24

	view := m.View()
	assertFooterLastLine(t, view, "deepseek-v4-flash . high")
	assertFooterLastLine(t, view, "thinking: on")
}
func TestChatFooterUsesSemanticColorSegments(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := newModel(nil, "deepseek-v4-pro", "max", "on")
	m.width = 100
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale-theme-colors"
	m.viewMode = protocol.ViewModeFocus

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "\x1b[") {
		t.Fatalf("expected styled footer segments, got %q in view:\n%s", footer, view)
	}
	plain := xansi.Strip(footer)
	for _, want := range []string{
		"deepseek-v4-pro . max",
		"thinking: on",
		"whale-theme-colors",
		"focus",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected stripped footer to contain %q, got %q", want, plain)
		}
	}
}
func TestModelSetRefreshesHeaderCache(t *testing.T) {
	m := newModel(nil, "old-model", "high", "on")
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected startup header to be printed to native scrollback after first resize")
	}
	if header := m.startupHeaderText(); !strings.Contains(header, "model:     old-model") {
		t.Fatalf("expected initial header model:\n%s", header)
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "info",
		Text:   "model set: newer-model  effort: low  thinking: off",
	}))
	m = next.(model)

	header := m.startupHeaderText()
	if !strings.Contains(header, "model:     newer-model") {
		t.Fatalf("expected refreshed header after model set:\n%s", header)
	}
	if strings.Contains(header, "model:     old-model") {
		t.Fatalf("expected stale header model to disappear:\n%s", header)
	}
	view := m.View()
	if strings.Contains(view, "model set: newer-model") {
		t.Fatalf("expected printed model set result not to repeat in tail viewport:\n%s", view)
	}
	assertFooterLastLine(t, view, "newer-model . low")
	assertFooterLastLine(t, view, "thinking: off")
}
func TestFormatElapsedCompact(t *testing.T) {
	cases := []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 0, want: "0s"},
		{elapsed: 12 * time.Second, want: "12s"},
		{elapsed: time.Minute + 5*time.Second, want: "1m 05s"},
		{elapsed: time.Hour + 2*time.Minute + 9*time.Second, want: "1h 02m 09s"},
	}
	for _, tc := range cases {
		if got := formatElapsedCompact(tc.elapsed); got != tc.want {
			t.Fatalf("formatElapsedCompact(%v) = %q, want %q", tc.elapsed, got, tc.want)
		}
	}
}
func TestClearScreenResetsStateAndShowsHeader(t *testing.T) {
	m := model{
		assembler: tuirender.NewAssembler(),
		mode:      modeChat,
		width:     80,
		height:    24,
		model:     "deepseek-v4-flash",
		effort:    "high",
		cwd:       "~/work",
		version:   "v0.1.0",
	}
	// Add some state
	m.assembler.AddNotice("old notice")
	m.logs = []logEntry{{Kind: "info", Summary: "old"}}
	m.diffs = []diffEntry{{Source: "x", Line: "old"}}
	m.status = "ready"

	next, cmd := m.Update(svcMsg(protocol.Event{Kind: protocol.EventScreenClearRequested}))
	m2 := next.(model)

	if cmd == nil {
		t.Fatal("expected clear screen to return a command")
	}
	if m2.status != "terminal cleared" {
		t.Fatalf("expected status 'terminal cleared', got %q", m2.status)
	}
	if len(m2.logs) != 0 {
		t.Fatalf("expected logs cleared, got %d", len(m2.logs))
	}
	if len(m2.diffs) != 0 {
		t.Fatalf("expected diffs cleared, got %d", len(m2.diffs))
	}
	// The transcript is cleared; the header is rendered as the first chat item.
	snap := m2.assembler.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected empty live assembler, got %+v", snap)
	}
	if len(m2.transcript) != 0 {
		t.Fatalf("expected empty transcript, got %d: %+v", len(m2.transcript), m2.transcript)
	}
	view := m2.View()
	if strings.Contains(view, "WHALE") || strings.Contains(view, "██╗") {
		t.Fatalf("expected startup header to be printed to scrollback, not the live viewport:\n%s", view)
	}
	if !m2.startupHeaderPrinted {
		t.Fatal("expected clear screen to schedule startup header print")
	}
	if m2.nativeScrollbackPrinted != len(m2.transcript) {
		t.Fatalf("expected clear screen to reset native scrollback cursor, got cursor %d for %d transcript items", m2.nativeScrollbackPrinted, len(m2.transcript))
	}
}
func TestClearScreenInvalidatesRenderedChatCache(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "high", "on")
	m.width = 80
	m.height = 24
	m.appendTranscript("assistant", tuirender.KindText, "old cached content")
	if view := m.View(); !strings.Contains(view, "old cached content") {
		t.Fatalf("expected old content before clear:\n%s", view)
	}

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventScreenClearRequested}))
	m = next.(model)
	view := m.View()
	if strings.Contains(view, "old cached content") {
		t.Fatalf("expected first clear to remove cached content:\n%s", view)
	}
	if strings.Contains(view, "WHALE") || strings.Contains(view, "██╗") {
		t.Fatalf("expected startup header to land in scrollback after clear, not the live viewport:\n%s", view)
	}
	if !m.startupHeaderPrinted {
		t.Fatal("expected first clear to schedule startup header print")
	}
}
