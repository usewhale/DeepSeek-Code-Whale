package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/usewhale/whale/internal/acp"
	"github.com/usewhale/whale/internal/core"
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
