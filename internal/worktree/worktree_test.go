package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{"feature", "feature-1", "foo/bar.baz", "A_B.1"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q): %v", name, err)
		}
	}
	invalid := []string{"", ".", "..", "../x", "x/../y", "has space", "x:$"}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Fatalf("ValidateName(%q) should fail", name)
		}
	}
}

func TestFlattenAndBranchName(t *testing.T) {
	if got := FlattenName("foo/bar"); got != "foo+bar" {
		t.Fatalf("FlattenName = %q", got)
	}
	if got := BranchName("foo/bar"); got != "worktree-foo+bar" {
		t.Fatalf("BranchName = %q", got)
	}
}

func TestStartCreatesReusesAndCopiesOnlyLocalConfig(t *testing.T) {
	repo := newGitRepo(t)
	mkdir(t, filepath.Join(repo, ".whale", "sessions"))
	write(t, filepath.Join(repo, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-pro\"\n"))
	write(t, filepath.Join(repo, ".whale", "settings.json"), []byte("{}"))
	write(t, filepath.Join(repo, ".whale", "sessions", "s1.jsonl"), []byte("{}\n"))

	sess, err := Start(repo, "feature/test")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	wantPath := filepath.Join(repo, ".whale", "worktrees", "feature+test")
	if !sess.Created || sess.Path != wantPath || sess.Branch != "worktree-feature+test" {
		t.Fatalf("unexpected session: %+v", sess)
	}
	exclude := read(t, filepath.Join(repo, ".git", "info", "exclude"))
	for _, pattern := range managedExcludePatterns {
		if !strings.Contains(exclude, pattern) {
			t.Fatalf("git info/exclude should contain %q, got:\n%s", pattern, exclude)
		}
	}
	if sess.OriginalWorkspace != repo || sess.OriginalBranch != "main" || sess.OriginalHeadCommit == "" {
		t.Fatalf("unexpected original metadata: %+v", sess)
	}
	if got := read(t, filepath.Join(sess.Path, ".whale", "config.local.toml")); got != "model = \"deepseek-v4-pro\"\n" {
		t.Fatalf("copied config = %q", got)
	}
	if got := strings.TrimSpace(gitOut(t, sess.Path, "status", "--porcelain")); got != "" {
		t.Fatalf("new worktree should remain clean after copying local config, status:\n%s", got)
	}
	for _, unexpected := range []string{
		filepath.Join(sess.Path, ".whale", "settings.json"),
		filepath.Join(sess.Path, ".whale", "sessions", "s1.jsonl"),
	} {
		if _, err := os.Stat(unexpected); !os.IsNotExist(err) {
			t.Fatalf("should not copy %s", unexpected)
		}
	}

	reused, err := Start(repo, "feature/test")
	if err != nil {
		t.Fatalf("Start reuse: %v", err)
	}
	if reused.Created {
		t.Fatalf("expected reuse, got %+v", reused)
	}
}

func TestCountChangesWithoutOriginalHeadCountsOnlyDirtyFiles(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "legacy-metadata")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	write(t, filepath.Join(sess.Path, "dirty.txt"), []byte("dirty\n"))

	got, err := CountChanges(sess.Path, sess.OriginalWorkspace, "")
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if got.ChangedFiles != 1 || got.Commits != 0 {
		t.Fatalf("CountChanges = %+v, want 1 dirty file and 0 commits", got)
	}
}

func TestCountChangesIncludesIgnoredFiles(t *testing.T) {
	repo := newGitRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), []byte(".env\nbuild/\n"))
	run(t, repo, "git", "add", ".gitignore")
	run(t, repo, "git", "commit", "-m", "ignore local artifacts")
	sess, err := Start(repo, "ignored-files")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	write(t, filepath.Join(sess.Path, ".env"), []byte("secret\n"))

	got, err := CountChanges(sess.Path, sess.OriginalWorkspace, sess.OriginalHeadCommit)
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if got.ChangedFiles != 0 || got.IgnoredFiles != 1 || got.Commits != 0 {
		t.Fatalf("CountChanges = %+v, want 1 ignored file only", got)
	}
}

func TestCountChangesExcludesCopiedLocalConfig(t *testing.T) {
	repo := newGitRepo(t)
	write(t, filepath.Join(repo, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-pro\"\n"))
	sess, err := Start(repo, "config-clean")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess.Path, ".whale", "config.local.toml")); err != nil {
		t.Fatalf("expected copied local config in worktree: %v", err)
	}

	got, err := CountChanges(sess.Path, sess.OriginalWorkspace, sess.OriginalHeadCommit)
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if got.ChangedFiles != 0 || got.IgnoredFiles != 0 || got.Commits != 0 {
		t.Fatalf("CountChanges = %+v, want clean worktree (copied local config excluded)", got)
	}
}

func TestCountChangesExcludesLocalConfigWhenWholeWhaleDirIgnored(t *testing.T) {
	repo := newGitRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), []byte(".whale/\n"))
	run(t, repo, "git", "add", ".gitignore")
	run(t, repo, "git", "commit", "-m", "ignore whale directory")
	write(t, filepath.Join(repo, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-pro\"\n"))

	sess, err := Start(repo, "whale-dir-ignored")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess.Path, ".whale", "config.local.toml")); err != nil {
		t.Fatalf("expected copied local config in worktree: %v", err)
	}

	got, err := CountChanges(sess.Path, sess.OriginalWorkspace, sess.OriginalHeadCommit)
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if got.ChangedFiles != 0 || got.IgnoredFiles != 0 || got.Commits != 0 {
		t.Fatalf("CountChanges = %+v, want clean worktree even when .whale/ is ignored wholesale", got)
	}

	// A user file alongside the copied config under the ignored .whale/ dir
	// must still be counted.
	write(t, filepath.Join(sess.Path, ".whale", "scratch.txt"), []byte("notes\n"))
	got, err = CountChanges(sess.Path, sess.OriginalWorkspace, sess.OriginalHeadCommit)
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if got.ChangedFiles != 0 || got.IgnoredFiles != 1 || got.Commits != 0 {
		t.Fatalf("CountChanges = %+v, want the user file under ignored .whale/ counted", got)
	}
}

func TestCountChangesCountsEditedLocalConfig(t *testing.T) {
	repo := newGitRepo(t)
	write(t, filepath.Join(repo, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-pro\"\n"))
	sess, err := Start(repo, "config-edited")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Simulate the user editing the copied local config during the session.
	write(t, filepath.Join(sess.Path, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-flash\"\n"))

	got, err := CountChanges(sess.Path, sess.OriginalWorkspace, sess.OriginalHeadCommit)
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if got.ChangedFiles != 0 || got.IgnoredFiles != 1 || got.Commits != 0 {
		t.Fatalf("CountChanges = %+v, want the edited local config counted as a change", got)
	}
}

func TestCountChangesExcludesSubdirLocalConfigButCountsOtherIgnored(t *testing.T) {
	repo := newGitRepo(t)
	subdir := filepath.Join(repo, "packages", "api")
	write(t, filepath.Join(subdir, "api.txt"), []byte("api\n"))
	run(t, repo, "git", "add", "packages/api/api.txt")
	write(t, filepath.Join(repo, ".gitignore"), []byte("build/\n"))
	run(t, repo, "git", "add", ".gitignore")
	run(t, repo, "git", "commit", "-m", "add api package and ignore build")
	write(t, filepath.Join(subdir, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-flash\"\n"))

	sess, err := Start(subdir, "subdir-config")
	if err != nil {
		t.Fatalf("Start from subdir: %v", err)
	}
	write(t, filepath.Join(sess.Path, "build", "artifact.bin"), []byte("binary\n"))

	got, err := CountChanges(sess.Path, sess.OriginalWorkspace, sess.OriginalHeadCommit)
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if got.ChangedFiles != 0 || got.IgnoredFiles != 1 || got.Commits != 0 {
		t.Fatalf("CountChanges = %+v, want only the genuinely ignored build artifact counted", got)
	}
}

func TestStartFromSubdirectoryCopiesLocalConfigToMatchingSubdirectory(t *testing.T) {
	repo := newGitRepo(t)
	subdir := filepath.Join(repo, "packages", "api")
	write(t, filepath.Join(subdir, "api.txt"), []byte("api\n"))
	run(t, repo, "git", "add", "packages/api/api.txt")
	run(t, repo, "git", "commit", "-m", "add api package")
	write(t, filepath.Join(repo, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-pro\"\n"))
	write(t, filepath.Join(subdir, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-flash\"\n"))

	sess, err := Start(subdir, "subdir")
	if err != nil {
		t.Fatalf("Start from subdir: %v", err)
	}
	if got := read(t, filepath.Join(sess.Path, "packages", "api", ".whale", "config.local.toml")); got != "model = \"deepseek-v4-flash\"\n" {
		t.Fatalf("copied subdir config = %q", got)
	}
	if _, err := os.Stat(filepath.Join(sess.Path, ".whale", "config.local.toml")); !os.IsNotExist(err) {
		t.Fatalf("root local config should not be copied from subdir, err=%v", err)
	}
	if got := strings.TrimSpace(gitOut(t, sess.Path, "status", "--porcelain")); got != "" {
		t.Fatalf("new worktree should remain clean after copying subdir local config, status:\n%s", got)
	}
	exclude := read(t, filepath.Join(repo, ".git", "info", "exclude"))
	if !strings.Contains(exclude, "packages/api/.whale/config.local.toml") {
		t.Fatalf("git info/exclude should contain subdir local config, got:\n%s", exclude)
	}
}

func TestStartCreatesBranchWhenManagedNameExistsOnlyAsTag(t *testing.T) {
	repo := newGitRepo(t)
	run(t, repo, "git", "tag", "worktree-feature")

	sess, err := Start(repo, "feature")
	if err != nil {
		t.Fatalf("Start feature with same-named tag: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, sess.Path, "branch", "--show-current")); got != "worktree-feature" {
		t.Fatalf("expected managed local branch checkout, got %q", got)
	}
	if got := strings.TrimSpace(gitOut(t, repo, "rev-parse", "--verify", "refs/heads/worktree-feature")); got == "" {
		t.Fatal("expected local managed branch to be created")
	}
}

func TestStartDoesNotDirtyParentRepoWithManagedWorktreePath(t *testing.T) {
	repo := newGitRepo(t)
	if got := strings.TrimSpace(gitOut(t, repo, "status", "--porcelain")); got != "" {
		t.Fatalf("test repo should start clean, status:\n%s", got)
	}

	if _, err := Start(repo, "isolated"); err != nil {
		t.Fatalf("Start isolated: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, repo, "status", "--porcelain")); got != "" {
		t.Fatalf("parent repo should remain clean after managed worktree create, status:\n%s", got)
	}
	if got := strings.TrimSpace(gitOut(t, repo, "check-ignore", ".whale/worktrees/isolated")); got != ".whale/worktrees/isolated" {
		t.Fatalf("managed worktree path should be ignored, got %q", got)
	}
}

func TestStartFromLinkedWorktreeUsesCanonicalRoot(t *testing.T) {
	repo := newGitRepo(t)
	first, err := Start(repo, "first")
	if err != nil {
		t.Fatalf("Start first: %v", err)
	}
	second, err := Start(first.Path, "second")
	if err != nil {
		t.Fatalf("Start second: %v", err)
	}
	if !strings.HasPrefix(second.Path, filepath.Join(repo, ".whale", "worktrees")) {
		t.Fatalf("second worktree path should use canonical root, got %s", second.Path)
	}
}

func TestStartFromLinkedWorktreeCopiesInvokingLocalConfig(t *testing.T) {
	repo := newGitRepo(t)
	write(t, filepath.Join(repo, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-flash\"\n"))
	first, err := Start(repo, "first-config")
	if err != nil {
		t.Fatalf("Start first-config: %v", err)
	}
	write(t, filepath.Join(first.Path, ".whale", "config.local.toml"), []byte("model = \"deepseek-v4-pro\"\n"))

	second, err := Start(first.Path, "second-config")
	if err != nil {
		t.Fatalf("Start second-config: %v", err)
	}
	if got := read(t, filepath.Join(second.Path, ".whale", "config.local.toml")); got != "model = \"deepseek-v4-pro\"\n" {
		t.Fatalf("copied local config from wrong workspace: %q", got)
	}
}

func TestStartFromLinkedWorktreeUsesCurrentHeadAsBase(t *testing.T) {
	repo := newGitRepo(t)
	first, err := Start(repo, "base")
	if err != nil {
		t.Fatalf("Start base: %v", err)
	}
	write(t, filepath.Join(first.Path, "base-work.txt"), []byte("base work\n"))
	run(t, first.Path, "git", "add", "base-work.txt")
	run(t, first.Path, "git", "commit", "-m", "base work")
	baseHead := strings.TrimSpace(gitOut(t, first.Path, "rev-parse", "HEAD"))

	second, err := Start(first.Path, "from-base")
	if err != nil {
		t.Fatalf("Start from linked worktree: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, second.Path, "rev-parse", "HEAD")); got != baseHead {
		t.Fatalf("nested worktree base = %s, want current checkout head %s", got, baseHead)
	}
	if got := read(t, filepath.Join(second.Path, "base-work.txt")); got != "base work\n" {
		t.Fatalf("expected nested worktree to contain current checkout commit, got %q", got)
	}
}

func TestStartUsesExistingBranchWithoutResettingIt(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "kept")
	if err != nil {
		t.Fatalf("Start kept: %v", err)
	}
	write(t, filepath.Join(sess.Path, "work.txt"), []byte("committed work\n"))
	run(t, sess.Path, "git", "add", "work.txt")
	run(t, sess.Path, "git", "commit", "-m", "worktree work")
	branchHead := strings.TrimSpace(gitOut(t, sess.Path, "rev-parse", "HEAD"))
	if err := runGit(repo, "worktree", "remove", "--force", sess.Path); err != nil {
		t.Fatalf("remove worktree directory: %v", err)
	}

	recreated, err := Start(repo, "kept")
	if err != nil {
		t.Fatalf("Start kept again: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, recreated.Path, "rev-parse", "HEAD")); got != branchHead {
		t.Fatalf("existing branch was reset: got %s want %s", got, branchHead)
	}
	if got := read(t, filepath.Join(recreated.Path, "work.txt")); got != "committed work\n" {
		t.Fatalf("expected committed work in recreated worktree, got %q", got)
	}
}

func TestStatusAndRemoveAcceptFlattenedNamesFromList(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "feature/test")
	if err != nil {
		t.Fatalf("Start nested: %v", err)
	}
	items, err := List(repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Name != "feature+test" {
		t.Fatalf("expected flattened listed name, got %+v", items)
	}
	status, err := Status(repo, items[0].Name)
	if err != nil {
		t.Fatalf("Status flattened: %v", err)
	}
	if status.Path != sess.Path {
		t.Fatalf("status path = %s, want %s", status.Path, sess.Path)
	}
	if _, err := Remove(repo, items[0].Name, false); err != nil {
		t.Fatalf("Remove flattened: %v", err)
	}
}

func TestCanonicalRepoRootUsesSubmoduleWorktreeRoot(t *testing.T) {
	super := newGitRepo(t)
	sub := newGitRepo(t)
	run(t, super, "git", "-c", "protocol.file.allow=always", "submodule", "add", sub, "deps/sub")
	run(t, super, "git", "commit", "-m", "add submodule")

	subWorktree := filepath.Join(super, "deps", "sub")
	root, err := CanonicalRepoRoot(subWorktree)
	if err != nil {
		t.Fatalf("CanonicalRepoRoot submodule: %v", err)
	}
	if root != subWorktree {
		t.Fatalf("root = %s, want submodule worktree %s", root, subWorktree)
	}
	sess, err := Start(subWorktree, "sub-work")
	if err != nil {
		t.Fatalf("Start in submodule: %v", err)
	}
	wantPrefix := filepath.Join(subWorktree, ".whale", "worktrees")
	if !strings.HasPrefix(sess.Path, wantPrefix) {
		t.Fatalf("submodule worktree path = %s, want under %s", sess.Path, wantPrefix)
	}
}

func TestListStatusAndRemoveWorktrees(t *testing.T) {
	repo := newGitRepo(t)
	clean, err := Start(repo, "clean")
	if err != nil {
		t.Fatalf("Start clean: %v", err)
	}
	dirty, err := Start(repo, "dirty")
	if err != nil {
		t.Fatalf("Start dirty: %v", err)
	}
	write(t, filepath.Join(dirty.Path, "scratch.txt"), []byte("dirty\n"))

	items, err := List(repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]Entry{}
	for _, item := range items {
		byName[item.Name] = item
	}
	if byName["clean"].Missing || byName["clean"].Dirty || byName["clean"].Head == "" || byName["clean"].Branch != "worktree-clean" {
		t.Fatalf("unexpected clean entry: %+v", byName["clean"])
	}
	if !byName["dirty"].Dirty {
		t.Fatalf("expected dirty entry, got %+v", byName["dirty"])
	}

	status, err := Status(repo, "clean")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Name != "clean" || status.Path != clean.Path {
		t.Fatalf("unexpected status: %+v", status)
	}

	if _, err := Remove(repo, "dirty", false); err == nil || !strings.Contains(err.Error(), "has changes") {
		t.Fatalf("expected dirty remove guard, got %v", err)
	}
	res, err := Remove(repo, "dirty", true)
	if err != nil {
		t.Fatalf("Remove dirty force: %v", err)
	}
	if !res.BranchDeleted {
		t.Fatalf("expected branch deleted, got %+v", res)
	}
	if _, err := os.Stat(dirty.Path); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree should be removed, stat err=%v", err)
	}

	res, err = Remove(repo, "clean", false)
	if err != nil {
		t.Fatalf("Remove clean: %v", err)
	}
	if !res.BranchDeleted {
		t.Fatalf("expected clean branch deleted, got %+v", res)
	}
}

func TestRemoveRejectsCurrentWorktree(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "current")
	if err != nil {
		t.Fatalf("Start current: %v", err)
	}
	if _, err := Remove(sess.Path, "current", true); err == nil || !strings.Contains(err.Error(), "current worktree") {
		t.Fatalf("expected current worktree guard, got %v", err)
	}
}

func TestRemoveDoesNotForceDeleteUnmergedBranchByDefault(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "unmerged")
	if err != nil {
		t.Fatalf("Start unmerged: %v", err)
	}
	write(t, filepath.Join(sess.Path, "work.txt"), []byte("committed work\n"))
	run(t, sess.Path, "git", "add", "work.txt")
	run(t, sess.Path, "git", "commit", "-m", "worktree work")

	res, err := Remove(repo, "unmerged", false)
	if err != nil {
		t.Fatalf("Remove unmerged clean worktree: %v", err)
	}
	if res.BranchDeleted {
		t.Fatalf("branch should not be deleted without force: %+v", res)
	}
	if !strings.Contains(res.BranchWarning, "not fully merged") {
		t.Fatalf("expected non-force branch delete warning, got %q", res.BranchWarning)
	}
	if branch := gitOut(t, repo, "rev-parse", "--verify", "worktree-unmerged"); strings.TrimSpace(branch) == "" {
		t.Fatal("expected unmerged branch to remain")
	}

	if err := runGit(repo, "branch", "-D", "worktree-unmerged"); err != nil {
		t.Fatalf("cleanup branch: %v", err)
	}
}

func TestRemoveForceDeletesUnmergedBranch(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "unmerged-force")
	if err != nil {
		t.Fatalf("Start unmerged-force: %v", err)
	}
	write(t, filepath.Join(sess.Path, "work.txt"), []byte("committed work\n"))
	run(t, sess.Path, "git", "add", "work.txt")
	run(t, sess.Path, "git", "commit", "-m", "worktree work")

	res, err := Remove(repo, "unmerged-force", true)
	if err != nil {
		t.Fatalf("Remove unmerged force: %v", err)
	}
	if !res.BranchDeleted {
		t.Fatalf("expected force branch delete, got %+v", res)
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "worktree-unmerged-force")
	cmd.Dir = repo
	if err := cmd.Run(); err == nil {
		t.Fatal("expected force-deleted branch to be gone")
	}
}

func TestListRemoveAndStartRecoverDeletedRegisteredWorktree(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "ghost")
	if err != nil {
		t.Fatalf("Start ghost: %v", err)
	}
	if err := os.RemoveAll(sess.Path); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}

	items, err := List(repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *Entry
	for i := range items {
		if items[i].Name == "ghost" {
			found = &items[i]
		}
	}
	if found == nil || !found.Missing {
		t.Fatalf("expected missing ghost entry in List, got %+v", items)
	}

	res, err := Remove(repo, "ghost", false)
	if err != nil {
		t.Fatalf("Remove ghost: %v", err)
	}
	if !res.BranchDeleted {
		t.Fatalf("expected branch deleted after pruning ghost, got %+v", res)
	}

	if _, err := Start(repo, "ghost"); err != nil {
		t.Fatalf("Start after recovery: %v", err)
	}
}

func TestRemoveCorruptWorktreeDirRequiresForce(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "corrupt")
	if err != nil {
		t.Fatalf("Start corrupt: %v", err)
	}
	// Corrupt the worktree by removing its .git pointer so isGitWorktree fails
	// while the directory and files remain on disk.
	if err := os.Remove(filepath.Join(sess.Path, ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}

	if _, err := Remove(repo, "corrupt", false); err == nil || !strings.Contains(err.Error(), "use --force") {
		t.Fatalf("expected force-required error for corrupt worktree, got %v", err)
	}
	if _, statErr := os.Stat(sess.Path); statErr != nil {
		t.Fatalf("expected corrupt dir to remain without --force, stat err=%v", statErr)
	}

	res, err := Remove(repo, "corrupt", true)
	if err != nil {
		t.Fatalf("Remove corrupt force: %v", err)
	}
	if !res.BranchDeleted {
		t.Fatalf("expected branch deleted under force, got %+v", res)
	}
	if _, statErr := os.Stat(sess.Path); !os.IsNotExist(statErr) {
		t.Fatalf("expected corrupt dir removed under --force, stat err=%v", statErr)
	}

	if _, err := Start(repo, "corrupt"); err != nil {
		t.Fatalf("Start after corrupt cleanup: %v", err)
	}
}

func TestStartPrunesStaleRegistrationBeforeReadd(t *testing.T) {
	repo := newGitRepo(t)
	sess, err := Start(repo, "stale")
	if err != nil {
		t.Fatalf("Start stale: %v", err)
	}
	if err := os.RemoveAll(sess.Path); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	again, err := Start(repo, "stale")
	if err != nil {
		t.Fatalf("Start after stale registration: %v", err)
	}
	if !again.Created {
		t.Fatalf("expected new creation after pruning, got %+v", again)
	}
}

func TestStatusReportsMissingWorktree(t *testing.T) {
	repo := newGitRepo(t)
	got, err := Status(repo, "missing")
	if err != nil {
		t.Fatalf("Status missing: %v", err)
	}
	if !got.Missing || got.Name != "missing" {
		t.Fatalf("expected missing entry, got %+v", got)
	}
}

func TestResolveSessionComputesSessionWithoutSideEffects(t *testing.T) {
	repo := newGitRepo(t)
	before := read(t, filepath.Join(repo, ".git", "info", "exclude"))
	wtRoot := filepath.Join(repo, ".whale", "worktrees")
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Fatalf("worktrees dir should not exist before ResolveSession: %v", err)
	}

	sess, err := ResolveSession(repo, "feature/test")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if sess.Created {
		t.Fatal("ResolveSession must not mark the session as created")
	}
	wantPath := filepath.Join(repo, ".whale", "worktrees", "feature+test")
	if sess.Path != wantPath || sess.Branch != "worktree-feature+test" {
		t.Fatalf("unexpected session: %+v", sess)
	}
	if sess.OriginalWorkspace != repo || sess.OriginalBranch != "main" || sess.OriginalHeadCommit == "" {
		t.Fatalf("unexpected original metadata: %+v", sess)
	}
	if got := read(t, filepath.Join(repo, ".git", "info", "exclude")); got != before {
		t.Fatalf("ResolveSession must not touch git info/exclude:\nbefore: %q\nafter:  %q", before, got)
	}
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Fatalf("ResolveSession must not create the worktrees dir: %v", err)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("ResolveSession must not create the worktree: %v", err)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test User")
	// Keep test repos hermetic: disable commit/tag signing so these tests do
	// not depend on the developer's gpg setup (commit.gpgsign / tag.gpgsign),
	// which otherwise blocks headless runs waiting for a passphrase.
	run(t, dir, "git", "config", "commit.gpgsign", "false")
	run(t, dir, "git", "config", "tag.gpgsign", "false")
	write(t, filepath.Join(dir, "README.md"), []byte("test\n"))
	run(t, dir, "git", "add", "README.md")
	run(t, dir, "git", "commit", "-m", "initial")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve temp repo: %v", err)
	}
	return resolved
}

func run(t *testing.T, cwd, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, string(out))
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func gitOut(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
