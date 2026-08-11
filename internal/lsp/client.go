package lsp

// Client manages a single language server subprocess and its JSON-RPC
// connection. Each Client corresponds to one language (e.g. "go", "python").
//
// Lifecycle:
//  1. Created by Manager.clientForServer / backgroundStart
//  2. Start() launches the server process and completes the LSP initialize
//     handshake (initialize → initialized notification)
//  3. ready is set to true after the handshake succeeds
//  4. On each tool operation, ensureDocumentOpen sends textDocument/didOpen
//     so the server parses the file (idempotent — skipped if already open)
//  5. Close() sends shutdown → exit, then kills the process after a timeout
//
// Thread safety: Client uses a sync.Mutex for connection/process state
// and an atomic.Bool for the ready flag.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/usewhale/whale/internal/shell"
)

// Client manages a single language server process.
type Client struct {
	language string   // display name (e.g. "go")
	command  string   // resolved executable path
	args     []string // command-line arguments
	rootURI  string

	env                   map[string]string // process environment overrides
	initializationOptions any               // passed to initialize request
	settings              any               // sent via workspace/didChangeConfiguration
	startupTimeout        time.Duration     // max time for initialize handshake
	shutdownTimeout       time.Duration     // max time for graceful shutdown
	languageByExt         map[string]string // extension → languageID (from server config)

	mu         sync.Mutex
	conn       *rpcConn
	cmd        *exec.Cmd
	caps       *ServerCapabilities
	cancel     context.CancelFunc
	done       chan struct{}
	ready      atomic.Bool             // true after initialize handshake completes
	readyCh    chan struct{}           // closed when ready becomes true
	starting   atomic.Bool             // true while a Start() call is in progress
	exited     atomic.Bool             // true after cmd.Wait() returns
	procDone   chan struct{}           // closed once cmd.Wait() has reaped the process
	cleanup    *shell.CommandCleanup   // kills the whole process tree (job object on Windows)
	stderrDone chan struct{}           // closed when stderr reader goroutine finishes
	stderrBuf  *strings.Builder        // captured stderr for crash diagnostics
	openDocs   map[string]openDocState // URI → didOpen state (bounded by maxOpenDocs)
}

// maxOpenDocs bounds how many documents stay open per server. Servers keep
// parsed state for every open document, so an unbounded set grows their
// memory with each didOpen; the least-recently-used document is closed once
// the limit is exceeded.
const maxOpenDocs = 64

type openDocState struct {
	modTime  time.Time // last modTime sent via didOpen
	lastUsed time.Time // last time a request touched this document
}

// Start launches the language server process and performs the LSP handshake.
// The client lock is only held briefly for field writes; the slow
// initialize handshake happens outside the lock.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil && c.ready.Load() {
		c.mu.Unlock()
		return nil // already started and ready
	}
	// Prevent concurrent Start() calls from racing to create the process.
	if !c.starting.CompareAndSwap(false, true) {
		c.mu.Unlock()
		// Another goroutine is already starting. Wait for it via readyCh.
		c.mu.Lock()
		ch := c.readyCh
		c.mu.Unlock()
		if ch != nil {
			select {
			case <-ch:
				if c.ready.Load() {
					return nil
				}
			case <-time.After(c.startupTimeout):
			}
		}
		return fmt.Errorf("language server start failed")
	}
	if c.openDocs == nil {
		c.openDocs = make(map[string]openDocState)
	}
	c.readyCh = make(chan struct{})
	readyCh := c.readyCh
	// Publish procDone up front so Close (and its test) can always observe a
	// process-tracking channel once a start attempt is in flight. The starting
	// CAS above is visible to isStarting() before the spawn block below stores
	// procDone, so Close must never see procDone == nil for a start that has
	// begun; the reaper goroutine closes the same channel when cmd.Wait()
	// returns.
	procDone := make(chan struct{})
	c.procDone = procDone
	c.mu.Unlock()
	defer c.starting.Store(false)
	// Release concurrent waiters even when startup fails (they check
	// c.ready to tell success from failure). Runs before the starting
	// flag is cleared, so no new Start can have replaced readyCh yet.
	defer func() {
		if !c.ready.Load() {
			close(readyCh)
		}
	}()

	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, c.command, c.args...)
	shell.ConfigureCommand(cmd)
	if len(c.env) > 0 {
		cmd.Env = mergeEnv(os.Environ(), c.env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start %s: %w", c.language, err)
	}
	// Attach after Start: the job object needs the live process handle to
	// cover the whole tree (a language server may spawn helpers such as
	// `go list` that hold handles on the workspace).
	cleanup := shell.AttachCommandCleanup(cmd)

	stderrBuf := new(strings.Builder)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		data, _ := io.ReadAll(stderr)
		if len(data) > 0 {
			stderrBuf.Write(data)
		}
	}()

	conn := newRPCConn(stdout, stdin)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.readLoop()
	}()

	// Wait for the process in the background to release OS resources.
	// procDone signals that the process has actually been reaped — on
	// Windows an unreaped process still holds handles on its working
	// directory. The channel was published before the spawn so Close can
	// always wait on it; the goroutine closes it once cmd.Wait() returns.
	go func() {
		defer close(procDone)
		if err := cmd.Wait(); err != nil {
			fmt.Fprintf(stderrBuf, "process exited: %v\n", err)
		}
		c.exited.Store(true)
	}()

	// Store fields so alive() sees them
	c.mu.Lock()
	c.conn = conn
	c.cmd = cmd
	c.cancel = cancel
	c.stderrBuf = stderrBuf
	c.stderrDone = stderrDone
	c.done = done
	c.procDone = procDone
	c.cleanup = cleanup
	c.mu.Unlock()

	// Slow: initialize handshake (gopls may take seconds)
	var initResult InitializeResult
	handshakeCtx, handshakeCancel := context.WithTimeout(cctx, c.startupTimeout)
	defer handshakeCancel()
	err = conn.sendRequest(handshakeCtx, "initialize", InitializeParams{
		ProcessID:             os.Getpid(),
		RootURI:               c.rootURI,
		InitializationOptions: c.initializationOptions,
		Capabilities: ClientCapabilities{
			TextDocument: &TextDocumentClientCapabilities{
				Hover:          &struct{ ContentFormat []string }{ContentFormat: []string{"markdown", "plaintext"}},
				Definition:     &struct{ LinkSupport bool }{},
				References:     &struct{}{},
				Implementation: &struct{ LinkSupport bool }{},
				DocumentSymbol: &struct {
					HierarchicalDocumentSymbolSupport bool `json:"hierarchicalDocumentSymbolSupport"`
				}{HierarchicalDocumentSymbolSupport: true},
				CallHierarchy: &struct{}{},
			},
			Workspace: &WorkspaceClientCapabilities{
				Symbol: &struct {
					SymbolKind struct{ ValueSet []int } `json:"symbolKind"`
				}{
					SymbolKind: struct{ ValueSet []int }{
						ValueSet: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
					},
				},
			},
		},
	}, &initResult)
	if err != nil {
		c.abortStart(cancel, cleanup, procDone, stderrDone)
		if stderrBuf.Len() > 0 {
			return fmt.Errorf("initialize: %w (stderr: %s)", err, stderrBuf.String())
		}
		return fmt.Errorf("initialize: %w", err)
	}

	// Send initialized notification
	if err := conn.sendNotification("initialized", struct{}{}); err != nil {
		c.abortStart(cancel, cleanup, procDone, stderrDone)
		if stderrBuf.Len() > 0 {
			return fmt.Errorf("initialized: %w (stderr: %s)", err, stderrBuf.String())
		}
		return fmt.Errorf("initialized: %w", err)
	}

	// Send workspace/didChangeConfiguration with settings, if any
	if c.settings != nil {
		_ = conn.sendNotification("workspace/didChangeConfiguration", map[string]any{
			"settings": c.settings,
		})
	}

	c.mu.Lock()
	c.caps = &initResult.Capabilities
	c.mu.Unlock()
	c.ready.Store(true)
	close(readyCh)
	return nil
}

// abortStart tears down a partially-started server: kill the process, then
// wait (bounded) until it is reaped so no directory handles survive.
// abortStart tears down a partially-started server: kill its process tree,
// then wait (bounded) until the process is reaped so no directory handles
// survive.
func (c *Client) abortStart(cancel context.CancelFunc, cleanup *shell.CommandCleanup, procDone, stderrDone chan struct{}) {
	cancel()
	_ = cleanup.Cleanup()
	<-stderrDone
	select {
	case <-procDone:
	case <-time.After(3 * time.Second):
	}
	c.mu.Lock()
	c.conn = nil
	c.mu.Unlock()
}

// readyChan returns the channel closed when the in-flight Start attempt
// finishes (successfully or not). Nil if no Start has begun yet.
func (c *Client) readyChan() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readyCh
}

// alive reports whether the server process and connection are still healthy.
func (c *Client) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready.Load() {
		return false
	}
	// Check if readLoop has exited
	select {
	case <-c.done:
		return false
	default:
	}
	// Check if process has exited (atomic set by cmd.Wait goroutine)
	if c.exited.Load() {
		return false
	}
	return true
}

// isStarting reports whether the server process has been launched but the
// initialize handshake has not yet completed, or a Start() call is in flight.
func (c *Client) isStarting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starting.Load() || (c.conn != nil && !c.ready.Load())
}

// Close gracefully shuts down the language server.
func (c *Client) Close() error {
	// Wait for any in-progress Start() to finish setting c.conn so
	// we don't return early and leak the process.
	deadline := time.Now().Add(5 * time.Second)
	for c.starting.Load() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		c.ready.Store(false)
		c.exited.Store(false)
		// The next process knows nothing about previously opened documents.
		c.openDocs = nil
	}()

	if c.conn == nil {
		return nil
	}

	// Best-effort graceful shutdown: send shutdown + exit with timeout
	timeout := c.shutdownTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), timeout)
	defer shutdownCancel()
	_ = c.conn.sendRequest(shutdownCtx, "shutdown", nil, nil)
	_ = c.conn.sendNotification("exit", nil)

	// Cancel context, drain readLoop, kill process
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
		}
	}
	if c.cleanup != nil {
		// Kill the whole tree: helpers spawned by the server (e.g. go list)
		// hold handles on the workspace too.
		_ = c.cleanup.Cleanup()
	} else if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	// Wait until the process is actually reaped: an unreaped process still
	// holds handles on its working directory (breaks temp-dir cleanup on
	// Windows and leaks the process elsewhere).
	if c.procDone != nil {
		select {
		case <-c.procDone:
		case <-time.After(2 * time.Second):
		}
	}

	c.conn = nil
	c.cmd = nil
	c.caps = nil
	return nil
}

// snapshotConn returns the current rpcConn under lock, or nil if closed.
func (c *Client) snapshotConn() *rpcConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

// --- LSP Operation Methods ---

func (c *Client) ensureDocumentOpen(ctx context.Context, uri string) error {
	conn := c.snapshotConn()
	if conn == nil {
		return fmt.Errorf("language server not connected")
	}

	path := URIToPath(uri)
	info, statErr := os.Stat(path)
	if statErr != nil {
		return fmt.Errorf("stat file for didOpen: %w", statErr)
	}

	// Skip if the file hasn't changed since the last didOpen.
	c.mu.Lock()
	prev, wasOpen := c.openDocs[uri]
	if wasOpen && prev.modTime.Equal(info.ModTime()) {
		prev.lastUsed = time.Now()
		c.openDocs[uri] = prev
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file for didOpen: %w", err)
	}

	langID := languageIDFromPath(c, path)

	// The LSP spec forbids didOpen for an already-open document (servers
	// keep the stale buffer); close the old version first.
	if wasOpen {
		_ = conn.sendNotification("textDocument/didClose", struct {
			TextDocument TextDocumentIdentifier `json:"textDocument"`
		}{TextDocument: TextDocumentIdentifier{URI: uri}})
	}

	err = conn.sendNotification("textDocument/didOpen", struct {
		TextDocument TextDocumentItem `json:"textDocument"`
	}{
		TextDocument: TextDocumentItem{
			URI:        uri,
			LanguageID: langID,
			Version:    0,
			Text:       string(data),
		},
	})
	if err != nil {
		return fmt.Errorf("didOpen: %w", err)
	}

	c.mu.Lock()
	if c.openDocs == nil {
		c.openDocs = make(map[string]openDocState)
	}
	c.openDocs[uri] = openDocState{modTime: info.ModTime(), lastUsed: time.Now()}
	evicted := evictOldestDocs(c.openDocs, maxOpenDocs, uri)
	c.mu.Unlock()
	for _, u := range evicted {
		_ = conn.sendNotification("textDocument/didClose", struct {
			TextDocument TextDocumentIdentifier `json:"textDocument"`
		}{TextDocument: TextDocumentIdentifier{URI: u}})
	}
	return nil
}

// evictOldestDocs removes least-recently-used entries until docs is within
// limit, never evicting keep. Returns the removed URIs.
func evictOldestDocs(docs map[string]openDocState, limit int, keep string) []string {
	var evicted []string
	for len(docs) > limit {
		oldest := ""
		var oldestAt time.Time
		for u, d := range docs {
			if u == keep {
				continue
			}
			if oldest == "" || d.lastUsed.Before(oldestAt) {
				oldest, oldestAt = u, d.lastUsed
			}
		}
		if oldest == "" {
			return evicted
		}
		delete(docs, oldest)
		evicted = append(evicted, oldest)
	}
	return evicted
}

// GoToDefinition returns the definition locations for a symbol.
func (c *Client) GoToDefinition(ctx context.Context, uri string, line, character int) ([]Location, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result []Location
	err := conn.sendRequest(ctx, "textDocument/definition", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	return result, err
}

// FindReferences returns all references to a symbol.
func (c *Client) FindReferences(ctx context.Context, uri string, line, character int, includeDeclaration bool) ([]Location, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result []Location
	err := conn.sendRequest(ctx, "textDocument/references", ReferenceParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: line, Character: character},
		},
		Context: ReferenceContext{IncludeDeclaration: includeDeclaration},
	}, &result)
	return result, err
}

// Hover returns hover information at a position.
func (c *Client) Hover(ctx context.Context, uri string, line, character int) (*HoverResult, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result HoverResult
	err := conn.sendRequest(ctx, "textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// DocumentSymbols returns all symbols in a document.
func (c *Client) DocumentSymbols(ctx context.Context, uri string) ([]DocumentSymbol, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	// Use json.RawMessage to determine format with a single RPC
	var raw json.RawMessage
	err := conn.sendRequest(ctx, "textDocument/documentSymbol", struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
	}{TextDocument: TextDocumentIdentifier{URI: uri}}, &raw)
	if err != nil {
		return nil, err
	}
	// Detect format by checking for "location" key in first array element.
	// SymbolInformation has a "location" field; DocumentSymbol does not.
	if len(raw) > 0 && raw[0] == '[' {
		var firstElements []struct {
			Location json.RawMessage `json:"location"`
		}
		if json.Unmarshal(raw, &firstElements) == nil && len(firstElements) > 0 && firstElements[0].Location != nil {
			var siResult []SymbolInformation
			if err := json.Unmarshal(raw, &siResult); err != nil {
				return nil, err
			}
			dsResult := make([]DocumentSymbol, 0, len(siResult))
			for _, si := range siResult {
				dsResult = append(dsResult, DocumentSymbol{
					Name:           si.Name,
					Kind:           si.Kind,
					Range:          si.Location.Range,
					SelectionRange: si.Location.Range,
				})
			}
			return dsResult, nil
		}
	}
	var dsResult []DocumentSymbol
	if err := json.Unmarshal(raw, &dsResult); err != nil {
		return nil, err
	}
	return dsResult, nil
}

// WorkspaceSymbols searches for symbols across the entire workspace.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]SymbolInformation, error) {
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result []SymbolInformation
	err := conn.sendRequest(ctx, "workspace/symbol", WorkspaceSymbolParams{Query: query}, &result)
	return result, err
}

// GoToImplementation returns implementation locations.
func (c *Client) GoToImplementation(ctx context.Context, uri string, line, character int) ([]Location, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result []Location
	err := conn.sendRequest(ctx, "textDocument/implementation", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	return result, err
}

// PrepareCallHierarchy returns call hierarchy items for a symbol.
func (c *Client) PrepareCallHierarchy(ctx context.Context, uri string, line, character int) ([]CallHierarchyItem, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result []CallHierarchyItem
	err := conn.sendRequest(ctx, "textDocument/prepareCallHierarchy", CallHierarchyPrepareParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	return result, err
}

// IncomingCalls returns functions that call the given item.
func (c *Client) IncomingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyIncomingCall, error) {
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result []CallHierarchyIncomingCall
	err := conn.sendRequest(ctx, "callHierarchy/incomingCalls", struct {
		Item CallHierarchyItem `json:"item"`
	}{Item: item}, &result)
	return result, err
}

// OutgoingCalls returns functions called by the given item.
func (c *Client) OutgoingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyOutgoingCall, error) {
	conn := c.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("language server not connected")
	}
	var result []CallHierarchyOutgoingCall
	err := conn.sendRequest(ctx, "callHierarchy/outgoingCalls", struct {
		Item CallHierarchyItem `json:"item"`
	}{Item: item}, &result)
	return result, err
}

// mergeEnv creates a combined environment from base plus overrides.
// Keys in overrides replace those in base.
func mergeEnv(base []string, overrides map[string]string) []string {
	envMap := make(map[string]string, len(base))
	for _, kv := range base {
		if idx := strings.Index(kv, "="); idx >= 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}
	for k, v := range overrides {
		envMap[k] = v
	}
	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result
}

// languageIDFromPath returns a LSP language ID for a file path.
// languageIDFromPath returns a LSP language ID for a file path. When the
// client carries an extension→language mapping (from the server config), it
// is used; otherwise a built-in table provides reasonable defaults for the
// most common file types, falling back to the bare extension.
func languageIDFromPath(c *Client, path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if c.languageByExt != nil {
		if id, ok := c.languageByExt[ext]; ok {
			return id
		}
	}
	// The extension may be empty for dot-files or extensionless paths; the
	// built-in table covers known languages; everything else falls through
	// to the bare extension.
	switch ext {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py", ".pyi":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".mjs", ".cjs":
		return "javascript"
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".h":
		return "c"
	case ".hpp", ".hxx":
		return "cpp"
	case ".yaml", ".yml":
		return "yaml"
	case ".vue":
		return "vue"
	case ".json", ".jsonc":
		return "json"
	case ".css", ".scss", ".less":
		return "css"
	case ".html", ".htm":
		return "html"
	default:
		if ext != "" && ext[0] == '.' {
			return ext[1:]
		}
		return ext
	}
}
