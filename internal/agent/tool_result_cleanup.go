package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const toolResultRetention = 7 * 24 * time.Hour

var toolResultCleanupLoops sync.Map

func startToolResultCleanupLoop(ctx context.Context, root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	root = filepath.Clean(root)
	if _, loaded := toolResultCleanupLoops.LoadOrStore(root, struct{}{}); loaded {
		return
	}
	go func() {
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				toolResultCleanupLoops.Delete(root)
				return
			case <-timer.C:
			}
			if err := cleanupExpiredToolResults(root, time.Now(), toolResultRetention); err != nil {
				slog.Warn("tool result cleanup failed", "root", root, "error", err)
			}
			timer.Reset(time.Hour)
		}
	}()
}

func cleanupExpiredToolResults(root string, now time.Time, retention time.Duration) error {
	root = strings.TrimSpace(root)
	if root == "" || retention <= 0 {
		return nil
	}
	cutoff := now.Add(-retention)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(root, entry.Name())
		sessionInfo, statErr := entry.Info()
		files, err := os.ReadDir(sessionDir)
		if err != nil {
			continue
		}
		for _, file := range files {
			path := filepath.Join(sessionDir, file.Name())
			info, err := file.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if info.IsDir() {
				if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
					slog.Warn("failed to remove expired tool result directory", "path", path, "error", err)
				}
				continue
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Warn("failed to remove expired tool result", "path", path, "error", err)
			}
		}
		if statErr == nil {
			removeExpiredEmptyDir(sessionDir, sessionInfo, cutoff)
		}
	}
	return nil
}

func removeExpiredEmptyDir(path string, info os.FileInfo, cutoff time.Time) {
	if info == nil || !info.IsDir() || info.ModTime().After(cutoff) {
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to inspect tool result session directory", "path", path, "error", err)
		}
		return
	}
	if len(entries) > 0 {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove empty tool result session directory", "path", path, "error", err)
	}
}
