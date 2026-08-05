//go:build unix

package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/usewhale/whale/internal/execboundary"
	"github.com/usewhale/whale/internal/execenv"
)

var execBoundaryProbeCache struct {
	sync.Mutex
	key string
	err error
}

func runShellPTY(ctx context.Context, cmd *exec.Cmd, task *shellTask) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	defer func() {
		_ = ptmx.Close()
	}()
	task.setStdin(ptmx)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(task.outputWriter(false), ptmx)
	}()

	err = waitCommandContext(ctx, cmd, func() error {
		_ = ptmx.Close()
		return killPTYCommandGroup(cmd)
	})
	// On normal exit, let the reader observe the slave-side EOF and drain any
	// output queued just before the process exited. Closing the master first can
	// discard that tail (for example, the final line printed after read returns).
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		// A misbehaving PTY must not keep the task alive forever. Closing the
		// master unblocks io.Copy; wait once more so it cannot write into the
		// task buffer after the task has been marked complete.
		_ = ptmx.Close()
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
		}
	}
	return err
}

func runShellExecBoundaryPTY(ctx context.Context, dir, command string, task *shellTask) error {
	shellPath, wrapperPath, err := shellExecBoundaryConfig()
	if err != nil {
		return err
	}
	wrapperShim, cleanup, err := createExecBoundaryWrapperShim(wrapperPath)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.Command(shellPath, "-lc", command)
	cmd.Dir = dir
	sessionID, approval := task.execBoundaryApproval()
	server, err := execboundary.StartServer(ctx, task.execBoundaryPolicy(), approval, sessionID)
	if err != nil {
		return err
	}
	defer server.Close()
	cmd.Env = append(os.Environ(),
		execenv.ExecWrapperEnv+"="+wrapperShim,
		execenv.SocketEnv+"="+server.SocketPath(),
	)
	return runShellPTY(ctx, cmd, task)
}

func createExecBoundaryWrapperShim(wrapperPath string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "whale-exec-boundary-wrapper-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	shim := filepath.Join(dir, "exec-wrapper")
	body := "#!/bin/sh\n" +
		"export " + execenv.WrapperModeEnv + "=1\n" +
		"exec " + shellSingleQuote(wrapperPath) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return shim, cleanup, nil
}

func shellExecBoundaryEnabled() bool {
	_, _, err := shellExecBoundaryConfig()
	return err == nil
}

func shellExecBoundaryConfig() (string, string, error) {
	shellPath, shellSource := shellExecBoundaryShellPath()
	if shellPath == "" {
		return "", "", errors.New("exec-boundary shell is not configured")
	}
	if err := requireExecutableFile(shellPath, "exec-boundary shell"); err != nil {
		return "", "", err
	}
	wrapperPath := strings.TrimSpace(os.Getenv(execenv.WrapperPathEnv))
	if wrapperPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		wrapperPath = exe
	}
	if err := requireExecutableFile(wrapperPath, "exec-boundary wrapper"); err != nil {
		return "", "", err
	}
	if err := cachedProbeExecBoundaryShell(shellPath, wrapperPath); err != nil {
		if shellSource == "" {
			return "", "", err
		}
		return "", "", fmt.Errorf("%s is not a usable exec-boundary shell: %w", shellSource, err)
	}
	return shellPath, wrapperPath, nil
}

func shellExecBoundaryShellPath() (string, string) {
	if shellPath := strings.TrimSpace(os.Getenv(execenv.ShellEnv)); shellPath != "" {
		return shellPath, execenv.ShellEnv
	}
	for _, candidate := range shellExecBoundaryShellCandidates() {
		if candidate == "" {
			continue
		}
		if err := requireExecutableFile(candidate, "exec-boundary shell"); err != nil {
			continue
		}
		return candidate, candidate
	}
	return "", ""
}

func shellExecBoundaryShellCandidates() []string {
	exePath := ""
	if exe, err := os.Executable(); err == nil {
		exePath = exe
	}
	homeDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		homeDir = home
	}
	return shellExecBoundaryShellCandidatesFor(exePath, homeDir)
}

func shellExecBoundaryShellCandidatesFor(exe, home string) []string {
	var candidates []string
	if exe != "" {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "runtime", "zsh"),
			filepath.Join(exeDir, "zsh"),
			filepath.Clean(filepath.Join(exeDir, "..", "libexec", "runtime", "zsh")),
			filepath.Clean(filepath.Join(exeDir, "..", "libexec", "whale", "zsh")),
			filepath.Clean(filepath.Join(exeDir, "..", "libexec", "whale", "runtime", "zsh")),
			filepath.Clean(filepath.Join(exeDir, "..", "lib", "whale", "zsh")),
			filepath.Clean(filepath.Join(exeDir, "..", "share", "whale", "runtime", "zsh")),
		)
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".whale", "runtime", "zsh"),
			filepath.Join(home, ".local", "share", "whale", "runtime", "zsh"),
		)
	}
	return candidates
}

func cachedProbeExecBoundaryShell(shellPath, wrapperPath string) error {
	key := shellPath + "\x00" + wrapperPath
	execBoundaryProbeCache.Lock()
	if execBoundaryProbeCache.key == key {
		err := execBoundaryProbeCache.err
		execBoundaryProbeCache.Unlock()
		return err
	}
	execBoundaryProbeCache.Unlock()

	err := probeExecBoundaryShell(shellPath)
	execBoundaryProbeCache.Lock()
	execBoundaryProbeCache.key = key
	execBoundaryProbeCache.err = err
	execBoundaryProbeCache.Unlock()
	return err
}

func requireExecutableFile(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s is not available: %w", label, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory: %s", label, path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable: %s", label, path)
	}
	return nil
}

func probeExecBoundaryShell(shellPath string) error {
	dir, err := os.MkdirTemp("", "whale-exec-boundary-probe-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	marker := filepath.Join(dir, "called")
	probeWrapper := filepath.Join(dir, "wrapper")
	body := "#!/bin/sh\n" +
		"printf called > " + shellSingleQuote(marker) + "\n" +
		"exit 0\n"
	if err := os.WriteFile(probeWrapper, []byte(body), 0o755); err != nil {
		return err
	}
	cmd := exec.Command(shellPath, "-lc", "command /bin/echo whale-exec-boundary-probe")
	cmd.Env = append(os.Environ(), execenv.ExecWrapperEnv+"="+probeWrapper)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec-boundary shell probe failed: %w", err)
	}
	if _, err := os.Stat(marker); err != nil {
		return fmt.Errorf("exec-boundary shell does not honor %s", execenv.ExecWrapperEnv)
	}
	return nil
}

func shellPTYSupported() bool {
	return true
}

func waitCommandContext(ctx context.Context, cmd *exec.Cmd, cancel func() error) error {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if cancel != nil {
			_ = cancel()
		} else if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			return ctx.Err()
		}
	}
}

func killPTYCommandGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
