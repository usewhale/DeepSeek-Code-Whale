package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
)

func TestListResumeChoicesShowsReadableConversationTable(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{Title: "Saved title", Branch: "main"}); err != nil {
		t.Fatalf("save s1 meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte("{\"Role\":\"user\",\"Text\":\"fallback\"}\n"), 0o600); err != nil {
		t.Fatalf("write s1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s2.jsonl"), []byte("{\"Role\":\"user\",\"Text\":\"first prompt\"}\n"), 0o600); err != nil {
		t.Fatalf("write s2: %v", err)
	}
	now := time.Now()
	_ = os.Chtimes(filepath.Join(sessionsDir, "s1.jsonl"), now.Add(-2*time.Minute), now.Add(-2*time.Minute))
	_ = os.Chtimes(filepath.Join(sessionsDir, "s2.jsonl"), now.Add(-time.Minute), now.Add(-time.Minute))

	app := &App{
		sessionsDir: sessionsDir,
		sessionID:   "s2",
	}
	out, err := app.ListResumeChoices(10)
	if err != nil {
		t.Fatalf("list resume choices: %v", err)
	}
	rendered := strings.Join(out, "\n")
	if !strings.Contains(rendered, "Updated") || !strings.Contains(rendered, "Branch") || !strings.Contains(rendered, "Conversation") {
		t.Fatalf("expected readable table headers, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "*  1)") || !strings.Contains(rendered, "first prompt") {
		t.Fatalf("expected current latest session with fallback title, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Saved title") || !strings.Contains(rendered, "main") {
		t.Fatalf("expected saved title and branch, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, " - ") {
		t.Fatalf("expected empty branch placeholder, got:\n%s", rendered)
	}
}

func TestApplyResumeChoiceBlocksCrossWorkspaceID(t *testing.T) {
	current := t.TempDir()
	other := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	writeResumeTestSession(t, sessionsDir, "s1", "from another workspace")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{Workspace: other, Branch: "main"}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	app := &App{
		sessionsDir:   sessionsDir,
		workspaceRoot: current,
		sessionID:     "current",
	}
	out, err := app.ApplyResumeChoice("s1")
	if err != nil {
		t.Fatalf("ApplyResumeChoice: %v", err)
	}
	if out.Resumed {
		t.Fatal("expected cross-workspace resume to be blocked")
	}
	if app.SessionID() != "current" {
		t.Fatalf("session changed to %q", app.SessionID())
	}
	if !strings.Contains(out.Message, "This conversation is from a different directory.") ||
		!strings.Contains(out.Message, "To resume, run:") ||
		!strings.Contains(out.Message, "cd ") ||
		!strings.Contains(out.Message, " resume s1") {
		t.Fatalf("unexpected cross-workspace message:\n%s", out.Message)
	}
	meta, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.Workspace != other {
		t.Fatalf("workspace was mutated to %q, want %q", meta.Workspace, other)
	}
}

func TestApplyResumeChoiceUsesWorkspaceFilteredNumbering(t *testing.T) {
	current := t.TempDir()
	other := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	writeResumeTestSession(t, sessionsDir, "hidden-newer", "other workspace")
	writeResumeTestSession(t, sessionsDir, "visible-older", "current workspace")
	if err := session.SaveSessionMeta(sessionsDir, "hidden-newer", session.SessionMeta{Workspace: other, Branch: "other"}); err != nil {
		t.Fatalf("save hidden meta: %v", err)
	}
	if err := session.SaveSessionMeta(sessionsDir, "visible-older", session.SessionMeta{Workspace: current, Branch: "current"}); err != nil {
		t.Fatalf("save visible meta: %v", err)
	}
	now := time.Now()
	_ = os.Chtimes(filepath.Join(sessionsDir, "hidden-newer.jsonl"), now.Add(-time.Minute), now.Add(-time.Minute))
	_ = os.Chtimes(filepath.Join(sessionsDir, "visible-older.jsonl"), now.Add(-2*time.Minute), now.Add(-2*time.Minute))

	app := &App{
		sessionsDir:   sessionsDir,
		workspaceRoot: current,
		sessionID:     "current",
	}
	choices, err := app.ListResumeChoices(20)
	if err != nil {
		t.Fatalf("ListResumeChoices: %v", err)
	}
	rendered := strings.Join(choices, "\n")
	if strings.Contains(rendered, "other workspace") || strings.Contains(rendered, "other") {
		t.Fatalf("hidden workspace session should not be rendered:\n%s", rendered)
	}
	if !strings.Contains(rendered, "1)") || !strings.Contains(rendered, "current workspace") {
		t.Fatalf("visible session should be numbered as first choice:\n%s", rendered)
	}

	out, err := app.ApplyResumeChoice("1")
	if err != nil {
		t.Fatalf("ApplyResumeChoice: %v", err)
	}
	if !out.Resumed {
		t.Fatalf("expected filtered visible session to resume, got:\n%s", out.Message)
	}
	if app.SessionID() != "visible-older" {
		t.Fatalf("session = %q, want visible-older", app.SessionID())
	}
}

func TestResolveResumeWorktreeReturnsSessionWorktree(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := store.DefaultSessionsDir(dataDir)
	worktreePath := t.TempDir()
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace:          worktreePath,
		Branch:             "worktree-feature",
		WorktreeName:       "feature",
		WorktreePath:       worktreePath,
		WorktreeBranch:     "worktree-feature",
		OriginalWorkspace:  "/tmp/original",
		OriginalBranch:     "main",
		OriginalHeadCommit: "abc123",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	got, err := ResolveResumeWorktree(Config{DataDir: dataDir}, StartOptions{SessionID: "s1"}, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveResumeWorktree: %v", err)
	}
	if got.Session.Name != "feature" || got.Session.Path != worktreePath || got.Session.Branch != "worktree-feature" || got.Session.OriginalWorkspace != "/tmp/original" {
		t.Fatalf("unexpected worktree session: %+v", got)
	}
}

func TestResolveResumeWorktreeClearsMissingPath(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := store.DefaultSessionsDir(dataDir)
	missing := filepath.Join(t.TempDir(), "missing")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace:          missing,
		Branch:             "worktree-missing",
		WorktreeName:       "missing",
		WorktreePath:       missing,
		WorktreeBranch:     "worktree-missing",
		OriginalWorkspace:  "/tmp/original",
		OriginalBranch:     "main",
		OriginalHeadCommit: "abc123",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	got, err := ResolveResumeWorktree(Config{DataDir: dataDir}, StartOptions{SessionID: "s1"}, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveResumeWorktree: %v", err)
	}
	if got.Session.Path != "" {
		t.Fatalf("expected missing worktree to resume normally, got %+v", got)
	}
	meta, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.Workspace != "/tmp/original" || meta.Branch != "main" {
		t.Fatalf("unexpected fallback meta: %+v", meta)
	}
	if meta.WorktreeName != "" || meta.WorktreePath != "" || meta.WorktreeBranch != "" || meta.OriginalWorkspace != "" || meta.OriginalBranch != "" || meta.OriginalHeadCommit != "" {
		t.Fatalf("expected worktree meta to be cleared: %+v", meta)
	}
}

func TestResolveResumeWorktreeSkipsPicker(t *testing.T) {
	got, err := ResolveResumeWorktree(Config{DataDir: t.TempDir()}, StartOptions{ResumeMenu: true}, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveResumeWorktree picker: %v", err)
	}
	if got.Session.Path != "" {
		t.Fatalf("picker should not resolve worktree, got %+v", got)
	}
}

func TestResolveResumeWorktreeDecisionIsReadOnly(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := store.DefaultSessionsDir(dataDir)
	missing := filepath.Join(t.TempDir(), "missing")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace:          missing,
		Branch:             "worktree-missing",
		WorktreeName:       "missing",
		WorktreePath:       missing,
		WorktreeBranch:     "worktree-missing",
		OriginalWorkspace:  "/tmp/original",
		OriginalBranch:     "main",
		OriginalHeadCommit: "abc123",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	before, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load before meta: %v", err)
	}

	got, err := ResolveResumeWorktreeDecision(Config{DataDir: dataDir}, StartOptions{SessionID: "s1"}, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveResumeWorktreeDecision: %v", err)
	}
	if !got.MissingWorktree {
		t.Fatalf("expected missing-worktree intent, got %+v", got)
	}
	after, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load after meta: %v", err)
	}
	if after != before {
		t.Fatalf("read-only decision rewrote sidecar:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestCommitMissingWorktreeCleanupClearsMeta(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := store.DefaultSessionsDir(dataDir)
	missing := filepath.Join(t.TempDir(), "missing")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace:          missing,
		Branch:             "worktree-missing",
		WorktreeName:       "missing",
		WorktreePath:       missing,
		WorktreeBranch:     "worktree-missing",
		OriginalWorkspace:  "/tmp/original",
		OriginalBranch:     "main",
		OriginalHeadCommit: "abc123",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	if err := CommitMissingWorktreeCleanup(sessionsDir, "s1", t.TempDir()); err != nil {
		t.Fatalf("CommitMissingWorktreeCleanup: %v", err)
	}
	meta, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.Workspace != "/tmp/original" || meta.Branch != "main" {
		t.Fatalf("unexpected fallback meta: %+v", meta)
	}
	if meta.WorktreeName != "" || meta.WorktreePath != "" || meta.WorktreeBranch != "" || meta.OriginalWorkspace != "" || meta.OriginalBranch != "" || meta.OriginalHeadCommit != "" {
		t.Fatalf("expected worktree meta to be cleared: %+v", meta)
	}
}

func TestResumeCommandWindowsQuotesWorkspaceAndExecutableWithSpaces(t *testing.T) {
	got := resumeCommandFor("windows", `C:\Whale Repro Home\workspace original`, "s1", `C:\Program Files\Whale Repro\whale.exe`)
	want := `cmd /v:on /c "set whale_resume_workspace=C:\Whale Repro Home\workspace original&&set whale_resume_bin=C:\Program Files\Whale Repro\whale.exe&&set whale_resume_session=s1&&cd /d "!whale_resume_workspace!"&&"!whale_resume_bin!" resume "!whale_resume_session!""`
	if got != want {
		t.Fatalf("resume command:\n got: %q\nwant: %q", got, want)
	}
}

func TestResumeCommandWindowsQuotesUnsafeSessionID(t *testing.T) {
	got := resumeCommandFor("windows", `C:\work`, "s&1", `C:\Program Files\Whale Repro\whale.exe`)
	want := `cmd /v:on /c "set whale_resume_workspace=C:\work&&set whale_resume_bin=C:\Program Files\Whale Repro\whale.exe&&set whale_resume_session=s^&1&&cd /d "!whale_resume_workspace!"&&"!whale_resume_bin!" resume "!whale_resume_session!""`
	if got != want {
		t.Fatalf("resume command:\n got: %q\nwant: %q", got, want)
	}
}

func TestResumeCommandWindowsEscapesCmdExpansion(t *testing.T) {
	got := resumeCommandFor("windows", `C:\Users\%USERNAME%\workspace`, "s%USERNAME%", `C:\Program Files\Whale %USERNAME%\whale.exe`)
	want := `cmd /v:on /c "set whale_resume_workspace=C:\Users\^%USERNAME^%\workspace&&set whale_resume_bin=C:\Program Files\Whale ^%USERNAME^%\whale.exe&&set whale_resume_session=s^%USERNAME^%&&cd /d "!whale_resume_workspace!"&&"!whale_resume_bin!" resume "!whale_resume_session!""`
	if got != want {
		t.Fatalf("resume command:\n got: %q\nwant: %q", got, want)
	}
}

func TestCmdSetValueEscapesCmdMetacharacters(t *testing.T) {
	tests := map[string]string{
		`s%USERNAME%`:    `s^%USERNAME^%`,
		`s&1`:            `s^&1`,
		`s^1`:            `s^^1`,
		`s!USERNAME!`:    `s^^!USERNAME^^!`,
		`C:\work(a)\out`: `C:\work^(a^)\out`,
	}
	for input, want := range tests {
		if got := cmdSetValue(input); got != want {
			t.Fatalf("cmdSetValue(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResumeCommandPOSIXQuotesWorkspaceExecutableAndUnsafeSessionID(t *testing.T) {
	got := resumeCommandFor("darwin", `/tmp/Whale Repro/workspace original`, "s&1", `/tmp/Whale Repro/whale`)
	want := `cd '/tmp/Whale Repro/workspace original' && '/tmp/Whale Repro/whale' resume 's&1'`
	if got != want {
		t.Fatalf("resume command:\n got: %q\nwant: %q", got, want)
	}
}

func TestApplyResumeChoiceAllowsSameWorkspace(t *testing.T) {
	current := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	writeResumeTestSession(t, sessionsDir, "s1", "same workspace")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{Workspace: current, Branch: "main"}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	app := &App{
		sessionsDir:   sessionsDir,
		workspaceRoot: current,
		sessionID:     "current",
	}
	out, err := app.ApplyResumeChoice("1")
	if err != nil {
		t.Fatalf("ApplyResumeChoice: %v", err)
	}
	if !out.Resumed {
		t.Fatalf("expected same-workspace resume, got message:\n%s", out.Message)
	}
	if app.SessionID() != "s1" {
		t.Fatalf("session = %q, want s1", app.SessionID())
	}
}

func TestApplyResumeChoiceAllowsLegacySessionWithoutWorkspace(t *testing.T) {
	current := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	writeResumeTestSession(t, sessionsDir, "s1", "legacy workspace")

	app := &App{
		sessionsDir:   sessionsDir,
		workspaceRoot: current,
		sessionID:     "current",
	}
	out, err := app.ApplyResumeChoice("1")
	if err != nil {
		t.Fatalf("ApplyResumeChoice: %v", err)
	}
	if !out.Resumed {
		t.Fatalf("expected legacy session to resume, got message:\n%s", out.Message)
	}
	meta, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.Workspace != "" {
		t.Fatalf("legacy workspace = %q, want empty until a new turn is written", meta.Workspace)
	}
}

func TestApplyResumeChoiceDoesNotRewriteExistingSessionMeta(t *testing.T) {
	current := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	writeResumeTestSession(t, sessionsDir, "s1", "same workspace")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace: current,
		Branch:    "original-branch",
		Summary:   "existing summary",
		TurnCount: 4,
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	before, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load before meta: %v", err)
	}

	app := &App{
		sessionsDir:   sessionsDir,
		workspaceRoot: current,
		sessionID:     "current",
		branch:        "new-branch",
	}
	out, err := app.ApplyResumeChoice("1")
	if err != nil {
		t.Fatalf("ApplyResumeChoice: %v", err)
	}
	if !out.Resumed {
		t.Fatalf("expected resume, got message:\n%s", out.Message)
	}

	after, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load after meta: %v", err)
	}
	if after != before {
		t.Fatalf("resume rewrote meta:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestNewResumeMenuDoesNotPatchMostRecentSessionWorkspace(t *testing.T) {
	current := t.TempDir()
	other := t.TempDir()
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	writeResumeTestSession(t, sessionsDir, "s1", "do not mutate")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{Workspace: other, Branch: "main"}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	t.Chdir(current)
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")

	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	app, err := New(context.Background(), cfg, StartOptions{ResumeMenu: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer app.Close()

	meta, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.Workspace != other {
		t.Fatalf("resume menu mutated workspace to %q, want %q", meta.Workspace, other)
	}
}

func TestNewDirectResumeBlocksCrossWorkspace(t *testing.T) {
	current := t.TempDir()
	other := t.TempDir()
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	writeResumeTestSession(t, sessionsDir, "s1", "direct resume")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{Workspace: other}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	t.Chdir(current)
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")

	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	_, err := New(context.Background(), cfg, StartOptions{SessionID: "s1"})
	if err == nil {
		t.Fatal("expected cross-workspace direct resume to be blocked")
	}
	if !IsCrossWorkspaceResumeError(err) {
		t.Fatalf("expected cross-workspace error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "This conversation is from a different directory.") {
		t.Fatalf("unexpected error message:\n%s", err)
	}
}

func TestNewDirectResumeKeepsSessionMetaUnchangedAndAppliesRuntimeConfig(t *testing.T) {
	current := t.TempDir()
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	writeResumeTestSession(t, sessionsDir, "s1", "resume without rewrite")
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace: current,
		Branch:    "saved-branch",
		Summary:   "saved summary",
		TurnCount: 3,
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	before, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load before meta: %v", err)
	}

	t.Chdir(current)
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")

	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	cfg.ReasoningEffort = "max"
	cfg.ThinkingEnabled = false
	app, err := New(context.Background(), cfg, StartOptions{SessionID: "s1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer app.Close()

	if app.SessionID() != "s1" {
		t.Fatalf("session = %q, want s1", app.SessionID())
	}
	if app.ReasoningEffort() != "max" {
		t.Fatalf("effort: want max override, got %s", app.ReasoningEffort())
	}
	if app.ThinkingEnabled() {
		t.Fatal("thinking: want false override on resumed runtime")
	}

	after, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load after meta: %v", err)
	}
	if after != before {
		t.Fatalf("resume init rewrote meta:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abcdef", 4); got != "abc…" {
		t.Fatalf("unexpected ascii truncation: %q", got)
	}
	if got := truncateRunes("中文标题", 3); got != "中文…" {
		t.Fatalf("unexpected unicode truncation: %q", got)
	}
}

func writeResumeTestSession(t *testing.T, sessionsDir, id, text string) {
	t.Helper()
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, id+".jsonl"), []byte(`{"Role":"user","Text":"`+text+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
}
