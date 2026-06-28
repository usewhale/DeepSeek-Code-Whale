package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentCloseCancelsCleanupLoopContext(t *testing.T) {
	a := NewAgent(nil, nil, nil)
	if err := a.cleanupLoopContext().Err(); err != nil {
		t.Fatalf("cleanup loop context should start active: %v", err)
	}

	a.Close()

	if err := a.cleanupLoopContext().Err(); err != context.Canceled {
		t.Fatalf("cleanup loop context err = %v, want %v", err, context.Canceled)
	}
}

func TestCleanupExpiredToolResults(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	oldDir := filepath.Join(root, "old-session")
	freshDir := filepath.Join(root, "fresh-session")
	oldEmptyDir := filepath.Join(root, "old-empty-session")
	freshEmptyDir := filepath.Join(root, "fresh-empty-session")
	oldNestedDir := filepath.Join(oldDir, "nested")
	freshNestedDir := filepath.Join(freshDir, "nested")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatalf("mkdir old session: %v", err)
	}
	if err := os.MkdirAll(freshDir, 0o700); err != nil {
		t.Fatalf("mkdir fresh session: %v", err)
	}
	if err := os.MkdirAll(oldEmptyDir, 0o700); err != nil {
		t.Fatalf("mkdir old empty session: %v", err)
	}
	if err := os.MkdirAll(freshEmptyDir, 0o700); err != nil {
		t.Fatalf("mkdir fresh empty session: %v", err)
	}
	if err := os.MkdirAll(oldNestedDir, 0o700); err != nil {
		t.Fatalf("mkdir old nested dir: %v", err)
	}
	if err := os.MkdirAll(freshNestedDir, 0o700); err != nil {
		t.Fatalf("mkdir fresh nested dir: %v", err)
	}
	oldFile := filepath.Join(oldDir, "tool-call.txt")
	freshFile := filepath.Join(freshDir, "tool-call.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	if err := os.WriteFile(freshFile, []byte("fresh"), 0o600); err != nil {
		t.Fatalf("write fresh file: %v", err)
	}
	oldTime := now.Add(-toolResultRetention - time.Hour)
	freshTime := now.Add(-time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old file: %v", err)
	}
	if err := os.Chtimes(freshFile, freshTime, freshTime); err != nil {
		t.Fatalf("chtimes fresh file: %v", err)
	}
	if err := os.Chtimes(oldNestedDir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old nested dir: %v", err)
	}
	if err := os.Chtimes(freshNestedDir, freshTime, freshTime); err != nil {
		t.Fatalf("chtimes fresh nested dir: %v", err)
	}
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old dir: %v", err)
	}
	if err := os.Chtimes(freshDir, freshTime, freshTime); err != nil {
		t.Fatalf("chtimes fresh dir: %v", err)
	}
	if err := os.Chtimes(oldEmptyDir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old empty dir: %v", err)
	}
	if err := os.Chtimes(freshEmptyDir, freshTime, freshTime); err != nil {
		t.Fatalf("chtimes fresh empty dir: %v", err)
	}

	if err := cleanupExpiredToolResults(root, now, toolResultRetention); err != nil {
		t.Fatalf("cleanup expired tool results: %v", err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("old file should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Fatalf("fresh file should remain: %v", err)
	}
	if _, err := os.Stat(oldNestedDir); !os.IsNotExist(err) {
		t.Fatalf("old nested dir should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshNestedDir); err != nil {
		t.Fatalf("fresh nested dir should remain: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("empty old session dir should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(oldEmptyDir); !os.IsNotExist(err) {
		t.Fatalf("old empty session dir should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshEmptyDir); err != nil {
		t.Fatalf("fresh empty session dir should remain: %v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("fresh non-empty session dir should remain: %v", err)
	}
}
