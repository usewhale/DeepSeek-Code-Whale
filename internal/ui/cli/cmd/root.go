package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/usewhale/whale/internal/app"
	"github.com/usewhale/whale/internal/app/appserver"
	"github.com/usewhale/whale/internal/build"
	"github.com/usewhale/whale/internal/defaults"
	"github.com/usewhale/whale/internal/tui"
	"github.com/usewhale/whale/internal/ui/tuiadapter"
	whaleworktree "github.com/usewhale/whale/internal/worktree"
)

type cliOptions struct {
	cfg                        app.Config
	dangerouslySkipPermissions bool
	worktreeName               string
	worktreeSession            app.WorktreeSession
}

func Execute() error {
	opts := &cliOptions{cfg: app.DefaultConfig()}
	root := newRootCmd(opts)
	root.SetArgs(normalizeWorktreeArgs(os.Args[1:], stdinIsTerminal()))
	return root.Execute()
}

func bindPersistentFlags(c *cobra.Command, opts *cliOptions) {
	c.PersistentFlags().StringVarP(&opts.cfg.Model, "model", "m", opts.cfg.Model, "Model to use ("+strings.Join(defaults.SupportedModels(), "|")+")")
	c.PersistentFlags().BoolVar(&opts.cfg.ThinkingEnabled, "thinking", opts.cfg.ThinkingEnabled, "Override thinking for this run only")
	c.PersistentFlags().StringVar(&opts.cfg.ReasoningEffort, "effort", opts.cfg.ReasoningEffort, "Override reasoning effort for this run only (high|max)")
	c.PersistentFlags().BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "Auto-accept permission prompts for this run; extremely dangerous")
	c.PersistentFlags().StringVarP(&opts.worktreeName, "worktree", "w", "", "Create or reuse an isolated git worktree for this run")
	if f := c.PersistentFlags().Lookup("worktree"); f != nil {
		f.NoOptDefVal = autoWorktreeName()
	}
	c.Flags().BoolP("version", "V", false, "Print version")
}

func runLoop(opts *cliOptions, start app.StartOptions) error {
	start.Worktree = opts.worktreeSession
	ctx := context.Background()
	cfg, err := loadCLIConfigIfNeeded(opts.cfg)
	if err != nil {
		return err
	}
	outcome, action, err := runUpdatePromptIfNeeded(ctx, cfg)
	if err != nil {
		return err
	}
	if outcome == updatePromptRun {
		return runUpdateAction(action)
	}
	if outcome == updatePromptInterrupt {
		return nil
	}
	rt, err := tuiadapter.NewRuntime(ctx, cfg, start)
	if err != nil {
		if app.IsCrossWorkspaceResumeError(err) {
			fmt.Println(err.Error())
			return nil
		}
		return err
	}
	return tui.RunTUI(rt, tui.RunOptions{ResumeMenu: start.ResumeMenu})
}

func loadCLIConfigIfNeeded(cfg app.Config) (app.Config, error) {
	if cfg.ConfigLoaded {
		return cfg, nil
	}
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return cfg, fmt.Errorf("get workspace: %w", err)
	}
	return app.LoadAndApplyConfig(cfg, workspaceRoot)
}

func prepareWorktree(cmd *cobra.Command, opts *cliOptions) error {
	if !worktreeFlagChanged(cmd) {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}
	checkoutRoot, err := whaleworktree.CheckoutRoot(cwd)
	if err != nil {
		return err
	}
	workspaceRel, err := filepath.Rel(checkoutRoot, cwd)
	if err != nil {
		return fmt.Errorf("resolve workspace relative path: %w", err)
	}
	sess, err := whaleworktree.Start(cwd, opts.worktreeName)
	if err != nil {
		return err
	}
	targetWorkspace := filepath.Join(sess.Path, workspaceRel)
	if _, err := os.Stat(targetWorkspace); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat worktree workspace: %w", err)
		}
		targetWorkspace = sess.Path
	}
	if err := os.Chdir(targetWorkspace); err != nil {
		return fmt.Errorf("enter worktree: %w", err)
	}
	opts.worktreeSession = worktreeSessionFrom(sess)
	opts.worktreeSession.Workspace = targetWorkspace
	return nil
}

func worktreeSessionFrom(s whaleworktree.Session) app.WorktreeSession {
	return app.WorktreeSession{
		Name:               s.Name,
		Workspace:          s.Path,
		Path:               s.Path,
		Branch:             s.Branch,
		OriginalWorkspace:  s.OriginalWorkspace,
		OriginalBranch:     s.OriginalBranch,
		OriginalHeadCommit: s.OriginalHeadCommit,
	}
}

func rejectWorktreeFlag(cmd *cobra.Command) error {
	if worktreeFlagChanged(cmd) {
		return fmt.Errorf("--worktree is only supported for whale and whale exec")
	}
	return nil
}

func worktreeFlagChanged(cmd *cobra.Command) bool {
	return flagChanged(cmd, "worktree")
}

func autoWorktreeName() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "session-" + time.Now().Format("20060102-150405.000000000") + "-" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("session-%s-%d", time.Now().Format("20060102-150405.000000000"), os.Getpid())
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

func normalizeWorktreeArgs(args []string, stdinTerminal bool) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--worktree" || arg == "-w") && i+1 < len(args) && shouldConsumeWorktreeValue(args, i, stdinTerminal) {
			if arg == "-w" {
				out = append(out, "-w="+args[i+1])
			} else {
				out = append(out, "--worktree="+args[i+1])
			}
			i++
			continue
		}
		out = append(out, arg)
	}
	return out
}

func shouldConsumeWorktreeValue(args []string, index int, stdinTerminal bool) bool {
	next := args[index+1]
	if next == "" || strings.HasPrefix(next, "-") {
		return false
	}
	if worktreeFlagIsAfterExec(args, index) {
		// `exec --worktree NAME PROMPT` always consumes NAME.
		if hasExecPromptAfterWorktreeName(args, index+2) {
			return true
		}
		// No separate prompt follows. On a TTY the single arg is the
		// positional prompt (worktree gets an auto name). With non-TTY
		// stdin (pipe / CI / </dev/null) stdin supplies the prompt, so
		// the arg is the worktree name — but only when it actually
		// parses as one; otherwise it is a quoted prompt like "fix bug".
		return !stdinTerminal && whaleworktree.ValidateName(next) == nil
	}
	switch next {
	case "exec", "resume", "doctor", "setup", "help", "completion":
		return false
	default:
		return true
	}
}

func hasExecPromptAfterWorktreeName(args []string, start int) bool {
	for i := start; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return i+1 < len(args)
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return true
	}
	return false
}

func worktreeFlagIsAfterExec(args []string, index int) bool {
	for i := 0; i < index; i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if arg == "exec" {
			return true
		}
	}
	return false
}

func prepareCLIConfig(cmd *cobra.Command, opts *cliOptions) error {
	flagCfg := opts.cfg
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}
	cfg, err := app.LoadAndApplyConfig(flagCfg, workspaceRoot)
	if err != nil {
		return err
	}
	if flagChanged(cmd, "model") {
		cfg.Model = flagCfg.Model
		cfg.ModelExplicit = true
	}
	if flagChanged(cmd, "thinking") {
		cfg.ThinkingEnabled = flagCfg.ThinkingEnabled
	}
	if flagChanged(cmd, "effort") {
		effort, err := validateEffort(flagCfg.ReasoningEffort)
		if err != nil {
			return err
		}
		cfg.ReasoningEffort = effort
	}
	if flagChanged(cmd, "dangerously-skip-permissions") && opts.dangerouslySkipPermissions {
		cfg.PermissionDefault = "allow"
		cfg.AutoAcceptPermissions = true
	}
	opts.cfg = cfg
	return validateModel(opts.cfg.Model)
}

func flagChanged(cmd *cobra.Command, name string) bool {
	for _, flags := range []*pflag.FlagSet{
		cmd.Flags(),
		cmd.PersistentFlags(),
		cmd.InheritedFlags(),
		cmd.Root().PersistentFlags(),
	} {
		if f := flags.Lookup(name); f != nil && f.Changed {
			return true
		}
	}
	return false
}

func validateModel(v string) error {
	if !defaults.IsSupportedModel(v) {
		return fmt.Errorf("unsupported model: %s", v)
	}
	return nil
}

func validateEffort(v string) (string, error) {
	normalized := app.NormalizeEffort(v)
	for _, supported := range app.SupportedReasoningEfforts() {
		if strings.EqualFold(normalized, supported) && strings.EqualFold(strings.TrimSpace(v), normalized) {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("unsupported effort: %s (supported: %s)", v, strings.Join(app.SupportedReasoningEfforts(), ", "))
}

func newRootCmd(opts *cliOptions) *cobra.Command {
	root := &cobra.Command{
		Use:     "whale",
		Short:   "Whale: DeepSeek-native coding agent for the terminal.",
		Version: build.CurrentVersion(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command: %s", args[0])
			}
			if err := prepareWorktree(cmd, opts); err != nil {
				return err
			}
			if err := prepareCLIConfig(cmd, opts); err != nil {
				return err
			}
			return runLoop(opts, app.StartOptions{NewSession: true})
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	bindPersistentFlags(root, opts)
	root.AddCommand(newExecCmd(opts))
	root.AddCommand(newDoctorCmd(opts))
	root.AddCommand(newSetupCmd(opts))
	root.AddCommand(newResumeCmd(opts))
	root.AddCommand(newPluginCmd(opts))
	root.AddCommand(newAppServerCmd(opts))
	return root
}

func newAppServerCmd(opts *cliOptions) *cobra.Command {
	return &cobra.Command{
		Use:    "app-server",
		Short:  "Run the local Whale app-server protocol over stdio.",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectWorktreeFlag(cmd); err != nil {
				return err
			}
			if err := prepareCLIConfig(cmd, opts); err != nil {
				return err
			}
			return appserver.Run(cmd.Context(), opts.cfg, app.StartOptions{NewSession: true}, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
