package tui

import (
	"fmt"
	"net/http"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/usewhale/whale/internal/core"
)

const shellOutputPreviewLines = 6
const shellOutputHeadLines = 2
const shellOutputTailLines = 2
const shellOutputLineRunes = 220

func summarizeToolResultForChat(toolName, raw string) (string, string) {
	body := strings.TrimSpace(raw)
	if body == "" {
		return "result", body
	}
	env, ok := parseToolEnvelopeOK(body)
	if !ok {
		return "result_error", "ERROR · malformed tool result"
	}
	return summarizeToolResultEnvelope(toolName, env)
}

// summarizeToolResultStructured prefers the structured fields carried on
// tool-result events and falls back to parsing the text for events that
// predate them.
func summarizeToolResultStructured(toolName, text, outcome, code string, payload map[string]any) (string, string) {
	if env, ok := toolEnvelopeFromStructured(outcome, code, payload); ok {
		return summarizeToolResultEnvelope(toolName, env)
	}
	return summarizeToolResultForChat(toolName, text)
}

func summarizeToolResultEnvelope(toolName string, env toolResultEnvelope) (string, string) {
	successBySignal := env.success
	if !env.hasSuccess {
		switch {
		case env.hasOK && env.ok:
			successBySignal = true
		case env.code == "ok":
			successBySignal = true
		default:
			successBySignal = true
		}
	}
	if env.status != "" && env.status != "ok" && env.status != "running" && env.status != "async_launched" && env.status != "done" && env.status != "completed" && env.status != "success" && env.status != "exited" {
		successBySignal = false
	}

	switch toolDisplayKind(toolName) {
	case "shell":
		return summarizeShellResult(env, successBySignal)
	case "explore":
		return summarizeExploreResult(toolName, env, successBySignal)
	case "edit":
		return summarizeEditResult(toolName, env, successBySignal)
	case "task":
		return summarizeTaskResult(toolName, env, successBySignal)
	case "workflow":
		return summarizeWorkflowResult(env, successBySignal)
	case "mcp":
		return summarizeMCPResult(env, successBySignal)
	default:
		if !successBySignal {
			return summarizeFailedResult(env, "tool failed")
		}
		return "result_ok", "✓"
	}
}

func summarizeWorkflowResult(env toolResultEnvelope, successBySignal bool) (string, string) {
	if !successBySignal {
		return summarizeFailedResult(env, "workflow failed")
	}
	summary := firstNonEmptyLine(env.summary)
	if summary == "" {
		summary = firstNonEmptyLine(core.AsString(env.data["summary"]))
	}
	switch env.status {
	case "async_launched", "running":
		if summary != "" {
			return "result_running", "running in background · " + summary
		}
		return "result_running", "running in background"
	default:
		if summary != "" {
			return "result_ok", "✓ · " + summary
		}
		return "result_ok", "✓"
	}
}

func summarizeTaskResult(toolName string, env toolResultEnvelope, successBySignal bool) (string, string) {
	if !successBySignal {
		return summarizeFailedResult(env, "task failed")
	}
	duration := formatDurationMS(asInt64(env.metadata["duration_ms"]))
	parts := []string{"✓"}
	if duration != "" {
		parts = append(parts, duration)
	}
	switch toolName {
	case "parallel_reason":
		if count := len(anySlice(env.data["results"])); count > 0 {
			parts = append(parts, fmt.Sprintf("%d result(s)", count))
		}
		return "result_ok", strings.Join(parts, " · ")
	case "spawn_subagent":
		role := core.FirstNonEmpty(core.AsString(env.data["role"]), "explore")
		parts = append(parts, role)
		if summary := firstNonEmptyLine(core.FirstNonEmpty(core.AsString(env.data["summary"]), env.summary)); summary != "" {
			return "result_ok", strings.Join(parts, " · ") + "\n" + summary
		}
		return "result_ok", strings.Join(parts, " · ")
	default:
		return "result_ok", strings.Join(parts, " · ")
	}
}

func anySlice(v any) []any {
	if xs, ok := v.([]any); ok {
		return xs
	}
	return nil
}

type toolResultEnvelope struct {
	success    bool
	hasSuccess bool
	ok         bool
	hasOK      bool
	code       string
	message    string
	summary    string
	status     string
	data       map[string]any
	metrics    map[string]any
	payload    map[string]any
	diagnosis  map[string]any
	metadata   map[string]any
}

func summarizeShellResult(env toolResultEnvelope, successBySignal bool) (string, string) {
	exitCode := asInt(env.metrics["exit_code"])
	hasExitCode := hasInt(env.metrics["exit_code"])
	duration := formatDurationMS(asInt64(env.metrics["duration_ms"]))
	if env.status == "running" {
		reason := shellDiagnosisLabel(core.AsString(env.diagnosis["reason"]))
		if reason != "" && duration != "" {
			return "result_running", reason + " · " + duration
		}
		if reason != "" {
			return "result_running", reason
		}
		if duration != "" {
			return "result_running", "running · " + duration
		}
		return "result_running", "running"
	}
	if env.status == "cancelled" || env.status == "canceled" {
		return "result_canceled", "CANCELED"
	}

	if !successBySignal {
		return summarizeFailedShellResult(env)
	}

	if shellExitIsNoMatches(env) {
		return summarizeNoMatchShellResult(env)
	}
	if hasExitCode && exitCode != 0 {
		return summarizeNonZeroShellResult(env, exitCode, duration)
	}

	parts := []string{"✓"}
	if duration != "" {
		parts = append(parts, duration)
	}
	output := summarizeShellOutput(shellPayloadOutput(env, false))
	if output != "" {
		return "result_ok", strings.Join(parts, " · ") + "\n" + output
	}
	return "result_ok", strings.Join(parts, " · ")
}

func summarizeNonZeroShellResult(env toolResultEnvelope, exitCode int, duration string) (string, string) {
	parts := []string{fmt.Sprintf("exit %d", exitCode)}
	if exitMsg := core.ShellExitMessage(
		core.AsString(env.payload["command"]),
		exitCode,
		core.AsString(env.payload["stdout"]),
		core.AsString(env.payload["stderr"]),
	); exitMsg != "" {
		parts = append(parts, exitMsg)
	}
	if duration != "" {
		parts = append(parts, duration)
	}
	if reason := shellDiagnosisLabel(core.AsString(env.diagnosis["reason"])); reason != "" {
		parts = append(parts, reason)
	}
	output := summarizeShellOutput(shellPayloadOutput(env, true))
	if output != "" {
		return "result_nonzero", strings.Join(parts, " · ") + "\n" + output
	}
	appendEmptyStreamNotes(&parts, env)
	return "result_nonzero", strings.Join(parts, " · ")
}

func appendEmptyStreamNotes(parts *[]string, env toolResultEnvelope) {
	if strings.TrimSpace(core.AsString(env.payload["stderr"])) == "" {
		*parts = append(*parts, "stderr empty")
	}
	if strings.TrimSpace(core.AsString(env.payload["stdout"])) == "" {
		*parts = append(*parts, "stdout empty")
	}
}

func summarizeFailedShellResult(env toolResultEnvelope) (string, string) {
	if isModeBlockedCode(env.code) {
		return "result_mode_hint", summarizeModeBlocked(env, true)
	}
	if shellFailureIsNoMatches(env) {
		return summarizeNoMatchShellResult(env)
	}
	if shellFailureUsesGenericSummary(env) {
		return summarizeFailedResult(env, "command failed")
	}
	output := summarizeShellOutput(shellPayloadOutput(env, true))
	// asInt returns 0 when exit_code is missing from metrics, which is
	// safe: ShellExitMessage's find/diff/test cases are all exit-1-gated,
	// so a missing code cannot misfire as a false interpretation.
	exitMsg := core.ShellExitMessage(
		core.AsString(env.payload["command"]),
		asInt(env.metrics["exit_code"]),
		core.AsString(env.payload["stdout"]),
		core.AsString(env.payload["stderr"]),
	)
	if output == "" {
		if env.code != "exec_failed" {
			return summarizeFailedResult(env, "command failed")
		}
		parts := []string{shellFailureLabel(env)}
		if exitMsg != "" {
			parts = append(parts, exitMsg)
		}
		appendEmptyStreamNotes(&parts, env)
		return "result_failed", strings.Join(parts, " · ")
	}
	duration := formatDurationMS(asInt64(env.metrics["duration_ms"]))
	parts := []string{shellFailureLabel(env)}
	if exitMsg != "" {
		parts = append(parts, exitMsg)
	}
	if duration != "" {
		parts = append(parts, duration)
	}
	if reason := shellDiagnosisLabel(core.AsString(env.diagnosis["reason"])); reason != "" {
		parts = append(parts, reason)
	}
	return "result_failed", strings.Join(parts, " · ") + "\n" + output
}

func shellFailureLabel(env toolResultEnvelope) string {
	exitCode := asInt(env.metrics["exit_code"])
	if hasInt(env.metrics["exit_code"]) && exitCode > 0 {
		return fmt.Sprintf("Command failed (exit %d)", exitCode)
	}
	return "Command failed"
}

func shellFailureIsNoMatches(env toolResultEnvelope) bool {
	if env.code != "exec_failed" || !hasInt(env.metrics["exit_code"]) || asInt(env.metrics["exit_code"]) != 1 {
		return false
	}
	return shellExitOneIsNoMatches(env)
}

func shellExitIsNoMatches(env toolResultEnvelope) bool {
	if !hasInt(env.metrics["exit_code"]) || asInt(env.metrics["exit_code"]) != 1 {
		return false
	}
	return shellExitOneIsNoMatches(env)
}

func shellExitOneIsNoMatches(env toolResultEnvelope) bool {
	return core.ShellExitMeansNoMatches(
		core.AsString(env.payload["command"]),
		1,
		core.AsString(env.payload["stdout"]),
		core.AsString(env.payload["stderr"]),
	)
}

func summarizeNoMatchShellResult(env toolResultEnvelope) (string, string) {
	duration := formatDurationMS(asInt64(env.metrics["duration_ms"]))
	output := summarizeShellOutput(core.AsString(env.payload["stdout"]))
	if duration != "" {
		if output != "" {
			return "result_neutral", "No matches · " + duration + "\n" + output
		}
		return "result_neutral", "No matches · " + duration
	}
	if output != "" {
		return "result_neutral", "No matches\n" + output
	}
	return "result_neutral", "No matches"
}

func shellFailureUsesGenericSummary(env toolResultEnvelope) bool {
	switch env.code {
	case "request_replan", "approval_denied", "policy_denied", "permission_denied", "timeout", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func shellPayloadOutput(env toolResultEnvelope, preferStderr bool) string {
	stdout := strings.TrimRight(core.AsString(env.payload["stdout"]), "\n")
	stderr := strings.TrimRight(core.AsString(env.payload["stderr"]), "\n")
	if preferStderr {
		return joinShellOutput(stderr, stdout)
	}
	return joinShellOutput(stdout, stderr)
}

func joinShellOutput(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(xansi.Strip(part)) != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, "\n")
}

func summarizeShellOutput(text string) string {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(xansi.Strip(text)) == "" {
		return ""
	}
	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		lines = append(lines, truncateShellOutputLine(strings.TrimRight(line, "\r")))
	}
	if len(lines) <= shellOutputPreviewLines {
		return strings.Join(lines, "\n")
	}
	head := min(shellOutputHeadLines, len(lines))
	tail := min(shellOutputTailLines, len(lines)-head)
	omitted := len(lines) - head - tail
	out := make([]string, 0, head+1+tail)
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("... %d lines omitted", omitted))
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

func truncateShellOutputLine(line string) string {
	if xansi.StringWidth(line) <= shellOutputLineRunes {
		return line
	}
	return xansi.Truncate(line, shellOutputLineRunes, "...")
}

func summarizeFailedResult(env toolResultEnvelope, fallback string) (string, string) {
	exitCode := asInt(env.metrics["exit_code"])
	hasExitCode := hasInt(env.metrics["exit_code"])
	duration := formatDurationMS(asInt64(env.metrics["duration_ms"]))
	detail := firstNonEmptyLine(core.FirstNonEmpty(
		env.summary,
		core.AsString(env.payload["stderr"]),
		core.AsString(env.payload["stdout"]),
		env.message,
		core.AsString(env.data["summary"]),
		fallback,
	))

	switch env.code {
	case "request_replan":
		return "result_failed", summarizeReplanRequired(env)
	case "ask_mode_blocked", "plan_mode_blocked", "mode_blocked":
		return "result_mode_hint", summarizeModeBlocked(env, false)
	case "fetch_failed", "web_fetch_failed", "web_search_failed", "search_timeout":
		// web_fetch/web_search report timeouts under a generic failure code with
		// the timeout signal only in the message/recovery text (e.g. web_search
		// returns "duckduckgo failed: timeout; bing failed: timeout"). Detect it
		// here, where the detail is a network error string (never a parameter
		// list), so the TIMEOUT rendering survives without the broad detail scan
		// below that would false-match on tools listing a timeout_ms parameter.
		// Non-timeout failures fall through to the generic failure rendering.
		if detailIndicatesTimeout(detail) {
			if duration != "" {
				return "result_timeout", "TIMEOUT · " + duration
			}
			return "result_timeout", "TIMEOUT"
		}
		if status := httpStatusSummary(env); status != "" {
			return "result_http_error", status
		}
	case "invalid_args", "invalid_input":
		return "result_usage_hint", summarizeInvalidArgs(detail)
	case "not_file":
		return "result_blocked", summarizePathIsDirectory(env, detail)
	case "approval_denied", "policy_denied":
		return "result_denied", "DENIED · " + detail
	case "permission_denied":
		if isOutsideWorkspaceDetail(detail) {
			return "result_blocked", core.FirstNonEmpty(accessBlockedSummary(env, detail), "Access blocked")
		}
		return "result_denied", "DENIED · " + detail
	case "mcp_allowed_dirs_denied":
		if isOutsideWorkspaceDetail(detail) {
			return "result_blocked", core.FirstNonEmpty(accessBlockedSummary(env, detail), "Access blocked")
		}
		return "result_denied", "DENIED · " + detail
	case "timeout":
		if reason := shellDiagnosisLabel(core.AsString(env.diagnosis["reason"])); reason != "" {
			if duration != "" {
				return "result_timeout", "TIMEOUT · " + duration + " · " + reason
			}
			return "result_timeout", "TIMEOUT · " + reason
		}
		if duration != "" {
			return "result_timeout", "TIMEOUT · " + duration
		}
		return "result_timeout", "TIMEOUT"
	case "cancelled", "canceled":
		return "result_canceled", "CANCELED"
	}

	lower := strings.ToLower(detail + " " + env.code)
	if strings.Contains(lower, "outside") && (strings.Contains(lower, "workspace") || strings.Contains(lower, "allowed directories")) {
		return "result_blocked", core.FirstNonEmpty(accessBlockedSummary(env, detail), "Access blocked")
	}
	if strings.Contains(lower, " is a directory") || strings.Contains(lower, "use list_dir") || strings.Contains(lower, "use list_directory") {
		return "result_blocked", summarizePathIsDirectory(env, detail)
	}
	if strings.Contains(lower, "denied") || strings.Contains(lower, "approval") || strings.Contains(lower, "policy") {
		return "result_denied", "DENIED · " + detail
	}
	// The timeout/cancel fallbacks key off the error code only, never the
	// free-text detail: a recovery hint that merely lists timeout_ms as a
	// valid parameter (e.g. an invalid_input on shell_run) must not be
	// misclassified as a TIMEOUT.
	codeLower := strings.ToLower(env.code)
	if strings.Contains(codeLower, "timeout") {
		if duration != "" {
			return "result_timeout", "TIMEOUT · " + duration
		}
		return "result_timeout", "TIMEOUT"
	}
	if strings.Contains(codeLower, "cancel") {
		return "result_canceled", "CANCELED"
	}

	prefix := "✗"
	if hasExitCode && exitCode > 0 {
		prefix = fmt.Sprintf("✗ (exit %d)", exitCode)
	}
	if duration != "" {
		return "result_failed", fmt.Sprintf("%s · %s · %s", prefix, duration, detail)
	}
	return "result_failed", fmt.Sprintf("%s · %s", prefix, detail)
}

// detailIndicatesTimeout reports whether a network error detail describes a
// timeout. Used only for fetch failures, whose detail is an error string, so
// matching on phrases here cannot misfire on parameter-list hints.
func detailIndicatesTimeout(detail string) bool {
	lower := strings.ToLower(detail)
	return strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "deadline exceeded")
}

func isModeBlockedCode(code string) bool {
	return code == "ask_mode_blocked" || code == "plan_mode_blocked" || code == "mode_blocked"
}

func summarizeModeBlocked(env toolResultEnvelope, isShell bool) string {
	mode := "Current mode"
	switch env.code {
	case "ask_mode_blocked":
		mode = "Ask mode"
		if isShell {
			return mode + " · switch to /agent to run commands"
		}
		return mode + " · switch to /agent to edit"
	case "plan_mode_blocked":
		mode = "Plan mode"
		if isShell {
			return mode + " · shell command not confirmed read-only"
		}
		return mode + " · tool unavailable while planning"
	}
	if isShell {
		return mode + " · shell command unavailable"
	}
	return mode + " · tool unavailable"
}

func summarizeInvalidArgs(detail string) string {
	detail = firstNonEmptyLine(strings.TrimSpace(detail))
	if detail == "" {
		return "Invalid tool input"
	}
	if field, typ, ok := parseJSONUnmarshalFieldType(detail); ok {
		return "Invalid tool input · " + field + " must be " + typ
	}
	return "Invalid tool input · " + detail
}

func parseJSONUnmarshalFieldType(detail string) (string, string, bool) {
	const fieldMarker = " into Go struct field ."
	fieldStart := strings.Index(detail, fieldMarker)
	if fieldStart < 0 {
		return "", "", false
	}
	fieldStart += len(fieldMarker)
	fieldEnd := strings.Index(detail[fieldStart:], " ")
	if fieldEnd < 0 {
		return "", "", false
	}
	field := strings.TrimSpace(detail[fieldStart : fieldStart+fieldEnd])
	const typeMarker = " of type "
	typeStart := strings.LastIndex(detail, typeMarker)
	if typeStart < 0 {
		return "", "", false
	}
	typ := strings.TrimSpace(detail[typeStart+len(typeMarker):])
	if field == "" || typ == "" {
		return "", "", false
	}
	return field, typ, true
}

func httpStatusSummary(env toolResultEnvelope) string {
	message := strings.ToLower(strings.TrimSpace(core.FirstNonEmpty(env.message, env.summary, core.AsString(env.data["summary"]))))
	fields := strings.Fields(message)
	for i, field := range fields {
		if strings.Trim(field, ":") != "http" || i+1 >= len(fields) {
			continue
		}
		codeText := strings.Trim(fields[i+1], ".,:;")
		code := 0
		for _, r := range codeText {
			if r < '0' || r > '9' {
				code = 0
				break
			}
			code = code*10 + int(r-'0')
		}
		if code > 0 {
			if text := http.StatusText(code); text != "" {
				return fmt.Sprintf("HTTP %d %s", code, text)
			}
			return fmt.Sprintf("HTTP %d", code)
		}
	}
	return ""
}

func summarizePathIsDirectory(env toolResultEnvelope, detail string) string {
	path := core.FirstNonEmpty(core.AsString(env.payload["file_path"]), core.AsString(env.payload["path"]), core.AsString(env.data["file_path"]), core.AsString(env.data["path"]))
	if path == "" {
		path = pathFromDirectoryMessage(detail)
	}
	if path != "" {
		return "Path is a directory · " + path
	}
	return "Path is a directory"
}

func pathFromDirectoryMessage(detail string) string {
	before, _, ok := strings.Cut(detail, " is a directory")
	if !ok {
		return ""
	}
	return strings.TrimSpace(before)
}

func accessBlockedSummary(env toolResultEnvelope, detail string) string {
	path := core.FirstNonEmpty(core.AsString(env.payload["file_path"]), core.AsString(env.payload["path"]), core.AsString(env.data["file_path"]), core.AsString(env.data["path"]))
	if path == "" {
		path = outsideWorkspacePath(detail)
	}
	if path != "" {
		return "Access blocked · " + path
	}
	if detail != "" && isOutsideWorkspaceDetail(detail) {
		return "Access blocked · " + detail
	}
	return ""
}

func isOutsideWorkspaceDetail(detail string) bool {
	lower := strings.ToLower(detail)
	return strings.Contains(lower, "outside") && (strings.Contains(lower, "workspace") || strings.Contains(lower, "allowed directories")) ||
		strings.Contains(lower, "path outside") ||
		strings.Contains(lower, "outside allowed directories") ||
		strings.Contains(lower, "cannot access") && strings.Contains(lower, "allowed directories")
}

func outsideWorkspacePath(detail string) string {
	for _, marker := range []string{"outside workspace:", "outside workspace", "cannot access", "allowed directories:"} {
		lower := strings.ToLower(detail)
		idx := strings.Index(lower, marker)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(detail[idx+len(marker):])
		if rest == "" {
			continue
		}
		fields := strings.Fields(strings.Trim(rest, " .;:,"))
		if len(fields) > 0 {
			return strings.Trim(fields[0], " .;:,")
		}
	}
	return ""
}

func shellDiagnosisLabel(reason string) string {
	switch reason {
	case "build_test_long_running":
		return "build/test running"
	case "package_manager_long_running":
		return "package manager running"
	case "download_long_running":
		return "download running"
	case "watch_long_running":
		return "watch running"
	case "remote_command_long_running":
		return "remote command running"
	case "unknown_long_running":
		return "running in background"
	case "interactive_prompt":
		return "waiting for input"
	case "network_blocked":
		return "network blocked"
	case "foreground_timeout_too_short":
		return "timeout too short"
	case "silent_timeout":
		return "no output before timeout"
	case "build_or_test_timeout":
		return "build/test timeout"
	case "background_runtime_timeout":
		return "background timeout"
	case "interactive_or_auth":
		return "interactive/auth"
	case "ordinary_timeout":
		return "ordinary timeout"
	default:
		return ""
	}
}

func summarizeReplanRequired(env toolResultEnvelope) string {
	last := strings.TrimSpace(core.AsString(env.data["last_error"]))
	if last != "" {
		if inner, ok := parseToolEnvelopeOK(last); ok {
			_, text := summarizeFailedResult(inner, "tool failed")
			if strings.TrimSpace(text) != "" {
				return text
			}
		}
		return "✗ · " + firstNonEmptyLine(last)
	}
	if tool := strings.TrimSpace(core.AsString(env.data["tool_name"])); tool != "" {
		return "✗ · " + tool + " failed; choose a different approach"
	}
	return "✗ · tool failed; choose a different approach"
}

func summarizeExploreResult(toolName string, env toolResultEnvelope, successBySignal bool) (string, string) {
	if !successBySignal {
		return summarizeFailedResult(env, "exploration failed")
	}
	switch toolName {
	case "read_file":
		total := asInt(env.metrics["total_lines"])
		returned := asInt(env.metrics["returned_lines"])
		if total > 0 {
			return "result_ok", fmt.Sprintf("✓ · %d/%d lines", returned, total)
		}
	case "list_dir":
		items := stringSlice(firstNonEmptyAny(env.payload["items"], env.data["items"]))
		return "result_ok", fmt.Sprintf("✓ · %d items", len(items))
	case "search_files":
		total := asInt(env.metrics["total_matches"])
		if total > 0 {
			return "result_ok", fmt.Sprintf("✓ · %d matches", total)
		}
		items := stringSlice(firstNonEmptyAny(env.payload["items"], env.data["items"]))
		return "result_ok", fmt.Sprintf("✓ · %d matches", len(items))
	case "grep", "search_content":
		total := asInt(env.metrics["total_matches"])
		files := asInt(env.metrics["files_matched"])
		if files > 0 {
			return "result_ok", fmt.Sprintf("✓ · %d matches in %d files", total, files)
		}
		return "result_ok", fmt.Sprintf("✓ · %d matches", total)
	case "fetch", "web_fetch":
		status := asInt(firstNonEmptyAny(env.payload["status_code"], env.data["status_code"]))
		format := core.FirstNonEmpty(core.AsString(env.payload["format"]), core.AsString(env.data["format"]))
		if status > 0 && format != "" {
			return "result_ok", fmt.Sprintf("✓ · HTTP %d · %s", status, format)
		}
	case "web_search":
		return "result_ok", "✓"
	}
	return "result_ok", "✓"
}

// isRecoverableEditMiss reports whether a failed edit result is a
// search-not-found miss that the model is expected to self-correct by
// re-reading the file. The edit/multi_edit tools emit this code only with a
// read_file recovery hint, so it is never a terminal failure.
func isRecoverableEditMiss(env toolResultEnvelope) bool {
	return env.code == "search_not_found"
}

func summarizeEditResult(toolName string, env toolResultEnvelope, successBySignal bool) (string, string) {
	if !successBySignal {
		if isRecoverableEditMiss(env) {
			// An edit/multi_edit search miss is recoverable: the tool emits a
			// read_file recovery hint and the model self-corrects by re-reading
			// and retrying. Render it as a soft, non-error outcome (amber,
			// "Re-reading") rather than a red failure. Scoped to the edit path
			// so a non-edit tool reusing this code never gets edit-specific
			// "re-reading file" phrasing. Mirrors codex's distinct
			// patch-apply-failure cell.
			return "result_recoverable", "No match · re-reading file to retry"
		}
		return summarizeFailedResult(env, "edit failed")
	}
	switch toolName {
	case "write_file", "write":
		if n := asInt(firstNonEmptyAny(env.payload["bytes"], env.data["bytes"])); n > 0 {
			return "result_ok", fmt.Sprintf("✓ · %d bytes", n)
		}
	case "edit_file", "edit", "multi_edit":
		if n := asInt(firstNonEmptyAny(env.payload["replacements"], env.data["replacements"])); n > 0 {
			return "result_ok", fmt.Sprintf("✓ · %d %s", n, pluralize(n, "replacement", "replacements"))
		}
	}
	return "result_ok", "✓"
}
