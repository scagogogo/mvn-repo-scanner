# Claude Code 接入指南

[Claude Code](https://www.anthropic.com/claude-code) 是 Anthropic 官方的命令行 AI 编码助手，能在你的终端里直接执行 shell 命令、读写文件。它是接入 `mvn-repo-scanner` 最顺滑的方式——AI 可以自己编译工具、运行扫描、读取结果文件并解读。

## 前置条件

1. 已安装 [Claude Code](https://docs.anthropic.com/en/docs/claude-code/overview) CLI 并完成登录
2. 终端里能运行 `go`（Go 1.25+）

::: tip 不确定是否装了 Go？
在提示词里就让它先检查，AI 会用 `go version` 确认并指导你安装。
:::

## 一键复制提示词

在 Claude Code 的交互框中粘贴以下内容，回车即可：

```text
我想使用 mvn-repo-scanner 扫描 Maven 仓库中的敏感内容泄露。
项目地址：https://github.com/scagogogo/mvn-repo-scanner

请作为我的扫描助手分步引导我，每步先说明意图再执行，关键决策点给我选项和推荐，全程简体中文：

1. 运行 `go version` 确认 Go 1.25+，缺失则指导安装（可用 GOPROXY=https://goproxy.cn,direct 加速）。
2. clone 仓库到当前目录，`go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner` 编译，`./mvn-repo-scanner version` 验证。
3. 询问我：扫描目标仓库（中央仓库/私服URL）、groupId 过滤、规则集（core/extended/all，默认 core）、是否需要私服认证、是否周期扫描。
4. 先用 `./mvn-repo-scanner scan --repo https://repo.maven.apache.org/maven2 --group javax.inject --rules-level core -v` 小范围试扫，解释 Discovered/Scanned/Failed/Findings。
5. 按我的需求组装正式命令，大型仓库启用 --state-file/--checkpoint-interval/--concurrency，说明 --resume 和 --retry-failed。提醒我可按 Ctrl+C 安全中断。
6. 扫描结束解读 Findings（规则、严重级别、文件、匹配内容、处置建议）。若生成了 report.json 帮我解析汇总。
7. 若需周期扫描，用 --scan-interval --task-id 注册，演示 task list/run/pause/resume。

遇错主动排查修复。
```

## 预期交互过程

粘贴后，Claude Code 大致会这样推进：

```text
Claude: 我先检查你的 Go 环境。
        $ go version
        go version go1.25.0 linux/amd64  ✓

Claude: Go 已就绪。接下来克隆并编译工具。
        $ git clone https://github.com/scagogogo/mvn-repo-scanner
        $ cd mvn-repo-scanner
        $ go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner
        $ ./mvn-repo-scanner version
        mvn-repo-scanner v0.1.0 ✓

Claude: 工具就绪。请回答几个问题：
        a) 扫描哪个仓库？ [1]Maven Central [2]私服
        b) groupId 过滤范围？(建议先小范围)
        c) 规则集？ [1]core [2]extended [3]all
        ...

你:    1, com.example, core

Claude: 先小范围试扫验证...
        $ ./mvn-repo-scanner scan --repo ... --group com.example -v
        ...
```

## 进阶：让 Claude Code 自动化全流程

如果你已经有明确需求，可以用更精简的"一次到位"提示词：

```text
帮我用 mvn-repo-scanner 扫描 Maven Central 的 org.slf4j 这个 groupId，
规则集用 extended，并发 5，状态保存到 .slf4j-state.json，结果输出 JSON 到 slf4j-report.json。
编译工具、执行扫描、扫描完解读所有 Findings。中途如失败自动重试或 resume。
```

## 排查：Claude Code 执行命令被拦截

Claude Code 默认会在执行某些命令前请求你确认。如果你想让它更自主（例如全流程无人值守），可以：

- 在它请求确认时选择 "Yes, and don't ask again for commands like this"
- 或启动时使用更宽松的权限模式（参见 Claude Code 文档）

::: warning 安全提醒
让 AI 自主执行扫描命令是安全的（只读扫描），但若涉及 `--auth-password` 传入私服密码，建议你手动执行那一条命令，避免密码留在 AI 对话历史中。
:::
