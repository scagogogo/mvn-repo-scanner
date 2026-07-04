# 命令参考

`mvn-repo-scanner` 提供以下子命令：

| 命令 | 说明 |
|------|------|
| [`scan`](#scan-扫描仓库) | 扫描 Maven 仓库敏感内容（核心命令） |
| [`task`](#task-任务管理) | 管理定时扫描任务 |
| [`history`](#history-历史记录) | 查看扫描历史与统计 |
| [`rules`](#rules-规则管理) | 列出与管理检测规则 |
| `version` | 打印版本号 |

## scan — 扫描仓库

核心命令，遍历仓库目录树，下载并扫描每个 artifact。

### 基本用法

```bash
mvn-repo-scanner scan [flags]
```

### 参数详解

#### 仓库与范围

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-r, --repo` | `https://repo.maven.apache.org/maven2` | Maven 仓库 URL（中央仓库或私服） |
| `-g, --group` | （空） | groupId 前缀过滤，如 `com.example`、`org.apache` |
| `--exclude` | （空） | 排除的 artifact 模式（可多次指定） |

#### 检测规则

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--rules-level` | `core` | 规则集档位：`core`(6条) / `extended`(32条含1条熵) / `all`(38条) |
| `--rules` | （内置） | 自定义规则 YAML 文件路径 |
| `--rules-merge` | `false` | 自定义规则按 ID 叠加到内置规则上（默认：完全覆盖内置） |
| `--max-file-size` | `50MB` | 单文件最大扫描大小 |

#### 并发与限速

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-c, --concurrency` | `10` | 通用并发数（下载/扫描未单独配置时使用） |
| `--download-concurrency` | `0`(=concurrency) | 下载 goroutine 数（IO 密集，可调高掩盖延迟） |
| `--scan-concurrency` | `0`(=concurrency) | 扫描 goroutine 数（CPU 密集，宜接近核数） |
| `--qps` | `0`(不限) | 每秒最大请求数 |
| `-t, --timeout` | `30s` | HTTP 请求超时 |
| `--retries` | `3` | 下载重试次数（仅重试临时错误，404 等永久错误立即失败） |
| `--disk-budget` | `1000`(1GB) | 临时文件最大占用 MB |

#### 断点续扫

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--state-file` | `.mvn-scan-state.json` | 扫描状态文件路径 |
| `--resume` | `false` | 从状态文件断点续扫 |
| `--checkpoint-interval` | `50` | 每 N 个 artifact 存盘一次（0=每个都存） |
| `--retry-failed` | `false` | resume 时重试之前失败的 artifact |
| `--rediscover` | `false` | 强制重新发现，忽略缓存的发现结果 |

#### 私有仓库认证

| 参数 | 说明 |
|------|------|
| `--auth-username` | HTTP Basic Auth 用户名 |
| `--auth-password` | HTTP Basic Auth 密码 |
| `--auth-token` | Bearer Token |

详见 [私有仓库认证](./private-repo)。

#### 输出

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-o, --output` | `console` | 输出格式：`console` / `json` |
| `--output-file` | stdout | 输出文件路径 |
| `-v, --verbose` | `false` | 详细日志 |

#### 任务调度

| 参数 | 说明 |
|------|------|
| `--scan-interval` | 注册为周期任务，指定间隔（如 `1h`、`30m`），`0`=一次性 |
| `--task-id` | 关联的任务 ID（用于任务管理） |

详见 [任务调度](./scheduling)。

### 常用组合

```bash
# 扫描中央仓库某组，core 规则
./mvn-repo-scanner scan --group com.example

# 大型仓库，断点续扫
./mvn-repo-scanner scan --group com.example \
  --concurrency 5 --checkpoint-interval 20 \
  --state-file .state.json
./mvn-repo-scanner scan --resume --state-file .state.json

# 扫描私服 + JSON 报告
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repo/maven-public \
  --auth-username admin --auth-password '***' \
  --group com.internal --output json --output-file report.json

# 注册每小时定时任务
./mvn-repo-scanner scan --group com.example \
  --scan-interval 1h --task-id hourly
```

## task — 任务管理

管理通过 `--scan-interval` 注册的周期扫描任务。任务持久化在 `~/.mvn-repo-scanner/scan.db`（SQLite）。

### 子命令

```bash
mvn-repo-scanner task list                  # 列出所有任务
mvn-repo-scanner task show <task-id>        # 查看任务详情（含完整配置快照）
mvn-repo-scanner task pause <task-id>       # 暂停任务
mvn-repo-scanner task resume <task-id>      # 恢复已暂停任务
mvn-repo-scanner task delete <task-id>      # 删除任务
mvn-repo-scanner task run                   # 运行所有到期任务（cron 式）
```

`task run` 适合配合系统 cron 定时调用，例如每 10 分钟检查一次到期任务。

详见 [任务调度](./scheduling)。

## history — 历史记录

查看扫描历史与统计（数据来自 SQLite）。

```bash
mvn-repo-scanner history          # 查看历史
mvn-repo-scanner history --help   # 查看可用参数
```

## rules — 规则管理

列出与管理检测规则。

```bash
mvn-repo-scanner rules list                    # 列出所有规则
mvn-repo-scanner rules list --level extended   # 查看某档位规则
```

详见 [检测规则](./rules)。

## 退出码

| 退出码 | 含义 |
|--------|------|
| 0 | 扫描正常完成（即使有 findings） |
| 非 0 | 配置错误或扫描过程中出错 |
