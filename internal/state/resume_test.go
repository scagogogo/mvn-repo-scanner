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
	require.NoError(t, s.MarkCompleted("com/x/a/1.0"))
	require.NoError(t, s.MarkFailed("com/x/a/2.0", "boom", 1))
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
// 用 checkpointEvery=1000 抑制 MarkCompleted 的自动 flush，让显式 Flush 精确控制
// 落盘时序：首次 Flush 写主=[1.0] 无 .bak；MarkCompleted(2.0) 后第二次 Flush
// 先 ReadFile(主=[1.0]) 写 .bak=[1.0]，再写主=[1.0,2.0]。故 .bak 恒含 [1.0]。
func TestLoadScanState_CorruptFallsBackToBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	s := NewScanStateWithConfig("cb1", "https://a.com", "", path, 1000,
		ConfigSnapshot{RepoURL: "https://a.com", MaxFileSize: "50MB"})
	require.NoError(t, s.MarkCompleted("com/a/1.0"))
	require.NoError(t, s.Flush()) // 主=[1.0]，无 .bak
	require.NoError(t, s.MarkCompleted("com/a/2.0"))
	require.NoError(t, s.Flush()) // .bak=[1.0]，主=[1.0,2.0]

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
