# Codex 接入指南

[Codex](https://openai.com/codex/) 是 OpenAI 的编码 Agent，同样能在终端中执行 shell 命令。接入 `mvn-repo-scanner` 的流程与 Claude Code 类似——把提示词交给它，它自行编译、扫描、解读。

## 前置条件

1. 已安装 Codex CLI 并完成登录
2. Go 1.25+（`go version` 可用）

## 一键复制提示词

在 Codex 交互框中粘贴以下内容：

```text
我要用 mvn-repo-scanner 扫描 Maven 仓库的敏感内容泄露。
项目地址：https://github.com/scagogogo/mvn-repo-scanner

作为我的安全扫描助手，分步引导我，每步先说明意图再执行，关键决策点给选项和推荐理由，全程简体中文：

步骤1 环境检查：`go version` 确认 Go 1.25+，缺失则指导安装（GOPROXY=https://goproxy.cn,direct 可加速依赖下载）。
步骤2 安装：clone 仓库，`go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner`，`./mvn-repo-scanner version` 验证。
步骤3 需求确认：询问 a)目标仓库(中央/私服URL) b)groupId过滤 c)规则集(core/extended/all,默认core) d)私服认证方式(Basic/Bearer) e)是否周期扫描。
步骤4 小范围试扫：`./mvn-repo-scanner scan --repo https://repo.maven.apache.org/maven2 --group javax.inject --rules-level core -v`，解释输出字段 Discovered/Scanned/Failed/Findings。
步骤5 正式扫描：按需求组装命令，大型仓库启用 --state-file/--checkpoint-interval/--concurrency，说明 --resume 断点续扫与 --retry-failed。提醒 Ctrl+C 可安全中断。
步骤6 结果解读：Findings 逐条解读(规则/级别/文件/匹配/处置)。若 report.json 存在则解析汇总。
步骤7 周期任务(可选)：--scan-interval --task-id 注册，演示 task list/run/pause/resume/delete。

遇错主动修复。
```

## 与 Claude Code 的差异

两者提示词通用，但有细微差异：

| 维度 | Claude Code | Codex |
|------|-------------|-------|
| 命令执行 | 终端原生，输出直接进对话 | 同样可执行 shell |
| 权限确认 | 默认逐条确认，可放宽 | 取决于 Codex 配置 |
| 推荐场景 | Anthropic 生态用户 | OpenAI 生态用户 |

提示词本身**完全兼容**，你可以两边的通用提示词都用，哪个顺手用哪个。

## 精简版（已有明确需求）

```text
用 mvn-repo-scanner 扫描 Maven Central 的 javax.mail groupId，
规则集 core，并发 3，状态存 mail-state.json，JSON 报告存 mail-report.json。
负责编译、扫描、解读 Findings，失败自动 resume。
```

## 安全提醒

- 涉及私服密码（`--auth-password`）的命令，建议你手动执行，不要让 AI 经手明文密码
- 扫描结果（report.json）可能包含真实泄露的密钥，**不要把 JSON 内容直接粘贴到外部对话**，让 AI 在本地解读即可
