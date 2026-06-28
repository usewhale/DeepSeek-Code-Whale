package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
)

type regTestTool struct{ name string }

func (t regTestTool) Name() string { return t.name }
func (t regTestTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: "ok"}, nil
}
func (t regTestTool) Description() string { return "desc " + t.name }
func (t regTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
}
func (t regTestTool) ReadOnly() bool { return true }

func TestToolRegistrySpecsAndDispatch(t *testing.T) {
	r := NewToolRegistry([]Tool{regTestTool{name: "read_file"}})
	specs := r.Specs()
	if len(specs) != 1 || specs[0].Name != "read_file" || !specs[0].ReadOnly {
		t.Fatalf("unexpected specs: %+v", specs)
	}
	res, err := r.Dispatch(context.Background(), ToolCall{ID: "tc-1", Name: "read_file"})
	if err != nil {
		t.Fatalf("dispatch err: %v", err)
	}
	if res.IsError() || res.ModelText != "ok" || res.Code != "ok" {
		t.Fatalf("unexpected dispatch result: %+v", res)
	}
}

func TestToolRegistryDispatchNotFound(t *testing.T) {
	r := NewToolRegistry(nil)
	res, err := r.Dispatch(context.Background(), ToolCall{ID: "tc-1", Name: "missing"})
	if err != nil {
		t.Fatalf("dispatch err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "not_found") {
		t.Fatalf("expected not_found error, got %+v", res)
	}
}

type badSpecTool struct{}

func (b badSpecTool) Name() string { return "bad_spec" }
func (b badSpecTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{ToolCallID: call.ID, Name: call.Name, ModelText: "ok"}, nil
}
func (b badSpecTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string"}},
		"required":   []string{"missing"},
	}
}

func TestToolRegistryCheckedInvalidSpecReturnsError(t *testing.T) {
	if _, err := NewToolRegistryChecked([]Tool{badSpecTool{}}); err == nil {
		t.Fatal("expected invalid tool spec error")
	}
}

func TestToolRegistryReplaceToolsKeepsPreviousToolsOnInvalidSpec(t *testing.T) {
	r := NewToolRegistry([]Tool{regTestTool{name: "read_file"}})
	if err := r.ReplaceTools([]Tool{badSpecTool{}}); err == nil {
		t.Fatal("expected invalid tool spec error")
	}
	if got := r.Get("read_file"); got == nil {
		t.Fatal("expected previous tool to remain registered")
	}
	if got := r.Get("bad_spec"); got != nil {
		t.Fatal("invalid replacement should not be registered")
	}
}

type longOutputTool struct{}

func (l longOutputTool) Name() string { return "long_out" }
func (l longOutputTool) Run(_ context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		ModelText:  strings.Repeat("a", 4096),
	}, nil
}

func TestToolRegistryTruncatesLargeResult(t *testing.T) {
	r := NewToolRegistry([]Tool{longOutputTool{}})
	r.SetMaxResultChars(2048)
	res, err := r.Dispatch(context.Background(), ToolCall{ID: "tc-1", Name: "long_out"})
	if err != nil {
		t.Fatalf("dispatch err: %v", err)
	}
	if len(res.ModelText) > 2048 {
		t.Fatalf("truncated result length = %d, want <= 2048: %s", len(res.ModelText), res.ModelText)
	}
	if res.IsError() {
		t.Fatalf("truncated successful result should not be an error: %+v", res)
	}
	if !strings.Contains(res.ModelText, "...[output truncated:") {
		t.Fatalf("truncated result should carry the omission marker, got: %s", res.ModelText)
	}
	if res.Outcome != core.OutcomeSuccess || res.Code != "ok" {
		t.Fatalf("truncated result should preserve success classification, got: %+v", res)
	}
	payload, ok := res.Payload.(map[string]any)
	if !ok || payload["truncated"] != true {
		t.Fatalf("expected bounded truncation payload, got: %#v", res.Payload)
	}
	if payload["head"] == "" || payload["tail"] == "" {
		t.Fatalf("expected truncated head/tail in payload, got: %#v", res.Payload)
	}
	if res.Metadata["output_truncated"] != true {
		t.Fatalf("expected output_truncated metadata, got: %#v", res.Metadata)
	}
}

func TestToolRegistryArchivesLargeResultBeforeTruncation(t *testing.T) {
	r := NewToolRegistry([]Tool{longOutputTool{}})
	r.SetMaxResultChars(2048)
	archiveDir := t.TempDir()
	ctx := core.WithToolResultArchive(context.Background(), archiveDir, "sess/1")

	res, err := r.Dispatch(ctx, ToolCall{ID: "tc/1", Name: "long_out"})
	if err != nil {
		t.Fatalf("dispatch err: %v", err)
	}
	path, _ := res.Metadata["full_result_path"].(string)
	if path == "" {
		t.Fatalf("missing full_result_path metadata: %#v", res.Metadata)
	}
	if !strings.HasPrefix(path, filepath.Join(archiveDir, "sess_1")) {
		t.Fatalf("archive path not scoped to sanitized session dir: %q", path)
	}
	if filepath.Ext(path) != ".txt" {
		t.Fatalf("archive path extension = %q, want .txt", path)
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(full) <= len(res.ModelText) || !strings.Contains(string(full), strings.Repeat("a", 256)) {
		t.Fatalf("archive did not preserve larger normalized payload, archived=%d replay=%d", len(full), len(res.ModelText))
	}
	if res.Metadata["full_result_path"] != path || res.Metadata["output_truncated"] != true {
		t.Fatalf("tool result metadata missing archive path: %+v", res.Metadata)
	}
	for _, want := range []string{
		"Full output saved to: " + path,
		"read_file",
		"offset/limit",
		"grep/search_files",
	} {
		if !strings.Contains(res.ModelText, want) {
			t.Fatalf("truncated model text missing %q:\n%s", want, res.ModelText)
		}
	}
}
