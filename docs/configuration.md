# 配置

## 🚀 快速配置

最快的方式：

```bash
whale setup
```

这会把你的 DeepSeek API Key 保存到 `~/.whale/credentials.json`。

也可以用环境变量（优先级更高）：

```bash
DEEPSEEK_API_KEY=sk-... whale
```

任何时候想确认当前配置，运行 `whale doctor`。

---

## 常见操作

### 使用其他模型或 endpoint

```toml
# .whale/config.toml（项目级）或 ~/.whale/config.toml（全局）
[model]
provider = "openai-compatible"
model = "deepseek-chat"
base_url = "https://api.deepseek.com/v1"
```

Whale 是 DeepSeek 原生的，但可以指向任何兼容 OpenAI 的 endpoint。
其他模型可能不支持全部功能（工具调用、长上下文）。

常见第三方 provider 的配置示例见 [Provider 配置指南](providers.md)，包括阿里云百炼、OpenCode Go/Zen 等。

### 设置代理

```toml
[model]
http_proxy = "http://127.0.0.1:7890"
https_proxy = "http://127.0.0.1:7890"
```

Whale 也支持 `$HTTP_PROXY` 和 `$HTTPS_PROXY` 环境变量。

### 自定义系统提示词

```toml
[settings]
prompt = "你是一个偏爱 Rust 而非 Go 的编程助手。"
```

这个提示词会在每个新会话的开头注入。

### 项目级配置

```toml
# .whale/config.toml — 可以提交到 git，团队共享
[model]
model = "deepseek-chat"
```

```toml
# .whale/config.local.toml — 个人覆盖，不要提交
[model]
model = "deepseek-reasoner"
```

配置文件合并顺序：`默认值 < 全局 < 项目共享 < 项目本地 < CLI 标志/环境变量`

### 禁用特定工具

```toml
[disabled_tools]
tools = ["web_search", "web_fetch"]
```

### 提高前台 shell 等待时间

```toml
[shell]
foreground_wait_default_ms = 15000
foreground_wait_max_ms = 120000 # 最大可设为 1800000（30 分钟）
```

可以为 TUI、subagent 和 workflow 启动的 agent 提高前台 `shell_run` 等待时间。后台 shell task 的行为不变，仍然最多运行 30 分钟。

### 接入 Hooks

需要在会话开始、用户提交 prompt、工具执行前后或结束前运行脚本？见 [Hooks 文档](hooks.md)。

### 实验功能

```toml
[experimental]
deepseek_prefix_completion = true
```

启用 DeepSeek Beta 的 Prefix completion。Whale 只会在明确适合的无工具、强格式文本请求中使用它，例如需要模型直接返回 JSON 的内部 hook prompt。这个功能主要提升格式稳定性，不承诺节省 token。

### 多模态附件 harness

DeepSeek 多模态 API 可能还不是所有账号都可用。你可以先用 OpenAI-compatible 的多模态 endpoint 测试图片、PDF、文件或音频附件：

```toml
[providers.deepseek.multimodal]
enabled = true
compat = "openai"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
model = "gpt-4o"
```

这个 override 只用于带附件的 turn，例如 `whale exec --attach screen.png "describe this"`，或者在 TUI 里粘贴图片/本地图片路径后提交的 prompt。普通纯文本 turn 仍使用常规 DeepSeek 配置。DeepSeek 多模态公开可用后，把 `base_url`、`api_key_env` 和 `model` 指回 DeepSeek 兼容值即可。

### 联网搜索 —— 搜索在哪里执行

`web_search` 设置决定搜索**在哪里执行**：在 Whale 的工具系统内（`local`），还是在 DeepSeek 服务器上（`server`）。它**不**选择 API 传输通道 —— 那是独立的 `api` 开关（见下一节）。`server` 搜索是 DeepSeek Responses API 的一部分：不需要第三方搜索密钥，搜索在 DeepSeek 服务器上完成并直接喂给模型。目前**仅 `deepseek-v4-flash` 模型**支持服务端搜索。

```toml
# 默认（不配置时）deepseek-v4-flash 就使用服务端内置搜索：
[providers.deepseek]
model = "deepseek-v4-flash"
# web_search = "auto"   # auto(默认) | local | server
```

- `auto`（**默认**，不配置即此行为）：模型是 `deepseek-v4-flash` 时走 `server`，其他模型走 `local`。
- `local`：搜索在 Whale 工具系统内执行 —— 本地 `web_search` 工具（DuckDuckGo + Bing 回退），由工具系统执行。
- `server`：搜索在 DeepSeek 服务器上通过 Responses API 执行，结果在同一轮响应里直接并入模型回答；不做本地工具分发。需要 Responses API 传输通道：如果传输通道被显式设为 `chat_completions`，则 `server` 降级为 `local` 并给出警告 —— 绝不会拒绝启动。

注意事项：

- 服务端搜索无法控制搜索参数（`search_context_size`、`user_location` 会被忽略），搜索时机由服务端 `tool_choice: auto` 决定。
- 搜索上下文（`web_search_call` item）保存在进程内，重启后会话恢复时会重新搜索（结果仍然正确，只是多花一次搜索）。
- `server` 模式下思考关闭时请求会显式传 `reasoning.effort = "none"`（Responses API 默认开启思考）。
- 带附件的 turn 仍然走多模态通道，不受 `web_search = "server"` 影响。

### API 传输通道 —— Responses API 与 chat completions

Whale 通过两种传输通道之一与 DeepSeek 通信：**Responses API**（`POST /responses`）或 **chat completions API**（`POST /chat/completions`）。`api` 设置显式选择传输通道；模型仍然决定负载格式，两者解耦。`api` 是传输通道的权威 —— `web_search = "server"` 充其量是"正在使用 Responses API"的一种推断，因此它可以推导出 Responses API，但绝不能覆盖显式的 `chat_completions` 选择。

```toml
[providers.deepseek]
api = "auto"   # auto(默认) | responses | chat_completions
```

- `auto`（**默认**，不配置即此行为）：传输通道由模型推断 —— `deepseek-v4-flash` 使用 Responses API，`web_search = "server"` 也推导为 Responses API；其他情况使用 chat completions。
- `responses`：任何模型都使用 Responses API。
- `chat_completions`：任何模型都使用 chat completions。如果同时设置了 `web_search = "server"`，冲突通过把 `web_search` 降级为 `local` 并给出警告解决 —— 绝不会拒绝启动。

同样的选择也可以通过 `WHALE_API` 环境变量设置（`responses` | `chat_completions` | `auto`）；环境变量优先于配置文件。ACP 入口（`whale-acp`，Zed 运行的 agent）直接读取 `WHALE_API` —— 它不读取 `config.toml` 的 provider 设置。

---

## 参考

### 配置文件路径

| 路径 | 范围 | 是否提交？ |
|---|---|---|
| `~/.whale/config.toml` | 全局 — 所有项目 | 否 |
| `.whale/config.toml` | 项目 — 团队共享 | 是 |
| `.whale/config.local.toml` | 项目 — 个人覆盖 | 否 |

Windows 上默认全局目录是 `%USERPROFILE%\\.whale`。
设置 `WHALE_HOME` 可自定义目录。

### 所有配置项（`config.toml`）

```toml
[model]
provider = "deepseek"                  # 或 "openai-compatible"
model = "deepseek-chat"                # 或 "deepseek-reasoner"
base_url = "https://api.deepseek.com/v1"
http_proxy = ""                        # API 调用代理
https_proxy = ""

[settings]
prompt = ""                            # 自定义系统提示词前缀
max_tokens = 4096                      # 最大响应 token 数

[permissions]
allowed_directories = []               # 限制文件访问目录

[permissions.web_search]
"*" = "allow"                          # 默认不审批；改为 "ask" 可恢复每次确认

[permissions.web_fetch]
"*" = "allow"                          # 可用 "host:example.com" 单独配置域名

[permissions.mcp]
fs = "allow"                           # "allow" | "ask" | "deny" 按 MCP 服务器设置

[disabled_tools]
tools = []                             # 按名称隐藏内置工具

[mcp]
config_path = ""                       # 自定义 MCP 配置路径

[shell]
foreground_wait_default_ms = 15000     # 前台 shell_run 默认等待时间
foreground_wait_max_ms = 120000        # 前台 shell_run 最大等待时间；硬上限为 1800000

	[workflows]
	enabled = false                        # 是否启用 workflow runtime/tool
	keyword_trigger_enabled = true         # 是否允许 workflow 目录提示触发自动使用
	max_concurrency = 3                    # 并行 agent 数

[skills]
disabled = []                          # 禁用的技能
enabled = []                           # 强制启用的技能

[plugins.memory]
enabled = true                         # 每个插件单独配置启用状态

[experimental]
deepseek_prefix_completion = false     # DeepSeek Prefix completion（实验功能）

[providers.deepseek]
web_search = "auto"                    # 搜索在哪里执行：local | server | auto
api = "auto"                           # API 传输通道：responses | chat_completions | auto

[providers.deepseek.multimodal]
enabled = false                        # 将带附件的 turn 路由到 OpenAI-compatible 多模态 endpoint
compat = "openai"
base_url = ""
api_key_env = ""
model = ""

[logging]
level = "info"                         # debug | info | warn | error
```

### 环境变量

| 变量 | 覆盖内容 |
|---|---|
| `DEEPSEEK_API_KEY` | `~/.whale/credentials.json` 中的凭据 |
| `WHALE_HOME` | 全局数据目录（`~/.whale`） |
| `HTTP_PROXY` / `HTTPS_PROXY` | 配置中的代理设置 |
| `WHALE_MCP_CONFIG` | MCP 配置文件路径 |
| `WHALE_API` | API 传输通道（`responses` \| `chat_completions` \| `auto`）；环境变量优先于配置文件 |

以下变量适用于 ACP 入口（`whale-acp`，Zed 运行的 agent）。它们在进程启动时读取一次 —— 修改后需要重启 agent 进程：

| 变量 | 覆盖内容 |
|---|---|
| `WHALE_MODEL` | 模型名（校验必须属于受支持集合；默认 `deepseek-v4-flash`） |
| `WHALE_API` | API 传输通道（`responses` \| `chat_completions` \| `auto`） |
| `WHALE_COMPACT_THRESHOLD` | 自动压缩触发阈值（上下文窗口的比例，例如 `0.85`） |
| `WHALE_CONTEXT_WINDOW` | 上下文窗口覆盖（token 数；默认由模型推导，V4 模型为 1M） |
| `WHALE_MAX_TOOL_ITERS` | 每轮工具迭代上限（默认 `300`） |

自动压缩按 token 数计算，而不是按成本：大量命中的缓存前缀成本几乎为零，但仍然占用窗口，因此大型缓存会话与未缓存会话一样会在越界时被压缩。

### 工作目录（Worktree）

Whale 支持 git worktree 进行隔离开发：

```bash
whale --worktree
whale exec --worktree
```

退出时，Whale 会自动清理干净的 worktree。有未提交的改动时会询问是否保留。

---

## 本地状态存在哪里？

```
~/.whale/
├── credentials.json    # API key
├── config.toml         # 全局配置
├── mcp.json            # MCP 服务器配置
├── sessions/           # 会话历史
└── usage/         # 使用量日志
```

不要提交这些文件。

---

## 需要帮助？

```bash
whale doctor     # 检查当前配置
whale --help     # CLI 参考
```
