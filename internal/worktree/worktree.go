package worktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

const (
	dirName                   = "worktrees"
	branchPrefix              = "worktree-"
	maxNameLen                = 64
	worktreesExcludePattern   = ".whale/worktrees/"
	localConfigExcludePattern = ".whale/config.local.toml"
)

var managedExcludePatterns = []string{
	worktreesExcludePattern,
	localConfigExcludePattern,
}

var validSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var validManagedName = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

type Session struct {
	Name               string
	Path               string
	Branch             string
	OriginalWorkspace  string
	OriginalBranch     string
	OriginalHeadCommit string
	Created            bool
}

type Entry struct {
	Name    string
	Path    string
	Branch  string
	Head    string
	Dirty   bool
	Missing bool
}

type RemoveResult struct {
	Entry         Entry
	BranchDeleted bool
	BranchWarning string
}

type ChangeSummary struct {
	ChangedFiles int
	IgnoredFiles int
	Commits      int
}

// ResolveSession computes the Session that Start would create or reuse for
// cwd and name without performing any side effects: no worktree is added, no
// branch is created, and no ignore-file or config changes are made. Callers
// use it for read-only pre-validation before committing to worktree creation.
func ResolveSession(cwd, name string) (Session, error) {
	sess, _, err := resolveSession(cwd, name)
	return sess, err
}

func resolveSession(cwd, name string) (Session, string, error) {
	name = strings.TrimSpace(name)
	if err := ValidateName(name); err != nil {
		return Session{}, "", err
	}
	originalWorkspace, err := filepath.Abs(cwd)
	if err != nil {
		return Session{}, "", fmt.Errorf("resolve workspace: %w", err)
	}
	repoRoot, err := CanonicalRepoRoot(originalWorkspace)
	if err != nil {
		return Session{}, "", err
	}
	sess := Session{
		Name:               name,
		Path:               WorktreePath(repoRoot, name),
		Branch:             BranchName(name),
		OriginalWorkspace:  originalWorkspace,
		OriginalBranch:     gitOutput(originalWorkspace, "branch", "--show-current"),
		OriginalHeadCommit: gitOutput(originalWorkspace, "rev-parse", "HEAD"),
	}
	return sess, repoRoot, nil
}

func Start(cwd, name string) (Session, error) {
	sess, repoRoot, err := resolveSession(cwd, name)
	if err != nil {
		return Session{}, err
	}
	checkoutRoot, err := CheckoutRoot(sess.OriginalWorkspace)
	if err != nil {
		return Session{}, err
	}
	workspaceRel, err := filepath.Rel(checkoutRoot, sess.OriginalWorkspace)
	if err != nil {
		return Session{}, fmt.Errorf("resolve workspace relative path: %w", err)
	}
	flatName := FlattenName(sess.Name)
	branch := sess.Branch
	path := sess.Path

	if err := ensureManagedPathsIgnored(repoRoot, workspaceRel); err != nil {
		return Session{}, err
	}
	if isGitWorktree(path) {
		return sess, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Session{}, fmt.Errorf("create worktrees dir: %w", err)
	}
	if registered, _ := registeredManagedWorktrees(repoRoot); registered != nil {
		if _, ok := registered[flatName]; ok {
			_ = runGit(repoRoot, "worktree", "prune")
		}
	}
	addArgs := []string{"worktree", "add"}
	if branchExists(repoRoot, branch) {
		addArgs = append(addArgs, path, branch)
	} else {
		base := core.FirstNonEmpty(sess.OriginalHeadCommit, "HEAD")
		addArgs = append(addArgs, "-b", branch, path, base)
	}
	if err := runGit(repoRoot, addArgs...); err != nil {
		return Session{}, err
	}
	sess.Created = true
	if err := copyLocalConfig(sess.OriginalWorkspace, filepath.Join(path, workspaceRel)); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("worktree name is required")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("worktree name is too long: max %d characters", maxNameLen)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." || !validSegment.MatchString(segment) {
			return fmt.Errorf("invalid worktree name: %s", name)
		}
	}
	return nil
}

func ValidateManagedName(name string) error {
	name = strings.TrimSpace(name)
	if strings.Contains(name, "+") && !strings.Contains(name, "/") {
		if name == "" {
			return fmt.Errorf("worktree name is required")
		}
		if len(name) > maxNameLen {
			return fmt.Errorf("worktree name is too long: max %d characters", maxNameLen)
		}
		if name == "." || name == ".." || !validManagedName.MatchString(name) {
			return fmt.Errorf("invalid worktree name: %s", name)
		}
		return nil
	}
	return ValidateName(name)
}

func FlattenName(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), "/", "+")
}

func BranchName(name string) string {
	return branchPrefix + FlattenName(name)
}

func WorktreePath(repoRoot, name string) string {
	return filepath.Join(repoRoot, ".whale", dirName, FlattenName(name))
}

func List(cwd string) ([]Entry, error) {
	repoRoot, err := CanonicalRepoRoot(cwd)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(repoRoot, ".whale", dirName)
	seen := make(map[string]bool)
	out := []Entry{}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read worktrees: %w", err)
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		path := filepath.Join(root, name)
		item := Entry{Name: name, Path: path, Branch: BranchName(name)}
		if !isGitWorktree(path) {
			item.Missing = true
			out = append(out, item)
			seen[name] = true
			continue
		}
		item.Branch = core.FirstNonEmpty(gitOutput(path, "branch", "--show-current"), item.Branch)
		item.Head = gitOutput(path, "rev-parse", "--short", "HEAD")
		item.Dirty = strings.TrimSpace(gitOutput(path, "status", "--porcelain")) != ""
		out = append(out, item)
		seen[name] = true
	}
	if registered, err := registeredManagedWorktrees(repoRoot); err == nil {
		for name, path := range registered {
			if seen[name] {
				continue
			}
			out = append(out, Entry{Name: name, Path: path, Branch: BranchName(name), Missing: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func Status(cwd, name string) (Entry, error) {
	if err := ValidateManagedName(name); err != nil {
		return Entry{}, err
	}
	repoRoot, err := CanonicalRepoRoot(cwd)
	if err != nil {
		return Entry{}, err
	}
	path := WorktreePath(repoRoot, name)
	entry := Entry{Name: FlattenName(name), Path: path, Branch: BranchName(name)}
	if !isGitWorktree(path) {
		entry.Missing = true
		return entry, nil
	}
	entry.Branch = core.FirstNonEmpty(gitOutput(path, "branch", "--show-current"), entry.Branch)
	entry.Head = gitOutput(path, "rev-parse", "--short", "HEAD")
	entry.Dirty = strings.TrimSpace(gitOutput(path, "status", "--porcelain")) != ""
	return entry, nil
}

func CountChanges(path, originalWorkspace, originalHeadCommit string) (ChangeSummary, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ChangeSummary{}, fmt.Errorf("worktree path is required")
	}
	// --untracked-files=all expands fully-ignored directories into their
	// individual files. Without it git collapses a directory whose entire
	// contents are ignored (e.g. a repo that ignores all of `.whale/`) into a
	// single `!! .whale/` entry, which would hide Whale's managed
	// config.local.toml copy from the path match below.
	status, err := gitOutputErr(path, "status", "--porcelain", "--ignored", "--untracked-files=all")
	if err != nil {
		return ChangeSummary{}, fmt.Errorf("read worktree status: %w", err)
	}
	changed := 0
	ignored := 0
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "!!") {
			// Whale copies the invoking workspace's config.local.toml into the
			// worktree and adds it to git's exclude list, so git reports it as an
			// ignored file. Skip Whale's own managed copy only while it still
			// matches the source it was copied from; once the user edits it
			// during the session, count it so exit does not silently discard
			// those edits.
			if rel := managedLocalConfigRelPath(line); rel != "" {
				if managedLocalConfigUnchanged(path, rel, originalWorkspace) {
					continue
				}
			}
			ignored++
		} else {
			changed++
		}
	}
	if strings.TrimSpace(originalHeadCommit) == "" {
		return ChangeSummary{ChangedFiles: changed, IgnoredFiles: ignored}, nil
	}
	commitsOut, err := gitOutputErr(path, "rev-list", "--count", strings.TrimSpace(originalHeadCommit)+"..HEAD")
	if err != nil {
		return ChangeSummary{}, fmt.Errorf("count worktree commits: %w", err)
	}
	commitsOut = strings.TrimSpace(commitsOut)
	commits := 0
	if commitsOut != "" {
		if _, err := fmt.Sscanf(commitsOut, "%d", &commits); err != nil {
			return ChangeSummary{}, fmt.Errorf("parse worktree commit count: %w", err)
		}
	}
	return ChangeSummary{ChangedFiles: changed, IgnoredFiles: ignored, Commits: commits}, nil
}

func Remove(cwd, name string, force bool) (RemoveResult, error) {
	entry, err := Status(cwd, name)
	if err != nil {
		return RemoveResult{}, err
	}
	repoRoot, err := CanonicalRepoRoot(cwd)
	if err != nil {
		return RemoveResult{}, err
	}
	if entry.Missing {
		pathExists := false
		if _, statErr := os.Stat(entry.Path); statErr == nil {
			pathExists = true
		}
		registered, _ := registeredManagedWorktrees(repoRoot)
		_, hasRegistration := registered[FlattenName(name)]
		if !pathExists && !hasRegistration {
			return RemoveResult{}, fmt.Errorf("worktree not found: %s", name)
		}
		if pathExists && !force {
			return RemoveResult{}, fmt.Errorf("worktree directory exists but is not a valid git worktree; use --force to discard it")
		}
		if hasRegistration {
			if err := runGit(repoRoot, "worktree", "prune"); err != nil {
				return RemoveResult{}, err
			}
		}
		if pathExists {
			if err := os.RemoveAll(entry.Path); err != nil {
				return RemoveResult{}, fmt.Errorf("remove worktree dir: %w", err)
			}
		}
		res := RemoveResult{Entry: entry}
		deleteFlag := "-d"
		if force {
			deleteFlag = "-D"
		}
		if err := runGit(repoRoot, "branch", deleteFlag, BranchName(name)); err != nil {
			res.BranchWarning = err.Error()
			return res, nil
		}
		res.BranchDeleted = true
		return res, nil
	}
	current, err := filepath.Abs(cwd)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	if insidePath(current, entry.Path) {
		return RemoveResult{}, fmt.Errorf("cannot remove the current worktree")
	}
	if entry.Dirty && !force {
		return RemoveResult{}, fmt.Errorf("worktree has changes; use --force to discard them")
	}
	removeArgs := []string{"worktree", "remove"}
	if force {
		removeArgs = append(removeArgs, "--force")
	}
	removeArgs = append(removeArgs, entry.Path)
	if err := runGit(repoRoot, removeArgs...); err != nil {
		return RemoveResult{}, err
	}
	res := RemoveResult{Entry: entry}
	deleteFlag := "-d"
	if force {
		deleteFlag = "-D"
	}
	if err := runGit(repoRoot, "branch", deleteFlag, BranchName(name)); err != nil {
		res.BranchWarning = err.Error()
		return res, nil
	}
	res.BranchDeleted = true
	return res, nil
}

func CanonicalRepoRoot(cwd string) (string, error) {
	root, err := CheckoutRoot(cwd)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(gitOutput(cwd, "rev-parse", "--show-superproject-working-tree")) != "" {
		return root, nil
	}
	commonDir, err := gitOutputErr(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return "", fmt.Errorf("resolve git common dir: empty result")
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir), nil
	}
	return root, nil
}

func CheckoutRoot(cwd string) (string, error) {
	root, err := gitOutputErr(cwd, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("--worktree requires a git repository")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("resolve git root: empty result")
	}
	return root, nil
}

func insidePath(path, root string) bool {
	path = normalizedPath(path)
	root = normalizedPath(root)
	if path == "" || root == "" {
		return false
	}
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func normalizedPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}

func registeredManagedWorktrees(repoRoot string) (map[string]string, error) {
	out, err := gitOutputErr(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	managedRoot := normalizedPath(filepath.Join(repoRoot, ".whale", dirName))
	result := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if path == "" {
			continue
		}
		abs := path
		if a, err := filepath.Abs(path); err == nil {
			abs = a
		}
		if !insidePath(abs, managedRoot) {
			continue
		}
		result[filepath.Base(abs)] = abs
	}
	return result, nil
}

func isGitWorktree(path string) bool {
	out, err := gitOutputErr(path, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return false
	}
	return normalizedPath(strings.TrimSpace(out)) == normalizedPath(path)
}

func copyLocalConfig(sourceWorkspace, worktreeWorkspace string) error {
	src := filepath.Join(sourceWorkspace, ".whale", "config.local.toml")
	b, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read local config: %w", err)
	}
	dst := filepath.Join(worktreeWorkspace, ".whale", "config.local.toml")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create worktree config dir: %w", err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return fmt.Errorf("copy local config: %w", err)
	}
	return nil
}

func ensureManagedPathsIgnored(repoRoot, workspaceRel string) error {
	commonDir, err := gitOutputErr(repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return fmt.Errorf("resolve git common dir: empty result")
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")
	b, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read git exclude: %w", err)
	}
	existing := string(b)
	patterns := append([]string{}, managedExcludePatterns...)
	if pattern := localConfigExcludeFor(workspaceRel); pattern != localConfigExcludePattern {
		patterns = append(patterns, pattern)
	}
	missing := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if !gitCheckIgnore(repoRoot, pattern) && !excludeContainsPattern(existing, pattern) {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create git exclude dir: %w", err)
	}
	prefix := ""
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		prefix = "\n"
	}
	var builder strings.Builder
	builder.WriteString(prefix)
	for _, pattern := range missing {
		builder.WriteString(pattern)
		builder.WriteByte('\n')
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open git exclude: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(builder.String()); err != nil {
		return fmt.Errorf("write git exclude: %w", err)
	}
	return nil
}

func localConfigExcludeFor(workspaceRel string) string {
	workspaceRel = filepath.Clean(strings.TrimSpace(workspaceRel))
	if workspaceRel == "" || workspaceRel == "." {
		return localConfigExcludePattern
	}
	return filepath.ToSlash(filepath.Join(workspaceRel, ".whale", "config.local.toml"))
}

// managedLocalConfigRelPath returns the slash-separated, worktree-relative path
// of Whale's managed config.local.toml copy when the given `git status
// --porcelain --ignored` line refers to it, or "" otherwise. The path is
// matched at any workspace depth because Start copies the config into the
// worktree subdirectory that mirrors the invoking workspace.
func managedLocalConfigRelPath(line string) string {
	p := strings.TrimSpace(strings.TrimPrefix(line, "!!"))
	p = strings.Trim(p, `"`)
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "/")
	if p == localConfigExcludePattern || strings.HasSuffix(p, "/"+localConfigExcludePattern) {
		return p
	}
	return ""
}

// managedLocalConfigUnchanged reports whether the managed config copy at
// worktreePath/rel is byte-identical to the config.local.toml in
// originalWorkspace that Start copied it from. When the baseline cannot be read
// it returns false, so the caller conservatively counts the file as a user
// change rather than silently discarding a possible edit.
func managedLocalConfigUnchanged(worktreePath, rel, originalWorkspace string) bool {
	originalWorkspace = strings.TrimSpace(originalWorkspace)
	if originalWorkspace == "" {
		return false
	}
	copyContent, err := os.ReadFile(filepath.Join(worktreePath, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	baseContent, err := os.ReadFile(filepath.Join(originalWorkspace, ".whale", "config.local.toml"))
	if err != nil {
		return false
	}
	return bytes.Equal(copyContent, baseContent)
}

func excludeContainsPattern(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == pattern || line == "/"+pattern {
			return true
		}
	}
	return false
}

func gitCheckIgnore(cwd, path string) bool {
	cmd := exec.Command("git", "check-ignore", "-q", path)
	cmd.Dir = cwd
	return cmd.Run() == nil
}

func gitOutput(cwd string, args ...string) string {
	out, err := gitOutputErr(cwd, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitOutputErr(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func branchExists(cwd, branch string) bool {
	if strings.TrimSpace(branch) == "" {
		return false
	}
	_, err := gitOutputErr(cwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func runGit(cwd string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}
