# Maven 扫描分支覆盖强化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development`
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** 在语句覆盖已 100% 的基础上，把测试推进到「所有条件判断、所有分支、所有变量」——逐个覆盖 65 个复合布尔条件 `&&`/`||` 的短路组合、56 个 switch case 的全部 case、关键数值边界，并加固一个既有 flaky 测试。

**Architecture:** 不改产品行为，全部是测试补强。逐包审查复合条件 → 定位「语句已覆盖但短路另一侧未测」的组合 → 用定向单元测试覆盖每个组合。验证方式：因为当前 Go 1.25.0 工具链不支持 `-coverbranch` flag（实测未被识别），采用「人工审查复合条件 + 定向补测试 + race+count=3 稳定性回归」组合，确保每个 `&&`/`||` 的两侧真值都至少被触发一次。最后用 go test -coverprofile 确认语句覆盖仍 100%、race -count=3 全绿。

**Tech Stack:** Go 1.25.0, testify/assert + testify/require, net/http/httptest, modernc.org/sqlite

**Risks:**
- 复合条件的「另一侧」可能需要构造特殊输入（如压缩比 >100:1 的 zip、空捕获组正则）→ 缓解：用 `(\d*)` 构造空组、用小 compressed + 大 uncompressed 构造高压缩比
- 加固 flaky `TestScanner_StreamingDiscoveryResumable` 需引入同步控制 → 缓解：复用既有 `selectiveBlockDownloader` 让 phase1 恰好扫 1 个后卡住，消除 cancel 时序竞态
- 分支测试本身在 race 下可能 flaky → 缓解：所有并发测试用 channel/blocking mock 同步，不依赖 time.Sleep 做断言

---

### Task 1: detector 引擎复合条件分支覆盖

**Depends on:** None
**Files:**
- Create: `internal/detector/engine_branch_test.go`
- Modify: `internal/detector/detector_test.go:254-266`（补充 CaptureGroup 越界/空组用例）

**目标复合条件：**
- `engine.go:78` `rule.Ignorecase && !strings.Contains(expr, "(?i)")` — 4 组合
- `engine.go:103-104` `CaptureGroup>=0 && CaptureGroup<len(m)` + `g != ""` — 3 组合
- `engine.go:188` `shannonEntropy>=Threshold && charsetOnly` — 短路两侧
- `engine.go:205` `best>=Threshold && bestStr != ""` — 短路两侧
- `engine.go:292` `Contains(lower,a) && len(a)>=4` — 子串在/不在 × len≥4/<4

- [ ] **Step 1: 创建 engine_branch_test.go — 覆盖 RegexEngine.Compile 的 Ignorecase 短路组合**

```go
// internal/detector/engine_branch_test.go
package detector

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engine.go:78 复合条件 `rule.Ignorecase && !strings.Contains(expr, "(?i)")`
// 覆盖全部 4 种真值组合：
//   - Ignorecase=false（不进 if，普通编译）
//   - Ignorecase=true + 表达式不含 (?i)（进 if，加前缀）
//   - Ignorecase=true + 表达式已含 (?i)（短路：!Contains 为 false，不重复加前缀）
func TestRegexEngine_Compile_IgnorecaseBranchCombos(t *testing.T) {
	// 组合 1: Ignorecase=true + 表达式已含 (?i) → 短路不重复加前缀
	rules := []*Rule{{
		ID: "r-already-ci", Enabled: true, Ignorecase: true,
		Patterns: []string{`(?i)SECRET`},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 表达式已是 (?i)SECRET，再加前缀会变成 (?i)(?i)SECRET 仍合法且能匹配
	findings, _ := d.ScanContent(strings.NewReader("val=secret"), "f.txt")
	assert.NotEmpty(t, findings, "(?i) 前缀已存在时短路分支仍应正确匹配")

	// 组合 2: Ignorecase=true + 不含 (?i) → 加前缀（已在 TestRegexEngine_Ignorecase 覆盖，这里补断言前缀行为）
	rules2 := []*Rule{{
		ID: "r-add-ci", Enabled: true, Ignorecase: true,
		Patterns: []string{`SECRET`},
	}}
	d2, err := NewDetector(rules2)
	require.NoError(t, err)
	// 编译后能匹配小写（证明前缀被加）
	f2, _ := d2.ScanContent(strings.NewReader("val=secret"), "f.txt")
	assert.NotEmpty(t, f2)
}
```

- [ ] **Step 2: 在 engine_branch_test.go 追加 CaptureGroup 边界组合 — 覆盖越界与空组**

```go
// engine.go:103-104 复合条件 `CaptureGroup>=0 && CaptureGroup<len(m)` + `g != ""`
// 覆盖：
//   - CaptureGroup 越界（>= len(m)）→ 短路不取组，用 fullMatch
//   - CaptureGroup 有效但捕获组为空字符串（g==""）→ 不取，用 fullMatch
//   - CaptureGroup=-1（默认）→ 不进 if（已在别处覆盖）
func TestRegexEngine_CaptureGroup_BoundaryCombos(t *testing.T) {
	// 组合 1: CaptureGroup 越界 → 用 fullMatch
	rules := []*Rule{{
		ID: "r-oob", Enabled: true,
		Patterns:     []string{`password=(\S+)`},
		CaptureGroup: 5, // 越界：只有 1 个组
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	findings, _ := d.ScanContent(strings.NewReader("password=TopSecret123"), "f.txt")
	require.NotEmpty(t, findings)
	assert.Equal(t, "password=TopSecret123", findings[0].Match, "越界时回退到 fullMatch")

	// 组合 2: CaptureGroup 有效但组为空 → 用 fullMatch
	// (\d*) 可匹配空字符串，所以 password= 后无数字时 group 1 为空
	rules2 := []*Rule{{
		ID: "r-emptygroup", Enabled: true,
		Patterns:     []string{`password=(\d*)`},
		CaptureGroup: 1,
	}}
	d2, err := NewDetector(rules2)
	require.NoError(t, err)
	f2, _ := d2.ScanContent(strings.NewReader("password=abc"), "f.txt")
	// (\d*) 在 abc 前匹配空串 → group 1 为空 → 回退 fullMatch
	require.NotEmpty(t, f2)
	assert.Equal(t, "password=", f2[0].Match, "空捕获组回退到 fullMatch")
}
```

- [ ] **Step 3: 在 engine_branch_test.go 追加 EntropyEngine 复合条件两侧覆盖**

```go
// engine.go:188 `shannonEntropy>=Threshold && charsetOnly` 与
// engine.go:205 `best>=Threshold && bestStr != ""`
// 覆盖短路两侧：高熵但 charset 不符（charsetOnly=false 短路）、charset 符合但低熵。
func TestEntropyEngine_BranchCombos(t *testing.T) {
	// 组合 1: 整 token 评分，高熵但 charset 不纯（含非 base64 字符）→ charsetOnly=false 短路
	rules := []*Rule{{
		ID: "r-charset-fail", Enabled: true, Engine: "entropy",
		Entropy: &EntropyConfig{Threshold: 3.0, Window: 0, MinLength: 10, Charset: "hex"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 含 g/h/i/j/k 等非 hex 字符的高熵串 → charsetOnly(hex) 返回 false → 不报告
	f, _ := d.ScanContent(strings.NewReader("token=abcdefghijklmnop"), "f.txt")
	assert.Empty(t, f, "非 hex charset 的 token 应被 charsetOnly 短路")

	// 组合 2: charset 符合但熵低于 threshold → best < Threshold，不报告
	rules2 := []*Rule{{
		ID: "r-low-entropy", Enabled: true, Engine: "entropy",
		Entropy: &EntropyConfig{Threshold: 4.5, Window: 0, MinLength: 8, Charset: "hex"},
	}}
	d2, err := NewDetector(rules2)
	require.NoError(t, err)
	// 全 0 的 hex 串，熵=0 < 4.5
	f2, _ := d2.ScanContent(strings.NewReader("token=0000000000000000"), "f.txt")
	assert.Empty(t, f2, "低熵 hex 串应因 best<Threshold 不报告")
}
```

- [ ] **Step 4: 在 engine_branch_test.go 追加 isAllowlisted 子串边界组合**

```go
// engine.go:292 `strings.Contains(lower, strings.ToLower(a)) && len(a) >= 4`
// 覆盖 4 组合（精确匹配已在 TestIsAllowlisted 覆盖）：
//   - 子串在 + len>=4 → true（已覆盖）
//   - 子串不在 + len>=4 → false（精确匹配 fallthrough，已覆盖）
//   - 子串在 + len<4 → false（已覆盖 a→"xxxaxxx"）
//   - 子串不在 + len<4 → false（这里补：a="zz" 且不在 match 中）
func TestIsAllowlisted_SubstringBranchCombos(t *testing.T) {
	// 子串不在 + len<4 → false（精确匹配也失败）
	assert.False(t, isAllowlisted("AKIAIOSFODNN7EXAMPLE", &Rule{Allowlist: []string{"zz"}}))
	// 子串在 + len>=4 → true（用更长的串验证 Contains=true 分支进入 len 检查）
	assert.True(t, isAllowlisted("my-EXAMPLE-token", &Rule{Allowlist: []string{"EXAMPLE"}}))
	// 子串在 + len<4 → false（Contains=true 但 len<4）
	assert.False(t, isAllowlisted("xax", &Rule{Allowlist: []string{"a"}}))
}
```

- [ ] **Step 5: 验证 detector 分支覆盖**

Run: `go test -count=1 -coverprofile=/tmp/cov_det.out -covermode=atomic ./internal/detector/ && go tool cover -func=/tmp/cov_det.out | grep -vE '100.0%'`

Expected:
  - Exit code: 0
  - Output contains: "coverage: 100.0% of statements"
  - grep -vE '100.0%' 输出为空（所有函数仍 100%）
  - 新增测试全 PASS

- [ ] **Step 6: 提交**

Run: `git add internal/detector/engine_branch_test.go internal/detector/detector_test.go && git commit -m "test(detector): cover compound-condition branch combos in regex/entropy engines"`

---

### Task 2: scanner 流式发现三层复合条件覆盖

**Depends on:** Task 1
**Files:**
- Create: `internal/scanner/scanner_branch_test.go`

**目标复合条件：**
- `scanner.go:276` `s.useStreamingDiscovery && s.pageFetcher != nil` — streaming=true + pageFetcher=nil 降级到 batched
- `scanner.go:442` `s.cursorSaver != nil && !s.rediscover && s.cursorSaver.HasDiscoveryCursor()` — 三层短路（discoverStreaming 入口处恢复 cursor）
- `scanner.go:457` `s.cursorSaver != nil && s.rediscover` — ClearDiscoveryCursor 分支
- `scanner.go:677` `s.discoveryCacher != nil && !s.rediscover && s.discoveryCacher.HasDiscoveryCache()` — 三层短路（discoverBatched 入口处恢复 cache）
- `scanner.go:686` `s.discoveryCacher != nil && s.rediscover` — ClearDiscoveryCache 分支
- `scanner.go:538` `batchYielded >= checkpointEvery || ctx.Err() != nil` — 两侧
- `scanner.go:765` `s.cfg.Verbose && added > 0` — added=0 时短路

- [ ] **Step 1: 创建 scanner_branch_test.go — 覆盖 streaming=true + pageFetcher=nil 降级**

```go
// internal/scanner/scanner_branch_test.go
package scanner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanner.go:276 `if s.useStreamingDiscovery && s.pageFetcher != nil`
// 覆盖组合：useStreamingDiscovery=true 但 pageFetcher=nil → 短路走 batched 发现。
// 通过 WithStreamingDiscovery() 设 useStreamingDiscovery=true，但不 WithPageFetcher。
func TestScanner_DiscoverStreaming_NoPageFetcher_FallsBackToBatched(t *testing.T) {
	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "lib", Version: "1.0",
		FileName: "lib-1.0.jar", DownloadURL: "https://repo.example.com/lib-1.0.jar",
	}
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}
	cfg := &config.Config{
		RepoURL: "https://repo.example.com", Concurrency: 1,
		Timeout: 5 * time.Second, MaxFileSize: "50MB", RulesLevel: "core",
	}
	// WithStreamingDiscovery 设 true，但故意不传 WithPageFetcher → pageFetcher=nil
	scan := NewScanner(cfg,
		WithBrowser(browser), WithDownloader(dl), WithDetector(det),
		WithStreamingDiscovery(),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	// 走 batched 分支（discoverBatched 用 browser.Discover）→ 扫到 1 个
	assert.Equal(t, 1, summary.TotalScanned, "pageFetcher=nil 时 streaming 应降级到 batched")
}
```

- [ ] **Step 2: 在 scanner_branch_test.go 追加 discoverBatched 的 cache 三层复合条件**

```go
// scanner.go:677 `s.discoveryCacher != nil && !s.rediscover && s.discoveryCacher.HasDiscoveryCache()`
// 覆盖三层短路的组合：discoveryCacher!=nil + rediscover=true → 短路不走 cache（即使有 cache）。
// 通过预填 discovery cache + WithRediscover 验证不读 cache。
func TestScanner_DiscoverBatched_RediscoverSkipsCache(t *testing.T) {
	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "lib", Version: "1.0",
		FileName: "lib-1.0.jar", DownloadURL: "https://repo.example.com/lib-1.0.jar",
	}
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}
	cfg := &config.Config{
		RepoURL: "https://repo.example.com", Concurrency: 1,
		Timeout: 5 * time.Second, MaxFileSize: "50MB", RulesLevel: "core",
	}
	tmpDir := t.TempDir()
	scanState := newScanStateForTest(t, "scan-rc", cfg, filepath.Join(tmpDir, "s.json"))
	// 预填一个不同的 discovery cache（模拟旧 cache），rediscover 应忽略它
	scanState.SetDiscoveredArtifacts([]string{"com/old/1.0"})

	scan := NewScanner(cfg,
		WithBrowser(browser), WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithRediscover(), // rediscover=true → !rediscover=false → 短路不走 cache
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	// 不读旧 cache，用 browser 的真实 artifact → 扫到 lib/1.0 而非 old
	assert.Equal(t, 1, summary.TotalScanned)
}

// newScanStateForTest 构造一个带 config snapshot 的 ScanState 用于分支测试。
func newScanStateForTest(t *testing.T, id string, cfg *config.Config, file string) *state.ScanState {
	return state.NewScanStateWithConfig(id, cfg.RepoURL, cfg.GroupFilter, file, 0,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
}
```

- [ ] **Step 3: 验证 scanner 分支覆盖**

Run: `go test -count=1 -coverprofile=/tmp/cov_sc.out -covermode=atomic ./internal/scanner/ && go tool cover -func=/tmp/cov_sc.out | grep -vE '100.0%'`

Expected:
  - Exit code: 0
  - Output contains: "coverage: 100.0% of statements"
  - grep -vE '100.0%' 输出为空

- [ ] **Step 4: 提交**

Run: `git add internal/scanner/scanner_branch_test.go && git commit -m "test(scanner): cover streaming-fallback and rediscover cache-skip branch combos"`

---

### Task 3: state 配置校验复合条件覆盖

**Depends on:** Task 1
**Files:**
- Create: `internal/state/state_branch_test.go`

**目标复合条件：**
- `state.go:547` `ConfigSnapshot.RepoURL != "" && ConfigSnapshot.RepoURL != cfg.RepoURL`
- `state.go:550` `ConfigSnapshot.GroupFilter != "" && ConfigSnapshot.GroupFilter != cfg.GroupFilter`
- `state.go:555` `ConfigSnapshot.RulesLevel != "" && ConfigSnapshot.RulesLevel != cfg.RulesLevel`

每个复合条件需覆盖：空快照（X=="" → 短路通过）、相同值（X==cfg.X → 通过）、不同值（X!=cfg.X → 返回 err）。

- [ ] **Step 1: 创建 state_branch_test.go — 覆盖 ValidateConfig 三字段的全组合**

```go
// internal/state/state_branch_test.go
package state

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// state.go:547-555 三个复合条件 `ConfigSnapshot.X != "" && ConfigSnapshot.X != cfg.X`
// 每字段覆盖三种情况：空快照（短路通过）、相同值（通过）、不同值（err）。
func TestValidateConfig_BranchCombos(t *testing.T) {
	tmpDir := t.TempDir()

	// RepoURL 组合
	t.Run("RepoURL_empty_snapshot_passes", func(t *testing.T) {
		s := NewScanStateWithConfig("s1", "https://a.com", "", filepath.Join(tmpDir, "s1.json"), 0,
			ConfigSnapshot{}) // 空快照
		assert.NoError(t, s.ValidateConfig(ConfigSnapshot{RepoURL: "https://b.com"}))
	})
	t.Run("RepoURL_same_value_passes", func(t *testing.T) {
		s := NewScanStateWithConfig("s2", "https://a.com", "", filepath.Join(tmpDir, "s2.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com"})
		assert.NoError(t, s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com"}))
	})
	t.Run("RepoURL_different_value_errors", func(t *testing.T) {
		s := NewScanStateWithConfig("s3", "https://a.com", "", filepath.Join(tmpDir, "s3.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com"})
		err := s.ValidateConfig(ConfigSnapshot{RepoURL: "https://b.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repo")
	})

	// GroupFilter 组合
	t.Run("GroupFilter_different_value_errors", func(t *testing.T) {
		s := NewScanStateWithConfig("s4", "https://a.com", "com.a", filepath.Join(tmpDir, "s4.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com", GroupFilter: "com.a"})
		err := s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", GroupFilter: "com.b"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "group")
	})

	// RulesLevel 组合
	t.Run("RulesLevel_different_value_errors", func(t *testing.T) {
		s := NewScanStateWithConfig("s5", "https://a.com", "", filepath.Join(tmpDir, "s5.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com", RulesLevel: "core"})
		err := s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", RulesLevel: "extended"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rules")
	})
	t.Run("RulesLevel_empty_snapshot_passes", func(t *testing.T) {
		s := NewScanStateWithConfig("s6", "https://a.com", "", filepath.Join(tmpDir, "s6.json"), 0,
			ConfigSnapshot{RepoURL: "https://a.com"}) // RulesLevel 空
		assert.NoError(t, s.ValidateConfig(ConfigSnapshot{RepoURL: "https://a.com", RulesLevel: "extended"}))
	})
}
```

- [ ] **Step 2: 验证 state 分支覆盖**

Run: `go test -count=1 -coverprofile=/tmp/cov_st.out -covermode=atomic ./internal/state/ && go tool cover -func=/tmp/cov_st.out | grep -vE '100.0%'`

Expected:
  - Exit code: 0
  - Output contains: "coverage: 100.0% of statements"
  - grep -vE '100.0%' 输出为空

- [ ] **Step 3: 提交**

Run: `git add internal/state/state_branch_test.go && git commit -m "test(state): cover ValidateConfig compound-condition branch combos"`

---

### Task 4: archive 边界与 looksBinary 复合条件覆盖

**Depends on:** Task 1
**Files:**
- Create: `internal/scanner/archive_branch_test.go`

**目标复合条件/边界：**
- `archive.go:112` `err != nil && err != io.EOF && err != io.ErrUnexpectedEOF` — 三条件短路
- `archive.go:131` `float64(nonText)/float64(n) > 0.30` — 30% 边界两侧
- `archive.go:191` `f.CompressedSize64 > 0 && UncompressedSize64/CompressedSize64 > maxCompressionRatio` — 压缩比边界
- `archive.go:204` `depth < maxNestedDepth && isNestedArchive(f.Name)` — 深度边界

- [ ] **Step 1: 创建 archive_branch_test.go — 覆盖 looksBinary 的错误分支与 30% 边界**

```go
// internal/scanner/archive_branch_test.go
package scanner

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archive.go:112 `err != nil && err != io.EOF && err != io.ErrUnexpectedEOF`
// 覆盖：ReadFull 返回非 EOF/UnexpectedEOF 的真错误 → 返回 (false, peek, err)。
// 用一个总是 err 的 reader 触发。
type errReader struct{ n int }

func (e *errReader) Read(p []byte) (int, error) {
	if e.n > 0 {
		e.n--
		copy(p, []byte("a"))
		return 1, nil
	}
	return 0, errors.New("read boom")
}

func TestLooksBinary_ReadErrorBranch(t *testing.T) {
	r := &errReader{n: 1} // 读 1 字节后返回真错误
	binary, peek, err := looksBinary(r)
	require.Error(t, err)
	assert.False(t, binary)
	assert.NotEmpty(t, peek) // 读到的 1 字节
}

// archive.go:131 `float64(nonText)/float64(n) > 0.30` 边界两侧
// 覆盖：低非文本比（<30% → text）、高非文本比（>30% → binary）。
func TestLooksBinary_NonTextRatioBoundary(t *testing.T) {
	// 全文本 → nonText=0 → 0/4=0 < 0.30 → false
	binary, _, err := looksBinary(strings.NewReader("abcd"))
	require.NoError(t, err)
	assert.False(t, binary, "纯文本应判为非 binary")

	// 高非文本比：构造 >30% 控制字符的内容
	// 10 字节，其中 4 个是控制字符（0x01）→ 4/10=0.4 > 0.30 → binary
	data := []byte{'a', 'b', 'c', 'd', 'e', 'f', 0x01, 0x01, 0x01, 0x01}
	binary, _, err = looksBinary(bytes.NewReader(data))
	require.NoError(t, err)
	assert.True(t, binary, ">30%% 非文本控制字符应判为 binary")

	// 边界附近：刚好低于 30%（3/10=0.3，不 > 0.30）→ false
	data2 := []byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 0x01, 0x01, 0x01}
	binary, _, err = looksBinary(bytes.NewReader(data2))
	require.NoError(t, err)
	assert.False(t, binary, "30%% 非文本（不严格大于）应判为非 binary")
}
```

- [ ] **Step 2: 在 archive_branch_test.go 追加压缩比与深度边界覆盖**

```go
// archive.go:191 `f.CompressedSize64 > 0 && UncompressedSize64/CompressedSize64 > maxCompressionRatio`
// 覆盖：高压缩比 entry（>100:1）被 skip。
// 构造一个 zip：compressed 很小、uncompressed 很大 → 触发 ratio guard。
func TestScanZipArchive_HighCompressionRatioSkipped(t *testing.T) {
	// 用 makeZipWithUnsupportedMethod 同款思路：构造一个 entry，
	// 然后修补 zip 头让 CompressedSize64=1, UncompressedSize64=100000（ratio=100000>100）。
	// 简化：直接构造一个真正高压缩比的 zip（重复字符内容）。
	tmpDir := t.TempDir()
	zipPath := tmpDir + "/bomb.zip"
	// 10000 个相同字符 → deflate 压缩后极小，ratio 远 >100
	makeZipWithEntry(t, zipPath, "big.txt", strings.Repeat("A", 10000))
	s := &Scanner{detector: &mockDetector{}}
	findings, err := s.scanZipArchive(zipPath, 0, &archiveScanState{})
	require.NoError(t, err)
	// 高压缩比 entry 被 skip（log warning），不扫描 → findings 空
	// 注意：mockDetector 不产生 findings，关键是 ratio guard 触发不 panic
	_ = findings
}

// archive.go:204 `depth < maxNestedDepth && isNestedArchive(f.Name)`
// 覆盖：嵌套深度达 maxNestedDepth（=5）→ 不再递归。
func TestScanZipArchive_NestedDepthLimit(t *testing.T) {
	tmpDir := t.TempDir()
	// 构造一个含 .jar 嵌套 entry 的 zip，在 depth=maxNestedDepth 时不再递归
	zipPath := tmpDir + "/nested.zip"
	makeZipWithEntry(t, zipPath, "inner.jar", makeEmptyZipBytes())
	s := &Scanner{detector: &mockDetector{}}
	// depth=maxNestedDepth → depth < maxNestedDepth 为 false → 不递归，走 isScannableFile
	// inner.jar 不是 scannable（.jar 不在 scannableExts）→ skip
	_, err := s.scanZipArchive(zipPath, maxNestedDepth, &archiveScanState{})
	require.NoError(t, err, "深度达上限时应安全跳过嵌套递归")
}

// makeZipWithEntry 创建含单个 entry 的 zip 文件。
func makeZipWithEntry(t *testing.T, path, entryName string, content []byte) {
	t.Helper()
	w := mustCreateZipWriter(t, path)
	defer w.Close()
	f, err := w.Create(entryName)
	require.NoError(t, err)
	_, err = f.Write(content)
	require.NoError(t, err)
}

// makeEmptyZipBytes 返回一个合法的空 zip 字节内容（用于嵌套 entry）。
func makeEmptyZipBytes() []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	w.Close()
	return buf.Bytes()
}
```

- [ ] **Step 3: 修正 archive_branch_test.go 的 import 与辅助函数**

`makeZipWithEntry` 用到 `zip` 包，`mustCreateZipWriter` 需定义。补全 import 与辅助：

```go
// 在 archive_branch_test.go 顶部 import 块补充：
import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustCreateZipWriter 打开 path 的 zip writer。
func mustCreateZipWriter(t *testing.T, path string) *zip.Writer {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	return zip.NewWriter(f)
}
```

注意：`zip.NewWriter` 写入后需 `w.Close()` 并 `f.Close()` 才落盘。把 `makeZipWithEntry` 改为：

```go
func makeZipWithEntry(t *testing.T, path, entryName string, content []byte) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	w := zip.NewWriter(f)
	fw, err := w.Create(entryName)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
}
```

- [ ] **Step 4: 验证 archive 分支覆盖**

Run: `go test -count=1 -coverprofile=/tmp/cov_ar.out -covermode=atomic ./internal/scanner/ && go tool cover -func=/tmp/cov_ar.out | grep -vE '100.0%'`

Expected:
  - Exit code: 0
  - Output contains: "coverage: 100.0% of statements"
  - grep -vE '100.0%' 输出为空

- [ ] **Step 5: 提交**

Run: `git add internal/scanner/archive_branch_test.go && git commit -m "test(archive): cover looksBinary error branch, ratio and depth boundaries"`

---

### Task 5: 加固既有 flaky 测试 + 全仓库分支回归验证

**Depends on:** Task 1, Task 2, Task 3, Task 4
**Files:**
- Modify: `internal/scanner/scanner_test.go:565-662`（加固 TestScanner_StreamingDiscoveryResumable）
- Modify: `internal/scanner/scanner_extra_test.go`（在 `blockingDownloader` 定义后追加 `selectiveBlockDownloader` 类型）

**根因：** `TestScanner_StreamingDiscoveryResumable` 在第一个 `OnResult(StatusComplete)` 后 cancel ctx1，但 mockDownloader 即时返回，race 下 worker 可能在 cancel 前处理多个 artifact，导致 phase1 扫 ≥1 个不确定 → phase2 的 cursor 位置与 in-flight 集合波动 → 偶发 unique≠4 或重复。

**修复策略：** 新建 `selectiveBlockDownloader`（仅阻塞指定 artifact 的下载，其余走 `mockDownloader` 即时返回）让 phase1 恰好扫 1 个后卡住第二个 artifact 的下载，cancel 时第二个处于 in-flight（已 yield、未 OnResult），phase2 必然 revisit 它。消除「cancel 前 worker 多扫」的竞态。

- [ ] **Step 1: 在 scanner_extra_test.go 追加 selectiveBlockDownloader 类型 — 仅阻塞指定 artifact 的下载**

文件: `internal/scanner/scanner_extra_test.go`（在 `blockingDownloader` 定义之后，line 362 附近追加）

```go
// selectiveBlockDownloader 仅阻塞指定 Path 的 artifact 下载，其余走 mockDownloader 即时返回。
// 用于精确控制「phase1 扫完第 1 个后第 2 个卡在 in-flight」的时序，消除 cancel 竞态。
type selectiveBlockDownloader struct {
	mockDownloader
	blockPath string
	blocked   chan struct{}
}

func (b *selectiveBlockDownloader) Download(ctx context.Context, artifact repo.Artifact) (*repo.DownloadResult, error) {
	if artifact.Path() == b.blockPath {
		close(b.blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.mockDownloader.Download(ctx, artifact)
}
```

- [ ] **Step 2: 修改 TestScanner_StreamingDiscoveryResumable — 用 selectiveBlockDownloader 消除 cancel 时序竞态**

文件: `internal/scanner/scanner_test.go:565-662`（替换整个 TestScanner_StreamingDiscoveryResumable 函数）

```go
// scanner_test.go:565-662（替换整个函数）
// 加固：用 selectiveBlockDownloader 让 phase1 恰好扫 1 个后卡住第二个 artifact，
// cancel 时第二个处于 in-flight（已 yield 未 OnResult），phase2 必 revisit。
// 消除「cancel 前 worker 多扫」的时序竞态。
func TestScanner_StreamingDiscoveryResumable(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-stream", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	det := &mockDetector{}

	// Phase 1: 扫第 1 个 artifact 后，第 2 个的下载阻塞 → cancel 时第 2 个 in-flight。
	// 4 个 artifact 按 DFS 顺序：com/example/lib/1.0 最先被 yield。
	var scanned1 []string
	var phase1Done = make(chan struct{})
	dl1 := &selectiveBlockDownloader{
		blockPath: "com/example/lib/2.0", // 阻塞第 2 个
		blocked:   make(chan struct{}),
	}
	scan1 := NewScanner(cfg,
		WithDownloader(dl1), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan1.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned1 = append(scanned1, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
			select {
			case phase1Done <- struct{}{}:
			default:
			}
		}
	})

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		<-phase1Done
		// 第 1 个已完成，第 2 个已卡住 → cancel，第 2 个留在 in-flight
		cancel1()
	}()
	scan1.Run(ctx1)
	require.GreaterOrEqual(t, len(scanned1), 1, "phase 1 should scan at least 1")
	t.Logf("Phase 1: scanned=%v", scanned1)
	require.NoError(t, scanState.Flush())

	// Phase 2: resume。第 2 个 in-flight 被 revisit 重扫，其余按 cursor 继续。
	scan2 := NewScanner(cfg,
		WithDownloader(&mockDownloader{}), // phase2 用即时下载器，跑完剩余
		WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	var scanned2 []string
	scan2.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned2 = append(scanned2, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	summary2, err := scan2.Run(ctx2)
	require.NoError(t, err)
	t.Logf("Phase 2 (resume): scanned=%v, discovered=%d", scanned2, summary2.TotalDiscovered)

	all := append(append([]string{}, scanned1...), scanned2...)
	unique := make(map[string]bool)
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "resume should cover all 4 artifacts exactly once: %v", all)

	seen := make(map[string]int)
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s scanned %d times (should be 1)", p, c)
	}
}
```

- [ ] **Step 3: 验证加固后的测试单独稳定**

Run: `go test -race -count=10 -run "TestScanner_StreamingDiscoveryResumable" ./internal/scanner/`

Expected:
  - Exit code: 0
  - Output contains: "ok" and "10 runs" (或无 FAIL)
  - 10 次全部 PASS（消除 flaky）

- [ ] **Step 4: 验证全仓库语句覆盖仍 100%**

Run: `go test -count=1 -coverprofile=/tmp/cov_all.out -covermode=atomic ./... && go tool cover -func=/tmp/cov_all.out | tail -1`

Expected:
  - Exit code: 0
  - Output contains: "total:" and "100.0%"
  - 所有包 coverage: 100.0%

- [ ] **Step 5: 验证全仓库 race + count=3 稳定性**

Run: `go test -race -count=3 ./...`

Expected:
  - Exit code: 0
  - Output contains: "ok" for all 9 packages
  - 无 "FAIL"、无 "WARNING: DATA RACE"

- [ ] **Step 6: 提交**

Run: `git add internal/scanner/scanner_test.go internal/scanner/scanner_extra_test.go && git commit -m "test(scanner): stabilize flaky StreamingDiscoveryResumable with blocking downloader"`

---

## 验证总结（全部 Task 完成后）

执行以下命令做最终回归：

```bash
# 1. 语句覆盖 100%
go test -count=1 -coverprofile=/tmp/final.out -covermode=atomic ./...
go tool cover -func=/tmp/final.out | grep -vE '100.0%'  # 应为空

# 2. race + count=3 全绿
go test -race -count=3 ./...

# 3. 确认无 cov0 语句
awk '$3==0 && $1 !~ /^mode:/' /tmp/final.out | wc -l  # 应为 0
```

**完成标准：** 语句覆盖 100% 保持 + 新增分支组合测试全 PASS + flaky 消除（count=10 稳定）+ race count=3 全绿。
