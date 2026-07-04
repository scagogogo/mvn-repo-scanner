# 常见问题排查

## 扫描 Maven Central 没有发现任何结果

这是**正常现象**。Maven Central 是规范化发布的公共仓库，发布的 jar 包经过了维护者的审查，极少包含敏感信息。绝大多数 jar 只是编译后的 `.class` 文件，不含配置文件。

### 为什么扫不出？

1. **jar 内部没有文本配置文件** — 编译产物只有 `.class` 文件，没有 `.properties`、`.xml`、`.yml` 等
2. **敏感信息已被清理** — 维护者知道不该硬编码凭证，示例代码会用占位符
3. **Maven 发布规范** — 正式发布流程会排除测试资源和配置示例

### 如何验证工具正常工作？

创建一个包含泄露的测试 jar，用本地 HTTP 服务器模拟仓库：

```bash
# 创建测试仓库
mkdir -p /tmp/test-repo/com/example/test/1.0
echo 'password=HardcodedPassword123!' > /tmp/application.properties
cd /tmp/test-repo/com/example/test/1.0
zip test-1.0.jar /tmp/application.properties
echo '<?xml version="1.0"?><project><groupId>com.example</groupId><artifactId>test</artifactId><version>1.0</version></project>' > test-1.0.pom

# 启动本地服务器
cd /tmp/test-repo && python3 -m http.server 8099 &

# 扫描
./mvn-repo-scanner scan --repo http://localhost:8099 --group com.example --rules-level core
```

预期输出：

```
CRITICAL [hardcoded-password] Hardcoded Password
  Artifact: com.example:test:1.0
  File:     application.properties:1
  Match:    password=HardcodedPassword123!
```

### 什么时候能扫出真实泄露？

扫描以下仓库更可能发现泄露：

- **企业私服**（Nexus / Artifactory）— 内部开发的库可能不规范
- **测试仓库** — 快照版本、原型项目可能打包了测试配置
- **不规范发布的项目** — 早期项目、个人项目可能疏忽

## 扫描速度很慢

Maven Central 目录树庞大，发现阶段需要逐级请求 HTML 解析。优化方式：

1. **使用更深的 groupId** — `--group com.example.specific` 比 `--group com.example` 小得多
2. **提高 QPS** — `--qps 50`（注意别触发限流）
3. **限制并发** — 下载 IO 密集，`--download-concurrency 10-20`；扫描 CPU 密集，`--scan-concurrency` 设为 CPU 核数

## 发现阶段卡住

Maven Central 对目录浏览请求有限流。如果发现阶段长时间无进度：

1. 降低 `--qps`（默认无限制可能触发 429）
2. 检查网络连通性：`curl https://repo.maven.apache.org/maven2/`
3. 用 `--resume` 从断点继续

## 扫描大量 404 错误

部分 artifact 目录下可能缺少 `.jar` 或 `.pom` 文件（只发布了 pom）。工具会记录失败项，可用 `--retry-failed` 重试。

## 熵检测误报多

`high-entropy-secret` 规则对高随机性字符串报警，可能误报 base64 编码的正常数据。处置方式：

1. 用 `allowlist` 排除已知占位符
2. 调高 `threshold`（默认 4.5 bits/char）
3. 用 `--rules-level core` 跳过熵规则

## 后续阅读

- [检测规则](/guide/rules) — 规则配置与自定义
- [敏感内容检测原理](/principle/detection) — 多引擎架构
- [私有仓库认证](/guide/private-repo) — 扫描企业私服
