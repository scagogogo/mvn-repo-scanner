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
	s := state.NewScanStateWithConfig("mismatch", "https://A.com", "", stateFile, 0,
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
	s := state.NewScanStateWithConfig("mf", "https://A.com", "", stateFile, 0,
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
	s := state.NewScanStateWithConfig("done", "https://A.com", "", stateFile, 0,
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
	s := state.NewScanStateWithConfig("fin", "https://A.com", "", stateFile, 0,
		state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "50MB"})
	s.MarkFinishedStatus()
	require.NoError(t, s.Flush())

	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, state.ScanFinished, loaded.GetStatus())
}

// 场景 5：损坏 state 文件 + .bak 恢复在 cmd 加载路径可用。
// 用 checkpointEvery=1000 抑制 MarkCompleted 的自动 flush，让显式 Flush 精确控制
// 落盘时序：首次 Flush 写主=[1.0] 无 .bak；MarkCompleted(2.0) 后第二次 Flush
// 先 ReadFile(主=[1.0]) 写 .bak=[1.0]，再写主=[1.0,2.0]。故 .bak 恒含 [1.0]。
func TestResume_CorruptStateRecoversFromBak(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "corrupt.json")
	s := state.NewScanStateWithConfig("corrupt", "https://A.com", "", stateFile, 1000,
		state.ConfigSnapshot{RepoURL: "https://A.com", RulesLevel: "core", MaxFileSize: "50MB"})
	require.NoError(t, s.MarkCompleted("com/a/1.0"))
	require.NoError(t, s.Flush())
	require.NoError(t, s.MarkCompleted("com/a/2.0"))
	require.NoError(t, s.Flush()) // .bak 含 1.0

	// 损坏主文件
	require.NoError(t, os.WriteFile(stateFile, []byte("{bad"), 0644))

	loaded, err := state.LoadScanState(stateFile)
	require.NoError(t, err, "cmd 加载损坏 state 应从 .bak 恢复")
	assert.True(t, loaded.IsCompleted("com/a/1.0"))
}
