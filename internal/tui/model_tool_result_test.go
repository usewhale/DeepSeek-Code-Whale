package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
	"strings"
	"testing"
)

func TestShellToolResultRefreshesGitBranch(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "checkout", "-B", "whale-test-base")
	runGit(t, dir, "checkout", "-b", "feat/after-shell")

	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	cmd, _, _ := m.handleServiceEvent(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-shell",
		ToolName:   "shell_run",
		Text:       `{"success":true,"code":"ok","data":{"status":"ok","metrics":{"exit_code":0},"payload":{"command":"git checkout -b feat/after-shell","stdout":"","stderr":""}}}`,
	})
	if cmd == nil {
		t.Fatal("expected shell tool result to schedule git branch refresh")
	}
	msg, ok := cmd().(gitBranchUpdatedMsg)
	if !ok {
		t.Fatalf("expected gitBranchUpdatedMsg, got %T", msg)
	}
	if msg.cwd != dir {
		t.Fatalf("expected cwd %q, got %q", dir, msg.cwd)
	}
	if msg.branch != "feat/after-shell" {
		t.Fatalf("expected refreshed branch %q, got %q", "feat/after-shell", msg.branch)
	}
}

func TestAuditOnlyAutoDeniedToolResultDoesNotRenderToolCard(t *testing.T) {
	m := newModel(nil, "", "", "")
	next, cmd := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-denied",
		ToolName:   "write",
		Text:       `{"success":false,"code":"read_only_turn_denied","message":"read-only"}`,
		Metadata: map[string]any{
			"auto_denied":   true,
			"ui_visibility": "audit",
		},
	}))
	m = next.(model)
	_ = cmd
	if len(m.transcript) != 0 {
		t.Fatalf("audit-only auto-deny should not render transcript entries: %+v", m.transcript)
	}
	if m.assembler != nil && len(m.assembler.Snapshot()) != 0 {
		t.Fatalf("audit-only auto-deny should not render live entries: %+v", m.assembler.Snapshot())
	}
}

func TestRepeatedAutoDeniedToolResultDoesNotRenderNotice(t *testing.T) {
	m := newModel(nil, "", "", "")
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-denied",
		ToolName:   "write",
		Text:       `{"success":false,"code":"read_only_turn_denied","message":"read-only"}`,
		Metadata: map[string]any{
			"auto_denied":         true,
			"ui_visibility":       "audit",
			"auto_deny_notice":    "Repeated write attempts were blocked because this turn is read-only.",
			"blocked_reason_code": "read_only_turn_denied",
		},
	}))
	m = next.(model)
	if len(m.transcript) != 0 {
		t.Fatalf("repeated audit-only auto-deny should not render transcript entries: %+v", m.transcript)
	}
	if m.assembler != nil && len(m.assembler.Snapshot()) != 0 {
		t.Fatalf("repeated audit-only auto-deny should not render live entries: %+v", m.assembler.Snapshot())
	}
}
func TestMCPLocalResultRendersAsStructuredTranscriptEntry(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 14

	m.input.SetValue("/mcp")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
			Sections: []protocol.LocalResultSection{{
				Title: "fs",
				Fields: []protocol.LocalResultField{
					{Label: "Status", Value: "failed", Tone: "error"},
					{Label: "Error", Value: "timeout", Tone: "error"},
				},
			}},
		},
	}))
	m = next.(model)

	if len(m.transcript) < 2 {
		t.Fatalf("expected command echo and mcp result, got %+v", m.transcript)
	}
	got := m.transcript[len(m.transcript)-1]
	if got.Kind != tuirender.KindLocalMCP || got.Role != "local_mcp" || got.Local == nil {
		t.Fatalf("expected local mcp transcript entry, got role=%q kind=%q local=%+v", got.Role, got.Kind, got.Local)
	}
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	for _, want := range []string{"/mcp", "MCP", "Config", "/tmp/mcp.json", "fs", "failed", "timeout"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered mcp transcript to contain %q:\n%s", want, rendered)
		}
	}
}
func TestToolResultShowsDiffMetadata(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-1",
		ToolName:   "edit",
		Text:       `edit: a.txt`,
	}))
	m = next.(model)
	next, cmd := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-1",
		ToolName:   "edit",
		Text:       `{"success":true,"data":{"payload":{"file_path":"a.txt","replacements":1}}}`,
		Metadata:   testFileDiffMetadata(),
	}))
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected wait-event command")
	}
	snap := m.assembler.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected completed tool cell to leave live assembler empty, got %+v", snap)
	}
	got := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	plain := xansi.Strip(got)
	if !strings.Contains(plain, "Edited a.txt") {
		t.Fatalf("expected completed tool cell in transcript:\n%s", got)
	}
	if !strings.Contains(plain, "✓ · 1 replacement") {
		t.Fatalf("expected edit status inline with singular wording:\n%s", got)
	}
	for _, bad := range []string{"```diff", "  └ ✓", "  └ ---"} {
		if strings.Contains(plain, bad) {
			t.Fatalf("tool diff/status should not render as nested markdown child %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{"a.txt (+1 -1)", "+whale"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected structured diff preview to contain %q:\n%s", want, got)
		}
	}
	if got := strings.Join(m.renderDiffs(), "\n"); !strings.Contains(got, "+whale") {
		t.Fatalf("expected /diff content from metadata:\n%s", got)
	}
}

func TestToolResultEditSearchNotFoundShowsRecoverableVerb(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 24}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-edit-fail",
		ToolName:   "edit",
		Text:       `edit: a.txt`,
	}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-edit-fail",
		ToolName:   "edit",
		Text:       `{"success":false,"code":"search_not_found","message":"search text not found"}`,
	}))
	m = next.(model)
	plain := xansi.Strip(strings.Join(tuirender.ChatLines(m.transcript, 100), "\n"))
	// A search_not_found miss is recoverable (the model re-reads and retries),
	// so it renders as a soft "Edit re-reading" outcome rather than a hard
	// "Edit failed" red error.
	if !strings.Contains(plain, "Edit re-reading a.txt") {
		t.Fatalf("expected recoverable edit title in transcript:\n%s", plain)
	}
	if strings.Contains(plain, "Edit failed a.txt") {
		t.Fatalf("recoverable edit miss should not use the hard-failure verb:\n%s", plain)
	}
	if strings.Contains(plain, "Edited a.txt") {
		t.Fatalf("failed edit should not use successful verb:\n%s", plain)
	}
}

func TestToolResultShowsLargeTranslationDiffTailInChat(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 120
	m.height = 30
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-translation",
		ToolName:   "write",
		Text:       `write: roadmap.md`,
	}))
	m = next.(model)
	next, cmd := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-translation",
		ToolName:   "write",
		Text:       `{"success":true,"data":{"payload":{"file_path":"roadmap.md"}}}`,
		Metadata:   largeTranslationDiffMetadata(190, 190),
	}))
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected wait-event command")
	}
	got := strings.Join(tuirender.ChatLines(m.transcript, 120), "\n")
	plain := xansi.Strip(got)
	if !strings.Contains(plain, "Edited roadmap.md") {
		t.Fatalf("expected completed tool cell in transcript:\n%s", got)
	}
	if !strings.Contains(plain, "+English 189") {
		t.Fatalf("expected output box diff preview to include translated additions:\n%s", got)
	}
	if strings.Contains(plain, "```diff") || strings.Contains(plain, "  └ ---") {
		t.Fatalf("expected output box diff preview to use structured diff styling:\n%s", got)
	}
	if strings.Contains(plain, "... diff truncated (") {
		t.Fatalf("expected translation-size diff to fit in output preview:\n%s", got)
	}
}
func TestSummarizeToolResultForChat_ShellRunSuccessShowsOutputSummary(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"ok","metrics":{"exit_code":0,"duration_ms":29},"payload":{"command":"date","stdout":"Sun May 3\n","stderr":""}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_ok" {
		t.Fatalf("expected result_ok role, got %q", role)
	}
	want := "✓ · 29ms\nSun May 3"
	if got != want {
		t.Fatalf("unexpected summary text:\nwant: %q\ngot:  %q", want, got)
	}
}
func TestSummarizeToolResultForChat_ShellWaitExitedShowsSuccess(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"exited","metrics":{"exit_code":0},"payload":{"command":"sleep 1; echo whale-background-smoke","stdout":"whale-background-smoke\n","stderr":"","done":true}}}`
	role, got := summarizeToolResultForChat("shell_wait", raw)
	if role != "result_ok" {
		t.Fatalf("expected result_ok role, got %q: %s", role, got)
	}
	want := "✓\nwhale-background-smoke"
	if got != want {
		t.Fatalf("unexpected summary text:\nwant: %q\ngot:  %q", want, got)
	}
}
func TestShellRunTranscriptKeepsStatusAndOutputSeparate(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-shell",
		ToolName:   "shell_run",
		Text:       `shell_run: {"command":"cd internal/tui && wc -l model.go model_events.go model_keys.go model_prompt.go"}`,
	}))
	m = next.(model)
	raw := `{"success":true,"code":"ok","data":{"status":"ok","metrics":{"exit_code":0,"duration_ms":23},"payload":{"command":"cd internal/tui && wc -l model.go model_events.go model_keys.go model_prompt.go","cwd":"internal/tui","stdout":"284 model.go\n202 model_events.go\n401 model_keys.go\n88 model_prompt.go\n975 total\n","stderr":""}}}`
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-shell",
		ToolName:   "shell_run",
		Text:       raw,
	}))
	m = next.(model)
	wantIdentity := "cd internal/tui && wc -l model.go model_events.go model_keys.go model_prompt.go\x00cwd=internal/tui"
	if len(m.transcript) != 1 || m.transcript[0].ToolIdentity != wantIdentity {
		t.Fatalf("shell result should preserve payload command identity, got %+v", m.transcript)
	}
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	if strings.Contains(rendered, "23ms 284 model.go") {
		t.Fatalf("status and shell output collapsed onto one line:\n%s", rendered)
	}
	for _, want := range []string{"Ran cd internal/tui && wc -l", "✓ · 23ms", "284 model.go", "202 model_events.go", "975 total"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered transcript to contain %q:\n%s", want, rendered)
		}
	}
}
func TestShellResultFallsBackToRunningCommandWhenPayloadOmitsCommand(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-shell",
		ToolName:   "shell_run",
		Text:       `shell_run: {"command":"git status"}`,
	}))
	m = next.(model)
	raw := `{"success":false,"code":"denied","message":"denied","data":{"status":"error","summary":"denied","payload":{"stderr":"","stdout":""}}}`
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-shell",
		ToolName:   "shell_run",
		Text:       raw,
	}))
	m = next.(model)
	if len(m.transcript) != 1 {
		t.Fatalf("expected one transcript message, got %+v", m.transcript)
	}
	if m.transcript[0].Text != "Ran git status\nDENIED · denied" {
		t.Fatalf("shell result should preserve previous command in title, got %q", m.transcript[0].Text)
	}
	if m.transcript[0].ToolIdentity != "git status" {
		t.Fatalf("shell result should use previous command as identity, got %q", m.transcript[0].ToolIdentity)
	}
}
func TestSummarizeToolResultForChat_ShellRunFailureShowsReason(t *testing.T) {
	raw := `{"success":false,"code":"exec_failed","message":"command failed","data":{"status":"error","summary":"command failed","metrics":{"exit_code":2,"duration_ms":1210},"payload":{"stderr":"ls: cannot access x: No such file or directory\n","stdout":""}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_failed" {
		t.Fatalf("expected result_failed role, got %q", role)
	}
	want := "Command failed (exit 2) · 1.2s\nls: cannot access x: No such file or directory"
	if got != want {
		t.Fatalf("unexpected summary text:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestSummarizeToolResultForChat_ShellRunNonZeroExitIsNotToolFailure(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"exited","summary":"command exited with code 1","metrics":{"exit_code":1,"duration_ms":33},"payload":{"command":"printf marker; exit 1","stderr":"","stdout":"marker"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_nonzero" {
		t.Fatalf("expected result_nonzero role, got %q", role)
	}
	want := "exit 1 · 33ms\nmarker"
	if got != want {
		t.Fatalf("unexpected summary text:\nwant: %q\ngot:  %q", want, got)
	}
	title := completedToolTitle("shell_run", raw, "")
	if title != "Ran printf marker; exit 1 (exit 1)" {
		t.Fatalf("unexpected non-zero title: %q", title)
	}
}

func TestSummarizeToolResultForChat_ShellRunNonZeroExitWithStderr(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"exited","summary":"command exited with code 2","metrics":{"exit_code":2,"duration_ms":1210},"payload":{"command":"ls x","stderr":"ls: cannot access x: No such file or directory\n","stdout":""}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_nonzero" {
		t.Fatalf("expected result_nonzero role, got %q", role)
	}
	want := "exit 2 · 1.2s\nls: cannot access x: No such file or directory"
	if got != want {
		t.Fatalf("unexpected summary text:\nwant: %q\ngot:  %q", want, got)
	}
	if strings.Contains(got, "Command failed") {
		t.Fatalf("non-zero command result should not render as tool failure: %q", got)
	}
}

func TestSummarizeToolResultForChat_EditSearchNotFoundIsRecoverable(t *testing.T) {
	raw := `{"success":false,"code":"search_not_found","message":"search text not found","payload":{"file_path":"a.txt","recovery":{"recommended_next_tool":"read_file"}}}`
	role, got := summarizeToolResultForChat("edit", raw)
	if role != "result_recoverable" {
		t.Fatalf("expected result_recoverable role, got %q", role)
	}
	if !strings.Contains(got, "re-reading") {
		t.Fatalf("expected re-reading summary, got %q", got)
	}
	if !isRecoverableEditMiss(parseToolEnvelope(raw)) {
		t.Fatalf("search_not_found should be classified as a recoverable edit miss")
	}
	if title := completedToolTitle("edit", raw, ""); strings.HasPrefix(title, "Edit failed") {
		t.Fatalf("recoverable miss should not render as a hard failure title: %q", title)
	}
}

func TestSummarizeToolResultForChat_NonEditSearchNotFoundIsNotRecoverable(t *testing.T) {
	// The recoverable treatment is edit-specific. A non-edit tool (e.g. MCP or a
	// custom tool) reusing code:"search_not_found" must not be shown as an
	// edit-style "re-reading" retry — it is a genuine failure for that tool.
	raw := `{"success":false,"code":"search_not_found","message":"no match in index"}`
	role, got := summarizeToolResultForChat("some_mcp_tool", raw)
	if role == "result_recoverable" {
		t.Fatalf("non-edit search miss must not be result_recoverable, got role=%q text=%q", role, got)
	}
	if strings.Contains(got, "re-reading") {
		t.Fatalf("non-edit search miss must not use edit-specific re-reading phrasing: %q", got)
	}
}

func TestRetitleEditForRole(t *testing.T) {
	// Live path: completedToolTitle re-parses the model-facing message string
	// (not JSON), so it cannot see the code and returns the "Edited" verb. The
	// authoritative role must correct it.
	cases := []struct {
		title string
		role  string
		want  string
	}{
		{"Edited calc.go", "result_recoverable", "Edit re-reading calc.go"},
		{"Edited calc.go", "result_failed", "Edit failed calc.go"},
		{"Edited calc.go", "result_error", "Edit failed calc.go"},
		{"Edited calc.go", "result_ok", "Edited calc.go"},
		// Idempotent: a title that already carries a non-success verb (JSON raw
		// path, e.g. unit tests) is left untouched.
		{"Edit re-reading calc.go", "result_recoverable", "Edit re-reading calc.go"},
		{"Edit failed calc.go", "result_failed", "Edit failed calc.go"},
	}
	for _, c := range cases {
		if got := retitleEditForRole(c.title, c.role); got != c.want {
			t.Errorf("retitleEditForRole(%q, %q) = %q, want %q", c.title, c.role, got, c.want)
		}
	}
}

func TestSummarizeToolResultForChat_ShellGrepNoMatchesIsNeutral(t *testing.T) {
	raw := `{"success":false,"code":"exec_failed","message":"command failed","data":{"status":"error","summary":"command failed","metrics":{"exit_code":1,"duration_ms":52},"payload":{"command":"grep -rn \"^func firstNonEmpty\\b\" internal/ --include='*.go' | grep -v \"core/\" | grep -v \"_test.go\"","stderr":"","stdout":""}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_neutral" {
		t.Fatalf("expected result_neutral role, got %q", role)
	}
	if got != "No matches · 52ms" {
		t.Fatalf("unexpected summary text: %q", got)
	}
	if title := completedToolTitle("shell_run", raw, ""); strings.HasPrefix(title, "Command failed") {
		t.Fatalf("no-match grep should not render as command failure title: %q", title)
	}
}

func TestSummarizeToolResultForChat_ShellGrepNoMatchesIsNeutralForExitedResult(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"exited","summary":"command exited with code 1","metrics":{"exit_code":1,"duration_ms":52},"payload":{"command":"grep -rn \"^func firstNonEmpty\\b\" internal/ --include='*.go'","stderr":"","stdout":""}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_neutral" {
		t.Fatalf("expected result_neutral role, got %q", role)
	}
	if got != "No matches · 52ms" {
		t.Fatalf("unexpected summary text: %q", got)
	}
	if title := completedToolTitle("shell_run", raw, ""); strings.HasPrefix(title, "Command failed") {
		t.Fatalf("no-match grep should not render as command failure title: %q", title)
	}
}

func TestSummarizeToolResultForChat_ShellGrepNoMatchesWithPriorOutputIsNeutral(t *testing.T) {
	raw := `{"success":false,"code":"exec_failed","message":"command failed","data":{"status":"error","summary":"command failed","metrics":{"exit_code":1,"duration_ms":16},"payload":{"command":"cd /repo && wc -l internal/tui/model_events.go && grep -n \"func handleServiceEvent\" internal/tui/model_events.go","stderr":"","stdout":"     650 internal/tui/model_events.go\n"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_neutral" {
		t.Fatalf("expected result_neutral role, got %q", role)
	}
	want := "No matches · 16ms\n     650 internal/tui/model_events.go"
	if got != want {
		t.Fatalf("unexpected summary text:\nwant: %q\ngot:  %q", want, got)
	}
	if title := completedToolTitle("shell_run", raw, ""); strings.HasPrefix(title, "Command failed") {
		t.Fatalf("grep no-match with prior output should not render as command failure title: %q", title)
	}
}
func TestSummarizeToolResultForChat_ShellExitOneWithStderrStillFails(t *testing.T) {
	raw := `{"success":false,"code":"exec_failed","message":"command failed","data":{"status":"error","metrics":{"duration_ms":20,"exit_code":1},"payload":{"command":"grep pattern missing.txt","stderr":"grep: missing.txt: No such file or directory","stdout":""}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_failed" {
		t.Fatalf("expected result_failed role, got %q", role)
	}
	if !strings.Contains(got, "Command failed (exit 1)") {
		t.Fatalf("expected real grep error to remain a command failure, got %q", got)
	}
}
func TestSummarizeToolResultForChat_PermissionDeniedExternalAccessShowsAccessBlocked(t *testing.T) {
	raw := `{"success":false,"code":"permission_denied","message":"path outside MCP fs allowed directories: /workspace not in /tmp"}`
	role, got := summarizeToolResultForChat("mcp__fs__search_files", raw)
	if role != "result_blocked" {
		t.Fatalf("expected result_blocked role, got %q", role)
	}
	want := "Access blocked · /workspace"
	if got != want {
		t.Fatalf("unexpected summary:\nwant: %q\ngot:  %q", want, got)
	}
}
func TestSummarizeToolResultForChat_PermissionDeniedPolicyShowsDenied(t *testing.T) {
	raw := `{"success":false,"code":"permission_denied","message":"shell denied by permission rule"}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_denied" {
		t.Fatalf("expected result_denied role, got %q", role)
	}
	want := "DENIED · shell denied by permission rule"
	if got != want {
		t.Fatalf("unexpected summary:\nwant: %q\ngot:  %q", want, got)
	}
}
func TestMCPToolCallRendersUserFacingLabelAndArgs(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-mcp",
		ToolName:   "mcp__fs__list_directory",
		Text:       `mcp__fs__list_directory: {"path":"/tmp/中文目录"}`,
	}))
	m = next.(model)
	snap := m.liveTranscriptMessages()
	if len(snap) != 1 {
		t.Fatalf("expected one MCP tool call, got %+v", snap)
	}
	if strings.Contains(snap[0].Text, "mcp__fs__list_directory") {
		t.Fatalf("MCP display should hide raw tool name: %q", snap[0].Text)
	}
	for _, want := range []string{"Calling MCP fs · list_directory", "path: /tmp/中文目录"} {
		if !strings.Contains(snap[0].Text, want) {
			t.Fatalf("expected %q in MCP display text: %q", want, snap[0].Text)
		}
	}
}
func TestMCPToolResultKeepsLabelArgsAndOutputSeparate(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-mcp",
		ToolName:   "mcp__fs__list_directory",
		Text:       `mcp__fs__list_directory: {"path":"/tmp/project"}`,
	}))
	m = next.(model)
	raw := `{"ok":true,"success":true,"code":"ok","data":{"server":"fs","tool":"list_directory","text":"README.md\ncmd\n"},"metadata":{"duration_ms":121,"source_tool":"mcp__fs__list_directory"}}`
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-mcp",
		ToolName:   "mcp__fs__list_directory",
		Text:       raw,
	}))
	m = next.(model)
	if len(m.transcript) != 1 {
		t.Fatalf("expected completed MCP cell in transcript, got %+v", m.transcript)
	}
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	plain := xansi.Strip(rendered)
	for _, want := range []string{"Called MCP fs · list_directory", "path: /tmp/project", "✓ · 121ms", "README.md"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered MCP transcript to contain %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "mcp__fs__list_directory") || strings.Contains(plain, "source_tool") {
		t.Fatalf("MCP transcript leaked internal fields:\n%s", plain)
	}
}
func TestMCPToolResultSummarizesStructuredOnlyContent(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-mcp",
		ToolName:   "mcp__github__get_issue",
		Text:       `mcp__github__get_issue: {"owner":"usewhale","repo":"whale","number":169}`,
	}))
	m = next.(model)
	raw := `{"ok":true,"success":true,"code":"ok","data":{"server":"github","tool":"get_issue","text":"","structured_content":{"number":169,"title":"Structured MCP output","state":"open"}},"metadata":{"duration_ms":44}}`
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-mcp",
		ToolName:   "mcp__github__get_issue",
		Text:       raw,
	}))
	m = next.(model)
	if len(m.transcript) != 1 {
		t.Fatalf("expected completed MCP cell in transcript, got %+v", m.transcript)
	}
	plain := xansi.Strip(strings.Join(tuirender.ChatLines(m.transcript, 100), "\n"))
	for _, want := range []string{"Called MCP github · get_issue", "✓ · 44ms", "number: 169", "state: open", "title: Structured MCP output"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected structured MCP transcript to contain %q:\n%s", want, plain)
		}
	}
}
func TestSummarizeToolResultForChat_MCPAllowedDirsDeniedShowsAccessBlocked(t *testing.T) {
	raw := `{"success":false,"code":"mcp_allowed_dirs_denied","message":"MCP filesystem server cannot access /workspace; allowed directories: /tmp. Use Whale built-in file tools for this path, or add the directory to the MCP server configuration."}`
	role, got := summarizeToolResultForChat("mcp__fs__search_files", raw)
	if role != "result_blocked" {
		t.Fatalf("expected result_blocked role, got %q", role)
	}
	if got != "Access blocked · /workspace" {
		t.Fatalf("unexpected summary: %q", got)
	}
}
func TestSummarizeToolResultForChat_NonShellSummarized(t *testing.T) {
	raw := `{"success":true,"data":{"metrics":{"total_matches":3},"payload":{"items":["a.go","b.go","c.go"]}}}`
	role, got := summarizeToolResultForChat("search_files", raw)
	if role != "result_ok" {
		t.Fatalf("expected result_ok role for non-shell, got: %q", role)
	}
	if got != "✓ · 3 matches" {
		t.Fatalf("expected summarized non-shell payload, got: %q", got)
	}
	if strings.Contains(got, "{") || strings.Contains(got, "payload") {
		t.Fatalf("summary must not expose raw json: %q", got)
	}
}
func TestSummarizeToolResultForChat_Denied(t *testing.T) {
	raw := `{"success":false,"code":"approval_denied","message":"tool approval denied"}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_denied" || got != "DENIED · tool approval denied" {
		t.Fatalf("unexpected denied summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_AskModeBlockedShowsProductCommands(t *testing.T) {
	raw := `{"success":false,"code":"ask_mode_blocked","message":"tool unavailable in ask mode","summary":"Current mode: ask. Ask mode only allows read-only tools. To execute or modify files, switch to agent mode with /agent or Shift+Tab. To propose a reviewed approach first, switch to plan mode with /plan or Shift+Tab.","data":{"current_mode":"ask","suggested_modes":["/agent","/plan","Shift+Tab"]}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_mode_hint" {
		t.Fatalf("expected result_mode_hint role, got %q", role)
	}
	want := "Ask mode · switch to /agent to run commands"
	if got != want {
		t.Fatalf("unexpected ask-mode summary:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestSummarizeToolResultForChat_PlanShellModeBlockedIsConcise(t *testing.T) {
	raw := `{"success":false,"code":"plan_mode_blocked","message":"shell command is not classified as safe read-only","summary":"Current mode: plan. Plan mode allows only safe read-only shell commands. This shell command is not classified as safe read-only; stay here to refine the plan and produce a <proposed_plan> block for review. Only the user or UI can switch modes.","data":{"current_mode":"plan"}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_mode_hint" {
		t.Fatalf("expected result_mode_hint role, got %q", role)
	}
	want := "Plan mode · shell command not confirmed read-only"
	if got != want {
		t.Fatalf("unexpected plan-mode shell summary:\nwant: %q\ngot:  %q", want, got)
	}
	for _, noisy := range []string{"Only the user or UI", "<proposed_plan>", "Plan mode allows only"} {
		if strings.Contains(got, noisy) {
			t.Fatalf("plan-mode shell summary leaked noisy detail %q:\n%s", noisy, got)
		}
	}
}
func TestSummarizeToolResultForChat_FetchTimeoutShowsTimeout(t *testing.T) {
	// web_fetch timeouts surface under the generic fetch_failed code with the
	// timeout signal only in the message — must still render as TIMEOUT.
	raw := `{"success":false,"code":"fetch_failed","message":"Get \"https://example.com\": context deadline exceeded"}`
	role, got := summarizeToolResultForChat("fetch", raw)
	if role != "result_timeout" || got != "TIMEOUT" {
		t.Fatalf("unexpected fetch timeout summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_WebSearchTimeoutShowsTimeout(t *testing.T) {
	// Both backends timing out surfaces under web_search_failed with the timeout
	// signal only in the detail — must still render as TIMEOUT.
	raw := `{"success":false,"code":"web_search_failed","message":"duckduckgo failed: timeout; bing failed: timeout"}`
	role, got := summarizeToolResultForChat("web_search", raw)
	if role != "result_timeout" || got != "TIMEOUT" {
		t.Fatalf("unexpected web_search timeout summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_WebSearchNonTimeoutStaysFailure(t *testing.T) {
	// A non-timeout web_search failure must not be mislabeled as TIMEOUT.
	raw := `{"success":false,"code":"web_search_failed","message":"duckduckgo returned a bot challenge and bing fallback failed: blocked"}`
	role, _ := summarizeToolResultForChat("web_search", raw)
	if role == "result_timeout" {
		t.Fatalf("non-timeout web_search failure should not render as TIMEOUT")
	}
}
func TestSummarizeToolResultForChat_FetchHTTPStatusShowsHTTPError(t *testing.T) {
	raw := `{"success":false,"code":"fetch_failed","message":"http 403"}`
	role, got := summarizeToolResultForChat("fetch", raw)
	if role != "result_http_error" || got != "HTTP 403 Forbidden" {
		t.Fatalf("unexpected HTTP summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_ReadDirectoryShowsUsageHint(t *testing.T) {
	raw := `{"success":false,"code":"not_file","message":"internal/plugins is a directory; use list_dir for directories"}`
	role, got := summarizeToolResultForChat("read_file", raw)
	if role != "result_blocked" || got != "Path is a directory · internal/plugins" {
		t.Fatalf("unexpected directory summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_InvalidArgsShowsUsageHint(t *testing.T) {
	raw := `{"success":false,"code":"invalid_args","message":"json: cannot unmarshal string into Go struct field .literal_text of type bool"}`
	role, got := summarizeToolResultForChat("grep", raw)
	if role != "result_usage_hint" || got != "Invalid tool input · literal_text must be bool" {
		t.Fatalf("unexpected invalid args summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_InvalidInputUnknownFieldNotTimeout(t *testing.T) {
	// Regression: shell_run rejecting an unknown "description" field produces a
	// recovery hint that lists timeout_ms among the valid parameters. The TUI
	// must show a usage hint, not mislabel it as TIMEOUT.
	raw := `{"success":false,"code":"invalid_input","error":"unknown field \"description\"","summary":"description is not a parameter of shell_run; supported parameters: background, command, cwd, max_runtime_ms, mode, timeout_ms, yield_time_ms.","data":{"recovery":"description is not a parameter of shell_run; supported parameters: background, command, cwd, max_runtime_ms, mode, timeout_ms, yield_time_ms."}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_usage_hint" {
		t.Fatalf("expected usage hint, got role=%q text=%q", role, got)
	}
	if !strings.HasPrefix(got, "Invalid tool input · ") {
		t.Fatalf("expected invalid-input summary, got %q", got)
	}
}
func TestSummarizeToolResultForChat_Timeout(t *testing.T) {
	raw := `{"success":false,"code":"timeout","message":"command timed out","data":{"metrics":{"duration_ms":15000}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_timeout" || got != "TIMEOUT · 15s" {
		t.Fatalf("unexpected timeout summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_ShellRunAutoBackgrounded(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"running","metrics":{"duration_ms":15000,"auto_backgrounded":true},"payload":{"task_id":"task-123","command":"go test ./internal/tui","done":false}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_running" || got != "running · 15s" {
		t.Fatalf("unexpected running summary: role=%q text=%q", role, got)
	}
}

func TestCompletedToolTitle_ShellRunRunningStaysRunning(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"running","metrics":{"duration_ms":69026,"auto_backgrounded":true},"payload":{"task_id":"task-123","command":"rustup update stable 2>&1","done":false}}}`
	got := completedToolTitle("shell_run", raw, "")
	if got != "Running rustup update stable 2>&1" {
		t.Fatalf("unexpected running shell title: %q", got)
	}
}

func TestStructuredRunningShellResultKeepsRunningTitle(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-shell",
		ToolName:   "shell_run",
		Text:       `shell_run: {"command":"sleep 120"}`,
	}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:        protocol.EventToolResult,
		ToolCallID:  "tc-shell",
		ToolName:    "shell_run",
		Text:        "running in background (task_id=task-123)",
		ToolOutcome: "success",
		ToolCode:    "ok",
		ToolPayload: map[string]any{
			"status": "running",
			"metrics": map[string]any{
				"duration_ms":       float64(1900),
				"auto_backgrounded": true,
			},
			"payload": map[string]any{
				"task_id": "task-123",
				"command": "sleep 120",
				"done":    false,
			},
		},
	}))
	m = next.(model)
	messages := append([]tuirender.UIMessage{}, m.transcript...)
	messages = append(messages, m.liveTranscriptMessages()...)
	rendered := strings.Join(tuirender.ChatLines(messages, 100), "\n")
	if !strings.Contains(rendered, "Running sleep 120") || !strings.Contains(rendered, "running · 1.9s") {
		t.Fatalf("expected running shell card in visible transcript:\n%s", rendered)
	}
	if strings.Contains(rendered, "Ran shell command") {
		t.Fatalf("running shell result should not render completed fallback title:\n%s", rendered)
	}
}

func TestStructuredRunningShellResultUsesPayloadCommandWithoutToolCall(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:        protocol.EventToolResult,
		ToolCallID:  "tc-shell",
		ToolName:    "shell_run",
		Text:        "running in background (task_id=task-123)",
		ToolOutcome: "success",
		ToolCode:    "ok",
		ToolPayload: map[string]any{
			"status": "running",
			"metrics": map[string]any{
				"duration_ms":       float64(8000),
				"auto_backgrounded": true,
			},
			"payload": map[string]any{
				"task_id": "task-123",
				"command": "sleep 120",
				"done":    false,
			},
		},
	}))
	m = next.(model)
	messages := append([]tuirender.UIMessage{}, m.transcript...)
	messages = append(messages, m.liveTranscriptMessages()...)
	rendered := strings.Join(tuirender.ChatLines(messages, 100), "\n")
	if !strings.Contains(rendered, "Running sleep 120") || !strings.Contains(rendered, "running · 8.0s") {
		t.Fatalf("expected running shell card to use payload command:\n%s", rendered)
	}
	if strings.Contains(rendered, "Running shell command") || strings.Contains(rendered, "Ran shell command") {
		t.Fatalf("running shell result should not render generic shell title:\n%s", rendered)
	}
}

func TestInterruptedRunningShellResultReplacesPendingToolCard(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30, busy: true}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-shell",
		ToolName:   "shell_run",
		Text:       `shell_run: {"command":"sleep 200"}`,
	}))
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:        protocol.EventToolResult,
		ToolCallID:  "tc-shell",
		ToolName:    "shell_run",
		Text:        "running in background (task_id=task-123)",
		ToolOutcome: "success",
		ToolCode:    "ok",
		ToolPayload: map[string]any{
			"status": "running",
			"metrics": map[string]any{
				"duration_ms":       float64(3100),
				"auto_backgrounded": true,
			},
			"payload": map[string]any{
				"task_id": "task-123",
				"command": "sleep 200",
				"done":    false,
			},
		},
	}))
	m = next.(model)
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	if strings.Count(rendered, "Running sleep 200") != 1 {
		t.Fatalf("expected one running shell card after interrupt/result merge:\n%s", rendered)
	}
	for _, want := range []string{"Conversation interrupted", "Running sleep 200", "running · 3.1s"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected transcript to contain %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Ran shell command") || strings.Contains(rendered, "Running shell command") {
		t.Fatalf("running shell result should not render generic/completed duplicate:\n%s", rendered)
	}
}

func TestSummarizeToolResultForChat_ShellRunDiagnosis(t *testing.T) {
	raw := `{"success":true,"code":"ok","data":{"status":"running","metrics":{"duration_ms":15000,"auto_backgrounded":true},"payload":{"task_id":"task-123","command":"go test ./internal/tui","done":false},"diagnosis":{"reason":"build_test_long_running","suggested_next_action":"shell_wait"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_running" || got != "build/test running · 15s" {
		t.Fatalf("unexpected diagnostic running summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_TimeoutDiagnosis(t *testing.T) {
	raw := `{"success":false,"code":"timeout","message":"command timed out","data":{"metrics":{"duration_ms":15000},"diagnosis":{"reason":"ordinary_timeout"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_timeout" || got != "TIMEOUT · 15s · ordinary timeout" {
		t.Fatalf("unexpected diagnostic timeout summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_TimeoutTooShortDiagnosis(t *testing.T) {
	raw := `{"success":false,"code":"timeout","message":"command timed out","data":{"metrics":{"duration_ms":50},"diagnosis":{"reason":"foreground_timeout_too_short"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_timeout" || got != "TIMEOUT · 50ms · timeout too short" {
		t.Fatalf("unexpected timeout-too-short summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_NetworkDiagnosis(t *testing.T) {
	raw := `{"success":false,"code":"exec_failed","message":"command failed","data":{"status":"error","metrics":{"duration_ms":20,"exit_code":1},"payload":{"stderr":"curl: (6) Could not resolve host: example.invalid"},"diagnosis":{"reason":"network_blocked"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_failed" || !strings.Contains(got, "network blocked") {
		t.Fatalf("unexpected network summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_Canceled(t *testing.T) {
	raw := `{"success":false,"code":"cancelled","message":"context canceled"}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_canceled" || got != "CANCELED" {
		t.Fatalf("unexpected canceled summary: role=%q text=%q", role, got)
	}
}
func TestSummarizeToolResultForChat_FailedNoExitCodeDoesNotShowZero(t *testing.T) {
	raw := `{"success":false,"code":"exec_failed","message":"command failed","data":{"metrics":{"duration_ms":41},"payload":{"stderr":"unknown flag: --bad"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_failed" {
		t.Fatalf("expected result_failed role, got %q", role)
	}
	if got == "Command failed (exit 0) · 41ms\nunknown flag: --bad" {
		t.Fatalf("must not show fake exit 0: %q", got)
	}
	if got != "Command failed · 41ms\nunknown flag: --bad" {
		t.Fatalf("unexpected failed summary: %q", got)
	}
}
func TestSummarizeToolResultForChat_OkWithoutSuccessField(t *testing.T) {
	raw := `{"code":"ok","data":{"status":"ok","metrics":{"exit_code":0,"duration_ms":237},"payload":{"stdout":"142.251.214.110","stderr":""}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_ok" {
		t.Fatalf("expected result_ok role, got %q", role)
	}
	if got != "✓ · 237ms\n142.251.214.110" {
		t.Fatalf("unexpected summary: %q", got)
	}
}
func TestSummarizeToolResultForChat_ShellOutputTruncated(t *testing.T) {
	stdout := strings.Join([]string{
		"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11", "l12", "l13", "l14",
	}, `\n`) + `\n`
	raw := `{"success":true,"data":{"status":"ok","payload":{"stdout":"` + stdout + `"}}}`
	role, got := summarizeToolResultForChat("shell_run", raw)
	if role != "result_ok" {
		t.Fatalf("expected result_ok role, got %q", role)
	}
	for _, want := range []string{"l1", "l2", "l13", "l14"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected compact output to keep %q, got: %q", want, got)
		}
	}
	if strings.Contains(got, "l3") || strings.Contains(got, "l12") {
		t.Fatalf("expected middle output to be omitted, got: %q", got)
	}
	if !strings.Contains(got, "10 lines omitted") {
		t.Fatalf("expected omitted output marker, got: %q", got)
	}
}
func TestToolResultUpdatesToolCellWithoutRawJSON(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolCall, ToolCallID: "tc-1", ToolName: "read_file", Text: `read_file: {"file_path":"internal/tui/model.go"}`}))
	m = next.(model)
	raw := `{"success":true,"data":{"status":"ok","metrics":{"returned_lines":24,"total_lines":100},"payload":{"file_path":"internal/tui/model.go","content":"package tui"}}}`
	next, cmd := m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolResult, ToolCallID: "tc-1", ToolName: "read_file", Text: raw}))
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected wait-event command")
	}
	snap := m.assembler.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected completed read cell to leave live assembler empty, got %+v", snap)
	}
	if got := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n"); !strings.Contains(got, "Read internal/tui/model.go") {
		t.Fatalf("expected completed read cell in transcript:\n%s", got)
	}
}
func TestMultipleToolResultsWaitForPendingToolCallsBeforeCommit(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat, width: 100, height: 30}
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolCall, ToolCallID: "todo-1", ToolName: "todo_update", Text: `todo_update: Summarize findings with severity tags`}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolCall, ToolCallID: "todo-2", ToolName: "todo_update", Text: `todo_update: Perform structured file-by-file review`}))
	m = next.(model)

	raw := `{"success":true,"data":{"count":2,"items":[]}}`
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolResult, ToolCallID: "todo-1", ToolName: "todo_update", Text: raw}))
	m = next.(model)
	if got := len(m.liveTranscriptMessages()); got != 2 {
		t.Fatalf("expected pending tool calls to stay live until all results arrive, got %d", got)
	}
	if got := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n"); strings.Contains(got, "✓") {
		t.Fatalf("first result should not create a standalone checkmark:\n%s", got)
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolResult, ToolCallID: "todo-2", ToolName: "todo_update", Text: raw}))
	m = next.(model)
	if got := len(m.assembler.Snapshot()); got != 0 {
		t.Fatalf("expected completed tool cells to be committed, got %+v", m.assembler.Snapshot())
	}
	rendered := strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	for _, want := range []string{"Todo updated", "Summarize findings with severity tags", "Perform structured file-by-file review"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered transcript to contain %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "\n┃ ✓") {
		t.Fatalf("todo results should not render as standalone checkmarks:\n%s", rendered)
	}
}
func TestTaskToolResultSummaries(t *testing.T) {
	rawParallel := `{"ok":true,"success":true,"data":{"model":"deepseek-v4-flash","results":[{"index":0,"output":"a"},{"index":1,"output":"b"}]},"metadata":{"duration_ms":42}}`
	role, got := summarizeToolResultForChat("parallel_reason", rawParallel)
	if role != "result_ok" || got != "✓ · 42ms · 2 result(s)" {
		t.Fatalf("unexpected parallel summary: role=%q got=%q", role, got)
	}
	rawSubagent := `{"ok":true,"success":true,"data":{"role":"review","summary":"no permission bypass found"},"metadata":{"duration_ms":1500}}`
	role, got = summarizeToolResultForChat("spawn_subagent", rawSubagent)
	if role != "result_ok" || got != "✓ · 1.5s · review\nno permission bypass found" {
		t.Fatalf("unexpected subagent summary: role=%q got=%q", role, got)
	}
}

func TestWorkflowAsyncLaunchRendersAsRunningNotFailed(t *testing.T) {
	raw := `{"ok":true,"success":true,"code":"ok","summary":"Find unreferenced symbols","data":{"runId":"run-123","status":"async_launched","summary":"Find unreferenced symbols"}}`
	role, got := summarizeToolResultForChat("workflow", raw)
	if role != "result_running" || got != "running in background · Find unreferenced symbols" {
		t.Fatalf("unexpected workflow launch summary: role=%q got=%q", role, got)
	}
	if title := completedToolTitle("workflow", raw, "Running dead-code-scan"); title != "Started workflow: run-123" {
		t.Fatalf("unexpected workflow title: %q", title)
	}

	m := newModel(nil, "", "", "")
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolCall, ToolCallID: "workflow-1", ToolName: "workflow", Text: `workflow: {"name":"dead-code-scan"}`}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventToolResult, ToolCallID: "workflow-1", ToolName: "workflow", Text: raw}))
	m = next.(model)
	messages := append([]tuirender.UIMessage{}, m.transcript...)
	if m.assembler != nil {
		messages = append(messages, m.assembler.Snapshot()...)
	}
	rendered := strings.Join(tuirender.ChatLines(messages, 80), "\n")
	plain := xansi.Strip(rendered)
	if strings.Contains(plain, "✗") || strings.Contains(plain, "failed") {
		t.Fatalf("workflow async launch should not render as failed:\n%s", plain)
	}
	for _, want := range []string{"Started workflow: run-123", "running in background", "Find unreferenced symbols"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q in rendered workflow launch:\n%s", want, plain)
		}
	}
}

func TestWorkflowCreateToolCallTitleUsesSaveAs(t *testing.T) {
	got := summarizeToolCallForChat("workflow", `workflow: create todo-scan`)
	if got != "Creating workflow: todo-scan" {
		t.Fatalf("unexpected workflow create title: %q", got)
	}
	got = summarizeToolCallForChat("workflow", `workflow: {"action":"create","saveAs":"todo-scan","script":"export const meta = { name: 'todo-scan' }"}`)
	if got != "Creating workflow: todo-scan" {
		t.Fatalf("unexpected workflow create json title: %q", got)
	}
}

func TestTaskActivityEventsUpdateStatusOnly(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventTaskStarted, ToolName: "spawn_subagent", Text: "spawn_subagent started · review"}))
	m = next.(model)
	if m.status != "spawn_subagent started · review" {
		t.Fatalf("unexpected status: %q", m.status)
	}
	if len(m.assembler.Snapshot()) != 0 {
		t.Fatalf("task activity event should not add transcript rows: %+v", m.assembler.Snapshot())
	}
}
func TestMCPStatusFailureUpdatesStatusAndLogOnly(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventMCPStatus, Status: "failed", Text: "MCP startup failed: fs. Run /mcp for details."}))
	m = next.(model)
	if m.status != "MCP startup failed: fs. Run /mcp for details." {
		t.Fatalf("unexpected status: %q", m.status)
	}
	if snap := m.assembler.Snapshot(); len(snap) != 0 {
		t.Fatalf("expected MCP failure to stay out of transcript, got: %+v", snap)
	}
	if len(m.logs) != 1 || m.logs[0].Kind != "mcp_status" || !strings.Contains(m.logs[0].Summary, "MCP startup failed: fs") {
		t.Fatalf("expected MCP failure log entry, got: %+v", m.logs)
	}
}
func TestTaskProgressUpdatesTaskToolRow(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       "spawn_subagent: review · inspect internal/tasks",
	}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventTaskProgress,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       "spawn_subagent running · review · reading internal/tasks/runner.go",
		Metadata: map[string]any{
			"child_session_id": "parent--subagent-tc-task",
			"child_tool":       "read_file",
			"role":             "review",
		},
	}))
	m = next.(model)
	snap := m.liveTranscriptMessages()
	if len(snap) != 1 {
		t.Fatalf("expected one tool row, got %+v", snap)
	}
	if snap[0].Kind != tuirender.KindSubagent || snap[0].Role != "result_running" {
		t.Fatalf("expected running subagent row, got %+v", snap[0])
	}
	for _, want := range []string{"Subagent review running", "session: parent--subagent-tc-task", "current: read_file", "detail: reading internal/tasks/runner.go"} {
		if !strings.Contains(snap[0].Text, want) {
			t.Fatalf("expected %q in progress row: %+v", want, snap[0])
		}
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventTaskProgress,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       `spawn_subagent running · review · Searched "TaskProgress" in internal/tui (*.go) · 7 matches in 3 files`,
		Metadata: map[string]any{
			"child_session_id": "parent--subagent-tc-task",
			"child_tool":       "grep",
			"role":             "review",
		},
	}))
	m = next.(model)
	snap = m.liveTranscriptMessages()
	if snap[0].Kind != tuirender.KindSubagent || snap[0].Role != "result_running" {
		t.Fatalf("expected running subagent row to persist: %+v", snap[0])
	}
	if !strings.Contains(snap[0].Text, "current: grep") || !strings.Contains(snap[0].Text, `detail: Searched "TaskProgress" in internal/tui (*.go) · 7 matches in 3 files`) {
		t.Fatalf("expected child tool and progress metric to be preserved: %+v", snap[0])
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventTaskProgress,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       "spawn_subagent compacted · review · Compacted child context (10 -> 3 messages)",
		Status:     "compacted",
		Metadata: map[string]any{
			"child_session_id": "parent--subagent-tc-task",
			"role":             "review",
		},
	}))
	m = next.(model)
	snap = m.liveTranscriptMessages()
	if snap[0].Kind != tuirender.KindSubagent || snap[0].Role != "result_running" || !strings.Contains(snap[0].Text, "Subagent review compacted") || !strings.Contains(snap[0].Text, "current: grep") {
		t.Fatalf("expected non-running progress status to update subagent row without losing current tool: %+v", snap[0])
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventTaskProgress,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       "spawn_subagent completed · review · Child finished",
		Status:     "completed",
		Metadata: map[string]any{
			"child_session_id": "parent--subagent-tc-task",
			"role":             "review",
		},
	}))
	m = next.(model)
	snap = m.liveTranscriptMessages()
	if snap[0].Kind != tuirender.KindSubagent || snap[0].Role != "result_ok" || !strings.Contains(snap[0].Text, "Subagent review completed") {
		t.Fatalf("expected completed progress status to update subagent row: %+v", snap[0])
	}

	result := `{"ok":true,"success":true,"data":{"role":"review","child_session_id":"parent--subagent-tc-task","summary":"no permission bypass found"},"metadata":{"duration_ms":1500}}`
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       result,
	}))
	m = next.(model)
	if len(m.transcript) == 0 {
		t.Fatalf("expected completed subagent row in transcript")
	}
	completed := m.transcript[len(m.transcript)-1]
	if completed.Kind != tuirender.KindSubagent {
		t.Fatalf("expected completed subagent row in transcript, got: %+v", completed)
	}
	for _, want := range []string{"Subagent review completed", "session: parent--subagent-tc-task", "current: grep", "duration: 1.5s", "summary: no permission bypass found"} {
		if !strings.Contains(completed.Text, want) {
			t.Fatalf("expected %q in completed row: %+v", want, completed)
		}
	}
}
func TestSubagentFailureUpdatesDedicatedCell(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       "spawn_subagent: review · inspect internal/tasks",
	}))
	m = next.(model)
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventTaskProgress,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       "spawn_subagent tool_failed · review · Read internal/tasks/runner.go failed",
		Metadata: map[string]any{
			"child_session_id": "parent--subagent-tc-task",
			"child_tool":       "read_file",
			"role":             "review",
		},
	}))
	m = next.(model)
	snap := m.liveTranscriptMessages()
	if !strings.Contains(snap[0].Text, "Subagent review failed") || !strings.Contains(snap[0].Text, "current: read_file") {
		t.Fatalf("unexpected progress row: %+v", snap[0])
	}

	result := `{"ok":false,"success":false,"code":"spawn_subagent_failed","error":"subagent failed","data":{"role":"review","child_session_id":"parent--subagent-tc-task"},"metadata":{"duration_ms":41}}`
	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-task",
		ToolName:   "spawn_subagent",
		Text:       result,
	}))
	m = next.(model)
	if len(m.transcript) == 0 {
		t.Fatalf("expected failed subagent row in transcript")
	}
	failed := m.transcript[len(m.transcript)-1]
	for _, want := range []string{"Subagent review failed", "session: parent--subagent-tc-task", "duration: 41ms", "summary: subagent failed"} {
		if !strings.Contains(failed.Text, want) {
			t.Fatalf("expected %q in failed row: %+v", want, failed)
		}
	}
}
func TestToolCallShowsSearchPatternAndPath(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-search",
		ToolName:   "grep",
		Text:       `grep: assistant_delta in internal/tui (*.go)`,
	}))
	m = next.(model)
	snap := m.liveTranscriptMessages()
	if len(snap) != 1 {
		t.Fatalf("expected one tool cell, got %+v", snap)
	}
	want := "Exploring\nSearch assistant_delta in internal/tui (*.go)"
	if snap[0].Text != want {
		t.Fatalf("unexpected search tool call text:\nwant: %q\ngot:  %q", want, snap[0].Text)
	}
}
func TestToolResultKeepsSearchDetailAndAddsSummary(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-search",
		ToolName:   "grep",
		Text:       `grep: assistant_delta in internal/tui (*.go)`,
	}))
	m = next.(model)
	raw := `{"success":true,"data":{"status":"ok","metrics":{"total_matches":1,"files_matched":1},"payload":{"matches":[]}}}`
	next, cmd := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolResult,
		ToolCallID: "tc-search",
		ToolName:   "grep",
		Text:       raw,
	}))
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected wait-event command")
	}
	snap := m.assembler.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected completed search cell to leave live assembler empty, got %+v", snap)
	}
	if got := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n"); !strings.Contains(got, "Search assistant_delta in internal/tui") {
		t.Fatalf("expected completed search cell in transcript:\n%s", got)
	}
}
func TestToolCallShowsWebSearchQuery(t *testing.T) {
	m := model{assembler: tuirender.NewAssembler(), mode: modeChat}
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind:       protocol.EventToolCall,
		ToolCallID: "tc-web",
		ToolName:   "web_search",
		Text:       `web_search: F1 pit strategy tools`,
	}))
	m = next.(model)
	snap := m.liveTranscriptMessages()
	if len(snap) != 1 {
		t.Fatalf("expected one web search cell, got %+v", snap)
	}
	want := "Exploring\nSearch web for F1 pit strategy tools"
	if snap[0].Text != want {
		t.Fatalf("unexpected web search tool call text:\nwant: %q\ngot:  %q", want, snap[0].Text)
	}
}
