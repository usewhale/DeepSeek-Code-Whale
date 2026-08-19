# Whale

<p align="center">
  <img src="docs/logo.svg" alt="Whale — 面向 DeepSeek 的 AI 编程 Agent，适配任何环境" width="640">
</p>

<p align="center">
  <strong>简体中文</strong> · <a href="./README.md">English</a>
</p>

<p align="center">
  <a href="https://github.com/usewhale/Whale/releases"><img src="https://img.shields.io/github/v/release/usewhale/Whale?label=release" alt="release"></a>
  <a href="https://www.npmjs.com/package/@usewhale/whale"><img src="https://img.shields.io/npm/v/@usewhale/whale" alt="npm"></a>
  <a href="https://github.com/usewhale/Whale/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/usewhale/Whale/ci.yml?label=CI" alt="CI"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/usewhale/Whale" alt="license"></a>
  <a href="https://github.com/usewhale/Whale/stargazers"><img src="https://img.shields.io/github/stars/usewhale/Whale?style=flat&logo=github&label=stars" alt="GitHub stars"></a>
  <img src="https://img.shields.io/badge/prompt%20cache-98%25-brightgreen" alt="98% prompt cache hit">
</p>

<p align="center">
  Blazingly fast · ~98% prompt cache hit · Zero bloat
</p>

<p align="center">
  <b>Whale — 面向 DeepSeek 的 AI 编程 Agent，适配任何环境。</b><br>
  超长上下文、工具调用、可编程工作流——<br>
  从终端出发，向桌面及更多场景延伸。
</p>

---

## 🚀 快速开始

任意平台：

```bash
npm install -g @usewhale/whale
```

macOS：

```bash
brew install usewhale/tap/whale
```

Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/usewhale/Whale/main/scripts/install.sh | sh
```

Windows PowerShell：

需要 Windows 10 或 Windows Server 2016 及以上版本。

```powershell
irm https://raw.githubusercontent.com/usewhale/Whale/main/scripts/install.ps1 | iex
```

Windows CMD：

```cmd
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/usewhale/Whale/main/scripts/install.ps1 | iex"
```

```bash
# 配置 DeepSeek API Key
whale setup

# 启动交互式 TUI
whale
```

搞定。输入你的问题，Whale 就开始工作——读取文件、执行命令、编辑代码、搜索网页。

> 需要其他模型、代理或自定义配置？见 [配置文档](docs/configuration.md)。

---

## ✨ 一览

| 特性 | 为什么重要 |
|---|---|
| 💰 **~98% prompt cache hit** | Whale 激进地复用缓存上下文——大多数 prompt 命中缓存，每次会话成本低至几美分。DeepSeek 定价 × Whale 缓存 = 可规模化的 AI 辅助编码。 |
| 🐋 **DeepSeek 原生** | 针对 DeepSeek 的 1M 长上下文、工具调用和成本优势深度优化，不做通用多模型外壳 |
| 🔁 **动态 Workflow** | 用 JavaScript 编排多个子 agent——扇出研究、多视角审查、流水线处理。兼容 Claude Code。 |
| 🔌 **MCP** | 接入 1,000+ MCP 服务器，扩展工具能力——文件操作、Shell、Git、网络等 |
| 🧩 **Skills + 插件** | 安装社区技能（代码审查、git 工作流等）或自己编写 |

---

## 🔁 动态 Workflow

Whale 的 **Dynamic Workflow** 让你用 JavaScript 脚本编排多 agent：

```js
// .whale/workflows/research.js
const results = await parallel([
  () => agent("搜索 Go 错误处理的最佳实践"),
  () => agent("找出 Go 错误处理的常见坑"),
]);
return agent("综合两边的发现，写一份简洁指南");
```

**扇出式研究 · 多视角审查 · 流水线处理 · 对抗性验证**

> ✅ **兼容 Claude Code** — 为 Claude Code 编写的 workflow 脚本无需修改即可在 Whale 中运行。

> **⚠️ 默认关闭** — 在 TUI 中运行 `/config` 开启 `Dynamic workflows`，或在 `.whale/config.local.toml` 中添加 `[workflows] enabled = true`。

了解更多：[Workflow 概览](docs/workflows.md) · [自定义 Workflow 指南](docs/custom-workflows.md)

---

## 🧰 MCP、Skills 与插件

| 扩展 | 作用 | 开始使用 |
|---|---|---|
| **MCP 服务器** | 接入 1,000+ 工具（数据库、API、浏览器自动化） | [docs/mcp.md](docs/mcp.md) |
| **Skills 技能** | 加载领域知识——代码审查、git-worktree 等 | [docs/skills.md](docs/skills.md) |
| **Subagents 子智能体** | 定义 reviewer、researcher 等专注角色 | [docs/agents.md](docs/agents.md) |
| **插件** | 扩展 Whale 运行时 | [docs/plugins.md](docs/plugins.md) |
| **Hooks** | 在生命周期事件上运行脚本 | [docs/hooks.md](docs/hooks.md) |

---

## 📸 运行模式

| 界面 | 适用场景 |
|---|---|
| **`whale`**（TUI） | 交互式编程——对话、审查、迭代，全程上下文不断 |
| **`whale ask "..."`**（CLI） | 一次性提问、快速代码审查、单条命令 |
| **`whale --headless`** | CI/CD、自动化 PR 审查、定时任务 |

---

## 🎯 非目标

- **通用多模型外壳。** Whale 是 DeepSeek-first，优先把 DeepSeek 的缓存、工具和定价优势做好。
- **IDE。** Whale 不是 IDE——它是与你一起编码的 Agent：无论是在终端、桌面还是 CI。

## 📦 项目状态

Whale 仍在快速迭代中。建议先用于个人项目、实验仓库或可回滚的开发流程。

> **免责声明：** 本项目与 DeepSeek Inc. 无关，系独立开源社区项目。

---

## 🤝 参与贡献

关于克隆、开发、测试、issues 和 pull requests，请查看 [CONTRIBUTING.md](CONTRIBUTING.md)。

当前开发方向和可认领任务见 [ROADMAP.md](ROADMAP.md)。

安全相关问题请查看 [SECURITY.md](SECURITY.md)。

---

## Star History（Star 历史）

<a href="https://star-history.dera.page/#usewhale/Whale&type=date">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://star-history.dera.page/svg?repos=usewhale/Whale&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://star-history.dera.page/svg?repos=usewhale/Whale&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://star-history.dera.page/svg?repos=usewhale/Whale&type=date&legend=top-left" />
 </picture>
</a>

---

## 🤝 贡献

欢迎参与 Whale 开发！阅读[贡献指南](docs/contributing.zh.md)了解如何搭建环境、运行测试和提交 PR。

## 🙏 致谢

Whale 站在巨人的肩膀上：

- [Charmbracelet](https://charm.sh) — Bubble Tea、Lip Gloss、Glamour 及整个 TUI 生态
- [fastschema/qjs](https://github.com/fastschema/qjs) — QuickJS Go 绑定，Workflow 脚本的运行时
- [spf13/cobra](https://github.com/spf13/cobra) — CLI 框架
- [alecthomas/chroma](https://github.com/alecthomas/chroma) — 语法高亮
- [yuin/goldmark](https://github.com/yuin/goldmark) — Markdown 解析
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) — MCP 协议支持
- [tetratelabs/wazero](https://github.com/tetratelabs/wazero) — 纯 Go WebAssembly 运行时

以及所有我们依赖的开源库——感谢你们。
