package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
)

type SessionSummary struct {
	ID           string
	ModTime      time.Time
	Size         int64
	Meta         SessionMeta
	Conversation string
}

func ListSessions(sessionsDir string, limit int) ([]SessionSummary, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]SessionSummary, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !core.IsSessionJSONLName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		if id == "" {
			continue
		}
		if IsSubagentSessionID(id) {
			continue
		}
		meta, err := LoadSessionMeta(sessionsDir, id)
		if err == nil && strings.TrimSpace(meta.Kind) == "subagent" {
			continue
		}
		out = append(out, SessionSummary{
			ID:      id,
			ModTime: info.ModTime(),
			Size:    info.Size(),
			Meta:    meta,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		out[i].Conversation = SessionConversationTitle(sessionsDir, out[i].ID, out[i].Meta)
	}
	return out, nil
}

func SessionConversationTitle(sessionsDir, sessionID string, meta SessionMeta) string {
	if title := strings.TrimSpace(meta.Title); title != "" {
		return singleLine(title)
	}
	if title, err := FirstVisibleUserMessage(sessionsDir, sessionID); err == nil && title != "" {
		return title
	}
	return "(no message yet)"
}

func FirstVisibleUserMessage(sessionsDir, sessionID string) (string, error) {
	path := FindSessionPathByID(sessionsDir, sessionID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
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
		var msg struct {
			Role   string
			Text   string
			Parts  []core.MessagePart `json:"parts,omitempty"`
			Hidden bool
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Role != "user" || msg.Hidden {
			continue
		}
		text := msg.Text
		if len(msg.Parts) > 0 {
			text = core.MessagePartsPlainText(msg.Parts)
		}
		if text := strings.TrimSpace(text); text != "" {
			return singleLine(text), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

func singleLine(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func FindSessionPathByID(sessionsDir, sessionID string) string {
	id := core.SanitizeSessionID(sessionID)
	return filepath.Join(sessionsDir, id+".jsonl")
}

// LastMessageUpdatedAt returns the persisted UpdatedAt of the last message in
// the session file — the true last-activity time, immune to file rewrites
// (compaction and session forking rewrite the .jsonl via tmp+rename, which
// bumps the file mtime without changing the last message's timestamp). It
// reads backward from EOF so the cost tracks the last line, not the file
// size; ok=false when the file is empty or the tail is unparseable, in which
// case callers fall back to the file mtime.
func LastMessageUpdatedAt(sessionsDir, sessionID string) (time.Time, bool) {
	path := FindSessionPathByID(sessionsDir, sessionID)
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return time.Time{}, false
	}
	const (
		readChunk = 64 * 1024
		maxLine   = 2 * 1024 * 1024 // same per-line cap as the store scanner
	)
	offset := st.Size()
	var tail []byte
	for len(tail) < maxLine && offset > 0 {
		start := offset - readChunk
		if start < 0 {
			start = 0
		}
		buf := make([]byte, offset-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return time.Time{}, false
		}
		tail = append(buf, tail...)
		if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
			if line := strings.TrimSpace(string(tail[i+1:])); line != "" {
				return parseMessageUpdatedAt(line)
			}
			// Trailing newline: the real last line sits before it.
			tail = tail[:i]
		}
		offset = start
	}
	// No newline (single line), or only a trailing one: parse the last
	// non-empty line we accumulated.
	if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return parseMessageUpdatedAt(strings.TrimSpace(string(tail)))
}

// parseMessageUpdatedAt extracts the persisted UpdatedAt from one session
// JSONL line. Messages are marshaled from core.Message without json tags on
// the timestamp fields, so the keys are the capitalized field names.
func parseMessageUpdatedAt(line string) (time.Time, bool) {
	var msg struct {
		UpdatedAt time.Time `json:"UpdatedAt"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return time.Time{}, false
	}
	if msg.UpdatedAt.IsZero() {
		return time.Time{}, false
	}
	return msg.UpdatedAt, true
}

func IsSubagentSessionID(id string) bool {
	id = strings.TrimSpace(id)
	return strings.Contains(id, "--subagent-") || strings.HasPrefix(id, "subagent-")
}
