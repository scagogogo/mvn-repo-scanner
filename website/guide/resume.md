# 断点续扫

大型 Maven 仓库（尤其私服）可能有上万个 artifact，单次扫描耗时数小时甚至更久。`mvn-repo-scanner` 提供完善的断点续扫能力：**中断后从断点继续，不重扫已完成的、不漏扫未完成的**。

## 核心机制

断点续扫基于两层持久化：

1. **JSON 状态文件**（`--state-file`）— 记录已扫描 artifact、失败项、in-flight 项、发现游标
2. **游标式发现**（discovery cursor）— 记录目录树遍历到哪了，只需 O(树深度) 状态

原理详见 [树形遍历与游标恢复](/principle/cursor)。

## 基本用法

### 首次扫描（带状态保存）

```bash
./mvn-repo-scanner scan \
  --repo https://repo.maven.apache.org/maven2 \
  --group org.apache \
  --concurrency 5 \
  --checkpoint-interval 20 \
  --state-file .apache-state.json
```

- `--state-file` 指定状态文件路径
- `--checkpoint-interval 20` 每扫 20 个 artifact 存盘一次（默认 50）

### 安全中断

扫描过程中按 `Ctrl+C`（SIGINT）或 `SIGTERM`，工具会：

1. 检测到信号，停止派发新任务
2. **立即 flush 当前状态**到 JSON 文件（包括已扫描、in-flight、游标）
3. 标记状态为 `interrupted`，安全退出

```text
^C
Received interrupt, flushing state and shutting down...
```

::: tip 中断是安全的
状态会在退出前强制写入磁盘，不会丢失已完成的进度。in-flight 的 artifact（已下载未扫完的）也会被记录，下次 resume 时重新处理，不会漏扫。
:::

### 断点续扫

```bash
./mvn-repo-scanner scan --resume --state-file .apache-state.json
```

`--resume` 会：
- 加载状态文件，校验配置一致性（RepoURL、GroupFilter、RulesLevel 等）
- 跳过已 `completed` 的 artifact
- 重新处理 `in-flight` 的 artifact（中断时未完成的）
- 从保存的游标继续目录树遍历

```text
Found existing state file (status: interrupted). Use --resume to continue...
Resuming scan scan-xxx (status: interrupted, discovered: 320, scanned: 280, failed: 2, in-flight: 3)
```

## 参数详解

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--state-file` | `.mvn-scan-state.json` | 状态文件路径 |
| `--resume` | false | 从状态文件续扫 |
| `--checkpoint-interval` | 50 | 每 N 个 artifact 存盘（0=每个都存，最安全但最慢） |
| `--retry-failed` | false | resume 时重试之前失败的 artifact |
| `--rediscover` | false | 强制重新发现，忽略缓存的发现结果 |

### checkpoint-interval 怎么选

- **小值（1-10）**：存盘频繁，中断丢失少，但 IO 开销大。适合不稳定环境。
- **大值（50-100）**：存盘稀疏，性能好，但中断时最多丢失 N-1 条进度。适合稳定环境。
- **0**：每个 artifact 都存盘，最安全。小型仓库可用。

::: tip 信号也会强制 flush
即使 `checkpoint-interval` 设得很大，收到 SIGINT 时也会**强制 flush 全部状态**，所以中断不会丢失"未满 checkpoint"的进度。
:::

## 重试失败的 artifact

扫描中部分 artifact 可能因网络错误、超时、损坏文件而失败。这些记录在状态的 `failed` 列表里。resume 时默认**跳过**它们（视为永久失败）。

如果想重试：

```bash
./mvn-repo-scanner scan --resume --retry-failed --state-file .apache-state.json
```

`--retry-failed` 会把可重试的失败项（重试次数未超 `--retries`）重新加入扫描队列。

## 强制重新发现

默认 resume 会复用上次缓存的发现结果（discovery cache），跳过目录树遍历阶段。如果你想重新遍历（例如仓库有新增 artifact）：

```bash
./mvn-repo-scanner scan --resume --rediscover --state-file .apache-state.json
```

`--rediscover` 清除发现缓存，重新遍历目录树。适合**增量监控**场景——每次重新发现新增的 artifact，已完成的自动跳过。

## 状态文件结构

状态文件是 JSON 格式，主要字段：

```json
{
  "version": 1,
  "scan_id": "scan-20260701-120000",
  "status": "interrupted",
  "repo_url": "https://repo.maven.apache.org/maven2",
  "config_snapshot": { "repo_url": "...", "group_filter": "...", "rules_level": "core" },
  "completed_artifacts": ["com/example/lib/1.0", "..."],
  "failed_entries": [{ "path": "...", "error": "...", "retries": 1 }],
  "in_flight_artifacts": ["com/example/lib/2.0"],
  "discovery_cursor": [{ "dir_path": "org/apache", "next_idx": 2 }],
  "total_discovered": 320,
  "total_scanned": 280
}
```

| 字段 | 说明 |
|------|------|
| `status` | `running` / `completed` / `interrupted` |
| `completed_artifacts` | 已成功扫描的路径 |
| `failed_entries` | 失败项及错误信息、重试次数 |
| `in_flight_artifacts` | 中断时正在处理的项（resume 会重扫） |
| `discovery_cursor` | 目录树遍历游标（栈） |
| `config_snapshot` | 配置快照，resume 时校验一致性 |

::: warning 不要手动编辑
状态文件由工具自动维护，手动编辑可能导致游标错位、漏扫或重扫。如需重新开始，删除状态文件即可。
:::

## 配置一致性校验

resume 时会校验当前配置与状态文件中的 `config_snapshot` 是否一致（RepoURL、GroupFilter、RulesLevel、MaxFileSize）。不一致会报错：

```text
config mismatch on resume: repo_url: expected https://... got https://...
```

避免你用不同配置 resume 导致结果混乱。如需更改配置重新扫描，删除状态文件或用 `--rediscover`。

## 实战：分批扫描大型私服

```bash
# 第一批：扫到自然中断或手动 Ctrl+C
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password "$PASS" \
  --group com.internal \
  --concurrency 5 --checkpoint-interval 50 \
  --state-file .internal-state.json

# 续扫（可多次执行直到 completed）
./mvn-repo-scanner scan --resume --state-file .internal-state.json

# 想顺便重试失败项
./mvn-repo-scanner scan --resume --retry-failed --state-file .internal-state.json

# 监控新增 artifact（重新发现 + 跳过已完成）
./mvn-repo-scanner scan --resume --rediscover --state-file .internal-state.json
```

## 下一步

- [任务调度](./scheduling) — 自动化周期扫描
- [树形遍历与游标恢复原理](/principle/cursor)
