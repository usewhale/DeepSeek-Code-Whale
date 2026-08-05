package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usewhale/whale/internal/build"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/shell"
)

const (
	StatusPending   = "pending"
	StatusDisabled  = "disabled"
	StatusStarting  = "starting"
	StatusConnected = "connected"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

type Manager struct {
	mu            sync.RWMutex
	cfg           Config
	workspaceRoot string
	sessions      map[string]*clientSession
	discovery     map[string][]discoveredTool
	states        map[string]ServerState
	tools         []core.Tool
}

type ServerState struct {
	Name      string
	Status    string
	Disabled  bool
	Connected bool
	Error     string
	Tools     int
	ToolNames []string
	Command   string
	URL       string
	Headers   []string
	Auth      string
}

type StartupEvent struct {
	State    ServerState
	Complete bool
}

type clientSession struct {
	cfg     ServerConfig
	session *sdk.ClientSession
	cancel  context.CancelFunc
}

type discoveredTool struct {
	serverName    string
	toolName      string
	spec          *sdk.Tool
	allowedDirs   []string
	workspaceRoot string
}

func NewManager(cfg Config, workspaceRoot ...string) *Manager {
	root := ""
	if len(workspaceRoot) > 0 {
		root = strings.TrimSpace(workspaceRoot[0])
	}
	m := &Manager{
		cfg:           cfg,
		workspaceRoot: root,
		sessions:      map[string]*clientSession{},
		discovery:     map[string][]discoveredTool{},
		states:        map[string]ServerState{},
	}
	m.resetStatesLocked()
	return m
}

func (m *Manager) Initialize(ctx context.Context) {
	m.InitializeWithEvents(ctx, nil)
}

func (m *Manager) InitializeWithEvents(ctx context.Context, emit func(StartupEvent)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.tools = nil
	m.discovery = map[string][]discoveredTool{}
	m.resetStatesLocked()
	m.mu.Unlock()
	events := make(chan StartupEvent, len(m.cfg.Servers)*2+1)
	var wg sync.WaitGroup
	for _, name := range sortedServerNames(m.cfg.Servers) {
		srv := m.cfg.Servers[name]
		srv.Name = name
		if srv.Disabled {
			m.setStateAndEmit(ServerState{Name: name, Status: StatusDisabled, Disabled: true}, emit)
			continue
		}
		wg.Add(1)
		go func(srv ServerConfig) {
			defer wg.Done()
			m.setState(ServerState{Name: srv.Name, Status: StatusStarting})
			events <- StartupEvent{State: m.stateByName(srv.Name)}
			if err := ctx.Err(); err != nil {
				m.setState(ServerState{Name: srv.Name, Status: StatusCancelled, Error: err.Error()})
				events <- StartupEvent{State: m.stateByName(srv.Name)}
				return
			}
			sess, discovered, toolNames, err := m.startServer(ctx, srv)
			if err != nil {
				status := StatusFailed
				if errors.Is(ctx.Err(), context.Canceled) {
					status = StatusCancelled
				}
				m.setState(ServerState{Name: srv.Name, Status: status, Error: err.Error()})
				events <- StartupEvent{State: m.stateByName(srv.Name)}
				return
			}
			events <- StartupEvent{State: m.registerConnectedServer(srv, sess, discovered, toolNames)}
		}(srv)
	}
	go func() {
		wg.Wait()
		close(events)
	}()
	for ev := range events {
		emitStartupEvent(emit, ev)
	}
	emitStartupEvent(emit, StartupEvent{Complete: true})
}

func (m *Manager) Tools() []core.Tool {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]core.Tool, len(m.tools))
	copy(out, m.tools)
	return out
}

func (m *Manager) States() []ServerState {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerState, 0, len(m.states))
	for _, st := range m.states {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) ConfigPath() string {
	if m == nil {
		return ""
	}
	return m.cfg.Path
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for name, sess := range m.sessions {
		if sess == nil {
			continue
		}
		if sess.session != nil {
			if err := sess.session.Close(); err != nil && first == nil {
				first = fmt.Errorf("close mcp %s: %w", name, err)
			}
		}
		if sess.cancel != nil {
			sess.cancel()
		}
		delete(m.sessions, name)
	}
	return first
}

func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*sdk.CallToolResult, error) {
	m.mu.RLock()
	sess := m.sessions[serverName]
	m.mu.RUnlock()
	if sess == nil || sess.session == nil {
		return nil, fmt.Errorf("mcp server %q is not connected", serverName)
	}
	callCtx, cancel := context.WithTimeout(ctx, sess.cfg.TimeoutDuration())
	defer cancel()
	return sess.session.CallTool(callCtx, &sdk.CallToolParams{Name: toolName, Arguments: args})
}

func (m *Manager) startServer(ctx context.Context, srv ServerConfig) (*clientSession, []discoveredTool, []string, error) {
	kind, err := srv.transportKind()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mcp server %q: %w", srv.Name, err)
	}
	mcpCtx, cancel := context.WithCancel(ctx)
	timeoutCtx, timeoutCancel := context.WithTimeout(mcpCtx, srv.TimeoutDuration())
	defer timeoutCancel()

	transport, stdioCmd, httpDiag, err := createTransport(mcpCtx, kind, srv)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "whale", Title: "Whale", Version: build.CurrentVersion()}, nil)
	session, err := client.Connect(timeoutCtx, transport, nil)
	if err != nil {
		cancel()
		if isContextTimeout(timeoutCtx, err) {
			return nil, nil, nil, startupTimeoutErr(srv, "connect")
		}
		if stdioCmd != nil {
			// Enrich ANY stdio connect failure with the captured stderr, not
			// just io.EOF: a server that exits before the handshake write
			// surfaces as EPIPE on Linux ("write |1: broken pipe"), which
			// errors.Is(err, io.EOF) misses — the diagnostic stderr was then
			// silently dropped.
			err = maybeStdioErr(err, stdioCmd, transport)
		}
		return nil, nil, nil, startupErr(srv, "connect", err, httpDiag)
	}
	listed, err := session.ListTools(timeoutCtx, &sdk.ListToolsParams{})
	if err != nil {
		_ = session.Close()
		cancel()
		if isContextTimeout(timeoutCtx, err) {
			return nil, nil, nil, startupTimeoutErr(srv, "list_tools")
		}
		return nil, nil, nil, startupErr(srv, "list_tools", err, httpDiag)
	}
	disabled := srv.disabledToolSet()
	allowedDirs := srv.filesystemAllowedDirs()
	discovered := make([]discoveredTool, 0, len(listed.Tools))
	toolNames := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" || disabled[tool.Name] {
			continue
		}
		toolNames = append(toolNames, strings.TrimSpace(tool.Name))
		discovered = append(discovered, discoveredTool{
			serverName:    srv.Name,
			toolName:      tool.Name,
			spec:          tool,
			allowedDirs:   allowedDirs,
			workspaceRoot: m.workspaceRoot,
		})
	}
	sort.Strings(toolNames)
	return &clientSession{cfg: srv, session: session, cancel: cancel}, discovered, toolNames, nil
}

func (m *Manager) registerConnectedServer(srv ServerConfig, sess *clientSession, discovered []discoveredTool, toolNames []string) ServerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[srv.Name] = sess
	m.discovery[srv.Name] = append([]discoveredTool(nil), discovered...)
	st := serverStateFromConfig(srv, StatusConnected)
	st.Connected = true
	st.Tools = len(discovered)
	st.ToolNames = append([]string(nil), toolNames...)
	m.states[srv.Name] = st
	m.tools = m.buildToolsLocked()
	return st
}

func (m *Manager) buildToolsLocked() []core.Tool {
	seen := map[string]bool{}
	tools := []core.Tool{}
	for _, serverName := range sortedDiscoveredServerNames(m.discovery) {
		for _, tool := range sortedDiscoveredTools(m.discovery[serverName]) {
			name := UniqueToolName(QualifyToolName(tool.serverName, tool.toolName), seen)
			tools = append(tools, &Tool{
				manager:        m,
				serverName:     tool.serverName,
				toolName:       tool.toolName,
				registeredName: name,
				spec:           tool.spec,
				allowedDirs:    tool.allowedDirs,
				workspaceRoot:  tool.workspaceRoot,
			})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })
	return tools
}

// BuildDeferredCatalog creates a DeferredToolCatalog from the current discovery data.
// Caller must hold a read lock or ensure the discovery map is stable.
func (m *Manager) BuildDeferredCatalog() *DeferredToolCatalog {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buildDeferredCatalogLocked()
}

func (m *Manager) buildDeferredCatalogLocked() *DeferredToolCatalog {
	seen := map[string]bool{}
	metas := make([]DeferredToolMeta, 0)
	for _, serverName := range sortedDiscoveredServerNames(m.discovery) {
		for _, tool := range sortedDiscoveredTools(m.discovery[serverName]) {
			qn := UniqueToolName(QualifyToolName(tool.serverName, tool.toolName), seen)
			desc := ""
			if tool.spec != nil {
				desc = strings.TrimSpace(tool.spec.Description)
			}
			if desc == "" {
				desc = "MCP tool"
			}
			metas = append(metas, DeferredToolMeta{
				Name:        qn,
				Server:      tool.serverName,
				Description: desc,
			})
		}
	}
	return NewDeferredToolCatalog(metas)
}

// BuildTools constructs full Tool objects for the given qualified names from discovery data.
func (m *Manager) BuildTools(names []string) ([]core.Tool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []core.Tool
	byName := make(map[string]discoveredTool)
	seen := map[string]bool{}
	// Resolve qualified names in the SAME deterministic order as
	// buildDeferredCatalogLocked so collision-disambiguated names (via
	// UniqueToolName) bind to the same underlying tools the catalog advertised.
	for _, serverName := range sortedDiscoveredServerNames(m.discovery) {
		for _, tool := range sortedDiscoveredTools(m.discovery[serverName]) {
			qn := UniqueToolName(QualifyToolName(tool.serverName, tool.toolName), seen)
			byName[qn] = tool
		}
	}
	for _, name := range names {
		dt, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("deferred tool %q not found in discovery data", name)
		}
		out = append(out, &Tool{
			manager:        m,
			serverName:     dt.serverName,
			toolName:       dt.toolName,
			registeredName: name,
			spec:           dt.spec,
			allowedDirs:    dt.allowedDirs,
			workspaceRoot:  dt.workspaceRoot,
		})
	}
	return out, nil
}

func sortedDiscoveredServerNames(discovery map[string][]discoveredTool) []string {
	names := make([]string, 0, len(discovery))
	for name := range discovery {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedDiscoveredTools(tools []discoveredTool) []discoveredTool {
	out := append([]discoveredTool(nil), tools...)
	sort.Slice(out, func(i, j int) bool { return out[i].toolName < out[j].toolName })
	return out
}

func createTransport(ctx context.Context, kind string, srv ServerConfig) (sdk.Transport, *exec.Cmd, *httpDiagnostics, error) {
	switch kind {
	case "stdio":
		if strings.TrimSpace(srv.Command) == "" {
			return nil, nil, nil, fmt.Errorf("mcp server %q requires command", srv.Name)
		}
		env, err := resolvedEnvPairs(srv.Env)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("mcp server %q env config: %w", srv.Name, err)
		}
		cmd := exec.CommandContext(ctx, expandStdioCommand(srv.Command), expandStdioArgs(srv.Args)...)
		cmd.Env = append(os.Environ(), env...)
		shell.ConfigureCommand(cmd)
		stderrBuf := &boundedStderr{}
		cmd.Stderr = stderrBuf // the SDK only touches stdout/stdin
		transport := &stdioProcessTransport{
			base:   &sdk.CommandTransport{Command: cmd},
			cmd:    cmd,
			stderr: stderrBuf,
		}
		return transport, cmd, nil, nil
	case "http":
		if strings.TrimSpace(srv.URL) == "" {
			return nil, nil, nil, fmt.Errorf("mcp server %q requires url", srv.Name)
		}
		headers, err := resolvedHeaders(srv.Headers)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("mcp server %q headers config: %w", srv.Name, err)
		}
		diag := &httpDiagnostics{}
		return &sdk.StreamableClientTransport{
			Endpoint: strings.TrimSpace(srv.URL),
			HTTPClient: &http.Client{Transport: headerRoundTripper{
				serverName: srv.Name,
				headers:    headers,
				base:       http.DefaultTransport,
				diag:       diag,
			}},
		}, nil, diag, nil
	default:
		return nil, nil, nil, fmt.Errorf("mcp server %q unsupported transport %q", srv.Name, kind)
	}
}

type headerRoundTripper struct {
	serverName string
	headers    map[string]string
	base       http.RoundTripper
	diag       *httpDiagnostics
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(rt.headers) > 0 {
		req = req.Clone(req.Context())
		for k, v := range rt.headers {
			req.Header.Set(k, v)
		}
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("mcp server %q http request failed (transport=http url=%s): %w", rt.serverName, safeHTTPURL(req.URL), err)
	}
	if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) && rt.diag != nil {
		rt.diag.record(req.URL, resp)
	}
	return resp, nil
}

func (m *Manager) setState(st ServerState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.states[st.Name]; ok {
		st = mergeServerStateMetadata(prev, st)
	}
	if st.Status == "" {
		switch {
		case st.Disabled:
			st.Status = StatusDisabled
		case st.Connected:
			st.Status = StatusConnected
		case st.Error != "":
			st.Status = StatusFailed
		}
	}
	m.states[st.Name] = st
}

func (m *Manager) stateByName(name string) ServerState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.states[name]
}

func mergeServerStateMetadata(prev, next ServerState) ServerState {
	if next.Command == "" {
		next.Command = prev.Command
	}
	if next.URL == "" {
		next.URL = prev.URL
	}
	if next.Auth == "" {
		next.Auth = prev.Auth
	}
	if len(next.Headers) == 0 {
		next.Headers = append([]string(nil), prev.Headers...)
	}
	if len(next.ToolNames) == 0 {
		next.ToolNames = append([]string(nil), prev.ToolNames...)
	}
	return next
}

func (m *Manager) resetStatesLocked() {
	m.states = map[string]ServerState{}
	for _, name := range sortedServerNames(m.cfg.Servers) {
		srv := m.cfg.Servers[name]
		srv.Name = name
		if srv.Disabled {
			m.states[name] = serverStateFromConfig(srv, StatusDisabled)
			continue
		}
		m.states[name] = serverStateFromConfig(srv, StatusPending)
	}
}

func serverStateFromConfig(srv ServerConfig, status string) ServerState {
	st := ServerState{
		Name:    strings.TrimSpace(srv.Name),
		Status:  status,
		Command: displayCommand(srv),
		URL:     displayURL(srv.URL),
		Headers: displayHeaders(srv.Headers),
		Auth:    displayAuth(srv),
	}
	if status == StatusDisabled || srv.Disabled {
		st.Disabled = true
	}
	return st
}

func displayURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return safeHTTPURL(u)
}

func displayCommand(srv ServerConfig) string {
	command := strings.TrimSpace(srv.Command)
	if command == "" {
		return ""
	}
	parts := []string{shellQuoteDisplay(command)}
	redactNext := false
	for _, arg := range srv.Args {
		display, next := redactCommandArgForDisplay(arg, redactNext)
		redactNext = next
		parts = append(parts, shellQuoteDisplay(display))
	}
	return strings.Join(parts, " ")
}

func redactCommandArgForDisplay(arg string, redact bool) (string, bool) {
	arg = strings.TrimSpace(arg)
	if redact {
		return "*****", false
	}
	if arg == "" {
		return arg, false
	}
	if key, value, ok := strings.Cut(arg, "="); ok {
		if isSecretCommandArgKey(key) {
			return key + "=*****", false
		}
		if looksSecretCommandArgValue(value) {
			return key + "=*****", false
		}
		return arg, false
	}
	if key, value, ok := strings.Cut(arg, ":"); ok {
		if isSecretCommandArgKey(key) || looksSecretCommandArgValue(value) {
			return key + ":*****", false
		}
		return arg, false
	}
	if isSecretCommandArgKey(arg) {
		return arg, true
	}
	if looksSecretCommandArgValue(arg) {
		return "*****", false
	}
	return arg, false
}

func isSecretCommandArgKey(key string) bool {
	key = strings.TrimLeft(strings.ToLower(strings.TrimSpace(key)), "-/")
	key = strings.ReplaceAll(key, "_", "-")
	for _, token := range []string{"api-key", "apikey", "auth", "authorization", "bearer", "client-secret", "credential", "password", "secret", "token"} {
		if key == token || strings.HasSuffix(key, "-"+token) || strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func looksSecretCommandArgValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if headerName, _, ok := strings.Cut(value, ":"); ok && isSecretCommandArgKey(headerName) {
		return true
	}
	value = strings.ToLower(value)
	return strings.HasPrefix(value, "bearer ") ||
		strings.HasPrefix(value, "sk-") ||
		strings.HasPrefix(value, "sk_") ||
		strings.HasPrefix(value, "xoxb-") ||
		strings.HasPrefix(value, "ghp_") ||
		strings.HasPrefix(value, "github_pat_")
}

func shellQuoteDisplay(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\n\"'\\$`") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func displayHeaders(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"=*****")
	}
	return out
}

func displayAuth(srv ServerConfig) string {
	if hasBearerAuthHeader(srv.Headers) {
		return "Bearer token"
	}
	return "Unsupported"
}

func hasBearerAuthHeader(headers map[string]string) bool {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), "Authorization") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") {
			return true
		}
	}
	return false
}

func (m *Manager) setStateAndEmit(st ServerState, emit func(StartupEvent)) {
	m.setState(st)
	emitStartupEvent(emit, StartupEvent{State: st})
}

func emitStartupEvent(emit func(StartupEvent), ev StartupEvent) {
	if emit != nil {
		emit(ev)
	}
}

func sortedServerNames(servers map[string]ServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func expandStdioCommand(command string) string {
	return expandStdioValue(command, true, runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func expandStdioArgs(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = expandStdioValue(arg, false, runtime.GOOS, os.Getenv, os.UserHomeDir)
	}
	return out
}

func expandStdioValue(value string, trim bool, goos string, getenv func(string) string, userHomeDir func() (string, error)) string {
	if trim {
		value = strings.TrimSpace(value)
	}
	if goos == "windows" {
		value = expandWindowsPercentEnv(value, getenv)
	}
	if value == "~" {
		if home, err := userHomeDir(); err == nil && home != "" {
			return home
		}
	}
	prefixes := []string{"~/"}
	if goos == "windows" {
		prefixes = append(prefixes, `~\`)
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			if home, err := userHomeDir(); err == nil && home != "" {
				return filepath.Join(home, strings.TrimPrefix(value, prefix))
			}
		}
	}
	return value
}

func expandWindowsPercentEnv(value string, getenv func(string) string) string {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '%' {
			out.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+1:], '%')
		if end < 0 {
			out.WriteByte(value[i])
			i++
			continue
		}
		name := value[i+1 : i+1+end]
		resolved := getenv(name)
		if strings.TrimSpace(name) == "" || resolved == "" {
			out.WriteString(value[i : i+end+2])
		} else {
			out.WriteString(resolved)
		}
		i += end + 2
	}
	return out.String()
}

func maybeStdioErr(err error, cmd *exec.Cmd, transport sdk.Transport) error {
	// Prefer the captured stderr from the original spawn — no re-spawn needed.
	if st, ok := transport.(*stdioProcessTransport); ok && st.stderr != nil {
		if out := strings.TrimSpace(st.stderr.String()); out != "" {
			return errors.Join(err, fmt.Errorf("%s", out))
		}
	}
	// Fallback for empty captures (e.g. the copy goroutine hadn't flushed):
	// re-run once to collect diagnostics.
	checkErr := stdioCheck(cmd)
	if checkErr == nil {
		return err
	}
	return errors.Join(err, checkErr)
}

func startupErr(srv ServerConfig, phase string, err error, httpDiag *httpDiagnostics) error {
	if err == nil {
		return nil
	}
	hint := startupHint(srv)
	if diag := httpDiag.summary(); diag != "" {
		return fmt.Errorf("mcp server %q failed during %s: %w (%s%s)", srv.Name, phase, err, diag, hint)
	}
	if hint != "" {
		return fmt.Errorf("mcp server %q failed during %s: %w (%s)", srv.Name, phase, err, strings.TrimPrefix(hint, "; "))
	}
	return fmt.Errorf("mcp server %q failed during %s: %w", srv.Name, phase, err)
}

func startupTimeoutErr(srv ServerConfig, phase string) error {
	if hint := startupHint(srv); hint != "" {
		return fmt.Errorf("mcp server %q timed out after %s during %s (%s)", srv.Name, srv.TimeoutDuration(), phase, strings.TrimPrefix(hint, "; "))
	}
	return fmt.Errorf("mcp server %q timed out after %s during %s", srv.Name, srv.TimeoutDuration(), phase)
}

func startupHint(srv ServerConfig) string {
	command := normalizedCommandBase(srv.Command)
	if command != "npx" && command != "npm" {
		return ""
	}
	return "; command uses npx/npm, which can download packages or consume stdio before the MCP server starts; install the MCP server and point command at its binary, or increase the server timeout"
}

func normalizedCommandBase(command string) string {
	command = strings.TrimSpace(command)
	command = strings.ReplaceAll(command, "\\", "/")
	command = strings.ToLower(filepath.Base(command))
	for _, suffix := range []string{".cmd", ".exe", ".bat"} {
		command = strings.TrimSuffix(command, suffix)
	}
	return command
}

func isContextTimeout(ctx context.Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

type httpDiagnostics struct {
	mu     sync.Mutex
	url    string
	status string
}

func (d *httpDiagnostics) record(reqURL *url.URL, resp *http.Response) {
	if d == nil || resp == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.url = safeHTTPURL(reqURL)
	d.status = resp.Status
}

func (d *httpDiagnostics) summary() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.status == "" {
		return ""
	}
	return fmt.Sprintf("transport=http url=%s status=%s", d.url, d.status)
}

func safeHTTPURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	out := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return out.String()
}

func stdioCheck(old *exec.Cmd) error {
	if old == nil {
		return nil
	}
	name := old.Path
	if name == "" && len(old.Args) > 0 {
		name = old.Args[0]
	}
	if name == "" {
		return nil
	}
	args := []string{}
	if len(old.Args) > 1 {
		args = old.Args[1:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = old.Env
	out, err := cmd.CombinedOutput()
	if err == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return fmt.Errorf("stdio diagnostic failed: %w", err)
	}
	return fmt.Errorf("%w: %s", err, output)
}
