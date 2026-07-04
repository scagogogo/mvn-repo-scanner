# mvn-repo-scanner

[![Go Report Card](https://goreportcard.com/badge/github.com/scagogogo/mvn-repo-scanner)](https://goreportcard.com/report/github.com/scagogogo/mvn-repo-scanner)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![GoDoc](https://godoc.org/github.com/scagogogo/mvn-repo-scanner?status.svg)](https://godoc.org/github.com/scagogogo/mvn-repo-scanner)

Maven 仓库敏感内容扫描器 —— 扫描 jar 包、pom.xml 与配置文件中泄露的密码、API Key、私钥、证书等敏感信息。支持 Maven 中央仓库与企业内部私服（Nexus / Artifactory）。

**[文档站](https://scagogogo.github.io/mvn-repo-scanner/)** | **[English](README.md)**

## 核心特性

- **38 条内置检测规则** —— 覆盖硬编码密码、AWS/GCP/Azure 密钥、GitHub/Slack/Stripe Token、PEM 私钥、JDBC/MongoDB 连接串、Maven settings 密码等。支持 `core`/`extended`/`all` 三档规则集与自定义 YAML。
- **游标断点续扫** —— 基于有序 DFS 遍历目录树，仅用 O(树深度) 的游标状态即可断点续跑。中断后 resume 不会漏扫、不会重扫已完成的 artifact。
- **四阶段并发流水线** —— 发现 → 下载 → 扫描 → 收集，goroutine 并发执行，可配置并发数、QPS 限速、磁盘预算与重试。Maven Central 实测稳定。
- **私有仓库认证** —— 支持 HTTP Basic Auth、Bearer Token，可扫描 Nexus / Artifactory 等私服中的内部 artifact。
- **定时任务调度** —— 用 `--scan-interval` 注册周期性扫描任务，配置与进度持久化到 SQLite，`task run` 像 cron 一样拉起到期任务。
- **原生支持 AI Agent** —— 提供 Claude Code / Codex 一键复制提示词，粘贴后 AI 自动引导你完成安装、配置、扫描与结果解读。

## 快速开始

### 前置条件

- Go 1.25+
- Git

### 安装

```bash
git clone https://github.com/scagogogo/mvn-repo-scanner.git
cd mvn-repo-scanner
go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner
```

### 扫描某个 groupId

```bash
# 扫描 Maven Central 的某个 groupId
./mvn-repo-scanner scan \
  --repo https://repo.maven.apache.org/maven2 \
  --group org.apache.commons \
  --rules-level core \
  --concurrency 5 \
  --state-file .scan-state.json

# 中断后续扫
./mvn-repo-scanner scan --resume --state-file .scan-state.json
```

### 扫描私有仓库

```bash
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password "$PASSWORD" \
  --group com.internal \
  --rules-level extended
```

### 定时任务

```bash
# 注册每小时扫描任务
./mvn-repo-scanner scan --group com.example --scan-interval 1h --task-id hourly-scan

# 任务管理
./mvn-repo-scanner task list
./mvn-repo-scanner task run   # 拉取并运行到期任务
```

## 输出示例

```
DISCOVERED   SCANNED   FAILED   FINDINGS
    1024      1020        4         12

CRITICAL  HIGH  MEDIUM  LOW
    3      5       2      2

Findings:
  [CRITICAL] hardcoded-password
    File: com/example/lib/1.0/lib-1.0.jar!/application.properties
    Line: 12
    Match: password=SuperSecret123

  [HIGH] aws-secret-key
    File: com/example/lib/1.0/lib-1.0.jar!/config/settings.yml
    Line: 8
    Match: aws_secret_access_key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

## 规则集分档

| 档位 | 数量 | 内容 |
|------|------|------|
| `core` | 6 | 最高频、最低误报：硬编码密码、AWS Key、PEM 私钥、JDBC 凭证、GitHub Token、通用 API Key |
| `extended` | 32 | 扩展云厂商与服务：GCP/Azure/Google/Slack/Stripe/SendGrid 等，含熵检测规则 |
| `all` | 38 | 全部规则，含 Maven settings 密码、GPG passphrase、Docker Registry 认证 |

用 `--rules-level` 选择档位。加 `--rules custom.yaml --rules-merge` 可将自定义规则按 ID 叠加到内置规则上。

## 工作原理

```
┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
│  发现     │ → │  下载     │ → │  扫描     │ → │  收集     │
│ 树形 DFS  │   │ 并发下载  │   │ 解压+检测 │   │ 汇总报告 │
│ 游标断点  │   │ 限速重试  │   │ 38条规则  │   │ + SQLite │
└──────────┘   └──────────┘   └──────────┘   └──────────┘
```

1. **发现阶段**用基于游标的有序 DFS 遍历目录树，游标持久化到 JSON，中断后从游标恢复，不重扫。
2. **下载阶段**N 个 goroutine 并发，QPS 限速、退避重试、磁盘预算背压。
3. **扫描阶段**M 个 goroutine 解压 jar，应用正则 + 熵规则，38 条内置模式。
4. **收集阶段**汇总到内存报告 + SQLite 历史，支持 console / JSON 输出。

详见[文档站工作原理](https://scagogogo.github.io/mvn-repo-scanner/principle/overview)。

## 文档

- [快速开始](https://scagogogo.github.io/mvn-repo-scanner/guide/getting-started)
- [检测规则](https://scagogogo.github.io/mvn-repo-scanner/guide/rules)
- [断点续扫](https://scagogogo.github.io/mvn-repo-scanner/guide/resume)
- [私有仓库认证](https://scagogogo.github.io/mvn-repo-scanner/guide/private-repo)
- [任务调度](https://scagogogo.github.io/mvn-repo-scanner/guide/scheduling)
- [AI Agent 提示词](https://scagogogo.github.io/mvn-repo-scanner/ai-agent/)

## 许可证

[Apache 2.0](LICENSE)

## 贡献

欢迎提 Issue 与 PR。重大改动请先开 Issue 讨论。
