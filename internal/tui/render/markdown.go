package render

import (
	"bytes"
	"strings"

	"github.com/charmbracelet/glamour/ansi"
	"github.com/muesli/termenv"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmrenderer "github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"

	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

func boolPtr(v bool) *bool       { return &v }
func stringPtr(v string) *string { return &v }
func uintPtr(v uint) *uint       { return &v }

const (
	markdownRendererPriority = 1000
	markdownSpecialChars     = "\\`*_{}[]<>()#+-.!"
)

type autolinkEscaping bool

const (
	escapeAutolinksForRenderer autolinkEscaping = true
	stripAutolinkBracketsOnly  autolinkEscaping = false
)

func Markdown(input string, width int, quiet bool) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	if width < 20 {
		// Narrow fallback skips goldmark. No wrap, so terminal URL autodetect
		// already finds the full link — no OSC 8 needed and no code marking.
		input = normalizeMarkdownLinks(input, stripAutolinkBracketsOnly)
		return strings.TrimRight(input, "\n")
	}
	input = normalizeMarkdownLinks(input, escapeAutolinksForRenderer)
	key := markdownCacheKey{input: input, width: width, quiet: quiet}
	if cached, ok := markdownCacheGet(key); ok {
		return cached
	}
	style := markdownStyle()
	if quiet {
		style = quietMarkdownStyle()
	}
	rendered, err := renderMarkdown(input, width, style)
	if err != nil {
		return injectHyperlinks(strings.TrimRight(input, "\n"))
	}
	out := injectHyperlinks(strings.TrimRight(rendered, "\n"))
	markdownCachePut(key, out)
	return out
}

// urlSkipMark is a zero-width C0 control byte (FS) inserted right before
// http(s):// URLs that originate inside fenced/inline code regions. Glamour
// preserves the byte and treats it as zero width during wrap, so injecting it
// in the markdown source doesn't shift any visible columns. injectHyperlinks
// reads the marker to suppress OSC 8 wrapping for those URLs and then strips
// the byte from the final output.
const urlSkipMark = '\x1c'

// injectHyperlinks wraps http(s) URLs in OSC 8 escape sequences so terminal
// clicks resolve to the full URL even when word-wrap inserted newlines and
// padding inside the URL text. Glamour's ANSI renderer breaks long URLs at
// hyphens; without OSC 8, terminals fall back to text-based URL detection,
// which stops at the first whitespace/newline and yields a truncated link.
func injectHyperlinks(s string) string {
	if !strings.Contains(s, "http://") && !strings.Contains(s, "https://") {
		if strings.IndexByte(s, urlSkipMark) >= 0 {
			return strings.ReplaceAll(s, string(urlSkipMark), "")
		}
		return s
	}
	var out strings.Builder
	out.Grow(len(s) + 32)
	i := 0
	for i < len(s) {
		start := findURLStart(s, i)
		if start < 0 {
			out.WriteString(s[i:])
			break
		}
		skip := start > 0 && s[start-1] == urlSkipMark
		if skip {
			out.WriteString(s[i : start-1])
		} else {
			out.WriteString(s[i:start])
		}
		end, url := scanWrappedURL(s, start)
		if end <= start || url == "" {
			out.WriteByte(s[start])
			i = start + 1
			continue
		}
		if skip {
			out.WriteString(s[start:end])
		} else {
			out.WriteString("\x1b]8;;")
			out.WriteString(url)
			out.WriteString("\x07")
			out.WriteString(s[start:end])
			out.WriteString("\x1b]8;;\x07")
		}
		i = end
	}
	result := out.String()
	if strings.IndexByte(result, urlSkipMark) >= 0 {
		result = strings.ReplaceAll(result, string(urlSkipMark), "")
	}
	return result
}

func findURLStart(s string, from int) int {
	for {
		hi := strings.Index(s[from:], "http")
		if hi < 0 {
			return -1
		}
		p := from + hi
		rest := s[p:]
		switch {
		case strings.HasPrefix(rest, "https://"), strings.HasPrefix(rest, "http://"):
			return p
		}
		from = p + 4
	}
}

// scanWrappedURL walks URL characters starting at start, treating
// "[spaces]\n[spaces]" (glamour's wrap padding) as a gap that joins URL
// fragments when the next line still looks like URL syntax. Returns the end
// index in s (one past the last consumed byte) and the reconstructed URL (with
// joined gaps removed). Trailing punctuation that commonly trails URLs in prose
// (.,;:!?) and unbalanced closers (), ], >) is trimmed off both the returned
// URL and the end index.
func scanWrappedURL(s string, start int) (int, string) {
	var url strings.Builder
	i := start
	for i < len(s) {
		c := s[i]
		if isURLByte(c) {
			url.WriteByte(c)
			i++
			continue
		}
		if c == ' ' || c == '\n' {
			j := i
			for j < len(s) && s[j] == ' ' {
				j++
			}
			if j < len(s) && s[j] == '\n' {
				j++
				for j < len(s) && s[j] == ' ' {
					j++
				}
				if j < len(s) && isURLByte(s[j]) && shouldJoinWrappedURL(s, j, url.String()) {
					i = j
					continue
				}
			}
		}
		break
	}
	raw := url.String()
	for shouldTrimURLTail(raw) {
		raw = raw[:len(raw)-1]
		i--
	}
	return i, raw
}

func shouldJoinWrappedURL(s string, next int, raw string) bool {
	if raw == "" || next >= len(s) {
		return false
	}
	nextEnd := next
	for nextEnd < len(s) && isURLByte(s[nextEnd]) {
		nextEnd++
	}
	nextPart := s[next:nextEnd]
	if nextPart == "" {
		return false
	}
	if strings.IndexAny(nextPart, "/?#=&%") >= 0 {
		return true
	}
	switch nextPart[0] {
	case '/', '?', '#', '&', '=', '%', '.', '-', '_', '~':
		return true
	}
	switch raw[len(raw)-1] {
	case '/', '?', '#', '&', '=', '%', '.', '-', '_', '~', ':', '(', '[', ',':
		return true
	}
	return false
}

func shouldTrimURLTail(raw string) bool {
	if raw == "" {
		return false
	}
	last := raw[len(raw)-1]
	switch last {
	case '.', ',', ';', ':', '!', '?':
		return true
	case ')':
		return strings.Count(raw, ")") > strings.Count(raw, "(")
	case ']':
		return strings.Count(raw, "]") > strings.Count(raw, "[")
	case '>':
		return strings.Count(raw, ">") > strings.Count(raw, "<")
	default:
		return false
	}
}

// markCodeURLs prefixes every http(s):// occurrence with urlSkipMark so the
// post-render hyperlink injector knows to leave those URLs literal.
func markCodeURLs(s string, on bool) string {
	if !on || !strings.Contains(s, "http") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], "http")
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		p := i + idx
		rest := s[p:]
		if strings.HasPrefix(rest, "https://") || strings.HasPrefix(rest, "http://") {
			b.WriteString(s[i:p])
			b.WriteByte(urlSkipMark)
			b.WriteString("http")
			i = p + 4
		} else {
			b.WriteString(s[i : p+4])
			i = p + 4
		}
	}
	return b.String()
}

func isURLByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '-', '.', '_', '~', ':', '/', '?', '#', '[', ']', '@',
		'!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', '%':
		return true
	}
	return false
}

func renderMarkdown(input string, width int, style ansi.StyleConfig) (string, error) {
	opts := ansi.Options{
		WordWrap:     width,
		ColorProfile: termenv.TrueColor,
		Styles:       style,
	}
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.TaskList,
			extension.DefinitionList,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
	)
	ar := ansi.NewRenderer(opts)
	md.SetRenderer(gmrenderer.NewRenderer(
		gmrenderer.WithNodeRenderers(
			util.Prioritized(ar, markdownRendererPriority),
		),
	))

	var buf bytes.Buffer
	err := md.Convert([]byte(input), &buf)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func normalizeMarkdownLinks(input string, escaping autolinkEscaping) string {
	markCode := escaping == escapeAutolinksForRenderer
	var out strings.Builder
	for i := 0; i < len(input); {
		if inFence, fenceChar, fenceLen, _ := detectFence(input, i); inFence {
			lineEnd := nextLineEnd(input, i)
			end := findFenceEnd(input, lineEnd, fenceChar, fenceLen)
			if end < 0 {
				out.WriteString(markCodeURLs(input[i:], markCode))
				return out.String()
			}
			out.WriteString(markCodeURLs(input[i:end], markCode))
			i = end
			continue
		}
		if input[i] == '`' {
			end := findInlineCodeEnd(input, i)
			out.WriteString(markCodeURLs(input[i:end], markCode))
			i = end
			continue
		}
		if repl, next, ok := parseMarkdownAutolink(input, i, escaping); ok {
			out.WriteString(repl)
			i = next
			continue
		}
		if repl, next, ok := parseMarkdownLink(input, i); ok {
			out.WriteString(repl)
			i = next
			continue
		}
		out.WriteByte(input[i])
		i++
	}
	return out.String()
}

func detectFence(input string, start int) (bool, byte, int, int) {
	lineStart := start == 0 || input[start-1] == '\n'
	if !lineStart {
		return false, 0, 0, 0
	}
	j := start
	for j < len(input) && j-start < 3 && input[j] == ' ' {
		j++
	}
	if j >= len(input) {
		return false, 0, 0, 0
	}
	ch := input[j]
	if ch != '`' && ch != '~' {
		return false, 0, 0, 0
	}
	k := j
	for k < len(input) && input[k] == ch {
		k++
	}
	if k-j < 3 {
		return false, 0, 0, 0
	}
	return true, ch, k - j, j - start
}

func findFenceEnd(input string, start int, fenceChar byte, fenceLen int) int {
	i := start
	for i < len(input) {
		lineEnd := nextLineEnd(input, i)
		if ok, _, runLen, _ := detectFence(input, i); ok {
			j := i
			for j < len(input) && j-i < 3 && input[j] == ' ' {
				j++
			}
			if input[j] == fenceChar && runLen >= fenceLen {
				return lineEnd
			}
		}
		if lineEnd >= len(input) {
			return -1
		}
		i = lineEnd
	}
	return -1
}

func nextLineEnd(input string, start int) int {
	nl := strings.IndexByte(input[start:], '\n')
	if nl < 0 {
		return len(input)
	}
	return start + nl + 1
}

func findInlineCodeEnd(input string, start int) int {
	run := 0
	for start+run < len(input) && input[start+run] == '`' {
		run++
	}
	i := start + run
	for i < len(input) {
		if input[i] == '`' {
			j := i
			for j < len(input) && input[j] == '`' {
				j++
			}
			if j-i == run {
				return j
			}
			i = j
			continue
		}
		i++
	}
	return len(input)
}

func parseMarkdownAutolink(input string, start int, escaping autolinkEscaping) (string, int, bool) {
	if start >= len(input) || input[start] != '<' {
		return "", start, false
	}
	end := strings.IndexByte(input[start+1:], '>')
	if end < 0 {
		return "", start, false
	}
	end += start + 1
	target := input[start+1 : end]
	if target == "" || strings.ContainsAny(target, " \t\r\n") || !isMarkdownAutolinkTarget(target) {
		return "", start, false
	}
	if escaping == stripAutolinkBracketsOnly {
		return target, end + 1, true
	}
	return escapeMarkdownLiteral(target), end + 1, true
}

func isMarkdownAutolinkTarget(target string) bool {
	lower := strings.ToLower(target)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "mailto:")
}

func escapeMarkdownLiteral(input string) string {
	var out strings.Builder
	for i := 0; i < len(input); i++ {
		if strings.IndexByte(markdownSpecialChars, input[i]) >= 0 {
			out.WriteByte('\\')
		}
		out.WriteByte(input[i])
	}
	return out.String()
}

func parseMarkdownLink(input string, start int) (string, int, bool) {
	if start >= len(input) || input[start] != '[' {
		return "", start, false
	}
	if start > 0 && input[start-1] == '!' {
		return "", start, false
	}
	endText := -1
	depth := 0
	for i := start; i < len(input); i++ {
		switch input[i] {
		case '\\':
			i++
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				endText = i
				goto foundText
			}
		}
	}
foundText:
	if endText < 0 || endText+1 >= len(input) || input[endText+1] != '(' {
		return "", start, false
	}
	endURL := -1
	depth = 0
	for i := endText + 1; i < len(input); i++ {
		switch input[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				endURL = i
				goto foundURL
			}
		}
	}
foundURL:
	if endURL < 0 {
		return "", start, false
	}
	label := input[start+1 : endText]
	target := input[endText+2 : endURL]
	if label == target {
		return target, endURL + 1, true
	}
	return label + " (" + target + ")", endURL + 1, true
}

func markdownStyle() ansi.StyleConfig {
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{BlockPrefix: "", BlockSuffix: ""},
			Margin:         uintPtr(0),
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)},
		},
		Strong: ansi.StylePrimitive{Bold: boolPtr(true)},
		Emph:   ansi.StylePrimitive{Italic: boolPtr(true)},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(string(tuitheme.Default.Info)),
			},
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "│ ",
				Italic: boolPtr(true),
			},
		},
		List: ansi.StyleList{
			LevelIndent: 2,
			StyleBlock: ansi.StyleBlock{
				IndentToken: stringPtr(" "),
			},
		},
		Item: ansi.StylePrimitive{
			BlockPrefix: "• ",
		},
		Enumeration: ansi.StylePrimitive{
			BlockPrefix: ". ",
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Prefix: "  ",
				},
				Margin: uintPtr(1),
			},
		},
		HorizontalRule: ansi.StylePrimitive{
			Format: "────────────────────────────",
		},
		Task: ansi.StyleTask{
			Ticked:   "[x] ",
			Unticked: "[ ] ",
		},
	}
}

func quietMarkdownStyle() ansi.StyleConfig {
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{BlockPrefix: "", BlockSuffix: ""},
			Margin:         uintPtr(0),
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Prefix: "# "},
		},
		List: ansi.StyleList{
			LevelIndent: 2,
			StyleBlock: ansi.StyleBlock{
				IndentToken: stringPtr(" "),
			},
		},
		Item: ansi.StylePrimitive{
			BlockPrefix: "- ",
		},
		Enumeration: ansi.StylePrimitive{
			BlockPrefix: ". ",
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Prefix: "> "},
		},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Prefix: "`", Suffix: "`"},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Prefix: "  "},
				Margin:         uintPtr(1),
			},
		},
		Link: ansi.StylePrimitive{
			Format: "{{.text}}",
		},
		Task: ansi.StyleTask{
			Ticked:   "[x] ",
			Unticked: "[ ] ",
		},
	}
}
