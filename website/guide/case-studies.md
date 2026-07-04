# 实测案例

本页展示 `mvn-repo-scanner` 的真实扫描输出，帮助理解工具能力与结果格式。

## 案例一：测试仓库（含三类泄露）

构造一个包含硬编码密码、高熵密钥、GitHub Token 的测试 jar，模拟仓库扫描：

### 准备测试仓库

```bash
mkdir -p /tmp/test-repo/com/example/leaky-lib/1.0
cat > /tmp/application.properties <<'EOF'
password=HardcodedPassword123!
aws.secret.key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
github.token=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
EOF
cd /tmp/test-repo/com/example/leaky-lib/1.0
zip leaky-lib-1.0.jar /tmp/application.properties
cat > leaky-lib-1.0.pom <<'EOF'
<?xml version="1.0"?>
<project>
  <groupId>com.example</groupId>
  <artifactId>leaky-lib</artifactId>
  <version>1.0</version>
</project>
EOF

# 启动本地 HTTP 仓库
cd /tmp/test-repo && python3 -m http.server 8099 &
```

### 扫描

```bash
./mvn-repo-scanner scan \
  --repo http://localhost:8099 \
  --group com.example \
  --rules-level all \
  --concurrency 2
```

### 真实输出

```
2026/07/05 02:15:40 Starting scan: http://localhost:8099 (concurrency=2, rules=all)

  CRITICAL [hardcoded-password] Hardcoded Password
    Artifact: com.example:leaky-lib:1.0
    File:     META-INF/application.properties:1
    Match:    password=HardcodedPassword123!

  MEDIUM   [high-entropy-secret] High Entropy Secret
    Artifact: com.example:leaky-lib:1.0
    File:     META-INF/application.properties:2
    Match:    wJalrXUtnFEMI/K7MDENG/bPxRfiCYEX

  HIGH     [github-token] GitHub Token
    Artifact: com.example:leaky-lib:1.0
    File:     META-INF/application.properties:3
    Match:    ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

============================================================
Scan Summary:
  Discovered: 2
  Scanned:    1
  Failed:     0
  Findings:   3
  By Severity:
    CRITICAL: 1
    HIGH: 1
    MEDIUM: 1
============================================================
```

### 引擎覆盖

这个案例同时命中了三种检测能力：

| Finding | 引擎 | 规则 | 说明 |
|---------|------|------|------|
| `password=HardcodedPassword123!` | regex | hardcoded-password | 正则匹配 `password=...` 模式 |
| `wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY` | entropy | high-entropy-secret | 40 字符 base64，香农熵 4.7 ≥ 阈值 4.5 |
| `ghp_xxxxxxxxxxxx...` | regex | github-token | 匹配 `ghp_` 前缀 + 36 位字符 |

## 案例二：Maven Central 实测

对 Maven Central 公共仓库的多个 groupId 进行实测扫描：

| groupId | Discovered | Scanned | Findings | 说明 |
|---------|-----------|---------|----------|------|
| `javax.inject` | 10 | 4 | 0 | 规范库，纯 class |
| `com.typesafe.config` | 129 | 33 | 0 | 编译产物，无配置文件 |
| `io.jsonwebtoken` | 601 | 193 | 0 | JWT 库，jar 内仅 pom.xml |
| `com.zaxxer` | 1003 | 283 | 0 | HikariCP，配置无敏感信息 |

**结论**：Maven Central 作为规范化发布的公共仓库，jar 包内极少包含敏感信息——这是仓库性质决定的，不是工具问题。维护者知道不该硬编码凭证，正式发布流程也会清理测试资源。

要扫出真实泄露，建议扫描：

- **企业私服** — 内部开发的库可能不规范，更易泄露
- **快照仓库** — 开发中的版本可能包含测试凭证
- **不规范发布的项目** — 早期项目、个人项目

## 验证工具检测能力

如需验证工具是否能正常检测，用案例一的测试仓库即可——三种引擎（regex、entropy）均会命中。

## 相关

- [常见问题排查](./troubleshooting) — 扫不出结果怎么办
- [检测规则](./rules) — 38 条规则详情
- [敏感内容检测原理](/principle/detection) — 多引擎架构
