package policy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy/effects"
	"github.com/usewhale/whale/internal/policy/shellrisk"
)

type ApprovalRequest struct {
	SessionID string
	ToolCall  core.ToolCall
	Spec      core.ToolSpec
	Reason    string
	Code      string
	Key       string
	Keys      []string
	Metadata  map[string]any
}

type ApprovalDecision int

const (
	ApprovalDeny ApprovalDecision = iota
	ApprovalAllow
	ApprovalAllowForSession
	ApprovalCancel
)

func (d ApprovalDecision) Approved() bool {
	return d == ApprovalAllow || d == ApprovalAllowForSession
}

func (d ApprovalDecision) ForSession() bool {
	return d == ApprovalAllowForSession
}

func (d ApprovalDecision) Canceled() bool {
	return d == ApprovalCancel
}

type ApprovalFunc func(req ApprovalRequest) ApprovalDecision

type SessionApprovalCache struct {
	mu     sync.RWMutex
	data   map[string]map[string]bool
	loaded map[string]bool
}

func NewSessionApprovalCache() *SessionApprovalCache {
	return &SessionApprovalCache{
		data:   make(map[string]map[string]bool),
		loaded: make(map[string]bool),
	}
}

func (c *SessionApprovalCache) Has(sessionID, key string) bool {
	return c.HasAll(sessionID, []string{key})
}

func (c *SessionApprovalCache) HasAll(sessionID string, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	bySession, ok := c.data[sessionID]
	if !ok {
		return false
	}
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || !approvalKeyGranted(bySession, key) {
			return false
		}
	}
	return true
}

func approvalKeyGranted(granted map[string]bool, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	if granted[key] {
		return true
	}
	for grantedKey, allowed := range granted {
		if !allowed {
			continue
		}
		if effects.GrantAllowsKey(grantedKey, key) {
			return true
		}
		if legacyExternalReadGrantAllows(grantedKey, key) {
			return true
		}
	}
	return false
}

func (c *SessionApprovalCache) Grant(sessionID, key string) {
	c.GrantAll(sessionID, []string{key})
}

func (c *SessionApprovalCache) GrantAll(sessionID string, keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bySession, ok := c.data[sessionID]
	if !ok {
		bySession = make(map[string]bool)
		c.data[sessionID] = bySession
	}
	for _, key := range keys {
		if strings.TrimSpace(key) != "" {
			bySession[key] = true
		}
	}
}

func (c *SessionApprovalCache) SetLoaded(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded[sessionID] = true
}

func (c *SessionApprovalCache) IsLoaded(sessionID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded[sessionID]
}

func (c *SessionApprovalCache) Merge(sessionID string, keys map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bySession, ok := c.data[sessionID]
	if !ok {
		bySession = make(map[string]bool)
		c.data[sessionID] = bySession
	}
	for k, v := range keys {
		if v {
			bySession[k] = true
		}
	}
}

func ApprovalKey(call core.ToolCall) string {
	keys := ApprovalKeys(call)
	if len(keys) > 0 {
		return keys[0]
	}
	return call.Name + "|" + strings.TrimSpace(call.Input)
}

func ApprovalKeys(call core.ToolCall) []string {
	base := call.Name
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return []string{base + "|" + strings.TrimSpace(call.Input)}
	}
	switch call.Name {
	case "edit", "write", "multi_edit":
		if v, _ := body["file_path"].(string); strings.TrimSpace(v) != "" {
			return []string{approvalFileKey(v)}
		}
	case "shell_run":
		if v, _ := body["command"].(string); strings.TrimSpace(v) != "" {
			return ShellApprovalKeys(v)
		}
	case "write_stdin":
		return []string{WriteStdinApprovalKey(body)}
	case "web_search":
		return []string{"web_search:*"}
	case "fetch", "web_fetch":
		if host := approvalURLHost(body); host != "" {
			return []string{"web_fetch:host:" + host}
		}
	case "remember":
		scope, _ := body["scope"].(string)
		name, _ := body["name"].(string)
		scope = strings.ToLower(strings.TrimSpace(scope))
		name = strings.TrimSpace(name)
		if scope != "" && name != "" {
			return []string{fmt.Sprintf("memory:%s:%s:%s:%s", base, scope, name, memoryWritePayloadHash(body))}
		}
	case "forget":
		scope, _ := body["scope"].(string)
		name, _ := body["name"].(string)
		scope = strings.ToLower(strings.TrimSpace(scope))
		name = strings.TrimSpace(name)
		if scope != "" && name != "" {
			return []string{fmt.Sprintf("memory:%s:%s:%s", base, scope, name)}
		}
	}
	return []string{base + "|" + strings.TrimSpace(call.Input)}
}

func ApprovalKeysForDecision(call core.ToolCall, decision PolicyDecision) []string {
	if !externalDirectoryGrantEligible(call) {
		return ApprovalKeys(call)
	}
	var keys []string
	for _, req := range decision.ApprovalRequirements {
		if req.Permission == "external_directory" && strings.TrimSpace(req.Pattern) != "" {
			keys = append(keys, externalDirectoryGrantKey(req.Pattern))
		}
	}
	if decision.Permission == "external_directory" && strings.TrimSpace(decision.Pattern) != "" {
		keys = append(keys, externalDirectoryGrantKey(decision.Pattern))
	}
	if len(keys) > 0 {
		if DecisionRequiresNonExternalApproval(decision) {
			keys = append(keys, ApprovalKeys(call)...)
		}
		return compactApprovalKeys(keys)
	}
	return ApprovalKeys(call)
}

func DecisionRequiresNonExternalApproval(decision PolicyDecision) bool {
	for _, req := range decision.ApprovalRequirements {
		if strings.TrimSpace(req.Permission) != "" && req.Permission != "external_directory" {
			return true
		}
	}
	return false
}

func ExternalReadApprovalRootsFromKeys(keys []string) []string {
	var roots []string
	for _, key := range compactApprovalKeys(keys) {
		if grant, ok := effects.ParseGrantKey(key); ok && grant.Kind == effects.ExternalDirectory && strings.TrimSpace(grant.Pattern) != "" {
			roots = append(roots, filepath.Clean(filepath.FromSlash(grant.Pattern)))
			continue
		}
		if root, ok := legacyExternalReadRoot(key); ok {
			roots = append(roots, root)
		}
	}
	return roots
}

func ExternalReadApprovalRootsForDecision(call core.ToolCall, decision PolicyDecision) []string {
	if !readOnlyFilesystemTool(call.Name) {
		return nil
	}
	roots := ExternalReadApprovalRootsFromKeys(ApprovalKeysForDecision(call, decision))
	for _, req := range decision.AllowedRequirements {
		if req.Permission == "external_directory" && strings.TrimSpace(req.Pattern) != "" {
			roots = append(roots, filepath.Clean(req.Pattern))
		}
	}
	return uniqueStrings(roots)
}

func readOnlyFilesystemTool(name string) bool {
	switch name {
	case "read_file", "list_dir", "grep", "search_files":
		return true
	default:
		return false
	}
}

// externalDirectoryGrantEligible reports whether a tool call may have its
// external_directory approval satisfied by a directory-scoped grant
// (grant:external_directory:<dir>), so that approving a directory once widens
// to its subtree across calls.
//
// Read-only file tools and shell commands qualify: an external_directory grant
// is path-scoped ("you may access this directory"), so once a directory is
// approved it is reused for any later access to that subtree, regardless of the
// command's shape (cat, `ls || echo`, a for-loop, ...). This mirrors how the
// permission ruleset evaluates a directory glob uniformly.
//
// Mutation protection is not the job of this check — it lives in the shell
// permission rules. A command that independently requires approval (e.g.
// rm */curl * ask rules) still contributes a non-external requirement, so
// ApprovalKeysForDecision appends its command-scoped key and the directory
// grant alone cannot approve it (see DecisionRequiresNonExternalApproval).
func externalDirectoryGrantEligible(call core.ToolCall) bool {
	return readOnlyFilesystemTool(call.Name) || call.Name == "shell_run"
}

func externalDirectoryGrantKey(path string) string {
	return effects.GrantKey(effects.ExternalDirectory, path)
}

func legacyExternalReadGrantAllows(grantedKey, requestedKey string) bool {
	grantedRoot, ok := legacyExternalReadRoot(grantedKey)
	if !ok {
		return false
	}
	requested, ok := effects.ParseGrantKey(requestedKey)
	if !ok || requested.Kind != effects.ExternalDirectory || strings.TrimSpace(requested.Pattern) == "" {
		return false
	}
	target := filepath.Clean(filepath.FromSlash(requested.Pattern))
	return pathInsideOrFalse(target, grantedRoot)
}

func legacyExternalReadRoot(key string) (string, bool) {
	root, ok := strings.CutPrefix(strings.TrimSpace(key), "external_read:")
	if !ok || strings.TrimSpace(root) == "" {
		return "", false
	}
	return filepath.Clean(filepath.FromSlash(root)), true
}

func ApprovalGrantKeysAllowAll(granted map[string]bool, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || !approvalKeyGranted(granted, key) {
			return false
		}
	}
	return true
}

func memoryWritePayloadHash(body map[string]any) string {
	payload := map[string]string{}
	for _, key := range []string{"type", "description", "content"} {
		if v, ok := body[key].(string); ok {
			payload[key] = strings.TrimSpace(v)
		}
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("payload:%x", sum[:8])
}

func ApprovalRequestKeys(req ApprovalRequest) []string {
	if len(req.Keys) > 0 {
		return compactApprovalKeys(req.Keys)
	}
	if strings.TrimSpace(req.Key) != "" {
		return []string{req.Key}
	}
	return ApprovalKeys(req.ToolCall)
}

func ApprovalKeysFileScoped(keys []string) bool {
	keys = compactApprovalKeys(keys)
	if len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "file:") {
			return false
		}
	}
	return true
}

func ApprovalFiles(call core.ToolCall) []string {
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return nil
	}
	switch call.Name {
	case "edit", "write", "multi_edit":
		if v, _ := body["file_path"].(string); strings.TrimSpace(v) != "" {
			return []string{approvalCleanPath(v)}
		}
	}
	return nil
}

func ApprovalKind(call core.ToolCall) string {
	switch call.Name {
	case "edit", "write", "multi_edit":
		return "file_diff_review"
	case "shell_run":
		return "shell"
	case "write_stdin":
		return "terminal"
	case "web_search":
		return "web_search"
	case "fetch", "web_fetch":
		return "web_fetch"
	default:
		return "tool"
	}
}

func ApprovalSessionScope(call core.ToolCall) string {
	files := ApprovalFiles(call)
	switch len(files) {
	case 0:
		if call.Name == "shell_run" {
			if decision := ShellRiskDecision(call); strings.TrimSpace(decision.SessionScope) != "" {
				return decision.SessionScope
			}
			if command := shellCommandFromCall(call); command != "" {
				if family, ok := shellrisk.ArityFamily(command); ok {
					return family + " *"
				}
			}
			return "this shell command"
		}
		if call.Name == "write_stdin" {
			return "this stdin write"
		}
		if call.Name == "web_search" {
			return "Web Search commands"
		}
		if call.Name == "fetch" || call.Name == "web_fetch" {
			if host := approvalCallURLHost(call); host != "" {
				return host
			}
			return "this host"
		}
		return "this tool request"
	case 1:
		return "this file: " + files[0]
	default:
		return "these files: " + formatApprovalFiles(files)
	}
}

func NormalizeShellApprovalCommand(command string) string {
	return strings.TrimSpace(command)
}

func ShellApprovalKeys(command string) []string {
	decision := shellrisk.Classify(command)
	if len(decision.ApprovalKeys) > 0 && decision.Level != shellrisk.LevelNeedsApproval {
		return compactApprovalKeys(decision.ApprovalKeys)
	}
	// For a single simple command, key the approval by its command family
	// (arity prefix) so that allow-for-session covers the whole family rather
	// than the exact argument list. Compound commands keep the exact key.
	//
	// This intentionally mirrors opencode's arity model uniformly: it applies
	// even to commands that shellrisk routes to LevelNeedsApproval with an
	// "exact approval" rationale (e.g. go test -exec, go build). Those still
	// require an explicit prompt the first time, but an allow-for-session grant
	// widens to the family (go test *, go build *) just as opencode's `go: 2`
	// arity does. Keeping exact scope here would diverge from opencode; that
	// trade-off was chosen deliberately.
	if family, ok := shellrisk.ArityFamily(command); ok {
		return []string{"shell_run|cmd:" + family}
	}
	return []string{"shell_run|cmd:" + NormalizeShellApprovalCommand(command)}
}

func shellCommandFromCall(call core.ToolCall) string {
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return ""
	}
	command, _ := body["command"].(string)
	return strings.TrimSpace(command)
}

func WriteStdinApprovalKey(body map[string]any) string {
	taskID, _ := body["task_id"].(string)
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		taskID = "missing-task"
	}
	payload := map[string]any{}
	if v, ok := body["chars"]; ok {
		payload["chars"] = v
	}
	if v, ok := body["keys"]; ok {
		payload["keys"] = v
	}
	encoded, _ := json.Marshal(payload)
	return "terminal:write_stdin:" + taskID + ":payload:" + hashString(string(encoded))
}

func ShellRiskDecision(call core.ToolCall) shellrisk.Decision {
	if call.Name != "shell_run" {
		return shellrisk.Decision{}
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return shellrisk.Decision{}
	}
	command, _ := body["command"].(string)
	if strings.TrimSpace(command) == "" {
		return shellrisk.Decision{}
	}
	return shellrisk.Classify(command)
}

func ApprovalMetadata(call core.ToolCall, keys []string, metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+5)
	for k, v := range metadata {
		out[k] = v
	}
	if strings.TrimSpace(stringValue(out["approval_kind"])) == "" {
		out["approval_kind"] = ApprovalKind(call)
	}
	if strings.TrimSpace(stringValue(out["approval_scope"])) == "" {
		out["approval_scope"] = ApprovalScope(call)
	}
	if strings.TrimSpace(stringValue(out["approval_session_scope"])) == "" {
		out["approval_session_scope"] = ApprovalSessionScope(call)
	}
	if compact := compactApprovalKeys(keys); len(compact) > 0 {
		out["approval_keys"] = compact
		for _, key := range compact {
			grant, ok := effects.ParseGrantKey(key)
			if !ok {
				continue
			}
			out["effect_kind"] = string(grant.Kind)
			out["effect_scope"] = grant.Pattern
			out["grant_pattern"] = grant.Pattern
			break
		}
	}
	if files := ApprovalFiles(call); len(files) > 0 {
		out["approval_files"] = files
	}
	if call.Name == "shell_run" {
		decision := ShellRiskDecision(call)
		if strings.TrimSpace(decision.Code) != "" {
			out["shell_risk_code"] = decision.Code
		}
		if strings.TrimSpace(decision.Level) != "" {
			out["shell_risk_level"] = decision.Level
		}
		if strings.TrimSpace(decision.Reason) != "" {
			out["shell_risk_reason"] = decision.Reason
		}
		if len(decision.WriteScopes) > 0 {
			out["shell_write_scopes"] = append([]string(nil), decision.WriteScopes...)
		}
		if len(decision.ApprovalKeys) > 0 && decision.Level == shellrisk.LevelBoundedWrite {
			out["shell_approval_family"] = true
		}
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func ApprovalSummary(call core.ToolCall) string {
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return call.Name
	}
	switch call.Name {
	case "shell_run":
		if v, _ := body["command"].(string); strings.TrimSpace(v) != "" {
			return fmt.Sprintf("shell_run: %s", strings.TrimSpace(v))
		}
	case "write_stdin":
		return writeStdinApprovalSummary(body)
	case "write":
		if v, _ := body["file_path"].(string); strings.TrimSpace(v) != "" {
			return fmt.Sprintf("write: %s", strings.TrimSpace(v))
		}
	case "edit":
		if v, _ := body["file_path"].(string); strings.TrimSpace(v) != "" {
			return fmt.Sprintf("edit: %s", strings.TrimSpace(v))
		}
	case "multi_edit":
		if v, _ := body["file_path"].(string); strings.TrimSpace(v) != "" {
			return fmt.Sprintf("multi_edit: %s", strings.TrimSpace(v))
		}
	case "web_search":
		if v := approvalSearchQuery(body); v != "" {
			return fmt.Sprintf("web_search: %s", v)
		}
	case "fetch", "web_fetch":
		if v, _ := body["url"].(string); strings.TrimSpace(v) != "" {
			return fmt.Sprintf("%s: %s", call.Name, strings.TrimSpace(v))
		}
	}
	return call.Name
}

func writeStdinApprovalSummary(body map[string]any) string {
	taskID, _ := body["task_id"].(string)
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		taskID = "unknown-task"
	}
	parts := []string{"write_stdin:", taskID}
	if chars, ok := body["chars"].(string); ok {
		parts = append(parts, "chars="+strconv.Quote(shortApprovalPreview(chars, 120)))
	}
	if keys := stringSliceValue(body["keys"]); len(keys) > 0 {
		parts = append(parts, "keys=["+strings.Join(keys, ",")+"]")
	}
	return strings.Join(parts, " ")
}

func shortApprovalPreview(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "...[truncated]"
}

func ApprovalScope(call core.ToolCall) string {
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return "workspace"
	}
	if v, _ := body["file_path"].(string); strings.TrimSpace(v) != "" {
		return "file:" + approvalCleanPath(v)
	}
	if v, _ := body["path"].(string); strings.TrimSpace(v) != "" {
		return "path:" + strings.TrimSpace(v)
	}
	if call.Name == "shell_run" {
		return "shell"
	}
	if call.Name == "write_stdin" {
		return "terminal"
	}
	return "workspace"
}

func approvalSearchQuery(body map[string]any) string {
	for _, key := range []string{"query", "q"} {
		if v, _ := body[key].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if raw, ok := body["search_query"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"q", "query"} {
				if v, _ := m[key].(string); strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	return ""
}

func approvalCallURLHost(call core.ToolCall) string {
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return ""
	}
	return approvalURLHost(body)
}

func approvalURLHost(body map[string]any) string {
	raw, _ := body["url"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(u.Hostname()) == "" {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
}

func approvalFileKey(path string) string {
	return "file:" + approvalCleanPath(path)
}

func approvalCleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func approvalPatchFiles(body map[string]any) []string {
	patch, _ := body["patch"].(string)
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(patch, "\r\n", "\n"), "\r", "\n"), "\n")
	seen := map[string]bool{}
	for _, line := range lines {
		for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			path := approvalCleanPath(strings.TrimPrefix(line, prefix))
			if path != "" {
				seen[path] = true
			}
		}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

func compactApprovalKeys(keys []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func formatApprovalFiles(files []string) string {
	files = append([]string(nil), files...)
	sort.Strings(files)
	const maxVisible = 3
	if len(files) <= maxVisible {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(files[:maxVisible], ", "), len(files)-maxVisible)
}
