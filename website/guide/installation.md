# 安装

`mvn-repo-scanner` 是用 Go 编写的单二进制命令行工具，无运行时依赖。

## 前置条件

- **Go 1.25+**（用于源码编译）

检查：

```bash
go version
# go version go1.25.0 linux/amd64
```

未安装 Go？参见 [Go 官方安装指南](https://go.dev/doc/install)。

## 方式一：源码编译（推荐）

```bash
git clone https://github.com/scagogogo/mvn-repo-scanner
cd mvn-repo-scanner
go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner
```

或用 Make：

```bash
make build
```

编译后在当前目录生成 `mvn-repo-scanner` 二进制。

## 方式二：安装到 PATH

如果你已配置 `$GOPATH/bin`（通常在 `~/go/bin`）到 PATH：

```bash
go install ./cmd/mvn-repo-scanner
# 之后可直接全局调用
mvn-repo-scanner version
```

或用 Make：

```bash
make install
```

## 方式三：直接 go run

不想编译，也可直接运行：

```bash
go run ./cmd/mvn-repo-scanner scan --repo https://repo.maven.apache.org/maven2 --group javax.inject
```

::: warning
`go run` 每次会重新编译，不适合频繁使用。正式使用建议先 `go build`。
:::

## 验证安装

```bash
./mvn-repo-scanner version
# mvn-repo-scanner v0.1.0 (linux/amd64)

./mvn-repo-scanner --help
```

能看到版本号和命令列表即安装成功。

## 依赖下载加速

国内用户编译时若卡在依赖下载：

```bash
export GOPROXY=https://goproxy.cn,direct
export GOSUMDB=off   # 可选，关闭校验加速
go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner
```

## 从源码更新

```bash
git pull
go build -o mvn-repo-scanner ./cmd/mvn-repo-scanner
```

## 构建产物说明

| 文件 | 说明 |
|------|------|
| `mvn-repo-scanner` | 主二进制，单文件可分发 |
| `configs/rules.yaml` | 默认规则配置（已内置，无需额外携带） |

二进制内置了所有 38 条检测规则和默认配置，分发时只需拷贝单个二进制文件即可。

## 下一步

- [快速开始](./getting-started) — 跑通第一次扫描
- [命令参考](./commands) — 所有参数详解
