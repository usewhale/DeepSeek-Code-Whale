package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/skills"
)

func (p RulePolicy) externalDirs(command string) []string {
	return p.externalDirsFromRoot(command, p.WorkspaceRoot, false)
}

// externalDirsFromRoot extracts outside-workspace directories a command would
// touch. programPathTrusted reports whether the command word is a resolved
// program path (as seen at the exec boundary, e.g. /bin/cat) — only then is it
// matched by basename.
//
// For a model-authored command string it is false, so a path-qualified
// invocation like /bin/cat is NOT treated as a file command. This is an
// intentional alignment with opencode's command-string permission model, and a
// deliberate trade-off: command-string rules are inherently evadable by
// path-qualifying (e.g. /bin/rm sidesteps the rm deny rules too), so on pipe
// transport a path-qualified file command is not gated by external_directory.
// The exec boundary (PTY transport) still basename-matches and gates it.
func (p RulePolicy) externalDirsFromRoot(command, pathRoot string, programPathTrusted bool) []string {
	root := cleanAbs(pathRoot)
	if command == "" || root == "" {
		return nil
	}
	projectRoots := p.projectRoots()
	var out []string
	for _, segment := range expandShellRuleSegments(command) {
		// Redirection targets are intentionally not scanned (aligns with
		// opencode, which skips redirection nodes). A shell redirect write such
		// as `echo x > /path` is therefore not gated by external_directory; only
		// file-command operands below are checked.
		argv := strings.Fields(segment)
		if len(argv) == 0 || !shellFileCommandWord(argv[0], programPathTrusted) {
			continue
		}
		for _, arg := range argv[1:] {
			arg = strings.Trim(arg, `"'`)
			arg = shellPathArgBeforeRedirection(arg)
			if !shellFileCommandPathArg(arg) {
				continue
			}
			clean := p.resolveShellPathToken(root, arg)
			if clean == "" || pathInsideAny(clean, projectRoots) || pathInsideTrustedShellPath(clean) {
				continue
			}
			out = append(out, externalDirForToken(clean))
		}
	}
	return uniqueStrings(out)
}

func (p RulePolicy) execBoundaryImplicitCWDExternalDir(req ExecBoundaryRequest) string {
	if !execBoundaryFileCommandUsesImplicitCWD(req) {
		return ""
	}
	cwd := cleanAbs(req.CWD)
	if cwd == "" || pathInsideAny(cwd, p.projectRoots()) {
		return ""
	}
	return externalDirForToken(cwd)
}

func (p RulePolicy) externalDirsFromMCPInput(input string) []string {
	root := cleanAbs(p.WorkspaceRoot)
	if root == "" || strings.TrimSpace(input) == "" {
		return nil
	}
	projectRoots := p.projectRoots()
	var body map[string]any
	if err := json.Unmarshal([]byte(input), &body); err != nil {
		return nil
	}
	var out []string
	for _, token := range mcpPathTokens(body) {
		clean := p.resolveShellPathToken(root, token)
		if clean == "" || pathInsideAny(clean, projectRoots) || pathInsideTrustedTemp(clean) {
			continue
		}
		out = append(out, externalDirForToken(clean))
	}
	return uniqueStrings(out)
}
func (p RulePolicy) externalDirsFromReadInput(call core.ToolCall) []string {
	root := cleanAbs(p.WorkspaceRoot)
	if root == "" {
		return nil
	}
	target := readScopeTarget(call)
	if target == "*" {
		return nil
	}
	clean := p.resolveShellPathToken(root, target)
	if clean == "" || pathInsideAny(clean, p.projectRoots()) || p.isDiscoveredSkillReadPath(clean) {
		return nil
	}
	return []string{externalDirForToken(clean)}
}
func (p RulePolicy) isDiscoveredSkillReadPath(target string) bool {
	root := cleanAbs(p.WorkspaceRoot)
	if root == "" || strings.TrimSpace(target) == "" {
		return false
	}
	targetReal := canonicalAccessPath(target)
	if targetReal == "" {
		return false
	}
	for _, skill := range skills.Discover(skills.DefaultRoots(root)) {
		if skill == nil || strings.TrimSpace(skill.Path) == "" {
			continue
		}
		dirReal := canonicalAccessPath(skill.Path)
		if dirReal != "" && pathInsideOrFalse(targetReal, dirReal) {
			return true
		}
	}
	return false
}
func (p RulePolicy) projectRoots() []string {
	roots := []string{}
	if root := cleanAbs(p.WorkspaceRoot); root != "" {
		roots = append(roots, root)
	}
	if root := cleanAbs(p.WorktreeRoot); root != "" {
		roots = append(roots, root)
	}
	return uniqueStrings(roots)
}
func mcpPathTokens(body map[string]any) []string {
	keys := []string{"path", "file_path", "root", "directory", "source", "destination"}
	out := []string{}
	for _, key := range keys {
		if v, ok := body[key].(string); ok && strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	if xs, ok := body["paths"].([]any); ok {
		for _, x := range xs {
			if v, ok := x.(string); ok && strings.TrimSpace(v) != "" {
				out = append(out, strings.TrimSpace(v))
			}
		}
	}
	return out
}
func (p RulePolicy) mcpAllowedDirsDenied(spec core.ToolSpec, call core.ToolCall) string {
	if !strings.HasPrefix(spec.Name, "mcp__") {
		return ""
	}
	allowedDirs := mcpFilesystemAllowedDirs(spec)
	if len(allowedDirs) == 0 {
		return ""
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Input), &body); err != nil {
		return ""
	}
	for _, token := range mcpPathTokens(body) {
		token = expandHome(token)
		if !filepath.IsAbs(token) {
			continue
		}
		clean := cleanAbs(token)
		if !pathInsideAnyCanonical(clean, allowedDirs) {
			return fmt.Sprintf("MCP filesystem server cannot access %s; allowed directories: %s. Use Whale built-in file tools for this path, or add the directory to the MCP server configuration.", clean, strings.Join(allowedDirs, ", "))
		}
	}
	return ""
}
func mcpFilesystemAllowedDirs(spec core.ToolSpec) []string {
	const prefix = "mcp_filesystem_allowed_dir:"
	var out []string
	for _, cap := range spec.Capabilities {
		if dir, ok := strings.CutPrefix(strings.TrimSpace(cap), prefix); ok {
			if dir = cleanAbs(dir); dir != "" {
				out = append(out, dir)
			}
		}
	}
	return uniqueStrings(out)
}
func mcpFilesystemTool(spec core.ToolSpec) bool {
	return hasCapability(spec, "mcp_filesystem") || len(mcpFilesystemAllowedDirs(spec)) > 0
}
func pathInsideAnyCanonical(path string, roots []string) bool {
	path = canonicalAccessPath(path)
	for _, root := range roots {
		root = canonicalAccessPath(root)
		if pathInsideOrFalse(path, root) {
			return true
		}
	}
	return false
}
func canonicalAccessPath(path string) string {
	clean := cleanAbs(path)
	if clean == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(real)
	}
	cur := clean
	var suffix []string
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			parts := append([]string{filepath.Clean(real)}, suffix...)
			return filepath.Clean(filepath.Join(parts...))
		}
		cur = parent
	}
}

// shellFileCommandWord reports whether word names a built-in file command whose
// operands should be checked against the external_directory rules. When
// programPathTrusted is set the word is a resolved program path (exec boundary)
// and is matched by basename, so /bin/cat still counts; otherwise only a bare
// command name matches, mirroring how shell permission rules and shellrisk treat
// the command word (see externalDirsFromRoot for the rationale and trade-off).
func shellFileCommandWord(word string, programPathTrusted bool) bool {
	name := strings.TrimSpace(word)
	if programPathTrusted {
		name = filepath.Base(name)
	}
	switch strings.ToLower(name) {
	case "cat", "ls", "cp", "mv", "rm", "mkdir", "rmdir", "touch", "chmod", "chown", "readlink", "realpath", "stat", "du", "head", "tail", "wc":
		return true
	default:
		return false
	}
}

func execBoundaryFileCommandUsesImplicitCWD(req ExecBoundaryRequest) bool {
	program := strings.TrimSpace(req.Program)
	argv := append([]string(nil), req.Argv...)
	if len(argv) == 0 {
		argv = []string{program}
	}
	if !shellFileCommandWord(program, true) && !shellFileCommandWord(argv[0], true) {
		return false
	}
	for _, arg := range argv[1:] {
		arg = strings.Trim(arg, `"'`)
		arg = shellPathArgBeforeRedirection(arg)
		if shellFileCommandPathArg(arg) {
			return false
		}
	}
	return true
}

func shellFileCommandPathArg(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "-") || strings.Contains(arg, "://") {
		return false
	}
	if strings.ContainsAny(arg, "<>$`") || strings.Contains(arg, "$(") || strings.Contains(arg, "${") {
		return false
	}
	return true
}
func shellPathArgBeforeRedirection(arg string) string {
	idx := strings.IndexAny(arg, "<>")
	if idx < 0 {
		return arg
	}
	return arg[:idx]
}
func pathInsideTrustedTemp(clean string) bool {
	if clean == "" {
		return false
	}
	if temp := cleanAbs(os.TempDir()); temp != "" && pathInsideOrFalse(clean, temp) {
		return true
	}
	return clean == "/tmp" || strings.HasPrefix(clean, "/tmp/") ||
		clean == "/private/tmp" || strings.HasPrefix(clean, "/private/tmp/")
}
func pathInsideTrustedShellPath(clean string) bool {
	return filepath.Clean(clean) == "/dev/null"
}

// externalDirForToken returns the directory a shell path token should be
// evaluated against. A token that is itself a directory is matched as-is so
// explicit rules for that directory apply; otherwise its parent directory is
// used (the token names a file, existing or yet to be created).
func externalDirForToken(clean string) string {
	if info, err := os.Stat(clean); err == nil && info.IsDir() {
		return clean
	}
	return filepath.Dir(clean)
}
func (p RulePolicy) resolveShellPathToken(root, token string) string {
	token = expandHome(token)
	if filepath.IsAbs(token) {
		return cleanAbs(token)
	}
	if root == "" {
		return ""
	}
	return cleanAbs(filepath.Join(root, token))
}
func expandHome(path string) string {
	path = strings.TrimSpace(path)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if strings.HasPrefix(path, "$HOME/") {
		return filepath.Join(home, strings.TrimPrefix(path, "$HOME/"))
	}
	if path == "$HOME" {
		return home
	}
	return path
}
func cleanAbs(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}
func pathInsideOrFalse(path, root string) bool {
	ok, err := core.PathInside(path, root)
	if err != nil {
		return false
	}
	return ok
}
func pathInsideAny(path string, roots []string) bool {
	for _, root := range roots {
		if pathInsideOrFalse(path, root) {
			return true
		}
	}
	return false
}
func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// toolResultReadPath extracts the file path from a read-only filesystem tool call
// and reports whether it falls inside the tool-results session directory, which
// should be auto-allowed without user approval.
func ToolResultReadPath(dir string, call core.ToolCall) bool {
	target := strings.TrimSpace(readScopeTarget(call))
	if target == "" || target == "*" {
		return false
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		targetAbs = filepath.Clean(target)
	} else {
		targetAbs = filepath.Clean(targetAbs)
	}
	return pathInsideAny(targetAbs, []string{filepath.Clean(dir)})
}
