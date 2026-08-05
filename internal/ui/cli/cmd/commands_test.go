package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/usewhale/whale/internal/app"
	"github.com/usewhale/whale/internal/llm/deepseek"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/runtime/protocol"
	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
	whaleworktree "github.com/usewhale/whale/internal/worktree"
)

// localExecConfig returns a default config pinned to the local web_search mode:
// exec tests mock the chat-completions endpoint, and the default (auto) mode
// would route deepseek-v4-flash through the Responses API.
func localExecConfig() app.Config {
	cfg := app.DefaultConfig()
	cfg.DeepSeekWebSearch = deepseek.WebSearchModeLocal
	return cfg
}

func TestRunSetupSavesCredentials(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	in := strings.NewReader("sk-1234567890abcdef1234\n")

	if err := runSetup(&out, in, dir); err != nil {
		t.Fatalf("runSetup: %v", err)
	}

	creds, err := app.LoadCredentials(dir)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds.DeepSeekAPIKey != "sk-1234567890abcdef1234" {
		t.Fatalf("deepseek_api_key: got %q", creds.DeepSeekAPIKey)
	}
	if !strings.Contains(out.String(), filepath.Join(dir, "credentials.json")) {
		t.Fatalf("expected output to mention credentials path, got %q", out.String())
	}
}

func TestRunSetupRejectsInvalidKey(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	in := strings.NewReader("invalid\n")

	if err := runSetup(&out, in, dir); err == nil {
		t.Fatal("expected invalid key error")
	}
}

func TestPluginCLIInstallListInspectUninstall(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	pluginDir := t.TempDir()
	writeCLIFile(t, filepath.Join(pluginDir, "skills", "demo", "SKILL.md"), []byte("# Demo\n"))
	writeCLIFile(t, filepath.Join(pluginDir, "whale-plugin.toml"), []byte(`id = "demo-cli"
name = "Demo CLI"
version = "0.1.0"
description = "CLI installed plugin."
authors = ["Whale"]

[components]
skills = "./skills"
`))
	cfg := app.DefaultConfig()
	cfg.DataDir = dataDir

	var out bytes.Buffer
	if err := runPluginInstall(&out, cfg, pluginDir); err != nil {
		t.Fatalf("runPluginInstall: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "installed plugin: demo-cli@0.1.0") {
		t.Fatalf("unexpected install output:\n%s", got)
	}

	out.Reset()
	if err := runPluginList(&out, cfg); err != nil {
		t.Fatalf("runPluginList: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "memory") || !strings.Contains(got, "demo-cli") || !strings.Contains(got, "disabled") {
		t.Fatalf("unexpected list output:\n%s", got)
	}

	out.Reset()
	if err := runPluginSetEnabled(&out, cfg, "demo-cli", true); err != nil {
		t.Fatalf("runPluginSetEnabled(true): %v", err)
	}
	if got := out.String(); !strings.Contains(got, "enabled plugin: demo-cli") {
		t.Fatalf("unexpected enable output:\n%s", got)
	}

	out.Reset()
	cfg, err = app.LoadAndApplyConfig(app.Config{DataDir: dataDir}, workspace)
	if err != nil {
		t.Fatalf("LoadAndApplyConfig: %v", err)
	}
	if err := runPluginList(&out, cfg); err != nil {
		t.Fatalf("runPluginList after enable: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "demo-cli") || !strings.Contains(got, "enabled") {
		t.Fatalf("unexpected enabled list output:\n%s", got)
	}

	out.Reset()
	if err := runPluginSetEnabled(&out, cfg, "demo-cli", false); err != nil {
		t.Fatalf("runPluginSetEnabled(false): %v", err)
	}
	if got := out.String(); !strings.Contains(got, "disabled plugin: demo-cli") {
		t.Fatalf("unexpected disable output:\n%s", got)
	}
	cfg, err = app.LoadAndApplyConfig(app.Config{DataDir: dataDir}, workspace)
	if err != nil {
		t.Fatalf("LoadAndApplyConfig after disable: %v", err)
	}

	out.Reset()
	if err := runPluginInspect(&out, cfg, "demo-cli"); err != nil {
		t.Fatalf("runPluginInspect: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Demo CLI") || !strings.Contains(got, "components:") || !strings.Contains(got, "skills: ./skills") {
		t.Fatalf("unexpected inspect output:\n%s", got)
	}
	if got := out.String(); !strings.Contains(got, "status: disabled") {
		t.Fatalf("expected disabled inspect status after disable:\n%s", got)
	}

	out.Reset()
	if err := runPluginUninstall(&out, cfg, "demo-cli"); err != nil {
		t.Fatalf("runPluginUninstall: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "uninstalled plugin: demo-cli") {
		t.Fatalf("unexpected uninstall output:\n%s", got)
	}
	if err := runPluginInspect(&out, cfg, "demo-cli"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected inspect failure after uninstall, got %v", err)
	}
}

func TestPluginCLIRejectsBuiltinUninstall(t *testing.T) {
	cfg := app.DefaultConfig()
	cfg.DataDir = t.TempDir()
	var out bytes.Buffer
	if err := runPluginUninstall(&out, cfg, "memory"); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("expected built-in uninstall error, got %v", err)
	}
}

func TestRunDoctorReturnsExitErrorOnFailures(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("DEEPSEEK_BASE_URL", "http://127.0.0.1:1")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	err = runDoctor(&out, app.Config{DataDir: filepath.Join(dir, ".whale")})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("expected ExitError{1}, got %v", err)
	}
	if !strings.Contains(out.String(), "0 fail") && !strings.Contains(out.String(), "1 fail") {
		t.Fatalf("expected summary output, got %q", out.String())
	}
}

func TestRunDoctorOutputReportsWhaleHomeOverride(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("WHALE_HOME", dataDir)
	t.Setenv("DEEPSEEK_BASE_URL", "http://127.0.0.1:1")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	err = runDoctor(&out, app.Config{})
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("expected ExitError{1}, got %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "data dir: "+dataDir) || !strings.Contains(got, "data dir override") || !strings.Contains(got, "WHALE_HOME="+dataDir) {
		t.Fatalf("expected doctor output to report WHALE_HOME data dir, got:\n%s", got)
	}
}

func TestDoctorBadge(t *testing.T) {
	if got := doctorBadge(app.DoctorOK); got != "ok" {
		t.Fatalf("doctorBadge ok = %q", got)
	}
	if got := doctorBadge(app.DoctorWarn); got != "warn" {
		t.Fatalf("doctorBadge warn = %q", got)
	}
	if got := doctorBadge(app.DoctorFail); got != "fail" {
		t.Fatalf("doctorBadge fail = %q", got)
	}
}

func TestAppServerCommandRunsStdioProtocol(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = t.TempDir()
	root := newRootCmd(opts)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(`{"type":"intent","intent":{"kind":"shutdown"}}` + "\n"))
	root.SetArgs([]string{"app-server"})
	if err := root.Execute(); err != nil {
		t.Fatalf("app-server: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"type":"`+string(protocol.ServerMessageReady)+`"`) ||
		!strings.Contains(got, `"type":"`+string(protocol.ServerMessageClosed)+`"`) {
		t.Fatalf("expected ready and closed messages, got:\n%s", got)
	}
}

func TestPrepareCLIConfigLoadsConfigAndAppliesFlagOverride(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".whale"), 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	if err := app.SaveConfigFile(app.GlobalConfigPath(dataDir), app.FileConfig{Model: "deepseek-v4-pro"}); err != nil {
		t.Fatalf("save global config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("model", "deepseek-v4-flash"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.Model != "deepseek-v4-flash" {
		t.Fatalf("model: want CLI override, got %s", opts.cfg.Model)
	}
	if !opts.cfg.ModelExplicit {
		t.Fatal("model should be explicit")
	}
}

func TestPrepareCLIConfigPreservesConfiguredThinking(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	thinking := true
	if err := app.SaveConfigFile(app.GlobalConfigPath(dataDir), app.FileConfig{ThinkingEnabled: &thinking}); err != nil {
		t.Fatalf("save global config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if !opts.cfg.ThinkingEnabled {
		t.Fatal("thinking_enabled should stay true from config")
	}
}

func TestPrepareCLIConfigExplicitThinkingFalseOverridesConfig(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	thinking := true
	if err := app.SaveConfigFile(app.GlobalConfigPath(dataDir), app.FileConfig{ThinkingEnabled: &thinking}); err != nil {
		t.Fatalf("save global config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("thinking", "false"); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.ThinkingEnabled {
		t.Fatal("thinking_enabled should be overridden to false")
	}

	loaded, _, err := app.LoadConfigFile(app.GlobalConfigPath(dataDir))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if loaded.ThinkingEnabled == nil || !*loaded.ThinkingEnabled {
		t.Fatal("global config should remain unchanged")
	}
}

func TestPrepareCLIConfigExplicitThinkingTrueOverridesConfig(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	thinking := false
	if err := app.SaveConfigFile(app.GlobalConfigPath(dataDir), app.FileConfig{ThinkingEnabled: &thinking}); err != nil {
		t.Fatalf("save global config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("thinking", "true"); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if !opts.cfg.ThinkingEnabled {
		t.Fatal("thinking_enabled should be overridden to true")
	}
}

func TestPrepareCLIConfigExplicitEffortOverridesConfig(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	if err := app.SaveConfigFile(app.GlobalConfigPath(dataDir), app.FileConfig{ReasoningEffort: "high"}); err != nil {
		t.Fatalf("save global config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("effort", "max"); err != nil {
		t.Fatalf("set effort: %v", err)
	}
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort: want max, got %s", opts.cfg.ReasoningEffort)
	}
}

func TestPrepareCLIConfigDangerouslySkipPermissionsOverridesConfig(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".whale"), 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	if err := app.SaveConfigFile(filepath.Join(workspace, ".whale", app.ConfigFileName), app.FileConfig{
		Permissions: app.FilePermissionsConfig{Default: "ask"},
	}); err != nil {
		t.Fatalf("save project config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("dangerously-skip-permissions", "true"); err != nil {
		t.Fatalf("set dangerously-skip-permissions: %v", err)
	}
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.PermissionDefault != policy.PermissionAllow || !opts.cfg.AutoAcceptPermissions {
		t.Fatalf("dangerously skip permissions not applied: default=%s auto_accept=%v", opts.cfg.PermissionDefault, opts.cfg.AutoAcceptPermissions)
	}

	loaded, _, err := app.LoadConfigFile(filepath.Join(workspace, ".whale", app.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if loaded.Permissions.Default != "ask" {
		t.Fatalf("project config should remain ask, got %q", loaded.Permissions.Default)
	}
}

func TestPrepareCLIConfigDangerouslySkipPermissionsAppliesToSubcommands(t *testing.T) {
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("dangerously-skip-permissions", "true"); err != nil {
		t.Fatalf("set dangerously-skip-permissions: %v", err)
	}
	execCmd, _, err := root.Find([]string{"exec"})
	if err != nil {
		t.Fatalf("find exec: %v", err)
	}
	if err := prepareCLIConfig(execCmd, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.PermissionDefault != policy.PermissionAllow || !opts.cfg.AutoAcceptPermissions {
		t.Fatalf("dangerously skip permissions not applied: default=%s auto_accept=%v", opts.cfg.PermissionDefault, opts.cfg.AutoAcceptPermissions)
	}
}

func TestPrepareWorktreeRunsBeforeConfigLoad(t *testing.T) {
	repo := newCLIGitRepo(t)
	writeCLIFile(t, filepath.Join(repo, ".whale", app.LocalConfigFileName), []byte("model = \"deepseek-v4-pro\"\n"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = t.TempDir()
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("worktree", "feature/test"); err != nil {
		t.Fatalf("set worktree: %v", err)
	}
	if err := prepareWorktree(root, opts); err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	if got, want := opts.worktreeSession.Path, filepath.Join(repo, ".whale", "worktrees", "feature+test"); got != want {
		t.Fatalf("worktree path: got %s want %s", got, want)
	}
	if cwd, _ := os.Getwd(); cwd != opts.worktreeSession.Path {
		t.Fatalf("cwd: got %s want %s", cwd, opts.worktreeSession.Path)
	}
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.Model != "deepseek-v4-pro" {
		t.Fatalf("model should be loaded from copied worktree local config, got %s", opts.cfg.Model)
	}
}

func TestPrepareWorktreePreservesSubdirectoryWorkspace(t *testing.T) {
	repo := newCLIGitRepo(t)
	subdir := filepath.Join(repo, "packages", "api")
	writeCLIFile(t, filepath.Join(subdir, "api.txt"), []byte("api\n"))
	runCLI(t, repo, "git", "add", "packages/api/api.txt")
	runCLI(t, repo, "git", "commit", "-m", "add api package")
	writeCLIFile(t, filepath.Join(repo, ".whale", app.LocalConfigFileName), []byte("model = \"deepseek-v4-pro\"\n"))
	writeCLIFile(t, filepath.Join(subdir, ".whale", app.LocalConfigFileName), []byte("model = \"deepseek-v4-flash\"\n"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(subdir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = t.TempDir()
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("worktree", "subdir"); err != nil {
		t.Fatalf("set worktree: %v", err)
	}
	if err := prepareWorktree(root, opts); err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	wantCWD := filepath.Join(opts.worktreeSession.Path, "packages", "api")
	if cwd, _ := os.Getwd(); cwd != wantCWD {
		t.Fatalf("cwd: got %s want %s", cwd, wantCWD)
	}
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.Model != "deepseek-v4-flash" {
		t.Fatalf("model should be loaded from copied subdir local config, got %s", opts.cfg.Model)
	}
	if got := strings.TrimSpace(gitCLIOut(t, opts.worktreeSession.Path, "status", "--porcelain")); got != "" {
		t.Fatalf("worktree should stay clean after copying subdir local config, status:\n%s", got)
	}
}

func TestUnsupportedSubcommandsRejectWorktree(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "--worktree=x"},
		{"resume", "--worktree=x"},
		{"setup", "--worktree=x"},
	} {
		opts := &cliOptions{cfg: localExecConfig()}
		root := newRootCmd(opts)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "--worktree is only supported") {
			t.Fatalf("%v: expected unsupported worktree error, got %v", args, err)
		}
	}
}

func TestPrepareCLIConfigRejectsUnsupportedEffortAlias(t *testing.T) {
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("effort", "xhigh"); err != nil {
		t.Fatalf("set effort: %v", err)
	}
	err = prepareCLIConfig(root, opts)
	if err == nil {
		t.Fatal("expected unsupported effort error")
	}
	if !strings.Contains(err.Error(), "unsupported effort: xhigh") || !strings.Contains(err.Error(), "high, max") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareCLIConfigKeepsUnspecifiedThinkingAndEffortFromConfig(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	thinking := true
	if err := app.SaveConfigFile(app.GlobalConfigPath(dataDir), app.FileConfig{
		ReasoningEffort: "max",
		ThinkingEnabled: &thinking,
	}); err != nil {
		t.Fatalf("save global config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := prepareCLIConfig(root, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if !opts.cfg.ThinkingEnabled {
		t.Fatal("thinking_enabled should stay true from config")
	}
	if opts.cfg.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort: want max from config, got %s", opts.cfg.ReasoningEffort)
	}
}

func TestReadExecPromptPrefersArg(t *testing.T) {
	got, err := readExecPrompt(strings.NewReader("stdin prompt"), []string{"arg prompt"})
	if err != nil {
		t.Fatalf("readExecPrompt: %v", err)
	}
	if got != "arg prompt" {
		t.Fatalf("prompt = %q", got)
	}
}

func TestExecHelpShowsSessionAndMode(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"exec", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("exec --help: %v", err)
	}
	help := out.String()
	for _, want := range []string{"--session", "--mode", "agent", "ask", "plan", "overrides the session's saved mode"} {
		if !strings.Contains(help, want) {
			t.Fatalf("exec --help missing %q:\n%s", want, help)
		}
	}
}

func TestRootHelpHidesSessionAndMode(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("root --help: %v", err)
	}
	help := out.String()
	for _, flag := range []string{"--session", "--mode"} {
		if strings.Contains(help, flag+" ") || strings.Contains(help, flag+"=") {
			t.Fatalf("root help must not expose %s flag:\n%s", flag, help)
		}
	}
}

func TestExecCombinedArgsParse(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	execCmd, _, err := root.Find([]string{"exec"})
	if err != nil {
		t.Fatalf("find exec: %v", err)
	}
	args := []string{"--json", "--timeout-sec", "60", "--attach", "a.txt", "--session", "s1", "--mode", "agent", "--model", "deepseek-v4-flash", "--effort", "high", "prompt"}
	if err := execCmd.ParseFlags(args); err != nil {
		t.Fatalf("combined args must parse: %v", err)
	}
	if got, _ := execCmd.Flags().GetString("session"); got != "s1" {
		t.Fatalf("session = %q, want s1", got)
	}
	if got, _ := execCmd.Flags().GetString("mode"); got != "agent" {
		t.Fatalf("mode = %q, want agent", got)
	}
}

func TestExecRunERejectsInvalidModeBeforeProvider(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	for _, m := range []string{"wat", "   "} {
		dir := t.TempDir()
		workspace := t.TempDir()
		oldwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		if err := os.Chdir(workspace); err != nil {
			t.Fatalf("Chdir: %v", err)
		}

		opts := &cliOptions{cfg: localExecConfig()}
		opts.cfg.DataDir = dir
		root := newRootCmd(opts)
		root.SetArgs([]string{"exec", "--mode", m, "hi"})
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		err = root.Execute()
		_ = os.Chdir(oldwd)
		if err == nil {
			t.Fatalf("expected invalid mode %q to fail", m)
		}
		if !app.IsInvalidModeError(err) {
			t.Fatalf("expected InvalidModeError for %q, got %T: %v", m, err, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("expected zero provider requests for invalid mode, got %d", requests)
	}
}

func TestExecInvalidModeDoesNotCreateWorktree(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	repo := newCLIGitRepo(t)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = t.TempDir()
	root := newRootCmd(opts)
	root.SetArgs([]string{"exec", "--worktree=feature-x", "--mode", "bogus", "hi"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err == nil {
		t.Fatal("expected invalid mode to fail")
	} else if !app.IsInvalidModeError(err) {
		t.Fatalf("expected InvalidModeError, got %T: %v", err, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".whale", "worktrees", "feature-x")); !os.IsNotExist(err) {
		t.Fatalf("invalid mode must not create a worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".whale")); !os.IsNotExist(err) {
		t.Fatalf("invalid mode must not create .whale directories: %v", err)
	}
}

func TestExecJSONErrorOutputOnce(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "never reached")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	root := newRootCmd(opts)
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"exec", "--json", "--session", "missing", "hi"})
	err = root.Execute()
	if err == nil {
		t.Fatal("expected rejected resume to fail")
	}
	code, ok := ExitCode(err)
	if !ok || code != 1 {
		t.Fatalf("expected ExitError{Code:1}, got %v", err)
	}
	raw := out.String()
	if n := strings.Count(raw, "{"); n != 1 {
		t.Fatalf("expected exactly one JSON error result, got %d objects:\n%s", n, raw)
	}
	var res app.ExecResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json error: %v\n%s", err, raw)
	}
	if res.Status != "error" || res.Error == "" {
		t.Fatalf("expected status=error with message, got %+v", res)
	}
}

func TestExecJSONErrorNotDoubleEmittedOnRunFailure(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "ok")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	root := newRootCmd(opts)
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"exec", "--json", "hi"})
	if err := root.Execute(); err != nil {
		t.Fatalf("successful exec should not error: %v", err)
	}
	if n := strings.Count(out.String(), "{"); n != 1 {
		t.Fatalf("expected exactly one JSON result, got %d objects:\n%s", n, out.String())
	}
	var res app.ExecResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out.String())
	}
	if res.Status != "completed" || res.SessionID == "" {
		t.Fatalf("expected completed result with session id, got %+v", res)
	}
}

func TestExecJSONRuntimeFailureDoesNotPolluteStderr(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	root := newRootCmd(opts)
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"exec", "--json", "hi"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected runtime failure to fail exec")
	}
	if n := strings.Count(out.String(), "{"); n != 1 {
		t.Fatalf("expected exactly one JSON result, got %d objects:\n%s", n, out.String())
	}
	var res app.ExecResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out.String())
	}
	if res.Status != "error" || res.Error == "" {
		t.Fatalf("expected status=error with message, got %+v", res)
	}
	if got := errOut.String(); strings.Contains(got, "Error:") || strings.Contains(got, "Usage:") {
		t.Fatalf("cobra must not pollute stderr after a JSON error result:\n%s", got)
	}
}

func TestExecRunEAcceptsExplicitValidMode(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "ok")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	root := newRootCmd(opts)
	root.SetOut(&out)
	root.SetArgs([]string{"exec", "--mode", "ASK", "hi"})
	if err := root.Execute(); err != nil {
		t.Fatalf("exec with ASK should succeed: %v", err)
	}
}

func TestExecModeFlagValidation(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	execCmd, _, err := newRootCmd(opts).Find([]string{"exec"})
	if err != nil {
		t.Fatalf("find exec: %v", err)
	}

	t.Run("valid modes accepted case-insensitively", func(t *testing.T) {
		for _, m := range []string{"agent", "ASK", "Plan"} {
			execCmd.Flags().Set("mode", m)
			if !flagChanged(execCmd, "mode") {
				t.Fatalf("mode flag not marked changed for %q", m)
			}
		}
	})

	t.Run("invalid mode rejected", func(t *testing.T) {
		execCmd.Flags().Set("mode", "wat")
		if !flagChanged(execCmd, "mode") {
			t.Fatal("mode flag not marked changed")
		}
		if _, err := session.ParseMode("wat"); err == nil {
			t.Fatal("expected ParseMode to reject invalid mode")
		}
	})

	t.Run("blank explicit mode rejected", func(t *testing.T) {
		if _, err := session.ParseMode("   "); err != nil {
			t.Fatalf("ParseMode(blank) must keep Agent semantics: %v", err)
		}
		mode, err := session.ParseMode("")
		if err != nil || mode != session.ModeAgent {
			t.Fatalf("ParseMode(\"\") = %q, %v; want agent", mode, err)
		}
	})
}

func TestReadExecPromptFallsBackToStdin(t *testing.T) {
	got, err := readExecPrompt(strings.NewReader("stdin prompt\n"), nil)
	if err != nil {
		t.Fatalf("readExecPrompt: %v", err)
	}
	if got != "stdin prompt" {
		t.Fatalf("prompt = %q", got)
	}
}

func TestRunExecTextOutput(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "hello from exec")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader(""), opts, []string{"hi"}, false, 0, nil, "", ""); err != nil {
		t.Fatalf("runExec: %v", err)
	}
	if got := out.String(); got != "hello from exec\n" {
		t.Fatalf("stdout = %q", got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunExecJSONOutput(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "hello json")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("stdin prompt"), opts, nil, true, 0, nil, "", ""); err != nil {
		t.Fatalf("runExec: %v", err)
	}
	var res app.ExecResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out.String())
	}
	if res.Status != "completed" || res.Output != "hello json" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.SessionID == "" {
		t.Fatalf("expected session id: %+v", res)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunExecResumeSeededEmptySessionAppendsHistory(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "first round ok")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessionFile := filepath.Join(sessionsDir, "run-1.jsonl")
	if err := os.WriteFile(sessionFile, nil, 0o600); err != nil {
		t.Fatalf("seed empty session: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("first round"), opts, nil, true, 0, nil, "run-1", ""); err != nil {
		t.Fatalf("runExec on seeded empty session: %v", err)
	}
	var res app.ExecResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out.String())
	}
	if res.SessionID != "run-1" {
		t.Fatalf("session = %q, want run-1", res.SessionID)
	}
	raw, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !strings.Contains(string(raw), "first round") {
		t.Fatalf("expected round appended to seeded session, got:\n%s", raw)
	}
}

func TestRunExecResumeSessionAppendsHistory(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "round two")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	first := filepath.Join(sessionsDir, "s1.jsonl")
	if err := os.WriteFile(first, []byte(`{"SessionID":"s1","Role":"user","Text":"first round"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write first round: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("second round"), opts, nil, true, 0, nil, "s1", ""); err != nil {
		t.Fatalf("runExec resume: %v", err)
	}
	var res app.ExecResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out.String())
	}
	if res.SessionID != "s1" {
		t.Fatalf("session = %q, want s1", res.SessionID)
	}
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "first round") || !strings.Contains(content, "second round") {
		t.Fatalf("expected both rounds appended to same jsonl, got:\n%s", content)
	}
}

func TestRunExecResumeRestoresWorktreeContext(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	requests := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n", "resumed")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	worktreePath := t.TempDir()
	subdir := filepath.Join(worktreePath, "packages", "api")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace:          subdir,
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

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(subdir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	if err := runExec(&out, &errOut, strings.NewReader("resume round"), opts, nil, true, 0, nil, "s1", ""); err != nil {
		t.Fatalf("runExec resume: %v", err)
	}
	var payload map[string]any
	select {
	case payload = <-requests:
	default:
		t.Fatal("expected exec request payload")
	}
	messages, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %+v", payload["messages"])
	}
	for _, item := range messages {
		message, _ := item.(map[string]any)
		content, _ := message["content"].(string)
		if strings.Contains(content, "Current worktree root: "+worktreePath) {
			return
		}
	}
	t.Fatalf("resumed round lost the recorded worktree context: %+v", messages)
}

func TestRunExecResumeUnknownSessionFails(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("hi"), opts, nil, true, 0, nil, "missing", ""); err == nil {
		t.Fatal("expected resume of unknown session to fail")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("expected zero provider requests on rejected resume, got %d", requests)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "sessions")); err == nil && len(entries) > 0 {
		t.Fatalf("expected zero session state writes, found: %v", entries)
	}
}

func TestRunExecPlanModeSingleRoundExits(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "here is the plan")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("plan this"), opts, nil, false, 0, nil, "", "plan"); err != nil {
		t.Fatalf("runExec plan mode: %v", err)
	}
	if !strings.Contains(out.String(), "here is the plan") {
		t.Fatalf("expected plan output, got %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunExecResumeSavesExplicitModeForNextRound(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "ok")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"first"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("plan this"), opts, nil, true, 0, nil, "s1", "plan"); err != nil {
		t.Fatalf("runExec with explicit mode: %v", err)
	}
	st, err := session.LoadModeState(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load mode: %v", err)
	}
	if st.Mode != session.ModePlan {
		t.Fatalf("explicit mode not persisted for next round, got %q", st.Mode)
	}

	out.Reset()
	errOut.Reset()
	if err := runExec(&out, &errOut, strings.NewReader("again"), opts, nil, true, 0, nil, "s1", ""); err != nil {
		t.Fatalf("runExec without mode: %v", err)
	}
	st, err = session.LoadModeState(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load mode after next round: %v", err)
	}
	if st.Mode != session.ModePlan {
		t.Fatalf("saved mode not carried to next round, got %q", st.Mode)
	}
}

func TestRunExecRejectedResumeDoesNotSaveMode(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "never reached")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("hi"), opts, nil, true, 0, nil, "missing", "plan"); err == nil {
		t.Fatal("expected rejected resume to fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "missing.state.json")); err == nil {
		t.Fatal("explicit mode must not be saved when pre-checks fail")
	}
}

func TestValidateResumeTargetWorktreeSessionFromOriginalWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	worktreePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace:         worktreePath,
		WorktreeName:      "feature",
		WorktreePath:      worktreePath,
		WorktreeBranch:    "worktree-feature",
		OriginalWorkspace: "/tmp/original",
		OriginalBranch:    "main",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	originalWorkspace := t.TempDir()
	target, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, app.StartOptions{SessionID: "s1"}, originalWorkspace)
	if err != nil {
		t.Fatalf("worktree session resumed from original workspace must pass: %v", err)
	}
	if target.Session.Path != worktreePath {
		t.Fatalf("target path = %q, want %q", target.Session.Path, worktreePath)
	}
}

func TestValidateResumeTargetWorktreeSubdirWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	worktreePath := t.TempDir()
	subdir := filepath.Join(worktreePath, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		Workspace:         subdir,
		WorktreeName:      "feature",
		WorktreePath:      worktreePath,
		WorktreeBranch:    "worktree-feature",
		OriginalWorkspace: "/tmp/original",
		OriginalBranch:    "main",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	originalWorkspace := t.TempDir()
	decision, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, app.StartOptions{SessionID: "s1"}, originalWorkspace)
	if err != nil {
		t.Fatalf("worktree session with subdir workspace resumed from original workspace must pass: %v", err)
	}
	if decision.Session.Path != worktreePath {
		t.Fatalf("decision path = %q, want worktree root %q", decision.Session.Path, worktreePath)
	}
	if decision.Session.Workspace != subdir {
		t.Fatalf("decision workspace = %q, want subdir %q", decision.Session.Workspace, subdir)
	}
	if decision.TargetWorkspace != subdir {
		t.Fatalf("decision target workspace = %q, want subdir %q", decision.TargetWorkspace, subdir)
	}
}

func TestRunExecResumeKeepsSavedMode(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	srv := newExecTestServer(t, "ok")
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"first"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := session.SaveModeState(sessionsDir, "s1", session.ModePlan); err != nil {
		t.Fatalf("save mode: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	if err := runExec(&out, &errOut, strings.NewReader("again"), opts, nil, true, 0, nil, "s1", ""); err != nil {
		t.Fatalf("runExec resume: %v", err)
	}
	var res app.ExecResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out.String())
	}
	if res.SessionID != "s1" {
		t.Fatalf("session = %q, want s1", res.SessionID)
	}
	st, err := session.LoadModeState(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load mode: %v", err)
	}
	if st.Mode != session.ModePlan {
		t.Fatalf("saved mode = %q, want plan kept", st.Mode)
	}
}

func TestValidateResumeTargetWorktreeMismatch(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	recorded := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{WorktreePath: recorded}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	start := app.StartOptions{
		SessionID: "s1",
		Worktree:  app.WorktreeSession{Path: t.TempDir()},
	}
	_, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, start, t.TempDir())
	if err == nil {
		t.Fatal("expected explicit worktree mismatch to fail")
	}
	if !app.IsResumeRejectedError(err) {
		t.Fatalf("expected ResumeRejectedError, got %T: %v", err, err)
	}
}

func TestValidateResumeTargetRejectsExplicitWorktreeWithoutRecord(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	start := app.StartOptions{
		SessionID: "s1",
		Worktree:  app.WorktreeSession{Path: t.TempDir()},
	}
	if _, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, start, t.TempDir()); err == nil {
		t.Fatal("expected explicit worktree without session record to fail")
	}
}

func TestValidateResumeTargetMissingWorktreeAllowsFallback(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	originalWorkspace := t.TempDir()
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{
		WorktreePath:      missing,
		OriginalWorkspace: originalWorkspace,
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	// Resuming from the recorded original workspace is the fallback path:
	// the session rebinds to this directory after cleanup.
	target, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, app.StartOptions{SessionID: "s1"}, originalWorkspace)
	if err != nil {
		t.Fatalf("missing worktree should allow fallback from the original workspace: %v", err)
	}
	if target.Session.Path != "" || target.MissingWorktree == false {
		t.Fatalf("expected missing-worktree decision, got %+v", target)
	}

	withExplicit := app.StartOptions{
		SessionID: "s1",
		Worktree:  app.WorktreeSession{Path: t.TempDir()},
	}
	if _, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, withExplicit, originalWorkspace); err == nil {
		t.Fatal("expected explicit worktree takeover of missing worktree session to fail")
	}
}

func TestValidateResumeTargetMissingWorktreeCrossWorkspaceRejectedReadOnly(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	meta := session.SessionMeta{
		Workspace:          missing,
		Branch:             "worktree-feature",
		WorktreeName:       "feature",
		WorktreePath:       missing,
		WorktreeBranch:     "worktree-feature",
		OriginalWorkspace:  "/somewhere/else",
		OriginalBranch:     "main",
		OriginalHeadCommit: "abc123",
	}
	if err := session.SaveSessionMeta(sessionsDir, "s1", meta); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	before, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load before meta: %v", err)
	}

	_, err = app.ValidateResumeTarget(app.Config{DataDir: dataDir}, app.StartOptions{SessionID: "s1"}, t.TempDir())
	if err == nil {
		t.Fatal("expected missing-worktree cross-workspace resume to be rejected")
	}
	if !app.IsCrossWorkspaceResumeError(err) {
		t.Fatalf("expected CrossWorkspaceResumeError, got %T: %v", err, err)
	}
	after, err := session.LoadSessionMeta(sessionsDir, "s1")
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if after != before {
		t.Fatalf("rejected resume must not mutate session metadata:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestValidateResumeTargetRejectsCrossWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"SessionID":"s1","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := session.SaveSessionMeta(sessionsDir, "s1", session.SessionMeta{Workspace: "/somewhere/else"}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	_, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, app.StartOptions{SessionID: "s1"}, t.TempDir())
	if err == nil {
		t.Fatal("expected cross-workspace resume to be rejected")
	}
	if !app.IsCrossWorkspaceResumeError(err) {
		t.Fatalf("expected CrossWorkspaceResumeError, got %T: %v", err, err)
	}
}

func TestValidateResumeTargetRejectsSubagent(t *testing.T) {
	dataDir := t.TempDir()
	sessionsDir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "subagent-x.jsonl"), []byte(`{"SessionID":"subagent-x","Role":"user","Text":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	_, err := app.ValidateResumeTarget(app.Config{DataDir: dataDir}, app.StartOptions{SessionID: "subagent-x"}, t.TempDir())
	if err == nil {
		t.Fatal("expected subagent session to be rejected")
	}
	if !app.IsResumeRejectedError(err) {
		t.Fatalf("expected ResumeRejectedError, got %T: %v", err, err)
	}
}

func TestRunExecAttachSendsOpenAICompatibleFilePart(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	requests := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n", "attached")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	attachment := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(attachment, []byte("attachment body"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var out bytes.Buffer
	var errOut bytes.Buffer
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	opts.cfg.DeepSeekMultimodal = app.MultimodalProviderConfig{
		Enabled: true,
		Compat:  "openai",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	}
	if err := runExec(&out, &errOut, strings.NewReader(""), opts, []string{"inspect"}, false, 0, []string{attachment}, "", ""); err != nil {
		t.Fatalf("runExec: %v", err)
	}
	var payload map[string]any
	select {
	case payload = <-requests:
	default:
		t.Fatal("expected exec request payload")
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %+v", payload["messages"])
	}
	var msg map[string]any
	for _, item := range messages {
		candidate, _ := item.(map[string]any)
		if candidate["role"] != "user" {
			continue
		}
		if parts, ok := candidate["content"].([]any); ok && len(parts) == 2 {
			msg = candidate
			break
		}
	}
	if msg == nil {
		t.Fatalf("missing user message: %+v", messages)
	}
	parts, ok := msg["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %+v", msg["content"])
	}
	if parts[0].(map[string]any)["text"] != "inspect" {
		t.Fatalf("text part = %+v", parts[0])
	}
	filePart := parts[1].(map[string]any)
	fileInfo := filePart["file"].(map[string]any)
	if filePart["type"] != "file" || fileInfo["filename"] != "note.txt" || !strings.HasPrefix(fmt.Sprint(fileInfo["file_data"]), "data:text/plain") {
		t.Fatalf("file part = %+v", filePart)
	}
}

func TestRootExecAppliesThinkingAndEffortOverridesWithoutChangingTextOutput(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1234567890abcdef1234")
	requests := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n", "hello from exec")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	dir := t.TempDir()
	workspace := t.TempDir()
	thinking := false
	if err := app.SaveConfigFile(app.GlobalConfigPath(dir), app.FileConfig{
		ReasoningEffort: "high",
		ThinkingEnabled: &thinking,
	}); err != nil {
		t.Fatalf("save global config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dir
	root := newRootCmd(opts)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"exec", "--thinking=true", "--effort=max", "hi"})
	if err := root.Execute(); err != nil {
		t.Fatalf("root exec: %v", err)
	}
	if got := out.String(); got != "hello from exec\n" {
		t.Fatalf("stdout = %q", got)
	}

	var payload map[string]any
	select {
	case payload = <-requests:
	default:
		t.Fatal("expected exec request payload")
	}
	thinkingPayload, ok := payload["thinking"].(map[string]any)
	if !ok || thinkingPayload["type"] != "enabled" {
		t.Fatalf("thinking payload = %#v, want enabled", payload["thinking"])
	}
	if got := payload["reasoning_effort"]; got != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", got)
	}
}

func TestPrepareCLIConfigMarksExplicitDefaultModel(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("model", opts.cfg.Model, "")
	if err := cmd.Flags().Set("model", "deepseek-v4-flash"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if err := prepareCLIConfig(cmd, opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if !opts.cfg.ModelExplicit {
		t.Fatal("expected ModelExplicit=true")
	}
}

func TestRootHelpOnlyShowsPublicRootFlags(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	help := out.String()
	for _, removed := range []string{
		"--data-dir",
		"--approval-mode",
		"--allow-prefixes",
		"--deny-prefixes",
		"--auto-compact",
		"--auto-compact-threshold",
		"--model-context-window",
		"--memory-enabled",
		"--memory-max-chars",
		"--memory-file-order",
		"--mcp-config",
		"--budget-warning-usd",
		"--config",
		"--session",
		"--mode",
	} {
		if helpHasFlag(help, removed) {
			t.Fatalf("removed flag should not appear in help: %s\n%s", removed, help)
		}
	}
	for _, expected := range []string{"--model", "--thinking", "--effort", "--dangerously-skip-permissions", "--worktree", "--version"} {
		if !helpHasFlag(help, expected) {
			t.Fatalf("expected public flag %s in help, got:\n%s", expected, help)
		}
	}
	if !strings.Contains(help, "Override thinking for this run only") || !strings.Contains(help, "Override reasoning effort for this run only") {
		t.Fatalf("expected public flags in help, got:\n%s", help)
	}
}

func TestExecRejectsUnsupportedEffortBeforeRun(t *testing.T) {
	workspace := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"exec", "--effort=low", "hi"})
	err = root.Execute()
	if err == nil {
		t.Fatal("expected unsupported effort error")
	}
	if !strings.Contains(err.Error(), "unsupported effort: low") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "unsupported effort: low") {
		t.Fatalf("expected CLI error output, got %q", out.String())
	}
	if strings.Contains(out.String(), "hello from exec") {
		t.Fatalf("expected exec to stop before normal output, got %q", out.String())
	}
}

func TestThinkingAndEffortFlagsArePersistent(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)

	if root.PersistentFlags().Lookup("thinking") == nil {
		t.Fatal("expected thinking to be a root persistent flag")
	}
	if root.PersistentFlags().Lookup("effort") == nil {
		t.Fatal("expected effort to be a root persistent flag")
	}

	for _, args := range [][]string{
		{"exec", "--help"},
		{"resume", "--help"},
	} {
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%s help: %v", strings.Join(args[:1], " "), err)
		}
		help := out.String()
		if !helpHasFlag(help, "--thinking") || !helpHasFlag(help, "--effort") {
			t.Fatalf("expected inherited flags in %v help, got:\n%s", args, help)
		}
	}
}

func TestWorktreeFlagIsOptionalValuePersistentFlag(t *testing.T) {
	opts := &cliOptions{cfg: localExecConfig()}
	root := newRootCmd(opts)
	flag := root.PersistentFlags().Lookup("worktree")
	if flag == nil {
		t.Fatal("expected worktree flag")
	}
	if flag.NoOptDefVal == "" || !strings.HasPrefix(flag.NoOptDefVal, "session-") {
		t.Fatalf("unexpected worktree NoOptDefVal: %q", flag.NoOptDefVal)
	}
}

func TestAutoWorktreeNameHasCollisionResistantSuffix(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name := autoWorktreeName()
		if err := whaleworktree.ValidateName(name); err != nil {
			t.Fatalf("auto worktree name should be valid, got %q: %v", name, err)
		}
		if seen[name] {
			t.Fatalf("auto worktree name collided: %s", name)
		}
		seen[name] = true
	}
}

func TestNormalizeWorktreeArgsConsumesSeparatedNameButNotCommand(t *testing.T) {
	tests := []struct {
		name          string
		in            []string
		stdinTerminal bool
		want          []string
	}{
		{
			name:          "root named worktree",
			in:            []string{"--worktree", "feature-x"},
			stdinTerminal: true,
			want:          []string{"--worktree=feature-x"},
		},
		{
			name:          "root auto before exec",
			in:            []string{"--worktree", "exec", "hi"},
			stdinTerminal: true,
			want:          []string{"--worktree", "exec", "hi"},
		},
		{
			name:          "exec named worktree",
			in:            []string{"exec", "--worktree", "feature-x", "hi"},
			stdinTerminal: true,
			want:          []string{"exec", "--worktree=feature-x", "hi"},
		},
		{
			name:          "short named worktree",
			in:            []string{"exec", "-w", "feature-x", "hi"},
			stdinTerminal: true,
			want:          []string{"exec", "-w=feature-x", "hi"},
		},
		{
			name:          "exec auto worktree preserves only prompt on terminal stdin",
			in:            []string{"exec", "--worktree", "fix bug"},
			stdinTerminal: true,
			want:          []string{"exec", "--worktree", "fix bug"},
		},
		{
			name:          "exec bare worktree preserves valid-name single arg as prompt on tty",
			in:            []string{"exec", "--worktree", "test"},
			stdinTerminal: true,
			want:          []string{"exec", "--worktree", "test"},
		},
		{
			name:          "exec bare worktree with invalid-name arg treats arg as prompt on non-tty stdin",
			in:            []string{"exec", "--worktree", "fix bug"},
			stdinTerminal: false,
			want:          []string{"exec", "--worktree", "fix bug"},
		},
		{
			name:          "exec named worktree consumes name on non-tty stdin with stdin prompt",
			in:            []string{"exec", "--worktree", "feature-x"},
			stdinTerminal: false,
			want:          []string{"exec", "--worktree=feature-x"},
		},
		{
			name:          "exec named worktree consumes name with trailing flag on non-tty stdin",
			in:            []string{"exec", "--worktree", "feature-x", "--json"},
			stdinTerminal: false,
			want:          []string{"exec", "--worktree=feature-x", "--json"},
		},
		{
			name:          "exec named worktree consumes name with separate prompt on non-tty stdin",
			in:            []string{"exec", "--worktree", "feature-x", "hi"},
			stdinTerminal: false,
			want:          []string{"exec", "--worktree=feature-x", "hi"},
		},
		{
			name:          "exec named worktree with equals",
			in:            []string{"exec", "--worktree=feature-x", "hi"},
			stdinTerminal: true,
			want:          []string{"exec", "--worktree=feature-x", "hi"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeWorktreeArgs(tc.in, tc.stdinTerminal)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("normalizeWorktreeArgs = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRootExecuteSupportsSeparatedWorktreeName(t *testing.T) {
	repo := newCLIGitRepo(t)
	writeCLIFile(t, filepath.Join(repo, ".whale", app.LocalConfigFileName), []byte("model = \"deepseek-v4-pro\"\n"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = t.TempDir()
	root := newRootCmd(opts)
	root.SetArgs(normalizeWorktreeArgs([]string{"exec", "--worktree", "feature-x", "--effort=low", "hi"}, true))
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "unsupported effort: low") {
		t.Fatalf("expected effort validation after worktree preparation, got %v", err)
	}
	if opts.worktreeSession.Name != "feature-x" {
		t.Fatalf("worktree session = %+v", opts.worktreeSession)
	}
}

func TestRootExecBareWorktreePreservesPrompt(t *testing.T) {
	repo := newCLIGitRepo(t)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = t.TempDir()
	root := newRootCmd(opts)
	root.SetArgs(normalizeWorktreeArgs([]string{"exec", "--worktree", "--effort=low", "fix bug"}, true))
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "unsupported effort: low") {
		t.Fatalf("expected effort validation after preserving prompt, got %v", err)
	}
	if !strings.HasPrefix(opts.worktreeSession.Name, "session-") {
		t.Fatalf("expected auto-named worktree, got %+v", opts.worktreeSession)
	}
}

func TestPrepareResumeWorktreeChdirsBeforeConfigLoad(t *testing.T) {
	repo := newCLIGitRepo(t)
	dataDir := t.TempDir()

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("worktree", "resume-test"); err != nil {
		t.Fatalf("set worktree: %v", err)
	}
	if err := prepareWorktree(root, opts); err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	worktreePath := opts.worktreeSession.Path
	if err := app.SaveConfigFile(filepath.Join(worktreePath, ".whale", app.LocalConfigFileName), app.FileConfig{Model: "deepseek-v4-pro"}); err != nil {
		t.Fatalf("save worktree config: %v", err)
	}
	if err := session.SaveSessionMeta(store.DefaultSessionsDir(dataDir), "resume-session", session.SessionMeta{
		Workspace:      worktreePath,
		Branch:         "worktree-resume-test",
		WorktreeName:   "resume-test",
		WorktreePath:   worktreePath,
		WorktreeBranch: "worktree-resume-test",
	}); err != nil {
		t.Fatalf("save session meta: %v", err)
	}

	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir repo: %v", err)
	}
	opts = &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	if err := prepareResumeWorktree([]string{"resume-session"}, false, opts); err != nil {
		t.Fatalf("prepareResumeWorktree: %v", err)
	}
	if cwd, _ := os.Getwd(); cwd != worktreePath {
		t.Fatalf("cwd: got %s want %s", cwd, worktreePath)
	}
	if err := prepareCLIConfig(newRootCmd(opts), opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.Model != "deepseek-v4-pro" {
		t.Fatalf("model should come from resume worktree config, got %s", opts.cfg.Model)
	}
}

func TestPrepareResumeWorktreeUsesRecordedSubdirectoryWorkspace(t *testing.T) {
	repo := newCLIGitRepo(t)
	dataDir := t.TempDir()
	subdir := filepath.Join(repo, "packages", "api")
	writeCLIFile(t, filepath.Join(subdir, "api.txt"), []byte("api\n"))
	runCLI(t, repo, "git", "add", "packages/api/api.txt")
	runCLI(t, repo, "git", "commit", "-m", "add api package")
	writeCLIFile(t, filepath.Join(subdir, ".whale", app.LocalConfigFileName), []byte("model = \"deepseek-v4-flash\"\n"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(subdir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	root := newRootCmd(opts)
	if err := root.PersistentFlags().Set("worktree", "resume-subdir"); err != nil {
		t.Fatalf("set worktree: %v", err)
	}
	if err := prepareWorktree(root, opts); err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	worktreePath := opts.worktreeSession.Path
	worktreeWorkspace := filepath.Join(worktreePath, "packages", "api")
	if err := session.SaveSessionMeta(store.DefaultSessionsDir(dataDir), "resume-subdir-session", session.SessionMeta{
		Workspace:      worktreeWorkspace,
		Branch:         "worktree-resume-subdir",
		WorktreeName:   "resume-subdir",
		WorktreePath:   worktreePath,
		WorktreeBranch: "worktree-resume-subdir",
	}); err != nil {
		t.Fatalf("save session meta: %v", err)
	}

	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir repo: %v", err)
	}
	opts = &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	if err := prepareResumeWorktree([]string{"resume-subdir-session"}, false, opts); err != nil {
		t.Fatalf("prepareResumeWorktree: %v", err)
	}
	if cwd, _ := os.Getwd(); cwd != worktreeWorkspace {
		t.Fatalf("cwd: got %s want %s", cwd, worktreeWorkspace)
	}
	if err := prepareCLIConfig(newRootCmd(opts), opts); err != nil {
		t.Fatalf("prepareCLIConfig: %v", err)
	}
	if opts.cfg.Model != "deepseek-v4-flash" {
		t.Fatalf("model should come from resume subdir worktree config, got %s", opts.cfg.Model)
	}
}

func TestPrepareResumeWorktreeMissingPathFallsBack(t *testing.T) {
	dataDir := t.TempDir()
	original := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	if err := session.SaveSessionMeta(store.DefaultSessionsDir(dataDir), "resume-session", session.SessionMeta{
		Workspace:          missing,
		Branch:             "worktree-missing",
		WorktreeName:       "missing",
		WorktreePath:       missing,
		WorktreeBranch:     "worktree-missing",
		OriginalWorkspace:  original,
		OriginalBranch:     "main",
		OriginalHeadCommit: "abc123",
	}); err != nil {
		t.Fatalf("save session meta: %v", err)
	}
	opts := &cliOptions{cfg: localExecConfig()}
	opts.cfg.DataDir = dataDir
	if err := prepareResumeWorktree([]string{"resume-session"}, false, opts); err != nil {
		t.Fatalf("prepareResumeWorktree: %v", err)
	}
	if opts.worktreeSession.Path != "" {
		t.Fatalf("expected no resume worktree, got %+v", opts.worktreeSession)
	}
	meta, err := session.LoadSessionMeta(store.DefaultSessionsDir(dataDir), "resume-session")
	if err != nil {
		t.Fatalf("load session meta: %v", err)
	}
	if meta.Workspace != original || meta.Branch != "main" {
		t.Fatalf("unexpected fallback meta: %+v", meta)
	}
	if meta.WorktreeName != "" || meta.WorktreePath != "" || meta.WorktreeBranch != "" || meta.OriginalWorkspace != "" || meta.OriginalBranch != "" || meta.OriginalHeadCommit != "" {
		t.Fatalf("expected worktree metadata to be cleared: %+v", meta)
	}
}

func helpHasFlag(help, flag string) bool {
	for _, field := range strings.Fields(help) {
		field = strings.TrimRight(field, ",")
		if field == flag || strings.HasPrefix(field, flag+"=") {
			return true
		}
	}
	return false
}

func TestResumeStartOptions(t *testing.T) {
	got, err := resumeStartOptions(nil, false)
	if err != nil {
		t.Fatalf("resumeStartOptions picker: %v", err)
	}
	if !got.ResumeMenu || got.SessionID != "" || got.NewSession {
		t.Fatalf("picker start options = %+v", got)
	}

	got, err = resumeStartOptions(nil, true)
	if err != nil {
		t.Fatalf("resumeStartOptions last: %v", err)
	}
	if got.ResumeMenu || got.SessionID != "" || got.NewSession {
		t.Fatalf("last start options = %+v", got)
	}

	got, err = resumeStartOptions([]string{"sess-1"}, false)
	if err != nil {
		t.Fatalf("resumeStartOptions id: %v", err)
	}
	if got.ResumeMenu || got.SessionID != "sess-1" || got.NewSession {
		t.Fatalf("id start options = %+v", got)
	}

	if _, err = resumeStartOptions([]string{"sess-1"}, true); err == nil {
		t.Fatal("expected --last with id to fail")
	}
}

func newExecTestServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n", content)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func newCLIGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runCLI(t, dir, "git", "init", "-b", "main")
	runCLI(t, dir, "git", "config", "user.email", "test@example.com")
	runCLI(t, dir, "git", "config", "user.name", "Test User")
	writeCLIFile(t, filepath.Join(dir, ".gitignore"), []byte(".whale/worktrees/\n"))
	writeCLIFile(t, filepath.Join(dir, "README.md"), []byte("test\n"))
	runCLI(t, dir, "git", "add", ".gitignore", "README.md")
	runCLI(t, dir, "git", "commit", "-m", "initial")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve temp repo: %v", err)
	}
	return resolved
}

func runCLI(t *testing.T, cwd, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, string(out))
	}
}

func gitCLIOut(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

func writeCLIFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
