package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usewhale/whale/internal/shell"
)

const (
	stdioCloseGrace = 500 * time.Millisecond
	stdioCloseLimit = 5 * time.Second
)

type stdioProcessTransport struct {
	base   *sdk.CommandTransport
	cmd    *exec.Cmd
	stderr *boundedStderr // captures the server's stderr so failures can be
	// diagnosed without re-spawning the command
}

// stdioStderrCap bounds how much of a server's stderr is retained for
// diagnostics. A stdio server may live for the whole process lifetime and emit
// arbitrary output, so the capture must be bounded; keeping the tail preserves
// the last lines, which are the ones that explain a failure.
const stdioStderrCap = 64 << 10

// boundedStderr is a concurrency-safe, bounded stderr sink. os/exec's internal
// copy goroutine writes to it while the parent may read it concurrently from
// another goroutine (e.g. maybeStdioErr after a failed Connect) — bytes.Buffer
// is not safe for that. It keeps the tail of the stream, capped at
// stdioStderrCap bytes.
type boundedStderr struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) > stdioStderrCap {
		p = p[len(p)-stdioStderrCap:]
	}
	if b.buf.Len()+len(p) > stdioStderrCap {
		// Make room by dropping the oldest bytes — the tail (the newest
		// writes) is what explains a failure.
		b.buf.Next(b.buf.Len() + len(p) - stdioStderrCap)
	}
	return b.buf.Write(p)
}

// String returns the retained tail, safe for concurrent use.
func (b *boundedStderr) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (t *stdioProcessTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &stdioProcessConnection{
		Connection: conn,
		cleanup:    shell.AttachCommandCleanup(t.cmd),
	}, nil
}

type stdioProcessConnection struct {
	sdk.Connection
	cleanup *shell.CommandCleanup
}

func (c *stdioProcessConnection) Close() error {
	if c == nil {
		return nil
	}
	if c.Connection == nil {
		return ignoreProcessDone(c.cleanup.Cleanup())
	}

	done := make(chan error, 1)
	go func() {
		done <- c.Connection.Close()
	}()

	var cleanupErr error
	select {
	case connErr := <-done:
		cleanupErr = ignoreProcessDone(c.cleanup.Cleanup())
		return closeErr(connErr, cleanupErr)
	case <-time.After(stdioCloseGrace):
		cleanupErr = ignoreProcessDone(c.cleanup.Cleanup())
	}

	select {
	case connErr := <-done:
		return closeErr(connErr, cleanupErr)
	case <-time.After(stdioCloseLimit):
		return errors.Join(cleanupErr, fmt.Errorf("mcp stdio close timed out"))
	}
}

func closeErr(connErr, cleanupErr error) error {
	if cleanupErr == nil && isIntentionalProcessExit(connErr) {
		return nil
	}
	return errors.Join(connErr, cleanupErr)
}

func ignoreProcessDone(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func isIntentionalProcessExit(err error) bool {
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}
