package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usewhale/whale/internal/runtime/protocol"
)

func altPKey() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p"), Alt: true}
}

func TestAltPCyclesPermissionModes(t *testing.T) {
	m, intents := newModelWithDispatchSpy()

	// ask -> auto-review
	m, _ = updateTestModel(t, m, altPKey())
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSetAutoReview || (*intents)[0].AutoReview != true {
		t.Fatalf("expected auto_review intent from ask, got %+v", *intents)
	}
	if !m.autoReviewEnabled || m.autoAccept {
		t.Fatalf("expected optimistic auto-review state, got autoReview=%v autoAccept=%v", m.autoReviewEnabled, m.autoAccept)
	}
	if m.busy {
		t.Fatal("permission cycle should not start working state")
	}

	// auto-review -> auto-accept
	m, _ = updateTestModel(t, m, altPKey())
	if len(*intents) != 2 || (*intents)[1].Kind != protocol.IntentSetApprovalMode || (*intents)[1].ApprovalMode != "auto_accept" {
		t.Fatalf("expected auto_accept intent from auto-review, got %+v", *intents)
	}
	if !m.autoAccept || m.autoReviewEnabled {
		t.Fatalf("expected optimistic auto-accept state, got autoReview=%v autoAccept=%v", m.autoReviewEnabled, m.autoAccept)
	}

	// auto-accept -> ask
	m, _ = updateTestModel(t, m, altPKey())
	if len(*intents) != 3 || (*intents)[2].Kind != protocol.IntentSetApprovalMode || (*intents)[2].ApprovalMode != "ask" {
		t.Fatalf("expected ask intent from auto-accept, got %+v", *intents)
	}
	if m.autoAccept || m.autoReviewEnabled {
		t.Fatalf("expected optimistic ask state, got autoReview=%v autoAccept=%v", m.autoReviewEnabled, m.autoAccept)
	}

	// ask -> auto-review again (wraps)
	m, _ = updateTestModel(t, m, altPKey())
	if len(*intents) != 4 || (*intents)[3].Kind != protocol.IntentSetAutoReview {
		t.Fatalf("expected cycle to wrap back to auto_review, got %+v", *intents)
	}
}

func TestAltPCyclesWhileBusy(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.busy = true
	m.busySince = time.Now().Add(-5 * time.Minute)

	m, _ = updateTestModel(t, m, altPKey())

	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSetAutoReview {
		t.Fatalf("expected permission cycle to work while busy, got %+v", *intents)
	}
}

func TestAltPBlockedWhileSlashSuggestionsOpen(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/per")
	m.updateSlashMatches()
	if !m.hasSlashSuggestions() {
		t.Fatalf("expected slash suggestions open, got %+v", m.slash.matches)
	}

	m, _ = updateTestModel(t, m, altPKey())

	if len(*intents) != 0 {
		t.Fatalf("suggestion panel should swallow Alt+P, got %+v", *intents)
	}
}

func TestAltPWaitsForPendingLocalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.localSubmitPending = 1
	m.status = "command pending"

	m, _ = updateTestModel(t, m, altPKey())

	if len(*intents) != 0 {
		t.Fatalf("pending local submit should block permission cycle, got %+v", *intents)
	}
	if m.localSubmitPending != 1 {
		t.Fatalf("expected pending local submit to remain, got %d", m.localSubmitPending)
	}
	if m.status != "wait for command to finish" {
		t.Fatalf("expected wait status while local submit is pending, got %q", m.status)
	}
}

func TestAltPCycleRendersFooterIndicator(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 100
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale"

	m, _ = updateTestModel(t, m, altPKey())
	view := m.View()
	if !strings.Contains(view, "auto-review") {
		t.Fatalf("expected auto-review footer indicator after first Alt+P:\n%s", view)
	}

	m, _ = updateTestModel(t, m, altPKey())
	view = m.View()
	if !strings.Contains(view, "auto-accept on") {
		t.Fatalf("expected auto-accept footer indicator after second Alt+P:\n%s", view)
	}

	m, _ = updateTestModel(t, m, altPKey())
	view = m.View()
	if !strings.Contains(view, "permission: ask") {
		t.Fatalf("expected ask permission footer indicator after third Alt+P:\n%s", view)
	}
}
