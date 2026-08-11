package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestDisplaySessionChoiceRowPreservesOrdinal(t *testing.T) {
	got := displaySessionChoiceRow("   2) 1m ago    main                     hello")
	if got != "   2) 1m ago    main                     hello" {
		t.Fatalf("expected ordinal to be preserved, got %q", got)
	}
}

func TestDisplaySessionChoiceRowHidesCurrentSessionMarker(t *testing.T) {
	got := displaySessionChoiceRow("*  1) 4s ago    -                        who are you")
	if got != "   1) 4s ago    -                        who are you" {
		t.Fatalf("expected marker to be replaced with spacing, got %q", got)
	}
}

func TestSessionChoiceNumberAtStillParsesCurrentMarker(t *testing.T) {
	rows := []string{
		"recent sessions:",
		"   #   Updated   Branch                    Conversation",
		"*  1) 4s ago    -                        who are you",
	}
	if got := sessionChoiceNumberAt(rows, 2); got != 1 {
		t.Fatalf("expected session number 1, got %d", got)
	}
}

func TestParseSessionChoiceDisplaySplitsColumns(t *testing.T) {
	got, ok := parseSessionChoiceDisplay("   2) 1m ago    main                     hello resume")
	if !ok {
		t.Fatal("expected session choice row to parse")
	}
	if got.Number != "2)" || got.Updated != "1m ago" || got.Branch != "main" || got.Conversation != "hello resume" {
		t.Fatalf("unexpected parsed session row: %+v", got)
	}
}

func TestParseSessionChoiceDisplayHandlesNonASCIIBranch(t *testing.T) {
	branch := strings.Repeat("中", 24)
	row := fmt.Sprintf("   6) %-9s %-24s %s", "14m ago", branch, "git status")
	got, ok := parseSessionChoiceDisplay(row)
	if !ok {
		t.Fatal("expected non-ASCII session choice row to parse")
	}
	if got.Number != "6)" || got.Updated != "14m ago" || got.Branch != branch || got.Conversation != "git status" {
		t.Fatalf("unexpected parsed non-ASCII session row: %+v", got)
	}
	rendered := pickerSessionChoiceRow(got, true)
	if strings.Contains(rendered, "�") {
		t.Fatalf("rendered session row contains replacement characters:\n%s", rendered)
	}
}
func TestSessionChoiceNavigationWrapsAround(t *testing.T) {
	rows := []string{
		"recent sessions:",
		"*  1) 4s ago    -                        who are you",
		"   2) 1m ago    main                     hello",
		"   3) 5m ago    feat/x                   git status",
	}
	// Down from the last selectable row wraps to the first selectable row.
	if got := nextSessionChoiceIndex(rows, 3); got != 1 {
		t.Fatalf("expected down at last to wrap to first, got %d", got)
	}
	// Up from the first selectable row wraps to the last selectable row.
	if got := prevSessionChoiceIndex(rows, 1); got != 3 {
		t.Fatalf("expected up at first to wrap to last, got %d", got)
	}
	// A single selectable row stays put on both directions.
	only := []string{"recent sessions:", "   1) 4s ago    -                        who are you"}
	if got := prevSessionChoiceIndex(only, 1); got != 1 {
		t.Fatalf("expected up to stay on only selectable row, got %d", got)
	}
	if got := nextSessionChoiceIndex(only, 1); got != 1 {
		t.Fatalf("expected down to stay on only selectable row, got %d", got)
	}
}
