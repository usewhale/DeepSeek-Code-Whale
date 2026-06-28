package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/shellsafe"
)

func (b *Toolset) shellRun(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		Command      string `json:"command"`
		TimeoutMS    int    `json:"timeout_ms"`
		YieldTimeMS  int    `json:"yield_time_ms"`
		MaxRuntimeMS int    `json:"max_runtime_ms"`
		Background   bool   `json:"background"`
		CWD          string `json:"cwd"`
		Mode         string `json:"mode"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	if strings.TrimSpace(in.Command) == "" {
		return marshalToolError(call, "invalid_args", "command is required"), nil
	}
	workdir, relCWD, err := b.resolveShellCWD(in.CWD)
	if err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	mode, err := shellRunMode(in.Mode)
	if err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	warnings := b.shellRunWarnings(in.Command)
	requestedYieldMS, effectiveYieldMS := shellRunYieldMS(in.YieldTimeMS, in.TimeoutMS, b.foregroundShellWait)
	requestedMaxRuntimeMS, effectiveMaxRuntimeMS := shellRunMaxRuntimeMS(in.MaxRuntimeMS, in.TimeoutMS, in.Background)
	if in.Background {
		shellPol := shellPolicyWithForegroundWait(in.Command, requestedYieldMS, b.foregroundShellWait)
		transport, err := shellRunTransport(mode, shellPol)
		if err != nil {
			return marshalToolError(call, "unsupported_transport", err.Error()), nil
		}
		task := b.tasks.create(in.Command, relCWD, transport)
		task.setExecBoundaryPolicy(b.execBoundaryPolicy())
		task.setExecBoundaryApproval(b.execBoundarySessionID(), b.execApproval)
		task.setTimeoutContext(shellTimeoutContext{
			Policy:             shellPol,
			RequestedTimeoutMS: requestedMaxRuntimeMS,
			EffectiveTimeoutMS: effectiveMaxRuntimeMS,
			DefaultWaitMS:      b.foregroundShellWait.DefaultMS,
			BackgroundTask:     true,
		})
		cctx, cancel := context.WithTimeout(context.Background(), time.Duration(effectiveMaxRuntimeMS)*time.Millisecond)
		task.setCancel(cancel)
		go func() {
			defer b.tasks.completed(task.ID)
			defer cancel()
			runShellBackground(cctx, workdir, in.Command, task)
		}()
		if transport == shellTransportPTY {
			_ = task.waitForStdin(ctx, time.Duration(shellStdinReadyTimeoutMS)*time.Millisecond)
		}
		snap := task.snapshot()
		return marshalToolResult(call, map[string]any{
			"status": "running",
			"payload": map[string]any{
				"task_id":   task.ID,
				"command":   in.Command,
				"cwd":       relCWD,
				"transport": string(transport),
				"can_write": snap.CanWrite,
			},
			"metrics": map[string]any{
				"requested_yield_time_ms":  requestedYieldMS,
				"effective_yield_time_ms":  effectiveYieldMS,
				"requested_max_runtime_ms": requestedMaxRuntimeMS,
				"effective_max_runtime_ms": effectiveMaxRuntimeMS,
				"requested_timeout_ms":     requestedMaxRuntimeMS,
				"effective_timeout_ms":     effectiveMaxRuntimeMS,
				"timeout_clamped":          requestedMaxRuntimeMS > 0 && requestedMaxRuntimeMS != effectiveMaxRuntimeMS,
			},
			"warnings": warnings,
			"summary":  "background shell task started",
		})
	}

	shellPol := shellPolicyWithForegroundWait(in.Command, requestedYieldMS, b.foregroundShellWait)
	transport, err := shellRunTransport(mode, shellPol)
	if err != nil {
		return marshalToolError(call, "unsupported_transport", err.Error()), nil
	}
	task := b.tasks.create(in.Command, relCWD, transport)
	task.setExecBoundaryPolicy(b.execBoundaryPolicy())
	task.setExecBoundaryApproval(b.execBoundarySessionID(), b.execApproval)
	task.setTimeoutContext(shellTimeoutContext{
		Policy:             shellPol,
		RequestedTimeoutMS: requestedMaxRuntimeMS,
		EffectiveTimeoutMS: effectiveMaxRuntimeMS,
		DefaultWaitMS:      b.foregroundShellWait.DefaultMS,
		BackgroundTask:     false,
	})
	cctx, cancel := context.WithTimeout(context.Background(), time.Duration(effectiveMaxRuntimeMS)*time.Millisecond)
	task.setCancel(cancel)
	go func() {
		defer b.tasks.completed(task.ID)
		defer cancel()
		runShellBackground(cctx, workdir, in.Command, task)
	}()

	timer := time.NewTimer(time.Duration(effectiveYieldMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		snap := task.snapshot()
		if snap.Status != "running" {
			b.tasks.release(task.ID)
			return shellRunForegroundFinalResult(ctx, call, snap, requestedMaxRuntimeMS, effectiveMaxRuntimeMS, warnings)
		}
		return marshalToolResult(call, shellRunBackgroundedResult(snap, requestedYieldMS, effectiveYieldMS, requestedMaxRuntimeMS, effectiveMaxRuntimeMS, warnings, shellYieldInterruptedDiagnosis(shellPol)))
	case <-task.done:
		snap := task.snapshot()
		b.tasks.release(task.ID)
		return shellRunForegroundFinalResult(ctx, call, snap, requestedMaxRuntimeMS, effectiveMaxRuntimeMS, warnings)
	case <-timer.C:
		if snap := task.snapshot(); snap.Status != "running" {
			b.tasks.release(task.ID)
			return shellRunForegroundFinalResult(ctx, call, snap, requestedMaxRuntimeMS, effectiveMaxRuntimeMS, warnings)
		}
		snap := task.snapshot()
		return marshalToolResult(call, shellRunBackgroundedResult(snap, requestedYieldMS, effectiveYieldMS, requestedMaxRuntimeMS, effectiveMaxRuntimeMS, warnings, shellYieldTimeoutDiagnosis(shellPol, snap)))
	}
}

func shellRunYieldMS(yieldTimeMS, legacyTimeoutMS int, waitCfg foregroundShellWaitConfig) (int, int) {
	requested := yieldTimeMS
	if requested <= 0 {
		requested = legacyTimeoutMS
	}
	return requested, requestedForegroundWaitMS(requested, waitCfg.MaxMS, waitCfg.DefaultMS)
}

func shellRunMaxRuntimeMS(maxRuntimeMS, legacyTimeoutMS int, legacyTimeoutIsRuntime bool) (int, int) {
	requested := maxRuntimeMS
	if requested <= 0 && legacyTimeoutIsRuntime {
		requested = legacyTimeoutMS
	}
	effective := defaultBackgroundShellTimeoutMS
	if requested > 0 {
		effective = requested
		if effective > maxBackgroundShellTimeoutMS {
			effective = maxBackgroundShellTimeoutMS
		}
	} else {
		requested = effective
	}
	return requested, effective
}

func shellYieldInterruptedDiagnosis(policy shellContinuationPolicy) shellDiagnosis {
	diagnosis := shellDiagnosisForReason(policy.Reason)
	if diagnosis.Reason == "" {
		diagnosis.Reason = "unknown_long_running"
	}
	diagnosis.Reason = "yield_interrupted"
	diagnosis.Hint = "The tool call was interrupted, but the shell process is still running. Continue with shell_wait using the existing task_id."
	diagnosis.SuggestedNextAction = "shell_wait"
	return diagnosis
}

func shellYieldTimeoutDiagnosis(policy shellContinuationPolicy, snap shellTaskSnapshot) shellDiagnosis {
	if policy.Reason == "interactive_or_auth" {
		return shellInteractiveAuthDiagnosis(snap.Transport)
	}
	if shellOutputLooksInteractivePrompt(strings.TrimSpace(snap.Stderr + "\n" + snap.Stdout)) {
		return shellInteractivePromptDiagnosis(snap.Transport)
	}
	diagnosis := shellDiagnosisForReason(policy.Reason)
	if diagnosis.Reason == "" {
		diagnosis.Reason = "unknown_long_running"
	}
	diagnosis.Hint = "Command is still running. Continue with shell_wait using the existing task_id instead of rerunning the command."
	diagnosis.SuggestedNextAction = "shell_wait"
	return diagnosis
}

func shellRunMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		return "auto", nil
	}
	switch mode {
	case "auto", "pipe", "pty":
		return mode, nil
	default:
		return "", fmt.Errorf("mode must be auto, pipe, or pty")
	}
}

func shellRunTransport(mode string, policy shellContinuationPolicy) (shellTransportKind, error) {
	switch mode {
	case "pty":
		if !shellPTYSupported() {
			return "", fmt.Errorf("PTY shell transport is not supported on this platform")
		}
		if !shellExecBoundaryEnabled() {
			return "", fmt.Errorf("PTY shell transport requires an exec-boundary shell")
		}
		return shellTransportPTY, nil
	case "pipe":
		return shellTransportPipe, nil
	default:
		if policy.Reason == "interactive_or_auth" && shellPTYSupported() && shellExecBoundaryEnabled() {
			return shellTransportPTY, nil
		}
		return shellTransportPipe, nil
	}
}

type shellRunResultInput struct {
	Status             string
	SummaryParts       []string
	Command            string
	CWD                string
	Stdout             string
	Stderr             string
	StdoutRaw          string
	StderrRaw          string
	StdoutTruncation   truncationMeta
	StderrTruncation   truncationMeta
	DurationMS         int64
	ExitCode           *int
	TimedOut           bool
	RequestedTimeoutMS int
	EffectiveTimeoutMS int
	Diagnosis          shellDiagnosis
	Warnings           []string
	Transport          shellTransportKind
	CanWrite           bool
}

func shellRunResult(in shellRunResultInput) map[string]any {
	summaryParts := append([]string(nil), in.SummaryParts...)
	if len(in.Warnings) > 0 {
		summaryParts = append(summaryParts, "warning: "+strings.Join(in.Warnings, "; "))
	}
	if len(summaryParts) == 0 {
		summaryParts = append(summaryParts, "command completed")
	}
	metrics := map[string]any{
		"exit_code":            in.ExitCode,
		"duration_ms":          in.DurationMS,
		"stdout_chars":         len([]rune(in.StdoutRaw)),
		"stderr_chars":         len([]rune(in.StderrRaw)),
		"stdout_truncation":    in.StdoutTruncation,
		"stderr_truncation":    in.StderrTruncation,
		"timed_out":            in.TimedOut,
		"command_was_present":  strings.TrimSpace(in.Command) != "",
		"requested_timeout_ms": in.RequestedTimeoutMS,
		"effective_timeout_ms": in.EffectiveTimeoutMS,
		"timeout_clamped":      in.RequestedTimeoutMS > 0 && in.EffectiveTimeoutMS > 0 && in.RequestedTimeoutMS != in.EffectiveTimeoutMS,
	}
	result := map[string]any{
		"status":  in.Status,
		"metrics": metrics,
		"payload": map[string]any{
			"command":   in.Command,
			"cwd":       in.CWD,
			"transport": string(in.Transport),
			"can_write": in.CanWrite,
			"stdout":    in.Stdout,
			"stderr":    in.Stderr,
		},
		"warnings": in.Warnings,
		"summary":  strings.Join(summaryParts, " | "),
	}
	if diagnosis := in.Diagnosis.asMap(); diagnosis != nil {
		result["diagnosis"] = diagnosis
	}
	return result
}

func shellRunForegroundFinalResult(ctx context.Context, call core.ToolCall, snap shellTaskSnapshot, requestedTimeoutMS int, effectiveTimeoutMS int, warnings []string) (core.ToolResult, error) {
	stdout, stdoutTr := truncateTextSmart(snap.Stdout, maxToolTextChars)
	stderr, stderrTr := truncateTextSmart(snap.Stderr, maxToolTextChars)
	archivePath := archiveTruncatedShellOutput(ctx, call, snap, stdoutTr, stderrTr)
	stdout, stderr = annotateTruncatedShellOutput(stdout, stderr, stdoutTr, stderrTr, archivePath)
	durationMS := shellTaskDurationMS(snap)
	status := "ok"
	summaryParts := []string{"command completed"}
	success := true
	code := "ok"
	message := ""
	timedOut := false
	switch snap.Status {
	case "failed":
		if snap.ExitCode != nil && core.ShellExitMeansNoMatches(snap.Command, *snap.ExitCode, snap.Stdout, snap.Stderr) {
			// Search commands answer "found nothing" via exit 1; that is a
			// result, not a failure — don't push the model into retries or
			// count it as a tool error.
			status = "exited"
			summaryParts = []string{"no matches (exit 1)"}
			break
		}
		status = "error"
		summaryParts = []string{"command failed"}
		if hint := summarizeText(snap.Stderr, 220); hint != "" {
			summaryParts = append(summaryParts, hint)
		}
		success = false
		code = "exec_failed"
		message = "command failed"
	case "timeout":
		status = "timeout"
		summaryParts = []string{"command timed out"}
		success = false
		code = "timeout"
		message = "command timed out"
		timedOut = true
	case "canceled":
		return marshalToolError(call, "cancelled", "command canceled"), nil
	}
	if snap.Status == "exited" {
		if hint := summarizeText(snap.Stderr, 200); hint != "" {
			summaryParts = []string{"stderr:" + hint}
		}
	}
	result := shellRunResult(shellRunResultInput{
		Status:             status,
		SummaryParts:       summaryParts,
		Command:            snap.Command,
		CWD:                snap.CWD,
		Stdout:             stdout,
		Stderr:             stderr,
		StdoutRaw:          snap.Stdout,
		StderrRaw:          snap.Stderr,
		StdoutTruncation:   stdoutTr,
		StderrTruncation:   stderrTr,
		DurationMS:         durationMS,
		ExitCode:           snap.ExitCode,
		TimedOut:           timedOut,
		RequestedTimeoutMS: requestedTimeoutMS,
		EffectiveTimeoutMS: effectiveTimeoutMS,
		Diagnosis:          snap.Diagnosis,
		Warnings:           warnings,
		Transport:          snap.Transport,
		CanWrite:           snap.CanWrite,
	})
	if success {
		return marshalToolResultWithMetadata(call, result, shellArchiveMetadata(archivePath))
	}
	content, marshalErr := core.MarshalToolEnvelope(core.ToolEnvelope{Success: false, Data: result, Message: message, Code: code})
	if marshalErr != nil {
		return marshalToolError(call, code, message), nil
	}
	return core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: content, Outcome: core.OutcomeForErrorCode(code), Code: code, Metadata: shellArchiveMetadata(archivePath)}, nil
}

func archiveTruncatedShellOutput(ctx context.Context, call core.ToolCall, snap shellTaskSnapshot, stdoutTr, stderrTr truncationMeta) string {
	if !stdoutTr.Truncated && !stderrTr.Truncated {
		return ""
	}
	payload := shellArchivePayload(snap, stdoutTr.Truncated, stderrTr.Truncated)
	if strings.TrimSpace(payload) == "" {
		return ""
	}
	return core.ArchiveToolResult(ctx, call.Name, call.ID, []byte(payload))
}

func shellArchivePayload(snap shellTaskSnapshot, stdoutTruncated, stderrTruncated bool) string {
	stdout := snap.Stdout
	stderr := snap.Stderr
	switch {
	case stdoutTruncated && !stderrTruncated && strings.TrimSpace(stderr) == "":
		return stdout
	case stderrTruncated && !stdoutTruncated && strings.TrimSpace(stdout) == "":
		return stderr
	default:
		var b strings.Builder
		b.WriteString("command: ")
		b.WriteString(snap.Command)
		if strings.TrimSpace(snap.CWD) != "" {
			b.WriteString("\ncwd: ")
			b.WriteString(snap.CWD)
		}
		b.WriteString("\n\nstdout:\n")
		b.WriteString(stdout)
		b.WriteString("\n\nstderr:\n")
		b.WriteString(stderr)
		return b.String()
	}
}

func annotateTruncatedShellOutput(stdout, stderr string, stdoutTr, stderrTr truncationMeta, archivePath string) (string, string) {
	if archivePath == "" {
		return stdout, stderr
	}
	hint := shellArchiveHint(archivePath)
	if stdoutTr.Truncated {
		return stdout + hint, stderr
	}
	if stderrTr.Truncated {
		return stdout, stderr + hint
	}
	return stdout, stderr
}

func shellArchiveHint(path string) string {
	return fmt.Sprintf("\nFull output saved to: %s\nUse read_file with offset/limit or grep/search_files first; do not read the full file unless needed.", path)
}

func shellArchiveMetadata(path string) map[string]any {
	if path == "" {
		return nil
	}
	return map[string]any{
		"output_truncated": true,
		"full_result_path": path,
	}
}

func shellRunForegroundTimeoutResult(call core.ToolCall, snap shellTaskSnapshot, requestedTimeoutMS int, effectiveTimeoutMS int, warnings []string) (core.ToolResult, error) {
	stdout, stdoutTr := truncateTextSmart(snap.Stdout, maxToolTextChars)
	stderr, stderrTr := truncateTextSmart(snap.Stderr, maxToolTextChars)
	result := shellRunResult(shellRunResultInput{
		Status:             "timeout",
		SummaryParts:       []string{"command timed out"},
		Command:            snap.Command,
		CWD:                snap.CWD,
		Stdout:             stdout,
		Stderr:             stderr,
		StdoutRaw:          snap.Stdout,
		StderrRaw:          snap.Stderr,
		StdoutTruncation:   stdoutTr,
		StderrTruncation:   stderrTr,
		DurationMS:         shellTaskDurationMS(snap),
		ExitCode:           nil,
		TimedOut:           true,
		RequestedTimeoutMS: requestedTimeoutMS,
		EffectiveTimeoutMS: effectiveTimeoutMS,
		Diagnosis:          snap.Diagnosis,
		Warnings:           warnings,
		Transport:          snap.Transport,
		CanWrite:           snap.CanWrite,
	})
	content, marshalErr := core.MarshalToolEnvelope(core.ToolEnvelope{Success: false, Data: result, Message: "command timed out", Code: "timeout"})
	if marshalErr != nil {
		return marshalToolError(call, "timeout", "command timed out"), nil
	}
	return core.ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: content, Outcome: core.OutcomeTimeout, Code: "timeout"}, nil
}

func shellRunBackgroundedResult(snap shellTaskSnapshot, requestedYieldMS int, effectiveYieldMS int, requestedMaxRuntimeMS int, effectiveMaxRuntimeMS int, warnings []string, diagnosis shellDiagnosis) map[string]any {
	summaryParts := []string{"command still running in background"}
	if len(warnings) > 0 {
		summaryParts = append(summaryParts, "warning: "+strings.Join(warnings, "; "))
	}
	result := map[string]any{
		"status": "running",
		"metrics": map[string]any{
			"duration_ms":              shellTaskDurationMS(snap),
			"requested_yield_time_ms":  requestedYieldMS,
			"effective_yield_time_ms":  effectiveYieldMS,
			"requested_max_runtime_ms": requestedMaxRuntimeMS,
			"effective_max_runtime_ms": effectiveMaxRuntimeMS,
			"requested_timeout_ms":     requestedMaxRuntimeMS,
			"effective_timeout_ms":     effectiveMaxRuntimeMS,
			"timeout_clamped":          requestedMaxRuntimeMS > 0 && requestedMaxRuntimeMS != effectiveMaxRuntimeMS,
			"auto_backgrounded":        true,
		},
		"payload": map[string]any{
			"task_id":    snap.ID,
			"command":    snap.Command,
			"cwd":        snap.CWD,
			"transport":  string(snap.Transport),
			"can_write":  snap.CanWrite,
			"done":       false,
			"started_at": snap.StartedAt,
		},
		"warnings": warnings,
		"summary":  strings.Join(summaryParts, " | "),
	}
	if diagnosis.Reason == "" {
		diagnosis = snap.Diagnosis
	}
	if diag := diagnosis.asMap(); diag != nil {
		result["diagnosis"] = diag
	}
	return result
}

func shellTaskDurationMS(snap shellTaskSnapshot) int64 {
	end := time.Now()
	if snap.FinishedAt != nil {
		end = *snap.FinishedAt
	}
	return end.Sub(snap.StartedAt).Milliseconds()
}

func (b *Toolset) shellRunWarnings(command string) []string {
	original := strings.TrimSpace(b.originalWorkspace)
	worktree := strings.TrimSpace(b.worktreeRoot)
	if original == "" || worktree == "" || original == worktree {
		return nil
	}
	var warnings []string
	for _, part := range shellCommandParts(command) {
		argv, ok := parsePOSIXReadOnlyShellCommand(part)
		if !ok || len(argv) == 0 {
			if shellCommandMentionsOriginalWorkspace(part, original) {
				warnings = append(warnings, "command references the original workspace; this session is running in a worktree")
			}
			continue
		}
		if argv[0] == "cd" && len(argv) >= 2 && samePath(argv[1], original) {
			warnings = append(warnings, "command cd's to the original workspace; keep work in the current worktree unless the user explicitly asks otherwise")
			continue
		}
		if argv[0] == "git" {
			for i := 1; i+1 < len(argv); i++ {
				if argv[i] == "-C" && samePath(argv[i+1], original) {
					warnings = append(warnings, "command uses git -C with the original workspace; keep git commands in the current worktree unless the user explicitly asks otherwise")
					break
				}
			}
		}
	}
	return dedupeStrings(warnings)
}

func shellCommandParts(command string) []string {
	parts := []string{strings.TrimSpace(command)}
	if split, ok := shellsafe.SplitAndList(command); ok {
		parts = split
	}
	out := parts[:0]
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func samePath(a, b string) bool {
	a = cleanOptionalAbsPath(a)
	b = cleanOptionalAbsPath(b)
	return a != "" && b != "" && a == b
}

func shellCommandMentionsOriginalWorkspace(command, original string) bool {
	original = strings.TrimSpace(original)
	if original == "" {
		return false
	}
	return strings.Contains(command, "cd "+original) || strings.Contains(command, "git -C "+original)
}

func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (b *Toolset) shellWait(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		TaskID    string `json:"task_id"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return marshalToolError(call, "invalid_args", "task_id is required"), nil
	}
	task, ok := b.tasks.get(strings.TrimSpace(in.TaskID))
	if !ok {
		return marshalToolError(call, "not_found", "task not found"), nil
	}
	return b.shellWaitForTask(ctx, call, task, in.TimeoutMS, "task still running", nil)
}

func (b *Toolset) writeStdin(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		TaskID    string   `json:"task_id"`
		Chars     string   `json:"chars"`
		Keys      []string `json:"keys"`
		TimeoutMS int      `json:"timeout_ms"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return marshalToolError(call, "invalid_args", "task_id is required"), nil
	}
	task, ok := b.tasks.get(taskID)
	if !ok {
		return marshalToolError(call, "not_found", "task not found"), nil
	}
	input, err := shellStdinInput(in.Chars, in.Keys)
	if err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	if len([]byte(input)) > maxShellStdinBytes {
		return marshalToolError(call, "stdin_too_large", fmt.Sprintf("stdin input is %d bytes; maximum per write_stdin call is %d bytes", len([]byte(input)), maxShellStdinBytes)), nil
	}
	wrote := input != ""
	if wrote {
		if err := task.writeStdin(ctx, input); err != nil {
			switch {
			case err == errShellStdinUnavailable:
				return marshalToolError(call, "stdin_not_available", err.Error()), nil
			case err == errShellStdinClosed:
				return marshalToolError(call, "stdin_closed", err.Error()), nil
			default:
				return marshalToolError(call, "stdin_write_failed", err.Error()), nil
			}
		}
	}
	extra := map[string]any{
		"stdin_written": wrote,
		"chars_written": len([]rune(in.Chars)),
		"keys_written":  len(in.Keys),
	}
	summary := "task still running"
	if wrote {
		summary = "stdin written; task still running"
	}
	return b.shellWaitForTask(ctx, call, task, in.TimeoutMS, summary, extra)
}

func shellStdinInput(chars string, keys []string) (string, error) {
	var b strings.Builder
	b.WriteString(chars)
	for _, key := range keys {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "enter":
			b.WriteByte('\n')
		case "tab":
			b.WriteByte('\t')
		case "escape":
			b.WriteByte(0x1b)
		case "ctrl-c":
			b.WriteByte(0x03)
		default:
			return "", fmt.Errorf("unsupported key %q; supported keys are enter, tab, escape, ctrl-c", key)
		}
	}
	return b.String(), nil
}

func (b *Toolset) shellWaitForTask(ctx context.Context, call core.ToolCall, task *shellTask, timeoutMS int, runningSummary string, extraMetrics map[string]any) (core.ToolResult, error) {
	deadline := time.Duration(defaultShellWaitTimeoutMS) * time.Millisecond
	if timeoutMS > 0 {
		if timeoutMS > maxShellWaitTimeoutMS {
			timeoutMS = maxShellWaitTimeoutMS
		}
		deadline = time.Duration(timeoutMS) * time.Millisecond
	}
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	timer := time.NewTimer(deadline)
	defer timer.Stop()

	for {
		snap := task.snapshot()
		if snap.Status != "running" {
			return b.shellWaitFinalResult(ctx, call, snap, extraMetrics)
		}
		select {
		case <-ctx.Done():
			return marshalToolError(call, "cancelled", ctx.Err().Error()), nil
		case <-timer.C:
			snap = task.snapshot()
			if snap.Status != "running" {
				return b.shellWaitFinalResult(ctx, call, snap, extraMetrics)
			}
			result := map[string]any{
				"status": "running",
				"payload": map[string]any{
					"task_id":        snap.ID,
					"command":        snap.Command,
					"cwd":            snap.CWD,
					"transport":      string(snap.Transport),
					"can_write":      snap.CanWrite,
					"stdout":         summarizeTaskOutputForRunning(snap.Stdout),
					"stderr":         summarizeTaskOutputForRunning(snap.Stderr),
					"done":           false,
					"started_at":     snap.StartedAt,
					"last_output_at": snap.LastOutput,
				},
				"metrics": map[string]any{
					"duration_ms": shellTaskDurationMS(snap),
				},
				"summary": runningSummary,
			}
			if extraMetrics != nil {
				metrics := result["metrics"].(map[string]any)
				for k, v := range extraMetrics {
					metrics[k] = v
				}
			}
			if diagnosis := snap.Diagnosis.asMap(); diagnosis != nil {
				result["diagnosis"] = diagnosis
			}
			return marshalToolResult(call, result)
		case <-poll.C:
		}
	}
}

func (b *Toolset) shellWaitFinalResult(ctx context.Context, call core.ToolCall, snap shellTaskSnapshot, extraMetrics map[string]any) (core.ToolResult, error) {
	stdout, stdoutTr := truncateTextSmart(snap.Stdout, maxToolTextChars)
	stderr, stderrTr := truncateTextSmart(snap.Stderr, maxToolTextChars)
	archivePath := archiveTruncatedShellOutput(ctx, call, snap, stdoutTr, stderrTr)
	stdout, stderr = annotateTruncatedShellOutput(stdout, stderr, stdoutTr, stderrTr, archivePath)
	b.tasks.release(snap.ID)
	result := map[string]any{
		"status": snap.Status,
		"payload": map[string]any{
			"task_id":    snap.ID,
			"command":    snap.Command,
			"cwd":        snap.CWD,
			"transport":  string(snap.Transport),
			"can_write":  snap.CanWrite,
			"stdout":     stdout,
			"stderr":     stderr,
			"done":       true,
			"started_at": snap.StartedAt,
			"ended_at":   snap.FinishedAt,
		},
		"metrics": map[string]any{
			"exit_code":         snap.ExitCode,
			"stdout_truncation": stdoutTr,
			"stderr_truncation": stderrTr,
			"duration_ms":       shellTaskDurationMS(snap),
		},
	}
	if extraMetrics != nil {
		metrics := result["metrics"].(map[string]any)
		for k, v := range extraMetrics {
			metrics[k] = v
		}
	}
	if diagnosis := snap.Diagnosis.asMap(); diagnosis != nil {
		result["diagnosis"] = diagnosis
	}
	return marshalToolResultWithMetadata(call, result, shellArchiveMetadata(archivePath))
}

func (b *Toolset) shellCancel(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return marshalToolError(call, "invalid_args", "task_id is required"), nil
	}
	task, ok := b.tasks.get(taskID)
	if !ok {
		return marshalToolError(call, "not_found", "task not found"), nil
	}
	task.cancelRun()
	select {
	case <-ctx.Done():
		return marshalToolError(call, "cancelled", ctx.Err().Error()), nil
	case <-task.done:
	case <-time.After(3 * time.Second):
	}
	snap := task.snapshot()
	return marshalToolResult(call, map[string]any{
		"status": "cancelled",
		"payload": map[string]any{
			"task_id":    snap.ID,
			"command":    snap.Command,
			"cwd":        snap.CWD,
			"done":       snap.Status != "running",
			"started_at": snap.StartedAt,
			"ended_at":   snap.FinishedAt,
		},
		"metrics": map[string]any{
			"duration_ms": shellTaskDurationMS(snap),
		},
		"diagnosis": shellDiagnosisForReason("cancelled").asMap(),
		"summary":   "task cancelled",
	})
}

func summarizeTaskOutputForRunning(text string) string {
	out, _ := truncateTextSmart(text, maxToolTextChars)
	return out
}

func (b *Toolset) resolveShellCWD(raw string) (abs string, rel string, err error) {
	abs, err = b.safePath(raw)
	if err != nil {
		return "", "", err
	}
	rel, err = filepath.Rel(b.root, abs)
	if err != nil {
		return "", "", err
	}
	if rel == "." {
		return abs, ".", nil
	}
	return abs, filepath.ToSlash(rel), nil
}
