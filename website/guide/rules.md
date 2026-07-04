# 检测规则

`mvn-repo-scanner` 内置 **38 条**敏感内容检测规则，覆盖密码、云厂商密钥、私钥证书、数据库连接串、第三方服务 Token 等常见泄露场景。规则由**多个引擎**执行：正则引擎匹配已知格式，熵引擎检测无固定格式的高随机性密钥。

## 规则集档位

通过 `--rules-level` 选择启用范围：

| 档位 | 规则数 | 适用场景 |
|------|--------|---------|
| `core` | 6 | 核心高频规则，速度快误报低，适合快速扫描 |
| `extended` | 32 | 扩展规则，覆盖更多云厂商与服务，含 1 条熵检测，适合深度审计 |
| `all` | 38 | 全部规则，最全面的检测 |

默认 `core`。档位越高，检测越全但可能误报略增。

```bash
# 核心规则快速扫
./mvn-repo-scanner scan --group com.example --rules-level core

# 全量规则深度审计
./mvn-repo-scanner scan --group com.example --rules-level all
```

## 严重级别

每条规则标注严重级别，便于按优先级处置：

| 级别 | 含义 | 典型规则 |
|------|------|---------|
| `CRITICAL` | 直接可被利用的高危凭证 | 硬编码密码、PEM 私钥、GCP 服务账号、Stripe Key、Maven settings 密码 |
| `HIGH` | 云厂商密钥、数据库凭证 | AWS/GCP/Azure/Google/GitHub Token、JDBC/MongoDB 连接串 |
| `MEDIUM` | 可能可利用或需上下文判断 | 通用 API Key、Redis 连接串、Basic Auth Header、Bearer Token |
| `LOW` | 较低风险 | — |

## 规则分类总览

### 凭证与密码（CRITICAL/HIGH）

- `hardcoded-password` — 硬编码密码（password/passwd/pwd 字段）
- `maven-password-xml` — Maven settings.xml 中的密码
- `maven-passphrase` — Maven GPG passphrase
- `spring-credential` — Spring Boot 配置中的凭证
- `sql-credential` — SQL 配置中的凭证
- `encryption-key` — 配置中的加密密钥
- `jwt-secret` — JWT 密钥
- `oauth-client-secret` — OAuth 客户端密钥

### 云厂商密钥（HIGH/CRITICAL）

- `aws-secret-key` — AWS Secret Access Key
- `gcp-service-account` — GCP 服务账号密钥（CRITICAL）
- `google-api-key` / `google-oauth` — Google API Key / OAuth Token
- `azure-storage-key` / `azure-tenant-secret` — Azure 存储与租户密钥
- `digitalocean-token` — DigitalOcean Token
- `firebase-key` — Firebase API Key

### 第三方服务 Token（HIGH/MEDIUM）

- `github-token` — GitHub Personal Access Token
- `slack-token` / `slack-webhook` — Slack Token / Webhook
- `stripe-api-key` — Stripe API Key（CRITICAL）
- `sendgrid-api-key` / `mailgun-api-key` / `twilio-api-key`
- `square-access-token` / `npm-token`
- `docker-registry-auth` — Docker Registry 认证

### 私钥与证书（CRITICAL）

- `private-key` — PEM 格式私钥
- `ssh-private-key` — SSH 私钥
- `pgp-private-key` — PGP 私钥块

### 数据库连接串（HIGH/MEDIUM）

- `jdbc-credentials` — JDBC 连接串中的凭证
- `mongodb-connection` / `mysql-connection` / `postgres-connection` — 数据库连接串
- `redis-connection` — Redis 连接串

### 通用模式（MEDIUM）

- `generic-api-key` — 通用 API Key 模式
- `basic-auth-header` — HTTP Basic Auth Header
- `bearer-token` — Bearer Token
- `high-entropy-secret` — 高熵密钥（entropy 引擎）

## 完整 38 条规则速查表

| ID | 级别 | 名称 | 档位 |
|----|------|------|------|
| `hardcoded-password` | CRITICAL | Hardcoded Password | core |
| `aws-secret-key` | HIGH | AWS Secret Key | core |
| `private-key` | CRITICAL | Private Key | core |
| `jdbc-credentials` | HIGH | JDBC Credentials | core |
| `github-token` | HIGH | GitHub Token | core |
| `generic-api-key` | MEDIUM | Generic API Key | core |
| `google-api-key` | HIGH | Google API Key | extended |
| `google-oauth` | HIGH | Google OAuth Access Token | extended |
| `azure-storage-key` | HIGH | Azure Storage Account Key | extended |
| `azure-tenant-secret` | HIGH | Azure Tenant Secret | extended |
| `gcp-service-account` | CRITICAL | GCP Service Account Key | extended |
| `digitalocean-token` | HIGH | DigitalOcean Token | extended |
| `mongodb-connection` | HIGH | MongoDB Connection String | extended |
| `mysql-connection` | HIGH | MySQL Connection String | extended |
| `postgres-connection` | HIGH | PostgreSQL Connection String | extended |
| `redis-connection` | MEDIUM | Redis Connection String | extended |
| `sql-credential` | HIGH | SQL Credential in Config | extended |
| `slack-token` | HIGH | Slack Token | extended |
| `slack-webhook` | MEDIUM | Slack Webhook URL | extended |
| `stripe-api-key` | CRITICAL | Stripe API Key | extended |
| `sendgrid-api-key` | HIGH | SendGrid API Key | extended |
| `mailgun-api-key` | HIGH | Mailgun API Key | extended |
| `twilio-api-key` | HIGH | Twilio API Key | extended |
| `square-access-token` | HIGH | Square Access Token | extended |
| `npm-token` | HIGH | NPM Access Token | extended |
| `ssh-private-key` | CRITICAL | SSH Private Key | extended |
| `pgp-private-key` | CRITICAL | PGP Private Key Block | extended |
| `jwt-secret` | HIGH | JWT Secret | extended |
| `encryption-key` | HIGH | Encryption Key in Config | extended |
| `basic-auth-header` | MEDIUM | Basic Auth Header | extended |
| `bearer-token` | MEDIUM | Bearer Token | extended |
| `oauth-client-secret` | HIGH | OAuth Client Secret | extended |
| `high-entropy-secret` | MEDIUM | High Entropy Secret（熵引擎） | extended |
| `maven-password-xml` | CRITICAL | Maven Settings Password | all |
| `maven-passphrase` | HIGH | Maven GPG Passphrase | all |
| `spring-credential` | HIGH | Spring Boot Credential | all |
| `docker-registry-auth` | MEDIUM | Docker Registry Auth | all |
| `firebase-key` | MEDIUM | Firebase API Key | all |

::: tip 档位划分
`core` = 6 条最高频低误报规则；`extended` 在 core 基础上加 26 条扩展规则（共 32）；`all` 在 extended 基础上加 6 条 Maven/Spring/Docker 等场景规则（共 38）。
:::

## 查看完整规则列表

```bash
./mvn-repo-scanner rules list
./mvn-repo-scanner rules list --level all
```

输出每条规则的 ID、严重级别、名称与启用状态。

## 文件类型适配

规则按 `file_patterns` 匹配目标文件类型，避免在二进制文件上误匹配：

| 文件类型 | 扫描方式 |
|---------|---------|
| `.properties` `.xml` `.yml` `.yaml` `.json` `.conf` `.cfg` `.ini` | 文本扫描 |
| `.jar` `.war` `.ear` | 解压后扫描内部文本文件 |
| `.pom` | 直接扫描 pom.xml |
| `.class` `.so` `.dll` 等二进制 | 自动跳过 |

## 自定义规则

通过 `--rules` 指定自定义 YAML 规则文件。默认覆盖内置规则；加 `--rules-merge` 则叠加到内置规则上。

```yaml
rules:
  - id: my-company-token
    name: My Company Internal Token
    severity: HIGH
    description: "Detects our internal service tokens"
    engine: regex            # 默认，可省略
    patterns:
      - 'MYTOKEN-[A-Za-z0-9]{32}'
    file_patterns:
      - '\.properties$'
      - '\.ya?ml$'
    allowlist: [MYTOKEN-TEST, example]   # 命中占位符则抑制
    enabled: true
  # 熵引擎规则：检测无固定格式的高随机性密钥
  - id: my-entropy-secret
    name: My High Entropy Secret
    severity: MEDIUM
    engine: entropy
    description: "Flag random-looking base64 strings"
    file_patterns: ['\.properties$', '\.ya?ml$', '\.json$']
    entropy:
      threshold: 4.5
      window: 32
      min_length: 24
      charset: base64
    enabled: true
```

```bash
# 覆盖模式：只用 my-rules.yaml 里的规则
./mvn-repo-scanner scan --group com.example --rules my-rules.yaml

# 叠加模式：内置 38 条 + my-rules.yaml（同 ID 覆盖，新增追加）
./mvn-repo-scanner scan --group com.example --rules my-rules.yaml \
  --rules-merge --rules-level all
```

::: tip 叠加模式适合微调
`--rules-merge` 让你在不复制全部 38 条内置规则的前提下，新增公司内部 Token 规则，或把某条内置规则的 severity 调低。resume 时会校验 rules 配置一致性，避免中途切换导致结果不可比。
:::

## 规则字段说明

| 字段 | 说明 |
|------|------|
| `id` | 规则唯一标识（merge 模式下同 ID 覆盖内置规则） |
| `name` | 规则显示名称 |
| `severity` | 严重级别：CRITICAL/HIGH/MEDIUM/LOW |
| `description` | 规则描述 |
| `engine` | 引擎：`regex`（默认）/`entropy`/自定义引擎名 |
| `patterns` | 正则数组（regex 引擎，任一匹配即命中） |
| `ignorecase` | 编译期加 `(?i)`，免去逐条手写 |
| `capture_group` | 报告指定分组为 Match（0=整条匹配） |
| `entropy` | 熵引擎配置：`threshold`/`window`/`min_length`/`charset` |
| `file_patterns` | 目标文件名正则数组 |
| `allowlist` | 命中值若为这些占位符（example/test）则抑制 |
| `min_length`/`max_length` | 约束 Match 长度 |
| `tags` | 自由分类标签 |
| `enabled` | 是否启用 |

## 下一步

- [命令参考](./commands) — 所有参数
- [私有仓库认证](./private-repo)
- [敏感内容检测原理](/principle/detection)
