# MCP (Model Context Protocol)

Whale can load tools from MCP servers at startup. MCP tools are registered
as normal Whale tools with names like `mcp__server__tool`.

> MCP lets you connect Whale to databases, APIs, browser automation,
> and 1,000+ other tools through a standard protocol.

---

## Quick Setup

### 1. Create or edit `~/.whale/mcp.json`

```json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }
  }
}
```

### 2. Restart Whale

MCP servers are loaded at startup. After restarting, run `/mcp` in the TUI
to check that your server is connected.

---

## Supported Transports

| Transport | When to use | Example |
|---|---|---|
| **stdio** | Local servers (npx, pip, go binaries) | `npx -y some-mcp-server` |
| **Streamable HTTP** | Remote servers over HTTP | `url: "https://example.com/mcp"` |

Whale does not currently support SSE MCP servers.

---

## Config File

Default path: `~/.whale/mcp.json` (or `$WHALE_HOME/mcp.json` if set).

Custom path via `config.toml`:

```toml
[mcp]
config_path = "/path/to/mcp.json"
```

On Windows, the default path resolves under `%USERPROFILE%\\.whale`.

---

## ACP (whale-acp)

The ACP agent (`whale-acp`) reads the same `~/.whale/mcp.json` baseline and
merges it with the `mcpServers` an ACP client sends on `session/new` and
`session/load` (client wins on name conflicts).

- Client-supplied servers must be stdio: whale-acp advertises
  `mcpCapabilities {http: false, sse: false}` and rejects http/sse ones.
- The local baseline is passed through unchanged, so an http server in
  `~/.whale/mcp.json` still connects (same as the main app) even though the
  client-facing advertisement is stdio-only.
- MCP tools are lazy-loaded: the schema starts with only `tool_search`; the
  model selects tools to activate them. Call `tool_search` with an empty
  query to list available tools (it serves the `<available-deferred-tools>`
  block). ACP sessions have no `/mcp` command — check the agent's stderr log
  for server state instead.
- `session/new` waits while servers connect (per-server timeout, default
  15s), so a slow server delays session creation.

**Security:** the ACP host is fully trusted — it controls `cwd`, may supply
`mcpServers` (arbitrary stdio processes), and can drive any tool. whale-acp
defaults permissions to **ask** (`permissions.default = "ask"`): operations not explicitly
allowed prompt the user via `session/request_permission`. Restore the
permissive behavior with `permissions.default = "allow"` in `config.toml`, or tighten
further with `permissions.default = "deny"` plus explicit `permissions` sections.

## Examples

### stdio — Filesystem server

```json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "timeout": 15
    }
  }
}
```

### stdio — Context7 (documentation search)

```json
{
  "mcpServers": {
    "context7": {
      "command": "npx",
      "args": ["-y", "@upstash/context7-mcp"]
    }
  }
}
```

### Streamable HTTP — Remote server

```json
{
  "mcpServers": {
    "remote": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${MCP_TOKEN}"
      },
      "timeout": 15
    }
  }
}
```

`type` can be `http`, `streamable-http`, `streamable_http`, or `streamablehttp`.

Environment variables in config values: use `${NAME}` syntax. Whale fails
startup for that server if the variable is missing.

---

## Optional Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `timeout` | number | `15` | Startup and call timeout in seconds |
| `disabled` | boolean | `false` | Skip this server |
| `disabled_tools` | string[] | `[]` | Hide specific tools (use the original MCP tool name, e.g. `read_file`) |
| `env` | object | `{}` | Environment variables for stdio servers |
| `headers` | object | `{}` | HTTP headers for Streamable HTTP servers |

### Example: Restrict a filesystem server to read-only

```json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "disabled_tools": ["write_file"]
    }
  }
}
```

---

## Check Status

In the TUI, run:

```text
/mcp
```

It shows the config path, server count, connection status, tool count,
and startup errors.

```
MCP Tools

config: /Users/me/.whale/mcp.json
servers: 2

- context7
  status: connected
  auth: unsupported
  command: npx -y @upstash/context7-mcp
  tools: resolve-library-id, get-library-docs

- remote
  status: failed
  auth: bearer token
  url: https://example.com/mcp
  error: mcp server "remote" failed during connect: ... status=401 Unauthorized
```

---

## Common Issues

**Only one server shows up.** Make sure every server is inside `mcpServers`:

```json
{
  "mcpServers": {
    "fs": { "command": "npx", "args": ["-y", "server-filesystem", "/tmp"] },
    "context7": { "command": "npx", "args": ["-y", "@upstash/context7-mcp"] }
  }
}
```

Do not put a server at the top level next to `mcpServers`.

**HTTP errors:**
- `401` / `403` — check `headers`, API keys, and env variables
- `404` — check the URL is the MCP endpoint, not a homepage
- `429` / `5xx` — check provider rate limits or service status

---

## Security Notes

- Whale does not inspect MCP tool arguments for filesystem paths.
  Configure filesystem access in the MCP server itself.
- Use `[permissions.mcp]` in `config.toml` to set `allow` / `ask` / `deny` per server.
- Do not commit `~/.whale/mcp.json` — it may contain secrets.
- Whale redacts secret-like MCP command arguments in `/mcp` output.
