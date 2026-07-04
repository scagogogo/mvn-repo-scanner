# mvn-repo-scanner

[![Go Report Card](https://goreportcard.com/badge/github.com/scagogogo/mvn-repo-scanner)](https://goreportcard.com/report/github.com/scagogogo/mvn-repo-scanner)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![GoDoc](https://godoc.org/github.com/scagogogo/mvn-repo-scanner?status.svg)](https://godoc.org/github.com/scagogogo/mvn-repo-scanner)

A security scanner for Maven repositories that detects leaked secrets (passwords, API keys, private keys, certificates) inside JARs, POMs, and configuration files. Works with Maven Central and private repositories (Nexus, Artifactory).

**[Documentation](https://scagogogo.github.io/mvn-repo-scanner/)** | **[简体中文](README.zh-CN.md)**

## Features

- **38 Built-in Detection Rules** — Hardcoded passwords, AWS/GCP/Azure keys, GitHub/Slack/Stripe tokens, PEM private keys, JDBC/MongoDB connection strings, Maven settings passwords, and more. Choose from `core`/`extended`/`all` rule levels or define custom rules via YAML.
- **Cursor-based Resume** — Ordered DFS traversal with O(tree depth) cursor state. Resume from interruption without re-scanning completed artifacts or missing in-flight ones.
- **4-Stage Concurrent Pipeline** — Discovery → Download → Scan → Collect. Configurable concurrency, QPS rate limiting, disk budget, and retries. Stable on Maven Central scale.
- **Private Repository Auth** — HTTP Basic Auth and Bearer Token support for Nexus, Artifactory, and other private repositories.
- **Scheduled Tasks** — Register periodic scans with `--scan-interval`. Task metadata persisted to SQLite; `task run` pulls due tasks like cron.
- **AI Agent Ready** — One-click prompts for Claude Code / Codex to guide you through installation, configuration, scanning, and result interpretation.

## Quick Start

### Prerequisites

- Go 1.25+
- Git

### Install

```bash
git clone https://github.com/scagogogo/mvn-repo-scanner.git
cd mvn-repo-scanner
go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner
```

### Scan a GroupId

```bash
# Scan Maven Central for a specific groupId
./mvn-repo-scanner scan \
  --repo https://repo.maven.apache.org/maven2 \
  --group org.apache.commons \
  --rules-level core \
  --concurrency 5 \
  --state-file .scan-state.json

# Resume after interruption
./mvn-repo-scanner scan --resume --state-file .scan-state.json
```

### Scan a Private Repository

```bash
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password "$PASSWORD" \
  --group com.internal \
  --rules-level extended
```

### Schedule Periodic Scans

```bash
# Register an hourly scan task
./mvn-repo-scanner scan --group com.example --scan-interval 1h --task-id hourly-scan

# Manage tasks
./mvn-repo-scanner task list
./mvn-repo-scanner task run   # Pull and run due tasks
```

## Output Example

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

## Rule Levels

| Level  | Count | Description |
|--------|-------|-------------|
| `core` | 6     | Most common, lowest false positives: hardcoded passwords, AWS keys, PEM private keys, JDBC credentials, GitHub tokens, generic API keys |
| `extended` | 32 | Extended cloud/service providers: GCP/Azure/Google/Slack/Stripe/SendGrid, plus entropy detection |
| `all` | 38 | Full set including Maven settings passwords, GPG passphrases, Docker registry auth |

Use `--rules-level` to select. Add `--rules custom.yaml --rules-merge` to overlay custom rules on top.

## How It Works

```
┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
│ Discover │ → │ Download │ → │  Scan    │ → │ Collect  │
│ Tree DFS │   │ Parallel │   │ Unzip+   │   │ Report + │
│ Cursor   │   │ Retry    │   │ Detect   │   │ SQLite   │
└──────────┘   └──────────┘   └──────────┘   └──────────┘
```

1. **Discover** — Ordered DFS traversal with cursor state saved to JSON. Resume from cursor without re-scanning.
2. **Download** — N goroutines with QPS limiting, retry with backoff, disk budget backpressure.
3. **Scan** — M goroutines unzip JARs, apply regex + entropy rules. 38 built-in patterns.
4. **Collect** — Aggregate to memory report + SQLite history. Console/JSON output.

See [Documentation](https://scagogogo.github.io/mvn-repo-scanner/principle/overview) for details.

## Documentation

- [Getting Started](https://scagogogo.github.io/mvn-repo-scanner/guide/getting-started)
- [Detection Rules](https://scagogogo.github.io/mvn-repo-scanner/guide/rules)
- [Resume & Checkpoints](https://scagogogo.github.io/mvn-repo-scanner/guide/resume)
- [Private Repository Auth](https://scagogogo.github.io/mvn-repo-scanner/guide/private-repo)
- [Task Scheduling](https://scagogogo.github.io/mvn-repo-scanner/guide/scheduling)
- [AI Agent Prompts](https://scagogogo.github.io/mvn-repo-scanner/ai-agent/)

## License

[Apache 2.0](LICENSE)

## Contributing

Issues and pull requests welcome. For major changes, open an issue first to discuss.
