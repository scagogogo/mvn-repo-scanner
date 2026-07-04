# AI Agent 接入

`mvn-repo-scanner` 是一个命令行工具，但你可以**完全不读文档**——把下面的提示词复制给你的 AI Agent，它会自动引导你完成安装、配置、扫描和结果解读。

## 为什么用 AI 接入？

这个工具功能较多（断点续扫、私有仓库认证、定时任务、37 条规则），命令行参数有 20+ 个。与其逐页读文档，不如让 AI 根据你的实际场景**动态组装命令、解释输出、排查报错**。

支持两类主流 AI 编码 Agent：

| Agent | 适用场景 | 特点 |
|-------|---------|------|
| [Claude Code](./claude-code) | Anthropic 官方 CLI，能在终端直接执行命令 | 可自动跑 `go build` / `scan`，全程无需你手动复制命令 |
| [Codex](./codex) | OpenAI 的编码 Agent | 同样能执行 shell 命令并解读输出 |

## 通用提示词（推荐）

下面这段提示词对 Claude Code 和 Codex 都适用。**全选复制，粘贴到你的 Agent 对话框，回车**，然后回答它的几个问题即可。

::: tip 一键复制
点击代码块右上角的复制按钮，粘贴到 Agent 中。
:::

```text
我想使用 mvn-repo-scanner 这个工具扫描 Maven 仓库中的敏感内容泄露（密码、密钥、证书等）。
项目地址：https://github.com/scagogogo/mvn-repo-scanner

请作为我的 Maven 仓库安全扫描助手，按以下步骤引导我完成，每一步都先解释清楚再执行，
遇到需要我做决策的地方给出选项和你的推荐，全程用简体中文回复：

【步骤 1 环境检查】
运行 `go version` 确认已安装 Go 1.25+。若未安装，根据我的操作系统指导安装方式。

【步骤 2 安装工具】
将仓库 clone 到本地，执行 `go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner` 编译，
运行 `./mvn-repo-scanner version` 确认二进制可用。

【步骤 3 了解我的需求】
依次询问我以下信息（每项给我选项和默认推荐）：
  a) 要扫描哪个仓库？中央仓库(repo.maven.apache.org) / 私服 URL
  b) 是否按 groupId 过滤？如 com.example / org.apache 等
  c) 规则集档位？core(6条核心) / extended(30+条) / all(37条)，默认 core
  d) 若是私服，认证方式？Basic Auth(用户名+密码) / Bearer Token
  e) 是否需要定时周期扫描？

【步骤 4 小范围试扫】
先用一个很小的 groupId 做试扫验证可用性，例如：
  ./mvn-repo-scanner scan --repo https://repo.maven.apache.org/maven2 \
    --group javax.inject --rules-level core -v
向我解释输出中 Discovered / Scanned / Failed / Findings 各项含义。

【步骤 5 正式扫描】
根据步骤 3 的回答组装正式扫描命令。对大型仓库提醒我：
  - 用 --state-file 指定状态文件保存进度
  - 用 --checkpoint-interval 控制存盘频率（默认 50）
  - 用 --concurrency 控制并发（默认 10）
  - 用 --resume 在中断后继续，--retry-failed 重试失败项
说明如何在扫描中按 Ctrl+C 安全中断（状态会自动保存）。

【步骤 6 结果解读】
扫描结束后，若 Findings > 0，逐条解读每条发现：
  - 命中的规则 ID 与名称、严重级别(CRITICAL/HIGH/MEDIUM/LOW)
  - 所在 artifact 与文件路径、匹配的具体内容
  - 处置建议（轮换密钥、删除文件、改为环境变量注入等）
若输出为 JSON（--output json --output-file report.json），帮我解析并汇总。

【步骤 7 定时任务（可选）】
若步骤 3 选择周期扫描，演示：
  - 用 --scan-interval 1h --task-id my-task 注册任务
  - 用 task list / task show / task pause / task resume / task delete 管理
  - 用 task run 拉起到期任务（配合 cron）

执行过程中遇到任何报错，主动排查根因并修复，不要把报错直接抛给我。
```

## 接入流程示意

```text
你 ──复制提示词──▶ AI Agent
                    │
                    ├─ 检查 Go 环境
                    ├─ 编译 mvn-repo-scanner
                    ├─ 询问你的扫描需求
                    ├─ 小范围试扫验证
                    ├─ 组装正式扫描命令并执行
                    ├─ 中断/恢复（如需要）
                    ├─ 解读 Findings
                    └─ （可选）注册定时任务
你 ◀──结果与建议── AI Agent
```

## 各 Agent 专属指南

两个 Agent 在执行细节上略有差异，点击查看专属页面：

- [Claude Code 接入指南](./claude-code) — 推荐方式，终端原生执行
- [Codex 接入指南](./codex) — OpenAI 编码 Agent

## 常见问题

### AI 执行 `go build` 很慢怎么办？

首次编译需要下载依赖。可以提前运行 `go mod download`，或使用国内代理：
```bash
export GOPROXY=https://goproxy.cn,direct
```

### 扫描 Maven Central 太慢？

Maven Central 体量极大（数百万 artifact）。**务必用 `--group` 限定范围**，只扫你关心的 groupId 前缀。完整扫描中央仓库既无必要也不现实。

### AI 能直接看懂扫描结果吗？

可以。控制台输出和 JSON 报告都是 AI 友好的文本格式。建议正式扫描时同时输出 JSON：
```bash
./mvn-repo-scanner scan ... --output json --output-file report.json
```
AI 能精确解析每条 finding 的字段。

### 不想让 AI 自动执行命令？

把提示词里的"执行"改成"给我命令让我自己执行"即可。AI 会只生成命令文本，由你手动运行。
