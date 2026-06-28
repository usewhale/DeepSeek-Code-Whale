package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/usewhale/whale/internal/core"
)

const (
	defaultGrepLimit       = 100
	maxGrepLimit           = 2000
	maxGrepLineChars       = 500
	grepScannerBufferBytes = 1024 * 1024
)

func (b *Toolset) searchContent(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in grepInput
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return marshalToolError(call, "invalid_args", "pattern is required"), nil
	}
	abs, err := b.safeToolResultReadPath(ctx, in.Path)
	if err != nil {
		return b.marshalReadPathError(call, in.Path, err), nil
	}

	limit := normalizeGrepLimit(in.Limit)
	matches, byFile, meta, err := b.runContentSearch(in, abs, limit)
	if err != nil {
		if isInvalidGrepPatternError(err) {
			return marshalToolErrorWithRecovery(call, "invalid_pattern", err.Error(), grepInvalidPatternRecovery()), nil
		}
		return marshalToolError(call, "exec_failed", err.Error()), nil
	}
	return marshalToolResult(call, buildGrepResult(in.Pattern, matches, byFile, meta, limit))
}

type grepInput struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Include string `json:"include"`
	Limit   int    `json:"limit"`
}

func (b *Toolset) runContentSearch(in grepInput, abs string, limit int) ([]matchRow, map[string]int, grepMeta, error) {
	matches, byFile, meta, searchErr := searchWithRipgrep(in.Pattern, abs, in.Include, limit, b.displayPath)
	if searchErr != nil {
		matches, byFile, meta, searchErr = searchWithGo(in.Pattern, abs, in.Include, limit, b.displayPath)
		if searchErr != nil {
			return nil, nil, grepMeta{}, searchErr
		}
	}
	return matches, byFile, meta, nil
}

func isInvalidGrepPatternError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "invalid include pattern") {
		return false
	}
	return strings.Contains(msg, "invalid pattern:") || strings.Contains(msg, "regex parse error")
}

func grepInvalidPatternRecovery() toolRecoveryHint {
	return toolRecoveryHint{
		Code:                "grep_invalid_pattern",
		RecommendedNextTool: "grep",
		Retryable:           true,
		Reason:              "grep pattern is parsed as a regular expression (RE2/ripgrep syntax); escape regexp metacharacters to match them literally (e.g. interface\\{\\} to find interface{})",
	}
}

func buildGrepResult(pattern string, matches []matchRow, byFile map[string]int, meta grepMeta, limit int) map[string]any {
	summaryParts := make([]string, 0, maxSummarySamples)
	for f, c := range byFile {
		summaryParts = append(summaryParts, f+":"+strconv.Itoa(c))
		if len(summaryParts) >= maxSummarySamples {
			break
		}
	}
	summary := strings.Join(summaryParts, " | ")
	if meta.MatchLimitReached {
		hintLimit := min(limit*2, maxGrepLimit)
		if summary != "" {
			summary += " | "
		}
		summary += fmt.Sprintf("%d matches limit reached; use limit=%d or refine pattern/path/include", limit, hintLimit)
	}
	if meta.LinesTruncated {
		if summary != "" {
			summary += " | "
		}
		summary += fmt.Sprintf("some lines truncated to %d chars; use read_file for full lines", maxGrepLineChars)
	}
	result := map[string]any{
		"status": "ok",
		"metrics": map[string]any{
			"total_matches":       len(matches),
			"returned_matches":    len(matches),
			"files_matched":       len(byFile),
			"pattern_length":      len([]rune(pattern)),
			"match_limit":         limit,
			"match_limit_reached": meta.MatchLimitReached,
			"lines_truncated":     meta.LinesTruncated,
			"truncated":           meta.Truncated(),
			"truncated_by":        meta.TruncatedBy(),
		},
		"payload": map[string]any{
			"matches": matches,
		},
		"summary": summary,
	}
	return result
}

type submatch struct {
	Match string `json:"match"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type grepMeta struct {
	MatchLimitReached bool
	LinesTruncated    bool
}

func (m grepMeta) Truncated() bool {
	return m.MatchLimitReached || m.LinesTruncated
}

func (m grepMeta) TruncatedBy() []string {
	var out []string
	if m.MatchLimitReached {
		out = append(out, "matches")
	}
	if m.LinesTruncated {
		out = append(out, "line_length")
	}
	return out
}

type matchRow struct {
	File       string     `json:"file"`
	LineNumber int        `json:"line_number"`
	Line       string     `json:"line"`
	Submatches []submatch `json:"submatches"`
}

// searchWithRipgrep tries to use ripgrep (rg) for fast searching.
// Returns an error if rg is not available or fails.
func searchWithRipgrep(pattern, path, include string, limit int, displayPath func(string) string) ([]matchRow, map[string]int, grepMeta, error) {
	if _, err := exec.LookPath("rg"); err != nil {
		return nil, nil, grepMeta{}, fmt.Errorf("rg not found: %w", err)
	}

	args := []string{"-n", "--no-heading", "--json"}
	if strings.TrimSpace(include) != "" {
		args = append(args, "-g", include)
	}
	args = append(args, pattern, path)
	cmd := exec.Command("rg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, grepMeta{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, grepMeta{}, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, grepMeta{}, err
	}

	var matches []matchRow
	byFile := map[string]int{}
	meta := grepMeta{}
	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- strings.TrimSpace(string(data))
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), grepScannerBufferBytes)
	for sc.Scan() {
		line := sc.Text()
		var evt map[string]any
		if json.Unmarshal([]byte(line), &evt) != nil {
			continue
		}
		if evt["type"] != "match" {
			continue
		}
		data, _ := evt["data"].(map[string]any)
		pathObj, _ := data["path"].(map[string]any)
		textObj, _ := data["lines"].(map[string]any)
		rawPath, _ := pathObj["text"].(string)
		rawLine, _ := textObj["text"].(string)
		num, _ := data["line_number"].(float64)

		rel := filepath.ToSlash(rawPath)
		if displayPath != nil {
			rel = displayPath(rawPath)
		}
		row := matchRow{
			File:       rel,
			LineNumber: int(num),
			Line:       strings.TrimRight(rawLine, "\n"),
		}
		if sms, ok := data["submatches"].([]any); ok {
			for _, one := range sms {
				obj, _ := one.(map[string]any)
				mobj, _ := obj["match"].(map[string]any)
				mv, _ := mobj["text"].(string)
				sv, _ := obj["start"].(float64)
				ev, _ := obj["end"].(float64)
				row.Submatches = append(row.Submatches, submatch{Match: mv, Start: int(sv), End: int(ev)})
			}
		}
		if truncated, ok := truncateGrepMatchRow(row); ok {
			row = truncated
			meta.LinesTruncated = true
		}
		matches = append(matches, row)
		byFile[row.File]++
		if len(matches) >= limit {
			meta.MatchLimitReached = true
			_ = cmd.Process.Kill()
			break
		}
	}
	if err := sc.Err(); err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		<-stderrDone
		if meta.MatchLimitReached {
			return matches, byFile, meta, nil
		}
		return nil, nil, grepMeta{}, err
	}
	err = cmd.Wait()
	stderrText := <-stderrDone
	if err != nil && !meta.MatchLimitReached {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return matches, byFile, meta, nil
		}
		if stderrText != "" {
			return nil, nil, grepMeta{}, fmt.Errorf("%w: %s", err, stderrText)
		}
		return nil, nil, grepMeta{}, err
	}
	return matches, byFile, meta, nil
}

// searchWithGo is a pure-Go fallback when ripgrep is not available.
// It walks the directory tree and searches file contents with Go's regexp.
func searchWithGo(pattern, path, include string, limit int, displayPath func(string) string) ([]matchRow, map[string]int, grepMeta, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, nil, grepMeta{}, fmt.Errorf("invalid pattern: %w", err)
	}

	var includeRe *regexp.Regexp
	if strings.TrimSpace(include) != "" {
		includeRe, err = globToRegexp(include)
		if err != nil {
			return nil, nil, grepMeta{}, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	var matches []matchRow
	byFile := map[string]int{}
	meta := grepMeta{}

	err = filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
		}
		// Skip hidden files and directories (like rg does by default)
		base := filepath.Base(filePath)
		if info.IsDir() {
			if base != "." && base != ".." && strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(base, ".") {
			return nil
		}
		// Apply include filter
		if includeRe != nil && !includeRe.MatchString(filePath) {
			return nil
		}
		// Skip binary files
		if isLikelyBinary(filePath) {
			return nil
		}

		fileMatches, err := grepFile(filePath, re, displayPath)
		if err != nil {
			return nil
		}
		for _, m := range fileMatches {
			if truncated, ok := truncateGrepMatchRow(m); ok {
				m = truncated
				meta.LinesTruncated = true
			}
			matches = append(matches, m)
			byFile[m.File]++
			if len(matches) >= limit {
				meta.MatchLimitReached = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, grepMeta{}, err
	}
	return matches, byFile, meta, nil
}

// grepFile searches a single file for regex matches, returning match rows.
func grepFile(filePath string, re *regexp.Regexp, displayPath func(string) string) ([]matchRow, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var matches []matchRow
	text, _ := normalizeTextFileBytes(data)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for idx, line := range lines {
		lineNum := idx + 1
		line = strings.TrimSuffix(line, "\r")
		locs := re.FindAllStringIndex(line, -1)
		if len(locs) == 0 {
			continue
		}
		rel := filepath.ToSlash(filePath)
		if displayPath != nil {
			rel = displayPath(filePath)
		}
		row := matchRow{
			File:       rel,
			LineNumber: lineNum,
			Line:       line,
		}
		for _, loc := range locs {
			row.Submatches = append(row.Submatches, submatch{
				Match: line[loc[0]:loc[1]],
				Start: loc[0],
				End:   loc[1],
			})
		}
		matches = append(matches, row)
	}
	return matches, nil
}

func truncateGrepLine(line string) (string, bool) {
	if len([]rune(line)) <= maxGrepLineChars {
		return line, false
	}
	end := runeIndexToByteOffset(line, maxGrepLineChars)
	return line[:end] + "...", true
}

func truncateGrepLineAroundMatches(line string, submatches []submatch) (string, []submatch, bool) {
	lineRunes := len([]rune(line))
	if lineRunes <= maxGrepLineChars {
		return line, submatches, false
	}
	if len(submatches) == 0 {
		truncated, _ := truncateGrepLine(line)
		return truncated, nil, true
	}

	first := submatches[0]
	if first.Start < 0 {
		first.Start = 0
	}
	if first.End < first.Start {
		first.End = first.Start
	}
	if first.Start > len(line) {
		first.Start = len(line)
	}
	if first.End > len(line) {
		first.End = len(line)
	}
	first.Start = utf8BoundaryBeforeOrAt(line, first.Start)
	first.End = utf8BoundaryAfterOrAt(line, first.End)

	matchStartRune := byteOffsetToRuneIndex(line, first.Start)
	matchEndRune := byteOffsetToRuneIndex(line, first.End)
	matchLen := max(matchEndRune-matchStartRune, 0)
	windowLen := maxGrepLineChars
	startRune := matchStartRune
	if matchLen < windowLen {
		startRune = matchStartRune - (windowLen-matchLen)/2
	}
	if startRune < 0 {
		startRune = 0
	}
	endRune := startRune + windowLen
	if endRune > lineRunes {
		endRune = lineRunes
		startRune = max(0, endRune-windowLen)
	}
	start := runeIndexToByteOffset(line, startRune)
	end := runeIndexToByteOffset(line, endRune)

	prefix := ""
	if startRune > 0 {
		prefix = "..."
	}
	suffix := ""
	if endRune < lineRunes {
		suffix = "..."
	}
	snippet := prefix + line[start:end] + suffix
	offset := len(prefix) - start
	adjusted := make([]submatch, 0, len(submatches))
	for _, sm := range submatches {
		if sm.Start < start || sm.End > end || sm.Start > sm.End {
			continue
		}
		next := sm
		next.Start = sm.Start + offset
		next.End = sm.End + offset
		if next.Start < 0 || next.End > len(snippet) || next.Start > next.End {
			continue
		}
		adjusted = append(adjusted, next)
	}
	return snippet, adjusted, true
}

func utf8BoundaryBeforeOrAt(s string, index int) int {
	if index <= 0 {
		return 0
	}
	if index >= len(s) {
		return len(s)
	}
	for index > 0 && !utf8.RuneStart(s[index]) {
		index--
	}
	return index
}

func byteOffsetToRuneIndex(s string, byteOffset int) int {
	if byteOffset <= 0 {
		return 0
	}
	if byteOffset >= len(s) {
		return len([]rune(s))
	}
	runeIndex := 0
	for i := range s {
		if i >= byteOffset {
			return runeIndex
		}
		runeIndex++
	}
	return runeIndex
}

func runeIndexToByteOffset(s string, runeIndex int) int {
	if runeIndex <= 0 {
		return 0
	}
	current := 0
	for i := range s {
		if current == runeIndex {
			return i
		}
		current++
	}
	return len(s)
}

func normalizeGrepLimit(limit int) int {
	if limit <= 0 {
		return defaultGrepLimit
	}
	return min(limit, maxGrepLimit)
}

func truncateGrepMatchRow(row matchRow) (matchRow, bool) {
	line, submatches, truncated := truncateGrepLineAroundMatches(row.Line, row.Submatches)
	if !truncated {
		return row, false
	}
	row.Line = line
	row.Submatches = submatches
	return row, true
}

func utf8BoundaryAfterOrAt(s string, index int) int {
	if index <= 0 {
		return 0
	}
	if index >= len(s) {
		return len(s)
	}
	for index < len(s) && !utf8.RuneStart(s[index]) {
		index++
	}
	return index
}

// globToRegexp converts a simple glob pattern to a compiled regexp.
// Supports *, ?, and {a,b} alternation.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	pattern := strings.ReplaceAll(glob, ".", "\\.")
	pattern = strings.ReplaceAll(pattern, "*", ".*")
	pattern = strings.ReplaceAll(pattern, "?", ".")
	// Handle {a,b} alternation
	braceRe := regexp.MustCompile(`\{([^}]+)\}`)
	pattern = braceRe.ReplaceAllStringFunc(pattern, func(m string) string {
		inner := m[1 : len(m)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})
	return regexp.Compile(pattern)
}

// isLikelyBinary checks if a file appears to be binary by looking for null bytes.
func isLikelyBinary(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return true // can't open, skip
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil {
		return n == 0
	}
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}
