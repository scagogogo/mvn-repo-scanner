# 结果输出

`mvn-repo-scanner` 支持控制台实时输出和结构化 JSON 报告两种输出方式。

## 输出格式

通过 `-o, --output` 选择：

| 格式 | 参数 | 适用场景 |
|------|------|---------|
| `console` | `--output console`（默认） | 实时查看，彩色高亮 |
| `json` | `--output json` | 程序处理、AI 解读、归档 |

## 控制台输出

默认格式，扫描过程中实时打印发现的 finding，结束时输出统计摘要：

```text
[CRITICAL] com.example:lib:1.0 — lib-1.0.jar
  Rule: Hardcoded Password
  File: application.properties
  Line: 12
  Match: password=MyS3cretPass123
  ────────────────────────────────────────
...

============================================================
Scan Summary:
  Discovered: 320
  Scanned:    318
  Failed:     2
  Findings:   5
============================================================
```

加 `-v` 显示更详细的逐个 artifact 处理日志。

## JSON 报告

输出结构化 JSON 到文件，便于后续处理：

```bash
./mvn-repo-scanner scan \
  --group com.example \
  --output json \
  --output-file report.json
```

`report.json` 结构示例：

```json
{
  "scan_id": "scan-20260701-120000",
  "repo_url": "https://repo.maven.apache.org/maven2",
  "group_filter": "com.example",
  "start_time": "2026-07-01T12:00:00+08:00",
  "end_time": "2026-07-01T12:05:30+08:00",
  "summary": {
    "total_discovered": 320,
    "total_scanned": 318,
    "total_failed": 2,
    "total_findings": 5,
    "by_severity": {
      "CRITICAL": 2,
      "HIGH": 2,
      "MEDIUM": 1
    },
    "by_rule": {
      "hardcoded-password": 2,
      "aws-secret-key": 1,
      "github-token": 1,
      "private-key": 1
    }
  },
  "findings": [
    {
      "gav": "com.example:lib:1.0",
      "rule_id": "hardcoded-password",
      "rule_name": "Hardcoded Password",
      "severity": "CRITICAL",
      "file_path": "application.properties",
      "line_number": 12,
      "line_content": "password=MyS3cretPass123",
      "match": "password=MyS3cretPass123"
    }
  ]
}
```

### 字段说明

**summary**

| 字段 | 说明 |
|------|------|
| `total_discovered` | 发现的 artifact 总数 |
| `total_scanned` | 成功扫描数 |
| `total_failed` | 失败数 |
| `total_findings` | 命中规则的总发现数 |
| `by_severity` | 按严重级别分组的计数 |
| `by_rule` | 按规则 ID 分组的计数 |

**findings[]**（每个发现）

| 字段 | 说明 |
|------|------|
| `gav` | artifact 坐标 `group:artifact:version` |
| `rule_id` | 命中的规则 ID |
| `rule_name` | 规则名称 |
| `severity` | 严重级别 CRITICAL/HIGH/MEDIUM/LOW |
| `file_path` | 命中文件在 jar 内的路径 |
| `line_number` | 行号 |
| `line_content` | 命中行的内容 |
| `match` | 实际匹配到的字符串 |

## 用 jq 处理 JSON 报告

```bash
# 只看 CRITICAL 级别的发现
jq '.findings[] | select(.severity=="CRITICAL")' report.json

# 按规则统计
jq '.summary.by_rule' report.json

# 列出所有命中 private-key 规则的 artifact
jq '.findings[] | select(.rule_id=="private-key") | .gav' report.json

# 导出为 CSV
jq -r '.findings[] | [.gav,.severity,.rule_id,.file_path,.line_number] | @csv' report.json
```

## 让 AI 解读报告

JSON 报告是 AI 友好的格式。扫描后让 Claude Code / Codex 读取并解读：

```bash
./mvn-repo-scanner scan --group com.example --output json --output-file report.json
```

然后在 AI Agent 中说：

```text
读取 report.json，按严重级别从高到低汇总所有 findings，
对每条发现给出处置建议（轮换密钥/删除文件/改用环境变量）。
```

详见 [AI Agent 接入](/ai-agent/)。

## SQLite 历史记录

除了 JSON 报告，每次扫描的结果也会写入 `~/.mvn-repo-scanner/scan.db`（SQLite），可用 `history` 命令查看，也可用 `sqlite3` 直接查询：

```bash
./mvn-repo-scanner history

# 直接查 SQLite
sqlite3 ~/.mvn-repo-scanner/scan.db "SELECT group_id, artifact_id, version, findings_count, scan_time FROM scan_records ORDER BY scan_time DESC LIMIT 10;"

# 查所有 CRITICAL 发现
sqlite3 ~/.mvn-repo-scanner/scan.db "SELECT * FROM findings WHERE severity='CRITICAL';"
```

详见 [持久化与任务管理原理](/principle/persistence)。

## 下一步

- [命令参考](./commands)
- [AI Agent 接入](/ai-agent/)
