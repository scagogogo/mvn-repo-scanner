# 快速开始

本页用 5 分钟带你完成第一次 Maven 仓库敏感内容扫描。

## 1. 编译安装

需要 Go 1.25+。检查：

```bash
go version
```

克隆并编译：

```bash
git clone https://github.com/scagogogo/mvn-repo-scanner
cd mvn-repo-scanner
go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner
```

验证：

```bash
./mvn-repo-scanner version
# mvn-repo-scanner v0.1.0 (linux/amd64)
```

::: tip 国内加速
依赖下载慢时设置代理：`export GOPROXY=https://goproxy.cn,direct`
:::

也可以用 `make build` 或 `go install ./cmd/mvn-repo-scanner`（后者会把二进制装到 `$GOPATH/bin`）。

## 2. 第一次扫描

扫描 Maven Central 的 `javax.inject`（体量小，几秒完成）：

```bash
./mvn-repo-scanner scan \
  --repo https://repo.maven.apache.org/maven2 \
  --group javax.inject \
  --rules-level core \
  -v
```

输出示例：

```text
Starting scan: https://repo.maven.apache.org/maven2 (concurrency=10, rules=core)

============================================================
Scan Summary:
  Discovered: 10
  Scanned:    10
  Failed:     0
  Findings:   0
============================================================
```

各项含义：

| 字段 | 含义 |
|------|------|
| Discovered | 遍历目录树发现的 artifact 文件总数 |
| Scanned | 成功扫描的文件数 |
| Failed | 下载或扫描失败的文件数 |
| Findings | 命中敏感内容规则的发现数 |

`Findings: 0` 表示这个 groupId 下没有检测到敏感内容泄露。

::: tip 为什么是 0？
Maven Central 是规范发布的公共仓库，jar 包内极少包含敏感信息。这不代表工具失效——下一节用测试 jar 验证检测能力。详见[常见问题排查](./troubleshooting)。
:::

## 2.5 验证检测能力（可选）

用一个含泄露的测试 jar 确认工具能正常检测：

```bash
# 创建测试仓库
mkdir -p /tmp/test-repo/com/example/leaky/1.0
echo 'password=HardcodedPassword123!' > /tmp/app.properties
cd /tmp/test-repo/com/example/leaky/1.0
zip leaky-1.0.jar /tmp/app.properties
echo '<?xml version="1.0"?><project><groupId>com.example</groupId><artifactId>leaky</artifactId><version>1.0</version></project>' > leaky-1.0.pom

# 启动本地仓库
cd /tmp/test-repo && python3 -m http.server 8099 &

# 扫描
./mvn-repo-scanner scan --repo http://localhost:8099 --group com.example --rules-level core
```

预期会扫出 `CRITICAL [hardcoded-password]`。详见[实测案例](./case-studies)。

## 3. 扫描更大的范围

扫描 `com.example` 组，启用更多规则，并发 5：

```bash
./mvn-repo-scanner scan \
  --repo https://repo.maven.apache.org/maven2 \
  --group com.example \
  --rules-level extended \
  --concurrency 5 \
  --state-file .mvn-scan-state.json
```

- `--rules-level extended` 启用 32 条规则（默认 core 只有 6 条核心规则）
- `--state-file` 保存扫描进度，便于中断后续扫

## 4. 中断与续扫

大型仓库扫描可能耗时较长。**按 `Ctrl+C` 可安全中断**，进度会自动保存到状态文件。继续扫描：

```bash
./mvn-repo-scanner scan --resume --state-file .mvn-scan-state.json
```

`--resume` 会跳过已完成的 artifact，从断点继续，不重扫不漏扫。

## 5. 输出 JSON 报告

正式扫描建议同时输出结构化 JSON，便于程序处理或 AI 解读：

```bash
./mvn-repo-scanner scan \
  --repo https://repo.maven.apache.org/maven2 \
  --group com.example \
  --output json \
  --output-file report.json
```

`report.json` 包含每个 artifact 的扫描结果与所有 findings 的详细信息（规则、严重级别、文件路径、匹配内容）。

## 下一步

- [命令参考](./commands) — 所有命令和参数详解
- [检测规则](./rules) — 38 条规则覆盖哪些敏感信息
- [断点续扫](./resume) — 大型仓库如何分批扫完
- [私有仓库认证](./private-repo) — 扫描 Nexus/Artifactory 私服
- [让 AI 帮我配置](/ai-agent/) — 不想读文档？复制提示词给 AI
