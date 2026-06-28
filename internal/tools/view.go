package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

const (
	defaultReadFileMaxLines     = 2000
	defaultReadFileMaxBytes     = 16 * 1024
	defaultReadFileFullMaxBytes = 32 * 1024
	defaultReadFileOutlineLines = 80
)

func (b *Toolset) readFile(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		FilePath string `json:"file_path"`
		Offset   *int   `json:"offset"`
		Limit    *int   `json:"limit"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	abs, err := b.safeToolResultReadPath(ctx, in.FilePath)
	if err != nil {
		return b.marshalReadPathError(call, in.FilePath, err), nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return b.marshalPathNotFound(call, in.FilePath, abs, err.Error()), nil
		}
		return marshalToolError(call, "read_failed", err.Error()), nil
	}
	if info.IsDir() {
		return marshalToolErrorWithRecovery(call, "not_file", abs+" is a directory; use list_dir or search_files for directories", toolRecoveryHint{
			Code:                "read_file_target_is_directory",
			RecommendedNextTool: "list_dir",
			RecommendedInput: map[string]any{
				"path": toolInputPath(in.FilePath),
			},
			Retryable: false,
			Reason:    "read_file requires a file path; inspect the directory first, then read a specific file",
		}), nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return b.marshalPathNotFound(call, in.FilePath, abs, err.Error()), nil
		}
		return marshalToolError(call, "read_failed", err.Error()), nil
	}
	text, _ := normalizeTextFileBytes(data)
	snapshotText := text
	b.storeFileState(abs, snapshotText)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
		text = strings.TrimSuffix(text, "\n")
	}
	total := len(lines)
	totalBytes := len([]byte(text))
	explicitRange := in.Offset != nil || in.Limit != nil
	start := 0
	if in.Offset != nil {
		start = *in.Offset
	}
	limit := defaultReadFileMaxLines
	limitWasDefaulted := in.Limit == nil
	if in.Limit != nil {
		limit = *in.Limit
	}
	note := ""
	if in.Offset == nil && in.Limit != nil {
		note = "offset was not provided; defaulted to 0. To read a different range, retry with both offset and limit."
	}
	if in.Offset != nil && in.Limit == nil {
		note = "limit was not provided; defaulted to 2000 lines. To read more or fewer lines, retry with both offset and limit."
	}
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	rel := b.displayPath(abs)

	if !explicitRange && totalBytes <= defaultReadFileFullMaxBytes {
		content := strings.Join(lines, "\n")
		result := readFileResult(rel, "full", total, totalBytes, len(lines), len([]byte(content)), 0, total, false, "", 0, false, false, content, "")
		if readFileFullResultFitsRegistryEnvelope(call.Name, result) {
			return marshalToolResult(call, result)
		}
	}

	if !explicitRange {
		result := buildReadFileOutlineResult(call.Name, rel, lines, total, totalBytes)
		return marshalToolResult(call, result)
	}

	selected, end, trunc := selectReadFileLines(lines, start, limit, defaultReadFileMaxBytes)
	truncatedLines := truncateReadFileDisplayLines(selected)
	content := strings.Join(selected, "\n")
	returnedBytes := len([]byte(content))
	autoTruncated := trunc.Truncated && (trunc.TruncatedBy == "bytes" || limitWasDefaulted)
	truncatedBy := ""
	if autoTruncated {
		truncatedBy = trunc.TruncatedBy
		if note != "" {
			note += " "
		}
		if trunc.FirstLineExceedsLimit {
			note += fmt.Sprintf("line %d exceeds the %d byte read_file limit; retry with a narrower shell command such as sed -n '%dp' < %s | head -c %d.", start+1, defaultReadFileMaxBytes, start+1, shellSingleQuote(rel), defaultReadFileMaxBytes)
		} else {
			note += fmt.Sprintf("read_file output truncated by %s; use offset=%d limit=%d to continue.", trunc.TruncatedBy, end, defaultReadFileMaxLines)
		}
	}
	result := readFileResult(rel, "range", total, totalBytes, len(selected), returnedBytes, start, end, autoTruncated, truncatedBy, truncatedLines, start > 0, end < total, content, note)
	return marshalToolResult(call, result)
}

func readFileResult(rel string, mode string, totalLines int, totalBytes int, returnedLines int, returnedBytes int, start int, end int, truncated bool, truncatedBy string, truncatedLines int, hasMoreBefore bool, hasMoreAfter bool, content string, note string) map[string]any {
	result := map[string]any{
		"status": "ok",
		"metrics": map[string]any{
			"mode":                 mode,
			"total_lines":          totalLines,
			"total_bytes":          totalBytes,
			"returned_lines":       returnedLines,
			"returned_bytes":       returnedBytes,
			"truncated":            truncated,
			"truncated_by":         truncatedBy,
			"max_lines":            defaultReadFileMaxLines,
			"max_bytes":            defaultReadFileMaxBytes,
			"truncated_line_count": truncatedLines,
		},
		"payload": map[string]any{
			"file_path": rel,
			"range": map[string]any{
				"start": start,
				"end":   end,
			},
			"has_more_before": hasMoreBefore,
			"has_more_after":  hasMoreAfter,
			"content":         content,
		},
		"summary": rel + ":" + fmt.Sprintf("%d-%d/%d", start, end, totalLines),
	}
	if note != "" {
		result["note"] = note
	}
	return result
}

func buildReadFileOutlineResult(toolName string, rel string, lines []string, total int, totalBytes int) map[string]any {
	headLines := min(defaultReadFileOutlineLines, total)
	for {
		result := readFileOutlineResult(rel, lines, total, totalBytes, headLines)
		if readFileResultFitsRegistryEnvelope(toolName, result) || headLines <= 1 {
			return result
		}
		headLines = max(headLines/2, 1)
	}
}

func readFileOutlineResult(rel string, lines []string, total int, totalBytes int, headLines int) map[string]any {
	headSelected := append([]string(nil), lines[:headLines]...)
	truncatedLines := truncateReadFileDisplayLines(headSelected)
	head := strings.Join(headSelected, "\n")
	recovery := readFileOutlineRecovery(rel, truncatedLines)
	content := formatReadFileOutline(rel, total, totalBytes, headLines, head, recovery)
	result := readFileResult(rel, "outline", total, totalBytes, headLines, len([]byte(content)), 0, headLines, true, "outline", truncatedLines, false, headLines < total, content, "large file outline mode; "+recovery)
	result["outline"] = map[string]any{
		"head_lines":      headLines,
		"threshold_bytes": defaultReadFileFullMaxBytes,
	}
	return result
}

func readFileOutlineRecovery(rel string, truncatedLines int) string {
	if truncatedLines > 0 {
		return fmt.Sprintf("one or more displayed lines were shortened; use shell_run with `sed -n '1p' < %s | head -c %d` for the first oversized line, or a narrower shell command for the exact byte range.", shellSingleQuote(rel), defaultReadFileMaxBytes)
	}
	return fmt.Sprintf("use offset=0 limit=%d to read a bounded range.", defaultReadFileMaxLines)
}

func shellSingleQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func truncateReadFileDisplayLines(lines []string) int {
	truncatedLines := 0
	for i := range lines {
		r := []rune(lines[i])
		if len(r) > maxViewLineChars {
			lines[i] = string(r[:maxViewLineChars]) + "...[line truncated]"
			truncatedLines++
		}
	}
	return truncatedLines
}

func readFileFullResultFitsRegistryEnvelope(toolName string, result map[string]any) bool {
	return readFileResultFitsRegistryEnvelope(toolName, result)
}

func readFileResultFitsRegistryEnvelope(toolName string, result map[string]any) bool {
	summary, _ := result["summary"].(string)
	b, err := json.Marshal(core.ToolEnvelope{
		OK:      true,
		Success: true,
		Code:    "ok",
		Summary: summary,
		Data:    result,
		Metadata: map[string]any{
			"source_tool": toolName,
			"duration_ms": int64(0),
		},
	})
	return err == nil && len(b) <= core.DefaultMaxToolResultChars-1024
}

func formatReadFileOutline(rel string, totalLines int, totalBytes int, headLines int, head string, recovery string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[large file: %d bytes, %d lines; outline mode threshold %d bytes]\n\n", totalBytes, totalLines, defaultReadFileFullMaxBytes)
	fmt.Fprintf(&b, "[head %d lines for orientation]\n", headLines)
	if head != "" {
		b.WriteString(head)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n[to read more, %s]", recovery)
	return b.String()
}

type readFileTruncation struct {
	Truncated             bool
	TruncatedBy           string
	FirstLineExceedsLimit bool
}

func selectReadFileLines(lines []string, start int, limit int, maxBytes int) ([]string, int, readFileTruncation) {
	if start >= len(lines) {
		return nil, start, readFileTruncation{}
	}
	if limit <= 0 {
		limit = defaultReadFileMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = defaultReadFileMaxBytes
	}

	selected := make([]string, 0, min(limit, len(lines)-start))
	bytesUsed := 0
	end := start
	for i := start; i < len(lines) && len(selected) < limit; i++ {
		line := lines[i]
		lineBytes := len([]byte(line))
		addBytes := lineBytes
		if len(selected) > 0 {
			addBytes++ // newline between returned lines
		}
		if bytesUsed+addBytes > maxBytes {
			if len(selected) == 0 && lineBytes > maxBytes {
				return selected, end, readFileTruncation{
					Truncated:             true,
					TruncatedBy:           "bytes",
					FirstLineExceedsLimit: true,
				}
			}
			return selected, end, readFileTruncation{Truncated: true, TruncatedBy: "bytes"}
		}
		selected = append(selected, line)
		bytesUsed += addBytes
		end = i + 1
	}
	if end < len(lines) && len(selected) >= limit {
		return selected, end, readFileTruncation{Truncated: true, TruncatedBy: "lines"}
	}
	if bytesUsed > maxBytes {
		return selected, end, readFileTruncation{Truncated: true, TruncatedBy: "bytes"}
	}
	return selected, end, readFileTruncation{}
}
