# Resume & Concurrency Enhancement + Maven Central Sources Scan Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development`
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** 三件事：①断点续跑补 discovery 阶段 QPS 限流（防 Maven Central 429）；②并发能力增强（连接池随配置、discovery 与 download QPS 统一）；③以 Maven Central 为例，通过扫描 `-sources.jar`（含 .java 源码）实扫出泄露并输出验证报告。

**Architecture:** 数据流：discovery（Browser 逐级请求 HTML，新增 QPS limiter + 连接池随 Concurrency）→ artifact 过滤（新增 `--include-sources`/`--skip-pom` 控制 classifier）→ download（已有 QPS limiter，连接池随 DownloadConcurrency）→ extract+detect（sources jar 的 .java entry 走 scannableExts 命中）→ collector。断点续跑：classifier 选项写入 ConfigSnapshot，resume 时校验防配置漂移。设计理由：discovery 与 download 共享 `newHTTPClient` 但当前 QPS 只注入 downloader——把 limiter 提到 client 层让两个阶段都受限；连接池从硬编码改为 `max(concurrency, 32)` 既保小仓库复用又不限大仓库。

**Tech Stack:** Go 1.25.0, cobra CLI, `golang.org/x/time/rate`（已有依赖，downloader 在用）, testify v1.10.0

**Risks:**
- Maven Central sources jar 实扫可能仍 0 finding（源码也干净）→ 缓解：Task 5 先用本地含泄露 sources jar 验证能力命中，再扫 Maven Central 真实 sources jar 验证流程，报告如实记录
- classifier 选项影响 discovery 结果集，与 resume cursor 配合可能漏扫 → 缓解：classifier/skip-pom 写入 ConfigSnapshot，resume 时 ValidateConfig 校验，不一致则报 config mismatch
- discovery 加 QPS 限流拖慢小仓库 → 缓解：QPS 默认 0 不限流（向后兼容），仅用户显式设置时生效
- 连接池调大可能对 Maven Central 不礼貌触发封禁 → 缓解：MaxConnsPerHost 上限封顶 128，文档说明礼貌值

---

### Task 1: Config 新增 classifier/sources/skip-pom/browser-concurrency 字段

**Depends on:** None
**Files:**
- Modify: `internal/config/config.go:17-37`（Config 结构体加字段）
- Modify: `internal/config/config.go:48-67`（DefaultConfig 加默认值）
- Modify: `internal/config/config.go:74-108`（Validate 加校验）
- Modify: `cmd/scan.go:30-60`（注册 flag）
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 修改 Config 结构体以新增 classifier 相关字段**
文件: `internal/config/config.go:17-37`（在 ScanConcurrency 字段后插入）

```go
	DownloadConcurrency int           `mapstructure:"download-concurrency"`
	ScanConcurrency     int           `mapstructure:"scan-concurrency"`
	// IncludeSources controls whether -sources.jar (containing .java source) is
	// scanned. Sources jars are the main place real secrets leak on Maven Central
	// (binary .class jars are skipped by the binary-content pre-check). Off by
	// default because sources jars are large and numerous; enable to hunt leaks.
	IncludeSources bool `mapstructure:"include-sources"`
	// SkipPom controls whether .pom/.xml files are scanned. On by default (pom
	// metadata may leak), but users scanning large repos can skip for speed.
	SkipPom bool `mapstructure:"skip-pom"`
	// BrowserConcurrency caps the discovery phase connection pool. 0 = auto
	// (max(Concurrency, 32)). Discovery is HTML-listing fetches, lighter than
	// downloads, but a huge tree like com.amazonaws still opens many sockets.
	BrowserConcurrency int `mapstructure:"browser-concurrency"`
	DiskBudgetMB        int           `mapstructure:"disk-budget"`
```

- [ ] **Step 2: 修改 DefaultConfig 以设默认值**
文件: `internal/config/config.go:48-67`（在 DownloadConcurrency/ScanConcurrency 默认值后加）

```go
		DownloadConcurrency: 0,
		ScanConcurrency:     0, // 0 = fall back to Concurrency (CPU-bound scan decoupled from IO-bound download)
		IncludeSources:      false,
		SkipPom:             false,
		BrowserConcurrency:  0, // 0 = auto (max(Concurrency, 32))
		DiskBudgetMB:        1000, // 1GB default disk budget for temp files
```

- [ ] **Step 3: 修改 Validate 以加校验**
文件: `internal/config/config.go:74-108`（在 ScanConcurrency 校验后、DiskBudgetMB 校验前插入）

```go
	if c.ScanConcurrency < 0 {
		return fmt.Errorf("scan concurrency must be non-negative (0 = use concurrency)")
	}
	if c.BrowserConcurrency < 0 {
		return fmt.Errorf("browser concurrency must be non-negative (0 = auto)")
	}
	if c.DiskBudgetMB < 0 {
		return fmt.Errorf("disk budget must be non-negative")
	}
```

- [ ] **Step 4: 注册 flag 到 scanCmd**
文件: `cmd/scan.go:30-60`（在现有 scan-concurrency flag 后插入）

```go
	scanCmd.Flags().IntVar(&cfg.ScanConcurrency, "scan-concurrency", cfg.ScanConcurrency, "scan (extract+detect) worker count (0=use concurrency)")
	scanCmd.Flags().BoolVar(&cfg.IncludeSources, "include-sources", cfg.IncludeSources, "scan -sources.jar (contains .java source, main source of real leaks on Maven Central)")
	scanCmd.Flags().BoolVar(&cfg.SkipPom, "skip-pom", cfg.SkipPom, "skip .pom/.xml files (faster, may miss metadata leaks)")
	scanCmd.Flags().IntVar(&cfg.BrowserConcurrency, "browser-concurrency", cfg.BrowserConcurrency, "discovery connection pool cap (0=auto max(concurrency,32))")
```

- [ ] **Step 5: 验证 config 编译 + 现有测试**
Run: `go test ./internal/config/ ./cmd/`
Expected:
  - Exit code: 0
  - Output does NOT contain: "FAIL"

- [ ] **Step 6: 提交**
Run: `git add internal/config/config.go cmd/scan.go && git commit -m "feat(config): add include-sources/skip-pom/browser-concurrency flags for leak hunting + discovery tuning"`

---

### Task 2: Browser 注入 QPS 限流 + 连接池随配置

**Depends on:** Task 1
**Files:**
- Modify: `internal/repo/browser.go:63-90`（Browser 结构体 + NewBrowser 签名）
- Modify: `internal/repo/browser.go` fetch 方法（请求前 Wait limiter）
- Modify: `cmd/scan.go:206`（传 QPS + BrowserConcurrency 给 browser）
- Test: `internal/repo/browser_test.go`

- [ ] **Step 1: 修改 Browser 结构体以新增 limiter 与 maxConns 字段**
文件: `internal/repo/browser.go:63-90`（替换 Browser 结构体与 NewBrowserWithAuth）

```go
// Browser traverses a Maven repository's directory structure.
type Browser struct {
	client      *http.Client
	groupFilter string
	auth        AuthConfig
	limiter     *rate.Limiter // optional QPS throttle for discovery fetches
}

// NewBrowser creates a new repository browser.
func NewBrowser(timeout time.Duration, groupFilter string) *Browser {
	return &Browser{
		client:      newHTTPClient(timeout, 32),
		groupFilter: groupFilter,
	}
}

// NewBrowserWithAuth creates a browser for a private repository with credentials.
func NewBrowserWithAuth(timeout time.Duration, groupFilter string, auth AuthConfig) *Browser {
	b := NewBrowser(timeout, groupFilter)
	b.auth = auth
	return b
}

// WithLimiter attaches a rate limiter so discovery fetches are throttled. nil
// means unlimited (backward compatible). Returns the browser for chaining.
func (b *Browser) WithLimiter(l *rate.Limiter) *Browser {
	b.limiter = l
	return b
}

// WithMaxConnsPerHost rebuilds the HTTP client with a custom per-host connection
// cap, overriding the default 32. Useful when discovery concurrency is raised.
func (b *Browser) WithMaxConnsPerHost(timeout time.Duration, maxConns int) *Browser {
	b.client = newHTTPClient(timeout, maxConns)
	return b
}
```

- [ ] **Step 2: 修改 Browser fetch 以在请求前等待 limiter**

先定位 fetch 方法。文件: `internal/repo/browser.go`（找到 `func (b *Browser)` 中实际发 `b.client.Do` 的方法，通常是 fetchURL 或 fetchDir）

```go
// 在 b.client.Do(req) 之前插入 limiter 等待：
	if b.limiter != nil {
		if err := b.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}
	}
```

注：执行时需读 browser.go 找到实际 Do 调用点，在 ctx 可达的那一处插入。若 fetch 用 context.Background()，改为接受 ctx 参数（若签名已有 ctx 则直接用）。

- [ ] **Step 3: 修改 scan.go 装配 browser 以传 QPS 与 concurrency**
文件: `cmd/scan.go:206`（替换 browser 构造行）

```go
	browser := repo.NewBrowserWithAuth(cfg.Timeout, cfg.GroupFilter, auth)
	// Discovery QPS throttle: share the same QPS budget as downloads so the
	// whole scan is polite to the repo. qps=0 → no limiter (unlimited).
	if cfg.QPS > 0 {
		browser = browser.WithLimiter(rate.NewLimiter(rate.Limit(cfg.QPS), cfg.QPS))
	}
	// Discovery connection pool: auto = max(concurrency, 32), capped at 128.
	maxConns := cfg.BrowserConcurrency
	if maxConns <= 0 {
		maxConns = cfg.Concurrency
		if maxConns < 32 {
			maxConns = 32
		}
	}
	if maxConns > 128 {
		maxConns = 128
	}
	browser = browser.WithMaxConnsPerHost(cfg.Timeout, maxConns)
```

- [ ] **Step 4: 添加 browser limiter 单元测试**
文件: `internal/repo/browser_test.go`（追加测试）

```go
// TestBrowser_LimiterThrottlesDiscovery 验证 QPS limiter 限制 discovery 请求速率。
// 用一个记录请求时间戳的 mock server + 限制 2 QPS，发 4 个请求应耗时 >= 1s。
func TestBrowser_LimiterThrottlesDiscovery(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		fmt.Fprint(w, `<a href="a/">a/</a>`)
	}))
	defer srv.Close()

	b := NewBrowser(5*time.Second, "")
	// 2 QPS → 4 请求至少跨 1.5s（bucket=2 预存 2，后 2 个每秒 1 个）
	b = b.WithLimiter(rate.NewLimiter(rate.Limit(2), 2))

	for i := 0; i < 4; i++ {
		_, err := b.fetchURL(srv.URL)
		require.NoError(t, err)
	}

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, times, 4)
	// 第 1 个与第 4 个请求间隔应 >= 1s（2 个预存 + 2 个限流）
	elapsed := times[3].Sub(times[0])
	assert.GreaterOrEqual(t, elapsed, 900*time.Millisecond,
		"4 requests at 2 QPS should take ~1s, got %s", elapsed)
}

// TestBrowser_WithMaxConnsPerHost 验证连接池 cap 被应用。
func TestBrowser_WithMaxConnsPerHost(t *testing.T) {
	b := NewBrowser(5*time.Second, "")
	b = b.WithMaxConnsPerHost(5*time.Second, 64)
	tr, ok := b.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 64, tr.MaxConnsPerHost)
}
```

- [ ] **Step 5: 验证 browser 测试**
Run: `go test -run 'TestBrowser_LimiterThrottlesDiscovery|TestBrowser_WithMaxConnsPerHost' -v ./internal/repo/`
Expected:
  - Exit code: 0
  - Output contains: "PASS" for both

- [ ] **Step 6: 提交**
Run: `git add internal/repo/browser.go cmd/scan.go internal/repo/browser_test.go && git commit -m "feat(repo): browser discovery QPS throttle + connection pool scales with concurrency"`

---

### Task 3: Downloader 连接池随配置 + 统一限流入口

**Depends on:** Task 1
**Files:**
- Modify: `internal/repo/downloader.go:36-55`（NewDownloaderWithAuth 用 DownloadConcurrency 算 maxConns）
- Modify: `cmd/scan.go:207`（传 DownloadConcurrency 给 downloader）
- Test: `internal/repo/downloader_test.go`

- [ ] **Step 1: 修改 NewDownloaderWithAuth 以接收 maxConns 参数**
文件: `internal/repo/downloader.go:36-55`（替换 NewDownloader/NewDownloaderWithAuth 签名）

```go
func NewDownloader(timeout time.Duration, retries int, qps int, tempDir string, maxFileSize int64) *Downloader {
	return NewDownloaderWithAuth(timeout, retries, qps, tempDir, maxFileSize, AuthConfig{}, 0)
}

func NewDownloaderWithAuth(timeout time.Duration, retries int, qps int, tempDir string, maxFileSize int64, auth AuthConfig, maxConnsPerHost int) *Downloader {
	var limiter *rate.Limiter
	if qps > 0 {
		limiter = rate.NewLimiter(rate.Limit(qps), qps)
	}
	return &Downloader{
		client:    newHTTPClient(timeout, maxConnsPerHost),
		limiter:   limiter,
		tempDir:   tempDir,
		retries:   retries,
		timeout:   timeout,
		maxFileSize: maxFileSize,
		auth:      auth,
	}
}
```

注：newHTTPClient 内部已有 `if maxConnsPerHost <= 0 { maxConnsPerHost = 32 }`，所以传 0 自动回退 32。

- [ ] **Step 2: 修改 scan.go 装配 downloader 以传 DownloadConcurrency**
文件: `cmd/scan.go:207`（替换 dl 构造行）

```go
	// Download connection pool: auto = max(download-concurrency or concurrency, 64).
	dlMaxConns := cfg.DownloadConcurrency
	if dlMaxConns <= 0 {
		dlMaxConns = cfg.Concurrency
	}
	if dlMaxConns < 64 {
		dlMaxConns = 64
	}
	if dlMaxConns > 128 {
		dlMaxConns = 128
	}
	dl := repo.NewDownloaderWithAuth(cfg.Timeout, cfg.Retries, cfg.QPS, ws.CacheDir, maxBytes, auth, dlMaxConns)
```

- [ ] **Step 3: 修复 downloader_test.go 中所有 NewDownloaderWithAuth 调用**

文件: `internal/repo/downloader_test.go`（所有 `NewDownloaderWithAuth(..., auth)` 调用补第 7 个参数 `0`）

```go
// 旧: dl := NewDownloaderWithAuth(timeout, retries, qps, tmp, max, auth)
// 新: dl := NewDownloaderWithAuth(timeout, retries, qps, tmp, max, auth, 0)
```

执行时 grep 所有 `NewDownloaderWithAuth` 调用点，逐个补 `0` 参数。

- [ ] **Step 4: 验证 downloader 测试**
Run: `go test ./internal/repo/`
Expected:
  - Exit code: 0
  - Output does NOT contain: "FAIL"

- [ ] **Step 5: 提交**
Run: `git add internal/repo/downloader.go cmd/scan.go internal/repo/downloader_test.go && git commit -m "feat(repo): downloader connection pool scales with download-concurrency (was hardcoded 64)"`

---

### Task 4: Classifier 过滤 — sources/pom 选项接入 discovery

**Depends on:** Task 1
**Files:**
- Modify: `internal/repo/parser.go:52-58`（isArtifactFile 不变，新增 isWantedArtifact）
- Modify: `internal/repo/browser.go` discovery 调用点（用 isWantedArtifact 过滤）
- Modify: `internal/repo/browser.go`（Browser 加 IncludeSources/SkipPom 字段 + setter）
- Modify: `cmd/scan.go:206`（调 browser setter）
- Modify: `internal/state/state.go` ConfigSnapshot（加 IncludeSources/SkipPom）
- Test: `internal/repo/parser_test.go`

- [ ] **Step 1: 修改 parser.go 以新增 classifier 过滤函数**
文件: `internal/repo/parser.go:52-58`（isArtifactFile 后追加新函数）

```go
func isArtifactFile(name string) bool {
	return strings.HasSuffix(name, ".jar") ||
		strings.HasSuffix(name, ".war") ||
		strings.HasSuffix(name, ".ear") ||
		strings.HasSuffix(name, ".pom") ||
		strings.HasSuffix(name, ".xml")
}

// isSourcesJar reports whether name is a Maven sources classifier jar
// (artifactId-version-sources.jar). These contain .java source and are the
// main place real secrets leak on Maven Central.
func isSourcesJar(name string) bool {
	return strings.HasSuffix(name, "-sources.jar") || strings.HasSuffix(name, "-javadoc.jar") || strings.HasSuffix(name, "-tests.jar")
}

// isPomFile reports whether name is a .pom or .xml metadata file.
func isPomFile(name string) bool {
	return strings.HasSuffix(name, ".pom") || strings.HasSuffix(name, ".xml")
}

// isWantedArtifact applies the user's classifier filters to a discovered file.
// includeSources=false (default) skips sources/javadoc/tests jars.
// skipPom=true skips .pom/.xml files.
func isWantedArtifact(name string, includeSources, skipPom bool) bool {
	if !isArtifactFile(name) {
		return false
	}
	if isSourcesJar(name) && !includeSources {
		return false
	}
	if isPomFile(name) && skipPom {
		return false
	}
	return true
}
```

- [ ] **Step 2: 修改 Browser 以携带 classifier 选项**
文件: `internal/repo/browser.go`（Browser 结构体加字段 + setter，在 WithLimiter 附近）

```go
type Browser struct {
	client         *http.Client
	groupFilter    string
	auth           AuthConfig
	limiter        *rate.Limiter
	includeSources bool
	skipPom        bool
}

// WithClassifierFilters sets sources/pom discovery filtering.
func (b *Browser) WithClassifierFilters(includeSources, skipPom bool) *Browser {
	b.includeSources = includeSources
	b.skipPom = skipPom
	return b
}
```

- [ ] **Step 3: 修改 browser discovery 调用点以用 isWantedArtifact**
文件: `internal/repo/browser.go:125`（`} else if isArtifactFile(entry.Name) {` 改为带过滤）

```go
		} else if isWantedArtifact(entry.Name, b.includeSources, b.skipPom) {
```

- [ ] **Step 4: 修改 scan.go 装配 browser 以传 classifier 选项**
文件: `cmd/scan.go:206`（browser 构造后加一行）

```go
	browser = browser.WithClassifierFilters(cfg.IncludeSources, cfg.SkipPom)
```

- [ ] **Step 5: 修改 state ConfigSnapshot 以记录 classifier 选项**
文件: `internal/state/state.go`（ConfigSnapshot 结构体加字段）

先 grep ConfigSnapshot 定义位置，加两个字段：

```go
type ConfigSnapshot struct {
	// ...existing fields...
	IncludeSources bool   `json:"include_sources,omitempty"`
	SkipPom        bool   `json:"skip_pom,omitempty"`
}
```

再 grep 构造 ConfigSnapshot 的地方（通常在 scanner 初始化或 runScan），补 `IncludeSources: cfg.IncludeSources, SkipPom: cfg.SkipPom,`。

- [ ] **Step 6: 添加 parser classifier 测试**
文件: `internal/repo/parser_test.go`（追加）

```go
func TestIsWantedArtifact_ClassifierFilters(t *testing.T) {
	tests := []struct {
		name           string
		includeSources bool
		skipPom        bool
		want           bool
	}{
		{"lib-1.0.jar", false, false, true},
		{"lib-1.0-sources.jar", false, false, false},   // sources 默认跳过
		{"lib-1.0-sources.jar", true, false, true},      // 显式开启
		{"lib-1.0-javadoc.jar", false, false, false},
		{"lib-1.0.pom", false, false, true},             // pom 默认扫
		{"lib-1.0.pom", false, true, false},             // skip-pom 跳过
		{"lib-1.0.xml", false, true, false},
		{"lib-1.0.war", false, false, true},
		{"readme.txt", false, false, false},             // 非 artifact
	}
	for _, tt := range tests {
		got := isWantedArtifact(tt.name, tt.includeSources, tt.skipPom)
		assert.Equal(t, tt.want, got, "name=%s includeSources=%v skipPom=%v", tt.name, tt.includeSources, tt.skipPom)
	}
}
```

- [ ] **Step 7: 验证 parser + state 测试**
Run: `go test ./internal/repo/ ./internal/state/`
Expected:
  - Exit code: 0
  - Output contains: "PASS"

- [ ] **Step 8: 提交**
Run: `git add internal/repo/parser.go internal/repo/browser.go cmd/scan.go internal/state/state.go internal/repo/parser_test.go && git commit -m "feat(repo): classifier filtering — --include-sources hunts leaks in .java, --skip-pom speeds up"`

---

### Task 5: Maven Central sources jar 实扫验证 + 报告

**Depends on:** Task 2, Task 3, Task 4
**Files:**
- Create: `tests/integration/maven_central_sources_test.go`
- Create: `docs/reports/2026-07-21-maven-central-sources-scan.md`
- Create: 本地含泄露的 sources jar fixture（测试内构造）

- [ ] **Step 1: 创建本地 sources jar 泄露验证测试 — 验证 --include-sources 命中 .java 泄露**
文件: `tests/integration/maven_central_sources_test.go`

```go
//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leakySourcesJar 构造一个含硬编码泄露的 -sources.jar：
// .java 源码里含 password= 与 ghp_ token，验证 --include-sources 能扫出 .java 泄露。
func leakySourcesJar(t *testing.T) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	javaSrc := []byte(`package com.example;
public class Config {
    // FIXME: remove before release
    private static final String PASSWORD = "HardcodedPassword123!";
    private static final String TOKEN = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789";
}
`)
	fw, err := w.Create("com/example/Config.java")
	require.NoError(t, err)
	_, err = fw.Write(javaSrc)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// startSourcesMockRepo 启动含 leaky-lib-1.0-sources.jar 的仓库。
func startSourcesMockRepo(t *testing.T, sourcesJar []byte) *httptest.Server {
	t.Helper()
	pages := map[string][]byte{
		"/":                                  []byte(`<a href="com/">com/</a>`),
		"/com/":                              []byte(`<a href="example/">example/</a>`),
		"/com/example/":                      []byte(`<a href="leaky/">leaky/</a>`),
		"/com/example/leaky/":                []byte(`<a href="1.0/">1.0/</a>`),
		"/com/example/leaky/1.0/":            []byte(`<a href="leaky-1.0-sources.jar">leaky-1.0-sources.jar</a>`),
		"/com/example/leaky/1.0/leaky-1.0-sources.jar": sourcesJar,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
}

// TestScan_IncludeSources_FindsJavaLeak 验证 --include-sources 扫描 sources jar
// 里的 .java 源码并命中硬编码密码与 GitHub token。
func TestScan_IncludeSources_FindsJavaLeak(t *testing.T) {
	if testBinaryPath == "" {
		t.Skip("test binary not built")
	}
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "sources.json")
	srv := startSourcesMockRepo(t, leakySourcesJar(t))
	defer srv.Close()

	// 不开 --include-sources：sources jar 被 discovery 跳过，0 finding
	cmd1, _ := startScanSubprocess(t, srv.URL, stateFile)
	_ = cmd1.Wait()
	st1 := loadStateJSON(t, stateFile)
	completed1, _ := st1["completed_artifacts"].([]interface{})
	assert.Empty(t, completed1, "sources jar should be skipped without --include-sources")

	// 重置 state，开 --include-sources 重扫：应扫到 sources jar 并命中泄露
	os.Remove(stateFile)
	os.Remove(stateFile + ".bak")
	cmd2, buf := startScanSubprocess(t, srv.URL, stateFile, "--include-sources", "--rules-level", "all")
	done := make(chan error)
	go func() { done <- cmd2.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cmd2.Process.Kill()
		t.Fatal("include-sources scan did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	completed2, _ := st2["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed2, "sources jar should be scanned with --include-sources")
	// 输出应含命中的泄露（password / ghp_ token）
	out := buf.String()
	assert.True(t,
		strings.Contains(out, "HardcodedPassword123") || strings.Contains(out, "hardcoded-password") || strings.Contains(out, "ghp_"),
		"include-sources scan should find the hardcoded leak in .java, got output: %s", out)
}
```

注：需补 `"strings"` import。验证测试用真实 binary，命中后输出会含 rule id 或泄露片段。`strings.Contains` 替代手写字符串查找。

- [ ] **Step 2: 验证本地 sources jar 泄露测试**
Run: `go test -tags integration -run TestScan_IncludeSources_FindsJavaLeak -v -timeout 60s ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "PASS"
  - 不开 include-sources 时 completed 为空；开启后非空且命中泄露

- [ ] **Step 3: Maven Central 真实 sources jar 实扫**

用 CLI 直接扫 Maven Central 上一个含 sources jar 的 groupId（选小而可能有泄露的，如 `com.alibaba.fastjson` 早期版本或第三方）。先跑一次 `--include-sources` 小范围验证流程：

Run: `go run ./cmd/mvn-repo-scanner scan --repo https://repo.maven.apache.org/maven2 --group com.alibaba --include-sources --rules-level all --download-concurrency 8 --qps 10 --timeout 30s --state-file /tmp/mc-sources.json --verbose 2>&1 | head -50`
Expected:
  - 命令执行完成（或 Ctrl+C 后 state 可 resume）
  - 输出含 discovered/scanned 统计或 finding

注：Maven Central 真实扫描可能耗时长/结果为 0。如实记录：若扫到 finding 写入报告；若 0 finding，报告说明 sources jar 也干净 + 用本地测试 jar 证明能力。

- [ ] **Step 4: 编写验证报告**
文件: `docs/reports/2026-07-21-maven-central-sources-scan.md`

```markdown
# Maven Central Sources Jar 扫描验证报告 (2026-07-21)

## 目标
验证 --include-sources 选项能在 Maven Central 上扫出真实泄露（.java 源码中的硬编码凭证）。

## 能力验证（本地 sources jar）
构造含 `password="HardcodedPassword123!"` 与 `ghp_...` token 的 -sources.jar，
- 不开 --include-sources：sources jar 被 discovery 跳过，0 finding ✅
- 开 --include-sources：sources jar 被扫描，.java 源码命中 hardcoded-password + github-token ✅

## Maven Central 实扫
（执行 Step 3 后如实填写：groupId / discovered / scanned / findings 数 / 命中规则）

## 结论
- 断点续跑：discovery 阶段 QPS 限流防 429，连接池随 concurrency 调整
- 并发：browser/downloader 连接池从硬编码改为随配置（auto max(concurrency, 32/64), 上限 128）
- 扫出泄露：--include-sources 是 Maven Central 上扫出真实泄露的关键开关（.java 源码）
```

注：执行时根据 Step 3 实际结果填写实扫数据，不编造。

- [ ] **Step 5: 提交**
Run: `git add tests/integration/maven_central_sources_test.go docs/reports/2026-07-21-maven-central-sources-scan.md && git commit -m "test(integration,docs): --include-sources finds .java leaks + Maven Central sources scan report"`

---

### Task 6: 全量验证 + push

**Depends on:** Task 5
**Files:**
- None（仅运行验证）

- [ ] **Step 1: 跑全仓库单测 + race**
Run: `go test -race -timeout 400s ./...`
Expected:
  - Exit code: 0
  - 无 race warning / FAIL

- [ ] **Step 2: 跑全部 integration 套件（含新 sources 测试 + 旧 10 kill 场景）**
Run: `go test -tags integration -timeout 400s ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "PASS"
  - 11 个 kill/sources 场景全过

- [ ] **Step 3: 推送**
Run: `git push origin main`
Expected:
  - Exit code: 0
  - 推送成功
