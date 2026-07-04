# 私有仓库认证

`mvn-repo-scanner` 不仅支持公开的 Maven Central，也能扫描企业内部的 Nexus / Artifactory 等私服。通过 `--auth-username` / `--auth-password` / `--auth-token` 提供认证。

## 支持的认证方式

| 方式 | 参数 | 适用场景 |
|------|------|---------|
| HTTP Basic Auth | `--auth-username` + `--auth-password` | Nexus、Artifactory 的用户名密码 |
| Bearer Token | `--auth-token` | 私服颁发的 Token |

认证会同时应用于：
- **目录浏览**（PageFetcher / Browser）— 列目录树需要认证
- **文件下载**（Downloader）— 下载 jar/pom 需要认证

## 用法示例

### Basic Auth（用户名密码）

```bash
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin \
  --auth-password 'P@ssw0rd!' \
  --group com.internal
```

### Bearer Token

```bash
./mvn-repo-scanner scan \
  --repo https://artifactory.example.com/artifactory/libs-release \
  --auth-token 'eyJhbGciOi...' \
  --group com.internal
```

### 同时输出 JSON 报告

```bash
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password '***' \
  --group com.internal \
  --rules-level all \
  --concurrency 5 \
  --output json --output-file internal-scan.json \
  --state-file .internal-state.json
```

## 私服 URL 格式

不同私服的仓库根路径不同，需根据实际配置填写 `--repo`：

| 私服类型 | 典型 URL |
|---------|---------|
| Nexus | `https://<host>/repository/<repo-name>/` |
| Artifactory | `https://<host>/artifactory/<repo-name>/` |
| 自建 Apache 目录 | `https://<host>/maven2/` |

::: tip 验证 URL
先用 `curl -u user:pass <url>` 确认 URL 能返回目录列表 HTML，再填入 `--repo`。
:::

## 安全注意事项

### 不要让密码出现在 shell 历史

::: danger 警告
直接在命令行写 `--auth-password '***'` 会让密码留在 `~/.bash_history` 和进程列表中。
:::

更安全的做法：

**方式一：用环境变量**（推荐先 `export`，命令里通过 shell 展开，但 mvn-repo-scanner 本身暂不支持从 env 读取，需 shell 展开）

```bash
read -s MAVEN_PASS   # 交互式输入，不回显
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password "$MAVEN_PASS" \
  --group com.internal
unset MAVEN_PASS
```

**方式二：用配置文件**（`~/.mvn-repo-scanner/config.yaml`）

```yaml
auth-username: admin
auth-password: '***'
repo: https://nexus.example.com/repository/maven-public
group: com.internal
```

```bash
./mvn-repo-scanner scan   # 自动读取配置文件
```

记得 `chmod 600 ~/.mvn-repo-scanner/config.yaml` 限制权限。

### 让 AI 接入时避免经手明文密码

如果你用 [AI Agent](/ai-agent/) 引导配置，**涉及密码的命令请手动执行**，不要让 AI 在对话中处理明文密码。或者使用 Token 方式（`--auth-token`）并设置较短的过期时间。

## 定时扫描私服

私服适合周期性监控（每次有新 artifact 发布都能及时检测）：

```bash
# 注册每日扫描任务
./mvn-repo-scanner scan \
  --repo https://nexus.example.com/repository/maven-public \
  --auth-username admin --auth-password "$MAVEN_PASS" \
  --group com.internal \
  --scan-interval 24h --task-id daily-internal-scan
```

::: warning
定时任务会把完整 config（含认证密码）快照到 `~/.mvn-repo-scanner/scan.db`。请确保该文件权限受限，或改用 Token 认证。
:::

详见 [任务调度](./scheduling)。

## 排查

### 401 Unauthorized

- 检查用户名密码是否正确
- 确认用户对该仓库有读取权限
- Nexus 需确认用户有对应 repository 的 view 权限

### 404 Not Found

- `--repo` 路径错误，确认末尾是否需要 `/`
- `--group` 前缀在私服中不存在

### 403 Forbidden

- 认证通过但无权限访问该路径
- 联系私服管理员开通权限

## 下一步

- [断点续扫](./resume) — 大型私服如何分批扫完
- [任务调度](./scheduling) — 周期性监控私服
