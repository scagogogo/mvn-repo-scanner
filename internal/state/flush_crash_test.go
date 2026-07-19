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

	// 第二次 flush 应在 hook 处 panic（MarkCompleted 内部调 flush）。
	// recover 捕获 panic，验证 hook 确实触发后检查主文件未被污染。
	func() {
		defer func() { _ = recover() }()
		s.MarkCompleted("com/a/2.0")
	}()
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
// 用 checkpointEvery=1000 抑制 MarkCompleted 的自动 flush，让显式 Flush 精确控制
// 落盘时序（与 resume_test.go 的 .bak 测试同模式）：首次 Flush 主=[1.0] 无 .bak；
// MarkCompleted(2.0) 后第二次 Flush 先 ReadFile(主=[1.0]) 写 .bak=[1.0]，再写主=[1.0,2.0]。
func TestFlush_NormalPath_StillUpdatesBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("normal1", "https://a.com", "", path, 1000,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})

	require.NoError(t, s.MarkCompleted("com/a/1.0"))
	require.NoError(t, s.Flush()) // 主=[1.0]，无 .bak
	first, _ := os.ReadFile(path)

	require.NoError(t, s.MarkCompleted("com/a/2.0"))
	require.NoError(t, s.Flush()) // .bak=[1.0]，主=[1.0,2.0]

	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, first, bak, "normal flush should still back up previous main to .bak")
}
