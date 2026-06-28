package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type ToolRegistry struct {
	mu              sync.RWMutex
	byName          map[string]Tool
	specs           map[string]ToolSpec
	ordered         []Tool
	providerSchemas *ProviderToolSchemaCache
	maxResultChars  int
}

const DefaultMaxToolResultChars = 32 * 1024

type toolResultArchiveContextKey struct{}

type toolResultArchiveConfig struct {
	Dir       string
	SessionID string
}

func WithToolResultArchive(ctx context.Context, dir, sessionID string) context.Context {
	dir = strings.TrimSpace(dir)
	sessionID = strings.TrimSpace(sessionID)
	if dir == "" || sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, toolResultArchiveContextKey{}, toolResultArchiveConfig{
		Dir:       dir,
		SessionID: sessionID,
	})
}

func ToolResultArchiveSessionDir(dir, sessionID string) string {
	dir = strings.TrimSpace(dir)
	sessionID = strings.TrimSpace(sessionID)
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, sanitizeArchivePathPart(sessionID, "session"))
}

func ArchiveToolResult(ctx context.Context, toolName, toolCallID string, payload []byte) string {
	return archiveToolResult(ctx, toolName, toolCallID, payload)
}

func NewToolRegistry(tools []Tool) *ToolRegistry {
	r, err := NewToolRegistryChecked(tools)
	if err != nil {
		panic(err)
	}
	return r
}

func NewToolRegistryChecked(tools []Tool) (*ToolRegistry, error) {
	r := &ToolRegistry{
		providerSchemas: NewProviderToolSchemaCache(),
		maxResultChars:  DefaultMaxToolResultChars,
	}
	if err := r.replaceToolsLocked(tools); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *ToolRegistry) replaceToolsLocked(tools []Tool) error {
	byName := make(map[string]Tool, len(tools))
	specs := make(map[string]ToolSpec, len(tools))
	ordered := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		name := t.Name()
		if name == "" {
			continue
		}
		if _, ok := byName[name]; !ok {
			ordered = append(ordered, t)
		}
		byName[name] = t
		spec := DescribeTool(t)
		spec.Parameters = normalizeToolSchema(spec.Parameters)
		if !isValidToolSpec(spec) {
			return fmt.Errorf("invalid tool spec for %q", name)
		}
		specs[name] = spec
	}
	r.byName = byName
	r.specs = specs
	r.ordered = ordered
	return nil
}

func (r *ToolRegistry) ReplaceTools(tools []Tool) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.replaceToolsLocked(tools)
}

func (r *ToolRegistry) Snapshot() *ToolRegistry {
	if r == nil {
		return NewToolRegistry(nil)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	byName := make(map[string]Tool, len(r.byName))
	for name, tool := range r.byName {
		byName[name] = tool
	}
	specs := make(map[string]ToolSpec, len(r.specs))
	for name, spec := range r.specs {
		specs[name] = spec
	}
	ordered := append([]Tool(nil), r.ordered...)
	return &ToolRegistry{
		byName:          byName,
		specs:           specs,
		ordered:         ordered,
		providerSchemas: r.providerSchemas,
		maxResultChars:  r.maxResultChars,
	}
}

func (r *ToolRegistry) Get(name string) Tool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byName[name]
}

func (r *ToolRegistry) Tools() []Tool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.ordered))
	for _, t := range r.ordered {
		wrapped := frozenSpecTool{
			tool:            t,
			spec:            r.specs[t.Name()],
			providerSchemas: r.providerSchemas,
		}
		if wrapped.spec.ReadOnlyCheck != nil {
			out = append(out, frozenSpecReadOnlyCheckTool{frozenSpecTool: wrapped})
			continue
		}
		out = append(out, wrapped)
	}
	return out
}

type frozenSpecTool struct {
	tool            Tool
	spec            ToolSpec
	providerSchemas *ProviderToolSchemaCache
}

func (t frozenSpecTool) Name() string {
	return t.spec.Name
}

func (t frozenSpecTool) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	return t.tool.Run(ctx, call)
}

func (t frozenSpecTool) RunWithProgress(ctx context.Context, call ToolCall, progress func(ToolProgress)) (ToolResult, error) {
	if runner, ok := t.tool.(ToolProgressRunner); ok {
		return runner.RunWithProgress(ctx, call, progress)
	}
	return t.tool.Run(ctx, call)
}

func (t frozenSpecTool) Preview(ctx context.Context, call ToolCall) (map[string]any, error) {
	if previewer, ok := t.tool.(ToolPreviewer); ok {
		return previewer.Preview(ctx, call)
	}
	return nil, nil
}

func (t frozenSpecTool) Description() string {
	return t.spec.Description
}

func (t frozenSpecTool) Parameters() map[string]any {
	return cloneSchemaMap(t.spec.Parameters)
}

func (t frozenSpecTool) ProviderToolPayload() map[string]any {
	return providerToolPayloadFromSpec(t.providerSchemas, t.spec)
}

func (t frozenSpecTool) ReadOnly() bool {
	return t.spec.ReadOnly
}

func (t frozenSpecTool) Capabilities() []string {
	return append([]string(nil), t.spec.Capabilities...)
}

func (t frozenSpecTool) ApprovalHint() string {
	return t.spec.ApprovalHint
}

func (t frozenSpecTool) SupportsParallel() bool {
	return t.spec.SupportsParallel
}

type frozenSpecReadOnlyCheckTool struct {
	frozenSpecTool
}

func (t frozenSpecReadOnlyCheckTool) ReadOnlyCheck(args map[string]any) bool {
	return t.spec.ReadOnlyCheck(args)
}

func (r *ToolRegistry) Specs() []ToolSpec {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.ordered))
	for _, t := range r.ordered {
		out = append(out, r.specs[t.Name()])
	}
	return out
}

func (r *ToolRegistry) Spec(name string) (ToolSpec, bool) {
	if r == nil {
		return ToolSpec{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[name]
	return spec, ok
}

func (r *ToolRegistry) SetMaxResultChars(limit int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxResultChars = limit
}

func (r *ToolRegistry) Dispatch(ctx context.Context, call ToolCall) (ToolResult, error) {
	return r.DispatchWithProgress(ctx, call, nil)
}

func (r *ToolRegistry) DispatchWithProgress(ctx context.Context, call ToolCall, progress func(ToolProgress)) (ToolResult, error) {
	start := time.Now()
	var (
		spec           ToolSpec
		hasSpec        bool
		tool           Tool
		maxResultChars int
	)
	if r != nil {
		r.mu.RLock()
		spec, hasSpec = r.specs[call.Name]
		tool = r.byName[call.Name]
		maxResultChars = r.maxResultChars
		r.mu.RUnlock()
	}
	if tool == nil {
		return normalizeRegistryResult(ctx, call, ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			ModelText:  `{"ok":false,"error":"tool not found","code":"not_found"}`,
			Code:       "not_found",
		}, maxResultChars, time.Since(start).Milliseconds()), nil
	}
	if hasSpec {
		if err := validateToolInput(spec.Parameters, call.Input); err != nil {
			return normalizeRegistryResult(ctx, call, ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				ModelText:  invalidToolInputContent(call.Name, spec.Parameters, err),
				Code:       "invalid_input",
			}, maxResultChars, time.Since(start).Milliseconds()), nil
		}
	}
	var res ToolResult
	var err error
	if runner, ok := tool.(ToolProgressRunner); ok {
		res, err = runner.RunWithProgress(ctx, call, progress)
	} else {
		res, err = tool.Run(ctx, call)
	}
	if err != nil {
		code := "exec_failed"
		if errors.Is(err, context.Canceled) {
			code = "cancelled"
		}
		return normalizeRegistryResult(ctx, call, ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			ModelText:  fmt.Sprintf(`{"ok":false,"error":%q,"code":%q}`, err.Error(), code),
			Code:       code,
		}, maxResultChars, time.Since(start).Milliseconds()), nil
	}
	return normalizeRegistryResult(ctx, call, res, maxResultChars, time.Since(start).Milliseconds()), nil
}

func invalidToolInputContent(toolName string, parameters map[string]any, err error) string {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	env := ToolEnvelope{
		OK:      false,
		Success: false,
		Code:    "invalid_input",
		Error:   msg,
	}
	hint, ok := ToolInputRecoveryHint(toolName, msg)
	if !ok {
		hint, ok = schemaFieldRecoveryHint(toolName, msg, parameters)
	}
	if ok {
		env.Summary = hint
		env.Data = map[string]any{"recovery": hint}
	}
	content, marshalErr := MarshalToolEnvelope(env)
	if marshalErr != nil {
		return fmt.Sprintf(`{"ok":false,"error":%q,"code":"invalid_input"}`, msg)
	}
	return content
}

func ToolInputRecoveryHint(toolName, msg string) (string, bool) {
	toolName = strings.TrimSpace(toolName)
	msg = strings.TrimSpace(msg)
	switch {
	case toolName == "search_files" && msg == `unknown field "include"`:
		return "search_files has no include field; put the glob directly in pattern (e.g. pattern=**/*.go), or use grep with include to search file contents.", true
	case toolName == "search_files" && msg == `missing required field "pattern"`:
		return "search_files requires pattern; provide pattern and path, or use grep for content search.", true
	case (toolName == "grep" || toolName == "search_content") && msg == `missing required field "pattern"`:
		return "grep requires pattern (a regex); provide pattern and optionally include/path, or use search_files to find file names.", true
	case (toolName == "fetch" || toolName == "web_fetch") && msg == `unknown field "max_results"`:
		return toolName + " does not support max_results; remove it or use web_search when you need multiple search results.", true
	case (toolName == "fetch" || toolName == "web_fetch") && msg == `unknown field "format"`:
		return toolName + " does not support format; omit it and use prompt to request the output shape.", true
	case (toolName == "fetch" || toolName == "web_fetch") && (strings.Contains(msg, "url scheme must be http or https") || msg == "valid url is required"):
		return toolName + " only supports http/https URLs; use read_file for local file paths or tool result files.", true
	}
	return "", false
}

// schemaFieldRecoveryHint derives a recovery hint from the tool's own schema for
// the generic "unknown field"/"missing required field" validation errors, so any
// tool gets actionable guidance (the wrong field plus the list of valid ones)
// without needing a hand-written entry in ToolInputRecoveryHint.
func schemaFieldRecoveryHint(toolName, msg string, parameters map[string]any) (string, bool) {
	tool := strings.TrimSpace(toolName)
	if tool == "" {
		tool = "this tool"
	}
	if field, ok := quotedFieldAfter(msg, "unknown field "); ok {
		if names := schemaParameterNames(parameters); len(names) > 0 {
			return fmt.Sprintf("%s is not a parameter of %s; supported parameters: %s.", field, tool, strings.Join(names, ", ")), true
		}
		return fmt.Sprintf("%s is not a parameter of %s; remove it.", field, tool), true
	}
	if field, ok := quotedFieldAfter(msg, "missing required field "); ok {
		if names := schemaParameterNames(parameters); len(names) > 0 {
			return fmt.Sprintf("%s requires the %s field; supported parameters: %s.", tool, field, strings.Join(names, ", ")), true
		}
		return fmt.Sprintf("%s requires the %s field.", tool, field), true
	}
	return "", false
}

// schemaParameterNames returns the sorted property names declared by a tool
// schema, or nil when the schema declares none. Sorting keeps the hint stable
// across runs (Go map iteration order is randomized).
func schemaParameterNames(parameters map[string]any) []string {
	props, _ := parameters["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// quotedFieldAfter extracts the double-quoted field name immediately following
// prefix in msg (e.g. `unknown field "foo"` -> "foo").
func quotedFieldAfter(msg, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(msg), prefix)
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 || rest[0] != '"' {
		return "", false
	}
	rest = rest[1:]
	end := strings.IndexByte(rest, '"')
	if end <= 0 {
		return "", false
	}
	return rest[:end], true
}

func normalizeRegistryResult(ctx context.Context, call ToolCall, res ToolResult, maxResultChars int, durationMS int64) ToolResult {
	if maxResultChars <= 0 {
		maxResultChars = DefaultMaxToolResultChars
	}
	// The tool's raw output (envelope or plain text) arrives in ModelText;
	// the normalize funnel re-derives every channel from it. Tool-level
	// error-ness flows through the envelope itself (ok/success flags), so
	// the fallback flag only matters for non-envelope output.
	content, isErr, archivePath, env, boundedPayload := normalizeToolContent(ctx, call.Name, call.ID, res.ModelText, res.IsError(), maxResultChars, durationMS)
	res.ToolCallID = call.ID
	res.Name = call.Name
	res.ModelText = content
	res.Code = env.Code
	if isErr {
		res.Outcome = OutcomeForErrorCode(env.Code)
	} else {
		res.Outcome = OutcomeSuccess
	}
	res.Payload = CanonicalizeToolPayload(env)
	if boundedPayload != nil {
		// The rendered text was truncated; keep the structured channel
		// bounded too so large results are not persisted twice.
		res.Payload = boundedPayload
		if res.Metadata == nil {
			res.Metadata = map[string]any{}
		}
		res.Metadata["output_truncated"] = true
		if archivePath != "" {
			res.Metadata["full_result_path"] = archivePath
		}
	}
	return res
}

func normalizeToolContent(ctx context.Context, toolName, toolCallID, raw string, fallbackErr bool, maxResultChars int, durationMS int64) (string, bool, string, ToolEnvelope, map[string]any) {
	env := ToolEnvelope{
		OK:        !fallbackErr,
		Success:   !fallbackErr,
		Code:      "ok",
		Data:      map[string]any{},
		Truncated: false,
		Metadata: map[string]any{
			"source_tool": toolName,
			"duration_ms": durationMS,
		},
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			if v, ok := parsed["ok"].(bool); ok {
				env.OK = v
			} else if v, ok := parsed["success"].(bool); ok {
				env.OK = v
			}
			if v, ok := parsed["success"].(bool); ok {
				env.Success = v
			} else {
				env.Success = env.OK
			}
			if v, ok := parsed["code"].(string); ok && strings.TrimSpace(v) != "" {
				env.Code = v
			}
			if v, ok := parsed["error"].(string); ok {
				env.Error = v
			} else if v, ok := parsed["message"].(string); ok {
				env.Error = v
			}
			if v, ok := parsed["summary"].(string); ok {
				env.Summary = v
			}
			if v, ok := parsed["data"]; ok {
				if data, ok := v.(map[string]any); ok {
					env.Data = data
				} else {
					env.Data = map[string]any{"payload": v}
				}
			} else {
				delete(parsed, "ok")
				delete(parsed, "success")
				delete(parsed, "code")
				delete(parsed, "message")
				delete(parsed, "error")
				delete(parsed, "summary")
				delete(parsed, "truncated")
				delete(parsed, "meta")
				delete(parsed, "metadata")
				if len(parsed) > 0 {
					env.Data = parsed
				}
			}
			if tv, ok := parsed["truncated"].(bool); ok {
				env.Truncated = tv
			}
			if mv, ok := parsed["meta"].(map[string]any); ok {
				for k, v := range mv {
					env.Metadata[k] = v
				}
			}
			if mv, ok := parsed["metadata"].(map[string]any); ok {
				for k, v := range mv {
					env.Metadata[k] = v
				}
			}
		} else {
			env.Data = map[string]any{"text": raw}
		}
	}
	if strings.TrimSpace(env.Summary) == "" {
		env.Summary = deriveSummary(env.Data, env.Error)
	}
	if trunc, ok := inferTruncated(env.Data, env.Metadata); ok {
		env.Truncated = trunc
	}
	env.Success = env.OK
	// Phase 2: the model-visible text is rendered plain (codex-style)
	// from the canonical payload instead of re-serializing the envelope.
	payload := CanonicalizeToolPayload(env)
	outcome := OutcomeSuccess
	if !env.OK {
		outcome = OutcomeForErrorCode(env.Code)
	}
	text := RenderToolResultText(toolName, outcome, env.Code, payload)
	if maxResultChars > 0 && len(text) > maxResultChars {
		archivePath := archiveToolResult(ctx, toolName, toolCallID, []byte(text))
		// The bounded payload must capture the FULL rendered text (head/
		// tail and original size) before the model text is truncated.
		bounded := BoundedTruncationPayload(text, len(text), env.Code, archivePath)
		text = RenderTruncatedToolText(text, maxResultChars, archivePath)
		return text, !env.OK, archivePath, env, bounded
	}
	return text, !env.OK, "", env, nil
}

var archivePathSanitizer = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func archiveToolResult(ctx context.Context, toolName, toolCallID string, payload []byte) string {
	cfg, ok := ctx.Value(toolResultArchiveContextKey{}).(toolResultArchiveConfig)
	if !ok || strings.TrimSpace(cfg.Dir) == "" || strings.TrimSpace(cfg.SessionID) == "" || len(payload) == 0 {
		return ""
	}
	callID := sanitizeArchivePathPart(toolCallID, "tool-call")
	tool := sanitizeArchivePathPart(toolName, "tool")
	sum := sha256.Sum256(payload)
	name := fmt.Sprintf("%s-%s-%s.txt", tool, callID, hex.EncodeToString(sum[:8]))
	dir := ToolResultArchiveSessionDir(cfg.Dir, cfg.SessionID)
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return ""
	}
	return path
}

func sanitizeArchivePathPart(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	value = archivePathSanitizer.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	if value == "" {
		return fallback
	}
	if len(value) > 96 {
		return value[:96]
	}
	return value
}

func splitToolResultBudget(maxResultChars int) (int, int) {
	if maxResultChars <= 0 {
		return 0, 0
	}
	tailBudget := min(1024, maxResultChars/10)
	headBudget := max(0, maxResultChars-tailBudget)
	return headBudget, tailBudget
}

func deriveSummary(data any, errMsg string) string {
	if strings.TrimSpace(errMsg) != "" {
		return clipSummary(errMsg, 220)
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return "tool completed"
	}
	if s, ok := obj["summary"].(string); ok && strings.TrimSpace(s) != "" {
		return clipSummary(s, 220)
	}
	if p, ok := obj["payload"].(map[string]any); ok {
		if s, ok := p["stdout"].(string); ok && strings.TrimSpace(s) != "" {
			return clipSummary(s, 220)
		}
	}
	if c, ok := obj["content"].(string); ok && strings.TrimSpace(c) != "" {
		return clipSummary(c, 220)
	}
	return "tool completed"
}

func inferTruncated(data any, metadata any) (bool, bool) {
	if md, ok := metadata.(map[string]any); ok {
		if t, ok := md["truncated"].(bool); ok {
			return t, true
		}
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return false, false
	}
	if t, ok := obj["truncated"].(bool); ok {
		return t, true
	}
	if m, ok := obj["metrics"].(map[string]any); ok {
		if t, ok := m["stdout_truncation"].(map[string]any); ok {
			if b, ok := t["truncated"].(bool); ok && b {
				return true, true
			}
		}
	}
	return false, false
}

func clipSummary(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return strings.TrimSpace(s[:limit]) + "..."
}

func isValidToolSpec(spec ToolSpec) bool {
	if spec.Name == "" || spec.Parameters == nil {
		return false
	}
	props := map[string]any{}
	if propsAny, ok := spec.Parameters["properties"]; ok {
		p, ok := propsAny.(map[string]any)
		if !ok {
			return false
		}
		props = p
	}
	requiredAny, hasReq := spec.Parameters["required"]
	if !hasReq {
		return true
	}
	req, ok := coerceStringSlice(requiredAny)
	if !ok {
		return false
	}
	for _, key := range req {
		if _, ok := props[key]; !ok {
			return false
		}
	}
	return true
}

func validateToolInput(parameters map[string]any, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return fmt.Errorf("input must be valid JSON object: %w", err)
	}
	propsAny, _ := parameters["properties"]
	props, _ := propsAny.(map[string]any)
	requiredAny, hasReq := parameters["required"]
	if hasReq {
		req, ok := coerceStringSlice(requiredAny)
		if !ok {
			return fmt.Errorf("schema required must be []string")
		}
		for _, k := range req {
			if _, ok := input[k]; !ok {
				return fmt.Errorf("missing required field %q", k)
			}
		}
	}
	ap, hasAP := parameters["additionalProperties"].(bool)
	if hasAP && !ap {
		for k := range input {
			if _, ok := props[k]; ok {
				continue
			}
			// "description" is a reserved, benign metadata field: models
			// routinely attach a human-readable label to tool calls (a habit
			// from other harnesses). Tolerate it on any tool instead of
			// failing the call, even when the tool does not declare it.
			if k == reservedDescriptionField {
				continue
			}
			return fmt.Errorf("unknown field %q", k)
		}
	}
	return nil
}

// reservedDescriptionField is accepted on every tool call and ignored by
// execution. Models routinely attach this human-readable label to tool calls
// (a habit from other harnesses), so we tolerate it on every tool instead of
// failing the call, even when the tool does not declare it in its schema.
const reservedDescriptionField = "description"

func normalizeToolSchema(parameters map[string]any) map[string]any {
	if parameters == nil {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": true,
		}
	}
	if _, ok := parameters["type"]; !ok {
		parameters["type"] = "object"
	}
	if _, ok := parameters["properties"]; !ok {
		parameters["properties"] = map[string]any{}
	}
	if _, ok := parameters["additionalProperties"]; !ok {
		parameters["additionalProperties"] = true
	}
	return parameters
}

func cloneSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneSchemaValue(v)
	}
	return out
}

func cloneSchemaValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneSchemaMap(x)
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, cloneSchemaValue(item))
		}
		return out
	case []string:
		return append([]string(nil), x...)
	case []int:
		return append([]int(nil), x...)
	case []float64:
		return append([]float64(nil), x...)
	case []bool:
		return append([]bool(nil), x...)
	default:
		return x
	}
}

func coerceStringSlice(v any) ([]string, bool) {
	if s, ok := v.([]string); ok {
		return s, true
	}
	if raw, ok := v.([]any); ok {
		out := make([]string, 0, len(raw))
		for _, it := range raw {
			str, ok := it.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	}
	return nil, false
}
