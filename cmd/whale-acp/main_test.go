package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/usewhale/whale/internal/acp"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/defaults"
	"github.com/usewhale/whale/internal/llm/deepseek"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/tools"
)

// TestMain doubles as the entry point for the in-process stdio MCP server used
// by the deferred-loading tests: when WHALE_ACP_TEST_MCP_SERVER is set, the
// test binary re-executes itself (via the MCP manager's stdio transport) and
// runs a minimal echo server instead of the test suite.
func TestMain(m *testing.M) {
	if os.Getenv(runACPMCPServerEnv) == "1" {
		os.Unsetenv(runACPMCPServerEnv)
		os.Exit(runACPMCPServer())
	}
	os.Exit(m.Run())
}

const runACPMCPServerEnv = "WHALE_ACP_TEST_MCP_SERVER"

type acpEchoInput struct {
	Message string `json:"message"`
}

type acpEchoOutput struct {
	Message string `json:"message"`
}

func runACPMCPServer() int {
	server := sdk.NewServer(&sdk.Implementation{Name: "whale-acp-test-mcp", Version: "v0.0.0"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "echoes a message"}, func(ctx context.Context, req *sdk.CallToolRequest, input acpEchoInput) (*sdk.CallToolResult, acpEchoOutput, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: "echo:" + input.Message}},
		}, acpEchoOutput{Message: input.Message}, nil
	})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// TestMCPConfigForSessionMergesBaselineAndClient verifies the session MCP
// config merges the local ~/.whale/mcp.json baseline with client-supplied
// servers, with client servers winning on name conflicts, and converts the
// ACP env array into the map form the MCP manager expects.
func TestMCPConfigForSessionMergesBaselineAndClient(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{
		"mcpServers": {"local": {"command": "/bin/echo", "args": ["a"]}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := mcpConfigForSession(dir, []acp.MCPServer{
		{Name: "local", Command: "/bin/false"}, // client overrides baseline
		{Name: "client", Command: "/bin/true",
			Env: []acp.EnvVariable{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}},
	})
	if err != nil {
		t.Fatalf("mcpConfigForSession: %v", err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("expected 2 merged servers, got %d: %+v", len(cfg.Servers), cfg.Servers)
	}
	if got := cfg.Servers["local"]; got.Command != "/bin/false" {
		t.Errorf("client should win on name conflict: local.command=%q", got.Command)
	}
	if got := cfg.Servers["client"]; got.Command != "/bin/true" || got.Env["A"] != "1" || got.Env["B"] != "2" {
		t.Errorf("client server not merged with env: %+v", got)
	}
}

func TestMCPConfigForSessionNoConfigFile(t *testing.T) {
	cfg, err := mcpConfigForSession(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("missing config should not error: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("expected no servers, got %+v", cfg.Servers)
	}
}

func TestEnvVariableMap(t *testing.T) {
	m := envVariableMap([]acp.EnvVariable{
		{Name: "A", Value: "1"},
		{Name: "B", Value: "2"},
		{Name: "", Value: "dropped"},
	})
	if len(m) != 2 || m["A"] != "1" || m["B"] != "2" {
		t.Errorf("unexpected map: %+v", m)
	}
	if _, ok := m[""]; ok {
		t.Error("empty env name should be skipped")
	}
}

// TestMCPConfigForSessionSkipsNonStdioClientServers verifies that
// client-supplied http/sse MCP servers are rejected: whale-acp advertises
// mcpCapabilities {http:false, sse:false}, i.e. stdio only.
func TestMCPConfigForSessionSkipsNonStdioClientServers(t *testing.T) {
	cfg, err := mcpConfigForSession(t.TempDir(), []acp.MCPServer{
		{Name: "ok", Command: "/bin/echo"},
		{Name: "httpSrv", URL: "http://localhost:3000/mcp"},
		{Name: "sseSrv", Type: "sse", URL: "http://localhost:3000/sse"},
		{Name: "httpType", Type: "http", Command: "/bin/echo"},
	})
	if err != nil {
		t.Fatalf("mcpConfigForSession: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("expected only the stdio server to survive, got %d: %+v", len(cfg.Servers), cfg.Servers)
	}
	if _, ok := cfg.Servers["ok"]; !ok {
		t.Errorf("stdio server missing: %+v", cfg.Servers)
	}
}

// TestClientMCPServerTransport verifies transport classification for
// client-supplied MCP servers.
func TestClientMCPServerTransport(t *testing.T) {
	cases := []struct {
		in   acp.MCPServer
		want string
	}{
		{acp.MCPServer{Name: "s", Command: "/bin/echo"}, "stdio"},
		{acp.MCPServer{Name: "s", URL: "http://x/mcp"}, "http"},
		{acp.MCPServer{Name: "s", Type: "sse"}, "sse"},
		{acp.MCPServer{Name: "s", Type: "streamable-http"}, "http"},
		{acp.MCPServer{Name: "s", Type: "  "}, "stdio"},
	}
	for _, c := range cases {
		if got := clientMCPServerTransport(c.in); got != c.want {
			t.Errorf("clientMCPServerTransport(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// writeEchoServerConfig writes an mcp.json whose server re-executes this test
// binary as the in-process stdio echo server.
func writeEchoServerConfig(t *testing.T, dataDir string) {
	t.Helper()
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"echo-srv": map[string]any{
				"command": os.Args[0],
				"args":    []string{"-test.run=^$"},
				"env":     map[string]string{runACPMCPServerEnv: "1"},
				"timeout": 10,
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "mcp.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func registryNames(reg *core.ToolRegistry) []string {
	var names []string
	for _, tool := range reg.Tools() {
		names = append(names, tool.Name())
	}
	return names
}

// TestWireMCPServersDeferredNoEagerLoading verifies the ACP MCP wiring
// complies with the codebase's lazy-loading design (bench/deferred_compare,
// internal/app/mcp_runtime.go): connected MCP tools must NOT appear in the
// session tool schema until the agent explicitly promotes them via
// tool_search, and promotion must add them on demand.
func TestWireMCPServersDeferredNoEagerLoading(t *testing.T) {
	dataDir := t.TempDir()
	writeEchoServerConfig(t, dataDir)

	ts, err := tools.NewToolset(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr, reg := wireMCPServers(ts, dataDir, t.TempDir(), nil)
	if mgr == nil {
		t.Fatal("expected a manager when servers are configured")
	}
	t.Cleanup(func() { _ = mgr.Close() })

	// 1. No eager loading: the registry must contain no mcp__ tools.
	for _, tool := range reg.Tools() {
		if strings.HasPrefix(tool.Name(), "mcp__") {
			t.Fatalf("eager MCP tool leaked into schema: %s", tool.Name())
		}
	}

	// 2. Deferred discovery is wired: tool_search must be present.
	var searchTool core.Tool
	for _, tool := range reg.Tools() {
		if tool.Name() == "tool_search" {
			searchTool = tool
			break
		}
	}
	if searchTool == nil {
		t.Fatalf("tool_search not wired despite connected MCP servers; tools: %v", registryNames(reg))
	}

	// 3. Promotion is lazy: selecting the tool via tool_search adds it to the
	// registry on demand, proving no eager registration. The qualified name is
	// derived from the catalog (server/tool components are sanitized, e.g.
	// "echo-srv" becomes "echo_srv").
	catalog := mgr.BuildDeferredCatalog()
	names := catalog.Names()
	if len(names) != 1 {
		t.Fatalf("expected 1 deferred tool in catalog, got %v", names)
	}
	target := names[0]
	query, err := json.Marshal(map[string]string{"query": "select:" + target})
	if err != nil {
		t.Fatal(err)
	}
	res, err := searchTool.Run(context.Background(), core.ToolCall{
		ID:    "call-1",
		Name:  "tool_search",
		Input: string(query),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError() {
		t.Fatalf("tool_search error: %s", res.ModelText)
	}
	found := false
	for _, tool := range reg.Tools() {
		if tool.Name() == target {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("promoted tool %q not registered after tool_search select; tools: %v", target, registryNames(reg))
	}
}

// TestWireMCPServersNoConfigNoDeferredTools verifies that without MCP config
// there is no manager, no mcp__ tools, and no tool_search (empty catalog).
func TestWireMCPServersNoConfigNoDeferredTools(t *testing.T) {
	ts, err := tools.NewToolset(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr, reg := wireMCPServers(ts, t.TempDir(), t.TempDir(), nil)
	if mgr != nil {
		t.Fatal("expected nil manager without MCP config")
	}
	for _, tool := range reg.Tools() {
		if strings.HasPrefix(tool.Name(), "mcp__") {
			t.Fatalf("unexpected mcp tool without config: %s", tool.Name())
		}
		if tool.Name() == "tool_search" {
			t.Fatal("tool_search present without MCP catalog")
		}
	}
}

// TestWireMCPServersFailedServerNoCatalog verifies that a server that cannot
// start is reported as failed and yields no mcp__ tools and no tool_search
// (empty deferred catalog) instead of crashing session creation.
func TestWireMCPServersFailedServerNoCatalog(t *testing.T) {
	dataDir := t.TempDir()
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"boom": map[string]any{
				"command": "/nonexistent/definitely-not-a-binary",
				"timeout": 2,
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "mcp.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	ts, err := tools.NewToolset(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr, reg := wireMCPServers(ts, dataDir, t.TempDir(), nil)
	if mgr == nil {
		t.Fatal("expected a manager when servers are configured")
	}
	t.Cleanup(func() { _ = mgr.Close() })
	for _, tool := range reg.Tools() {
		if strings.HasPrefix(tool.Name(), "mcp__") {
			t.Fatalf("unexpected mcp tool from failed server: %s", tool.Name())
		}
		if tool.Name() == "tool_search" {
			t.Fatal("tool_search present with empty catalog")
		}
	}
	states := mgr.States()
	if len(states) != 1 || states[0].Status != whalemcp.StatusFailed {
		t.Fatalf("expected one failed server state, got %+v", states)
	}
}

func TestModelFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv("WHALE_MODEL", "")
		m, err := modelFromEnv()
		if err != nil {
			t.Fatalf("modelFromEnv: %v", err)
		}
		if m != defaults.DefaultModel {
			t.Fatalf("model = %q, want default %q", m, defaults.DefaultModel)
		}
	})
	t.Run("valid override", func(t *testing.T) {
		t.Setenv("WHALE_MODEL", defaults.ProModel)
		m, err := modelFromEnv()
		if err != nil {
			t.Fatalf("modelFromEnv: %v", err)
		}
		if m != defaults.ProModel {
			t.Fatalf("model = %q, want %q", m, defaults.ProModel)
		}
	})
	t.Run("retired chat alias rejected", func(t *testing.T) {
		t.Setenv("WHALE_MODEL", "deepseek-chat")
		if _, err := modelFromEnv(); err == nil || !strings.Contains(err.Error(), "unsupported model") {
			t.Fatalf("err = %v, want unsupported model error", err)
		}
	})
	t.Run("mixed case canonicalized", func(t *testing.T) {
		// IsSupportedModel matches case-insensitively, but the returned name
		// feeds the provider, the window derivation (case-insensitive) AND the
		// transport inference (case-sensitive prefix). A non-canonical name
		// would make window (1M) and transport (chat completions) disagree and
		// send a non-canonical slug to the API — so the canonical lowercase
		// slug must be returned.
		for _, in := range []string{"DeepSeek-V4-Flash", " DEEPSEEK-V4-PRO ", "DeepSeek-V4-FLASH"} {
			t.Setenv("WHALE_MODEL", in)
			m, err := modelFromEnv()
			if err != nil {
				t.Fatalf("modelFromEnv(%q): %v", in, err)
			}
			want := strings.ToLower(strings.TrimSpace(in))
			if m != want {
				t.Fatalf("modelFromEnv(%q) = %q, want canonical %q", in, m, want)
			}
		}
	})
}

func TestAPIFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		want  deepseek.API
		error bool
	}{
		{name: "unset", env: "", want: deepseek.APIAuto},
		{name: "responses", env: "responses", want: deepseek.APIResponses},
		{name: "chat_completions", env: "chat_completions", want: deepseek.APIChatCompletions},
		{name: "auto", env: "auto", want: deepseek.APIAuto},
		{name: "alias rejected", env: "completions", error: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WHALE_API", tc.env)
			got, err := apiFromEnv()
			if tc.error {
				if err == nil {
					t.Fatalf("apiFromEnv(%q): expected error", tc.env)
				}
				return
			}
			if err != nil {
				t.Fatalf("apiFromEnv(%q): %v", tc.env, err)
			}
			if got != tc.want {
				t.Fatalf("apiFromEnv(%q) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

func TestMaxToolItersFromEnv(t *testing.T) {
	t.Run("default 300 when unset", func(t *testing.T) {
		t.Setenv("WHALE_MAX_TOOL_ITERS", "")
		got, err := maxToolItersFromEnv()
		if err != nil {
			t.Fatalf("maxToolItersFromEnv: %v", err)
		}
		if got != defaults.DefaultMaxToolIters {
			t.Fatalf("got %d, want %d", got, defaults.DefaultMaxToolIters)
		}
	})
	t.Run("explicit override", func(t *testing.T) {
		t.Setenv("WHALE_MAX_TOOL_ITERS", "450")
		got, err := maxToolItersFromEnv()
		if err != nil {
			t.Fatalf("maxToolItersFromEnv: %v", err)
		}
		if got != 450 {
			t.Fatalf("got %d, want 450", got)
		}
	})
	t.Run("zero rejected (cap must stay finite)", func(t *testing.T) {
		t.Setenv("WHALE_MAX_TOOL_ITERS", "0")
		if _, err := maxToolItersFromEnv(); err == nil {
			t.Fatal("expected error for 0 cap")
		}
	})
	t.Run("negative rejected", func(t *testing.T) {
		t.Setenv("WHALE_MAX_TOOL_ITERS", "-5")
		if _, err := maxToolItersFromEnv(); err == nil {
			t.Fatal("expected error for negative cap")
		}
	})
	t.Run("non-numeric rejected", func(t *testing.T) {
		t.Setenv("WHALE_MAX_TOOL_ITERS", "lots")
		if _, err := maxToolItersFromEnv(); err == nil {
			t.Fatal("expected error for non-numeric cap")
		}
	})
}

func TestCompactThresholdFromEnv(t *testing.T) {
	t.Run("default 0.85 when unset", func(t *testing.T) {
		t.Setenv("WHALE_COMPACT_THRESHOLD", "")
		got, err := compactThresholdFromEnv()
		if err != nil {
			t.Fatalf("compactThresholdFromEnv: %v", err)
		}
		if got != defaults.DefaultAutoCompactThreshold {
			t.Fatalf("got %v, want %v (CLI parity, not the agent's 0.90)", got, defaults.DefaultAutoCompactThreshold)
		}
	})
	t.Run("explicit override", func(t *testing.T) {
		t.Setenv("WHALE_COMPACT_THRESHOLD", "0.7")
		got, err := compactThresholdFromEnv()
		if err != nil {
			t.Fatalf("compactThresholdFromEnv: %v", err)
		}
		if got != 0.7 {
			t.Fatalf("got %v, want 0.7", got)
		}
	})
	// NaN must be rejected: it satisfies neither f <= 0 nor f >= 1, so a bare
	// range check would accept it and WithAutoCompact's threshold guard would
	// then silently keep the agent's 0.90 default — the trap this knob exists
	// to avoid. Infinities must be rejected too.
	for _, bad := range []string{"0", "1", "1.5", "-0.2", "abc", "NaN", "nan", "+Inf", "-Inf", "Inf"} {
		t.Run("reject "+bad, func(t *testing.T) {
			t.Setenv("WHALE_COMPACT_THRESHOLD", bad)
			if _, err := compactThresholdFromEnv(); err == nil {
				t.Fatalf("expected error for threshold %q", bad)
			}
		})
	}
}

func TestContextWindowFromEnv(t *testing.T) {
	t.Run("unset returns 0 (model-derived)", func(t *testing.T) {
		t.Setenv("WHALE_CONTEXT_WINDOW", "")
		got, err := contextWindowFromEnv()
		if err != nil {
			t.Fatalf("contextWindowFromEnv: %v", err)
		}
		if got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})
	t.Run("explicit override", func(t *testing.T) {
		t.Setenv("WHALE_CONTEXT_WINDOW", "512000")
		got, err := contextWindowFromEnv()
		if err != nil {
			t.Fatalf("contextWindowFromEnv: %v", err)
		}
		if got != 512000 {
			t.Fatalf("got %d, want 512000", got)
		}
	})
	for _, bad := range []string{"0", "-1", "x", "1.5"} {
		t.Run("reject "+bad, func(t *testing.T) {
			t.Setenv("WHALE_CONTEXT_WINDOW", bad)
			if _, err := contextWindowFromEnv(); err == nil {
				t.Fatalf("expected error for window %q", bad)
			}
		})
	}
}

// TestSanitizeLogName verifies control characters are stripped before server
// names/errors hit the log (log-injection defense), including the C0 controls
// and DEL beyond newline/CR/tab.
func TestSanitizeLogName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"evil\nserver\r\tname", "evil server  name"},
		{"plain-name", "plain-name"},
		{"", ""},
		{"nul\x00esc\x1bdel\x7f", "nul esc del "},
		{"tab\there", "tab here"},
	}
	for _, c := range cases {
		if got := sanitizeLogName(c.in); got != c.want {
			t.Errorf("sanitizeLogName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSortedKeys verifies deterministic iteration order for logs.
func TestSortedKeys(t *testing.T) {
	if got := sortedKeys(map[string]int{}); len(got) != 0 {
		t.Fatalf("empty map: %v", got)
	}
	got := sortedKeys(map[string]int{"z": 1, "a": 2, "m": 3})
	if strings.Join(got, ",") != "a,m,z" {
		t.Fatalf("sortedKeys = %v, want a,m,z", got)
	}
}

// writeMCPConfig writes an mcp.json baseline into dir.
func writeMCPConfig(t *testing.T, dir string, servers map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
// TestMCPConfigForSessionLogLines verifies the changed log sites in
// mcpConfigForSession: the client-supplied server count and the baseline http
// url-transport warning are both emitted and sanitized.
func TestMCPConfigForSessionLogLines(t *testing.T) {
	var logs bytes.Buffer
	prev := acp.Logger
	acp.Logger = log.New(&logs, "", 0)
	defer func() { acp.Logger = prev }()

	dataDir := t.TempDir()
	writeMCPConfig(t, dataDir, map[string]any{
		"baseline-http": map[string]any{"url": "http://127.0.0.1:9999"},
	})
	client := []acp.MCPServer{
		{Name: "client-a", Command: "/bin/echo", Args: []string{"hi"}},
		{Name: "client-b", Command: "/bin/echo", Args: []string{"bye"}},
	}
	cfg, err := mcpConfigForSession(dataDir, client)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Servers["client-a"].Command; got != "/bin/echo" {
		t.Fatalf("client server not merged into config: %+v", cfg.Servers["client-a"])
	}
	out := logs.String()
	if !strings.Contains(out, "connecting 2 MCP server(s) supplied by the ACP client") {
		t.Errorf("missing client-count log line, got:\n%s", out)
	}
	if !strings.Contains(out, "baseline mcp server baseline-http uses url transport") {
		t.Errorf("missing baseline url-transport warning, got:\n%s", out)
	}
}