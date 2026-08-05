# MCP

Whale can load tools from MCP servers at startup.

MCP tools are registered as normal Whale tools with names like `mcp__server__tool`. Normal approval behavior still applies.
Whale does not inspect MCP tool arguments for filesystem paths; configure filesystem access in the MCP server itself and use `[permissions.mcp]` or `disabled_tools` to control which MCP tools can run.
If you previously relied on Whale-side path checks for `@modelcontextprotocol/server-filesystem`, move those limits into the filesystem server's own directory arguments and use `[permissions.mcp]` only for tool-level `allow`, `ask`, or `deny` rules.

## Config file

By default, Whale reads:

```text
~/.whale/mcp.json
```

On Windows this resolves under `%USERPROFILE%\.whale` by default. If `WHALE_HOME`
is set, Whale reads the default MCP config from `$WHALE_HOME/mcp.json`.

You can use another file by setting `[mcp].config_path` in `config.toml`:

```toml
[mcp]
config_path = "/path/to/mcp.json"
```

Whale reads MCP config when the process starts. Restart Whale after editing the file.
Servers are started concurrently, so `/mcp` can show some servers as `starting`
while other servers are already connected and usable.

## Supported transports

Whale currently supports:

- stdio MCP servers using `command` and `args`
- Streamable HTTP MCP servers using `url` and optional `headers`

Whale does not currently support SSE MCP servers.

## Config format

Put servers under `mcpServers`:

```json
{
  "mcpServers": {
    "server-name": {
      "command": "npx",
      "args": ["-y", "some-mcp-server"]
    }
  }
}
```

Whale also accepts `servers`, but `mcpServers` is the recommended format because it matches common MCP examples.

## ACP（whale-acp）

ACP 代理（`whale-acp`）读取同一个 `~/.whale/mcp.json` 基线，并与 ACP 客户端在
`session/new`、`session/load` 中发送的 `mcpServers` 合并（名称冲突时客户端优先）。

- 客户端下发的服务必须为 stdio：whale-acp 声明 `mcpCapabilities {http: false, sse: false}`，
  并拒绝 http/sse 服务。
- 本地基线原样透传，因此 `~/.whale/mcp.json` 中的 http 服务仍会连接（与主程序一致），
  即使面向客户端的声明仅为 stdio。
- MCP 工具为懒加载：schema 起始只有 `tool_search`，由模型按需选中激活。
  以空查询调用 `tool_search` 可列出可用工具（返回 `<available-deferred-tools>` 块）。
  ACP 会话没有 `/mcp` 命令，可查看代理 stderr 日志确认服务状态。
- `session/new` 会等待服务连接完成（单服务超时，默认 15s），慢服务会延迟会话创建。

## stdio examples

Filesystem server:

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

Context7 server:

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

If the server requires an API key, prefer environment variables instead of committing secrets to the config:

```json
{
  "mcpServers": {
    "context7": {
      "command": "npx",
      "args": [
        "-y",
        "@upstash/context7-mcp",
        "--api-key",
        "${CONTEXT7_API_KEY}"
      ]
    }
  }
}
```

## Streamable HTTP example

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

`type` may be `http`, `streamable-http`, `streamable_http`, or `streamablehttp`.

Header values and stdio env values can reference environment variables with `${NAME}`. Whale fails startup for that server if the variable is missing.

## Optional fields

- `timeout`: startup and call timeout in seconds. Default: `15`.
- `disabled`: set to `true` to skip a server.
- `disabled_tools`: list of original MCP tool names to hide from Whale. Use the tool name reported by the MCP server, such as `read_file` or `write_file`, not Whale's registered `mcp__server__tool` name.
- `env`: environment variables for stdio servers.
- `headers`: HTTP headers for Streamable HTTP servers.

Example:

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

## Check status

Inside the TUI, run:

```text
/mcp
```

It shows the config path, server count, connection status, tool count, and startup errors.

Example:

```text
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
  http headers: Authorization=*****
  tools: none
  error: mcp server "remote" failed during connect: ... (transport=http url=https://example.com/mcp status=401 Unauthorized)
```

## Smoke test

For a user-path regression with a real model call, run:

```sh
DEEPSEEK_API_KEY=... scripts/smoke/mcp_tools.sh
```

The smoke uses a temporary `WHALE_HOME`, probes `/mcp` through the TUI, verifies
secret-like MCP command arguments are redacted, and runs `whale exec` to confirm
the model can call the configured filesystem MCP tool.

## Common issues

If only one server appears, make sure every server is inside `mcpServers`:

```json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    },
    "context7": {
      "command": "npx",
      "args": ["-y", "@upstash/context7-mcp"]
    }
  }
}
```

Do not put a server at the top level next to `mcpServers`; Whale will not load it.

For HTTP servers:

- `401` or `403`: check `headers`, API keys, and environment variables.
- `404`: check that the URL is the MCP endpoint, not a product homepage or API root.
- `429` or `5xx`: check provider rate limits or service status.

Whale omits query strings from HTTP startup errors, but config files can still contain secrets. Do not commit `~/.whale/mcp.json`.
