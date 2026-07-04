# 常见问答

## 工具能力

### 这个工具能扫描什么？

扫描 Maven 仓库（中央仓库或私服）中 artifact 的敏感内容泄露：
- jar/war/ear 内部的 `.properties`、`.xml`、`.yml`、`.json` 等文本文件
- pom.xml 与外部 xml 配置
- 38 类敏感信息：硬编码密码、AWS/GCP/Azure 密钥、GitHub/Slack/Stripe Token、PEM 私钥、JDBC/MongoDB 连接串、Maven settings 密码等

### 不能扫描什么？

- `.class` 字节码文件（二进制，自动跳过）
- 已编译进 class 的字符串常量（只能扫源码或文本配置）
- 加密存储的凭证（工具看到的是明文）

### 支持哪些仓库？

任何提供 HTTP 目录浏览的 Maven 仓库：
- Maven Central（`https://repo.maven.apache.org/maven2`）
- Nexus / Artifactory 私服
- 阿里云、华为云等镜像

### 支持哪些认证？

- HTTP Basic Auth（`--auth-username` / `--auth-password`）
- Bearer Token（`--auth-token`）

## 扫描结果

### 为什么扫 Maven Central 没有结果？

正常现象。Maven Central 是规范发布的公共仓库，jar 包内极少包含敏感信息。详见[常见问题排查](./troubleshooting)。

### 误报多怎么办？

1. 用 `--rules-level core` 跳过高误报的扩展规则
2. 自定义规则加 `allowlist` 排除占位符
3. 熵引擎调高 `threshold`（默认 4.5）
4. 用 `min_length`/`max_length` 约束匹配长度

### 扫到的密钥怎么处置？

**立即轮换密钥**，而不是仅删除文件——密钥可能已被使用。轮换后改用环境变量或密钥管理服务注入。详见[最佳实践](./best-practices)。

## 性能

### 扫描太慢怎么办？

1. 用更具体的 `--group`（`com.example.lib` 比 `com.example` 快）
2. 调高 `--download-concurrency`（IO 密集）
3. `--scan-concurrency` 设为 CPU 核数
4. `--qps` 避免触发限流

### 内存占用大吗？

不大。流式发现 + 通道背压使内存保持常数级，与仓库规模无关。游标状态仅几百字节。

### 支持分布式扫描吗？

暂不支持。单机并发已能应对大多数场景。SQLite 任务持久化支持跨进程的断点续跑。

## 断点续扫

### 中断后会漏扫吗？

不会。工具用 in-flight 集合 + 游标回退保证：已交付但未完成的 artifact 会在 resume 时重新处理。详见[游标恢复原理](/principle/cursor)。

### 中断后会重扫吗？

不会。`IsCompleted` 检查让已完成的 artifact 被跳过。只有 in-flight 的会被重新处理（最小重扫）。

### 可以改配置后 resume 吗？

不建议。resume 时会校验配置快照一致性（RepoURL、GroupFilter、RulesLevel、MaxFileSize、RulesFile、RulesMerge），不一致会报错。如需改配置，删除状态文件重新开始，或用 `--rediscover`。

## 自定义规则

### 怎么添加公司内部 Token 规则？

写 YAML 规则文件，用 `--rules-merge` 叠加到内置规则上：

```bash
./mvn-repo-scanner scan --rules company.yaml --rules-merge --rules-level all
```

### 怎么检测无固定格式的随机密钥？

用熵引擎规则（`engine: entropy`），检测高香农熵的 base64/hex 字符串。详见[检测规则](./rules)。

### 可以禁用某条内置规则吗？

可以。用 `--rules-merge` 叠加一条同 ID 的规则，设 `enabled: false`：

```yaml
rules:
  - id: generic-api-key    # 内置规则 ID
    enabled: false          # 禁用它
```

## 部署

### 怎么定时监控企业仓库？

用 `--scan-interval` 注册任务，配合 `task run` 拉起到期任务：

```bash
./mvn-repo-scanner scan --group com.internal --scan-interval 1h --task-id hourly
crontab: */10 * * * * cd /opt/scanner && ./mvn-repo-scanner task run
```

详见[任务调度](./scheduling)。

### 可以集成到 CI 吗？

可以。把扫描命令加入 CI 流水线，用 `--output json` 输出机器可读结果，按 findings 数决定是否阻断：

```bash
./mvn-repo-scanner scan --group com.example --output json --output-file results.json
# 解析 results.json，findings > 0 则告警
```
