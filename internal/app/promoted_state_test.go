package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Unit coverage for the promoted-tool state persistence and the sessionID
// synchronization added with the sessionMu fix: load/write/restore branches
// plus the setSessionID/SessionID/sessionPath accessor round-trip.

func TestLoadPromotedToolState(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := newMCPRuntimeTestApp(mgr)
	dir := t.TempDir()
	app.sessionsDir = dir
	app.sessionID = "sess"
	sessionDir := filepath.Join(dir, "sess")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	catalog := mgr.BuildDeferredCatalog()

	writeState := func(t *testing.T, state promotedToolState) {
		t.Helper()
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "promoted_tools.json"), b, 0644); err != nil {
			t.Fatalf("write state: %v", err)
		}
	}

	t.Run("missing file returns no tools", func(t *testing.T) {
		names, err := app.loadPromotedToolState()
		if err != nil || names != nil {
			t.Fatalf("load = %v, %v; want nil, nil", names, err)
		}
	})

	t.Run("stale catalog hash returns no tools", func(t *testing.T) {
		writeState(t, promotedToolState{CatalogHash: "stale-hash", ToolNames: []string{"mcp__runtime__echo"}})
		names, err := app.loadPromotedToolState()
		if err != nil || names != nil {
			t.Fatalf("load = %v, %v; want nil, nil (stale)", names, err)
		}
	})

	t.Run("valid state returns names and records hash", func(t *testing.T) {
		writeState(t, promotedToolState{CatalogHash: catalog.Hash(), ToolNames: []string{"mcp__runtime__echo"}})
		names, err := app.loadPromotedToolState()
		if err != nil || len(names) != 1 || names[0] != "mcp__runtime__echo" {
			t.Fatalf("load = %v, %v; want [mcp__runtime__echo], nil", names, err)
		}
		if app.promotedCatalogHash != catalog.Hash() {
			t.Fatalf("promotedCatalogHash = %q, want %q", app.promotedCatalogHash, catalog.Hash())
		}
	})

	t.Run("malformed json returns error", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(sessionDir, "promoted_tools.json"), []byte("not json"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := app.loadPromotedToolState(); err == nil {
			t.Fatal("load = nil error; want error for malformed JSON")
		}
	})

	t.Run("nil app returns no tools", func(t *testing.T) {
		var nilApp *App
		if names, err := nilApp.loadPromotedToolState(); err != nil || names != nil {
			t.Fatalf("nil app load = %v, %v; want nil, nil", names, err)
		}
	})
}

func TestWritePromotedToolState(t *testing.T) {
	app := &App{}
	dir := t.TempDir()
	app.sessionsDir = dir
	app.sessionID = "sess"

	t.Run("nil app is a no-op", func(t *testing.T) {
		var nilApp *App
		if err := nilApp.writePromotedToolState(promotedToolState{}); err != nil {
			t.Fatalf("nil app write: %v", err)
		}
	})

	t.Run("empty session path is a no-op", func(t *testing.T) {
		empty := &App{}
		if err := empty.writePromotedToolState(promotedToolState{CatalogHash: "h", ToolNames: []string{"x"}}); err != nil {
			t.Fatalf("empty path write: %v", err)
		}
	})

	t.Run("writes json to session path", func(t *testing.T) {
		state := promotedToolState{CatalogHash: "hash-123", ToolNames: []string{"mcp__runtime__echo", "tool_search"}}
		if err := app.writePromotedToolState(state); err != nil {
			t.Fatalf("write: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "sess", "promoted_tools.json"))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var got promotedToolState
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal back: %v", err)
		}
		if got.CatalogHash != state.CatalogHash || len(got.ToolNames) != len(state.ToolNames) {
			t.Fatalf("round-trip = %+v, want %+v", got, state)
		}
	})
}

func TestRestorePromotedToolsNoState(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := newMCPRuntimeTestApp(mgr)
	app.sessionsDir = t.TempDir()
	app.sessionID = "sess"

	if err := app.RestorePromotedTools(); err != nil {
		t.Fatalf("restore without state: %v", err)
	}

	t.Run("nil app and nil manager are no-ops", func(t *testing.T) {
		var nilApp *App
		if err := nilApp.RestorePromotedTools(); err != nil {
			t.Fatalf("nil app restore: %v", err)
		}
		noMgr := newMCPRuntimeTestApp(nil)
		if err := noMgr.RestorePromotedTools(); err != nil {
			t.Fatalf("nil manager restore: %v", err)
		}
	})
}

func TestRestorePromotedToolsSuccess(t *testing.T) {
	mgr := newMCPRuntimeTestManager(t, "echoes a message")
	app := newMCPRuntimeTestApp(mgr)
	dir := t.TempDir()
	app.sessionsDir = dir
	app.sessionID = "sess"
	sessionDir := filepath.Join(dir, "sess")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	catalog := mgr.BuildDeferredCatalog()
	state := promotedToolState{CatalogHash: catalog.Hash(), ToolNames: []string{"mcp__runtime__echo"}}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "promoted_tools.json"), b, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if err := app.RestorePromotedTools(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if app.toolRegistry.Get("mcp__runtime__echo") == nil && app.baseToolRegistry.Get("mcp__runtime__echo") == nil {
		t.Fatal("promoted tool not registered after restore")
	}
	if !app.promotedTools["mcp__runtime__echo"] {
		t.Fatalf("promotedTools = %v, want mcp__runtime__echo=true", app.promotedTools)
	}
}

func TestSessionIDAccessors(t *testing.T) {
	dir := t.TempDir()
	a := &App{sessionsDir: dir, sessionID: "old"}

	a.setSessionID("new")
	if got := a.SessionID(); got != "new" {
		t.Fatalf("SessionID() = %q, want %q", got, "new")
	}
	gotDir, gotID := a.sessionPath()
	if gotDir != dir || gotID != "new" {
		t.Fatalf("sessionPath() = (%q, %q), want (%q, %q)", gotDir, gotID, dir, "new")
	}
}
