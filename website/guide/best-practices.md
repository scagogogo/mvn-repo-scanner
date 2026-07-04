# 最佳实践

## 扫描策略

### 从小范围开始

不要一开始就扫描整个 `com.apache` 或 `com.amazonaws`——这些 groupId 下有数百个 artifact，发现阶段会很慢。先用具体的子 groupId 验证：

```bash
# ✅ 推荐：具体到子 groupId
./mvn-repo-scanner scan --group com.example.library ...

# ❌ 避免：太宽泛
./mvn-repo-scanner scan --group com ...
```

### 规则集选择

| 场景 | 推荐档位 | 理由 |
|------|---------|------|
| 快速验证工具 | `core` | 6 条规则，最快，最低误报 |
| 生产扫描 | `extended` | 32 条规则，覆盖主流云厂商与服务 |
| 全面审计 | `all` | 38 条规则，含 Maven settings、GPG 等 |
| 企业内扫私服 | `all` + 自定义规则 | 叠加公司内部 Token 规则 |

### 并发调优

下载（IO 密集）与扫描（CPU 密集）解耦调优：

```bash
./mvn-repo-scanner scan \
  --download-concurrency 20 \    # IO 密集，调高掩盖网络延迟
  --scan-concurrency 8 \          # CPU 密集，设为 CPU 逻辑核数
  --qps 30 \                       # 避免触发仓库限流
  --disk-budget 2000 \            # 2GB 临时文件预算
  --group com.example
```

## 大型仓库扫描

### 启用断点续扫

```bash
# 首次扫描，频繁存盘（不稳定环境）
./mvn-repo-scanner scan \
  --group com.large.corp \
  --checkpoint-interval 20 \
  --state-file .large-scan.json

# 中断后继续
./mvn-repo-scanner scan --resume --state-file .large-scan.json

# 重试失败的
./mvn-repo-scanner scan --resume --retry-failed --state-file .large-scan.json
```

### 定时增量监控

监控企业仓库新增的 artifact：

```bash
# 注册每小时任务（自动跳过已完成的，重新发现新增的）
./mvn-repo-scanner scan \
  --group com.internal \
  --scan-interval 1h \
  --task-id hourly-internal \
  --rediscover
```

## 自定义规则

### 叠加而非替换

`--rules-merge` 把自定义规则按 ID 叠加到内置规则上，不必复制 38 条内置规则：

```bash
./mvn-repo-scanner scan \
  --rules company-tokens.yaml \
  --rules-merge \
  --rules-level all
```

### 减少误报

用 `allowlist` 排除占位符，用 `min_length`/`max_length` 约束长度：

```yaml
- id: my-token
  patterns: ['MYTOKEN-[A-Za-z0-9]{32}']
  allowlist:
    - 'MYTOKEN-TEST'
    - 'MYTOKEN-EXAMPLE'
    - 'example'
  min_length: 20
  enabled: true
```

### 熵引擎调优

熵检测对高随机性字符串报警，误报较多时：

```yaml
- id: high-entropy-secret
  engine: entropy
  entropy:
    threshold: 4.7    # 调高阈值（默认 4.5）
    min_length: 32    # 只看更长的字符串
    charset: base64
  allowlist: [example, test, changeme, your-]
```

## 私服扫描

### 认证

```bash
# Basic Auth
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username scanner --auth-password "$SCANNER_PASS" \
  --group com.internal

# Bearer Token
./mvn-repo-scanner scan \
  --repo https://artifactory.example.com/artifactory/libs-release \
  --auth-token "$ARTIFACTORY_TOKEN" \
  --group com.internal
```

::: warning 不要泄露凭证到命令历史
用环境变量传递密码，避免出现在 shell history 或 process list：
```bash
--auth-password "$PASSWORD"  # ✅
--auth-password 'secret123'  # ❌ 会出现在 ps / history
```
:::

## 结果处置

### 优先级

1. **CRITICAL** — 立即轮换密钥（私钥、Stripe Key、硬编码密码、Maven settings 密码）
2. **HIGH** — 尽快轮换（云厂商密钥、数据库凭证、第三方服务 Token）
3. **MEDIUM** — 评估后处置（通用 API Key、Bearer Token、Redis 连接串）
4. **LOW** — 记录观察

### 处置流程

1. 核实 `line_content` 与 `match`，判断是否真实凭证
2. 看所在 artifact 是否为测试包（`*-test.jar`、`*-sources.jar`）
3. 确认真泄露后：**轮换密钥**（而非仅删除文件，因为可能已被使用）
4. 改用环境变量或密钥管理服务注入，避免再次硬编码

## 相关

- [常见问题排查](./troubleshooting)
- [检测规则](./rules)
- [断点续扫](./resume)
