# Configuration

## 🚀 Quick Setup

The fastest way to get started:

```bash
whale setup
```

This saves your DeepSeek API key to `~/.whale/credentials.json`.

You can also use the environment variable (takes precedence):

```bash
DEEPSEEK_API_KEY=sk-... whale
```

Run `whale doctor` anytime to confirm your current setup.

---

## Common Tasks

### Use a different model / endpoint

```toml
# .whale/config.toml (project) or ~/.whale/config.toml (global)
[model]
provider = "openai-compatible"
model = "deepseek-chat"
base_url = "https://api.deepseek.com/v1"
```

Whale is DeepSeek-native, but you can point it at any OpenAI-compatible endpoint.
Other models may not support all features (tool calling, long context).

For third-party providers such as Alibaba Cloud Bailian, OpenCode Go, and OpenCode Zen, see the [Provider Configuration Guide](providers.en.md).

### Set up a proxy

```toml
[model]
http_proxy = "http://127.0.0.1:7890"
https_proxy = "http://127.0.0.1:7890"
```

Whale respects `$HTTP_PROXY` and `$HTTPS_PROXY` environment variables too.

### Customize the system prompt

```toml
[settings]
prompt = "You are a coding assistant that prefers Rust over Go."
```

The prompt is injected at the start of every new session.

### Project-level settings

```toml
# .whale/config.toml — share with your team via git
[model]
model = "deepseek-chat"
```

```toml
# .whale/config.local.toml — personal overrides, do not commit
[model]
model = "deepseek-reasoner"
```

Config files are merged: `defaults < global < project shared < project local < CLI flags/env`

### Disable specific tools

```toml
[disabled_tools]
tools = ["web_search", "web_fetch"]
```

### Raise foreground shell wait limits

```toml
[shell]
foreground_wait_default_ms = 15000
foreground_wait_max_ms = 120000 # hard ceiling: 1800000 (30 minutes)
```

Foreground `shell_run` waits can be increased for long TUI turns, subagents, and workflow-spawned agents. Background shell task limits are unchanged and remain capped at 30 minutes.

### Add Hooks

Need to run scripts when a session starts, when the user submits a prompt, before or after tools run, or before Whale ends a turn? See [Hooks](hooks.en.md).

### Experimental Features

```toml
[experimental]
deepseek_prefix_completion = true
```

Enables DeepSeek Beta Prefix completion. Whale only uses it for explicit no-tool, strongly formatted text requests, such as internal hook prompts that must return JSON. This is a format-stability feature, not a promised token-saving feature.

### Multimodal attachment harness

DeepSeek multimodal API access may not be available on every account yet. To test image, PDF, file, or audio attachments with an OpenAI-compatible multimodal endpoint, configure a multimodal override:

```toml
[providers.deepseek.multimodal]
enabled = true
compat = "openai"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
model = "gpt-4o"
```

This override is used only for turns that include attachments, such as `whale exec --attach screen.png "describe this"` or TUI prompts submitted after pasting an image or local image path. Normal text-only turns keep using the regular DeepSeek configuration. When DeepSeek multimodal becomes publicly available, point `base_url`, `api_key_env`, and `model` back to the DeepSeek-compatible values.

### Server-side web search (Responses API)

DeepSeek's Responses API includes a server-executed web search: no third-party search key is needed, search runs on DeepSeek's servers and the results are fed directly to the model. Only the **`deepseek-v4-flash`** model currently supports it.

```toml
# Default (no config needed): deepseek-v4-flash uses the server-side search.
[providers.deepseek]
model = "deepseek-v4-flash"
# web_search = "auto"   # auto (default) | local | server
```

- `auto` (**default**, the behavior when unset): uses `server` for `deepseek-v4-flash`, `local` for everything else.
- `local`: current behavior. `web_search` is Whale's local tool (DuckDuckGo with Bing fallback), executed by the tool system.
- `server`: forces the DeepSeek Responses API and translates the local `web_search` tool into the server-side built-in search. The model answers in the same response turn with search results already incorporated; no local tool dispatch happens. If the model is not `deepseek-v4-flash`, it degrades silently to `local` behavior.

Notes:

- Server-side search parameters are not controllable (`search_context_size`, `user_location` are ignored); search timing is decided server-side via `tool_choice: auto`.
- Search context (`web_search_call` items) is kept in process memory; after a restart, resumed sessions re-run the search (still correct, just one extra search).
- In `server` mode with thinking disabled, the request sends `reasoning.effort = "none"` explicitly (the Responses API enables thinking by default).
- Turns with attachments still use the multimodal channel and are unaffected by `web_search = "server"`.

---

## Reference

### Config file locations

| Path | Scope | Commit? |
|---|---|---|
| `~/.whale/config.toml` | Global — all projects | No |
| `.whale/config.toml` | Project — shared with team | Yes |
| `.whale/config.local.toml` | Project — personal overrides | No |

On Windows, the default global directory is `%USERPROFILE%\\.whale`.
Set `WHALE_HOME` to use a custom directory on any platform.

### All settings (`config.toml`)

```toml
[model]
provider = "deepseek"                  # or "openai-compatible"
model = "deepseek-chat"                # or "deepseek-reasoner"
base_url = "https://api.deepseek.com/v1"
http_proxy = ""                        # proxy for API calls
https_proxy = ""

[settings]
prompt = ""                            # custom system prompt prefix
max_tokens = 4096                      # max response tokens

[permissions]
allowed_directories = []               # restrict file access to these dirs

[permissions.web_search]
"*" = "allow"                          # default is no approval; set to "ask" to confirm each search

[permissions.web_fetch]
"*" = "allow"                          # use "host:example.com" to configure a specific host

[permissions.mcp]
fs = "allow"                           # "allow" | "ask" | "deny" per MCP server

[disabled_tools]
tools = []                             # hide built-in tools by name

[mcp]
config_path = ""                       # custom MCP config path

[shell]
foreground_wait_default_ms = 15000     # default foreground shell_run wait
foreground_wait_max_ms = 120000        # max foreground shell_run wait; hard ceiling is 1800000

	[workflows]
	enabled = false                        # enable the workflow runtime/tool
	keyword_trigger_enabled = true         # allow workflow catalog hints to trigger automatic use
	max_concurrency = 3                    # parallel agent limit

[skills]
disabled = []                          # skills to hide
enabled = []                           # force-enable even if project disables

[plugins.memory]
enabled = true                         # configure each plugin by id

[experimental]
deepseek_prefix_completion = false     # DeepSeek Prefix completion (experimental)

[providers.deepseek.multimodal]
enabled = false                        # Route attachment turns through an OpenAI-compatible multimodal endpoint
compat = "openai"
base_url = ""
api_key_env = ""
model = ""

[logging]
level = "info"                         # debug | info | warn | error
```

### Environment variables

| Variable | Overrides |
|---|---|
| `DEEPSEEK_API_KEY` | Credential in `~/.whale/credentials.json` |
| `WHALE_HOME` | Global data directory (`~/.whale`) |
| `HTTP_PROXY` / `HTTPS_PROXY` | Proxy settings in config |
| `WHALE_MCP_CONFIG` | MCP config file path |

### Worktrees

Whale supports git worktrees for isolated feature development:

```bash
whale --worktree
whale exec --worktree
```

On exit, Whale removes a clean worktree automatically. Uncommitted changes
prompt you to keep or remove.

---

## Where is local state stored?

```
~/.whale/
├── credentials.json    # API keys
├── config.toml         # global config
├── mcp.json            # MCP server config
├── sessions/           # session history
└── usage/         # usage logs
```

Do not commit these files.

---

## Need help?

```bash
whale doctor     # check your setup
whale --help     # CLI reference
```
