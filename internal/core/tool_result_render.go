package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// RenderToolResultText renders the model-visible text of a tool result from
// its canonical payload. This is the phase-2 replacement for serializing the
// JSON envelope: a compact status header, the content verbatim (never
// re-escaped), and prefixed trailing lines (note:/warning:/diagnosis:/
// recovery:/data:). Deterministic: map rendering goes through
// MarshalToolJSON (sorted keys), no timestamps.
func RenderToolResultText(name string, outcome ToolOutcome, code string, payload map[string]any) string {
	if rawText, ok := rawPassthroughText(payload); ok {
		return rawText
	}
	if payload == nil {
		if outcome == OutcomeSuccess || outcome == OutcomeNoResult {
			return "ok"
		}
		return fmt.Sprintf("error (%s)", FirstNonEmpty(code, "unknown"))
	}
	switch name {
	case "shell_run", "shell_wait":
		return renderShellText(outcome, code, payload)
	}
	if outcome != OutcomeSuccess && outcome != OutcomeNoResult {
		return renderErrorText(code, payload)
	}
	switch name {
	case "read_file":
		return renderReadFileText(payload)
	case "grep", "search_content":
		return renderGrepText(payload)
	case "list_dir":
		return renderListDirText(payload)
	}
	return renderGenericText(payload)
}

// rawPassthroughText detects the normalize wrapper for non-JSON tool output
// ({"text": raw} plus reserved keys) and returns the raw text verbatim.
func rawPassthroughText(payload map[string]any) (string, bool) {
	if payload == nil {
		return "", false
	}
	text, ok := payload["text"].(string)
	if !ok {
		return "", false
	}
	for k := range payload {
		switch k {
		case "text", "metadata", "summary", "message", "truncated":
		default:
			return "", false
		}
	}
	return text, true
}

func renderErrorText(code string, payload map[string]any) string {
	var b strings.Builder
	msg := payloadString(payload, "message")
	if msg == "" {
		msg = payloadString(payload, "summary")
	}
	if msg != "" {
		fmt.Fprintf(&b, "error (%s): %s", FirstNonEmpty(code, "unknown"), msg)
	} else {
		fmt.Fprintf(&b, "error (%s)", FirstNonEmpty(code, "unknown"))
	}
	if summary := payloadString(payload, "summary"); summary != "" && summary != msg {
		b.WriteString("\nsummary: " + summary)
	}
	appendRecoveryLine(&b, payload["recovery"])
	appendDiagnosisLine(&b, payload)
	if data := remainingData(payload, "message", "summary", "recovery", "diagnosis"); len(data) > 0 {
		if blob, err := MarshalToolJSON(data); err == nil {
			b.WriteString("\ndata: " + string(blob))
		}
	}
	return b.String()
}

func renderShellText(outcome ToolOutcome, code string, payload map[string]any) string {
	metrics := payloadMap(payload, "metrics")
	inner := payloadMap(payload, "payload")
	var b strings.Builder

	if payloadString(payload, "status") == "running" {
		// Background/long-running tasks: the task id is what the model
		// needs (shell_wait/shell_cancel take it).
		if taskID := AsString(inner["task_id"]); taskID != "" {
			fmt.Fprintf(&b, "running in background (task_id=%s)", taskID)
		} else {
			b.WriteString("running in background")
		}
		if summary := payloadString(payload, "summary"); summary != "" {
			b.WriteString(" — " + firstNonEmptyTextLine(summary))
		}
		if data := remainingData(payload, "summary", "message"); len(data) > 0 {
			if blob, err := MarshalToolJSON(data); err == nil {
				b.WriteString("\ndata: " + string(blob))
			}
		}
		return b.String()
	}

	exitCode, hasExit := payloadInt(metrics, "exit_code")
	duration, _ := payloadInt(metrics, "duration_ms")
	timedOut, _ := metrics["timed_out"].(bool)
	isErr := outcome != OutcomeSuccess && outcome != OutcomeNoResult
	if !hasExit && !timedOut {
		// No process ran (validation, approval, policy failures): an exit
		// header would mislead the model into reasoning about a command
		// that never executed.
		if isErr {
			return renderErrorText(code, payload)
		}
		return renderGenericText(payload)
	}
	switch {
	case timedOut:
		fmt.Fprintf(&b, "timed out after %dms (no exit)", duration)
	default:
		fmt.Fprintf(&b, "exit %d (%dms)", exitCode, duration)
	}
	headline := FirstNonEmpty(payloadString(payload, "message"), payloadString(payload, "summary"))
	if isErr && code != "" && code != "exec_failed" {
		// Non-exit failures (policy, approval, repair) keep their code.
		fmt.Fprintf(&b, " — error (%s)", code)
	}
	if headline != "" && !strings.HasPrefix(headline, "command completed") && !strings.HasPrefix(headline, "stderr:") {
		b.WriteString(" — " + firstNonEmptyTextLine(headline))
	}

	stdout, _ := inner["stdout"].(string)
	stderr, _ := inner["stderr"].(string)
	stdout = strings.TrimRight(stdout, "\n")
	stderr = strings.TrimRight(stderr, "\n")
	switch {
	case stdout != "" && stderr != "":
		b.WriteString("\nstdout:\n" + stdout)
		b.WriteString("\nstderr:\n" + stderr)
	case stdout != "":
		b.WriteString("\n" + stdout)
	case stderr != "":
		b.WriteString("\nstderr:\n" + stderr)
	}

	for _, w := range AsAnySlice(payload["warnings"]) {
		if s := strings.TrimSpace(AsString(w)); s != "" {
			b.WriteString("\nwarning: " + s)
		}
	}
	appendDiagnosisLine(&b, payload)
	appendStreamTruncationNote(&b, metrics, "stdout")
	appendStreamTruncationNote(&b, metrics, "stderr")
	return b.String()
}

func renderReadFileText(payload map[string]any) string {
	metrics := payloadMap(payload, "metrics")
	inner := payloadMap(payload, "payload")
	var b strings.Builder

	path := AsString(inner["file_path"])
	total, _ := payloadInt(metrics, "total_lines")
	returned, _ := payloadInt(metrics, "returned_lines")
	header := path
	if header == "" {
		header = "file"
	}
	rng := payloadMap(inner, "range")
	if start, ok := payloadInt(rng, "start"); ok {
		end, _ := payloadInt(rng, "end")
		fmt.Fprintf(&b, "%s lines %d-%d of %d", header, start, end, total)
	} else if total > 0 {
		fmt.Fprintf(&b, "%s %d lines", header, total)
	} else {
		b.WriteString(header)
	}
	moreBefore, _ := inner["has_more_before"].(bool)
	moreAfter, _ := inner["has_more_after"].(bool)
	switch {
	case moreBefore && moreAfter:
		b.WriteString(" (more before/after)")
	case moreBefore:
		b.WriteString(" (more before)")
	case moreAfter:
		b.WriteString(" (more after)")
	}
	_ = returned
	if content, ok := inner["content"].(string); ok && content != "" {
		b.WriteString("\n" + strings.TrimRight(content, "\n"))
	}
	if note := payloadString(payload, "note"); note != "" {
		b.WriteString("\nnote: " + note)
	}
	return b.String()
}

func renderGrepText(payload map[string]any) string {
	metrics := payloadMap(payload, "metrics")
	totalMatches, _ := payloadInt(metrics, "total_matches")
	filesMatched, hasFiles := payloadInt(metrics, "files_matched")

	var b strings.Builder
	if hasFiles && filesMatched > 0 {
		fmt.Fprintf(&b, "%d matches in %d files", totalMatches, filesMatched)
	} else {
		fmt.Fprintf(&b, "%d matches", totalMatches)
	}
	matches := AsAnySlice(payload["matches"])
	if len(matches) == 0 {
		matches = AsAnySlice(payloadMap(payload, "payload")["matches"])
	}
	for _, m := range matches {
		row, ok := m.(map[string]any)
		if !ok {
			continue
		}
		file := AsString(row["file"])
		lineNo, _ := payloadInt(row, "line_number")
		line := strings.TrimRight(AsString(row["line"]), "\n")
		if file == "" && line == "" {
			continue
		}
		fmt.Fprintf(&b, "\n%s:%d: %s", file, lineNo, line)
	}
	if note := payloadString(payload, "note"); note != "" {
		b.WriteString("\nnote: " + note)
	}
	return b.String()
}

func renderListDirText(payload map[string]any) string {
	inner := payloadMap(payload, "payload")
	items := AsAnySlice(inner["items"])
	if len(items) == 0 {
		items = AsAnySlice(payload["items"])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entries", len(items))
	for _, it := range items {
		switch v := it.(type) {
		case string:
			b.WriteString("\n" + v)
		case map[string]any:
			name := FirstNonEmpty(AsString(v["name"]), AsString(v["path"]))
			if name != "" {
				b.WriteString("\n" + name)
			}
		}
	}
	return b.String()
}

// renderGenericText is the fallback for tools without a dedicated shape:
// status header, one dominant string field verbatim, then compact JSON of
// whatever structured data remains (keeps codes/fields greppable).
func renderGenericText(payload map[string]any) string {
	var b strings.Builder
	header := "ok"
	if summary := payloadString(payload, "summary"); summary != "" {
		header += " — " + firstNonEmptyTextLine(summary)
	}
	b.WriteString(header)

	dominantKey := ""
	for _, key := range [...]string{"content", "text", "stdout", "output", "message"} {
		if s, ok := dominantString(payload, key); ok {
			b.WriteString("\n" + strings.TrimRight(s, "\n"))
			dominantKey = key
			break
		}
	}
	if data := remainingData(payload, "summary", "message", dominantKey); len(data) > 0 {
		if blob, err := MarshalToolJSON(data); err == nil {
			b.WriteString("\ndata: " + string(blob))
		}
	}
	return b.String()
}

// RenderTruncatedToolText bounds rendered text to maxChars with a head/tail
// split and an omission marker; archivePath (optional) points at the full
// text archived on disk.
func RenderTruncatedToolText(text string, maxChars int, archivePath string) string {
	if maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	marker := fmt.Sprintf("\n...[output truncated: %d of %d chars omitted]...\n", 0, len(text))
	if archivePath != "" {
		marker = truncatedToolResultMarker(0, len(text), archivePath)
	}
	for budget := maxChars; budget >= 1024; budget = budget * 3 / 4 {
		bodyBudget := budget - len(marker) - 24 // slack for the omitted count digits
		if bodyBudget < 256 {
			break
		}
		headBudget, tailBudget := splitToolResultBudget(bodyBudget)
		head := truncateOnRuneBoundary(text[:min(headBudget, len(text))], false)
		tailStart := len(text) - tailBudget
		if tailStart < len(head) {
			tailStart = len(head)
		}
		tail := truncateOnRuneBoundary(text[tailStart:], true)
		omitted := len(text) - len(head) - len(tail)
		m := fmt.Sprintf("\n...[output truncated: %d of %d chars omitted]...\n", omitted, len(text))
		if archivePath != "" {
			m = truncatedToolResultMarker(omitted, len(text), archivePath)
		}
		out := head + m + tail
		if len(out) <= maxChars {
			return out
		}
	}
	// Budgets too small for the head/tail split still must signal the cut
	// in-band: a head slice plus a compact marker, never a bare prefix.
	smallMarker := fmt.Sprintf("\n...[truncated, %d chars total]...", len(text))
	if archivePath != "" {
		smallMarker = compactTruncatedToolResultMarker(len(text), archivePath)
	}
	headBudget := maxChars - len(smallMarker)
	if headBudget <= 0 {
		// Degenerate limit: the marker itself dominates; keep what fits.
		return truncateOnRuneBoundary(smallMarker[1:min(maxChars+1, len(smallMarker))], false)
	}
	return truncateOnRuneBoundary(text[:min(headBudget, len(text))], false) + smallMarker
}

func truncatedToolResultMarker(omitted, total int, archivePath string) string {
	if archivePath == "" {
		return fmt.Sprintf("\n...[output truncated: %d of %d chars omitted]...\n", omitted, total)
	}
	return fmt.Sprintf("\n...[output truncated: %d of %d chars omitted]...\nFull output saved to: %s\nUse read_file with offset/limit or grep/search_files first; do not read the full file unless needed.\n", omitted, total, archivePath)
}

func compactTruncatedToolResultMarker(total int, archivePath string) string {
	if archivePath == "" {
		return fmt.Sprintf("\n...[truncated, %d chars total]...", total)
	}
	return fmt.Sprintf("\n...[truncated, %d chars total]...\nFull output saved to: %s\nUse read_file with offset/limit or grep/search_files first; do not read the full file unless needed.", total, archivePath)
}

// BoundedTruncationPayload replaces the full canonical payload when the
// rendered text was truncated, so large results are not persisted twice.
func BoundedTruncationPayload(text string, originalChars int, code, archivePath string) map[string]any {
	keep := 2048
	if len(text) < keep*2 {
		keep = len(text) / 2
	}
	head := truncateOnRuneBoundary(text[:min(keep, len(text))], false)
	tailStart := len(text) - keep
	if tailStart < len(head) {
		tailStart = len(head)
	}
	out := map[string]any{
		"truncated":      true,
		"original_chars": originalChars,
		"head":           head,
		"tail":           truncateOnRuneBoundary(text[tailStart:], true),
	}
	if code != "" {
		out["code"] = code
	}
	if archivePath != "" {
		out["full_result_path"] = archivePath
	}
	// Canonicalize so the live value and the persistence round trip agree
	// (ints become float64 either way).
	if b, err := MarshalToolJSON(out); err == nil {
		var canonical map[string]any
		if json.Unmarshal(b, &canonical) == nil {
			return canonical
		}
	}
	return out
}

// truncateOnRuneBoundary trims a byte-sliced string so it never starts or
// ends with a partial UTF-8 rune. fromStart drops leading continuation
// bytes; otherwise a trailing incomplete rune (start byte without all its
// continuation bytes) is removed entirely.
func truncateOnRuneBoundary(s string, fromStart bool) string {
	if fromStart {
		i := 0
		for i < len(s) && i < utf8.UTFMax && (s[i]&0xC0) == 0x80 {
			i++
		}
		return s[i:]
	}
	i := len(s)
	for i > 0 && len(s)-i < utf8.UTFMax {
		if r, size := utf8.DecodeLastRuneInString(s[:i]); !(r == utf8.RuneError && size <= 1) {
			return s[:i]
		}
		i--
	}
	if len(s)-i >= utf8.UTFMax {
		// More trailing garbage than a partial rune: the input was not
		// valid UTF-8 to begin with (binary output); pass it through.
		return s
	}
	return s[:i]
}

func appendRecoveryLine(b *strings.Builder, recovery any) {
	switch v := recovery.(type) {
	case nil:
	case string:
		if strings.TrimSpace(v) != "" {
			b.WriteString("\nrecovery: " + strings.TrimSpace(v))
		}
	case map[string]any:
		parts := []string{}
		if retryable, ok := v["retryable"].(bool); ok {
			parts = append(parts, fmt.Sprintf("retry=%t", retryable))
		}
		if next := AsString(v["recommended_next_tool"]); next != "" {
			parts = append(parts, "next="+next)
		}
		if input, ok := v["recommended_input"].(map[string]any); ok && len(input) > 0 {
			if blob, err := MarshalToolJSON(input); err == nil {
				parts = append(parts, string(blob))
			}
		}
		if reason := AsString(v["reason"]); reason != "" {
			parts = append(parts, "— "+reason)
		}
		if len(parts) > 0 {
			b.WriteString("\nrecovery: " + strings.Join(parts, " "))
		}
	}
}

func appendDiagnosisLine(b *strings.Builder, payload map[string]any) {
	diagnosis := payloadMap(payload, "diagnosis")
	if len(diagnosis) == 0 {
		return
	}
	reason := AsString(diagnosis["reason"])
	hint := AsString(diagnosis["hint"])
	switch {
	case reason != "" && hint != "":
		b.WriteString("\ndiagnosis: " + reason + " — " + hint)
	case hint != "":
		b.WriteString("\ndiagnosis: " + hint)
	case reason != "":
		b.WriteString("\ndiagnosis: " + reason)
	}
}

func appendStreamTruncationNote(b *strings.Builder, metrics map[string]any, stream string) {
	tr := payloadMap(metrics, stream+"_truncation")
	if truncated, _ := tr["truncated"].(bool); !truncated {
		return
	}
	kept, _ := payloadInt(tr, "kept_chars")
	original, _ := payloadInt(tr, "original_chars")
	fmt.Fprintf(b, "\nnote: %s truncated, kept %d of %d chars", stream, kept, original)
}

func dominantString(payload map[string]any, key string) (string, bool) {
	if s, ok := payload[key].(string); ok && strings.TrimSpace(s) != "" {
		return s, true
	}
	inner := payloadMap(payload, "payload")
	if s, ok := inner[key].(string); ok && strings.TrimSpace(s) != "" {
		return s, true
	}
	return "", false
}

// remainingData strips reserved/rendered keys and returns what is left for
// the data: line. The payload/metrics shells are flattened away when they
// only duplicate rendered content.
func remainingData(payload map[string]any, rendered ...string) map[string]any {
	skip := map[string]bool{"metadata": true, "truncated": true, "note": true, "warnings": true}
	for _, k := range rendered {
		if k != "" {
			skip[k] = true
		}
	}
	out := map[string]any{}
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if skip[k] {
			continue
		}
		out[k] = payload[k]
	}
	// Drop an inner payload map if the dominant string was its only content.
	if inner, ok := out["payload"].(map[string]any); ok {
		trimmed := map[string]any{}
		for k, v := range inner {
			if skip[k] {
				continue
			}
			trimmed[k] = v
		}
		if len(trimmed) == 0 {
			delete(out, "payload")
		} else {
			out["payload"] = trimmed
		}
	}
	return out
}

func payloadMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func payloadString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func payloadInt(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

func firstNonEmptyTextLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
