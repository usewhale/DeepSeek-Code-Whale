package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

func shellCommandIdentityFromResult(raw string) string {
	env := parseToolEnvelope(raw)
	command := strings.TrimSpace(core.AsString(env.payload["command"]))
	if command == "" {
		return ""
	}
	cwd := strings.TrimSpace(core.AsString(env.payload["cwd"]))
	if cwd == "" {
		return command
	}
	return command + "\x00cwd=" + cwd
}

func summarizeToolCallForChat(toolName, text string) string {
	detail := toolCallDetail(text)
	switch toolDisplayKind(toolName) {
	case "shell":
		if detail == "" {
			detail = "shell command"
		}
		return "Running " + detail
	case "explore":
		line := explorationLine(toolName, detail, toolResultEnvelope{})
		return "Exploring\n" + line
	case "edit":
		line := editLine(toolName, detail, toolResultEnvelope{})
		return line
	case "task":
		if strings.TrimSpace(toolName) == "parallel_reason" {
			return "Parallel reasoning\n" + core.FirstNonEmpty(detail, "working")
		}
		role := taskRoleFromText(text)
		return "Subagent " + role + "\n" + core.FirstNonEmpty(detail, "starting")
	case "plan":
		return "Updating plan"
	case "todo":
		return todoToolTitle(toolName, text, "running")
	case "mcp":
		return mcpStartedText(toolName, text)
	case "workflow":
		return workflowToolCallTitle(text)
	default:
		if detail == "" {
			detail = toolName
		}
		return "Running " + detail
	}
}

func workflowToolCallTitle(text string) string {
	detail := workflowToolCallDetail(text)
	if detail == "" || detail == "workflow" {
		return "Running workflow"
	}
	if target := strings.TrimSpace(strings.TrimPrefix(detail, "create ")); target != detail && target != "" {
		return "Creating workflow: " + target
	}
	if target := strings.TrimSpace(strings.TrimPrefix(detail, "list ")); target != detail {
		return "Listing workflows"
	}
	if target := strings.TrimSpace(strings.TrimPrefix(detail, "run ")); target != detail && target != "" {
		return "Running " + target
	}
	return "Running " + detail
}

func workflowToolCallDetail(text string) string {
	t := strings.TrimSpace(text)
	if idx := strings.Index(t, ":"); idx >= 0 {
		t = strings.TrimSpace(t[idx+1:])
	}
	if strings.HasPrefix(t, "{") {
		var body map[string]any
		if err := json.Unmarshal([]byte(t), &body); err == nil {
			action := strings.TrimSpace(core.AsString(body["action"]))
			saveAs := strings.TrimSpace(core.AsString(body["saveAs"]))
			name := strings.TrimSpace(core.AsString(body["name"]))
			scriptPath := strings.TrimSpace(core.AsString(body["scriptPath"]))
			if saveAs != "" {
				if action == "" {
					action = "create"
				}
				return strings.TrimSpace(action + " " + saveAs)
			}
			if name != "" {
				if action == "" {
					return name
				}
				return strings.TrimSpace(action + " " + name)
			}
			if scriptPath != "" {
				return scriptPath
			}
			if action == "list" {
				return "list"
			}
		}
	}
	return strings.TrimSpace(t)
}

func summarizeTaskProgressForChat(toolName, text string) string {
	detail := taskProgressDetail(text)
	if strings.TrimSpace(toolName) == "parallel_reason" {
		return "Parallel reasoning\n" + core.FirstNonEmpty(detail, "running")
	}
	role := taskRoleFromText(text)
	if taskProgressFailed(text) {
		return "Subagent " + role + "\nFailed: " + core.FirstNonEmpty(detail, "subagent failed")
	}
	return "Subagent " + role + "\n" + core.FirstNonEmpty(detail, "running")
}

func taskProgressDetail(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	parts := strings.SplitN(t, "·", 3)
	if len(parts) == 3 {
		return strings.TrimSpace(parts[2])
	}
	parts = strings.Split(t, "·")
	return strings.TrimSpace(parts[len(parts)-1])
}

func taskRoleFromText(text string) string {
	t := strings.TrimSpace(text)
	if before, _, ok := strings.Cut(t, "·"); ok {
		if _, role, hasColon := strings.Cut(before, ":"); hasColon {
			role = strings.TrimSpace(role)
			if role != "" {
				return role
			}
		}
	}
	parts := strings.Split(t, "·")
	if len(parts) >= 2 {
		role := strings.TrimSpace(parts[1])
		if role != "" && !strings.Contains(role, "prompt") {
			return role
		}
	}
	return "explore"
}

func taskProgressFailed(text string) bool {
	first := strings.TrimSpace(strings.SplitN(text, "·", 2)[0])
	return strings.Contains(first, "failed")
}

func subagentStartedText(text string) string {
	role := taskRoleFromText(text)
	detail := core.FirstNonEmpty(taskProgressDetail(text), toolCallDetail(text), "starting")
	return subagentText(role, "running", "pending", "starting", detail, "", "")
}

func subagentProgressText(text, eventStatus string, metadata map[string]any, previous string) string {
	role := core.FirstNonEmpty(core.AsString(metadata["role"]), taskRoleFromText(text), previousSubagentField(previous, "role"), "explore")
	status := core.FirstNonEmpty(eventStatus, core.AsString(metadata["status"]))
	if status == "" {
		status = "running"
	}
	if taskProgressFailed(text) {
		status = "failed"
	}
	sessionID := core.FirstNonEmpty(core.AsString(metadata["child_session_id"]), previousSubagentField(previous, "session"), "pending")
	current := core.FirstNonEmpty(core.AsString(metadata["child_tool"]), previousSubagentField(previous, "current"), "starting")
	detail := core.FirstNonEmpty(taskProgressDetail(text), previousSubagentField(previous, "detail"), "running")
	return subagentText(role, status, sessionID, current, detail, "", "")
}

func subagentProgressRole(status, text string) string {
	if taskProgressFailed(text) {
		return "result_failed"
	}
	switch strings.TrimSpace(status) {
	case "completed", "done", "success", "succeeded":
		return "result_ok"
	case "failed", "error", "tool_recovery_failed":
		return "result_failed"
	case "denied", "canceled", "cancelled":
		return "result_denied"
	default:
		return "result_running"
	}
}

// subagentCompletedTextStructured prefers the structured fields on the
// event and falls back to parsing the text for events that predate them.
func subagentCompletedTextStructured(text, outcome, code string, payload map[string]any, previous string) string {
	if env, ok := toolEnvelopeFromStructured(outcome, code, payload); ok {
		return subagentCompletedTextFromEnvelope(env, previous)
	}
	return subagentCompletedText(text, previous)
}

func subagentCompletedText(raw, previous string) string {
	env, ok := parseToolEnvelopeOK(raw)
	if !ok {
		return subagentText(
			previousSubagentField(previous, "role"),
			"failed",
			previousSubagentField(previous, "session"),
			core.FirstNonEmpty(previousSubagentField(previous, "current"), "done"),
			"",
			"",
			"malformed tool result",
		)
	}
	return subagentCompletedTextFromEnvelope(env, previous)
}

func subagentCompletedTextFromEnvelope(env toolResultEnvelope, previous string) string {
	role := core.FirstNonEmpty(core.AsString(env.data["role"]), previousSubagentField(previous, "role"), "explore")
	status := "completed"
	if !toolEnvelopeSucceeded(env) {
		status = "failed"
	}
	sessionID := core.FirstNonEmpty(core.AsString(env.data["child_session_id"]), core.AsString(env.data["session_id"]), previousSubagentField(previous, "session"), "pending")
	current := core.FirstNonEmpty(previousSubagentField(previous, "current"), "done")
	duration := formatDurationMS(firstNonZeroInt64(asInt64(env.metadata["duration_ms"]), asInt64(env.data["duration_ms"])))
	summary := core.FirstNonEmpty(core.AsString(env.data["summary"]), env.summary, env.message)
	if summary == "" {
		if status == "failed" {
			summary = "subagent failed"
		} else {
			summary = "subagent completed"
		}
	}
	return subagentText(role, status, sessionID, current, "", duration, firstNonEmptyLine(summary))
}

func subagentText(role, status, sessionID, current, detail, duration, summary string) string {
	role = core.FirstNonEmpty(strings.TrimSpace(role), "explore")
	status = core.FirstNonEmpty(strings.TrimSpace(status), "running")
	lines := []string{"Subagent " + role + " " + status}
	lines = append(lines, "session: "+core.FirstNonEmpty(strings.TrimSpace(sessionID), "pending"))
	if current != "" {
		lines = append(lines, "current: "+strings.TrimSpace(current))
	}
	if detail != "" {
		lines = append(lines, "detail: "+strings.TrimSpace(detail))
	}
	if duration != "" {
		lines = append(lines, "duration: "+duration)
	}
	if summary != "" {
		lines = append(lines, "summary: "+strings.TrimSpace(summary))
	}
	return strings.Join(lines, "\n")
}

func previousSubagentField(previous, field string) string {
	field = strings.TrimSpace(field)
	if field == "role" {
		first := strings.TrimSpace(strings.SplitN(previous, "\n", 2)[0])
		parts := strings.Fields(first)
		if len(parts) >= 2 && parts[0] == "Subagent" {
			return parts[1]
		}
		return ""
	}
	prefix := field + ":"
	for _, line := range strings.Split(previous, "\n") {
		if _, rest, ok := strings.Cut(strings.TrimSpace(line), prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func toolEnvelopeSucceeded(env toolResultEnvelope) bool {
	if env.hasSuccess {
		return env.success
	}
	if env.hasOK {
		return env.ok
	}
	if env.code != "" {
		return env.code == "ok"
	}
	return env.status == "" || env.status == "ok" || env.status == "done" || env.status == "completed" || env.status == "success"
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func completedToolTitle(toolName, raw, previous string) string {
	env := parseToolEnvelope(raw)
	switch toolDisplayKind(toolName) {
	case "shell":
		cmd := strings.TrimSpace(core.AsString(env.payload["command"]))
		if cmd == "" {
			cmd = focusShellRawCommand(previous)
		}
		if cmd == "" {
			cmd = "shell command"
		}
		if env.status == "running" {
			return "Running " + cmd
		}
		if !toolEnvelopeSucceeded(env) && env.code == "exec_failed" && !shellFailureIsNoMatches(env) {
			return shellFailureLabel(env) + ": " + cmd
		}
		if hasInt(env.metrics["exit_code"]) {
			exitCode := asInt(env.metrics["exit_code"])
			if exitCode != 0 && toolEnvelopeSucceeded(env) {
				return fmt.Sprintf("Ran %s (exit %d)", cmd, exitCode)
			}
		}
		return "Ran " + cmd
	case "explore":
		return "Explored\n" + explorationLine(toolName, previousToolActionLine(previous), env)
	case "edit":
		return editLine(toolName, toolCallDetail(previousToolActionLine(previous)), env)
	case "task":
		if toolName == "parallel_reason" {
			return "Parallel reasoning"
		}
		role := core.FirstNonEmpty(core.AsString(env.data["role"]), "explore")
		return "Subagent " + role
	case "workflow":
		name := core.FirstNonEmpty(core.AsString(env.data["workflow"]), core.AsString(env.data["name"]), core.AsString(env.data["runId"]), "workflow")
		return "Started workflow: " + name
	case "plan":
		return "Updated plan"
	case "todo":
		return todoToolTitle(toolName, core.FirstNonEmpty(previousToolActionLine(previous), raw), "done")
	case "mcp":
		return mcpCompletedTitle(toolName, raw, previous)
	default:
		label := toolName
		if label == "" {
			label = "tool"
		}
		return "Ran " + label
	}
}

func shellResultRole(role string) string {
	switch strings.TrimSpace(role) {
	case "result_ok":
		return "shell_result_ok"
	case "result_neutral":
		return "shell_result_neutral"
	case "result_nonzero":
		return "shell_result_nonzero"
	case "result_failed":
		return "shell_result_failed"
	case "result_timeout":
		return "shell_result_timeout"
	case "result_denied":
		return "shell_result_denied"
	case "result_canceled":
		return "shell_result_canceled"
	case "result_error":
		return "shell_result_error"
	case "result_running":
		return "shell_result_running"
	case "result_blocked":
		return "shell_result_blocked"
	case "result_mode_hint":
		return "shell_result_mode_hint"
	case "result_http_error":
		return "shell_result_http_error"
	case "result_usage_hint":
		return "shell_result_usage_hint"
	default:
		return role
	}
}

func shouldShowUnmatchedToolResult(toolName, role, text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if (toolDisplayKind(toolName) == "todo" || toolDisplayKind(toolName) == "plan") && strings.TrimSpace(role) == "result_ok" {
		return false
	}
	return true
}

func toolDisplayKind(toolName string) string {
	name := strings.TrimSpace(toolName)
	if isMCPDisplayTool(name) {
		return "mcp"
	}
	switch name {
	case "shell_run", "shell_wait", "shell_cancel":
		return "shell"
	case "read_file", "list_dir", "search_files", "grep", "search_content", "fetch", "web_fetch", "web_search":
		return "explore"
	case "write_file", "edit_file", "write", "edit", "multi_edit":
		return "edit"
	case "parallel_reason", "spawn_subagent":
		return "task"
	case "workflow":
		return "workflow"
	case "update_plan":
		return "plan"
	case "todo_add", "todo_list", "todo_update", "todo_remove", "todo_clear_done":
		return "todo"
	default:
		return "unknown"
	}
}

func mcpExplorationKind(toolName string) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if !strings.HasPrefix(name, "mcp__") {
		return ""
	}
	switch {
	case strings.Contains(name, "read_text_file"), strings.Contains(name, "read_file"):
		return "read"
	case strings.Contains(name, "list_directory"), strings.Contains(name, "list_dir"):
		return "list"
	case strings.Contains(name, "search"), strings.Contains(name, "grep"):
		return "search"
	default:
		return ""
	}
}

func toolCallDetail(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	if idx := strings.Index(t, ":"); idx >= 0 {
		t = strings.TrimSpace(t[idx+1:])
	}
	if strings.HasPrefix(t, "{") {
		var body map[string]any
		if err := json.Unmarshal([]byte(t), &body); err == nil {
			detail := core.FirstNonEmpty(
				core.AsString(body["command"]),
				core.AsString(body["file_path"]),
				core.AsString(body["path"]),
				core.AsString(body["pattern"]),
				core.AsString(body["query"]),
				core.AsString(body["url"]),
				core.AsString(body["name"]),
				core.AsString(body["task_id"]),
				core.AsString(body["text"]),
				core.AsString(body["id"]),
			)
			if detail != "" {
				return detail
			}
			// Fall back to showing what we can: first non-empty value
			for _, v := range body {
				if s := core.AsString(v); strings.TrimSpace(s) != "" {
					return truncateDisplayText(s, 80)
				}
			}
			return ""
		}
	}
	return t
}

func explorationLine(toolName, fallback string, env toolResultEnvelope) string {
	payload := env.payload
	data := env.data
	switch toolName {
	case "read_file":
		path := core.FirstNonEmpty(core.AsString(payload["file_path"]), core.AsString(data["file_path"]), actionDetailFallback(fallback, "Read"), "file")
		return "Read " + path
	case "list_dir":
		path := core.FirstNonEmpty(core.AsString(payload["path"]), core.AsString(data["path"]), actionDetailFallback(fallback, "List"), ".")
		return "List " + path
	case "search_files":
		return formatSearchActionLine(core.FirstNonEmpty(searchDetailFromPayload(payload, data, ""), actionDetailFallback(fallback, "Search")), "files")
	case "grep", "search_content":
		return formatSearchActionLine(core.FirstNonEmpty(searchDetailFromPayload(payload, data, core.AsString(payload["include"])), actionDetailFallback(fallback, "Search")), "content")
	case "fetch", "web_fetch":
		return "Fetch " + core.FirstNonEmpty(core.AsString(payload["url"]), core.AsString(data["url"]), actionDetailFallback(fallback, "Fetch"), "url")
	case "web_search":
		query := core.FirstNonEmpty(webSearchQueryFromMaps(payload, data), actionDetailFallback(fallback, "Search web for"), actionDetailFallback(fallback, "Search"))
		if query != "" {
			return "Search web for " + query
		}
		return "Search web"
	default:
		switch mcpExplorationKind(toolName) {
		case "read":
			path := core.FirstNonEmpty(core.AsString(payload["file_path"]), core.AsString(payload["path"]), core.AsString(data["file_path"]), core.AsString(data["path"]), actionDetailFallback(fallback, "Read"), "file")
			return "Read " + path
		case "list":
			path := core.FirstNonEmpty(core.AsString(payload["path"]), core.AsString(data["path"]), actionDetailFallback(fallback, "List"), ".")
			return "List " + path
		case "search":
			return formatSearchActionLine(core.FirstNonEmpty(searchDetailFromPayload(payload, data, ""), actionDetailFallback(fallback, "Search")), "content")
		}
		return "Run " + core.FirstNonEmpty(fallback, toolName)
	}
}

func actionDetailFallback(fallback, action string) string {
	fallback = strings.TrimSpace(fallback)
	action = strings.TrimSpace(action)
	if fallback == "" || action == "" {
		return fallback
	}
	if after, ok := strings.CutPrefix(fallback, action+" "); ok {
		return strings.TrimSpace(after)
	}
	return fallback
}

func searchDetailFromPayload(payload, data map[string]any, includeFallback string) string {
	pattern := core.FirstNonEmpty(core.AsString(payload["pattern"]), core.AsString(data["pattern"]))
	path := core.FirstNonEmpty(core.AsString(payload["path"]), core.AsString(data["path"]))
	include := core.FirstNonEmpty(core.AsString(payload["include"]), core.AsString(data["include"]), includeFallback)
	return appendSearchDetail(pattern, path, include)
}

func webSearchQueryFromMaps(payload, data map[string]any) string {
	return core.FirstNonEmpty(core.AsString(payload["query"]), core.AsString(data["query"]))
}

func formatSearchActionLine(detail, fallback string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = fallback
	}
	if strings.HasPrefix(detail, "Search ") {
		return detail
	}
	return "Search " + detail
}

func appendSearchDetail(subject, path, include string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	if strings.TrimSpace(path) != "" {
		subject += " in " + strings.TrimSpace(path)
	}
	if strings.TrimSpace(include) != "" {
		subject += " (" + strings.TrimSpace(include) + ")"
	}
	return subject
}

func previousToolActionLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) >= 2 {
		return strings.TrimSpace(lines[1])
	}
	if len(lines) == 1 {
		return strings.TrimSpace(lines[0])
	}
	return ""
}

func todoToolTitle(toolName, text, state string) string {
	action := todoToolAction(toolName, state)
	detail := todoToolDetail(text)
	if detail == "" {
		return action
	}
	return action + "\n" + detail
}

func todoToolAction(toolName, state string) string {
	done := state == "done"
	switch strings.TrimSpace(toolName) {
	case "todo_add":
		if done {
			return "Todo added"
		}
		return "Adding todo"
	case "todo_update":
		if done {
			return "Todo updated"
		}
		return "Updating todo"
	case "todo_remove":
		if done {
			return "Todo removed"
		}
		return "Removing todo"
	case "todo_clear_done":
		if done {
			return "Cleared completed todos"
		}
		return "Clearing completed todos"
	case "todo_list":
		if done {
			return "Listed todos"
		}
		return "Listing todos"
	default:
		if done {
			return "Todo completed"
		}
		return "Updating todo"
	}
}

func todoToolDetail(text string) string {
	detail := toolCallDetail(text)
	if detail != "" && !strings.HasPrefix(detail, "{") {
		return detail
	}
	t := strings.TrimSpace(text)
	if idx := strings.Index(t, ":"); idx >= 0 {
		t = strings.TrimSpace(t[idx+1:])
	}
	if !strings.HasPrefix(t, "{") {
		return firstNonEmptyLine(t)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(t), &body); err != nil {
		return ""
	}
	return core.FirstNonEmpty(core.AsString(body["text"]), core.AsString(body["id"]))
}

func editLine(toolName, fallback string, env toolResultEnvelope) string {
	payload := env.payload
	data := env.data
	switch toolName {
	case "write_file", "write":
		return "Edited " + core.FirstNonEmpty(core.AsString(payload["file_path"]), core.AsString(data["file_path"]), actionDetailFallback(fallback, "Edited"), "file")
	case "edit_file", "edit":
		path := core.FirstNonEmpty(core.AsString(payload["file_path"]), core.AsString(data["file_path"]), actionDetailFallback(fallback, "Edited"), actionDetailFallback(fallback, "Edit failed"), actionDetailFallback(fallback, "Edit re-reading"), "file")
		if !toolEnvelopeSucceeded(env) {
			if isRecoverableEditMiss(env) {
				return "Edit re-reading " + path
			}
			return "Edit failed " + path
		}
		return "Edited " + path
	case "multi_edit":
		path := core.FirstNonEmpty(core.AsString(payload["file_path"]), core.AsString(data["file_path"]), actionDetailFallback(fallback, "Edited"), actionDetailFallback(fallback, "Edit failed"), actionDetailFallback(fallback, "Edit re-reading"), "file")
		if !toolEnvelopeSucceeded(env) {
			if isRecoverableEditMiss(env) {
				return "Edit re-reading " + path
			}
			return "Edit failed " + path
		}
		return "Edited " + path
	default:
		return "Edited " + core.FirstNonEmpty(fallback, "files")
	}
}
