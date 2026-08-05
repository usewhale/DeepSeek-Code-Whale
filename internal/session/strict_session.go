package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

type StrictSession struct {
	ID       string
	Meta     SessionMeta
	Mode     Mode
	Empty    bool
	Messages int
}

func ResolveStrictSession(sessionsDir, rawID string) (StrictSession, error) {
	trimmed := strings.TrimSpace(rawID)
	if trimmed == "" {
		return StrictSession{}, fmt.Errorf("session id is required")
	}
	if core.SanitizeSessionID(trimmed) != trimmed {
		return StrictSession{}, fmt.Errorf("invalid session id %q: unsupported characters", trimmed)
	}
	if isSubagentSessionID(trimmed) {
		return StrictSession{}, fmt.Errorf("session %q is a subagent session and cannot be resumed directly", trimmed)
	}
	path := filepath.Join(sessionsDir, trimmed+".jsonl")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StrictSession{}, fmt.Errorf("session %q does not exist", trimmed)
		}
		return StrictSession{}, fmt.Errorf("stat session %q: %w", trimmed, err)
	}
	if info.IsDir() {
		return StrictSession{}, fmt.Errorf("session %q is not a session file", trimmed)
	}
	meta, err := LoadSessionMeta(sessionsDir, trimmed)
	if err != nil {
		return StrictSession{}, err
	}
	if strings.TrimSpace(meta.Kind) == "subagent" {
		return StrictSession{}, fmt.Errorf("session %q is a subagent session and cannot be resumed directly", trimmed)
	}
	mode, err := LoadModeState(sessionsDir, trimmed)
	if err != nil {
		return StrictSession{}, err
	}
	st := StrictSession{ID: trimmed, Meta: meta, Mode: mode.Mode}
	if info.Size() == 0 {
		st.Empty = true
		return st, nil
	}
	count, canonical, err := scanSessionFile(path)
	if err != nil {
		return StrictSession{}, err
	}
	if canonical != "" && canonical != trimmed {
		return StrictSession{}, fmt.Errorf("session file %q exposes canonical id %q", trimmed, canonical)
	}
	if count == 0 {
		return StrictSession{}, fmt.Errorf("session %q is not recoverable", trimmed)
	}
	st.Messages = count
	return st, nil
}

func scanSessionFile(path string) (valid int, canonical string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("open session: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m core.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		valid++
		if canonical == "" && strings.TrimSpace(m.SessionID) != "" {
			canonical = strings.TrimSpace(m.SessionID)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, "", fmt.Errorf("scan session: %w", err)
	}
	return valid, canonical, nil
}
