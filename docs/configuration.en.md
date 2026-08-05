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

### Web search — where search runs

The `web_search` setting decides **where** search runs: inside Whale's tool system (`local`) or on DeepSeek's servers (`server`). It does **not** select the API transport — that is a separate knob (`api`, next section). `server` search is part of DeepSeek's Responses API: no third-party search key is needed, search runs on DeepSeek's servers and the results are fed directly to the model. Only the **`deepseek-v4-flash`** model currently supports server-side search.

```toml
# Default (no config needed): deepseek-v4-flash uses the server-side search.
[providers.deepseek]
model = "deepseek-v4-flash"
# web_search = "auto"   # auto (default) | local | server
```

- `auto` (**default**, the behavior when unset): uses `server` for `deepseek-v4-flash`, `local` for everything else.
- `local`: search runs inside Whale's tool system — the local `web_search` tool (DuckDuckGo with Bing fallback), executed by the tool system.
- `server`: search runs on DeepSeek's servers via the Responses API and the results are incorporated into the model's answer in the same response turn; no local tool dispatch happens. Requires the Responses API transport: if the transport is explicitly `chat_completions`, `server` degrades to `local` with a warning — never by refusing to start.

Notes:

- Server-side search parameters are not controllable (`search_context_size`, `user_location` are ignored); search timing is decided server-side via `tool_choice: auto`.
- Search context (`web_search_call` items) is kept in process memory; after a restart, resumed sessions re-run the search (still correct, just one extra search).
- In `server` mode with thinking disabled, the request sends `reasoning.effort = "none"` explicitly (the Responses API enables thinking by default).
- Turns with attachments still use the multimodal channel and are unaffected by `web_search = "server"`.

### API transport — Responses API vs chat completions

Whale speaks to DeepSeek over one of two transports: the **Responses API** (`POST /responses`) or the **chat completions API** (`POST /chat/completions`). The `api` setting selects the transport explicitly; the model still picks the payload shape, so the two are decoupled. The `api` knob is the transport authority — `web_search = "server"` is at best an *inference* that the Responses API is in use, so it can imply the Responses API but never overrides an explicit `chat_completions` choice.

```toml
[providers.deepseek]
api = "auto"   # auto (default) | responses | chat_completions
```

- `auto` (**default**, the behavior when unset): the transport is inferred — `deepseek-v4-flash` uses the Responses API, and `web_search = "server"` also implies the Responses API; everything else uses chat completions.
- `responses`: always speaks the Responses API, for any model. `web_search` keeps its meaning: with `web_search = "local"`, search still runs in Whale's tool system on the Responses transport; with `server`/`auto`, DeepSeek's built-in search is used.
- `chat_completions`: always speaks chat completions, for any model. If `web_search = "server"` is also set, the conflict is resolved by degrading `web_search` to `local` with a warning — never by refusing to start.

The same selection is available as the `WHALE_API` environment variable (`responses` | `chat_completions` | `auto`); the env var wins over the config value. The ACP entrypoint (`whale-acp`, the agent Zed runs) reads `WHALE_API` directly — it does not read `config.toml` provider settings.

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

[providers.deepseek]
web_search = "auto"                    # where search runs: local | server | auto
api = "auto"                           # API transport: responses | chat_completions | auto

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
| `WHALE_API` | API transport (`responses` \| `chat_completions` \| `auto`); env wins over the config value |

The following variables apply to the ACP entrypoint (`whale-acp`, the agent Zed runs). They are read once at process start — changing them requires restarting the agent process:

| Variable | Overrides |
|---|---|
| `WHALE_MODEL` | Model name (validated against the supported set; default `deepseek-v4-flash`) |
| `WHALE_API` | API transport (`responses` \| `chat_completions` \| `auto`) |
| `WHALE_COMPACT_THRESHOLD` | Auto-compaction trigger as a fraction of the context window, e.g. `0.85` |
| `WHALE_CONTEXT_WINDOW` | Context-window override in tokens (default derived from the model: 1M for V4 models) |
| `WHALE_MAX_TOOL_ITERS` | Per-turn tool-iteration cap (default `300`) |

Auto-compaction counts tokens, not cost: a heavily cached prompt prefix costs almost nothing yet still counts against the window, so a large cached session is compacted exactly like an uncached one.

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
