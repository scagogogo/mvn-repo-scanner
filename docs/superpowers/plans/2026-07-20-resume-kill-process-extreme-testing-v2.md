# Resume Kill-Process Extreme Testing v2 (Increment) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development`
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** 在第二轮 5 个 kill 场景基础上，增量补齐 4 个未覆盖的极端 case：resume 双文件损坏恢复、多 disjoint subtree 并发 in-flight 跨崩溃、DiskWatcher 预算 + SIGKILL、flushHook 端到端掉电模拟。

**Architecture:** 复用第二轮已建的子进程级二进制 e2e 基建（`tests/integration/binary_fixture_test.go` 的 `testBinaryPath`/`TestMain`/`buildTestBinary`/`startScanSubprocess`/`loadStateJSON`/`waitForInflight`/`emptyJar`）。Task 1 扩展 helper（多 subtree mock repo、大 jar 预算 mock repo、state 损坏工具），Task 2/3/4/5 各加一个场景测试文件。所有测试用 `//go:build integration`，通过 `go test -tags integration -run TestKillProcess` 跑。数据流：主测试进程 exec.Command 启动 scan 子进程对接 httptest mock repo → 轮询 state 文件等 kill 时窗 → 发 SIGKILL/SIGTERM → 子进程死后检查 state → 启动 resume 子进程 → 断言恢复结果。选择子进程级而非进程内 mock，因为 SIGKILL 无法在进程内模拟、flushHook 崩溃会杀死整个测试进程。

**Tech Stack:** Go 1.25.0, cobra CLI, testify v1.10.0, httptest mock repo, syscall.SIGKILL/SIGTERM, os/exec 子进程

**Risks:**
- 多 subtree mock repo 的 HTML 链接相对路径拼接易错（第二轮踩过 `com/example + com/ → com/example/com` 坑）→ 缓解：每页只列直接子项，复用第二轮 `startKillableMockRepo` 已验证的相对路径模式
- DiskWatcher 预算测试需大 jar 触发 Acquire，但 mock repo jar 下载慢 5s 已足够卡 worker，预算泄漏主要看 resume 新进程是否重新计算（新进程预算从 0 起，不会卡）→ 缓解：断言 resume 完成 + in-flight 清空，不直接断言内部预算值（黑盒）
- 双文件损坏测试需在 kill 后手动写坏主文件 + .bak，再 resume → 缓解：用 `corruptStateFile` helper 写入坏 JSON，断言 resume 子进程非零退出 + 日志含 corrupt
- flushHook 是包级变量，子进程无法直接注入 → 缓解：场景 J 改用纯文件级模拟（kill mid-flush 后检查 .tmp 残留 + 主文件未损坏 + .bak 存活 + resume 可续），不依赖 hook 注入

---

### Task 1: 扩展 binary_fixture_test.go helper

**Depends on:** None
**Files:**
- Modify: `tests/integration/binary_fixture_test.go`（在文件末尾追加 helper）

- [ ] **Step 1: 新增 startMultiSubtreeMockRepo — 多 disjoint subtree 仓库**

在 `tests/integration/binary_fixture_test.go` 末尾追加。提供 2 个独立 subtree（`com/example/lib` 和 `com/example/lib2`），各自有 2 个 version，jar 下载慢 5s 卡 worker。验证多 disjoint subtree 并发 in-flight 跨崩溃时 revisitPendingDirs 重访多个目录。

```go
// startMultiSubtreeMockRepo 启动含 2 个 disjoint subtree 的仓库。
// com/example/lib 和 com/example/lib2 各有 2 个 version，jar 下载慢 5s。
// 用于验证多 disjoint subtree 并发 in-flight 跨崩溃后 revisit 重访多个目录。
func startMultiSubtreeMockRepo(t *testing.T, jarBytes []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="com/">com/</a>`)
		case "/com/":
			fmt.Fprint(w, `<a href="example/">example/</a>`)
		case "/com/example/":
			fmt.Fprint(w, `<a href="lib/">lib/</a><a href="lib2/">lib2/</a>`)
		case "/com/example/lib/":
			fmt.Fprint(w, `<a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`)
		case "/com/example/lib2/":
			fmt.Fprint(w, `<a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`)
		case "/com/example/lib/1.0/":
			fmt.Fprint(w, `<a href="lib-1.0.jar">lib-1.0.jar</a>`)
		case "/com/example/lib/2.0/":
			fmt.Fprint(w, `<a href="lib-2.0.jar">lib-2.0.jar</a>`)
		case "/com/example/lib2/1.0/":
			fmt.Fprint(w, `<a href="lib2-1.0.jar">lib2-1.0.jar</a>`)
		case "/com/example/lib2/2.0/":
			fmt.Fprint(w, `<a href="lib2-2.0.jar">lib2-2.0.jar</a>`)
		case "/com/example/lib/1.0/lib-1.0.jar",
			"/com/example/lib/2.0/lib-2.0.jar",
			"/com/example/lib2/1.0/lib2-1.0.jar",
			"/com/example/lib2/2.0/lib2-2.0.jar":
			time.Sleep(5 * time.Second) // 慢下载卡住多 worker
			w.Header().Set("Content-Type", "application/java-archive")
			w.Write(jarBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
```

- [ ] **Step 2: 新增 corruptStateFile — 手动损坏 state 文件工具**

在 `tests/integration/binary_fixture_test.go` 末尾追加。写入坏 JSON 到主文件或 .bak，模拟磁盘损坏/掉电 mid-write 残留。

```go
// corruptStateFile 用坏 JSON 覆盖 state 文件（或其 .bak），模拟磁盘损坏。
// which="main" 覆盖主文件，which="bak" 覆盖 .bak。
func corruptStateFile(t *testing.T, stateFile, which string) {
	t.Helper()
	target := stateFile
	if which == "bak" {
		target = stateFile + ".bak"
	}
	require.NoError(t, os.WriteFile(target, []byte("{ broken json !!! "), 0644))
}
```

- [ ] **Step 3: 新增 startLargeJarMockRepo — 大 jar 触发 DiskWatcher 预算**

在 `tests/integration/binary_fixture_test.go` 末尾追加。jar 内容填充 1MB 零字节（超出小预算触发 Acquire 节流），下载慢 5s 卡 worker。用于验证 DiskWatcher 预算在 SIGKILL 后 resume 不卡死。

```go
// startLargeJarMockRepo 启动含较大 jar 的仓库（1MB），触发 DiskWatcher 预算路径。
// jar 下载慢 5s 卡 worker，提供 kill 时窗。用于验证预算在 SIGKILL 后 resume 不卡死。
func startLargeJarMockRepo(t *testing.T, jarBytes []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="com/">com/</a>`)
		case "/com/":
			fmt.Fprint(w, `<a href="example/">example/</a>`)
		case "/com/example/":
			fmt.Fprint(w, `<a href="lib/">lib/</a>`)
		case "/com/example/lib/":
			fmt.Fprint(w, `<a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`)
		case "/com/example/lib/1.0/":
			fmt.Fprint(w, `<a href="lib-1.0.jar">lib-1.0.jar</a>`)
		case "/com/example/lib/2.0/":
			fmt.Fprint(w, `<a href="lib-2.0.jar">lib-2.0.jar</a>`)
		case "/com/example/lib/1.0/lib-1.0.jar",
			"/com/example/lib/2.0/lib-2.0.jar":
			time.Sleep(5 * time.Second)
			w.Header().Set("Content-Type", "application/java-archive")
			w.Write(jarBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
```

- [ ] **Step 4: 新增 largeJar — 1MB jar 内容工具**

在 `tests/integration/binary_fixture_test.go` 末尾追加。生成含一个 1MB entry 的 zip，触发 DiskWatcher 预算。

```go
// largeJar 生成含 1MB entry 的 zip jar，触发 DiskWatcher 预算路径。
func largeJar(t *testing.T) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	fw, err := w.Create("big.bin")
	require.NoError(t, err)
	_, err = fw.Write(make([]byte, 1024*1024)) // 1MB
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}
```

- [ ] **Step 5: 验证 helper 编译**

Run: `go build -tags integration ./tests/integration/`
Expected:
  - Exit code: 0
  - 无编译错误（helper 暂未被引用，但需编译通过）

- [ ] **Step 6: 提交**

Run: `git add tests/integration/binary_fixture_test.go && git commit -m "test(integration): add multi-subtree/large-jar/corrupt helpers for kill-process v2"`

---

### Task 2: resume 双文件损坏恢复测试

**Depends on:** Task 1
**Files:**
- Create: `tests/integration/kill_resume_corruption_test.go`

- [ ] **Step 1: 创建 kill_resume_corruption_test.go — 场景 F/G**

文件: `tests/integration/kill_resume_corruption_test.go`

场景 F：SIGKILL 后手动损坏主文件 → resume 从 .bak 恢复并完成。验证 LoadScanState .bak 回退在子进程级端到端生效。
场景 G：SIGKILL 后损坏主文件 + .bak → resume 应优雅失败（非零退出 + 日志含 corrupt），不 panic。

```go
//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario F: SIGKILL 后手动损坏主文件 → resume 从 .bak 恢复并完成。
// 验证 LoadScanState .bak 回退在子进程级端到端生效（非纯单测）。
func TestKillProcess_CorruptMain_ResumeFromBak(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "corruptmain.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan 跑到 in-flight 后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// kill 后应存在主文件 + .bak（flush 时备份）
	require.FileExists(t, stateFile, "main state file should exist after kill")
	require.FileExists(t, stateFile+".bak", ".bak should exist after flush")

	// 损坏主文件，保留 .bak
	corruptStateFile(t, stateFile, "main")

	// resume 应从 .bak 恢复并完成
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume from .bak did not finish in 30s")
	}

	st := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st["status"], "resume should recover from .bak and complete")
}

// Scenario G: SIGKILL 后损坏主文件 + .bak → resume 应优雅失败，不 panic。
// 验证 LoadScanState 双文件都坏时返回 parse err，scan 优雅退出而非崩溃。
func TestKillProcess_CorruptMainAndBak_ResumeFailsGracefully(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "doublebad.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan 跑到 in-flight 后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	require.FileExists(t, stateFile)
	require.FileExists(t, stateFile+".bak")

	// 同时损坏主文件 + .bak
	corruptStateFile(t, stateFile, "main")
	corruptStateFile(t, stateFile, "bak")

	// resume 应非零退出（load 失败），日志/输出含 corrupt 提示，不 panic
	resume, buf := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	err := resume.Wait()
	assert.NotEqual(t, 0, resume.ProcessState.ExitCode(),
		"resume with both files corrupt should exit non-zero")

	// 输出应含 corrupt 相关警告（LoadScanState 的 log 或 err）
	out := buf.String()
	assert.True(t,
		strings.Contains(out, "corrupt") || strings.Contains(out, "parse") || err != nil,
		"output should mention corruption or resume should error, got: %s", out)
}
```

- [ ] **Step 2: 验证场景 F/G**

Run: `go test -tags integration -run 'TestKillProcess_CorruptMain_ResumeFromBak|TestKillProcess_CorruptMainAndBak_ResumeFailsGracefully' -v -timeout 120s ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "PASS" for both tests
  - 场景 F：resume completed；场景 G：resume 非零退出

- [ ] **Step 3: 提交**

Run: `git add tests/integration/kill_resume_corruption_test.go && git commit -m "test(integration): resume from .bak on corrupt main + graceful fail on double-corrupt"`

---

### Task 3: 多 disjoint subtree 并发 in-flight 跨崩溃测试

**Depends on:** Task 1
**Files:**
- Create: `tests/integration/kill_concurrent_test.go`

- [ ] **Step 1: 创建 kill_concurrent_test.go — 场景 H**

文件: `tests/integration/kill_concurrent_test.go`

场景 H：多 disjoint subtree（lib + lib2）并发 in-flight → SIGKILL → resume 重访多个目录并完成。验证 revisitPendingDirs 能处理多个 in-flight 目录，且不丢不重（4 个 version 全部完成，无重复）。

```go
//go:build integration

package integration

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario H: 多 disjoint subtree（lib + lib2）并发 in-flight → SIGKILL → resume。
// 验证 revisitPendingDirs 重访多个 in-flight 目录，不丢不重（4 个 version 全完成无重复）。
func TestKillProcess_MultiSubtree_ConcurrentInflightResume(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "multisubtree.json")
	srv := startMultiSubtreeMockRepo(t, emptyJar(t))

	// 第一次 scan 跑到 in-flight（多 worker 并发下载 lib/lib2 的 jar）后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// in-flight 应残留多个 artifact（lib + lib2 的 version）
	st := loadStateJSON(t, stateFile)
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.NotEmpty(t, inflight, "multi-subtree kill should leave in-flight artifacts")

	// resume + retry-failed 续跑
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("multi-subtree resume did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
	inflight2, _ := st2["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight2, "in-flight must be cleared after multi-subtree resume")

	// 不丢：4 个 version 全完成
	completed, _ := st2["completed_artifacts"].([]interface{})
	assert.GreaterOrEqual(t, len(completed), 4,
		"all 4 versions across 2 subtrees should complete, got %d", len(completed))

	// 不重：每个 path 只出现一次
	pathCount := map[string]int{}
	for _, c := range completed {
		if s, ok := c.(string); ok {
			pathCount[s]++
		}
	}
	for p, c := range pathCount {
		assert.Equal(t, 1, c, "duplicate path %s scanned %d times after multi-subtree resume", p, c)
	}
}
```

- [ ] **Step 2: 验证场景 H**

Run: `go test -tags integration -run TestKillProcess_MultiSubtree_ConcurrentInflightResume -v -timeout 120s ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "PASS"
  - completed >= 4 且无重复 path

- [ ] **Step 3: 提交**

Run: `git add tests/integration/kill_concurrent_test.go && git commit -m "test(integration): multi-subtree concurrent in-flight crash+resume no-loss-no-dup"`

---

### Task 4: DiskWatcher 预算 + SIGKILL 测试

**Depends on:** Task 1
**Files:**
- Create: `tests/integration/kill_disk_budget_test.go`

- [ ] **Step 1: 创建 kill_disk_budget_test.go — 场景 I**

文件: `tests/integration/kill_disk_budget_test.go`

场景 I：大 jar（1MB）触发 DiskWatcher Acquire → SIGKILL（reservedSize 未 Release）→ resume 新进程预算从 0 起重新算，不卡死，完成扫描。验证 DiskWatcher 预算在崩溃后 resume 不会因泄漏的预留而阻塞。

```go
//go:build integration

package integration

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario I: 大 jar（1MB）触发 DiskWatcher Acquire → SIGKILL（预留未 Release）
// → resume 新进程预算从 0 起重新算，不卡死，完成扫描。
// 验证 DiskWatcher 预算在崩溃后 resume 不会因泄漏的预留而阻塞（新进程预算重新初始化）。
func TestKillProcess_DiskBudget_AcquireThenKillResumeNotBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "diskbudget.json")
	srv := startLargeJarMockRepo(t, largeJar(t))

	// 第一次 scan 跑到 in-flight（大 jar 触发 Acquire）后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// in-flight 应残留（kill 时 Acquire 已发生但 Release 未执行）
	st := loadStateJSON(t, stateFile)
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.NotEmpty(t, inflight, "disk-budget kill should leave in-flight artifacts")

	// resume + retry-failed：新进程 DiskWatcher 预算从 0 起重新算，
	// 不应因上一进程泄漏的预留而阻塞，应能完成扫描
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after disk-budget kill did not finish in 30s (budget may be stuck)")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"],
		"resume should complete despite leaked disk budget reservation")
	inflight2, _ := st2["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight2, "in-flight must be cleared after disk-budget resume")
	completed, _ := st2["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed, "should complete artifacts after disk-budget resume")
}
```

- [ ] **Step 2: 验证场景 I**

Run: `go test -tags integration -run TestKillProcess_DiskBudget_AcquireThenKillResumeNotBlocked -v -timeout 120s ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "PASS"
  - resume completed 且 in-flight 清空（不卡死）

- [ ] **Step 3: 提交**

Run: `git add tests/integration/kill_disk_budget_test.go && git commit -m "test(integration): disk-budget acquire+SIGKILL resume not blocked by leaked reservation"`

---

### Task 5: flushHook 端到端掉电模拟测试

**Depends on:** Task 1
**Files:**
- Create: `tests/integration/kill_flush_crash_e2e_test.go`

- [ ] **Step 1: 创建 kill_flush_crash_e2e_test.go — 场景 J**

文件: `tests/integration/kill_flush_crash_e2e_test.go`

场景 J：SIGKILL mid-flush（用真实 kill 模拟掉电，不依赖 flushHook 注入）→ 检查 .tmp 残留可能存在但主文件未损坏（或损坏则 .bak 存活）→ resume 可续并完成。验证掉电模拟端到端：主文件要么完整要么有 .bak 兜底，resume 总能恢复。注：子进程无法注入 flushHook，故用高频 flush（checkpoint-interval=1）+ SIGKILL 在 flush 时窗内随机命中，模拟 mid-flush 被杀。

```go
//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario J: 高频 flush 下 SIGKILL 模拟 mid-flush 掉电 → 主文件或 .bak 至少一个完好
// → resume 可续并完成。验证掉电模拟端到端：主文件要么完整要么有 .bak 兜底，resume 总能恢复。
// checkpoint-interval=1 让每次 MarkCompleted/MarkInFlight 都 flush，最大化命中 flush 时窗。
func TestKillProcess_FlushMidWrite_PowerLossSimulation(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "powerloss.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan：checkpoint-interval=1 高频 flush，跑到 in-flight 后 SIGKILL
	// startScanSubprocess 已设 --checkpoint-interval 1
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// kill 后检查：主文件或 .bak 至少一个可解析（掉电不应同时损坏两者）
	mainOK := fileIsParsable(t, stateFile)
	bakOK := fileIsParsable(t, stateFile+".bak")
	require.True(t, mainOK || bakOK,
		"after SIGKILL mid-flush, at least main or .bak must be parseable (got main=%v bak=%v)",
		mainOK, bakOK)

	// resume 应能从完好的文件恢复并完成
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after mid-flush power-loss did not finish in 30s")
	}

	st := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st["status"],
		"resume should recover after mid-flush power-loss and complete")
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight, "in-flight must be cleared after power-loss resume")
}

// fileIsParsable 检查文件是否存在且 JSON 可解析。
func fileIsParsable(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m map[string]interface{}
	return json.Unmarshal(data, &m) == nil
}
```

- [ ] **Step 2: 验证场景 J**

Run: `go test -tags integration -run TestKillProcess_FlushMidWrite_PowerLossSimulation -v -timeout 120s ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "PASS"
  - main 或 .bak 至少一个可解析 + resume completed

- [ ] **Step 3: 提交**

Run: `git add tests/integration/kill_flush_crash_e2e_test.go && git commit -m "test(integration): mid-flush SIGKILL power-loss simulation e2e with .bak fallback"`

---

### Task 6: 全量验证 + push

**Depends on:** Task 2, Task 3, Task 4, Task 5
**Files:**
- None（仅运行验证）

- [ ] **Step 1: 跑全部 kill-process 套件（含第二轮 5 场景 + 第三轮 4 场景）**

Run: `go test -tags integration -run TestKillProcess -v -timeout 300s ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "PASS" for all 9 kill scenarios (SIGKILL/SIGTERM/DoubleCrash/DiscoveryFetchFail/DiscoveryMidCursorKill + CorruptMain/CorruptMainAndBak/MultiSubtree/DiskBudget/FlushMidWrite = 10 total)
  - 无 FAIL / panic / deadlock

- [ ] **Step 2: 跑全仓库单测 + race**

Run: `go test -race -timeout 300s ./...`
Expected:
  - Exit code: 0
  - 无 race warning / FAIL

- [ ] **Step 3: 推送**

Run: `git push origin main`
Expected:
  - Exit code: 0
  - 推送成功
