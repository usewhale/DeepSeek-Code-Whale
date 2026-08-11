package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/usewhale/whale/internal/store"
)

func TestInitialAppSessionIDStrictGate(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := store.DefaultSessionsDir(dataDir)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	t.Run("existing session resolves", func(t *testing.T) {
		got, err := initialAppSessionID(sessionsDir, StartOptions{SessionID: "s1"})
		if err != nil {
			t.Fatalf("initialAppSessionID: %v", err)
		}
		if got != "s1" {
			t.Fatalf("session = %q, want s1", got)
		}
	})

	t.Run("missing session rejected", func(t *testing.T) {
		if _, err := initialAppSessionID(sessionsDir, StartOptions{SessionID: "missing"}); err == nil {
			t.Fatal("expected missing session to be rejected by the strict gate")
		}
	})

	t.Run("unsanitized id rejected", func(t *testing.T) {
		if _, err := initialAppSessionID(sessionsDir, StartOptions{SessionID: "s1/x"}); err == nil {
			t.Fatal("expected unsanitized id to be rejected by the strict gate")
		}
	})
}
