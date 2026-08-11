package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStrictSessionFile(t *testing.T, sessionsDir, id, content string) {
	t.Helper()
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, id+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
}

func TestResolveStrictSessionRejectsBlankIDs(t *testing.T) {
	dir := t.TempDir()
	for _, raw := range []string{"", "   ", "\t\n"} {
		if _, err := ResolveStrictSession(dir, raw); err == nil {
			t.Fatalf("expected blank id %q to be rejected", raw)
		}
	}
}

func TestResolveStrictSessionRejectsUnsanitizedIDs(t *testing.T) {
	dir := t.TempDir()
	writeStrictSessionFile(t, dir, "a_b", `{"SessionID":"a_b","Role":"user","Text":"hi"}`+"\n")
	for _, raw := range []string{"a b", "a/b", "a.b", "a/b/c", "a b c"} {
		if _, err := ResolveStrictSession(dir, raw); err == nil {
			t.Fatalf("expected unsanitized id %q to be rejected", raw)
		}
	}
}

func TestResolveStrictSessionRequiresExactFile(t *testing.T) {
	dir := t.TempDir()
	writeStrictSessionFile(t, dir, "s1", `{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n")
	if _, err := ResolveStrictSession(dir, "s1"); err != nil {
		t.Fatalf("existing session should resolve: %v", err)
	}
	if _, err := ResolveStrictSession(dir, "missing"); err == nil {
		t.Fatal("expected missing session to be rejected")
	}
}

func TestResolveStrictSessionRejectsCanonicalMismatch(t *testing.T) {
	dir := t.TempDir()
	writeStrictSessionFile(t, dir, "s1", `{"SessionID":"s2","Role":"user","Text":"hi"}`+"\n")
	if _, err := ResolveStrictSession(dir, "s1"); err == nil {
		t.Fatal("expected canonical id mismatch to be rejected")
	}
}

func TestResolveStrictSessionRecoverability(t *testing.T) {
	t.Run("empty file passes", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "s1", "")
		st, err := ResolveStrictSession(dir, "s1")
		if err != nil {
			t.Fatalf("empty existing session should pass: %v", err)
		}
		if !st.Empty {
			t.Fatal("expected Empty flag on empty session")
		}
	})
	t.Run("partial corruption tolerated", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "s1", "not-json\n"+
			`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"+
			"also-not-json\n")
		st, err := ResolveStrictSession(dir, "s1")
		if err != nil {
			t.Fatalf("session with one valid line should resolve: %v", err)
		}
		if st.Messages != 1 {
			t.Fatalf("expected 1 recovered message, got %d", st.Messages)
		}
	})
	t.Run("all corrupt rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "s1", "not-json\nstill-not-json\n")
		if _, err := ResolveStrictSession(dir, "s1"); err == nil {
			t.Fatal("expected unrecoverable session to be rejected")
		}
	})
}

func TestResolveStrictSessionRejectsSubagent(t *testing.T) {
	t.Run("by id pattern", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "subagent-child", `{"SessionID":"subagent-child","Role":"user","Text":"hi"}`+"\n")
		if _, err := ResolveStrictSession(dir, "subagent-child"); err == nil {
			t.Fatal("expected subagent id pattern to be rejected")
		}
	})
	t.Run("by meta kind", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "s1", `{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n")
		if err := SaveSessionMeta(dir, "s1", SessionMeta{Kind: "subagent", ParentSessionID: "parent"}); err != nil {
			t.Fatalf("save meta: %v", err)
		}
		if _, err := ResolveStrictSession(dir, "s1"); err == nil {
			t.Fatal("expected meta-kind subagent to be rejected")
		}
	})
}

func TestResolveStrictSessionSidecarCompat(t *testing.T) {
	t.Run("missing sidecars default", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "s1", `{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n")
		st, err := ResolveStrictSession(dir, "s1")
		if err != nil {
			t.Fatalf("missing sidecars should resolve: %v", err)
		}
		if st.Mode != ModeAgent {
			t.Fatalf("missing mode sidecar should default to agent, got %q", st.Mode)
		}
	})
	t.Run("corrupt mode state fails", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "s1", `{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n")
		if err := os.WriteFile(modeStatePath(dir, "s1"), []byte("{not-json"), 0o600); err != nil {
			t.Fatalf("write corrupt mode: %v", err)
		}
		if _, err := ResolveStrictSession(dir, "s1"); err == nil {
			t.Fatal("expected corrupt mode sidecar to fail")
		}
	})
	t.Run("corrupt meta fails", func(t *testing.T) {
		dir := t.TempDir()
		writeStrictSessionFile(t, dir, "s1", `{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n")
		if err := os.WriteFile(metaStatePath(dir, "s1"), []byte("{not-json"), 0o600); err != nil {
			t.Fatalf("write corrupt meta: %v", err)
		}
		if _, err := ResolveStrictSession(dir, "s1"); err == nil {
			t.Fatal("expected corrupt meta sidecar to fail")
		}
	})
}

func TestResolveStrictSessionErrorsAreActionable(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveStrictSession(dir, "s1"); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "s1") {
		t.Fatalf("error should name the session: %v", err)
	}
}
