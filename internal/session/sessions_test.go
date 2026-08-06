package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListSessionsIgnoresLegacyPlanSummary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.plan.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write legacy plan: %v", err)
	}
	got, err := ListSessions(dir, 10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("unexpected sessions: %+v", got)
	}
}

func TestListSessionsIgnoresToolInputEventSidecars(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.tool_input_events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.approval_events.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write approval sidecar: %v", err)
	}
	got, err := ListSessions(dir, 10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("unexpected sessions: %+v", got)
	}
}

func TestLastMessageUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ts := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	older := ts.Add(-2 * time.Hour)

	// Last line wins, regardless of role.
	write("multi.jsonl",
		`{"Role":"user","Text":"a","UpdatedAt":"`+older.Format(time.RFC3339)+`"}`+"\n"+
			`{"Role":"assistant","Text":"b","UpdatedAt":"`+ts.Format(time.RFC3339)+`"}`+"\n")
	if got, ok := LastMessageUpdatedAt(dir, "multi"); !ok || !got.Equal(ts) {
		t.Fatalf("multi: got %v ok=%v, want %v", got, ok, ts)
	}

	// Trailing newline: the real last line is before it.
	write("trailing.jsonl",
		`{"Role":"user","Text":"x","UpdatedAt":"`+ts.Format(time.RFC3339)+`"}`+"\n")
	if got, ok := LastMessageUpdatedAt(dir, "trailing"); !ok || !got.Equal(ts) {
		t.Fatalf("trailing: got %v ok=%v, want %v", got, ok, ts)
	}

	// No timestamp -> not ok (caller falls back to file mtime).
	write("nots.jsonl", `{"Role":"user","Text":"no timestamp"}`+"\n")
	if _, ok := LastMessageUpdatedAt(dir, "nots"); ok {
		t.Fatal("nots: expected ok=false")
	}

	// Empty and missing files are not ok.
	write("empty.jsonl", "")
	if _, ok := LastMessageUpdatedAt(dir, "empty"); ok {
		t.Fatal("empty: expected ok=false")
	}
	if _, ok := LastMessageUpdatedAt(dir, "missing"); ok {
		t.Fatal("missing: expected ok=false")
	}
}

func TestLastMessageUpdatedAtEdgeCases(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Newline-only file: no message line.
	write("nl-only.jsonl", "\n")
	if _, ok := LastMessageUpdatedAt(dir, "nl-only"); ok {
		t.Fatal("nl-only: expected ok=false")
	}

	// Invalid JSON as the last line.
	write("bad.jsonl", `{"Role":"user","Text":"ok"}`+"\n"+`{not json}`+"\n")
	if _, ok := LastMessageUpdatedAt(dir, "bad"); ok {
		t.Fatal("bad: expected ok=false (unparseable tail)")
	}

	// Last line exceeds the per-line cap: unparseable -> ok=false.
	write("huge.jsonl", `{"Role":"user","Text":"x","UpdatedAt":"2026-08-05T12:00:00Z"}`+"\n"+
		`{"Role":"user","Text":"`+strings.Repeat("y", 2*1024*1024+1)+`"}`+"\n")
	if _, ok := LastMessageUpdatedAt(dir, "huge"); ok {
		t.Fatal("huge: expected ok=false (line over the 2 MiB cap)")
	}

	// Zero UpdatedAt on the last line -> ok=false (caller falls back to mtime).
	write("zero.jsonl", `{"Role":"user","Text":"z","UpdatedAt":"0001-01-01T00:00:00Z"}`+"\n")
	if _, ok := LastMessageUpdatedAt(dir, "zero"); ok {
		t.Fatal("zero: expected ok=false (zero timestamp)")
	}
}
