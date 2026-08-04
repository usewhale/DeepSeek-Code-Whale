package tui

import (
	"bytes"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
	"strings"
	"testing"
	"time"
)

func TestWindowsUnbracketedPasteFallbackKeepsLinesInOnePrompt(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first enter to be deferred")
	}
	if len(*intents) != 0 {
		t.Fatalf("first pasted newline submitted early: %+v", *intents)
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste burst should stay buffered before flush, input=%q", got)
	}
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("buffer after second pasted line = %q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter submitted after paste detection: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.windowsPasteBuffer(); got != "line one\nline two\n" {
		t.Fatalf("trailing pasted newline buffer = %q", got)
	}
	if len(*intents) != 0 {
		t.Fatalf("trailing pasted newline submitted early: %+v", *intents)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != "line one\nline two\n" {
		t.Fatalf("input after paste flush = %q", got)
	}
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final enter after fallback paste to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	if len(*intents) != 0 {
		t.Fatalf("final enter submitted before defer elapsed: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit after paste quiet period, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "line one\nline two" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsUnbracketedLargeMultilinePasteBurstUsesPlaceholder(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	firstLine := strings.Repeat("x", 600)
	secondLine := strings.Repeat("y", 500)
	large := firstLine + "\n" + secondLine

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(firstLine)})
	if cmd == nil {
		t.Fatal("expected multi-rune pasted text to schedule a flush")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("first pasted line should stay buffered before flush, got input %q", got)
	}
	if got := m.windowsPasteBuffer(); got != firstLine {
		t.Fatalf("first pasted line should enter paste buffer, got %q", got)
	}
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected pasted newline to defer submit")
	}
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(secondLine)})
	if cmd == nil {
		t.Fatal("expected large paste burst to schedule a flush")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("large paste should stay buffered before flush, input=%q", got)
	}
	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != large {
		t.Fatalf("expanded large paste value length = %d, want %d", len([]rune(got)), len([]rune(large)))
	}
	if view := m.input.View(); !strings.Contains(view, "[Pasted Content 1101 chars]") {
		t.Fatalf("expected large paste placeholder in composer view, got %q", view)
	}
}
func TestWindowsPasteBufferSurvivesUnhandledMessagesBeforeFlush(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.setWindowsPasteBuffer("line one\nline two")
	m.windowsPaste.activeUntil = time.Now().Add(windowsPasteQuietDelay)
	m.windowsPaste.burstID = 7

	m, _ = updateTestModel(t, m, struct{}{})
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("unhandled message should not clear active paste buffer, got %q", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("unhandled message should not flush paste buffer early, got input %q", got)
	}

	m, _ = updateTestModel(t, m, windowsPasteBurstFlushMsg{id: 7})
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("early flush timer should keep paste buffer until quiet window, got %q", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("early flush timer should not flush paste buffer, got input %q", got)
	}

	m.windowsPaste.activeUntil = time.Now().Add(-time.Millisecond)
	m, _ = updateTestModel(t, m, windowsPasteBurstFlushMsg{id: 7})
	if got := m.input.Value(); got != "line one\nline two" {
		t.Fatalf("paste buffer should flush after quiet window, got %q", got)
	}
}
func TestWindowsPasteBurstKeepsSingleDebounceTimer(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one\n")})
	if cmd == nil {
		t.Fatal("expected first paste chunk to schedule debounce timer")
	}
	firstID := m.windowsPaste.burstID
	firstDeadline := m.windowsPaste.activeUntil
	if !m.windowsPaste.burstFlushScheduled {
		t.Fatal("expected paste flush timer to be marked scheduled")
	}

	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if cmd != nil {
		t.Fatal("expected pasted rune to reuse existing debounce timer")
	}
	if m.windowsPaste.burstID != firstID {
		t.Fatalf("expected debounce id to stay %d, got %d", firstID, m.windowsPaste.burstID)
	}
	if m.windowsPaste.activeUntil.Before(firstDeadline) {
		t.Fatalf("expected paste deadline not to move backward, old=%v new=%v", firstDeadline, m.windowsPaste.activeUntil)
	}

	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ine two")})
	if cmd != nil {
		t.Fatal("expected pasted continuation to reuse existing debounce timer")
	}
	if m.windowsPaste.burstID != firstID {
		t.Fatalf("expected debounce id to remain %d after continuation, got %d", firstID, m.windowsPaste.burstID)
	}

	m.windowsPaste.activeUntil = time.Now().Add(-time.Millisecond)
	m, _ = updateTestModel(t, m, windowsPasteBurstFlushMsg{id: firstID})
	if m.windowsPaste.burstFlushScheduled {
		t.Fatal("expected debounce timer mark to clear after flush")
	}
	if got := m.input.Value(); got != "line one\nline two" {
		t.Fatalf("expected paste to flush after quiet window, got %q", got)
	}
}
func TestWindowsPasteFallbackFastEnterSubmitsTypedCharacter(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("hell")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if got := m.input.Value(); got != "hello" {
		t.Fatalf("expected typed character to render immediately, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("ordinary typed character should not enter paste buffer, got %q", got)
	}

	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected fast enter after typed character to defer submit")
	}
	if got := m.input.Value(); got != "hello" {
		t.Fatalf("fast enter should keep typed prompt intact, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("fast enter should not create paste buffer, got %q", got)
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "hello" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsPasteFallbackEnterThenHumanTypingSubmitsAndKeepsRune(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("hello")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter to defer submit")
	}
	if !m.windowsPaste.pendingEnter {
		t.Fatal("expected pendingEnter to be armed")
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !m.windowsPaste.pendingEnter {
		t.Fatal("first typed rune should wait briefly before resolving the deferred enter")
	}
	if got := m.windowsPaste.pendingEnterTail; got != "x" {
		t.Fatalf("expected typed rune to wait in pending tail, got %q", got)
	}
	if len(*intents) != 0 {
		t.Fatalf("pending tail should not submit until the short continuation window closes, got %+v", *intents)
	}

	tailID := m.windowsPaste.pendingEnterTailID
	m, _ = updateTestModel(t, m, windowsPendingEnterTailMsg{id: tailID})

	if m.windowsPaste.pendingEnter {
		t.Fatal("typing without paste continuation should resolve the deferred enter")
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("typed rune should not enter paste buffer, got %q", got)
	}
	if got := m.input.Value(); got != "x" {
		t.Fatalf("expected typed rune to start a fresh composer, got %q", got)
	}
	if len(*intents) != 1 {
		t.Fatalf("expected the deferred prompt to submit once, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "hello" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsPasteFallbackEnterThenRapidRunesBecomeContinuation(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("hello")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter to defer submit")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if got := m.windowsPasteBuffer(); got != "hello\nwo" {
		t.Fatalf("rapid runes should upgrade to paste continuation, got buffer %q", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste continuation should move composer text to buffer, got input %q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter should not submit after paste continuation, got %+v", *intents)
	}
}
func TestWindowsPasteFallbackLFPastePreservesTailBetweenNewlines(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})

	if got := m.windowsPaste.pendingEnterTail; got != "" {
		t.Fatalf("expected tail to be folded into burst before second newline, got %q", got)
	}
	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != "a\nb\nc" {
		t.Fatalf("expected LF-only paste to preserve single-char line, got %q", got)
	}
}
func TestWindowsPasteFallbackDeferredSubmitPreservesTailRune(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("hello")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter to defer submit")
	}
	submitID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := m.windowsPaste.pendingEnterTail; got != "x" {
		t.Fatalf("expected tail to hold rune, got %q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})

	if m.windowsPaste.pendingEnter {
		t.Fatal("expected deferred submit tick to resolve pending enter")
	}
	if got := m.input.Value(); got != "x" {
		t.Fatalf("expected held tail rune to land in fresh composer when submit tick wins the race, got %q", got)
	}
	if len(*intents) != 1 || (*intents)[0].Input != "hello" {
		t.Fatalf("expected single submit of original prompt, got %+v", *intents)
	}
}

// Issue #61(1) review: non-rune editing keys (Enter, Tab, arrows, …)
// must segment paste-cadence detection. Otherwise "ab" + Enter + "c"
// arriving within 60ms of each other would treat "c" as the third paste
// chunk and buffer "ab\nc" instead of submitting "ab" and starting a
// fresh prompt with "c".
func TestWindowsPasteFallbackEnterSegmentsPasteCadence(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	clock := newFakeClock()
	m.windowsPaste.nowFunc = clock.now

	clock.advance(50 * time.Millisecond)
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	clock.advance(50 * time.Millisecond)
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})

	// Enter — defers submit on windows fallback. classifier must reset.
	clock.advance(30 * time.Millisecond)
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.windowsPaste.pendingEnter {
		t.Fatal("expected enter to enter deferred-submit state")
	}

	// Quick keystroke after Enter MUST stay typed, not escalate.
	clock.advance(30 * time.Millisecond)
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.hasWindowsPasteBuffer() {
		t.Fatalf("post-Enter keystroke escalated into burst buffer: %q", m.windowsPasteBuffer())
	}

	// Resolve the deferred Enter as a real submit; "ab" should ship as
	// the prompt, "c" stays in the composer for the next turn.
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 || (*intents)[0].Input != "ab" {
		t.Fatalf("expected single submit of %q, got %+v", "ab", *intents)
	}
	if got := m.input.Value(); got != "c" {
		t.Fatalf("post-submit composer should hold %q, got %q", "c", got)
	}
}

// Issue #61(1): a paste delivered as many "typed-looking" single-rune
// chunks (no whitespace, no newlines, each < 16 runes) must be caught by
// the cadence escalator and routed through the paste burst path instead
// of being inserted into the textarea one rune at a time.
func TestWindowsPasteFallbackEscalatesFastSingleRuneStream(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	clock := newFakeClock()
	m.windowsPaste.nowFunc = clock.now

	const streamed = "abcdefghijklmnop"
	for _, r := range streamed {
		clock.advance(2 * time.Millisecond) // sub-typing cadence
		m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.hasWindowsPasteBuffer() {
		t.Fatalf("expected fast single-rune stream to populate paste buffer; composer=%q buffer=%q",
			m.input.Value(), m.windowsPasteBuffer())
	}
	combined := m.input.Value() + m.windowsPasteBuffer()
	if combined != streamed {
		t.Fatalf("expected combined composer+buffer to equal stream, got %q", combined)
	}
	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != streamed {
		t.Fatalf("after flush composer should hold full stream, got %q", got)
	}
}

// Issue #61(1) review: when cadence-escalation triggers mid-composer
// (cursor not at end), the early typed chunks have already landed at the
// cursor; the rest must flow through HandlePaste at that same cursor
// position so character order is preserved. Pasting "XYZ" into "a|c"
// must yield "aXYZc", not "aXYcZ".
func TestWindowsPasteFallbackEscalatedPastePreservesCursorPosition(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	clock := newFakeClock()
	m.windowsPaste.nowFunc = clock.now

	// Set up "a|c" — composer "ac" with cursor between the two runes.
	m.input.SetValue("ac")
	m.input.SetCursorEnd()
	// Move cursor one step left so it sits between 'a' and 'c'.
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyLeft})

	// Now simulate a fast paste of "XYZ" arriving as 3 single-rune chunks.
	for _, r := range "XYZ" {
		clock.advance(2 * time.Millisecond)
		m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.hasWindowsPasteBuffer() {
		m = flushWindowsPasteBurstForTest(t, m)
	}
	if got := m.input.Value(); got != "aXYZc" {
		t.Fatalf("mid-cursor escalated paste corrupted order: got %q, want %q", got, "aXYZc")
	}
}
func TestWindowsPasteFallbackFastEnterSubmitsShortBurst(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	clock := newFakeClock()
	m.windowsPaste.nowFunc = clock.now

	clock.advance(100 * time.Millisecond)
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	clock.advance(100 * time.Millisecond)
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if got := m.input.Value(); got != "hi" {
		t.Fatalf("expected short typed burst to render immediately, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("ordinary typed burst should not enter paste buffer, got %q", got)
	}

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected fast enter after short burst to defer submit")
	}
	if got := m.input.Value(); got != "hi" {
		t.Fatalf("expected fast enter to keep prompt intact, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("fast enter should not create paste buffer, got %q", got)
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "hi" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsPasteFallbackFastEnterSubmitsLongTypedPrompt(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	clock := newFakeClock()
	m.windowsPaste.nowFunc = clock.now
	line := "Reply later"

	for _, r := range line {
		clock.advance(100 * time.Millisecond)
		m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.input.Value(); got != line {
		t.Fatalf("expected typed prompt to render immediately, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("typed prompt should not enter paste buffer, got %q", got)
	}

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter after typed prompt to defer submit")
	}
	if got := m.input.Value(); got != line {
		t.Fatalf("enter should preserve typed prompt before deferred submit, got %q", got)
	}
	if len(*intents) != 0 {
		t.Fatalf("deferred enter submitted early: %+v", *intents)
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != line {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsPasteFallbackSingleLinePasteThenEnterSubmits(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("fix bug")})
	if cmd == nil {
		t.Fatal("expected single-line pasted burst to schedule flush")
	}
	if got := m.windowsPasteBuffer(); got != "fix bug" {
		t.Fatalf("expected single-line paste to stay buffered, got %q", got)
	}

	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter after single-line paste to defer submit")
	}
	if got := m.input.Value(); got != "fix bug" {
		t.Fatalf("expected single-line paste to flush before deferred submit, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("expected paste buffer to flush before deferred submit, got %q", got)
	}
	submitID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected single-line paste to submit after deferred enter, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "fix bug" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsPasteFallbackMultiRunePastedLineUpgradesDeferredEnterOnContinuation(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one")})
	if cmd == nil {
		t.Fatal("expected first pasted line to schedule burst flush")
	}
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected pasted line enter to defer submit")
	}
	if got := m.input.Value(); got != "line one" {
		t.Fatalf("expected first pasted line to flush while enter is deferred, got input %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("expected buffer to flush while enter is deferred, got %q", got)
	}
	if !m.windowsPaste.pendingEnter {
		t.Fatal("expected pasted line enter to arm deferred submit")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("expected continued paste buffer, got %q", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected continued paste to move flushed prefix back to buffer, got input %q", got)
	}
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter submitted after paste continuation: %+v", *intents)
	}
	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != "line one\nline two" {
		t.Fatalf("expected combined paste after flush, got %q", got)
	}
}
func TestWindowsUnbracketedPasteFallbackNormalizesCRLF(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("foo")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected CR half of pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if got := m.windowsPasteBuffer(); got != "foo\n" {
		t.Fatalf("buffer after CRLF pasted newline = %q", got)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bar")})
	if got := m.windowsPasteBuffer(); got != "foo\nbar" {
		t.Fatalf("buffer after CRLF pasted text = %q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter submitted after CRLF paste detection: %+v", *intents)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != "foo\nbar" {
		t.Fatalf("input after CRLF paste flush = %q", got)
	}
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final submit enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit after CRLF paste, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "foo\nbar" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsUnbracketedPasteFallbackNormalizesCRLFBlankLine(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("a")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first CR half of pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if got := m.windowsPasteBuffer(); got != "a\n\nb" {
		t.Fatalf("buffer after CRLF blank line paste = %q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter submitted after CRLF blank-line paste detection: %+v", *intents)
	}
	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != "a\n\nb" {
		t.Fatalf("input after CRLF blank-line paste flush = %q", got)
	}
}
func TestWindowsUnbracketedPasteFallbackAllowsSubsequentPasteBlocks(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("first")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first paste newline to be deferred")
	}
	firstDeferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("block")})
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: firstDeferredID})
	if len(*intents) != 0 {
		t.Fatalf("first paste block submitted early: %+v", *intents)
	}
	m = flushWindowsPasteBurstForTest(t, m)

	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected second paste newline to be deferred")
	}
	secondDeferredID := m.windowsPaste.pendingEnterID
	if len(*intents) != 0 {
		t.Fatalf("second paste newline submitted early: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")})
	if got := m.windowsPasteBuffer(); got != "first\nblock\nsecond" {
		t.Fatalf("buffer after subsequent pasted block = %q", got)
	}
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: secondDeferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale second deferred enter submitted after paste detection: %+v", *intents)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final submit enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit after both paste blocks, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "first\nblock\nsecond" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsUnbracketedPasteFallbackPreservesTabIndentAsSpaces(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("foo")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.windowsPasteBuffer(); got != "foo\n    " {
		t.Fatalf("buffer after pasted tab indentation = %q", got)
	}
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter submitted after pasted tab: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bar")})
	if got := m.windowsPasteBuffer(); got != "foo\n    bar" {
		t.Fatalf("buffer after tab-indented pasted text = %q", got)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final submit enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit after tab-indented paste, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "foo\n    bar" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsUnbracketedPasteFallbackPreservesTabBeforeFirstNewline(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "    " {
		t.Fatalf("input after leading pasted tab = %q", got)
	}
	if !m.windowsPaste.activeUntil.IsZero() {
		t.Fatalf("leading pasted tab should not start quiet window, activeUntil=%v", m.windowsPaste.activeUntil)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("foo")})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "    foo" {
		t.Fatalf("input after tab-indented first pasted line = %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("buffer after tab-indented first pasted line = %q", got)
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bar")})
	if got := m.windowsPasteBuffer(); got != "    foo\nbar" {
		t.Fatalf("buffer after tab-indented first pasted text = %q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter submitted after pasted leading tab: %+v", *intents)
	}
	m = flushWindowsPasteBurstForTest(t, m)
	if got := m.input.Value(); got != "    foo\nbar" {
		t.Fatalf("input after tab-indented first pasted line flush = %q", got)
	}
}
func TestWindowsUnbracketedPasteFallbackPreservesSingleLineTabBeforeSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("a")

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if got := m.input.Value(); got != "a    b" {
		t.Fatalf("input after single-line pasted tab = %q", got)
	}
	if !m.windowsPaste.activeUntil.IsZero() {
		t.Fatalf("single-line pasted tab should not start quiet window, activeUntil=%v", m.windowsPaste.activeUntil)
	}

	m, cmd := updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter after single-line pasted tab to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	if got := m.input.Value(); got != "a    b" {
		t.Fatalf("enter after single-line pasted tab should not insert newline, got %q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit after single-line pasted tab, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "a    b" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsUnbracketedPasteFallbackPreservesBlankLines(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("a")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first enter to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.windowsPasteBuffer(); got != "a\n\n" {
		t.Fatalf("buffer after blank pasted line = %q", got)
	}
	if len(*intents) != 0 {
		t.Fatalf("blank pasted line submitted early: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter submitted after blank-line paste detection: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if got := m.windowsPasteBuffer(); got != "a\n\nb" {
		t.Fatalf("buffer after pasted text following blank line = %q", got)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final enter after fallback paste to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	if len(*intents) != 0 {
		t.Fatalf("final enter submitted before defer elapsed: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit after paste quiet period, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "a\n\nb" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsUnbracketedPasteFallbackSteersWhileBusy(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first busy enter to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("first pasted newline queued early: %+v", m.queuedPrompts)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("buffer after second pasted line while busy = %q", got)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("pasted multiline prompt queued before final enter: %+v", m.queuedPrompts)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("stale deferred enter queued after paste detection: %+v", m.queuedPrompts)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final enter after fallback paste to be deferred")
	}
	queueID := m.windowsPaste.pendingEnterID
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("final enter queued before defer elapsed: %+v", m.queuedPrompts)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: queueID})
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "line one\nline two" {
		t.Fatalf("expected queued multiline prompt, got %+v", m.queuedPrompts)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no submitted intent while busy, got %+v", *intents)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared after queueing, got %q", got)
	}
}
func TestWindowsPasteFallbackCtrlCResetsQuietWindow(t *testing.T) {
	// Ctrl+C is the canonical clear-with-state-reset path after PR 2 moved
	// Ctrl+U to readline kill-to-line-start. handleGlobalKey clears the
	// composer and resets the Windows paste fallback state in one go.
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("old")
	m.setWindowsPasteBuffer("pending paste")
	m.windowsPaste.activeUntil = time.Now().Add(windowsPasteQuietDelay)

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected Ctrl+C to clear composer, got %q", got)
	}
	if m.windowsPaste.pendingEnter || m.windowsPasteBuffer() != "" || !m.windowsPaste.activeUntil.IsZero() {
		t.Fatalf("expected Ctrl+C to reset paste fallback state, got %+v", m.windowsPaste)
	}

	m = typeRunesForTest(t, m, "replacement")
	m, cmd := updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected replacement prompt enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	if got := m.input.Value(); got != "replacement" {
		t.Fatalf("replacement enter should submit, not insert a pasted newline; input=%q", got)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one replacement submit, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "replacement" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsPasteFallbackBackspaceClearsEmptyComposerState(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("x")
	m.windowsPaste.pendingEnterID = 7
	m.windowsPaste.pendingEnter = true
	m.windowsPaste.pendingEnterBusy = true
	m.windowsPaste.pendingEnterStop = true
	m.windowsPaste.activeUntil = time.Now().Add(windowsPasteQuietDelay)
	m.windowsPaste.busyInput = true
	m.windowsPaste.busyInputStop = true
	m.windowsPaste.bracketedThisInput = true

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected Backspace to clear composer, got %q", got)
	}
	if m.windowsPaste.pendingEnter || m.windowsPaste.pendingEnterBusy || m.windowsPaste.pendingEnterStop ||
		!m.windowsPaste.activeUntil.IsZero() || m.windowsPaste.busyInput || m.windowsPaste.busyInputStop ||
		m.windowsPaste.bracketedThisInput {
		t.Fatalf("expected empty composer to reset paste fallback state, got %+v", m.windowsPaste)
	}
}
func TestWindowsDeferredBusyEnterSteersSingleLine(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.input.SetValue("follow up while working")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	deferredID := m.windowsPaste.pendingEnterID
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("enter queued before defer elapsed: %+v", m.queuedPrompts)
	}

	next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
	m = next.(model)
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "follow up while working" {
		t.Fatalf("expected queued prompt, got %+v", m.queuedPrompts)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no submitted intent while busy, got %+v", *intents)
	}
}
func TestWindowsDeferredBusyEnterPreservesBusySlashClassification(t *testing.T) {
	t.Run("read only local command executes", func(t *testing.T) {
		m, intents := newModelWithDispatchSpy()
		m.windowsPaste.enabled = true
		m.busy = true
		m.status = "running"
		m.input.SetValue("/status ")

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(model)
		deferredID := m.windowsPaste.pendingEnterID
		if len(*intents) != 0 {
			t.Fatalf("busy slash should wait for deferred enter tick, got %+v", *intents)
		}

		next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
		m = next.(model)
		if len(*intents) != 1 {
			t.Fatalf("expected immediate local dispatch after deferred tick, got %+v", *intents)
		}
		if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/status" {
			t.Fatalf("unexpected dispatched intent: %+v", got)
		}
		if len(m.queuedPrompts) != 0 {
			t.Fatalf("expected no queued prompt for busy local command, got %+v", m.queuedPrompts)
		}
		if got := m.input.Value(); got != "" {
			t.Fatalf("expected input cleared after busy local command, got %q", got)
		}
	})

	t.Run("pending local submit does not block busy read only local command", func(t *testing.T) {
		m, intents := newModelWithDispatchSpy()
		m.windowsPaste.enabled = true
		m.busy = true
		m.localSubmitPending = 1
		m.status = "running"
		m.input.SetValue("/status")

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(model)
		if len(*intents) != 1 {
			t.Fatalf("expected immediate busy local dispatch, got %+v", *intents)
		}
		if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/status" {
			t.Fatalf("unexpected dispatched intent: %+v", got)
		}
		if m.windowsPaste.pendingEnter {
			t.Fatal("busy local command should not arm Windows deferred enter when local submit is pending")
		}
		if m.localSubmitPending != 2 {
			t.Fatalf("expected busy local command to keep existing pending submit and add one, got %d", m.localSubmitPending)
		}
	})

	t.Run("turn starting slash command is blocked", func(t *testing.T) {
		m, intents := newModelWithDispatchSpy()
		m.windowsPaste.enabled = true
		m.busy = true
		m.status = "running"
		m.input.SetValue("/ask inspect this")

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(model)
		deferredID := m.windowsPaste.pendingEnterID

		next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
		m = next.(model)
		if len(*intents) != 0 {
			t.Fatalf("turn-starting slash should be blocked while busy, got %+v", *intents)
		}
		if len(m.queuedPrompts) != 0 {
			t.Fatalf("turn-starting slash should not queue while busy, got %+v", m.queuedPrompts)
		}
		if got := m.input.Value(); got != "/ask inspect this" {
			t.Fatalf("expected blocked slash command to remain editable, got %q", got)
		}
		if m.status != "/ask disabled while working" {
			t.Fatalf("expected disabled status, got %q", m.status)
		}
	})

	t.Run("turn starting slash submits if turn finishes before tick", func(t *testing.T) {
		m, intents := newModelWithDispatchSpy()
		m.windowsPaste.enabled = true
		m.busy = true
		m.status = "running"
		m.input.SetValue("/ask inspect this")

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(model)
		deferredID := m.windowsPaste.pendingEnterID

		next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
		m = next.(model)
		if m.busy {
			t.Fatal("expected turn done to clear busy before deferred tick")
		}

		next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
		m = next.(model)
		if len(*intents) != 1 {
			t.Fatalf("expected slash command to submit after turn finished, got %+v", *intents)
		}
		if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "/ask inspect this" {
			t.Fatalf("unexpected dispatched intent: %+v", got)
		}
		if got := m.input.Value(); got != "" {
			t.Fatalf("expected input cleared after submit, got %q", got)
		}
		if !m.busy {
			t.Fatal("expected submitted slash to start a new turn")
		}
	})

	t.Run("local slash executes if turn finishes before tick", func(t *testing.T) {
		m, intents := newModelWithDispatchSpy()
		m.windowsPaste.enabled = true
		m.busy = true
		m.status = "running"
		m.input.SetValue("/new scratch")

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(model)
		deferredID := m.windowsPaste.pendingEnterID

		next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
		m = next.(model)
		if m.busy {
			t.Fatal("expected turn done to clear busy before deferred tick")
		}

		next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
		m = next.(model)
		if len(*intents) != 1 {
			t.Fatalf("expected local command to execute after turn finished, got %+v", *intents)
		}
		if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/new scratch" {
			t.Fatalf("unexpected dispatched intent: %+v", got)
		}
		if got := m.input.Value(); got != "" {
			t.Fatalf("expected input cleared after local command, got %q", got)
		}
		if m.localSubmitPending != 1 {
			t.Fatalf("expected local submit pending count, got %d", m.localSubmitPending)
		}
	})

	t.Run("local submit barrier still applies after turn finishes before tick", func(t *testing.T) {
		m, intents := newModelWithDispatchSpy()
		m.windowsPaste.enabled = true
		m.busy = true
		m.status = "running"
		m.input.SetValue("/ask inspect this")

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(model)
		deferredID := m.windowsPaste.pendingEnterID

		m.localSubmitPending = 1
		next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
		m = next.(model)
		if m.busy {
			t.Fatal("expected turn done to clear busy before deferred tick")
		}

		next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
		m = next.(model)
		if len(*intents) != 0 {
			t.Fatalf("local-submit barrier should block deferred slash after turn finished, got %+v", *intents)
		}
		if got := m.input.Value(); got != "/ask inspect this" {
			t.Fatalf("expected blocked slash command to remain editable, got %q", got)
		}
		if m.status != "wait for command to finish" {
			t.Fatalf("expected wait status while local submit is pending, got %q", m.status)
		}
	})
}
func TestWindowsDeferredBusyEnterSuppressesPlanPickerWhenTurnDoneArrivesFirst(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.chatMode = "plan"
	m.mode = modeChat
	m.sawPlanThisTurn = true
	m.sawPlanCompletedThisTurn = true
	m.input.SetValue("follow up while working")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected busy enter to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if m.mode == modePlanImplementation {
		t.Fatal("pending deferred busy enter should suppress plan implementation picker")
	}
	if len(*intents) != 0 {
		t.Fatalf("deferred enter submitted before delay elapsed: %+v", *intents)
	}

	next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "follow up while working" {
		t.Fatalf("expected deferred busy follow-up to submit after turn done, got %+v", *intents)
	}
	if m.mode != modeChat {
		t.Fatalf("expected chat mode after deferred follow-up submit, got %v", m.mode)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected no stale queued prompts, got %+v", m.queuedPrompts)
	}
}
func TestWindowsDeferredBusyEnterSurvivesQueuedDrainBeforeTick(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.queuedPrompts = []queuedPrompt{{Text: "older queued"}}
	m.input.SetValue("new follow up")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected busy enter to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "older queued" {
		t.Fatalf("expected older queued prompt to start, got %+v", *intents)
	}
	if got := m.input.Value(); got != "new follow up" {
		t.Fatalf("expected pending Windows input to survive queued drain, got %q", got)
	}
	if !m.windowsPaste.pendingEnter {
		t.Fatal("expected deferred enter state restored after queued drain")
	}

	next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("deferred follow-up should not submit while the queued turn is running, got intents %+v", *intents)
	}
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "new follow up" {
		t.Fatalf("expected deferred follow-up queued after tick, got %+v", m.queuedPrompts)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected composer cleared after queueing, got %q", got)
	}
}
func TestWindowsActiveBusyPasteSuppressesPlanPickerWhenTurnDoneArrivesFirst(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.chatMode = "plan"
	m.mode = modeChat
	m.sawPlanThisTurn = true
	m.sawPlanCompletedThisTurn = true
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("buffer after pasted continuation = %q", got)
	}

	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	if m.mode == modePlanImplementation {
		t.Fatal("active busy paste should suppress plan implementation picker")
	}
	if len(*intents) != 0 {
		t.Fatalf("active paste submitted before final enter: %+v", *intents)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter should not submit active paste, got %+v", *intents)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final submit enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "line one\nline two" {
		t.Fatalf("expected active busy paste to submit after final enter, got %+v", *intents)
	}
}
func TestWindowsActiveBusyPasteSuppressesPlanPickerAfterQuietWindow(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.chatMode = "plan"
	m.mode = modeChat
	m.sawPlanThisTurn = true
	m.sawPlanCompletedThisTurn = true
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	m.windowsPaste.activeUntil = time.Now().Add(-time.Second)

	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	if m.mode == modePlanImplementation {
		t.Fatal("expired quiet window should not let plan implementation picker cover busy pasted input")
	}
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("expected pasted input to remain buffered, got %q", got)
	}
	if !m.hasPendingWindowsBusyInput() {
		t.Fatal("expected pasted busy input to stay protected after quiet window")
	}
	if len(*intents) != 0 {
		t.Fatalf("pasted input should not submit before final enter, got %+v", *intents)
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter should not submit expired active paste, got %+v", *intents)
	}
}
func TestWindowsActiveBusyPasteSurvivesQueuedDrainBeforeFinalEnter(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.queuedPrompts = []queuedPrompt{{Text: "older queued"}}
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("buffer after pasted continuation = %q", got)
	}

	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "older queued" {
		t.Fatalf("expected older queued prompt to start, got %+v", *intents)
	}
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("expected active Windows paste to survive queued drain, got %q", got)
	}
	if m.windowsPaste.activeUntil.IsZero() {
		t.Fatal("expected active paste window restored after queued drain")
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 1 {
		t.Fatalf("stale deferred enter should not submit active paste, got %+v", *intents)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final submit enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("final paste should not submit while the queued turn is running, got intents %+v", *intents)
	}
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "line one\nline two" {
		t.Fatalf("expected pasted follow-up queued after final enter, got %+v", m.queuedPrompts)
	}
}
func TestWindowsExpiredActiveBusyPasteSurvivesQueuedDrainBeforeFinalEnter(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.queuedPrompts = []queuedPrompt{{Text: "older queued"}}
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	m.windowsPaste.activeUntil = time.Now().Add(-time.Second)

	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "older queued" {
		t.Fatalf("expected older queued prompt to start, got %+v", *intents)
	}
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("expected expired active Windows paste to survive queued drain, got %q", got)
	}
	if !m.hasPendingWindowsBusyInput() {
		t.Fatal("expected expired active paste to remain protected after queued drain")
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 1 {
		t.Fatalf("stale deferred enter should not submit expired active paste, got %+v", *intents)
	}

	m = flushWindowsPasteBurstForTest(t, m)
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected final submit enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("final paste should not submit while the queued turn is running, got intents %+v", *intents)
	}
	if len(m.queuedPrompts) != 1 || m.queuedPrompts[0].Text != "line one\nline two" {
		t.Fatalf("expected pasted follow-up queued after final enter, got %+v", m.queuedPrompts)
	}
}
func TestWindowsActiveBusyPasteSurvivesLocalSubmitDeferredQueuedDrain(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first pasted newline to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("buffer after pasted continuation = %q", got)
	}

	m.localSubmitPending = 1
	m.queuedPrompts = []queuedPrompt{{Text: "older queued"}}
	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	if len(*intents) != 0 {
		t.Fatalf("queued prompt should wait for local submit done, got %+v", *intents)
	}
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("expected active Windows paste to remain while local submit is pending, got %q", got)
	}

	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventLocalSubmitDone}))
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "older queued" {
		t.Fatalf("expected older queued prompt to start after local submit done, got %+v", *intents)
	}
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("expected active Windows paste to survive deferred queued drain, got %q", got)
	}
	if !m.hasPendingWindowsBusyInput() {
		t.Fatal("expected pasted busy input to stay protected after deferred queued drain")
	}

	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: deferredID})
	if len(*intents) != 1 {
		t.Fatalf("stale deferred enter should not submit restored active paste, got %+v", *intents)
	}
}
func TestWindowsActiveBusyPasteSuppressesDeferredPlanPickerAfterLocalSubmitDone(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.chatMode = "plan"
	m.mode = modeChat
	m.sawPlanThisTurn = true
	m.sawPlanCompletedThisTurn = true
	m.input.SetValue("line one")

	var cmd tea.Cmd
	m, cmd = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected first pasted newline to be deferred")
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	m.localSubmitPending = 1
	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	if !m.deferredPlanPicker {
		t.Fatal("expected plan implementation picker to defer while local submit is pending")
	}

	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{Kind: protocol.EventLocalSubmitDone}))
	if m.mode == modePlanImplementation {
		t.Fatal("pending Windows paste should suppress deferred implementation picker")
	}
	if m.deferredPlanPicker {
		t.Fatal("expected pending Windows paste to clear deferred picker flag")
	}
	if got := m.windowsPasteBuffer(); got != "line one\nline two" {
		t.Fatalf("expected active Windows paste to remain after local submit done, got %q", got)
	}
	if len(*intents) != 0 {
		t.Fatalf("pending paste should not submit before final enter, got %+v", *intents)
	}
}
func TestWindowsDeferredStoppingBusyEnterDoesNotSubmitAfterTurnDoneArrivesFirst(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.stopping = true
	m.chatMode = "plan"
	m.mode = modeChat
	m.sawPlanThisTurn = true
	m.sawPlanCompletedThisTurn = true
	m.input.SetValue("follow up while stopping")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected stopping enter to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if m.mode == modePlanImplementation {
		t.Fatal("pending deferred stopping enter should suppress plan implementation picker")
	}

	next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("stopping deferred enter should not submit after turn done, got %+v", *intents)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected no queued prompt after stopped turn, got %+v", m.queuedPrompts)
	}
	if got := m.input.Value(); got != "follow up while stopping" {
		t.Fatalf("expected stopping follow-up to remain in composer, got %q", got)
	}
	if m.windowsPaste.pendingEnter {
		t.Fatal("expected deferred enter state cleared")
	}
}
func TestWindowsDeferredBusyEnterInterruptedBeforeTickSubmitsAfterInterrupt(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.chatMode = "plan"
	m.mode = modeChat
	m.sawPlanThisTurn = true
	m.sawPlanCompletedThisTurn = true
	m.input.SetValue("follow up before interrupt")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected busy enter to be deferred")
	}
	deferredID := m.windowsPaste.pendingEnterID

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !m.stopping {
		t.Fatal("expected Esc to start stopping")
	}
	if m.windowsPaste.pendingEnter {
		t.Fatal("expected interrupt to clear pending Windows enter")
	}
	if !m.submitQueuedPromptAfterInterrupt {
		t.Fatal("expected interrupt to queue the draft for immediate submit after turn done")
	}

	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)
	if m.mode == modePlanImplementation {
		t.Fatal("pending interrupted enter should suppress plan implementation picker")
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "follow up before interrupt" {
		t.Fatalf("interrupted deferred enter should submit after turn done, got %+v", *intents)
	}

	next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("stale deferred enter should not submit again, got %+v", *intents)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected queued prompt consumed after interrupted turn, got %+v", m.queuedPrompts)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected interrupted follow-up to clear after submit, got %q", got)
	}
	if m.windowsPaste.pendingEnter {
		t.Fatal("expected deferred enter state cleared")
	}
}
func TestWindowsDeferredEnterStillSubmitsSingleLine(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("run the tests")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("windows enter should be deferred before submitting, got %+v", *intents)
	}

	next, _ = m.Update(windowsDeferredEnterMsg{id: m.windowsPaste.pendingEnterID})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected deferred submit, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "run the tests" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsDeferredEnterHonorsLocalSubmitBarrier(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.localSubmitPending = 1
	m.status = "command pending"
	m.input.SetValue("start a turn")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if len(*intents) != 0 {
		t.Fatalf("pending local submit should block Windows deferred enter, got %+v", *intents)
	}
	if m.windowsPaste.pendingEnter {
		t.Fatal("local submit barrier should not arm Windows deferred enter")
	}
	if got := m.input.Value(); got != "start a turn" {
		t.Fatalf("expected prompt to remain editable, got %q", got)
	}
	if m.status != "wait for command to finish" {
		t.Fatalf("expected wait status while local submit is pending, got %q", m.status)
	}
}
func TestWindowsDeferredEnterTickHonorsLocalSubmitBarrier(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("start a turn")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	deferredID := m.windowsPaste.pendingEnterID
	if !m.windowsPaste.pendingEnter {
		t.Fatal("expected Windows enter to be deferred")
	}

	m.localSubmitPending = 1
	m.status = "command pending"
	next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
	m = next.(model)

	if len(*intents) != 0 {
		t.Fatalf("deferred enter should not overtake pending local submit, got %+v", *intents)
	}
	if m.windowsPaste.pendingEnter {
		t.Fatal("expected deferred enter state cleared after blocked tick")
	}
	if got := m.input.Value(); got != "start a turn" {
		t.Fatalf("expected prompt to remain editable, got %q", got)
	}
	if m.status != "wait for command to finish" {
		t.Fatalf("expected wait status while local submit is pending, got %q", m.status)
	}
}
func TestWindowsDeferredEnterCancelsOnEditingKey(t *testing.T) {
	tests := []struct {
		name      string
		key       tea.KeyMsg
		wantInput string
	}{
		{name: "backspace", key: tea.KeyMsg{Type: tea.KeyBackspace}, wantInput: "ru"},
		{name: "left", key: tea.KeyMsg{Type: tea.KeyLeft}, wantInput: "run"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, intents := newModelWithDispatchSpy()
			m.windowsPaste.enabled = true
			m.input.SetValue("run")

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(model)
			deferredID := m.windowsPaste.pendingEnterID
			if !m.windowsPaste.pendingEnter {
				t.Fatal("expected Windows enter to be deferred")
			}

			next, _ = m.Update(tt.key)
			m = next.(model)
			if m.windowsPaste.pendingEnter {
				t.Fatal("editing key should cancel Windows deferred enter")
			}
			if got := m.input.Value(); got != tt.wantInput {
				t.Fatalf("unexpected input after editing key: got %q want %q", got, tt.wantInput)
			}

			next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
			m = next.(model)
			if len(*intents) != 0 {
				t.Fatalf("canceled deferred enter should not submit, got %+v", *intents)
			}
		})
	}
}
func TestWindowsDeferredEnterShiftEnterCancelsDeferredSubmit(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	if !m.shouldCancelWindowsDeferredEnterForKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("shift+enter")}) {
		t.Fatal("Shift+Enter must cancel pending Windows deferred enter before composer inserts newline")
	}
}
func TestWindowsBracketedPasteCanSubmitImmediately(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one\nline two"), Paste: true})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if len(*intents) != 1 {
		t.Fatalf("expected bracketed paste to submit immediately, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "line one\nline two" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestWindowsBracketedPasteContainingMouseCSIFragmentIsNotSwallowed(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.startBusy()

	pasted := "\x1b[<64;10;10M\npayload"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	m = next.(model)

	if got := m.input.Value(); got == "" || !strings.Contains(got, "[<64;10;10M\npayload") {
		t.Fatalf("expected bracketed paste content not swallowed, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("expected bracketed paste not to enter fallback buffer, got %q", got)
	}
}
func TestWindowsPasteFallbackIMECommitSubmitsNormally(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("你好")})
	m = next.(model)
	if got := m.input.Value(); got != "你好" {
		t.Fatalf("expected IME commit to render normally, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("expected IME commit not to enter paste buffer, got %q", got)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected Windows enter to be deferred")
	}
	submitID := m.windowsPaste.pendingEnterID
	next, _ = m.Update(windowsDeferredEnterMsg{id: submitID})
	m = next.(model)

	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "你好" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestDefaultEnterSubmitRemainsImmediate(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("run the tests")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected immediate submit, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "run the tests" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
}
func TestClearScreenCmdClearsWindowsScrollbackAndRenderer(t *testing.T) {
	var out bytes.Buffer
	cmd := clearScreenCmdForOS("windows", &out)
	msg := cmd()
	if got, want := out.String(), "\033[H\033[2J\033[3J"; got != want {
		t.Fatalf("windows clear sequence = %q, want %q", got, want)
	}
	if msg == nil {
		t.Fatal("windows clear should return a Bubble Tea clear-screen message")
	}
}
func TestWindowsPasteFallbackTypingPreservesSkillBinding(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.skills.all = []skillSuggestion{{Name: "code-review", Description: "Review local changes", SkillFilePath: "/tmp/code-review/SKILL.md"}}
	m.input.SetValue("$co")
	m.updateSlashMatches()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.skillBinding == nil {
		t.Fatal("expected selected skill to set binding")
	}

	m = typeRunesForTest(t, m, "review this diff")
	if got := m.input.Value(); got != "$code-review review this diff" {
		t.Fatalf("unexpected composer value after Windows fallback typing: %q", got)
	}
	if m.skillBinding == nil || m.skillBinding.Name != "code-review" {
		t.Fatalf("expected Windows fallback typing to preserve skill binding, got %+v", m.skillBinding)
	}

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	submitID := m.windowsPaste.pendingEnterID
	m, _ = updateTestModel(t, m, windowsDeferredEnterMsg{id: submitID})
	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	got := (*intents)[0]
	if got.Kind != protocol.IntentSubmit || got.Input != "$code-review review this diff" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
	if got.SkillBinding == nil || got.SkillBinding.Name != "code-review" || got.SkillBinding.SkillFilePath != "/tmp/code-review/SKILL.md" {
		t.Fatalf("expected submit skill binding after Windows fallback typing, got %+v", got.SkillBinding)
	}
}
func TestWindowsPasteViewCacheInvalidatesForApprovalModal(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 30
	chatView := m.View()
	if strings.Contains(chatView, "Approval required") {
		t.Fatalf("chat view unexpectedly contained approval prompt:\n%s", chatView)
	}

	m.setWindowsPasteBuffer("pasted text still streaming")
	m.mode = modeApproval
	m.status = "approval required"
	m.approval.toolCallID = "tool-1"
	m.approval.toolName = "shell_run"
	m.approval.reason = "shell_run: date"

	view := m.View()
	if !strings.Contains(view, "Approval required") {
		t.Fatalf("paste view cache hid approval prompt:\n%s", view)
	}
	if view == chatView {
		t.Fatal("expected modal transition to invalidate cached paste frame")
	}
}
func TestStoppingTurnDoneRestoresQueuedPromptsWithPendingWindowsPaste(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.stopping = true
	m.queuedPrompts = []queuedPrompt{{Text: "older queued"}}
	m.setWindowsPasteBuffer("pasted follow up")
	m.windowsPaste.activeUntil = time.Now().Add(windowsPasteQuietDelay)
	m.windowsPaste.busyInput = true
	m.windowsPaste.busyInputStop = true
	flushID := m.windowsPaste.burstID

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventTurnDone}))
	m = next.(model)

	if len(*intents) != 0 {
		t.Fatalf("expected no submitted intents after stopping, got %+v", *intents)
	}
	if len(m.queuedPrompts) != 0 {
		t.Fatalf("expected queue restored and cleared, got %+v", m.queuedPrompts)
	}
	if got := m.input.Value(); got != "older queued\npasted follow up" {
		t.Fatalf("expected queued prompt and pending paste restored to composer, got %q", got)
	}
	if m.windowsPasteBuffer() != "" || !m.windowsPaste.activeUntil.IsZero() || m.windowsPaste.busyInput || m.windowsPaste.busyInputStop {
		t.Fatalf("expected pending paste state cleared after restore, got %+v", m.windowsPaste)
	}

	next, _ = m.Update(windowsPasteBurstFlushMsg{id: flushID})
	m = next.(model)
	if got := m.input.Value(); got != "older queued\npasted follow up" {
		t.Fatalf("stale paste flush should not mutate restored composer, got %q", got)
	}
}
func TestCtrlCWhileBusyClearsDraftAndPendingWindowsEnter(t *testing.T) {
	// After PR 2's Ctrl+C-precedence change, a non-empty composer while busy
	// resolves Ctrl+C to "clear the draft" rather than "interrupt the turn".
	// The clear path still tears down the Windows deferred-enter state via
	// resetWindowsPasteFallbackInputState, so a stale deferred enter cannot
	// fire after the user abandons the queued draft. Esc remains the
	// unconditional busy interrupt for users who also want to cancel the
	// running turn.
	m, intents := newModelWithDispatchSpy()
	m.runtime = &testRuntime{}
	m.width = 80
	m.height = 24
	m.windowsPaste.enabled = true
	m.busy = true
	m.input.SetValue("queued prompt")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.windowsPaste.pendingEnter {
		t.Fatal("expected enter while busy to arm deferred submit")
	}
	deferredID := m.windowsPaste.pendingEnterID

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if m.windowsPaste.pendingEnter {
		t.Fatal("expected ctrl+c clear path to drop pending windows enter")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected Ctrl+C with non-empty composer to clear draft, got %q", got)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected Ctrl+C with non-empty composer not to interrupt the turn, got intents %+v", *intents)
	}
	if m.stopping {
		t.Fatal("expected Ctrl+C with non-empty composer not to mark the turn stopping")
	}

	next, _ = m.Update(windowsDeferredEnterMsg{id: deferredID})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("stale deferred enter should not submit after clear, got %+v", *intents)
	}
}
func TestChatFooterShowsWindowsDirectoryTail(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "high", "on")
	m.width = 100
	m.height = 24
	m.cwd = `C:\Users\goranka`

	view := m.View()
	assertFooterLastLine(t, view, `goranka`)
	assertFooterLastLineNotContains(t, view, ` ~`)
}
func TestWindowsPasteFallbackDoesNotCaptureMouseCSIFragments(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 80
	m.height = 8
	m.windowsPaste.enabled = true
	m.startBusy()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[<64;10;10M")})
	m = next.(model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected mouse CSI fragment not to enter composer, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("expected mouse CSI fragment not to enter Windows paste buffer, got %q", got)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<65;69;14M")})
	m = next.(model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected split mouse CSI fragment not to enter composer, got %q", got)
	}
	if got := m.windowsPasteBuffer(); got != "" {
		t.Fatalf("expected split mouse CSI fragment not to enter Windows paste buffer, got %q", got)
	}
}
func TestCtrlCWhileBusyClearsPendingWindowsPasteBuffer(t *testing.T) {
	// Windows paste fallback buffers burst chunks in m.windowsPasteBuffer()
	// for windowsPasteQuietDelay (80ms) before flushing into the textarea.
	// In that window m.input.Value() is still empty, so the busy gate must
	// also consult hasWindowsPasteBuffer() to avoid interrupting the turn
	// when the user is really just trying to drop the pasted draft.
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.busy = true
	m.setWindowsPasteBuffer("buffered paste chunk")
	m.windowsPaste.activeUntil = time.Now().Add(windowsPasteQuietDelay)

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.hasWindowsPasteBuffer() {
		t.Fatalf("expected Ctrl+C to drop pending paste buffer, got %q", m.windowsPasteBuffer())
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected composer to stay empty after clearing pending paste, got %q", got)
	}
	if m.stopping {
		t.Fatal("expected Ctrl+C with pending paste buffer not to interrupt the busy turn")
	}
}
func TestCtrlCClearsPendingWindowsPasteBufferNotBusy(t *testing.T) {
	// Outside of busy, the same paste-buffer state must hit the clear path
	// rather than arming quit — otherwise users get a "Press Ctrl+C again
	// to quit" prompt while their just-pasted draft is still pending.
	m, _ := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.setWindowsPasteBuffer("buffered chunk")
	m.windowsPaste.activeUntil = time.Now().Add(windowsPasteQuietDelay)

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.hasWindowsPasteBuffer() {
		t.Fatalf("expected Ctrl+C to drop pending paste buffer outside busy, got %q", m.windowsPasteBuffer())
	}
	if !m.quitArmedUntil.IsZero() {
		t.Fatal("expected Ctrl+C with pending paste buffer to clear (not arm quit)")
	}
}
func TestWindowsFallbackTypedRuneHeightGrowthUpdatesLayout(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.windowsPaste.enabled = true
	m.width = 32
	m.height = 10
	m.transcript = nil
	for i := 0; i < 60; i++ {
		m.appendTranscript("info", tuirender.KindText, fmt.Sprintf("entry-%02d", i))
	}
	m.input.SetValue("seed")
	clock := newFakeClock()
	m.windowsPaste.nowFunc = clock.now
	m.refreshViewportContentFollow(true)
	mainWidth, _ := m.layoutDims()
	initialBodyHeight := m.viewportBodyHeight(mainWidth)
	initialGeneration := m.chat.generation

	for i := 0; i < 80; i++ {
		clock.advance(100 * time.Millisecond)
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		m = next.(model)
	}
	mainWidth, _ = m.layoutDims()
	if got := m.input.Value(); got != "seed"+strings.Repeat("b", 80) {
		t.Fatalf("expected typed runes to enter composer, got %q", got)
	}
	if m.hasWindowsPasteBuffer() {
		t.Fatalf("ordinary typed runes should not enter Windows paste buffer, got %q", m.windowsPasteBuffer())
	}
	if got := m.viewportBodyHeight(mainWidth); got >= initialBodyHeight {
		t.Fatalf("expected typed rune wrapping to reduce chat body height, got %d want < %d", got, initialBodyHeight)
	}
	if m.chat.generation != initialGeneration {
		t.Fatalf("expected typed rune height growth not to rerender chat, gen=%d want=%d", m.chat.generation, initialGeneration)
	}
}
