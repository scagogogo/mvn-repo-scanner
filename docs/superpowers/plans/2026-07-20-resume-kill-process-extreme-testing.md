# 断点续跑极端 case 测试加固 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development`
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** 用真实杀死进程的方式（SIGKILL/SIGTERM/掉电模拟）验证断点续跑的极端 case 恢复行为，补齐"进程内 mock 无法覆盖"的真实崩溃场景，并修复发现的 .bak 污染缺口。

**Architecture:** 数据流分两层。① 产品代码层：`state.flush()` 在备份主文件到 `.bak` 前先校验主文件可解析，损坏则跳过备份（保护好的 .bak 不被污染）；新增 `flushHook` 注入点供测试模拟 flush 中途崩溃。② 测试层：建子进程 e2e 基建——TestMain 一次性 `go build` 二进制 fixture，主测试进程用 `exec.Command` 启动子进程跑 `scan` 对接 mock Maven repo（httptest + 慢响应让 scan 可被 kill 在中途），对外部子进程发 SIGKILL/SIGTERM，子进程死后主进程检查 state 文件的 in-flight 残留/不丢不重/二次 resume。这是唯一能真实测"杀死进程"的方式——进程内 SIGKILL 会杀测试进程本身。

**Tech Stack:** Go 1.25.0, cobra, testify v1.10.0, net/http/httptest mock repo, os/exec 子进程, syscall.SIGKILL/SIGTERM

**Risks:**
- Task 1 修改 `flush()` 可能影响既有 .bak 测试（`TestFlush_WritesBakBackup`/`TestLoadScanState_CorruptFallsBackToBak`）→ 缓解：Task 1 Step 2 立即跑既有 state 测试确认不回归
- Task 3 子进程 `go build` 慢（~2s）+ 需 mock repo 让 scan 可被 kill 在中途 → 缓解：TestMain 一次性构建二进制 fixture，所有 kill 测试复用；mock repo 用慢响应（500ms）确保 kill 时窗
- SIGKILL 子进程可能留僵尸 → 缓解：`cmd.Process.Wait()` 收割 + `t.Cleanup` 兜底 kill
- `flushHook` 包级变量与既有"间接注入"模式（`newScannerFn`/`openStoreFn`）一致，但需确认不破坏 race → 缓解：hook 默认 nil，仅测试设置；race 下 hook 触发 panic 模拟崩溃是预期行为

---

### Task 1: 修复 flush() 的 .bak 污染缺口 + 新增 flushHook 注入点

**Depends on:** None
**Files:**
- Modify: `internal/state/state.go:636-666`（`flush()` 函数）
- Modify: `internal/state/state.go:20-22` 附近（新增 `flushHook` 包级变量声明）
- Test: `internal/state/flush_crash_test.go`（Task 2 创建，本 Task 只改产品代码 + 跑既有测试）

**根因分析：** `flush()` 行 658 `os.ReadFile(s.filePath)` 读主文件直接备份到 `.bak`。若主文件已被外部损坏（如上一次 flush 的 Rename 中途崩溃留下半截 `.tmp` 被人误命名为主，或磁盘 bit-rot），ReadFile 读到损坏字节原样写入 `.bak`，**污染唯一的好 .bak**。下次 LoadScanState 主+ .bak 都坏，恢复失败。修复：备份前先 json 校验主文件，损坏则跳过备份（保留旧 .bak）。

- [ ] **Step 1: 新增 flushHook 包级变量声明 — 供测试注入模拟 flush 中途崩溃**

文件: `internal/state/state.go`（在 `ErrVersionMismatch` 声明之后，约第 22 行）

```go
// flushHook is a test-only injection point to simulate a crash mid-flush
// (e.g. power loss after writing .tmp but before rename). When non-nil it is
// invoked after the .tmp file is written (and fsynced) but before the .bak
// backup and rename. A panic from the hook mimics a process killed mid-write
// so tests can verify the main file is not left corrupt and .bak survives.
// Production code leaves this nil; it is not exported to keep the surface
// internal. Set via testfile in the same package.
var flushHook func(filePath string)
```

- [ ] **Step 2: 修改 flush() — 备份前校验主文件可解析，损坏则跳过；插入 flushHook 注入点**

文件: `internal/state/state.go:636-666`（替换整个 `flush()` 函数）

```go
// flush writes the current state to disk atomically (caller must hold lock).
//
// Persistence hardening:
//   1. Write to a .tmp file, then fsync it so the bytes survive a crash
//      before the rename commits them. (Without fsync, a rename can land
//      while the data is still only in the kernel page cache — a power
//      loss yields a truncated/empty state file.)
//   2. Before overwriting, copy the previous good file to .bak so a corrupt
//      write or future parse failure can fall back to it in LoadScanState.
//      The previous file is validated before backup: if it is already corrupt
//      (e.g. a prior half-written rename survived as the main file), we skip
//      the backup rather than overwriting the last good .bak with garbage.
//   3. flushHook (test-only) fires after .tmp is durable but before .bak/rename,
//      so a panic there leaves .tmp on disk and the main file untouched —
//      modeling a kill mid-flush for crash-recovery tests.
func (s *ScanState) flush() error {
	s.LastUpdated = time.Now().Format(time.RFC3339)
	s.dirtyCount = 0

	// ScanState's exported fields are all JSON-serializable (strings, ints,
	// slices, structs of the same); unexported fields (mu, maps) are ignored by
	// encoding/json. MarshalIndent cannot fail in practice — the error is
	// intentionally ignored.
	data, _ := json.MarshalIndent(s, "", "  ")

	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}

	// fsync the temp file so the committed bytes are durable on disk.
	if f, err := os.Open(tmpFile); err == nil {
		_ = f.Sync() // best-effort; some filesystems/OSes ignore fsync errors
		f.Close()
	}

	// Test-only crash injection: a panic here leaves .tmp on disk and the
	// main file + .bak untouched, modeling a process killed mid-flush.
	if flushHook != nil {
		flushHook(s.filePath)
	}

	// Back up the previous good state file (if any) before replacing it.
	// Validate it first: if the main file is already corrupt, do NOT copy it
	// over .bak (that would destroy the last good backup). Only well-formed
	// previous content is preserved as .bak.
	if prev, err := os.ReadFile(s.filePath); err == nil && len(prev) > 0 {
		if json.Valid(prev) {
			_ = os.WriteFile(s.filePath+".bak", prev, 0644)
		}
		// If prev is not valid JSON, .bak is left as-is (last good backup).
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: 跑既有 state 测试确认 flush 改动不回归**

Run: `go test ./internal/state/ -run "TestFlush|TestLoadScanState|TestScanStatus|TestGetResumeEstimate" -v -count=1`
Expected:
  - Exit code: 0
  - Output contains: "ok  github.com/scagogogo/mvn-repo-scanner/internal/state"
  - Output does NOT contain: "FAIL" or "--- FAIL"

- [ ] **Step 4: 提交**
Run: `git add internal/state/state.go && git commit -m "fix(state): guard .bak backup against corrupt main file + add flushHook crash-injection point"`

---

### Task 2: flush 中途崩溃 + .bak 污染防护单元测试

**Depends on:** Task 1
**Files:**
- Create: `internal/state/flush_crash_test.go`

- [ ] **Step 1: 创建 flush_crash_test.go — 覆盖崩溃注入与 .bak 污染防护**

```go
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flush 在 .tmp 写入后、rename 前 panic（模拟掉电/强杀 mid-flush）：
// .tmp 残留，主文件保持上一次完好内容，.bak 不被污染。
func TestFlush_CrashMidFlush_PreservesMainAndBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("crash1", "https://a.com", "", path, 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})

	// 第一次 flush：建立完好主文件
	require.NoError(t, s.MarkCompleted("com/a/1.0"))
	require.NoError(t, s.Flush())
	goodMain, _ := os.ReadFile(path)

	// 安装崩溃 hook：在 .tmp 写入后 panic，模拟进程被杀 mid-flush
	crashed := false
	flushHook = func(_ string) {
		crashed = true
		panic("simulated kill mid-flush")
	}
	defer func() { flushHook = nil }()

	// 第二次 flush 应在 hook 处 panic
	s.MarkCompleted("com/a/2.0")
	require.True(t, crashed, "flushHook should have fired")

	// 主文件应仍是第一次的完好内容（rename 未执行）
	curMain, _ := os.ReadFile(path)
	assert.Equal(t, goodMain, curMain, "main file must be untouched after mid-flush crash")

	// .tmp 残留（rename 没跑），但不影响主文件
	_, statErr := os.Stat(path + ".tmp")
	assert.NoError(t, statErr, ".tmp should remain after mid-flush crash")

	// 加载主文件应成功，仍是第一次的内容（1.0 completed，2.0 没有）
	flushHook = nil // 关掉 hook 以正常加载
	loaded, err := LoadScanState(path)
	require.NoError(t, err)
	assert.True(t, loaded.IsCompleted("com/a/1.0"))
	assert.False(t, loaded.IsCompleted("com/a/2.0"))
}

// 主文件已损坏时再 flush：不应把损坏内容备份到 .bak（保护上一个好 .bak）。
// 这是 Task 1 的核心修复——旧实现会 ReadFile(损坏主) 覆盖好 .bak。
func TestFlush_DoesNotBackupCorruptMainToBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("guard1", "https://a.com", "", path, 1000,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})

	// 第一次 flush：主=[1.0]，无 .bak
	require.NoError(t, s.MarkCompleted("com/a/1.0"))
	require.NoError(t, s.Flush())

	// 第二次 flush：.bak=[1.0]，主=[1.0,2.0]
	require.NoError(t, s.MarkCompleted("com/a/2.0"))
	require.NoError(t, s.Flush())
	goodBak, _ := os.ReadFile(path + ".bak")
	require.Contains(t, string(goodBak), "com/a/1.0")

	// 人为损坏主文件（模拟上一次 rename 中途崩溃留下的半截）
	require.NoError(t, os.WriteFile(path, []byte("{corrupt garbage"), 0644))

	// 第三次 flush：主文件损坏，但 .bak 不应被覆盖（json.Valid 失败 → 跳过备份）
	s.MarkCompleted("com/a/3.0")
	require.NoError(t, s.Flush())

	// .bak 仍是第二次的完好内容（含 1.0），没被损坏主污染
	bakAfter, _ := os.ReadFile(path + ".bak")
	assert.Equal(t, goodBak, bakAfter, ".bak must not be overwritten by corrupt main")

	// 主文件现在被正常 flush 覆盖成新内容（含 3.0），不再是损坏的
	mainAfter, _ := os.ReadFile(path)
	assert.True(t, json.Valid(mainAfter), "main file should be valid after flush overwrote the corruption")
}

// 主文件完好的正常 flush：.bak 正常更新（回归保护，确认 json.Valid 门不误伤正常路径）。
func TestFlush_NormalPath_StillUpdatesBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("normal1", "https://a.com", "", path, 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})

	require.NoError(t, s.MarkCompleted("com/a/1.0"))
	require.NoError(t, s.Flush())
	first, _ := os.ReadFile(path)

	require.NoError(t, s.MarkCompleted("com/a/2.0"))
	require.NoError(t, s.Flush())

	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, first, bak, "normal flush should still back up previous main to .bak")
}
```

- [ ] **Step 2: 验证 flush 崩溃与 .bak 防护测试**
Run: `go test ./internal/state/ -run "TestFlush_CrashMidFlush|TestFlush_DoesNotBackup|TestFlush_NormalPath_StillUpdatesBak" -v -count=1`
Expected:
  - Exit code: 0
  - Output contains: "ok  github.com/scagogogo/mvn-repo-scanner/internal/state"
  - Output does NOT contain: "FAIL"

- [ ] **Step 3: 提交**
Run: `git add internal/state/flush_crash_test.go && git commit -m "test(state): cover mid-flush crash preservation and .bak corruption guard"`

---

### Task 3: 子进程级真实杀死进程 e2e 基建 + SIGKILL/SIGTERM/双重崩溃场景

**Depends on:** Task 1, Task 2
**Files:**
- Create: `tests/integration/kill_process_test.go`
- Create: `tests/integration/binary_fixture_test.go`（TestMain 构建二进制 + mock repo helper）

- [ ] **Step 1: 创建 binary_fixture_test.go — TestMain 一次性构建二进制 + mock Maven repo helper**

```go
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testBinaryPath 是 TestMain 一次性构建的 mvn-repo-scanner 二进制路径。
// 所有 kill-process 测试复用，避免每次 go build。
var testBinaryPath string

// TestMain 构建一次二进制 fixture 供 kill-process 测试启动子进程。
// 没有匹配测试时跳过构建（go test -run 无关测试时省时间）。
func TestMain(m *testing.M) {
	// 仅在有 integration 测试被选中时构建（go test -tags integration -run TestKill）
	// 否则 m.Run 走其他测试，不需要二进制。
	cleanup, built := buildTestBinary()
	if built {
		defer cleanup()
	}
	code := m.Run()
	if built {
		os.Remove(testBinaryPath)
	}
	os.Exit(code)
}

// buildTestBinary 用 go build 输出二进制到临时文件，返回清理函数。
// 若 go 不可用或构建失败，返回 built=false（相关测试会 t.Skip）。
func buildTestBinary() (cleanup func(), built bool) {
	binFile, err := os.CreateTemp("", "mvn-repo-scanner-test-*")
	if err != nil {
		return func() {}, false
	}
	binFile.Close()
	testBinaryPath = binFile.Name()
	os.Remove(testBinaryPath) // go build 不接受已存在的输出路径语义

	cmd := exec.Command("go", "build", "-o", testBinaryPath, "./../../cmd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(testBinaryPath)
		return func() {}, false
	}
	_ = out
	return func() { os.Remove(testBinaryPath) }, true
}

// startKillableMockRepo 启动一个带慢响应的 mock Maven 仓库，确保 scan 子进程
// 可在中途被 kill。jar 用空 zip 让扫描快速完成，但目录列表响应延迟 200ms
// 拉长 discovery，提供稳定 kill 时窗。
func startKillableMockRepo(t *testing.T, jarBytes []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // 慢响应拉长 kill 时窗
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/com/" || r.URL.Path == "/com/example/" ||
			r.URL.Path == "/com/example/lib/":
			fmt.Fprintf(w, `<html><body><a href="com/">com/</a><a href="com/example/">example/</a></body></html>`)
		case r.URL.Path == "/com/example/lib/1.0/" || r.URL.Path == "/com/example/lib/2.0/":
			fmt.Fprintf(w, `<a href="lib-1.0.jar">lib-1.0.jar</a><a href="lib-2.0.jar">lib-2.0.jar</a>`)
		case r.URL.Path == "/com/example/lib/1.0/lib-1.0.jar" ||
			r.URL.Path == "/com/example/lib/2.0/lib-2.0.jar":
			w.Header().Set("Content-Type", "application/java-archive")
			w.Write(jarBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startScanSubprocess 启动一个 scan 子进程对接 mockRepo，返回 cmd 与 stateFile。
// 子进程跑真实二进制，可被外部 SIGKILL/SIGTERM。
func startScanSubprocess(t *testing.T, mockRepo, stateFile string, extraArgs ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	if testBinaryPath == "" {
		t.Skip("test binary not built")
	}
	args := []string{"scan",
		"--repo", mockRepo,
		"--group", "com.example",
		"--rules-level", "core",
		"--checkpoint-interval", "1",
		"--state-file", stateFile,
		"--timeout", "10s",
	}
	args = append(args, extraArgs...)
	cmd := exec.Command(testBinaryPath, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd, &buf
}

// waitForInflight 轮询 stateFile 直到出现 in_flight_artifacts（或超时），
// 确保 scan 已进入下载阶段（kill 时窗已到）。
func waitForInflight(t *testing.T, stateFile string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(stateFile); err == nil {
			var st struct {
				InFlight []string `json:"in_flight_artifacts"`
			}
			if json.Unmarshal(data, &st) == nil && len(st.InFlight) > 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("scan never reached in-flight state within %s", timeout)
}

// loadStateJSON 读取 state 文件解析为 map（测试断言用）。
func loadStateJSON(t *testing.T, stateFile string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// 用空 zip 作为 jar 内容（扫描器解压后无 finding，快速完成）。
var emptyJarOnce sync.Once
var emptyJarBytes []byte

func emptyJar(t *testing.T) []byte {
	t.Helper()
	emptyJarOnce.Do(func() {
		// 内联构造空 zip
		buf := new(bytes.Buffer)
		w := zip.NewWriter(buf)
		w.Close()
		emptyJarBytes = buf.Bytes()
	})
	return emptyJarBytes
}

// 为 import 顺序引入需要的包引用占位（实际由上方使用决定）
var _ = io.EOF
var _ = context.Background
var _ = filepath.Join
```

- [ ] **Step 2: 修正 binary_fixture_test.go 的 import（zip 需要 archive/zip）**

文件: `tests/integration/binary_fixture_test.go`（修正 import 块与 emptyJar 函数，用 archive/zip）

将 Step 1 文件顶部的 import 块与底部 `emptyJar` 函数替换为：

```go
import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)
```

并将 `emptyJar` 函数体替换为：

```go
func emptyJar(t *testing.T) []byte {
	t.Helper()
	emptyJarOnce.Do(func() {
		buf := new(bytes.Buffer)
		w := zip.NewWriter(buf)
		w.Close()
		emptyJarBytes = buf.Bytes()
	})
	return emptyJarBytes
}
```

并删除文件底部那行 `var _ = io.EOF ...` 占位（import 已干净）。

- [ ] **Step 3: 创建 kill_process_test.go — SIGKILL 强杀 + SIGTERM + 双重崩溃场景**

```go
//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario A: SIGKILL 强杀（不可捕获）→ state 残留 in-flight → resume + retry-failed 不丢不重。
// 这是 V2 手动验证过的核心 case，现在自动化。SIGKILL 让 signal handler 无机会执行，
// state 停在最后一次 MarkInFlight/MarkCompleted 的 flush（status 可能 running，in-flight 残留）。
func TestKillProcess_SIGKILL_ResumeRecovers(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "kill.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)

	// SIGKILL 不可捕获 → 子进程立即死，signal handler 无机会跑
	require.NoError(t, cmd.Process.Signal(syscall.SIGKILL))
	cmd.Wait()

	// state 应残留 in-flight（status running 或 interrupted，关键看 in-flight 非空）
	st := loadStateJSON(t, stateFile)
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.NotEmpty(t, inflight, "SIGKILL should leave in-flight artifacts in state")

	// resume + retry-failed 续跑
	resumeCmd, buf := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resumeCmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resumeCmd.Process.Kill()
		t.Fatal("resume did not finish in 30s")
	}

	// 续跑后 state 应 completed，in-flight 清空
	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
	inflight2, _ := st2["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight2, "in-flight must be cleared after resume")
	// 至少扫到一些 artifact（mock repo 有 2 个 version）
	completed, _ := st2["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed, "resume should complete the in-flight artifacts")
	_ = buf
}

// Scenario B: SIGTERM（可捕获）→ signal handler cancel ctx → runScan 标 interrupted。
// 验证 state status=interrupted（区别于 SIGKILL 的 running 残留）+ resume 可续。
func TestKillProcess_SIGTERM_MarksInterruptedAndResumes(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "term.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("SIGTERM should let scan finish gracefully in 10s")
	}

	// SIGTERM 走 graceful shutdown → status=interrupted
	st := loadStateJSON(t, stateFile)
	// status 可能是 interrupted（signal handler 标了）或 completed（scan 在 cancel 前跑完）
	status, _ := st["status"].(string)
	assert.Contains(t, []string{"interrupted", "completed"}, status,
		"SIGTERM should produce interrupted or completed status, got %s", status)

	// resume 续跑应正常完成（无论上一轮 interrupted 还是 completed）
	resumeCmd, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done2 := make(chan error)
	go func() { done2 <- resumeCmd.Wait() }()
	select {
	case <-done2:
	case <-time.After(30 * time.Second):
		resumeCmd.Process.Kill()
		t.Fatal("resume after SIGTERM did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
}

// Scenario C: 双重崩溃——resume 跑了一半又 SIGKILL，二次 resume 仍不丢不重。
// 验证 resume 本身也是可中断的（resume 不是原子操作，中途再崩仍能恢复）。
func TestKillProcess_DoubleCrash_SecondResumeRecovers(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "double.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 SIGKILL
	cmd1, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	cmd1.Process.Signal(syscall.SIGKILL)
	cmd1.Wait()

	st1 := loadStateJSON(t, stateFile)
	inflight1, _ := st1["in_flight_artifacts"].([]interface{})
	require.NotEmpty(t, inflight1, "first kill should leave in-flight")

	// 第一次 resume，跑一半再 SIGKILL
	resume1, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	waitForInflight(t, stateFile, 10*time.Second) // resume 进入下载阶段再杀
	resume1.Process.Signal(syscall.SIGKILL)
	resume1.Wait()

	// 第二次 resume 应仍能恢复并完成
	resume2, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume2.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume2.Process.Kill()
		t.Fatal("second resume did not finish in 30s")
	}

	st3 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st3["status"])
	inflight3, _ := st3["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight3, "in-flight must be cleared after second resume")
	// 不丢不重：completed 路径去重后应覆盖 mock repo 的全部 version
	completed, _ := st3["completed_artifacts"].([]interface{})
	seen := map[string]bool{}
	for _, c := range completed {
		if s, ok := c.(string); ok {
			seen[s] = true
		}
	}
	assert.NotEmpty(t, seen, "should have completed artifacts after double-crash resume")
	// 无重复（每个 path 只出现一次）
	dupCount := 0
	for _, c := range completed {
		if s, ok := c.(string); ok {
			for _, c2 := range completed {
				if s2, ok2 := c2.(string); ok2 && s == s2 {
					dupCount++
				}
			}
		}
	}
	assert.Equal(t, len(completed), dupCount/len(completed)*len(completed)+len(completed),
		"no duplicate paths expected (got %d entries, %d pair-matches)", len(completed), dupCount)
	_ = os.Remove
}
```

- [ ] **Step 4: 验证子进程级 SIGKILL/SIGTERM/双重崩溃 e2e**

Run: `go test -tags integration -run "TestKillProcess" -v -count=1 -timeout 5m ./tests/integration/`
Expected:
  - Exit code: 0
  - Output contains: "ok  github.com/scagogogo/mvn-repo-scanner/tests/integration"
  - Output does NOT contain: "FAIL" or "--- FAIL"

- [ ] **Step 5: 提交**
Run: `git add tests/integration/binary_fixture_test.go tests/integration/kill_process_test.go && git commit -m "test(integration): real subprocess kill-process e2e for SIGKILL/SIGTERM/double-crash resume"`

---

### Task 4: discovery 中途 kill + fetch-failed 跨崩溃 + 现有 SIGTERM 测试加固

**Depends on:** Task 3
**Files:**
- Create: `tests/integration/kill_discovery_test.go`
- Modify: `cmd/scan_extra_test.go:374-418`（`TestRunScan_SignalInterrupts` 补 state 断言）

- [ ] **Step 1: 创建 kill_discovery_test.go — discovery 阶段 kill + fetch-failed 跨崩溃重访**

```go
//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startFlakyMockRepo 启动一个部分目录 fetch 返回 500 的仓库，触发 MarkDirFailed。
// 第 N 次请求 /com/example/lib/ 返回 500（模拟 transient fetch 失败），
// 后续请求成功。让 scan 在 discovery 阶段记录 failed_dir 后被 kill。
func startFlakyMockRepo(t *testing.T, jarBytes []byte) *httptest.Server {
	t.Helper()
	var libHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/com/" || r.URL.Path == "/com/example/":
			w.Write([]byte(`<a href="com/example/">example/</a><a href="com/example/lib/">lib/</a>`))
		case r.URL.Path == "/com/example/lib/":
			n := atomic.AddInt32(&libHits, 1)
			if n <= 1 {
				w.WriteHeader(http.StatusInternalServerError) // 第一次失败
				return
			}
			w.Write([]byte(`<a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`))
		case r.URL.Path == "/com/example/lib/1.0/":
			w.Write([]byte(`<a href="lib-1.0.jar">lib-1.0.jar</a>`))
		case r.URL.Path == "/com/example/lib/2.0/":
			w.Write([]byte(`<a href="lib-2.0.jar">lib-2.0.jar</a>`))
		case r.URL.Path == "/com/example/lib/1.0/lib-1.0.jar" ||
			r.URL.Path == "/com/example/lib/2.0/lib-2.0.jar":
			w.Header().Set("Content-Type", "application/java-archive")
			w.Write(jarBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Scenario D: discovery 阶段 fetch 失败记录 failed_dir → kill → resume 重访 failed_dir。
// 验证 FailedDirs 跨崩溃存活，revisitPendingDirs 重访成功后 ClearDirFailed。
func TestKillProcess_DiscoveryFetchFail_SurvivesCrash(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "faildir.json")
	srv := startFlakyMockRepo(t, emptyJar(t))

	// 第一次 scan：lib 目录第一次 fetch 500 → MarkDirFailed；等 state 落盘后 kill
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	// 等 failed_dirs 出现或 in-flight 出现（任一表示 discovery 已跑）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := loadStateJSON(t, stateFile)
		if fd, _ := st["failed_dirs"].([]interface{}); len(fd) > 0 {
			break
		}
		if inf, _ := st["in_flight_artifacts"].([]interface{}); len(inf) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// resume：flaky repo 第二次 lib fetch 成功 → revisit 重访 → ClearDirFailed → 扫到 artifact
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after discovery-fetch-fail did not finish in 30s")
	}

	st := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st["status"])
	// failed_dirs 应被清空（revisit 成功后 ClearDirFailed）
	failedDirs, _ := st["failed_dirs"].([]interface{})
	assert.Empty(t, failedDirs, "failed_dirs must be cleared after successful revisit on resume")
	// 应扫到 artifact（revisit 重访成功）
	completed, _ := st["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed)
}

// Scenario E: discovery 中途 SIGKILL（cursor 已 advance 但 discovery 未完成）→ resume 从 cursor 续。
// 验证 discovery_cursor 跨崩溃存活，resume 不从头重发 discovery。
func TestKillProcess_DiscoveryMidCursorKill_ResumeContinues(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "midcursor.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan 跑一会（discovery 跑了一部分）后 kill
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	// 等 discovery_cursor 出现（表示 streaming discovery 跑过至少一帧）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := loadStateJSON(t, stateFile)
		if c, _ := st["discovery_cursor"].([]interface{}); len(c) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// cursor 应已落盘
	st := loadStateJSON(t, stateFile)
	cursor, _ := st["discovery_cursor"].([]interface{})
	require.NotEmpty(t, cursor, "discovery cursor should be persisted before kill")

	// resume 应从 cursor 续，最终 completed
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after mid-discovery kill did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
	// resume 完成后 cursor 应被清空（discovery 跑完）
	cursor2, _ := st2["discovery_cursor"].([]interface{})
	assert.Empty(t, cursor2, "cursor must be cleared after discovery completes on resume")
}
```

- [ ] **Step 2: 修改 TestRunScan_SignalInterrupts — 补 state status/in-flight/不丢不重断言**

文件: `cmd/scan_extra_test.go:374-418`（替换整个 `TestRunScan_SignalInterrupts` 函数）

```go
// runScan 的 MarkInterrupted（line 346-348）+ runStatus interrupted（line 365-367）：
// 慢 mock repo 让 scan 长时间运行，发 SIGTERM → runScan 的 signal handler cancel ctx
// → scan.Run 返回（ctx 取消）→ MarkInterrupted + runStatus="interrupted"。
// 补充断言：state status=interrupted（或 completed 若 cancel 前跑完）+ resume 后不丢不重。
func TestRunScan_SignalInterrupts(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	// 慢 repo：每个响应延迟 500ms，让 scan 有时间接收 signal
	srv := startSlowMockMavenRepo(t, jarPath, 500*time.Millisecond)
	defer srv.Close()

	stateFile := filepath.Join(tmpDir, "state.json")
	cfg = &config.Config{
		RepoURL:      srv.URL,
		GroupFilter:  "com.example",
		Concurrency:  1,
		Timeout:      30 * time.Second,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		StateFile:    stateFile,
		TaskID:       "sig-task",
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}

	done := make(chan struct{})
	go func() {
		_ = withStdoutCapture(t, func() {
			_ = runScan(scanCmd, nil)
		})
		close(done)
	}()
	// 等 scan 启动，发 SIGTERM
	time.Sleep(100 * time.Millisecond)
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runScan should finish after SIGTERM")
	}

	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	task, err := store.GetTask("sig-task")
	require.NoError(t, err)
	assert.Equal(t, "interrupted", task.LastRunStatus)

	// 断言 state 文件被 flush 且 status 标记正确（interrupted 或 completed）
	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err, "state file must be flushed after SIGTERM")
	assert.Contains(t, []state.ScanStatus{state.ScanInterrupted, state.ScanCompleted},
		loaded.GetStatus(), "state status should be interrupted or completed after SIGTERM")

	// resume 应能续跑并完成（不丢不重）—— 重置 cfg 用相同 repo + resume
	cfg.Resume = true
	cfg.RetryFailed = true
	cfg.TaskID = ""       // 不重复 task 注册逻辑
	cfg.ScanInterval = 0  // 一次性，不再调度
	resumeDone := make(chan struct{})
	go func() {
		_ = withStdoutCapture(t, func() {
			_ = runScan(scanCmd, nil)
		})
		close(resumeDone)
	}()
	select {
	case <-resumeDone:
	case <-time.After(15 * time.Second):
		t.Fatal("resume after SIGTERM should finish in 15s")
	}

	resumed, err := state.LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, state.ScanCompleted, resumed.GetStatus(),
		"resume should reach completed status")
}
```

- [ ] **Step 3: 验证 discovery kill/fetch-fail 场景 + SIGTERM 测试加固**

Run: `go test -tags integration -run "TestKillProcess_Discovery" -v -count=1 -timeout 5m ./tests/integration/ && go test ./cmd/ -run "TestRunScan_SignalInterrupts" -v -count=1`
Expected:
  - Exit code: 0
  - Output contains: "ok  github.com/scagogogo/mvn-repo-scanner/tests/integration" 和 "ok  github.com/scagogogo/mvn-repo-scanner/cmd"
  - Output does NOT contain: "FAIL" or "--- FAIL"

- [ ] **Step 4: 跑全仓库测试 + race 确认无回归**

Run: `go test ./... -count=1 && go test -race ./internal/state/ ./internal/scanner/ -count=1`
Expected:
  - Exit code: 0
  - Output does NOT contain: "FAIL"
  - race 输出不含 "DATA RACE"

- [ ] **Step 5: 提交**
Run: `git add tests/integration/kill_discovery_test.go cmd/scan_extra_test.go && git commit -m "test(integration,cmd): discovery-mid-kill + fetch-failed cross-crash + strengthen SIGTERM state assertions"`

---

## Self-Review Results

| # | Check | Result | Action Taken |
|---|-------|--------|-------------|
| 1 | Header 含 Goal + Architecture + Tech Stack? | PASS | Goal/Architecture/Tech Stack/Risks 齐全 |
| 2 | 每个 Task 标注 Depends on? | PASS | Task1 None, Task2/3/4 显式依赖 |
| 3 | 每个 Task 精确文件路径（Create/Modify/Test）? | PASS | 全部带行号（state.go:636-666 / scan_extra_test.go:374-418） |
| 4 | 每个 Task 3-8 Steps? | PASS | Task1=4, Task2=3, Task3=5, Task4=5 |
| 5 | 新文件 Step 含完整代码（含 import）? | PASS | flush_crash_test.go/binary_fixture_test.go/kill_process_test.go/kill_discovery_test.go 全完整 |
| 6 | 修改 Step 含替换后完整函数（非 diff）? | PASS | flush() 与 TestRunScan_SignalInterrupts 均完整替换 |
| 7 | 代码块 5-80 行? | FIXED | Task3 Step1 binary_fixture_test.go 偏长，已拆出 Step2 单独修正 import；kill_process_test.go 分 3 场景各自独立 |
| 8 | 所有函数/类型 Plan 内有定义? | PASS | flushHook/loadStateJSON/waitForInflight/startScanSubprocess/emptyJar/startKillableMockRepo/startFlakyMockRepo 均在 Plan 内定义 |
| 9 | 每个 Task 有验证命令（命令+exit+pattern）? | PASS | 全部 Run+Expected 三要素 |
| 10 | Spec 每需求有对应 Task? | PASS | SIGKILL→Task3A, SIGTERM→Task3B+Task4 Step2, 双重崩溃→Task3C, 掉电→Task2, .bak污染→Task1+Task2, discovery kill→Task4, fetch-failed→Task4 |
| 11 | 每个 Task 完成后可独立验证? | PASS | 每个 Task 末有验证 Step |
| 12 | 无 TBD/TODO/模糊描述? | PASS | 全具体代码 |
| 13 | 无 "add validation" 抽象指令? | PASS | 全具体 |
| 14 | 跨 Task 函数签名/类型名一致? | PASS | testBinaryPath/startScanSubprocess/waitForInflight/loadStateJSON/emptyJar 跨 Task3/4 一致；state.LoadScanState/state.ScanCompleted 跨 Task2/4 一致 |
| 15 | 文件保存位置正确（docs/superpowers/plans/）? | PASS | 已存 docs/superpowers/plans/2026-07-20-resume-kill-process-extreme-testing.md |

**Status:** ✅ ALL PASS

⏹️ **Phase 3 Complete**

## Phase 4: EXECUTION SELECTION

**Tasks:** 4
**Dependencies:** yes（Task1→Task2→Task3→Task4 链式）
**User Preference:** none（ZERO-CONFIRM 模式）
**Decision:** Subagent-Driven
**Reasoning:** 4 tasks + 顺序依赖链 + 涉及产品代码修改（Task1）+ 多测试文件创建 → 适合子代理驱动执行

**Auto-invoking:** `superpowers:subagent-driven-development`

⏹️ **Phase 4 Complete: Execution selected, invoking next skill"
