# 任务调度

对于需要持续监控的仓库（如企业私服），`mvn-repo-scanner` 支持把扫描注册为**周期性任务**，配置与进度持久化到本地 SQLite，配合 cron 实现无人值守的定时扫描。

## 工作模型

```text
注册任务                    管理任务                执行任务
─────────                  ─────────              ─────────
scan --scan-interval  →    task list              task run
       --task-id       →    task pause/resume      (cron 定时调用)
                           task show/delete
                                │
                                ▼
                    ~/.mvn-repo-scanner/scan.db
                    (SQLite: 任务记录 + 配置快照 + 进度)
```

- **注册**：`scan` 带 `--scan-interval` 时，自动把任务（含完整配置）存入 SQLite
- **管理**：`task` 子命令查询、暂停、恢复、删除任务
- **执行**：`task run` 查找到期任务并执行（配合系统 cron 定时触发）

## 注册周期任务

```bash
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password "$PASS" \
  --group com.internal \
  --rules-level extended \
  --concurrency 5 \
  --scan-interval 1h \
  --task-id hourly-internal-scan
```

- `--scan-interval 1h` — 每小时执行一次（支持 `30m`、`2h`、`24h` 等 Go duration 格式）
- `--task-id hourly-internal-scan` — 任务唯一 ID，便于后续管理

注册后会**立即执行第一次扫描**，扫描完成时记录运行结果并调度下次运行（`next_run_at = now + interval`）。

::: tip 一次性任务
`--scan-interval 0`（默认）表示一次性任务，扫完即标记 `completed`，不调度下次。
:::

## 管理任务

### 列出所有任务

```bash
./mvn-repo-scanner task list
```

```text
TASKID                REPO                     GROUP        INTERVAL  STATUS  NEXT_RUN          SCANS
hourly-internal-scan  https://nexus.../maven..  com.internal 1h0m0s    active  2026-07-01 14:00  3
```

### 查看任务详情

```bash
./mvn-repo-scanner task show hourly-internal-scan
```

输出任务 ID、仓库、间隔、状态、state 文件路径、创建时间、上次运行结果、扫描次数、下次运行时间，以及**完整的配置快照**（JSON）。

### 暂停 / 恢复

```bash
./mvn-repo-scanner task pause hourly-internal-scan   # 暂停（不会被执行）
./mvn-repo-scanner task resume hourly-internal-scan  # 恢复
```

### 删除任务

```bash
./mvn-repo-scanner task delete hourly-internal-scan
```

## 执行到期任务

`task run` 查找所有状态为 `active` 且 `next_run_at` 已到期的任务，逐个执行：

```bash
./mvn-repo-scanner task run
```

```text
Running 1 due task(s)...

=== Running task hourly-internal-scan (repo=https://nexus.../maven-public) ===
...扫描输出...
```

每个任务执行时会从持久化的配置快照重建配置，并**自动 resume** 该任务的状态文件（增量扫描，跳过已完成）。

### 配合系统 cron

`task run` 本身是一次性命令，适合用系统 cron 定时触发。例如每 10 分钟检查一次到期任务：

```bash
# crontab -e
*/10 * * * * cd /path/to && ./mvn-repo-scanner task run >> /var/log/mvn-scan.log 2>&1
```

这样：
- 注册了 `--scan-interval 1h` 的任务，每小时到期一次
- cron 每 10 分钟跑一次 `task run`，到点就执行，没到点就跳过
- 全程无人值守

## 任务状态

| 状态 | 含义 |
|------|------|
| `active` | 活跃，按间隔调度，`task run` 会执行 |
| `paused` | 已暂停，不会被 `task run` 执行 |
| `completed` | 一次性任务已扫完 |
| `error` | 任务出错 |

## 数据存储

任务数据存储在 `~/.mvn-repo-scanner/scan.db`（SQLite），包含：

- 任务元数据（ID、仓库、间隔、状态）
- **完整配置快照**（JSON，含所有参数）
- 运行历史（上次运行时间、结果、扫描次数、下次运行时间）
- state 文件路径（默认 `~/.mvn-repo-scanner/states/<task-id>.json`）

::: warning 认证密码会进快照
如果用 `--auth-password` 扫描私服，密码会被存入配置快照。请：
- 限制 `~/.mvn-repo-scanner/scan.db` 文件权限（`chmod 600`）
- 或改用 `--auth-token`（Token 可设置较短过期时间）
:::

## 完整示例：监控私服新增 artifact

```bash
# 1. 注册每日扫描任务（首次立即执行）
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password "$PASS" \
  --group com.internal \
  --rules-level all \
  --scan-interval 24h \
  --task-id daily-monitor

# 2. 配置 cron 每 30 分钟拉起到期任务
# */30 * * * * /path/to/mvn-repo-scanner task run >> /var/log/mvn-scan.log 2>&1

# 3. 查看任务状态
./mvn-repo-scanner task list
./mvn-repo-scanner task show daily-monitor

# 4. 临时暂停（如维护期间）
./mvn-repo-scanner task pause daily-monitor
./mvn-repo-scanner task resume daily-monitor
```

每次 `task run` 执行时会自动 resume 任务的状态文件——**只扫描上次之后新增的 artifact**，已完成的自动跳过，非常适合持续监控。

## 下一步

- [断点续扫](./resume) — 任务执行的底层 resume 机制
- [持久化与任务管理原理](/principle/persistence)
