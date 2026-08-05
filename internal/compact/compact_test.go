package compact

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/usewhale/whale/internal/core"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("abcd"); got != 1 {
		t.Fatalf("expected 1 token, got %d", got)
	}
	if got := EstimateTokens("你好"); got != 2 {
		t.Fatalf("expected 2 tokens, got %d", got)
	}
	if got := EstimateTokens("   "); got != 0 {
		t.Fatalf("expected blank text to estimate to 0, got %d", got)
	}
}

func TestEstimateMessagesTokensIncludesToolPayloads(t *testing.T) {
	msgs := []core.Message{{
		Role: core.RoleAssistant,
		Text: strings.Repeat("a", 8),
		ToolCalls: []core.ToolCall{{
			Name:  "write",
			Input: strings.Repeat("b", 8),
		}},
	}, {
		Role: core.RoleTool,
		ToolResults: []core.ToolResult{{
			Name:      "write",
			ModelText: strings.Repeat("c", 8),
		}},
	}}
	if got := EstimateMessagesTokens(msgs); got == 0 {
		t.Fatal("expected non-zero estimate")
	}
}

func TestToolResultReplayContentCompactsLargeOutput(t *testing.T) {
	raw := strings.Repeat("a", 4000) + strings.Repeat("middle", 2000) + strings.Repeat("z", 4000)
	got := ToolResultReplayContent(raw)
	if got == raw {
		t.Fatal("expected large tool result to be compacted")
	}
	if !strings.Contains(got, "[tool result compacted for model replay]") {
		t.Fatalf("missing compaction marker: %q", got[:min(len(got), 80)])
	}
	if !strings.Contains(got, strings.Repeat("a", 100)) || !strings.Contains(got, strings.Repeat("z", 100)) {
		t.Fatal("expected compacted replay to retain head and tail")
	}
}

func TestEstimateMessagesTokensUsesMessagePartsPlainText(t *testing.T) {
	msg := core.UserMessageFromParts("s1", []core.MessagePart{
		{Type: core.MessagePartText, Text: strings.Repeat("a", 8)},
		{Type: core.MessagePartAttachment, Attachment: &core.AttachmentRef{
			Kind:        core.AttachmentKindPDF,
			DisplayName: "paper.pdf",
		}},
	}, false)

	got := EstimateMessagesTokens([]core.Message{msg})
	if got == 0 {
		t.Fatal("expected non-zero estimate")
	}
	if got > EstimateTokens(msg.Text)+1 {
		t.Fatalf("estimate = %d unexpectedly exceeds plain text mirror", got)
	}
}

// TestEstimateMessagesTokensIsLowerBoundOnProviderInput locks the E5
// invariant: the estimator counts text + reasoning + tool-call names/inputs +
// tool-result modeltext, but NOT replayed reasoning/tool-result tokens, so it
// must never overestimate the provider's real input. If it did, compaction
// could trigger spuriously early on a healthy history.
func TestEstimateMessagesTokensIsLowerBoundOnProviderInput(t *testing.T) {
	replayed := ToolResultReplayContent(strings.Repeat("q", 9000) + strings.Repeat("tail", 4000))
	msgs := []core.Message{{
		Role:      core.RoleAssistant,
		Text:      strings.Repeat("a", 200),
		Reasoning: strings.Repeat("r", 3000),
		ToolCalls: []core.ToolCall{{
			Name:  "edit",
			Input: strings.Repeat("b", 500),
		}, {
			Name:  "write",
			Input: strings.Repeat("c", 700),
		}},
	}, {
		Role: core.RoleTool,
		ToolResults: []core.ToolResult{{
			Name:      "edit",
			ModelText: replayed, // replay-compacted tool result
		}, {
			Name:      "shell_run",
			ModelText: strings.Repeat("d", 400),
		}},
	}}

	estimate := EstimateMessagesTokens(msgs)
	if estimate <= 0 {
		t.Fatal("expected non-zero estimate")
	}

	// Every token costs at most 4 ASCII runes or 1 non-ASCII rune, so the
	// estimate is bounded above by the total runes it can see. If it ever
	// exceeded that, the estimator would be over-counting.
	ref := 0
	for _, m := range msgs {
		ref += utf8.RuneCountInString(core.MessagePlainText(m))
		ref += utf8.RuneCountInString(m.Reasoning)
		for _, tc := range m.ToolCalls {
			ref += utf8.RuneCountInString(tc.Name) + utf8.RuneCountInString(tc.Input)
		}
		for _, tr := range m.ToolResults {
			ref += utf8.RuneCountInString(tr.Name) + utf8.RuneCountInString(core.ToolResultModelText(tr))
		}
	}
	if estimate > ref {
		t.Fatalf("estimate %d exceeds visible rune count %d; estimator cannot be a lower bound", estimate, ref)
	}

	// The estimator sees the compacted replay, not the raw tool result, and
	// sees no replayed reasoning at all: the raw character volume (what the
	// provider actually re-reads) must exceed the estimate. This is exactly
	// the undercount property the live session showed (370K replayed
	// reasoning + 196K replayed tool-result tokens).
	raw := len(strings.Repeat("q", 9000)+strings.Repeat("tail", 4000)) + 400 + 3000
	if raw < estimate {
		t.Fatalf("raw replayed volume %d should exceed the compact estimate %d", raw, estimate)
	}
}
