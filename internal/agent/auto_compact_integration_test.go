package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usewhale/whale/internal/core"
)

// TestAutoCompactHugeHistoryIntegration drives the REAL auto-compaction
// pipeline (trigger -> split -> summarize -> rewrite -> event) over an
// in-memory history that deterministically crosses the production trigger —
// window 1M, threshold 0.85 (the exact constants the fixed ACP entrypoint
// passes to WithAutoCompact). Fixture-free: the history is generated, so this
// runs anywhere with no testdata dependency.
//
// It pins the RC1 regression at the real constants: a session whose history
// estimate exceeds 0.85 * 1M must emit ContextCompacted (auto) and rewrite to
// a summary + tail that fits under the trigger, with no forced summary.
func TestAutoCompactHugeHistoryIntegration(t *testing.T) {
	const (
		// Window/threshold from the fixed ACP wiring (defaults + main.go).
		window    = 1_000_000
		threshold = 0.85
		trigger   = 850_000

		seedMsgs = 400
		perMsg   = 10_000 // ~2,500 tokens each (4 ASCII runes/token) => ~1.0M total
	)

	store := NewInMemoryStore()
	const sid = "s-huge"
	for i := 0; i < seedMsgs; i++ {
		if _, err := store.Create(context.Background(), Message{
			SessionID: sid,
			Role:      RoleUser,
			Text:      fmt.Sprintf("seed-%03d %s", i, strings.Repeat("x", perMsg)),
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	prov := &autoCompactProvider{}
	a := NewAgentWithRegistry(prov, store, core.NewToolRegistry(nil),
		WithAutoCompact(true, threshold, window))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	events, err := a.RunStream(ctx, sid, "continue")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var compacted *CompactInfo
	var finalMsg *Message
	var forced bool
	for ev := range events {
		switch ev.Type {
		case AgentEventTypeContextCompacted:
			if ev.Compact != nil {
				c := *ev.Compact
				compacted = &c
			}
		case AgentEventTypeForcedSummaryStarted:
			forced = true
		case AgentEventTypeDone:
			finalMsg = ev.Message
		case AgentEventTypeError:
			t.Fatalf("agent error: %v", ev.Err)
		}
	}

	if compacted == nil || !compacted.Compacted {
		t.Fatal("expected ContextCompacted (Compacted=true)")
	}
	if !compacted.Auto {
		t.Fatal("expected auto compaction (not manual)")
	}
	if compacted.MessagesBefore != seedMsgs+1 { // seed + the new "continue" input
		t.Fatalf("MessagesBefore = %d, want %d", compacted.MessagesBefore, seedMsgs+1)
	}
	if compacted.BeforeEstimate <= trigger {
		t.Fatalf("BeforeEstimate %d must exceed the 0.85x1M trigger %d", compacted.BeforeEstimate, trigger)
	}
	if compacted.AfterEstimate >= compacted.BeforeEstimate {
		t.Fatalf("AfterEstimate %d must be below BeforeEstimate %d", compacted.AfterEstimate, compacted.BeforeEstimate)
	}
	if compacted.AfterEstimate >= trigger {
		t.Fatalf("AfterEstimate %d must fit under the trigger after compaction", compacted.AfterEstimate)
	}
	if forced {
		t.Fatal("unexpected forced summary: auto-compaction should have prevented it")
	}

	// The summarize request must have reached the provider with the real
	// compaction prompt.
	if len(prov.histories) == 0 {
		t.Fatal("provider never called")
	}
	first := prov.histories[0]
	if len(first) == 0 || !strings.Contains(first[len(first)-1].Text, "Summarize the conversation") {
		t.Fatalf("expected summarize request first, got %d histories, last msg: %+v", len(prov.histories), first[len(first)-1:])
	}

	// Session must be rewritten to summary-first, and the turn must complete
	// with a normal end_turn answer.
	msgs, err := store.List(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 || msgs[0].Role != RoleUser || msgs[0].FinishReason != FinishReasonEndTurn || strings.TrimSpace(msgs[0].Text) != "compact summary" {
		t.Fatalf("expected compact summary as the first rewritten message, got %+v", msgs[0])
	}
	if finalMsg == nil || strings.TrimSpace(finalMsg.Text) != "ok" {
		t.Fatalf("expected final assistant 'ok', got %+v", finalMsg)
	}

	t.Logf("auto-compacted synthetic huge history: %d msgs (%d tokens) -> %d msgs (%d tokens)",
		compacted.MessagesBefore, compacted.BeforeEstimate, compacted.MessagesAfter, compacted.AfterEstimate)
}
