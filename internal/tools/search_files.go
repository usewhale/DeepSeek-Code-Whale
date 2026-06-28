package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
)

const (
	defaultSearchFilesLimit = 200
	maxSearchFilesLimit     = 2000
	defaultSearchFilesTime  = 20 * time.Second
)

var defaultIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".next":        true,
	"target":       true,
}

type fileSearchMeta struct {
	MatchLimitReached bool
	Cancelled         bool
	TimedOut          bool
	Fallback          string
	Elapsed           time.Duration
}

func normalizeSearchFilesLimit(limit int) int {
	if limit <= 0 {
		return defaultSearchFilesLimit
	}
	return min(limit, maxSearchFilesLimit)
}

func (b *Toolset) searchFiles(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		return marshalToolError(call, "invalid_args", "pattern is required"), nil
	}
	limit := normalizeSearchFilesLimit(in.Limit)
	abs, err := b.safeToolResultReadPath(ctx, in.Path)
	if err != nil {
		return b.marshalReadPathError(call, in.Path, err), nil
	}

	searchCtx, cancel := context.WithTimeout(ctx, defaultSearchFilesTime)
	defer cancel()

	asGlob := patternIsGlob(pattern)
	start := time.Now()
	matches, meta, err := searchFileNamesWithRipgrep(searchCtx, abs, pattern, asGlob, limit, b.displayPath)
	if err != nil && !isContextStopped(err) {
		matches, meta, err = searchFileNamesWithGo(searchCtx, abs, pattern, asGlob, limit, b.displayPath)
	}
	meta.Elapsed = time.Since(start)
	if err != nil && !isContextStopped(err) {
		return marshalToolError(call, "exec_failed", err.Error()), nil
	}
	if errors.Is(err, context.Canceled) {
		meta.Cancelled = true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		meta.TimedOut = true
	}
	sort.Strings(matches)
	return marshalToolResult(call, buildSearchFilesResult(matches, meta, limit))
}

func isContextStopped(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// patternIsGlob reports whether a search_files pattern should be matched as a
// glob (e.g. **/*.go, *.test.js) rather than a case-insensitive substring. Only
// the unambiguous glob metacharacters * ? { signal glob intent; bare words keep
// the historical substring behavior so callers can still find "version" ->
// version.go. '[' deliberately does NOT trigger glob mode on its own, because
// literal brackets are common in real paths (e.g. Next.js app/users/[id]/page.tsx)
// and treating them as character classes would regress substring search. A
// bracket class is still honored when the pattern is already a glob via * ? {
// (e.g. *.[jt]s) — see compileSearchFilesGlob.
func patternIsGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?{")
}

func searchFileNamesWithRipgrep(ctx context.Context, abs string, pattern string, asGlob bool, limit int, displayPath func(string) string) ([]string, fileSearchMeta, error) {
	if _, err := exec.LookPath("rg"); err != nil {
		return nil, fileSearchMeta{}, err
	}
	args := []string{"--files", "--hidden", "--no-ignore"}
	// The user's include glob must come BEFORE the ignore globs: ripgrep gives
	// later glob rules precedence, so the negative defaultIgnoredDirs globs have
	// to be last to keep excluding node_modules/vendor/etc. for glob searches —
	// matching the Go fallback, which always skips those directories.
	if asGlob {
		args = append(args, "--glob", pattern)
	}
	for dir := range defaultIgnoredDirs {
		args = append(args, "--glob", "!**/"+dir+"/**")
	}
	// Search "." from within abs (cmd.Dir) rather than passing the absolute root
	// as the target: ripgrep evaluates --glob against paths relative to the search
	// target, so a scoped glob like src/**/*.ts only matches when paths are emitted
	// relative to the root. Passing /abs/root would make rg emit absolute paths the
	// scoped glob can never match, silently yielding zero results.
	args = append(args, ".")
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = abs
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fileSearchMeta{}, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fileSearchMeta{}, err
	}

	matches := make([]string, 0, min(limit, 128))
	pat := strings.ToLower(pattern)
	meta := fileSearchMeta{}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
			break
		}
		rawPath := strings.TrimSpace(sc.Text())
		if rawPath == "" {
			continue
		}
		if !filepath.IsAbs(rawPath) {
			rawPath = filepath.Join(abs, rawPath)
		}
		// In glob mode ripgrep's --glob already filtered the file list, so accept
		// every returned path; otherwise apply the case-insensitive substring match.
		if asGlob || searchFilePathMatches(abs, rawPath, pat) {
			matches = append(matches, displayPath(rawPath))
			if len(matches) >= limit {
				meta.MatchLimitReached = true
				_ = cmd.Process.Kill()
				break
			}
		}
	}
	if err := sc.Err(); err != nil && !meta.MatchLimitReached && ctx.Err() == nil {
		_ = cmd.Wait()
		return matches, meta, err
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		meta.applyContextStop(ctx.Err())
		return matches, meta, ctx.Err()
	}
	if waitErr != nil && !meta.MatchLimitReached && !isRipgrepNoMatch(waitErr) {
		return matches, meta, waitErr
	}
	return matches, meta, nil
}

// isRipgrepNoMatch reports whether err is ripgrep's "no matches" exit (status 1).
// rg exits 1 when --files plus the glob filters select no files, which is a
// successful empty result, not a tool failure — returning it as an error would
// wrongly trigger the Go-walker fallback (a second full tree scan reported as
// "ripgrep unavailable"). A real rg error exits 2 and is still surfaced.
func isRipgrepNoMatch(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func searchFileNamesWithGo(ctx context.Context, abs string, pattern string, asGlob bool, limit int, displayPath func(string) string) ([]string, fileSearchMeta, error) {
	matches := make([]string, 0, min(limit, 128))
	pat := strings.ToLower(pattern)
	var globRe *regexp.Regexp
	if asGlob {
		re, err := compileSearchFilesGlob(pattern)
		if err != nil {
			return nil, fileSearchMeta{Fallback: "go_walk"}, fmt.Errorf("invalid glob pattern: %w", err)
		}
		globRe = re
	}
	meta := fileSearchMeta{Fallback: "go_walk"}
	err := filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			meta.applyContextStop(ctx.Err())
			return ctx.Err()
		}
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if defaultIgnoredDirs[d.Name()] && path != abs {
				return filepath.SkipDir
			}
			return nil
		}
		if searchFileNameMatchesGlob(abs, path, pat, globRe) {
			matches = append(matches, displayPath(path))
			if len(matches) >= limit {
				meta.MatchLimitReached = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if errors.Is(err, context.Canceled) {
		return matches, meta, err
	}
	if err != nil {
		return matches, meta, err
	}
	return matches, meta, nil
}

func (m *fileSearchMeta) applyContextStop(err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		m.TimedOut = true
		return
	}
	if errors.Is(err, context.Canceled) {
		m.Cancelled = true
	}
}

func searchFilePathMatches(root string, path string, lowerPattern string) bool {
	rel := filepath.ToSlash(path)
	if r, err := filepath.Rel(root, path); err == nil && r != "." && !strings.HasPrefix(r, "..") && !filepath.IsAbs(r) {
		rel = filepath.ToSlash(r)
	}
	return strings.Contains(strings.ToLower(rel), lowerPattern) ||
		strings.Contains(strings.ToLower(filepath.Base(path)), lowerPattern)
}

// searchFileNameMatchesGlob is the Go-fallback matcher. When globRe is non-nil
// the pattern is matched as a glob against the path relative to root (the
// regexp built by compileSearchFilesGlob is fully anchored and already accounts
// for matching at any depth); otherwise it falls back to the case-insensitive
// substring match.
func searchFileNameMatchesGlob(root string, path string, lowerPattern string, globRe *regexp.Regexp) bool {
	if globRe == nil {
		return searchFilePathMatches(root, path, lowerPattern)
	}
	rel := filepath.ToSlash(path)
	if r, err := filepath.Rel(root, path); err == nil && r != "." && !strings.HasPrefix(r, "..") && !filepath.IsAbs(r) {
		rel = filepath.ToSlash(r)
	}
	return globRe.MatchString(rel)
}

// compileSearchFilesGlob converts a search_files glob pattern into an anchored
// regexp matched against the slash-separated path relative to the search root.
// It mirrors ripgrep's --glob (gitignore-style) semantics for the Go fallback so
// results agree whether or not ripgrep is installed:
//   - "**/" matches zero or more leading path components, so **/*.go matches both
//     version.go at the root and sub/alpha.go;
//   - a pattern with no "/" matches at any depth (i.e. against the base name);
//   - "*" and "?" do not cross path separators; "{a,b}" is alternation.
func compileSearchFilesGlob(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteByte('^')
	// A slashless pattern (e.g. *.go) is not anchored to the root: it matches in
	// any directory, so allow an optional leading path prefix.
	if !strings.Contains(glob, "/") {
		b.WriteString("(?:.*/)?")
	}
	braceDepth := 0
	runes := []rune(glob)
	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; c {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++ // consume the second '*'
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++ // consume the '/'; "**/" spans zero or more components
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			// Translate a glob bracket class (e.g. [jt], [a-z], [!x]) into the
			// equivalent regexp class so the fallback mirrors ripgrep. A class
			// that is unterminated or empty is treated as a literal '['.
			if class, next, ok := globBracketClass(runes, i); ok {
				b.WriteString(class)
				i = next
			} else {
				b.WriteString("\\[")
			}
		case '{':
			braceDepth++
			b.WriteString("(?:")
		case '}':
			if braceDepth > 0 {
				braceDepth--
				b.WriteByte(')')
			} else {
				b.WriteString("\\}")
			}
		case ',':
			if braceDepth > 0 {
				b.WriteByte('|')
			} else {
				b.WriteByte(',')
			}
		case '.', '+', '(', ')', '|', '^', '$', '\\', ']':
			b.WriteByte('\\')
			b.WriteRune(c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}

// globBracketClass translates a glob bracket expression starting at runes[start]
// (which is '[') into the equivalent regexp character class. It returns the
// regexp fragment, the index of the closing ']', and whether a valid, non-empty
// class was found. Glob negation "[!...]" maps to regexp "[^...]"; metacharacters
// inside the class are emitted verbatim except for the regexp-special ']', '^',
// and '\\', which are escaped. A "]" immediately after the (optional) negation
// is treated as a literal member, mirroring shell/ripgrep semantics.
func globBracketClass(runes []rune, start int) (string, int, bool) {
	i := start + 1
	var b strings.Builder
	b.WriteByte('[')
	if i < len(runes) && runes[i] == '!' {
		b.WriteByte('^')
		i++
	}
	// A ']' as the first class member is literal, not the terminator.
	if i < len(runes) && runes[i] == ']' {
		b.WriteString("\\]")
		i++
	}
	for ; i < len(runes); i++ {
		c := runes[i]
		if c == ']' {
			b.WriteByte(']')
			return b.String(), i, true
		}
		switch c {
		case '\\', '^':
			b.WriteByte('\\')
			b.WriteRune(c)
		default:
			b.WriteRune(c)
		}
	}
	return "", start, false // unterminated -> treat '[' as literal
}

func buildSearchFilesResult(matches []string, meta fileSearchMeta, limit int) map[string]any {
	truncated := meta.MatchLimitReached || meta.Cancelled || meta.TimedOut
	summaryParts := make([]string, 0, 3)
	if meta.MatchLimitReached {
		hintLimit := min(limit*2, maxSearchFilesLimit)
		summaryParts = append(summaryParts, fmt.Sprintf("%d file matches limit reached; use limit=%d or refine path/pattern", limit, hintLimit))
	}
	if meta.Cancelled {
		summaryParts = append(summaryParts, "search cancelled; refine path/pattern before retrying broad searches")
	}
	if meta.TimedOut {
		summaryParts = append(summaryParts, "search timed out; refine path/pattern before retrying broad searches")
	}
	if meta.Fallback != "" {
		summaryParts = append(summaryParts, "ripgrep unavailable; used Go filesystem walk fallback")
	}
	truncatedBy := ""
	switch {
	case meta.Cancelled:
		truncatedBy = "cancelled"
	case meta.TimedOut:
		truncatedBy = "timeout"
	case meta.MatchLimitReached:
		truncatedBy = "match_limit"
	}
	return map[string]any{
		"status": "ok",
		"metrics": map[string]any{
			"total_matches":       len(matches),
			"returned":            len(matches),
			"match_limit":         limit,
			"match_limit_reached": meta.MatchLimitReached,
			"truncated":           truncated,
			"truncated_by":        truncatedBy,
			"cancelled":           meta.Cancelled,
			"timed_out":           meta.TimedOut,
			"elapsed_ms":          meta.Elapsed.Milliseconds(),
		},
		"payload": map[string]any{
			"items": matches,
		},
		"summary": strings.Join(summaryParts, " | "),
	}
}
