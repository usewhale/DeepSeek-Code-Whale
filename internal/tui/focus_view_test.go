package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
)

func TestProjectFocusMessagesHidesThinkingAndToolDetails(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "inspect this"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "private reasoning"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran go test\nok"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "done"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectFocusMessages(messages), 100), "\n")
	for _, hidden := range []string{"private reasoning", "Ran go test", "\nok"} {
		if strings.Contains(rendered, hidden) {
			t.Fatalf("focus view leaked %q:\n%s", hidden, rendered)
		}
	}
	for _, want := range []string{"inspect this", "Read 1 file: internal/tui/model.go, Ran shell: go test", "(ctrl+o to expand)", "done"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("focus view missing %q:\n%s", want, rendered)
		}
	}
}

func TestProjectFocusMessagesHidesApprovalAuditNotices(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "run it"},
		{Role: "notice", Kind: tuirender.KindNotice, Text: "Cached approval", Notice: &tuirender.SystemNotice{Kind: "approval_cached_allowed", Action: "Cached", Subject: "approval"}},
		{Role: "notice", Kind: tuirender.KindNotice, Text: "Approved to run git status", Notice: &tuirender.SystemNotice{Kind: "approval_allowed_for_session", Action: "Approved", Detail: "to run", Command: "git status", Scope: "for this session"}},
		{Role: "assistant", Kind: tuirender.KindText, Text: "done"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectFocusMessages(messages), 100), "\n")
	if strings.Contains(rendered, "Cached approval") {
		t.Fatalf("focus view leaked cached approval audit notice:\n%s", rendered)
	}
	for _, want := range []string{"run it", "Approved to run git status", "done"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("focus view missing %q:\n%s", want, rendered)
		}
	}
}

func TestProjectFocusMessagesKeepsSingleShellCommandVisible(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nOn branch main"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	rendered := projected[0].Text
	for _, want := range []string{"Ran shell: git status", "(ctrl+o to expand)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("focus view missing shell command %q:\n%s", want, rendered)
		}
	}
	if projected[0].FocusSummary == nil {
		t.Fatalf("expected structured focus summary")
	}
	if got := projected[0].FocusSummary.Parts; len(got) != 1 || got[0].Kind != "shell" || got[0].Action != "Ran shell" || got[0].Detail != "git status" {
		t.Fatalf("unexpected focus summary parts: %+v", got)
	}
	if strings.Contains(rendered, "On branch main") {
		t.Fatalf("focus view should still hide shell output:\n%s", rendered)
	}
}

func TestProjectFocusMessagesShowsNonZeroShellExitWithoutOutput(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_nonzero", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran false (exit 1)\nexit 1 · 33ms\nhidden-output"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	rendered := projected[0].Text
	for _, want := range []string{"Ran shell: false (exit 1)", "(1 exited non-zero)", "(ctrl+o to expand)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("focus view missing non-zero shell detail %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "hidden-output") || strings.Contains(rendered, "33ms") {
		t.Fatalf("focus view should hide non-zero shell output details:\n%s", rendered)
	}
	if projected[0].FocusSummary == nil {
		t.Fatalf("expected structured focus summary")
	}
	got := projected[0].FocusSummary.Parts
	if len(got) != 1 || got[0].Kind != "shell" || got[0].State != "nonzero" || got[0].Detail != "false (exit 1)" {
		t.Fatalf("unexpected focus summary parts: %+v", got)
	}
}

func TestProjectFocusMessagesShowsRecoveredShellRetryCost(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Retried shell: gh pr create (1 failed, 1 succeeded) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected recovered shell summary:\nwant: %q\n got: %q", want, got)
	}
	part := projected[0].FocusSummary.Parts[0]
	if part.Kind != "shell" || part.State != "done" || part.Action != "Retried shell" || part.Detail != "gh pr create" || part.Status != "(1 failed, 1 succeeded)" {
		t.Fatalf("unexpected recovered shell part: %+v", part)
	}
}

func TestProjectFocusMessagesRecoversShellRetryWithResultOutput(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✗ · 1.2s\nFAIL"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✓ · 1.1s\nok"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Retried shell: make test (1 failed, 1 succeeded) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected recovered shell summary with output:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesRecoversMultipleFailedShellAttempts(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✗ · 1.2s\nFAIL"},
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✗ · 1.1s\nFAIL"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✓ · 1s\nok"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Retried shell: make test (2 failed, 1 succeeded) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected recovered shell summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "Failed shell") {
		t.Fatalf("recovered repeated retry should not leave a failed shell part:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesKeepsFailureAfterRecoveredShellRetry(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✗ · 1.2s\nFAIL"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✓ · 1s\nok"},
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test", Text: "Ran make test\n✗ · 1.1s\nFAIL"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{
		"Command failed: make test",
		"Retried shell: make test (1 failed, 1 succeeded)",
	} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
}

func TestProjectFocusMessagesKeepsUnrelatedShellFailureVisible(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran go test ./internal/tui\nFAIL"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{
		"Command failed: go test ./internal/tui",
		"Ran shell: git status",
		"(1 failed)",
	} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
	if strings.Contains(projected[0].Text, "Retried shell") || strings.Contains(projected[0].Text, "1 succeeded") {
		t.Fatalf("unrelated shell success should not be marked recovered:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesKeepsUnrecoveredShellFailureVisible(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran go test ./internal/tui\nFAIL"},
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{
		"Retried shell: gh pr create (1 failed, 1 succeeded)",
		"Command failed: go test ./internal/tui",
	} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
	if strings.Contains(projected[0].Text, "Command failed: 2 shell commands") {
		t.Fatalf("recovered retry should be split from unresolved shell failure:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesKeepsUnrelatedSuccessfulShellVisibleWithRecoveredRetry(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{
		"Retried shell: gh pr create (1 failed, 1 succeeded)",
		"Ran shell: git status",
	} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
}

func TestProjectFocusMessagesDoesNotRecoverShellByTruncatedPrefix(t *testing.T) {
	prefix := "go test ./internal/tui -run TestProjectFocusMessagesDoesNotRecoverShellByTruncatedPrefix"
	failedCommand := prefix + "A -count=1"
	successCommand := prefix + "B -count=1"
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran " + failedCommand + "\nFAIL"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran " + successCommand + "\nok"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{"Command failed:", "Ran shell:"} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
	if strings.Contains(projected[0].Text, "Retried shell") || strings.Contains(projected[0].Text, "1 succeeded") {
		t.Fatalf("commands with only matching truncated display should not be recovered:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesDoesNotRecoverShellBySignificantWhitespace(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran printf 'a  b'"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran printf 'a b'"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{"Command failed: printf 'a b'", "Ran shell: printf 'a b'"} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
	if strings.Contains(projected[0].Text, "Retried shell") || strings.Contains(projected[0].Text, "1 succeeded") {
		t.Fatalf("commands that differ by shell-significant whitespace should not be recovered:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesDoesNotRecoverShellByFirstLineOnly(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "printf setup\nfalse", Text: "Ran printf setup\n✗ · 1ms\nFAIL"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "printf setup\ntrue", Text: "Ran printf setup\n✓ · 1ms\nok"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{"Command failed: printf setup", "Ran shell: printf setup"} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
	if strings.Contains(projected[0].Text, "Retried shell") || strings.Contains(projected[0].Text, "1 succeeded") {
		t.Fatalf("multi-line commands that differ after the first line should not be recovered:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesDoesNotRecoverShellAcrossCWD(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test\x00cwd=internal/tui", Text: "Ran make test\nFAIL"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", ToolIdentity: "make test\x00cwd=internal/app", Text: "Ran make test\nok"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{"Command failed: make test", "Ran shell: make test"} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary missing %q:\n%s", want, projected[0].Text)
		}
	}
	for _, want := range []string{"(cwd: internal/tui)", "(cwd: internal/app)"} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("focus summary should disambiguate cwd with %q:\n%s", want, projected[0].Text)
		}
	}
	if strings.Contains(projected[0].Text, "Retried shell") || strings.Contains(projected[0].Text, "1 succeeded") {
		t.Fatalf("same command in different cwd should not be recovered:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesKeepsDeniedBeforeRecoveredShellRetry(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_denied", Kind: tuirender.KindToolCall, ToolName: "edit_file", Text: "Edited internal/tui/focus_view.go"},
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr create"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	deniedAt := strings.Index(projected[0].Text, "Denied 1 file")
	retriedAt := strings.Index(projected[0].Text, "Retried shell")
	if deniedAt < 0 || retriedAt < 0 {
		t.Fatalf("focus summary missing denied or retried part:\n%s", projected[0].Text)
	}
	if deniedAt > retriedAt {
		t.Fatalf("denied work should remain before recovered retry:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesCollapsesSubagentCells(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "inspect this"},
		{
			Role:     "result_running",
			Kind:     tuirender.KindSubagent,
			ToolName: "spawn_subagent",
			Text:     "Subagent review running\nsession: child-123\ncurrent: read_file\ndetail: reading internal/tasks/runner.go",
			SubagentSteps: []protocol.ProgressStep{
				{ToolName: "read_file", Status: "running", Summary: "reading internal/tasks/runner.go"},
			},
		},
		{Role: "assistant", Kind: tuirender.KindText, Text: "done"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectFocusMessages(messages), 100), "\n")
	// Subagent cards pass through focus unchanged; they are not collapsed
	// into the generic tool summary.
	for _, want := range []string{"inspect this", "Subagent review", "read_file: reading internal/tasks/runner.go", "done"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("focus view missing subagent card %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Subagent explore") {
		t.Fatalf("focus view mislabeled review subagent as explore:\n%s", rendered)
	}
}

func TestProjectFocusMessagesExpandedShowsHiddenDetailsWithCollapseHint(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "inspect this"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "private reasoning"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran go test\nok"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "done"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectExpandedFocusMessages(messages, false), 100), "\n")
	for _, want := range []string{"private reasoning", "Ran go test", "ok", "(ctrl+o to collapse)", "done"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expanded focus view missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "(ctrl+o to expand)") {
		t.Fatalf("expanded focus view should not show expand hint:\n%s", rendered)
	}
}

func TestProjectFocusMessagesExpandedDoesNotAnnotateThinkingText(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "think", Kind: tuirender.KindThinking, Text: "The\nnext thought"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectExpandedFocusMessages(messages, false), 100), "\n")
	if !strings.Contains(rendered, "The") || !strings.Contains(rendered, "next thought") {
		t.Fatalf("expanded focus view missing thinking text:\n%s", rendered)
	}
	if strings.Contains(rendered, "ctrl+o") {
		t.Fatalf("expanded focus view should not inject toggle hint into thinking text:\n%s", rendered)
	}
}

func TestProjectFocusMessagesExpandedCanShowFullReasoning(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "think", Kind: tuirender.KindThinking, Text: strings.Join([]string{"alpha", "bravo", "charlie", "delta", "echo"}, "\n")},
	}

	rendered := strings.Join(tuirender.ChatLines(projectExpandedFocusMessages(messages, true), 100), "\n")
	for _, want := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expanded focus view missing full reasoning %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "omitted") || strings.Contains(rendered, "scrolled past") {
		t.Fatalf("expanded focus view should not preview full reasoning:\n%s", rendered)
	}
}

func TestProjectNormalMessagesGroupsConsecutiveExploration(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus in internal/tui"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/chat_tools.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "list_dir", Text: "Explored\nList internal/tui"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectNormalMessages(messages, false), 100), "\n")
	for _, want := range []string{
		"• Explored",
		"  └ Search focus in internal/tui",
		"    Read internal/tui/focus_view.go, internal/tui/chat_tools.go",
		"    List internal/tui",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("normal exploration group missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "ctrl+o") {
		t.Fatalf("normal exploration group should not add focus toggle hints:\n%s", rendered)
	}
}

func TestProjectNormalMessagesShowsRunningExploration(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_running", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__read_text_file", Text: "Running internal/tui/focus_view.go"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectNormalMessages(messages, false), 100), "\n")
	for _, want := range []string{"• Exploring", "Read internal/tui/focus_view.go"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("normal exploration group missing %q:\n%s", want, rendered)
		}
	}
}

func TestProjectNormalMessagesFlushesExplorationBeforeNonExplore(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "edit_file", Text: "Edited internal/tui/model.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/render/chat.go"},
	}

	projected := projectNormalMessages(messages, false)
	if len(projected) != 3 {
		t.Fatalf("expected exploration/edit/exploration cells, got %d: %+v", len(projected), projected)
	}
	if projected[0].Role != "exploration_group" || projected[1].ToolName != "edit_file" || projected[2].Role != "exploration_group" {
		t.Fatalf("unexpected normal projection: %+v", projected)
	}
}

func TestProjectNormalMessagesDoesNotGroupGitStatusOrDiff(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git diff\n(no output)"},
	}

	rendered := strings.Join(tuirender.ChatLines(projectNormalMessages(messages, false), 100), "\n")
	if strings.Contains(rendered, "Explored") || strings.Contains(rendered, "Exploring") {
		t.Fatalf("git shell commands should not be grouped as exploration:\n%s", rendered)
	}
	for _, want := range []string{"Ran git status", "Ran git diff", "(ctrl+o to collapse)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("normal shell rendering missing %q:\n%s", want, rendered)
		}
	}
}

func TestProjectNormalMessagesDoesNotGroupMCPWithoutRealDetail(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__read_text_file", Text: "Ran mcp__fs__read_text_file"},
	}

	projected := projectNormalMessages(messages, false)
	if len(projected) != 1 {
		t.Fatalf("expected one ungrouped MCP message, got %+v", projected)
	}
	if projected[0].Role == "exploration_group" {
		t.Fatalf("MCP message without real detail should not be grouped: %+v", projected[0])
	}
	rendered := strings.Join(tuirender.ChatLines(projected, 100), "\n")
	if strings.Contains(rendered, "Read file") || strings.Contains(rendered, "Search content") {
		t.Fatalf("MCP message without real detail should not fall back to generic exploration labels:\n%s", rendered)
	}
}

func TestCompletedMCPDisplayUsesUserFacingLabel(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		previous string
		want     string
	}{
		{
			name:     "read",
			toolName: "mcp__fs__read_text_file",
			previous: "Exploring\nRead internal/tui/focus_view.go",
			want:     "Called MCP fs · read_text_file",
		},
		{
			name:     "list",
			toolName: "mcp__fs__list_directory",
			previous: "Exploring\nList internal/tui",
			want:     "Called MCP fs · list_directory",
		},
		{
			name:     "search",
			toolName: "mcp__fs__search_files",
			previous: "Exploring\nSearch focus in internal/tui",
			want:     "Called MCP fs · search_files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := completedToolTitle(tt.toolName, `{"success":true}`, tt.previous); got != tt.want {
				t.Fatalf("completed MCP title = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectNormalMessagesKeepsCompletedMCPDisplay(t *testing.T) {
	title := completedToolTitle("mcp__fs__read_text_file", `{"success":true}`, "Exploring\nRead internal/tui/focus_view.go")
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__read_text_file", Text: title},
	}

	rendered := strings.Join(tuirender.ChatLines(projectNormalMessages(messages, false), 100), "\n")
	for _, want := range []string{"Called MCP fs · read_text_file", "(ctrl+o to collapse)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("completed MCP display missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "• Explored") || strings.Contains(rendered, "Read file") {
		t.Fatalf("completed MCP display should not become generic exploration:\n%s", rendered)
	}
}

func TestModelChatMessagesApplyFocusView(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.viewMode = protocol.ViewModeFocus
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "question"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "hidden thought"},
		{Role: "tool", Kind: tuirender.KindToolCall, ToolName: "edit_file", Text: "Edited internal/tui/focus_view.go"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "answer"},
	}

	got := strings.Join(tuirender.ChatLines(m.chatMessages(), 100), "\n")
	if strings.Contains(got, "hidden thought") || strings.Contains(got, "\nEdited internal/tui/focus_view.go") {
		t.Fatalf("focus chat leaked hidden entries:\n%s", got)
	}
	if !strings.Contains(got, "Editing 1 file: internal/tui/focus_view.go (1 running)") || !strings.Contains(got, "answer") {
		t.Fatalf("focus chat missing summary/final answer:\n%s", got)
	}
}

func TestModelChatMessagesFocusHidesReasoningEvenWhenShowReasoningEnabled(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.viewMode = protocol.ViewModeFocus
	m.showReasoning = true
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "question"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "hidden thought"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "answer"},
	}

	got := strings.Join(tuirender.ChatLines(m.chatMessages(), 100), "\n")
	if strings.Contains(got, "hidden thought") {
		t.Fatalf("focus chat leaked reasoning despite show_reasoning=true:\n%s", got)
	}
	if !strings.Contains(got, "answer") {
		t.Fatalf("focus chat missing final answer:\n%s", got)
	}
}

func TestProjectFocusMessagesKeepsRunningHintVisible(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_running", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Reading 1 file: internal/tui/focus_view.go (1 running) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected running focus summary:\nwant: %q\n got: %q", want, got)
	}
	if projected[0].FocusSummary == nil || len(projected[0].FocusSummary.Parts) != 1 {
		t.Fatalf("expected one structured running summary part: %+v", projected[0].FocusSummary)
	}
	part := projected[0].FocusSummary.Parts[0]
	if part.Kind != "read" || part.Action != "Reading 1 file" || part.Detail != "internal/tui/focus_view.go" || part.Status != "(1 running)" {
		t.Fatalf("unexpected running summary part: %+v", part)
	}
}

func TestProjectFocusMessagesKeepsCompletedHints(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "list_dir", Text: "Explored\nList internal/tui"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := `Searched for 1 pattern: "focus", Read 1 file: internal/tui/model.go, Listed 1 directory: internal/tui (ctrl+o to expand)`
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected completed focus summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesUsesStableSemanticSummaryOrder(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "edit_file", Text: "Edited internal/tui/focus_view.go"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran go test ./internal/tui\nok"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 2 {
		t.Fatalf("expected edit to break focus summaries, got %d: %+v", len(projected), projected)
	}
	wants := []string{
		"Edited 1 file: internal/tui/focus_view.go (ctrl+o to expand)",
		"Read 1 file: internal/tui/model.go, Ran shell: go test ./internal/tui (ctrl+o to expand)",
	}
	for i, want := range wants {
		if got := projected[i].Text; got != want {
			t.Fatalf("unexpected ordered semantic summary %d:\nwant: %q\n got: %q", i, want, got)
		}
	}
}

func TestProjectFocusMessagesShowsCondensedEditStats(t *testing.T) {
	messages := []tuirender.UIMessage{
		{
			Role:     "result_ok",
			Kind:     tuirender.KindToolCall,
			ToolName: "edit_file",
			Text:     "Edited internal/tui/focus_view.go\n✓ · 1 replacement\n\ninternal/tui/focus_view.go (+1 -1)\n   10 -old\n   10 +new",
		},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Edited 1 file: internal/tui/focus_view.go (+1 -1) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected condensed edit summary:\nwant: %q\n got: %q", want, got)
	}
	for _, hidden := range []string{"10 -old", "10 +new", "✓ · 1 replacement"} {
		if strings.Contains(projected[0].Text, hidden) {
			t.Fatalf("focus summary leaked expanded edit detail %q:\n%s", hidden, projected[0].Text)
		}
	}
}

func TestProjectFocusMessagesSeparatesExplorationAndEditSummaries(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch //. (internal/tui/model_test.go)"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model_test.go"},
		{
			Role:     "result_ok",
			Kind:     tuirender.KindToolCall,
			ToolName: "edit_file",
			Text:     "Edited internal/tui/model_test.go\n✓ · 1 replacement\n\ninternal/tui/model_test.go (+1 -1)\n  501 -old\n  501 +new",
		},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 2 {
		t.Fatalf("expected exploration and edit summaries to be separate, got %d: %+v", len(projected), projected)
	}
	wants := []string{
		`Searched for 1 pattern: "//. (internal/tui/model_test.go)", Read 1 file: internal/tui/model_test.go (ctrl+o to expand)`,
		"Edited 1 file: internal/tui/model_test.go (+1 -1) (ctrl+o to expand)",
	}
	for i, want := range wants {
		if got := projected[i].Text; got != want {
			t.Fatalf("unexpected focus summary %d:\nwant: %q\n got: %q", i, want, got)
		}
	}
}

func TestProjectFocusMessagesUsesURLLanguageForFetch(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_http_error", Kind: tuirender.KindToolCall, ToolName: "fetch", Text: "Explored\nFetch https://7a65d37d.whale-site.pages.dev\nHTTP 403 Forbidden"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "web_fetch", Text: "Explored\nFetch https://whale-site.pages.dev"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "HTTP 403 Forbidden: https://7a65d37d.whale-site.pages.dev (1 HTTP error), Fetched 1 URL: https://whale-site.pages.dev (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected web fetch summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "file") {
		t.Fatalf("web fetch summary should not use file wording:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesUsesAccessBlockedLanguageForExternalAccess(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_blocked", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead ../src/Tool.ts\nAccess blocked · ../src/Tool.ts"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Access blocked: ../src/Tool.ts (1 blocked) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected blocked summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "Failed") || strings.Contains(projected[0].Text, "Denied") {
		t.Fatalf("access block should not use failed/denied language:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesPreservesLegacyOutsideWorkspaceDetail(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_blocked", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead ../src/Tool.ts\nOutside workspace · ../src/Tool.ts"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Outside workspace: ../src/Tool.ts (1 blocked) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected legacy blocked summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesUsesModeHintLanguage(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_mode_hint", Kind: tuirender.KindToolCall, ToolName: "update_plan", Text: "Updated plan\nPlan mode · switch to /agent to edit"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Plan mode: switch to /agent to edit (1 mode hint) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected mode hint summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesUsesModeHintLanguageForShell(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_mode_hint", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran rm -rf tmp\nAsk mode · switch to /agent to run commands"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Ask mode: switch to /agent to run commands (1 mode hint) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected shell mode hint summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "Blocked shell") || strings.Contains(projected[0].Text, "rm -rf") {
		t.Fatalf("shell mode hint should surface the mode guidance, not the command:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesKeepsStandaloneSemanticResultDetail(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_blocked", Kind: tuirender.KindToolResult, ToolName: "read_file", Text: "Access blocked · ../src/Tool.ts"},
		{Role: "result_mode_hint", Kind: tuirender.KindToolResult, ToolName: "shell_run", Text: "Ask mode · switch to /agent to run commands"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	for _, want := range []string{
		"Access blocked: ../src/Tool.ts",
		"Ask mode: switch to /agent to run commands",
	} {
		if !strings.Contains(projected[0].Text, want) {
			t.Fatalf("standalone semantic result missing %q:\n%s", want, projected[0].Text)
		}
	}
	if strings.Contains(projected[0].Text, "Blocked 1 tool") || strings.Contains(projected[0].Text, "Mode hint (") {
		t.Fatalf("standalone semantic result collapsed to generic text:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesUsesUsageHintForInvalidSearchInput(t *testing.T) {
	messages := []tuirender.UIMessage{
		{
			Role:     "result_usage_hint",
			Kind:     tuirender.KindToolCall,
			ToolName: "grep",
			Text: `Explored
Search {"include": "*.go", "literal_text": func asString(v any) string, "limit": 10}
Invalid tool input · literal_text must be bool`,
		},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Invalid tool input: literal_text must be bool (1 blocked) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected usage hint summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "Failed") || strings.Contains(projected[0].Text, "patterns") {
		t.Fatalf("invalid search input should not render as failed patterns:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesDoesNotSplitToolSummaryOnHiddenThinking(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "consider next file"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus in internal/tui"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "consider final file"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/render/chat.go"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one merged focus summary, got %d: %+v", len(projected), projected)
	}
	want := `Searched for 1 pattern: "focus in internal/tui", Read 2 files: internal/tui/model.go; internal/tui/render/chat.go (ctrl+o to expand)`
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected merged focus summary: %q", got)
	}
}

func TestProjectFocusMessagesMergesHydratedToolDenseSequence(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "think", Kind: tuirender.KindThinking, Text: "decide first file"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "decide second file"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch projectFocusMessages"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "decide third file"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/hydration.go"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "analysis complete"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 2 {
		t.Fatalf("expected merged summary plus answer, got %d: %+v", len(projected), projected)
	}
	want := `Searched for 1 pattern: "projectFocusMessages", Read 2 files: internal/tui/focus_view.go; internal/tui/hydration.go (ctrl+o to expand)`
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected merged focus summary: %q", got)
	}
}

func TestProjectFocusMessagesSeparatesSearchReadAndList(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "list_dir", Text: "Explored\nList internal/tui"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := `Searched for 1 pattern: "focus", Read 1 file: internal/tui/model.go, Listed 1 directory: internal/tui (ctrl+o to expand)`
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected search/read/list summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesSummarizesMultipleCompletedDetails(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/render/chat.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch summary"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch render"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := `Searched for 3 patterns: "focus"; "summary" (+1), Read 3 files: internal/tui/model.go; internal/tui/focus_view.go (+1) (ctrl+o to expand)`
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected multi-detail summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesDeduplicatesRepeatedDetails(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := `Searched for 2 patterns: "focus" (+1), Read 3 files: internal/tui/focus_view.go (+2) (ctrl+o to expand)`
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected deduped summary:\nwant: %q\n got: %q", want, got)
	}
	parts := projected[0].FocusSummary.Parts
	if len(parts) != 2 {
		t.Fatalf("expected search/read parts, got %+v", parts)
	}
	if parts[0].Kind != "search" || parts[0].State != "done" || parts[0].Count != 2 {
		t.Fatalf("unexpected search part metadata: %+v", parts[0])
	}
	if parts[1].Kind != "read" || parts[1].State != "done" || parts[1].Count != 3 {
		t.Fatalf("unexpected read part metadata: %+v", parts[1])
	}
}

func TestProjectFocusMessagesSummarizesMultipleShellCommandsWithSamples(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran gh pr view\nopen"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran make test-tui\nok"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Ran 3 shell commands: git status; gh pr view (+1) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected multi-shell summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesClassifiesMCPFilesystemTools(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_denied", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__read_text_file", Text: "Ran mcp__fs__read_text_file"},
		{Role: "result_denied", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__read_text_file", Text: "Ran mcp__fs__read_text_file"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Denied 2 files (2 denied/canceled) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected MCP read summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "shell") || strings.Contains(projected[0].Text, "mcp__fs__read_text_file") {
		t.Fatalf("MCP read tool should not render as shell/tool-name noise:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesDoesNotUseMCPToolNameAsRunningHint(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_running", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__read_text_file", Text: "Running read_text_file"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Reading 1 file (1 running) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected MCP running summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "read_text_file") || strings.Contains(projected[0].Text, "mcp__fs__read_text_file") {
		t.Fatalf("MCP tool name should not render as a running hint:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesKeepsRealMCPRunningDetail(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_running", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__read_text_file", Text: "Running internal/tui/focus_view.go"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Reading 1 file: internal/tui/focus_view.go (1 running) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected MCP running detail summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesOmitsNoisyMCPServerHint(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "mcp__fs__list_allowed_directories", Text: "Called MCP fs · list_allowed_directories"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Listed 1 directory (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected MCP allowed-directories summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, ": fs") || strings.Contains(projected[0].Text, "mcp__") {
		t.Fatalf("MCP allowed-directories summary should not expose server/raw name:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesShowsUnknownMCPAsMCPTool(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "mcp__docs__resolve_symbol", Text: "Called MCP docs · resolve_symbol\nquery: Renderer"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Called 1 MCP tool: docs.resolve_symbol (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected unknown MCP summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "mcp__docs__resolve_symbol") {
		t.Fatalf("unknown MCP summary should not expose raw tool name:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesKeepsAssistantTextAsGroupBreaker(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/model.go"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "milestone"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 3 {
		t.Fatalf("expected read summary, assistant text, and search summary, got %d: %+v", len(projected), projected)
	}
	want := []string{
		"Read 1 file: internal/tui/model.go (ctrl+o to expand)",
		"milestone",
		`Searched for 1 pattern: "focus" (ctrl+o to expand)`,
	}
	for i := range want {
		if projected[i].Text != want[i] {
			t.Fatalf("unexpected projected message %d:\nwant: %q\n got: %q", i, want[i], projected[i].Text)
		}
	}
}

func TestProjectFocusMessagesKeepsStatusAndTruncatesLongShellDetail(t *testing.T) {
	longCommand := "go test ./internal/tui -run TestProjectFocusMessagesKeepsStatusAndTruncatesLongShellDetail -count=1 -v"
	messages := []tuirender.UIMessage{
		{Role: "shell_result_failed", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran " + longCommand + "\nFAIL"},
		{Role: "result_denied", Kind: tuirender.KindToolCall, ToolName: "edit_file", Text: "Edited internal/tui/focus_view.go"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	rendered := projected[0].Text
	for _, want := range []string{"Denied 1 file", "Command failed: go test ./internal/tui -run", "...", "(1 failed)", "(1 denied/canceled)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("focus view missing %q:\n%s", want, rendered)
		}
	}
	if strings.Index(rendered, "Denied 1 file") > strings.Index(rendered, "Command failed") {
		t.Fatalf("denied summary should be prioritized before failed work:\n%s", rendered)
	}
	if strings.Contains(rendered, " -count=1 -v") {
		t.Fatalf("focus view should truncate long shell detail:\n%s", rendered)
	}
}

func TestProjectFocusMessagesSeparatesDeniedShellFromCompletedShell(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
		{Role: "shell_result_denied", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran go build ./...\nDENIED"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Denied 1 shell command (1 denied/canceled), Ran shell: git status (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected mixed shell summary:\nwant: %q\n got: %q", want, got)
	}
	parts := projected[0].FocusSummary.Parts
	if len(parts) != 2 {
		t.Fatalf("expected denied/done shell parts, got %+v", parts)
	}
	if parts[0].Kind != "shell" || parts[0].State != "denied" || parts[0].Count != 1 {
		t.Fatalf("unexpected denied shell part metadata: %+v", parts[0])
	}
	if parts[1].Kind != "shell" || parts[1].State != "done" || parts[1].Count != 1 {
		t.Fatalf("unexpected completed shell part metadata: %+v", parts[1])
	}
}

func TestProjectFocusMessagesSummarizesDeniedShellWithoutPlaceholderDetails(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "shell_result_denied", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran shell command"},
		{Role: "shell_result_denied", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran shell command"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Denied 2 shell commands (2 denied/canceled) (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected denied shell summary:\nwant: %q\n got: %q", want, got)
	}
	if strings.Contains(projected[0].Text, "shell command; shell command") {
		t.Fatalf("denied shell summary should not repeat placeholder details:\n%s", projected[0].Text)
	}
}

func TestProjectFocusMessagesSummarizesMultipleSimpleUpdates(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_running", Kind: tuirender.KindToolCall, ToolName: "update_plan", Text: "Updating plan"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "update_plan", Text: "Updated plan"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "todo_update", Text: "Updated todos"},
		{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "todo_update", Text: "Updated todos"},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one focus summary, got %d: %+v", len(projected), projected)
	}
	want := "Updating plan (1 running), Updated plan, Updated todos: 2 todo updates (ctrl+o to expand)"
	if got := projected[0].Text; got != want {
		t.Fatalf("unexpected simple update summary:\nwant: %q\n got: %q", want, got)
	}
}

func TestProjectFocusMessagesFallsBackWhenTaskHasNoDetail(t *testing.T) {
	messages := []tuirender.UIMessage{
		{Role: "result_running", Kind: tuirender.KindSubagent, ToolName: "spawn_subagent", Text: "   "},
	}

	projected := projectFocusMessages(messages)
	if len(projected) != 1 {
		t.Fatalf("expected one subagent card, got %d: %+v", len(projected), projected)
	}
	if projected[0].Kind != tuirender.KindSubagent {
		t.Fatalf("expected subagent card to pass through focus unchanged, got kind: %s", projected[0].Kind)
	}
}

func TestFocusNativeScrollbackDefersToolOnlySummaries(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "high", "on")
	m.viewMode = protocol.ViewModeFocus
	m.width = 80
	m.height = 24
	m.transcript = []tuirender.UIMessage{{Role: "you", Kind: tuirender.KindText, Text: "inspect these changes"}}
	m.nativeScrollbackPrinted = len(m.transcript)

	m.transcript = append(m.transcript,
		tuirender.UIMessage{Role: "think", Kind: tuirender.KindThinking, Text: "choose first file"},
		tuirender.UIMessage{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"},
	)
	if cmd := m.flushNativeScrollbackCmd(); cmd != nil {
		t.Fatalf("expected tool-only focus summary to stay in live viewport, got print command %#v", cmd())
	}
	if m.nativeScrollbackPrinted != 1 {
		t.Fatalf("expected native scrollback cursor to stay at user prompt, got %d", m.nativeScrollbackPrinted)
	}

	m.transcript = append(m.transcript,
		tuirender.UIMessage{Role: "think", Kind: tuirender.KindThinking, Text: "choose second file"},
		tuirender.UIMessage{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "grep", Text: "Explored\nSearch focus"},
		tuirender.UIMessage{Role: "assistant", Kind: tuirender.KindText, Text: "analysis complete"},
	)
	cmd := m.flushNativeScrollbackCmd()
	if cmd == nil {
		t.Fatal("expected visible answer to flush delayed focus summaries")
	}
	printed := fmt.Sprintf("%#v", cmd())
	if !strings.Contains(printed, `Searched for 1 pattern: \"focus\", Read 1 file: internal/tui/focus_view.go`) || strings.Contains(printed, "Read 1 file: internal/tui/focus_view.go (ctrl+o to expand)\\n\\n┃") {
		t.Fatalf("expected delayed native scrollback to print one merged summary, got %s", printed)
	}
}

func TestFocusNativeScrollbackDoesNotLeakHiddenOnlyMessages(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "high", "on")
	m.viewMode = protocol.ViewModeFocus
	m.width = 80
	m.height = 24
	m.transcript = []tuirender.UIMessage{{Role: "you", Kind: tuirender.KindText, Text: "question"}}
	m.nativeScrollbackPrinted = len(m.transcript)
	m.transcript = append(m.transcript, tuirender.UIMessage{Role: "think", Kind: tuirender.KindThinking, Text: "private reasoning"})

	cmd := m.flushNativeScrollbackCmd()
	if cmd != nil {
		t.Fatalf("expected hidden-only focus messages to produce no print command, got %#v", cmd())
	}
	if m.nativeScrollbackPrinted != len(m.transcript) {
		t.Fatalf("expected hidden-only messages to be marked printed, got %d of %d", m.nativeScrollbackPrinted, len(m.transcript))
	}
}

func TestFocusNativeScrollbackFlushesDeferredToolSummaryBeforeNextVisibleMessage(t *testing.T) {
	m := newModel(nil, "deepseek-v4-flash", "high", "on")
	m.viewMode = protocol.ViewModeFocus
	m.width = 80
	m.height = 24
	m.transcript = []tuirender.UIMessage{{Role: "you", Kind: tuirender.KindText, Text: "first prompt"}}
	m.nativeScrollbackPrinted = len(m.transcript)
	m.transcript = append(m.transcript, tuirender.UIMessage{Role: "result_ok", Kind: tuirender.KindToolCall, ToolName: "read_file", Text: "Explored\nRead internal/tui/focus_view.go"})
	if cmd := m.flushNativeScrollbackCmd(); cmd != nil {
		t.Fatalf("expected tool-only focus summary to be deferred, got %#v", cmd())
	}

	m.transcript = append(m.transcript, tuirender.UIMessage{Role: "you", Kind: tuirender.KindText, Text: "next prompt"})
	cmd := m.flushNativeScrollbackCmd()
	if cmd == nil {
		t.Fatal("expected next visible message to flush delayed tool summary")
	}
	printed := fmt.Sprintf("%#v", cmd())
	for _, want := range []string{"Read 1 file: internal/tui/focus_view.go", "next prompt"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("expected delayed flush to include %q, got %s", want, printed)
		}
	}
}

func TestViewModeChangedEventRefreshesFooter(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 100
	m.height = 20
	_, _, _ = m.handleServiceEvent(serviceViewModeChanged(protocol.ViewModeFocus))

	view := m.View()
	if !strings.Contains(view, "focus") {
		t.Fatalf("footer missing focus indicator:\n%s", view)
	}
	if !strings.Contains(view, "Focus view enabled") {
		t.Fatalf("view missing focus toggle message:\n%s", view)
	}
	if got := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n"); strings.Contains(got, "Focus view enabled") {
		t.Fatalf("focus toggle message should not enter transcript:\n%s", got)
	}
	m.commitLiveTranscript(true)
	if got := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n"); strings.Contains(got, "Focus view enabled") {
		t.Fatalf("focus toggle message should not be committed later:\n%s", got)
	}
}

func TestFocusFooterIndicatorSurvivesLongDirectory(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.viewMode = protocol.ViewModeFocus
	m.cwd = "/Users/goranka/Engineer/ai/dsk/whale-output-mouse-copy"
	m.width = 90
	m.height = 12

	lines := strings.Split(m.View(), "\n")
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "focus") {
		t.Fatalf("footer missing focus indicator:\n%s", footer)
	}
}

func TestFocusToggleMessageClearsOnNextSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.width = 100
	m.height = 20
	_, _, _ = m.handleServiceEvent(serviceViewModeChanged(protocol.ViewModeFocus))
	if !strings.Contains(m.View(), "Focus view enabled") {
		t.Fatalf("missing focus toggle message:\n%s", m.View())
	}

	m.input.SetValue("next prompt")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if strings.Contains(m.View(), "Focus view enabled") {
		t.Fatalf("focus toggle message should clear on next submit:\n%s", m.View())
	}
	if len(*intents) != 1 || (*intents)[0].Input != "next prompt" {
		t.Fatalf("expected next prompt dispatch, got %+v", *intents)
	}
}

func TestCtrlOTogglesFocusView(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.width = 100
	m.height = 20
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "question"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "hidden thought"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "answer"},
	}

	expanded := strings.Join(tuirender.ChatLines(m.chatMessages(), 100), "\n")
	if !strings.Contains(expanded, "hidden thought") || !strings.Contains(expanded, "(ctrl+o to collapse)") {
		t.Fatalf("expected default view with collapse hint:\n%s", expanded)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(model)
	collapsed := strings.Join(tuirender.ChatLines(m.chatMessages(), 100), "\n")
	if m.viewMode != protocol.ViewModeFocus || strings.Contains(collapsed, "hidden thought") || !strings.Contains(collapsed, "(ctrl+o to expand)") {
		t.Fatalf("expected ctrl+o to collapse into focus view:\n%s", collapsed)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected ctrl+o to stay local, got intents %+v", *intents)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(model)
	reexpanded := strings.Join(tuirender.ChatLines(m.chatMessages(), 100), "\n")
	if m.viewMode != protocol.ViewModeDefault || !strings.Contains(reexpanded, "hidden thought") || !strings.Contains(reexpanded, "(ctrl+o to collapse)") {
		t.Fatalf("expected second ctrl+o to expand into default view:\n%s", reexpanded)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected ctrl+o to stay local, got intents %+v", *intents)
	}
}

func TestCtrlODoesNotEndBusyTurn(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.width = 100
	m.height = 20
	m.busy = true
	m.status = "running"
	m.sawReasoningThisTurn = true
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "old question"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "old hidden thought"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "old answer"},
	}
	m.nativeScrollbackPrinted = len(m.transcript)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected busy ctrl+o to redraw transcript")
	}
	if !m.busy || m.status != "running" {
		t.Fatalf("expected ctrl+o not to complete busy turn, busy=%v status=%q", m.busy, m.status)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected busy ctrl+o not to dispatch service intent, got %+v", *intents)
	}
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	if strings.Contains(rendered, "Reasoning only") || strings.Contains(rendered, "did not produce a visible answer") {
		t.Fatalf("busy ctrl+o should not append reasoning-only notice:\n%s", rendered)
	}
}

func TestCtrlORedrawsPreviouslyPrintedTranscript(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 100
	m.height = 20
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "old question"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "old hidden thought"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "old answer"},
	}
	m.nativeScrollbackPrinted = len(m.transcript)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected ctrl+o to redraw printed transcript")
	}
	if m.viewMode != protocol.ViewModeFocus {
		t.Fatalf("expected focus view, got %q", m.viewMode)
	}
	if m.nativeScrollbackPrinted != len(m.transcript) {
		t.Fatalf("expected redrawn transcript to be marked printed, got %d of %d", m.nativeScrollbackPrinted, len(m.transcript))
	}
	collapsed := m.scrollbackText(m.transcript)
	for _, want := range []string{"old question", "Ran shell: git status", "(ctrl+o to expand)", "old answer"} {
		if !strings.Contains(collapsed, want) {
			t.Fatalf("expected collapsed redraw to include %q:\n%s", want, collapsed)
		}
	}
	for _, hidden := range []string{"old hidden thought", "clean"} {
		if strings.Contains(collapsed, hidden) {
			t.Fatalf("collapsed redraw leaked %q:\n%s", hidden, collapsed)
		}
	}

	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected second ctrl+o to redraw printed transcript")
	}
	expanded := m.scrollbackText(m.transcript)
	for _, want := range []string{"old hidden thought", "Ran git status", "clean", "(ctrl+o to collapse)"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded redraw to include %q:\n%s", want, expanded)
		}
	}
}

func TestViewModeChangedEventRedrawsPreviouslyPrintedTranscript(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 100
	m.height = 20
	m.viewMode = protocol.ViewModeFocus
	m.transcript = []tuirender.UIMessage{
		{Role: "you", Kind: tuirender.KindText, Text: "old question"},
		{Role: "think", Kind: tuirender.KindThinking, Text: "old hidden thought"},
		{Role: "shell_result_ok", Kind: tuirender.KindToolCall, ToolName: "shell_run", Text: "Ran git status\nclean"},
		{Role: "assistant", Kind: tuirender.KindText, Text: "old answer"},
	}
	m.nativeScrollbackPrinted = len(m.transcript)

	collapsed := m.scrollbackText(m.transcript)
	if !strings.Contains(collapsed, "Ran shell: git status") || !strings.Contains(collapsed, "(ctrl+o to expand)") {
		t.Fatalf("test setup should start with collapsed focus transcript:\n%s", collapsed)
	}

	cmd, _, _ := m.handleServiceEvent(serviceViewModeChanged(protocol.ViewModeDefault))
	if cmd == nil {
		t.Fatal("expected /focus view-mode event to redraw printed transcript")
	}
	if m.viewMode != protocol.ViewModeDefault {
		t.Fatalf("expected default view, got %q", m.viewMode)
	}
	if m.nativeScrollbackPrinted != len(m.transcript) {
		t.Fatalf("expected redrawn transcript to be marked printed, got %d of %d", m.nativeScrollbackPrinted, len(m.transcript))
	}
	expanded := m.scrollbackText(m.transcript)
	for _, want := range []string{"old hidden thought", "Ran git status", "clean", "(ctrl+o to collapse)"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected /focus redraw to include %q:\n%s", want, expanded)
		}
	}
}

func serviceViewModeChanged(mode string) protocol.Event {
	return protocol.Event{Kind: protocol.EventViewModeChanged, ViewMode: mode, Text: protocol.ViewModeToggleMessage(mode)}
}
