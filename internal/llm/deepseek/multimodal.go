package deepseek

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/usewhale/whale/internal/compact"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/llm"
)

func latestUserMessageHasAttachments(history []core.Message) bool {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != core.RoleUser {
			continue
		}
		if msg.Hidden {
			if i > 0 && history[i-1].Role == core.RoleUser && !history[i-1].Hidden {
				return messageHasAttachments(history[i-1])
			}
			return false
		}
		return messageHasAttachments(msg)
	}
	return false
}

func messageHasAttachments(msg core.Message) bool {
	for _, part := range msg.Parts {
		if part.Type == core.MessagePartAttachment && part.Attachment != nil {
			return true
		}
	}
	return false
}

func (c *Client) streamMultimodal(ctx context.Context, history []core.Message, tools []core.Tool, out chan<- llm.ProviderEvent) error {
	cfg, err := c.resolvedMultimodalConfig()
	if err != nil {
		return err
	}
	msgs, err := toOpenAICompatibleMessages(history)
	if err != nil {
		return err
	}
	msgs, sanitizeDiag := sanitizeDeepSeekMessagesForRequest(msgs, c.thinkingEnabled)
	stripReasoningContent(msgs)
	replayDiag := toolResultReplayDiagnostics(history, msgs)
	payload := map[string]any{
		"model":          cfg.Model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       msgs,
	}
	if len(tools) > 0 {
		payload["tools"] = toDeepSeekTools(tools)
	}
	if c.maxTokens > 0 {
		payload["max_tokens"] = c.maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal multimodal payload: %w", err)
	}
	return c.streamWithRetriesAuth(ctx, cfg.BaseURL, cfg.APIKey, cfg.Model, body, msgs, sanitizeDiag, replayDiag, out)
}

func stripReasoningContent(messages []map[string]any) {
	for _, msg := range messages {
		delete(msg, "reasoning_content")
	}
}

func (c *Client) resolvedMultimodalConfig() (MultimodalConfig, error) {
	cfg := c.multimodal
	if !cfg.Enabled {
		return MultimodalConfig{}, fmt.Errorf("multimodal attachments require [providers.deepseek.multimodal].enabled = true")
	}
	if cfg.Compat == "" {
		cfg.Compat = "openai"
	}
	if cfg.Compat != "openai" {
		return MultimodalConfig{}, fmt.Errorf("unsupported multimodal compat %q", cfg.Compat)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = c.baseURL
	}
	if cfg.APIKey == "" && cfg.APIKeyEnv != "" {
		cfg.APIKey = strings.TrimSpace(os.Getenv(cfg.APIKeyEnv))
		if cfg.APIKey == "" {
			return MultimodalConfig{}, fmt.Errorf("multimodal API key env %s is not set", cfg.APIKeyEnv)
		}
	}
	if cfg.APIKey == "" {
		cfg.APIKey = c.apiKey
	}
	if cfg.Model == "" {
		cfg.Model = c.model
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return MultimodalConfig{}, fmt.Errorf("multimodal API key is not configured")
	}
	return cfg, nil
}

func toOpenAICompatibleMessages(history []core.Message) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(history))
	pendingToolCalls := map[string]struct{}{}
	flushPending := func() {
		for id := range pendingToolCalls {
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": id,
				"content":      `{"success":false,"error":"missing tool result recovered before provider send","code":"missing_tool_result_recovered"}`,
			})
			delete(pendingToolCalls, id)
		}
	}
	for _, msg := range history {
		switch msg.Role {
		case core.RoleSystem:
			flushPending()
			out = append(out, map[string]any{"role": "system", "content": core.MessagePlainText(msg)})
		case core.RoleUser:
			flushPending()
			content, err := openAIUserContent(msg)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"role": "user", "content": content})
		case core.RoleAssistant:
			flushPending()
			m := map[string]any{
				"role":              "assistant",
				"content":           core.MessagePlainText(msg),
				"reasoning_content": msg.Reasoning,
			}
			if len(msg.ToolCalls) > 0 {
				tcs := make([]map[string]any, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							// Replay prior tool calls under the model-facing name
							// so history stays consistent with the tool schema.
							"name":      core.DisplayToolName(tc.Name),
							"arguments": tc.Input,
						},
					})
					if strings.TrimSpace(tc.ID) != "" {
						pendingToolCalls[tc.ID] = struct{}{}
					}
				}
				m["tool_calls"] = tcs
			}
			out = append(out, m)
		case core.RoleTool:
			for _, tr := range msg.ToolResults {
				if _, ok := pendingToolCalls[tr.ToolCallID]; !ok {
					continue
				}
				out = append(out, map[string]any{
					"role":         "tool",
					"tool_call_id": tr.ToolCallID,
					"content":      compact.ToolResultReplayContent(core.ToolResultModelText(tr)),
				})
				delete(pendingToolCalls, tr.ToolCallID)
			}
		}
	}
	flushPending()
	return out, nil
}

func openAIUserContent(msg core.Message) (any, error) {
	if len(msg.Parts) == 0 {
		return msg.Text, nil
	}
	parts := make([]map[string]any, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch part.Type {
		case core.MessagePartText:
			if part.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			}
		case core.MessagePartAttachment:
			encoded, err := encodeOpenAIAttachment(part.Attachment)
			if err != nil {
				return nil, err
			}
			parts = append(parts, encoded)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return parts, nil
}

func encodeOpenAIAttachment(att *core.AttachmentRef) (map[string]any, error) {
	if att == nil {
		return nil, fmt.Errorf("attachment reference is missing")
	}
	path := strings.TrimSpace(att.Path)
	if path == "" {
		return nil, fmt.Errorf("attachment %q has no stored path", attachmentDisplayName(att))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read attachment %q: %w", attachmentDisplayName(att), err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	mime := strings.TrimSpace(att.MIME)
	if mime == "" {
		mime = "application/octet-stream"
	}
	switch att.Kind {
	case core.AttachmentKindImage:
		return map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url":    dataURL(mime, encoded),
				"detail": defaultImageDetail,
			},
		}, nil
	case core.AttachmentKindPDF, core.AttachmentKindFile:
		return map[string]any{
			"type": "file",
			"file": map[string]any{
				"filename":  attachmentFilename(att),
				"file_data": dataURL(mime, encoded),
			},
		}, nil
	case core.AttachmentKindAudio:
		format, err := openAIAudioFormat(att)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type": "input_audio",
			"input_audio": map[string]any{
				"data":   encoded,
				"format": format,
			},
		}, nil
	case core.AttachmentKindVideo:
		return map[string]any{
			"type": "video_url",
			"video_url": map[string]any{
				"url": dataURL(mime, encoded),
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported attachment kind %q", att.Kind)
	}
}

func openAIAudioFormat(att *core.AttachmentRef) (string, error) {
	mime := strings.ToLower(strings.TrimSpace(att.MIME))
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(attachmentFilename(att)), "."))
	switch {
	case mime == "audio/mpeg" || ext == "mp3":
		return "mp3", nil
	case mime == "audio/wav" || mime == "audio/wave" || mime == "audio/x-wav" || ext == "wav":
		return "wav", nil
	default:
		return "", fmt.Errorf("audio attachment %q uses unsupported OpenAI-compatible format %q", attachmentDisplayName(att), core.FirstNonEmpty(mime, ext))
	}
}

func dataURL(mime, encoded string) string {
	return "data:" + mime + ";base64," + encoded
}

func attachmentFilename(att *core.AttachmentRef) string {
	if name := strings.TrimSpace(att.Filename); name != "" {
		return name
	}
	if name := strings.TrimSpace(att.DisplayName); name != "" {
		return name
	}
	if path := strings.TrimSpace(att.Path); path != "" {
		return filepath.Base(path)
	}
	return "attachment"
}

func attachmentDisplayName(att *core.AttachmentRef) string {
	if name := strings.TrimSpace(att.DisplayName); name != "" {
		return name
	}
	return attachmentFilename(att)
}
