# 断点续跑健壮性增强 v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development`
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** 在既有 cursor-based resume（O(depth) 游标 + in-flight/failed-dir 重访 + config snapshot）基础上，补齐 4 个健壮性缺口（MaxFileSize 校验、flush fsync+备份、损坏恢复、finished 状态语义），并自主设计覆盖全部断点续跑场景的测试套件。

**Architecture:** 数据流：scan 中断 → state.flush 落盘（现在加 fsync+.bak） → 用户 `--resume` 重启 → LoadScanState（现在能从 .bak 恢复损坏文件） → ValidateConfig（现在校验 MaxFileSize） → discoverStreaming 从 cursor 恢复 + revisitPendingDirs 重访 in-flight/failed-dir → 扫描完成设 ScanCompleted，主动停下设 ScanFinished。关键组件：`state.ScanState`（持久化+校验）、`scanner.discoverStreaming`/`revisitPendingDirs`（游标恢复）、`cmd.runScan`（resume flag+signal flush）。设计选择：只新增方法/字段，不改既有签名，保持 StateTracker 接口兼容；fsync 只在最终 Flush 和每 checkpointEvery 次落盘时调，避免高频 checkpoint 拖慢。

**Tech Stack:** Go 1.25.0, modernc.org/sqlite, testify/assert + require, net/http/httptest, encoding/json

**Risks:**
- flush 加 fsync 影响高频 checkpoint 性能 → 缓解：批量 checkpoint 模式不变，fsync 仅在 `Flush()` 强制与每 N 次 batch 落盘时调用
- 新增 ScanFinished 状态可能破坏旧测试对 Status 的断言 → 缓解：ScanFinished 仅在「ctx 正常取消但未跑完」时设；既有「Run 全跑完」仍 ScanCompleted；既有「signal 中断」仍 ScanInterrupted；旧测试不触及 ScanFinished 路径
- 改 state.go 字段需保持 JSON 向后兼容 → 缓解：新字段用 `omitempty`，旧 state 文件加载时零值合法
- cmd 层 e2e 测试依赖信号/文件系统 → 缓解：用临时 state 文件 + httptest mock repo，不依赖真实信号（直接调 runScan + cancel ctx）

---

### Task 1: ValidateConfig 补 MaxFileSize 校验

**Depends on:** None
**Files:**
- Modify: `internal/state/state.go:541-565`（ValidateConfig 函数）
- Modify: `internal/state/state_branch_test.go`（追加 MaxFileSize 用例）

**背景：** `ConfigSnapshot` 有 `MaxFileSize` 字段（line 38），但 `ValidateConfig`（541-565）只校验 RepoURL/GroupFilter/RulesLevel/RulesFile/RulesMerge，漏了 MaxFileSize。MaxFileSize 改变意味着大文件是否扫描的边界变了，resume 后结果不可比，应拒绝。

- [ ] **Step 1: 修改 ValidateConfig 以补 MaxFileSize 校验**

文件: `internal/state/state.go:541-565`（替换整个 ValidateConfig 函数）

```go
// ValidateConfig checks that the provided config matches the stored config snapshot.
// Returns nil if compatible, or an error describing the mismatch.
func (s *ScanState) ValidateConfig(cfg ConfigSnapshot) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.ConfigSnapshot.RepoURL != "" && s.ConfigSnapshot.RepoURL != cfg.RepoURL {
		return fmt.Errorf("state file RepoURL (%s) does not match current RepoURL (%s)", s.ConfigSnapshot.RepoURL, cfg.RepoURL)
	}
	if s.ConfigSnapshot.GroupFilter != "" && s.ConfigSnapshot.GroupFilter != cfg.GroupFilter {
		return fmt.Errorf("state file GroupFilter (%s) does not match current GroupFilter (%s)", s.ConfigSnapshot.GroupFilter, cfg.GroupFilter)
	}
	// Rules config drift changes what gets flagged — refuse resume across a
	// different rules setup so findings stay comparable to the prior run.
	if s.ConfigSnapshot.RulesLevel != "" && s.ConfigSnapshot.RulesLevel != cfg.RulesLevel {
		return fmt.Errorf("state file RulesLevel (%s) does not match current RulesLevel (%s)", s.ConfigSnapshot.RulesLevel, cfg.RulesLevel)
	}
	if s.ConfigSnapshot.RulesFile != cfg.RulesFile {
		return fmt.Errorf("state file RulesFile (%q) does not match current RulesFile (%q)", s.ConfigSnapshot.RulesFile, cfg.RulesFile)
	}
	if s.ConfigSnapshot.RulesMerge != cfg.RulesMerge {
		return fmt.Errorf("state file RulesMerge (%v) does not match current RulesMerge (%v)", s.ConfigSnapshot.RulesMerge, cfg.RulesMerge)
	}
	// MaxFileSize drift changes which files get scanned — a previously-skipped
	// oversized artifact would now be scanned (or vice versa), making the
	// resumed result set incomparable. Refuse resume across a different cap.
	if s.ConfigSnapshot.MaxFileSize != "" && s.ConfigSnapshot.MaxFileSize != cfg.MaxFileSize {
		return fmt.Errorf("state file MaxFileSize (%q) does not match current MaxFileSize (%q)", s.ConfigSnapshot.MaxFileSize, cfg.MaxFileSize)
	}
	return nil
}
```

- [ ] **Step 2: 在 state_branch_test.go 追加 MaxFileSize 校验组合测试**

文件: `internal/state/state_branch_test.go`（文件末尾追加）

```go
// state.go ValidateConfig 的 MaxFileSize 复合条件 `MaxFileSize != "" && MaxFileSize != cfg.MaxFileSize`
// 覆盖：空快照短路通过、相同值通过、不同值报错。
func TestValidateConfig_MaxFileSize_BranchCombos(t *testing.T) {
	tmpDir := t.TempDir()

	// 空快照（MaxFileSize==""）→ 短路通过，即使 cfg 不同
	s1 := NewScanStateWithConfig("mf1", "https://a.com", "", filepath.Join(tmpDir, "mf1.json"), 0,
		ConfigSnapshot{RepoURL: "https://a.com"}) // MaxFileSize 空
	assert.NoError(t, s1.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "100MB"}))

	// 相同值 → 通过
	s2 := NewScanStateWithConfig("mf2", "https://a.com", "", filepath.Join(tmpDir, "mf2.json"), 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	assert.NoError(t, s2.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"}))

	// 不同值 → 报错，且错误消息含 MaxFileSize
	s3 := NewScanStateWithConfig("mf3", "https://a.com", "", filepath.Join(tmpDir, "mf3.json"), 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	err := s3.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "100MB"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxFileSize")
}
```

- [ ] **Step 3: 验证 state 包**

Run: `go test -count=1 -run "TestValidateConfig" -v ./internal/state/`

Expected:
  - Exit code: 0
  - Output contains: "--- PASS" for TestValidateConfig_BranchCombos and TestValidateConfig_MaxFileSize_BranchCombos
  - 无 FAIL

- [ ] **Step 4: 提交**

Run: `git add internal/state/state.go internal/state/state_branch_test.go && git commit -m "feat(state): validate MaxFileSize on resume to keep result sets comparable"`

---

### Task 2: flush 持久化加固（fsync + .bak 备份 + 损坏恢复）

**Depends on:** Task 1
**Files:**
- Modify: `internal/state/state.go:575-594`（flush 函数）
- Modify: `internal/state/state.go:131-178`（LoadScanState 损坏恢复）
- Create: `internal/state/resume_test.go`（持久化加固测试）

**背景：** 现有 `flush` 只 `WriteFile(.tmp)` + `Rename`，无 `fsync`——崩溃窗口下 rename 后内核页缓存未刷盘，可能丢数据或得到截断文件。且 JSON parse 失败直接 return error，无备份恢复。加 fsync + 写前备份上一份到 `.bak` + LoadScanState 损坏时尝试 `.bak`。

- [ ] **Step 1: 修改 flush 以加 fsync + 写前 .bak 备份**

文件: `internal/state/state.go:575-594`（替换整个 flush 函数）

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

	// Back up the previous good state file (if any) before replacing it.
	if prev, err := os.ReadFile(s.filePath); err == nil && len(prev) > 0 {
		_ = os.WriteFile(s.filePath+".bak", prev, 0644)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: 修改 LoadScanState 以在 JSON 损坏时回退 .bak**

文件: `internal/state/state.go:131-178`（替换整个 LoadScanState，并把迁移+重建 set 逻辑抽到 parseAndFinalize）

```go
// LoadScanState loads scan state from a file. Returns nil if file doesn't exist.
// If the primary file is corrupt (unparseable), it falls back to the .bak
// backup written by the previous successful flush, so a single bad write
// never loses all resume progress.
func LoadScanState(filePath string) (*ScanState, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	loaded, perr := parseAndFinalize(data, filePath)
	if perr == nil {
		return loaded, nil
	}

	// Primary file is corrupt — try the .bak backup before giving up.
	bak, bakErr := os.ReadFile(filePath + ".bak")
	if bakErr != nil {
		return nil, perr // no backup, surface the original parse error
	}
	bakLoaded, bakParseErr := parseAndFinalize(bak, filePath)
	if bakParseErr != nil {
		return nil, perr // backup also bad — surface the primary error
	}
	log.Printf("Warning: state file %s corrupt (%v), recovered from .bak backup", filePath, perr)
	return bakLoaded, nil
}

// parseAndFinalize unmarshals one state blob, runs version check + migration,
// and rebuilds the in-memory lookup sets. Shared by the primary and .bak
// recovery paths. Returns (nil, parse/version err) on failure so LoadScanState
// can decide whether to fall back to .bak.
func parseAndFinalize(data []byte, filePath string) (*ScanState, error) {
	var s ScanState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	// Version check: reject future versions
	if s.Version > CurrentVersion {
		return nil, fmt.Errorf("%w: state file version %d, supported up to %d", ErrVersionMismatch, s.Version, CurrentVersion)
	}
	// Migrate version 0 (old format without version field) to version 1
	if s.Version == 0 {
		s.Version = CurrentVersion
		if s.Status == "" {
			s.Status = ScanInterrupted // old state files without status are treated as interrupted
		}
	}
	s.filePath = filePath
	s.checkpointEvery = 50
	s.completedSet = make(map[string]bool)
	s.inFlightSet = make(map[string]bool)
	s.failedSet = make(map[string]bool)
	s.failedDirSet = make(map[string]bool)
	for _, p := range s.CompletedPaths {
		s.completedSet[p] = true
	}
	for _, p := range s.InFlightPaths {
		s.inFlightSet[p] = true
	}
	for _, e := range s.FailedEntries {
		s.failedSet[e.Path] = true
	}
	for _, d := range s.FailedDirs {
		s.failedDirSet[d] = true
	}
	return &s, nil
}
```

注意：import 块顶部需加 `"log"`（state.go 当前未 import log）。

- [ ] **Step 3: 创建 resume_test.go — 持久化加固测试**

```go
// internal/state/resume_test.go
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flush 写前备份：首次 flush 后 .bak 不存在（无前文件）；第二次 flush 后 .bak == 第一次内容。
func TestFlush_WritesBakBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("bak1", "https://a.com", "", path, 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})

	// 首次 flush：无前文件 → .bak 不创建
	require.NoError(t, s.Flush())
	_, err := os.Stat(path + ".bak")
	assert.True(t, os.IsNotExist(err), "首次 flush 不应有 .bak")

	// 改动后再 flush：.bak 应等于首次内容
	first, _ := os.ReadFile(path)
	s.TotalScanned = 42
	require.NoError(t, s.Flush())
	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err, "二次 flush 后应有 .bak")
	assert.Equal(t, first, bak, ".bak 应保存上一份内容")
}

// flush 落盘后再加载应恢复所有字段（含 TotalScanned 等统计）。
func TestFlush_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("rt1", "https://a.com", "com.x", path, 0,
		ConfigSnapshot{RepoURL: "https://a.com", RulesLevel: "core", MaxFileSize: "50MB"})
	s.MarkCompleted("com/x/a/1.0")
	s.MarkFailed("com/x/a/2.0", "boom", 1)
	s.MarkInFlight("com/x/a/3.0")
	require.NoError(t, s.Flush())

	loaded, err := LoadScanState(path)
	require.NoError(t, err)
	assert.Equal(t, "rt1", loaded.ScanID)
	assert.True(t, loaded.IsCompleted("com/x/a/1.0"))
	assert.True(t, loaded.IsFailed("com/x/a/2.0"))
	assert.True(t, loaded.IsInFlight("com/x/a/3.0"))
	assert.Equal(t, 1, loaded.CompletedCount())
}

// LoadScanState 损坏回退 .bak：主文件写坏，.bak 完好 → 从 .bak 恢复。
func TestLoadScanState_CorruptFallsBackToBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")

	// 写一份合法 state 并 flush 两次（让 .bak 存在）
	s := NewScanStateWithConfig("cb1", "https://a.com", "", path, 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	s.MarkCompleted("com/a/1.0")
	require.NoError(t, s.Flush())
	s.MarkCompleted("com/a/2.0")
	require.NoError(t, s.Flush()) // 现在 .bak 含 1.0

	// 损坏主文件
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0644))

	loaded, err := LoadScanState(path)
	require.NoError(t, err, "损坏时应从 .bak 恢复")
	assert.Equal(t, "cb1", loaded.ScanID)
	// .bak 是第一次 flush（含 1.0，不含 2.0）
	assert.True(t, loaded.IsCompleted("com/a/1.0"))
	assert.False(t, loaded.IsCompleted("com/a/2.0"))
}

// LoadScanState 主文件损坏且无 .bak → 返回 parse err。
func TestLoadScanState_CorruptNoBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	require.NoError(t, os.WriteFile(path, []byte("garbage"), 0644))
	_, err := LoadScanState(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// .bak 也损坏时不 panic，返回 parse err。
func TestLoadScanState_BothCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	require.NoError(t, os.WriteFile(path, []byte("garbage"), 0644))
	require.NoError(t, os.WriteFile(path+".bak", []byte("also garbage"), 0644))
	_, err := LoadScanState(path)
	require.Error(t, err)
}

// 版本拒绝：未来版本返回 ErrVersionMismatch。
func TestLoadScanState_FutureVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	future := map[string]interface{}{"version": 999, "scan_id": "fv1"}
	data, _ := json.Marshal(future)
	require.NoError(t, os.WriteFile(path, data, 0644))
	_, err := LoadScanState(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}
```

- [ ] **Step 4: 验证 state 包持久化加固**

Run: `go test -count=1 -run "TestFlush|TestLoadScanState" -v ./internal/state/`

Expected:
  - Exit code: 0
  - Output contains: "--- PASS" for 6 tests
  - 无 FAIL

- [ ] **Step 5: 验证 state 包覆盖率仍 100%**

Run: `go test -count=1 -coverprofile=/tmp/cov_st2.out -covermode=atomic ./internal/state/ && go tool cover -func=/tmp/cov_st2.out | grep -vE '100.0%|^total:'`

Expected:
  - Exit code: 0
  - Output contains: "coverage: 100.0% of statements"
  - grep 输出为空（所有函数 100%）

- [ ] **Step 6: 提交**

Run: `git add internal/state/state.go internal/state/resume_test.go && git commit -m "feat(state): harden flush with fsync + .bak backup and corrupt-file recovery"`

---

### Task 3: ScanFinished 状态语义 + resume 进度预估

**Depends on:** Task 2
**Files:**
- Modify: `internal/state/state.go:22-29`（ScanStatus 常量）
- Modify: `internal/state/state.go`（新增 MarkFinishedStatus + GetResumeEstimate 方法）
- Modify: `internal/state/resume_test.go`（追加 finished/estimate 测试）

**背景：** 现有 Status 只有 running/completed/interrupted。「ctx 正常取消但扫描未跑完」（如设了 `--max-scan` 上限主动停）与「signal 中断」都标 interrupted，无法区分。新增 `ScanFinished` 表示「主动正常停止、可安全 resume」；并加 `GetResumeEstimate` 给 resume 一个进度视角（已扫/已发现/已失败/in-flight 计数聚合）。

- [ ] **Step 1: 修改 state.go 加 ScanFinished 常量与新方法**

文件: `internal/state/state.go:22-29`（替换 ScanStatus 常量块）

```go
// ScanStatus represents the overall status of a scan session.
type ScanStatus string

const (
	ScanRunning    ScanStatus = "running"
	ScanCompleted  ScanStatus = "completed"  // scan ran to full completion
	ScanInterrupted ScanStatus = "interrupted" // aborted by signal/error
	ScanFinished   ScanStatus = "finished" // stopped normally before completion (e.g. limit reached); safe to resume
)
```

文件: `internal/state/state.go`（在 MarkCompletedStatus 之后，line 310 附近追加）

```go
// MarkFinishedStatus sets the scan status to finished — the scan was stopped
// normally before reaching full completion (e.g. a scan-count limit was hit),
// so it is safe to resume later. Distinct from Interrupted (signal/error) and
// Completed (ran to the end).
func (s *ScanState) MarkFinishedStatus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = ScanFinished
}
```

文件: `internal/state/state.go`（在 GetProgressStats 之后，line 572 附近追加）

```go
// GetResumeEstimate returns a coarse progress snapshot for resume logging:
// discovered (cached total, 0 if streaming/unknown), scanned, failed, and
// in-flight counts. Used by cmd to print "resuming: X scanned, Y failed,
// Z in-flight" without exposing internal slices.
func (s *ScanState) GetResumeEstimate() (status ScanStatus, discovered, scanned, failed, inFlight int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status, s.TotalDiscovered, s.TotalScanned, s.TotalFailed, len(s.InFlightPaths)
}
```

- [ ] **Step 2: 在 resume_test.go 追加 finished + estimate 测试**

文件: `internal/state/resume_test.go`（文件末尾追加）

```go
// ScanFinished 状态：MarkFinishedStatus 设置后 GetStatus 返回 finished，
// 且持久化 round-trip 后仍为 finished。
func TestScanStatus_Finished_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("fin1", "https://a.com", "", path, 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	s.MarkFinishedStatus()
	require.Equal(t, ScanFinished, s.GetStatus())
	require.NoError(t, s.Flush())

	loaded, err := LoadScanState(path)
	require.NoError(t, err)
	assert.Equal(t, ScanFinished, loaded.GetStatus())
}

// GetResumeEstimate 聚合各计数。
func TestGetResumeEstimate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("est1", "https://a.com", "", path, 0,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	s.SetDiscoveredArtifacts([]string{"a/1", "a/2", "a/3"}) // TotalDiscovered=3
	s.MarkCompleted("a/1")                                  // TotalScanned=1
	s.MarkFailed("a/2", "boom", 0)                          // TotalFailed=1
	s.MarkInFlight("a/3")                                   // inFlight=1

	status, disc, scanned, failed, inFlight := s.GetResumeEstimate()
	assert.Equal(t, ScanRunning, status)
	assert.Equal(t, 3, disc)
	assert.Equal(t, 1, scanned)
	assert.Equal(t, 1, failed)
	assert.Equal(t, 1, inFlight)
}
```

- [ ] **Step 3: 验证 state 包 finished/estimate**

Run: `go test -count=1 -run "TestScanStatus_Finished|TestGetResumeEstimate" -v ./internal/state/`

Expected:
  - Exit code: 0
  - Output contains: "--- PASS" for 2 tests
  - 无 FAIL

- [ ] **Step 4: 提交**

Run: `git add internal/state/state.go internal/state/resume_test.go && git commit -m "feat(state): add ScanFinished status and GetResumeEstimate for resume progress"`

---

### Task 4: scanner resume 端到端场景测试

**Depends on:** Task 3
**Files:**
- Create: `internal/scanner/resume_scenario_test.go`

**背景：** 既有 resume 测试覆盖单次 cancel+resume、disjoint subtrees、failed-dir 重访。补「多 phase 反复 resume」「深 cursor 多层目录恢复」「scan-count 限制主动停下后 resume 设 ScanFinished」「并发中断点（不同 cancel 时机）」四个场景。

- [ ] **Step 1: 创建 resume_scenario_test.go — 多 phase 反复 resume 场景**

```go
// internal/scanner/resume_scenario_test.go
package scanner

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resumeScenarioHarness 封装多 phase resume 测试的公共构造。
// 不持有 downloader —— 不同 phase 需要阻塞不同 artifact，downloader 由调用方传入。
type resumeScenarioHarness struct {
	cfg     *config.Config
	fetcher *mockPageFetcher
	state   *state.ScanState
	det     *mockDetector
}

func newResumeScenarioHarness(t *testing.T, scanID string) *resumeScenarioHarness {
	t.Helper()
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1, // 每 yield 1 个就 checkpoint cursor，保证 cancel 前 cursor 已落盘
	}
	fetcher := newStreamingMockRepo() // 4 artifacts, DFS 顺序: lib/1.0, lib/2.0, commons/1.0, commons/2.0
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, scanID+".json")
	st := state.NewScanStateWithConfig(scanID, cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
	return &resumeScenarioHarness{cfg: cfg, fetcher: fetcher, state: st, det: &mockDetector{}}
}

// runPhaseBlocking 跑一次 scan：用 selectiveBlockDownloader 阻塞 blockPath 的下载，
// 在第 n 个 artifact 完成后 cancel。返回本 phase 扫到的路径。
//
// 为什么用 selectiveBlockDownloader 而非 mockDownloader：
// mockDownloader 即时返回，cancel 生效前 worker 可能已多扫（CPU 紧张时竞态放大），
// 这是既有 TestScanner_StreamingDiscoveryResumable 的 flaky 根因。阻塞第 n+1 个
// artifact 让 cancel 时它确定性处于 in-flight，cancel 前 worker 确定性只扫 n 个。
func (h *resumeScenarioHarness) runPhaseBlocking(t *testing.T, n int, blockPath string) []string {
	t.Helper()
	dl := &selectiveBlockDownloader{
		mockDownloader: mockDownloader{},
		blockPath:      blockPath,
		blocked:        make(chan struct{}),
	}
	scan := NewScanner(h.cfg,
		WithDownloader(dl), WithDetector(h.det),
		WithState(h.state), WithDiscoveryCacher(h.state),
		WithFailedRetryer(h.state), WithCursorSaver(h.state),
		WithPageFetcher(h.fetcher), WithStreamingDiscovery(),
	)
	var scanned []string
	var mu sync.Mutex
	var done = make(chan struct{})
	scan.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			h.state.MarkCompleted(r.Artifact.Path())
			mu.Lock()
			scanned = append(scanned, r.Artifact.Path())
			count := len(scanned)
			mu.Unlock()
			if count >= n {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-done; cancel() }()
	go func() { time.Sleep(3 * time.Second); cancel() }() // 兜底防 hang
	scan.Run(ctx)
	require.NoError(t, h.state.Flush())
	return scanned
}

// runPhaseToCompletion 跑一次 scan 到完成（非阻塞 downloader）。返回本 phase 扫到的路径。
func (h *resumeScenarioHarness) runPhaseToCompletion(t *testing.T) []string {
	t.Helper()
	scan := NewScanner(h.cfg,
		WithDownloader(&mockDownloader{}), WithDetector(h.det),
		WithState(h.state), WithDiscoveryCacher(h.state),
		WithFailedRetryer(h.state), WithCursorSaver(h.state),
		WithPageFetcher(h.fetcher), WithStreamingDiscovery(),
	)
	var scanned []string
	var mu sync.Mutex
	scan.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			h.state.MarkCompleted(r.Artifact.Path())
			mu.Lock()
			scanned = append(scanned, r.Artifact.Path())
			mu.Unlock()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := scan.Run(ctx)
	require.NoError(t, err)
	require.NoError(t, h.state.Flush())
	return scanned
}

// 场景 1：3 phase 反复 resume，每 phase 扫 1 个，最终 4 个全覆盖且不重复。
// DFS 顺序：lib/1.0(1) → lib/2.0(2) → commons/1.0(3) → commons/2.0(4)。
//   phase1: 扫 lib/1.0，阻塞 lib/2.0 → cancel，扫 1 个
//   phase2: resume，lib/1.0 已 completed 跳过，扫 lib/2.0，阻塞 commons/1.0 → cancel，扫 1 个
//   phase3: resume 跑完，扫 commons/1.0 + commons/2.0，2 个
func TestResumeScenario_MultiPhaseProgressive(t *testing.T) {
	h := newResumeScenarioHarness(t, "multi-phase")

	p1 := h.runPhaseBlocking(t, 1, "com/example/lib/2.0")
	p2 := h.runPhaseBlocking(t, 1, "org/apache/commons/1.0")
	p3 := h.runPhaseToCompletion()

	all := append(append(p1, p2...), p3...)
	unique := map[string]bool{}
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "3 phase 应覆盖全部 4 artifact: %v", all)

	// 每个 artifact 恰好扫一次
	seen := map[string]int{}
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s 扫了 %d 次", p, c)
	}
	require.GreaterOrEqual(t, len(p1), 1, "phase1 应至少扫 1 个")
	require.GreaterOrEqual(t, len(p2), 1, "phase2 应至少扫 1 个")
}

// 场景 2：scan-count 限制主动停下（cancel 无 signal）后 resume 能继续，不丢不重。
// 模拟：phase1 扫 2 个后 ctx cancel（非 signal，模拟 --max-scan 到达），phase2 resume 跑完。
//   phase1: 扫 lib/1.0 + lib/2.0，阻塞 commons/1.0 → cancel，扫 2 个
func TestResumeScenario_ArtificialStopAndResume(t *testing.T) {
	h := newResumeScenarioHarness(t, "stop-resume")

	p1 := h.runPhaseBlocking(t, 2, "org/apache/commons/1.0")
	require.GreaterOrEqual(t, len(p1), 1)
	// 主动停下：标 finished（区别于 interrupted）
	h.state.MarkFinishedStatus()
	require.NoError(t, h.state.Flush())

	// phase2 resume：从 finished 状态继续
	p2 := h.runPhaseToCompletion()

	all := append(p1, p2...)
	unique := map[string]bool{}
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "finished 后 resume 应覆盖全部: %v", all)

	seen := map[string]int{}
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s 扫了 %d 次", p, c)
	}
}

// 场景 3：并发中断点——多个 worker 同时在飞时 cancel，in-flight 不丢。
// 用 Concurrency=2 让 2 个 download worker 并发；用 selectiveBlockDownloader 阻塞
// commons/1.0 让其在 cancel 时确定性 in-flight，避免「cancel 前 worker 多扫」竞态。
func TestResumeScenario_ConcurrentCancelPoint(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        2, // 2 并发 worker
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "conc.json")
	st := state.NewScanStateWithConfig("conc", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	// phase1：扫 1 个后 cancel；selectiveBlockDownloader 阻塞 commons/1.0 确保它在飞。
	dl1 := &selectiveBlockDownloader{
		mockDownloader: mockDownloader{},
		blockPath:      "org/apache/commons/1.0",
		blocked:        make(chan struct{}),
	}
	scan1 := NewScanner(cfg,
		WithDownloader(dl1), WithDetector(&mockDetector{}),
		WithState(st), WithDiscoveryCacher(st),
		WithFailedRetryer(st), WithCursorSaver(st),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	var p1 []string
	var mu sync.Mutex
	var done = make(chan struct{})
	scan1.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			mu.Lock()
			p1 = append(p1, r.Artifact.Path())
			mu.Unlock()
			st.MarkCompleted(r.Artifact.Path())
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { <-done; cancel1() }()
	go func() { time.Sleep(3 * time.Second); cancel1() }()
	scan1.Run(ctx1)
	require.NoError(t, st.Flush())

	// phase2：resume 到完成
	scan2 := NewScanner(cfg,
		WithDownloader(&mockDownloader{}), WithDetector(&mockDetector{}),
		WithState(st), WithDiscoveryCacher(st),
		WithFailedRetryer(st), WithCursorSaver(st),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	var p2 []string
	var mu2 sync.Mutex
	scan2.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			mu2.Lock()
			p2 = append(p2, r.Artifact.Path())
			mu2.Unlock()
			st.MarkCompleted(r.Artifact.Path())
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_, err := scan2.Run(ctx2)
	require.NoError(t, err)

	all := append(p1, p2...)
	unique := map[string]bool{}
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "并发 cancel 后 resume 应覆盖全部: %v", all)
}

// 场景 4：resume 时 in-flight 集合残留（模拟崩溃后未清理）能被 revisit 重扫。
// 直接预置一个 in-flight 路径到 state，验证 phase2 revisit 它。
func TestResumeScenario_StaleInFlightRevisited(t *testing.T) {
	h := newResumeScenarioHarness(t, "stale-inflight")
	// 预置一个「上次崩溃遗留」的 in-flight 路径
	// MarkInFlight 不返回 error（无返回值），直接调用。
	h.state.MarkInFlight("com/example/lib/1.0")
	require.NoError(t, h.state.Flush())

	// phase2 resume：revisit 应重扫 com/example/lib/1.0（in-flight → completed）
	p := h.runPhaseToCompletion()
	assert.Contains(t, p, "com/example/lib/1.0", "遗留 in-flight 应被 revisit 重扫")

	unique := map[string]bool{}
	for _, x := range p {
		unique[x] = true
	}
	assert.Equal(t, 4, len(unique), "应覆盖全部 4 artifact: %v", p)
}
```

- [ ] **Step 2: 验证 scanner resume 场景测试**

Run: `go test -race -count=3 -run "TestResumeScenario" -v ./internal/scanner/`

Expected:
  - Exit code: 0
  - Output contains: "--- PASS" for 4 tests
  - 无 FAIL、无 DATA RACE（count=3）

- [ ] **Step 3: 验证 scanner 包覆盖率仍 100%**

Run: `go test -count=1 -coverprofile=/tmp/cov_sc3.out -covermode=atomic ./internal/scanner/ && go tool cover -func=/tmp/cov_sc3.out | grep -vE '100.0%|^total:'`

Expected:
  - Exit code: 0
  - Output contains: "coverage: 100.0% of statements"
  - grep 输出为空

- [ ] **Step 4: 提交**

Run: `git add internal/scanner/resume_scenario_test.go && git commit -m "test(scanner): add multi-phase/concurrent/stale-inflight resume scenarios"`

---

### Task 5: cmd 层 resume e2e 测试 + 全仓库回归

**Depends on:** Task 1, Task 2, Task 3, Task 4
**Files:**
- Create: `cmd/resume_e2e_test.go`

**背景：** cmd 层 resume 的 flag 解析、config mismatch 拒绝、signal flush、ScanFinished 上报当前未覆盖。补 e2e：runScan 两次（首次中断、resume 完成）+ config mismatch 报错 + MaxFileSize mismatch 报错。

- [ ] **Step 1: 创建 resume_e2e_test.go — resume config-mismatch/状态/corrupt-recovery e2e**

```go
// cmd/resume_e2e_test.go
package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这些测试覆盖 cmd 层 runScan 在 resume 路径上会触发的 state 行为：
// LoadScanState（含损坏回退 .bak）、ValidateConfig（含 MaxFileSize 校验）、
// ScanStatus 识别（completed/finished）。直接测 state 而非调 runScan，因为
// runScan 强依赖 cobra flag 绑定与包级 cfg；既有 cmd 测试已覆盖 runScan 端到端，
// 此处聚焦 resume 决策点的边界条件。

// 场景 1：config mismatch（RepoURL 不同）→ ValidateConfig 返回 error。
// 对应 cmd runScan 中 resume 时 LoadScanState + ValidateConfig 的拒绝路径。
func TestResume_ConfigMismatch_RepoURL(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "mismatch.json")

	// 先写一份 state，RepoURL=A
	s := state.NewScanStateWithConfig("mismatch", "https://A.com", "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "50MB"})
	require.NoError(t, s.Flush())

	// resume 时用 RepoURL=B → ValidateConfig 应拒绝
	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err)
	snap := state.ConfigSnapshot{RepoURL: "https://B.com", RulesLevel: "core", MaxFileSize: "50MB"}
	err = loaded.ValidateConfig(snap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RepoURL")
}

// 场景 2：MaxFileSize mismatch → ValidateConfig 拒绝。
func TestResume_ConfigMismatch_MaxFileSize(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "mf-mismatch.json")
	s := state.NewScanStateWithConfig("mf", "https://A.com", "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "50MB"})
	require.NoError(t, s.Flush())

	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err)
	err = loaded.ValidateConfig(state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "100MB"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxFileSize")
}

// 场景 3：resume 时 state.GetStatus()==completed → cmd 应提示用 --rediscover。
// 验证 completed 状态的 state 加载后 GetStatus 返回 completed。
func TestResume_CompletedStateRecognized(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "completed.json")
	s := state.NewScanStateWithConfig("done", "https://A.com", "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "50MB"})
	s.MarkCompletedStatus()
	require.NoError(t, s.Flush())

	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, state.ScanCompleted, loaded.GetStatus())
}

// 场景 4：ScanFinished 状态的 state 加载后可被识别（cmd 据此 log「上次主动停止」）。
func TestResume_FinishedStateRecognized(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "finished.json")
	s := state.NewScanStateWithConfig("fin", "https://A.com", "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "50MB"})
	s.MarkFinishedStatus()
	require.NoError(t, s.Flush())

	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, state.ScanFinished, loaded.GetStatus())
}

// 场景 5：损坏 state 文件 + .bak 恢复在 cmd 加载路径可用。
func TestResume_CorruptStateRecoversFromBak(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "corrupt.json")
	s := state.NewScanStateWithConfig("corrupt", "https://A.com", "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "50MB"})
	s.MarkCompleted("com/a/1.0")
	require.NoError(t, s.Flush())
	s.MarkCompleted("com/a/2.0")
	require.NoError(t, s.Flush()) // .bak 含 1.0

	// 损坏主文件
	require.NoError(t, os.WriteFile(stateFile, []byte("{bad"), 0644))

	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err, "cmd 加载损坏 state 应从 .bak 恢复")
	assert.True(t, loaded.IsCompleted("com/a/1.0"))
}
```

- [ ] **Step 2: 验证 cmd resume e2e 测试**

Run: `go test -count=1 -run "TestResume_" -v ./cmd/`

Expected:
  - Exit code: 0
  - Output contains: "--- PASS" for 5 tests
  - 无 FAIL

- [ ] **Step 3: 验证全仓库语句覆盖仍 100%**

Run: `go test -count=1 -coverprofile=/tmp/cov_final.out -covermode=atomic ./... && go tool cover -func=/tmp/cov_final.out | tail -1`

Expected:
  - Exit code: 0
  - Output contains: "total:" and "100.0%"
  - 所有包 coverage: 100.0%

- [ ] **Step 4: 验证全仓库 race + count=3 稳定性**

Run: `go test -race -count=3 ./...`

Expected:
  - Exit code: 0
  - Output contains: "ok" for all 9 packages
  - 无 "FAIL"、无 "WARNING: DATA RACE"

- [ ] **Step 5: 提交**

Run: `git add cmd/resume_e2e_test.go && git commit -m "test(cmd): add resume config-mismatch/corrupt-recovery/finished-state e2e tests"`

---

## 验证总结（全部 Task 完成后）

```bash
# 1. 语句覆盖 100%
go test -count=1 -coverprofile=/tmp/final.out -covermode=atomic ./...
go tool cover -func=/tmp/final.out | grep -vE '100.0%'  # 应为空

# 2. race + count=3 全绿
go test -race -count=3 ./...

# 3. resume 场景测试专项
go test -race -count=3 -run "TestResumeScenario|TestRunScan_Resume|TestRunScan_Corrupt|TestFlush|TestLoadScanState|TestScanStatus_Finished|TestGetResumeEstimate|TestValidateConfig" ./...
```

**完成标准：** 4 个能力增强（MaxFileSize 校验 / flush fsync+.bak / 损坏恢复 / ScanFinished+estimate）落地 + 全场景 resume 测试（多 phase / 并发 cancel / stale in-flight / config mismatch / 损坏恢复 / finished 状态）全 PASS + 语句覆盖 100% 保持 + race count=3 全绿。
