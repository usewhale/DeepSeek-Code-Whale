package render

import (
	"testing"
)

func TestIsMarkdownAutolinkTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   bool
	}{
		// Valid http/https/mailto
		{"http url", "http://example.com", true},
		{"https url", "https://example.com/path?q=1", true},
		{"mailto", "mailto:user@example.com", true},
		// Case-insensitive
		{"uppercase HTTP", "HTTP://EXAMPLE.COM", true},
		{"uppercase HTTPS", "HTTPS://example.com", true},
		{"mixed case Http", "Http://example.com", true},
		{"mixed case MailTo", "MailTo:user@example.com", true},
		// Invalid
		{"empty string", "", false},
		{"no protocol", "example.com", false},
		{"wrong protocol ftp", "ftp://example.com", false},
		{"wrong protocol file", "file:///tmp/foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMarkdownAutolinkTarget(tt.target); got != tt.want {
				t.Fatalf("isMarkdownAutolinkTarget(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestEscapeMarkdownLiteral(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no special chars", "https://example.com", `https://example\.com`},
		{"asterisk in URL", "https://example.com/a*b*c", `https://example\.com/a\*b\*c`},
		{"underscore in URL", "https://example.com/a_b", `https://example\.com/a\_b`},
		{"parentheses in URL", "https://example.com/a(b)c", `https://example\.com/a\(b\)c`},
		{"brackets in URL", "https://example.com/a[b]c", `https://example\.com/a\[b\]c`},
		{"backtick in URL", "https://example.com/a`b", "https://example\\.com/a\\`b"},
		{"hash in URL", "https://example.com/a#b", `https://example\.com/a\#b`},
		{"multiple special chars", "https://a*b(c).d", `https://a\*b\(c\)\.d`},
		{"mailto no specials but dot", "mailto:user@example.com", `mailto:user@example\.com`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeMarkdownLiteral(tt.input); got != tt.want {
				t.Fatalf("escapeMarkdownLiteral(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseMarkdownAutolink(t *testing.T) {
	strip := stripAutolinkBracketsOnly
	escape := escapeAutolinksForRenderer

	tests := []struct {
		name     string
		input    string
		start    int
		escaping autolinkEscaping
		wantRepl string
		wantNext int
		wantOK   bool
	}{
		{
			name:     "strip brackets only - simple URL",
			input:    "see <https://example.com> more",
			start:    4,
			escaping: strip,
			wantRepl: "https://example.com",
			wantNext: 25,
			wantOK:   true,
		},
		{
			name:     "escape for renderer - simple URL",
			input:    "<https://example.com>",
			start:    0,
			escaping: escape,
			wantRepl: `https://example\.com`,
			wantNext: 21,
			wantOK:   true,
		},
		{
			name:     "escape for renderer - URL with asterisk",
			input:    "<https://example.com/a*b>",
			start:    0,
			escaping: escape,
			wantRepl: `https://example\.com/a\*b`,
			wantNext: 25,
			wantOK:   true,
		},
		{
			name:     "strip - mailto",
			input:    "<mailto:user@example.com>",
			start:    0,
			escaping: strip,
			wantRepl: "mailto:user@example.com",
			wantNext: 25,
			wantOK:   true,
		},
		{
			name:     "empty brackets",
			input:    "<>",
			start:    0,
			escaping: strip,
			wantRepl: "",
			wantNext: 0,
			wantOK:   false,
		},
		{
			name:     "no closing bracket",
			input:    "<https://example.com",
			start:    0,
			escaping: strip,
			wantRepl: "",
			wantNext: 0,
			wantOK:   false,
		},
		{
			name:     "space inside brackets",
			input:    "<https://example .com>",
			start:    0,
			escaping: strip,
			wantRepl: "",
			wantNext: 0,
			wantOK:   false,
		},
		{
			name:     "HTML tag is not autolink",
			input:    `<p align="center">`,
			start:    0,
			escaping: escape,
			wantRepl: "",
			wantNext: 0,
			wantOK:   false,
		},
		{
			name:     "not at angle bracket",
			input:    "hello",
			start:    0,
			escaping: strip,
			wantRepl: "",
			wantNext: 0,
			wantOK:   false,
		},
		{
			name:     "multiple autolinks - first",
			input:    "<https://a.com> and <https://b.com>",
			start:    0,
			escaping: strip,
			wantRepl: "https://a.com",
			wantNext: 15,
			wantOK:   true,
		},
		{
			name:     "multiple autolinks - second",
			input:    "<https://a.com> and <https://b.com>",
			start:    20,
			escaping: strip,
			wantRepl: "https://b.com",
			wantNext: 35,
			wantOK:   true,
		},
		{
			name:     "autolink inside blockquote text",
			input:    "> see <https://x.com> now",
			start:    6,
			escaping: strip,
			wantRepl: "https://x.com",
			wantNext: 21,
			wantOK:   true,
		},
		{
			name:     "autolink after text with punctuation",
			input:    "URL: <https://example.com>.",
			start:    5,
			escaping: strip,
			wantRepl: "https://example.com",
			wantNext: 26,
			wantOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRepl, gotNext, gotOK := parseMarkdownAutolink(tt.input, tt.start, tt.escaping)
			if gotOK != tt.wantOK {
				t.Fatalf("parseMarkdownAutolink(%q, %d, %v) ok = %v, want %v", tt.input, tt.start, tt.escaping, gotOK, tt.wantOK)
			}
			if gotRepl != tt.wantRepl {
				t.Fatalf("parseMarkdownAutolink(%q, %d, %v) repl = %q, want %q", tt.input, tt.start, tt.escaping, gotRepl, tt.wantRepl)
			}
			if gotNext != tt.wantNext {
				t.Fatalf("parseMarkdownAutolink(%q, %d, %v) next = %d, want %d", tt.input, tt.start, tt.escaping, gotNext, tt.wantNext)
			}
		})
	}
}
